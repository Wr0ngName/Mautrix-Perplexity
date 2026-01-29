// Package sidecar provides a client for the Claude Agent SDK sidecar.
package sidecar

import (
	"context"
	"fmt"
	"time"

	"github.com/rs/zerolog"

	"go.mau.fi/mautrix-claude/pkg/claudeapi"
)

// MessageClient implements claudeapi.MessageClient using the sidecar.
// This allows using Pro/Max subscriptions via the Agent SDK instead of API credits.
type MessageClient struct {
	client  *Client
	metrics *claudeapi.Metrics
	log     zerolog.Logger
}

// Ensure MessageClient implements claudeapi.MessageClient
var _ claudeapi.MessageClient = (*MessageClient)(nil)

// NewMessageClient creates a new sidecar-backed MessageClient.
func NewMessageClient(baseURL string, timeout time.Duration, log zerolog.Logger) *MessageClient {
	return &MessageClient{
		client:  NewClient(baseURL, timeout, log),
		metrics: claudeapi.NewMetrics(),
		log:     log.With().Str("client_type", "sidecar").Logger(),
	}
}

// CreateMessageStream sends a message and returns a channel of streaming events.
// Note: The sidecar currently returns complete responses, so we simulate streaming
// by sending the complete response as a single event.
func (m *MessageClient) CreateMessageStream(ctx context.Context, req *claudeapi.CreateMessageRequest) (<-chan claudeapi.StreamEvent, error) {
	events := make(chan claudeapi.StreamEvent, 10)

	// Helper to send event with context check
	sendEvent := func(event claudeapi.StreamEvent) bool {
		select {
		case <-ctx.Done():
			return false
		case events <- event:
			return true
		}
	}

	go func() {
		defer close(events)

		// Check if context is already cancelled
		if ctx.Err() != nil {
			return
		}

		startTime := time.Now()
		m.metrics.TotalRequests.Add(1)

		// Extract portal ID from context or generate one
		portalID := "default"
		if pid, ok := ctx.Value(portalIDKey).(string); ok {
			portalID = pid
		}

		// Extract user credentials from context
		var userID, credentialsJSON string
		if uid, ok := ctx.Value(userIDKey).(string); ok {
			userID = uid
		}
		if creds, ok := ctx.Value(credentialsJSONKey).(string); ok {
			credentialsJSON = creds
		}

		// Extract session ID for resume (stored in bridge DB)
		var sessionID string
		if sid, ok := ctx.Value(sessionIDKey).(string); ok {
			sessionID = sid
		}

		// Extract message content (text and images) from request
		messageText, messageContent := extractMessageContent(req.Messages)
		if messageText == "" && len(messageContent) == 0 {
			m.metrics.FailedRequests.Add(1)
			sendEvent(claudeapi.StreamEvent{
				Type: "error",
				Error: &claudeapi.StreamError{
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
		if !sendEvent(claudeapi.StreamEvent{
			Type: "message_start",
			Message: &claudeapi.CreateMessageResponse{
				ID:    fmt.Sprintf("sidecar_%d", time.Now().UnixNano()),
				Model: req.Model,
				Usage: &claudeapi.Usage{},
			},
		}) {
			return // Context cancelled
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

		// Use ChatWithContent to support images
		resp, err := m.client.ChatWithContent(ctx, portalID, userID, credentialsJSON, messageText, messageContent, sessionID, systemPrompt, model)
		if err != nil {
			m.metrics.FailedRequests.Add(1)
			// Check if it was a context cancellation
			if ctx.Err() != nil {
				sendEvent(claudeapi.StreamEvent{
					Type: "error",
					Error: &claudeapi.StreamError{
						Type:    "cancelled",
						Message: "request cancelled: " + ctx.Err().Error(),
					},
				})
				return
			}
			sendEvent(claudeapi.StreamEvent{
				Type: "error",
				Error: &claudeapi.StreamError{
					Type:    "sidecar_error",
					Message: err.Error(),
				},
			})
			return
		}

		// Use actual model from response (sidecar tells us what was used)
		actualModel := resp.Model
		if actualModel == "" {
			actualModel = req.Model // Fallback to request model if response is empty
		}

		// Send content as a single block
		if !sendEvent(claudeapi.StreamEvent{
			Type: "content_block_delta",
			Delta: &claudeapi.ContentDelta{
				Type: "text_delta",
				Text: resp.Response,
			},
		}) {
			return // Context cancelled
		}

		// Track tokens if available from sidecar
		// Note: Sidecar returns combined total, we track as output tokens only
		// since we don't have input/output breakdown from Agent SDK
		if resp.TokensUsed != nil && *resp.TokensUsed > 0 {
			m.metrics.TotalOutputTokens.Add(int64(*resp.TokensUsed))
		}

		// Send message_delta with usage, actual model, and session_id for bridge to store
		if !sendEvent(claudeapi.StreamEvent{
			Type:      "message_delta",
			Model:     actualModel,    // Include actual model for ghost ID resolution
			SessionID: resp.SessionID, // Return session_id for bridge to store in DB
			Usage: &claudeapi.Usage{
				OutputTokens: estimateTokens(resp.Response),
			},
		}) {
			return // Context cancelled
		}

		// Send message_stop
		sendEvent(claudeapi.StreamEvent{
			Type: "message_stop",
		})

		// Record successful request
		outputTokens := estimateTokens(resp.Response)
		m.metrics.RecordRequest(actualModel, time.Since(startTime), 0, outputTokens)
	}()

	return events, nil
}

// CreateMessage sends a message and returns the complete response.
func (m *MessageClient) CreateMessage(ctx context.Context, req *claudeapi.CreateMessageRequest) (*claudeapi.CreateMessageResponse, error) {
	startTime := time.Now()
	m.metrics.TotalRequests.Add(1)

	// Extract portal ID from context
	portalID := "default"
	if pid, ok := ctx.Value(portalIDKey).(string); ok {
		portalID = pid
	}

	// Extract user credentials from context
	var userID, credentialsJSON string
	if uid, ok := ctx.Value(userIDKey).(string); ok {
		userID = uid
	}
	if creds, ok := ctx.Value(credentialsJSONKey).(string); ok {
		credentialsJSON = creds
	}

	// Extract message content (text and images)
	messageText, messageContent := extractMessageContent(req.Messages)
	if messageText == "" && len(messageContent) == 0 {
		m.metrics.FailedRequests.Add(1)
		return nil, fmt.Errorf("empty message")
	}

	// Extract session ID for resume (stored in bridge DB)
	var sessionID string
	if sid, ok := ctx.Value(sessionIDKey).(string); ok {
		sessionID = sid
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

	// Use ChatWithContent to support images
	resp, err := m.client.ChatWithContent(ctx, portalID, userID, credentialsJSON, messageText, messageContent, sessionID, systemPrompt, model)
	if err != nil {
		m.metrics.FailedRequests.Add(1)
		return nil, err
	}

	// Use actual model from response (sidecar tells us what was used)
	actualModel := resp.Model
	if actualModel == "" {
		actualModel = req.Model // Fallback to request model if response is empty
	}

	outputTokens := estimateTokens(resp.Response)
	m.metrics.RecordRequest(actualModel, time.Since(startTime), 0, outputTokens)

	return &claudeapi.CreateMessageResponse{
		ID:      resp.SessionID,
		Type:    "message",
		Role:    "assistant",
		Model:   actualModel,
		Content: []claudeapi.Content{{Type: "text", Text: resp.Response}},
		Usage: &claudeapi.Usage{
			OutputTokens: outputTokens,
		},
		StopReason: "end_turn",
	}, nil
}

// Validate checks if the sidecar is healthy and authenticated.
// Note: This checks GLOBAL authentication. For per-user auth, use GetHealth() instead.
func (m *MessageClient) Validate(ctx context.Context) error {
	health, err := m.client.Health(ctx)
	if err != nil {
		return fmt.Errorf("sidecar unavailable: %w", err)
	}
	if !health.Authenticated {
		msg := "Claude Code not authenticated - bridge admin must configure valid credentials"
		if health.Message != nil && *health.Message != "" {
			msg = *health.Message
		}
		return fmt.Errorf("sidecar not ready: %s", msg)
	}
	m.log.Info().Int("sessions", health.Sessions).Bool("authenticated", health.Authenticated).Msg("Sidecar is healthy")
	return nil
}

// GetHealth returns the sidecar health status without requiring global authentication.
// Use this when you want to check if sidecar is running but will provide per-user credentials.
func (m *MessageClient) GetHealth(ctx context.Context) (*HealthResponse, error) {
	return m.client.Health(ctx)
}

// TestAuth tests user credentials by making a minimal Claude API call via the sidecar.
// Returns nil error if credentials are valid.
func (m *MessageClient) TestAuth(ctx context.Context, userID, credentialsJSON string) error {
	resp, err := m.client.TestAuth(ctx, userID, credentialsJSON)
	if err != nil {
		return fmt.Errorf("failed to test credentials: %w", err)
	}
	if !resp.Success {
		return fmt.Errorf("%s", resp.Message)
	}
	return nil
}

// GetMetrics returns the metrics collector.
func (m *MessageClient) GetMetrics() *claudeapi.Metrics {
	return m.metrics
}

// OAuthStart initiates the OAuth login flow and returns an authorization URL.
func (m *MessageClient) OAuthStart(ctx context.Context, userID string) (*OAuthStartResponse, error) {
	return m.client.OAuthStart(ctx, userID)
}

// OAuthComplete completes the OAuth flow by exchanging the code for credentials.
func (m *MessageClient) OAuthComplete(ctx context.Context, userID, state, code string) (*OAuthCompleteResponse, error) {
	return m.client.OAuthComplete(ctx, userID, state, code)
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

// Context key for portal ID, user credentials, and session ID
type contextKey string

const (
	portalIDKey        contextKey = "portal_id"
	userIDKey          contextKey = "user_id"
	credentialsJSONKey contextKey = "credentials_json"
	sessionIDKey       contextKey = "session_id"
)

// WithPortalID returns a context with the portal ID set.
func WithPortalID(ctx context.Context, portalID string) context.Context {
	return context.WithValue(ctx, portalIDKey, portalID)
}

// WithUserCredentials returns a context with user ID and credentials JSON set.
func WithUserCredentials(ctx context.Context, userID, credentialsJSON string) context.Context {
	ctx = context.WithValue(ctx, userIDKey, userID)
	ctx = context.WithValue(ctx, credentialsJSONKey, credentialsJSON)
	return ctx
}

// WithSessionID returns a context with the Agent SDK session ID set (for resume).
func WithSessionID(ctx context.Context, sessionID string) context.Context {
	return context.WithValue(ctx, sessionIDKey, sessionID)
}

// extractMessageContent extracts text and structured content from the last user message.
// Returns the text content (for backward compatibility) and structured content blocks (for images).
// If there are images, content will be non-nil.
func extractMessageContent(messages []claudeapi.Message) (text string, content []ContentBlock) {
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

			// Only return content if there are images (for backward compatibility)
			// Text-only messages will use the simple Message field
			if !hasImages {
				content = nil
			}

			return text, content
		}
	}
	return "", nil
}

// extractMessageText extracts the text content from the last user message.
// Deprecated: Use extractMessageContent for multimodal support.
func extractMessageText(messages []claudeapi.Message) string {
	text, _ := extractMessageContent(messages)
	return text
}

// estimateTokens provides a rough estimate of token count.
// Assumes ~4 characters per token (rough average for English text).
func estimateTokens(text string) int {
	return len(text) / 4
}
