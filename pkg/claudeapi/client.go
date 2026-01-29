// Package claudeapi provides a wrapper around the official Anthropic Go SDK.
package claudeapi

import (
	"context"
	"fmt"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/rs/zerolog"
)

// Client wraps the official Anthropic SDK client.
type Client struct {
	sdk     *anthropic.Client
	Log     zerolog.Logger
	Metrics *Metrics
}

// Ensure Client implements MessageClient interface.
var _ MessageClient = (*Client)(nil)

// NewClient creates a new Claude API client using the official SDK.
func NewClient(apiKey string, log zerolog.Logger) *Client {
	sdk := anthropic.NewClient(
		option.WithAPIKey(apiKey),
	)

	return &Client{
		sdk:     &sdk,
		Log:     log,
		Metrics: NewMetrics(),
	}
}

// Validate checks if the API key is valid by making a minimal test request.
func (c *Client) Validate(ctx context.Context) error {
	// Use the Models API to validate the key
	_, err := c.sdk.Models.List(ctx, anthropic.ModelListParams{})
	if err != nil {
		c.Log.Debug().Err(err).Msg("API key validation failed")
		return err
	}
	return nil
}

// CreateMessage creates a new message (non-streaming).
func (c *Client) CreateMessage(ctx context.Context, req *CreateMessageRequest) (*CreateMessageResponse, error) {
	startTime := time.Now()

	// Convert our request to SDK format with optional caching
	sdkParams := anthropic.MessageNewParams{
		Model:     anthropic.Model(req.Model),
		MaxTokens: int64(req.MaxTokens),
		Messages:  convertMessagesToSDKWithCache(req.Messages, req.EnableCaching),
	}

	if req.System != "" {
		if req.EnableCaching {
			// Add cache control to system prompt
			sdkParams.System = []anthropic.TextBlockParam{
				{
					Text: req.System,
					CacheControl: anthropic.CacheControlEphemeralParam{
						Type: "ephemeral",
						TTL:  anthropic.CacheControlEphemeralTTLTTL5m,
					},
				},
			}
		} else {
			sdkParams.System = []anthropic.TextBlockParam{
				{Text: req.System},
			}
		}
	}

	if req.Temperature >= 0 {
		sdkParams.Temperature = anthropic.Float(req.Temperature)
	}

	c.Log.Debug().
		Str("model", req.Model).
		Int("max_tokens", req.MaxTokens).
		Bool("caching", req.EnableCaching).
		Msg("Sending message to Claude API")

	resp, err := c.sdk.Messages.New(ctx, sdkParams)
	if err != nil {
		c.Metrics.RecordError(err)
		return nil, err
	}

	// Record metrics
	duration := time.Since(startTime)
	inputTokens := int(resp.Usage.InputTokens)
	outputTokens := int(resp.Usage.OutputTokens)
	cacheCreationTokens := int(resp.Usage.CacheCreationInputTokens)
	cacheReadTokens := int(resp.Usage.CacheReadInputTokens)
	c.Metrics.RecordRequestWithCache(req.Model, duration, inputTokens, outputTokens, cacheCreationTokens, cacheReadTokens)

	// Convert response to our format
	return convertSDKResponse(resp), nil
}

// CreateMessageStream creates a new message with streaming.
func (c *Client) CreateMessageStream(ctx context.Context, req *CreateMessageRequest) (<-chan StreamEvent, error) {
	// Convert our request to SDK format with optional caching
	sdkParams := anthropic.MessageNewParams{
		Model:     anthropic.Model(req.Model),
		MaxTokens: int64(req.MaxTokens),
		Messages:  convertMessagesToSDKWithCache(req.Messages, req.EnableCaching),
	}

	if req.System != "" {
		if req.EnableCaching {
			// Add cache control to system prompt
			sdkParams.System = []anthropic.TextBlockParam{
				{
					Text: req.System,
					CacheControl: anthropic.CacheControlEphemeralParam{
						Type: "ephemeral",
						TTL:  anthropic.CacheControlEphemeralTTLTTL5m,
					},
				},
			}
		} else {
			sdkParams.System = []anthropic.TextBlockParam{
				{Text: req.System},
			}
		}
	}

	if req.Temperature >= 0 {
		sdkParams.Temperature = anthropic.Float(req.Temperature)
	}

	c.Log.Debug().
		Str("model", req.Model).
		Int("max_tokens", req.MaxTokens).
		Bool("caching", req.EnableCaching).
		Msg("Starting streaming message to Claude API")

	stream := c.sdk.Messages.NewStreaming(ctx, sdkParams)

	// Check if stream was created successfully
	if stream == nil {
		return nil, fmt.Errorf("failed to create message stream")
	}

	// Create output channel with buffer size of 100.
	// Buffer size rationale: Allows for ~100 text delta events to queue without blocking,
	// which covers typical response bursts. Too small risks blocking the SDK's HTTP reader,
	// too large wastes memory. 100 provides good balance for streaming responses.
	eventCh := make(chan StreamEvent, 100)

	// Start goroutine to process stream with proper context cancellation handling
	go func() {
		defer close(eventCh)
		defer stream.Close() // Ensure stream is closed to release resources
		defer func() {
			if r := recover(); r != nil {
				c.Log.Error().Interface("panic", r).Msg("Panic in stream processing goroutine")
			}
		}()
		startTime := time.Now()
		var inputTokens, outputTokens, cacheCreationTokens, cacheReadTokens int

		for stream.Next() {
			// Check for context cancellation to prevent goroutine leak
			select {
			case <-ctx.Done():
				c.Log.Debug().Msg("Stream cancelled by context")
				return
			default:
			}

			event := stream.Current()

			// Convert SDK event to our format
			streamEvent := convertSDKStreamEvent(event)
			if streamEvent == nil {
				// Log dropped events for debugging - these are usually harmless
				// (e.g., ping events, unknown event types) but worth tracking
				c.Log.Trace().Str("event_type", string(event.Type)).Msg("Dropped unhandled stream event")
				continue
			}

			// Track token usage
			if streamEvent.Message != nil && streamEvent.Message.Usage != nil {
				if streamEvent.Message.Usage.InputTokens > 0 {
					inputTokens = streamEvent.Message.Usage.InputTokens
				}
				if streamEvent.Message.Usage.CacheCreationTokens > 0 {
					cacheCreationTokens = streamEvent.Message.Usage.CacheCreationTokens
				}
				if streamEvent.Message.Usage.CacheReadTokens > 0 {
					cacheReadTokens = streamEvent.Message.Usage.CacheReadTokens
				}
			}
			if streamEvent.Usage != nil && streamEvent.Usage.OutputTokens > 0 {
				outputTokens = streamEvent.Usage.OutputTokens
			}

			// Non-blocking send to prevent deadlock if receiver abandons channel
			select {
			case eventCh <- *streamEvent:
			case <-ctx.Done():
				c.Log.Debug().Msg("Stream cancelled while sending event")
				return
			}
		}

		if err := stream.Err(); err != nil {
			// Check if error is due to context cancellation
			if ctx.Err() != nil {
				c.Log.Debug().Msg("Stream ended due to context cancellation")
				return
			}
			c.Log.Error().Err(err).Msg("Stream error")
			c.Metrics.RecordError(err)
			// Non-blocking error send
			select {
			case eventCh <- StreamEvent{
				Type: "error",
				Error: &StreamError{
					Type:    "stream_error",
					Message: err.Error(),
				},
			}:
			case <-ctx.Done():
			}
		} else {
			// Record successful request
			duration := time.Since(startTime)
			c.Log.Debug().
				Str("model", req.Model).
				Dur("duration", duration).
				Int("input_tokens", inputTokens).
				Int("output_tokens", outputTokens).
				Int("cache_creation_tokens", cacheCreationTokens).
				Int("cache_read_tokens", cacheReadTokens).
				Msg("Stream completed successfully, recording metrics")
			c.Metrics.RecordRequestWithCache(req.Model, duration, inputTokens, outputTokens, cacheCreationTokens, cacheReadTokens)
		}
	}()

	return eventCh, nil
}

// GetClientType returns the client type identifier.
func (c *Client) GetClientType() string {
	return ClientTypeAPI
}

// GetMetrics returns the metrics collector.
func (c *Client) GetMetrics() *Metrics {
	return c.Metrics
}

// CompactConversation calls Claude to generate a summary of the conversation for compaction.
// Returns the summary text that can be used to replace the conversation history.
func (c *Client) CompactConversation(ctx context.Context, model string, conversationText string) (string, error) {
	compactionPrompt := `You are being asked to create a concise summary of the conversation so far.
This summary will replace the full conversation history to manage context limits.

Create a summary that:
1. Preserves all important context, decisions, and key information discussed
2. Maintains the essential flow and topics of the conversation
3. Notes any specific user preferences or requirements mentioned
4. Keeps track of any ongoing tasks or open questions
5. Is written from a neutral perspective (not as "I" the assistant)

Format your summary as a clear, structured recap that another instance of Claude
could use to continue the conversation seamlessly. Be thorough but concise.

Respond ONLY with the summary, no preamble or explanation.

Here is the conversation to summarize:

` + conversationText

	resp, err := c.CreateMessage(ctx, &CreateMessageRequest{
		Model:     model,
		MaxTokens: 4096, // Enough for a detailed summary
		Messages: []Message{
			{
				Role: "user",
				Content: []Content{
					{Type: "text", Text: compactionPrompt},
				},
			},
		},
		Temperature: 0.3, // Low temperature for consistent summaries
	})
	if err != nil {
		return "", fmt.Errorf("failed to generate compaction summary: %w", err)
	}

	// Extract summary from response
	for _, content := range resp.Content {
		if content.Type == "text" && content.Text != "" {
			return content.Text, nil
		}
	}

	return "", fmt.Errorf("no summary in compaction response")
}

// convertMessagesToSDK converts our message format to SDK format.
func convertMessagesToSDK(messages []Message) []anthropic.MessageParam {
	return convertMessagesToSDKWithCache(messages, false)
}

// convertMessagesToSDKWithCache converts our message format to SDK format with optional caching.
// When enableCaching is true, cache_control is added to all messages except the last user message,
// since those form the stable prefix that can be cached.
func convertMessagesToSDKWithCache(messages []Message, enableCaching bool) []anthropic.MessageParam {
	result := make([]anthropic.MessageParam, 0, len(messages))

	for i, msg := range messages {
		var blocks []anthropic.ContentBlockParamUnion

		// Enable caching for all messages except the last one (which is the new user message)
		// The last message changes each turn, so it shouldn't be cached
		shouldCache := enableCaching && i < len(messages)-1

		for _, content := range msg.Content {
			switch content.Type {
			case "text":
				if shouldCache {
					// Add cache control to this text block
					blocks = append(blocks, anthropic.ContentBlockParamUnion{
						OfText: &anthropic.TextBlockParam{
							Text: content.Text,
							CacheControl: anthropic.CacheControlEphemeralParam{
								Type: "ephemeral",
								TTL:  anthropic.CacheControlEphemeralTTLTTL5m,
							},
						},
					})
				} else {
					blocks = append(blocks, anthropic.NewTextBlock(content.Text))
				}
			case "image":
				if content.Source != nil {
					blocks = append(blocks, anthropic.NewImageBlockBase64(
						content.Source.MediaType,
						content.Source.Data,
					))
				}
			}
		}

		switch msg.Role {
		case "user":
			result = append(result, anthropic.NewUserMessage(blocks...))
		case "assistant":
			result = append(result, anthropic.NewAssistantMessage(blocks...))
		}
	}

	return result
}

// convertSDKResponse converts SDK response to our format.
func convertSDKResponse(resp *anthropic.Message) *CreateMessageResponse {
	var content []Content
	for _, block := range resp.Content {
		if block.Type == "text" {
			content = append(content, Content{
				Type: "text",
				Text: block.Text,
			})
		}
	}

	return &CreateMessageResponse{
		ID:         resp.ID,
		Type:       string(resp.Type),
		Role:       string(resp.Role),
		Content:    content,
		Model:      string(resp.Model),
		StopReason: string(resp.StopReason),
		Usage: &Usage{
			InputTokens:         int(resp.Usage.InputTokens),
			OutputTokens:        int(resp.Usage.OutputTokens),
			CacheCreationTokens: int(resp.Usage.CacheCreationInputTokens),
			CacheReadTokens:     int(resp.Usage.CacheReadInputTokens),
		},
	}
}

// convertSDKStreamEvent converts SDK stream event to our format.
// Includes defensive nil checks to prevent panics from malformed responses.
func convertSDKStreamEvent(event anthropic.MessageStreamEventUnion) *StreamEvent {
	switch event.Type {
	case "message_start":
		// Defensive nil checks for message_start event
		if event.Message.ID != "" {
			usage := &Usage{}
			if event.Message.Usage.InputTokens > 0 {
				usage.InputTokens = int(event.Message.Usage.InputTokens)
			}
			if event.Message.Usage.CacheCreationInputTokens > 0 {
				usage.CacheCreationTokens = int(event.Message.Usage.CacheCreationInputTokens)
			}
			if event.Message.Usage.CacheReadInputTokens > 0 {
				usage.CacheReadTokens = int(event.Message.Usage.CacheReadInputTokens)
			}
			return &StreamEvent{
				Type: "message_start",
				Message: &CreateMessageResponse{
					ID:    event.Message.ID,
					Model: string(event.Message.Model),
					Usage: usage,
				},
			}
		}
	case "content_block_delta":
		// Defensive check for delta content
		if event.Delta.Type == "text_delta" && event.Delta.Text != "" {
			return &StreamEvent{
				Type: "content_block_delta",
				Delta: &ContentDelta{
					Type: "text_delta",
					Text: event.Delta.Text,
				},
			}
		}
	case "message_delta":
		usage := &Usage{}
		if event.Usage.OutputTokens > 0 {
			usage.OutputTokens = int(event.Usage.OutputTokens)
		}
		return &StreamEvent{
			Type:  "message_delta",
			Usage: usage,
		}
	case "message_stop":
		return &StreamEvent{
			Type: "message_stop",
		}
	case "error":
		return &StreamEvent{
			Type: "error",
			Error: &StreamError{
				Type:    "api_error",
				Message: fmt.Sprintf("%v", event),
			},
		}
	}
	return nil
}
