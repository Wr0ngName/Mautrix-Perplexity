// Package sidecar provides a client for the Perplexity API sidecar.
package sidecar

import (
	"context"
	"fmt"
	"time"

	"github.com/rs/zerolog"

	"go.mau.fi/mautrix-perplexity/pkg/perplexityapi"
)

// MessageClient implements perplexityapi.MessageClient using the sidecar.
type MessageClient struct {
	client  *Client
	metrics *perplexityapi.Metrics
	log     zerolog.Logger
}

// Ensure MessageClient implements perplexityapi.MessageClient
var _ perplexityapi.MessageClient = (*MessageClient)(nil)

// NewMessageClient creates a new sidecar-backed MessageClient.
func NewMessageClient(baseURL string, timeout time.Duration, log zerolog.Logger) *MessageClient {
	return &MessageClient{
		client:  NewClient(baseURL, timeout, log),
		metrics: perplexityapi.NewMetrics(),
		log:     log.With().Str("client_type", "sidecar").Logger(),
	}
}

// CreateMessageStream sends a message and returns a channel of streaming events.
func (m *MessageClient) CreateMessageStream(ctx context.Context, req *perplexityapi.CreateMessageRequest) (<-chan perplexityapi.StreamEvent, error) {
	events := make(chan perplexityapi.StreamEvent, 10)

	sendEvent := func(event perplexityapi.StreamEvent) bool {
		select {
		case <-ctx.Done():
			return false
		case events <- event:
			return true
		}
	}

	go func() {
		defer close(events)

		if ctx.Err() != nil {
			return
		}

		startTime := time.Now()
		m.metrics.TotalRequests.Add(1)

		// Extract portal ID from context
		portalID := "default"
		if pid, ok := ctx.Value(portalIDKey).(string); ok {
			portalID = pid
		}

		// Extract user info from context
		var userID, apiKey string
		if uid, ok := ctx.Value(userIDKey).(string); ok {
			userID = uid
		}
		if key, ok := ctx.Value(apiKeyContextKey).(string); ok {
			apiKey = key
		}

		// Extract web search options from context
		var webSearchOptions *WebSearchOptions
		if wso, ok := ctx.Value(webSearchOptionsKey).(*WebSearchOptions); ok {
			webSearchOptions = wso
		}

		// Extract conversation mode from context (default: false)
		var conversationMode bool
		if cm, ok := ctx.Value(conversationModeKey).(bool); ok {
			conversationMode = cm
		}

		// Extract message content (text and images) from request
		messageText, messageContent := extractMessageContent(req.Messages)
		if messageText == "" && len(messageContent) == 0 {
			m.metrics.FailedRequests.Add(1)
			sendEvent(perplexityapi.StreamEvent{
				Type: "error",
				Error: &perplexityapi.StreamError{
					Type:    "invalid_request",
					Message: "empty message",
				},
			})
			return
		}

		// Log if images are being sent
		hasImages := len(messageContent) > 0
		if hasImages {
			imageCount := 0
			for _, c := range messageContent {
				if c.Type == "image" {
					imageCount++
				}
			}
			m.log.Debug().Int("image_count", imageCount).Msg("Sending message with images to sidecar")
		}

		// Send message_start event
		if !sendEvent(perplexityapi.StreamEvent{
			Type: "message_start",
			Message: &perplexityapi.CreateMessageResponse{
				ID:    fmt.Sprintf("sidecar_%d", time.Now().UnixNano()),
				Model: req.Model,
				Usage: &perplexityapi.Usage{},
			},
		}) {
			return
		}

		// Call sidecar
		var systemPrompt *string
		if req.System != "" {
			systemPrompt = &req.System
		}
		var model *string
		if req.Model != "" {
			model = &req.Model
		}

		resp, err := m.client.ChatWithContent(ctx, portalID, userID, apiKey, messageText, messageContent, systemPrompt, model, webSearchOptions, conversationMode)
		if err != nil {
			m.metrics.FailedRequests.Add(1)
			if ctx.Err() != nil {
				sendEvent(perplexityapi.StreamEvent{
					Type: "error",
					Error: &perplexityapi.StreamError{
						Type:    "cancelled",
						Message: "request cancelled: " + ctx.Err().Error(),
					},
				})
				return
			}
			sendEvent(perplexityapi.StreamEvent{
				Type: "error",
				Error: &perplexityapi.StreamError{
					Type:    "sidecar_error",
					Message: err.Error(),
				},
			})
			return
		}

		// Use actual model from response
		actualModel := resp.Model
		if actualModel == "" {
			actualModel = req.Model
		}

		// Send content as a single block
		if !sendEvent(perplexityapi.StreamEvent{
			Type: "content_block_delta",
			Delta: &perplexityapi.ContentDelta{
				Type: "text_delta",
				Text: resp.Response,
			},
		}) {
			return
		}

		// Track tokens if available
		if resp.TokensUsed != nil && *resp.TokensUsed > 0 {
			m.metrics.TotalOutputTokens.Add(int64(*resp.TokensUsed))
		}

		// Send message_delta with usage and actual model
		if !sendEvent(perplexityapi.StreamEvent{
			Type:      "message_delta",
			Model:     actualModel,
			SessionID: resp.SessionID,
			Usage: &perplexityapi.Usage{
				OutputTokens: estimateTokens(resp.Response),
			},
		}) {
			return
		}

		// Send citations if available
		if len(resp.SearchResults) > 0 {
			var citations []perplexityapi.SearchResult
			for _, sr := range resp.SearchResults {
				citations = append(citations, perplexityapi.SearchResult{
					Title: sr.Title,
					URL:   sr.URL,
					Date:  sr.Date,
				})
			}
			if !sendEvent(perplexityapi.StreamEvent{
				Type:      "citations",
				Citations: citations,
			}) {
				return
			}
			m.log.Debug().Int("citation_count", len(citations)).Msg("Emitted citations event")
		}

		// Send images if available (requires return_images=true)
		if len(resp.Images) > 0 {
			var images []perplexityapi.ImageResult
			for _, img := range resp.Images {
				images = append(images, perplexityapi.ImageResult{
					URL:       img.URL,
					OriginURL: img.OriginURL,
					Height:    img.Height,
					Width:     img.Width,
				})
			}
			if !sendEvent(perplexityapi.StreamEvent{
				Type:   "images",
				Images: images,
			}) {
				return
			}
			m.log.Debug().Int("image_count", len(images)).Msg("Emitted images event")
		}

		// Send message_stop
		sendEvent(perplexityapi.StreamEvent{
			Type: "message_stop",
		})

		outputTokens := estimateTokens(resp.Response)
		m.metrics.RecordRequest(actualModel, time.Since(startTime), 0, outputTokens)
	}()

	return events, nil
}

// CreateMessage sends a message and returns the complete response.
func (m *MessageClient) CreateMessage(ctx context.Context, req *perplexityapi.CreateMessageRequest) (*perplexityapi.CreateMessageResponse, error) {
	startTime := time.Now()
	m.metrics.TotalRequests.Add(1)

	// Extract portal ID from context
	portalID := "default"
	if pid, ok := ctx.Value(portalIDKey).(string); ok {
		portalID = pid
	}

	// Extract user info from context
	var userID, apiKey string
	if uid, ok := ctx.Value(userIDKey).(string); ok {
		userID = uid
	}
	if key, ok := ctx.Value(apiKeyContextKey).(string); ok {
		apiKey = key
	}

	// Extract web search options from context
	var webSearchOptions *WebSearchOptions
	if wso, ok := ctx.Value(webSearchOptionsKey).(*WebSearchOptions); ok {
		webSearchOptions = wso
	}

	// Extract conversation mode from context (default: false)
	var conversationMode bool
	if cm, ok := ctx.Value(conversationModeKey).(bool); ok {
		conversationMode = cm
	}

	// Extract message content (text and images)
	messageText, messageContent := extractMessageContent(req.Messages)
	if messageText == "" && len(messageContent) == 0 {
		m.metrics.FailedRequests.Add(1)
		return nil, fmt.Errorf("empty message")
	}

	// Call sidecar
	var systemPrompt *string
	if req.System != "" {
		systemPrompt = &req.System
	}
	var model *string
	if req.Model != "" {
		model = &req.Model
	}

	resp, err := m.client.ChatWithContent(ctx, portalID, userID, apiKey, messageText, messageContent, systemPrompt, model, webSearchOptions, conversationMode)
	if err != nil {
		m.metrics.FailedRequests.Add(1)
		return nil, err
	}

	// Use actual model from response
	actualModel := resp.Model
	if actualModel == "" {
		actualModel = req.Model
	}

	outputTokens := estimateTokens(resp.Response)
	m.metrics.RecordRequest(actualModel, time.Since(startTime), 0, outputTokens)

	// Convert search results
	var searchResults []perplexityapi.SearchResult
	for _, sr := range resp.SearchResults {
		searchResults = append(searchResults, perplexityapi.SearchResult{
			Title: sr.Title,
			URL:   sr.URL,
			Date:  sr.Date,
		})
	}

	return &perplexityapi.CreateMessageResponse{
		ID:            resp.SessionID,
		Type:          "message",
		Role:          "assistant",
		Model:         actualModel,
		Content:       []perplexityapi.Content{{Type: "text", Text: resp.Response}},
		Usage:         &perplexityapi.Usage{OutputTokens: outputTokens},
		StopReason:    "end_turn",
		SearchResults: searchResults,
	}, nil
}

// Validate checks if the sidecar is healthy.
func (m *MessageClient) Validate(ctx context.Context) error {
	health, err := m.client.Health(ctx)
	if err != nil {
		return fmt.Errorf("sidecar unavailable: %w", err)
	}
	if health.Status != "healthy" {
		return fmt.Errorf("sidecar not healthy: %s", health.Status)
	}
	m.log.Info().Int("sessions", health.Sessions).Msg("Sidecar is healthy")
	return nil
}

// GetHealth returns the sidecar health status.
func (m *MessageClient) GetHealth(ctx context.Context) (*HealthResponse, error) {
	return m.client.Health(ctx)
}

// TestAuth tests user API key by making a minimal Perplexity API call via the sidecar.
func (m *MessageClient) TestAuth(ctx context.Context, userID, apiKey string) error {
	resp, err := m.client.TestAuth(ctx, userID, apiKey)
	if err != nil {
		return fmt.Errorf("failed to test credentials: %w", err)
	}
	if !resp.Success {
		return fmt.Errorf("%s", resp.Message)
	}
	return nil
}

// GetMetrics returns the metrics collector.
func (m *MessageClient) GetMetrics() *perplexityapi.Metrics {
	return m.metrics
}

// GetClientType returns the client type identifier.
func (m *MessageClient) GetClientType() string {
	return "sidecar"
}

// ClearSession clears the conversation history for a portal.
func (m *MessageClient) ClearSession(ctx context.Context, portalID string) error {
	return m.client.DeleteSession(ctx, portalID)
}

// GetSessionStats gets statistics about a session.
func (m *MessageClient) GetSessionStats(ctx context.Context, portalID string) (*SessionStats, error) {
	return m.client.GetSession(ctx, portalID)
}

// Context keys for portal ID, user credentials, web search options, and conversation mode
type contextKey string

const (
	portalIDKey          contextKey = "portal_id"
	userIDKey            contextKey = "user_id"
	apiKeyContextKey     contextKey = "api_key"
	webSearchOptionsKey  contextKey = "web_search_options"
	conversationModeKey  contextKey = "conversation_mode"
)

// WithPortalID returns a context with the portal ID set.
func WithPortalID(ctx context.Context, portalID string) context.Context {
	return context.WithValue(ctx, portalIDKey, portalID)
}

// WithUserCredentials returns a context with user ID and API key set.
func WithUserCredentials(ctx context.Context, userID, apiKey string) context.Context {
	ctx = context.WithValue(ctx, userIDKey, userID)
	ctx = context.WithValue(ctx, apiKeyContextKey, apiKey)
	return ctx
}

// WithWebSearchOptions returns a context with web search options set.
func WithWebSearchOptions(ctx context.Context, options *WebSearchOptions) context.Context {
	return context.WithValue(ctx, webSearchOptionsKey, options)
}

// WithConversationMode returns a context with conversation mode enabled/disabled.
func WithConversationMode(ctx context.Context, enabled bool) context.Context {
	return context.WithValue(ctx, conversationModeKey, enabled)
}

// extractMessageContent extracts text and structured content from the last user message.
func extractMessageContent(messages []perplexityapi.Message) (text string, content []ContentBlock) {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "user" {
			var textParts []string
			var hasImages bool

			for _, c := range messages[i].Content {
				switch c.Type {
				case "text":
					if c.Text != "" {
						textParts = append(textParts, c.Text)
						content = append(content, ContentBlock{
							Type: "text",
							Text: c.Text,
						})
					}
				case "image":
					if c.Source != nil {
						hasImages = true
						content = append(content, ContentBlock{
							Type: "image",
							Source: &ImageSource{
								Type:      c.Source.Type,
								MediaType: c.Source.MediaType,
								Data:      c.Source.Data,
							},
						})
					}
				}
			}

			// Combine text parts
			if len(textParts) > 0 {
				text = textParts[0]
				for i := 1; i < len(textParts); i++ {
					text += "\n" + textParts[i]
				}
			}

			// Only return content if there are images
			if !hasImages {
				content = nil
			}

			return text, content
		}
	}
	return "", nil
}

// estimateTokens provides a rough estimate of token count.
func estimateTokens(text string) int {
	return len(text) / 4
}
