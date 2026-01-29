// Package claudeapi provides a client for the Claude API.
package claudeapi

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
)

// Message represents a message in a conversation.
type Message struct {
	Role    string    `json:"role"`    // "user" or "assistant"
	Content []Content `json:"content"` // Text, images, etc.
}

// Content represents content within a message.
type Content struct {
	Type   string       `json:"type"`             // "text" or "image"
	Text   string       `json:"text,omitempty"`   // Text content
	Source *ImageSource `json:"source,omitempty"` // Image source
}

// ImageSource represents an image source.
type ImageSource struct {
	Type      string `json:"type"`       // "base64"
	MediaType string `json:"media_type"` // "image/jpeg", "image/png", etc.
	Data      string `json:"data"`       // Base64-encoded image data
}

// CreateMessageRequest represents a request to create a message.
type CreateMessageRequest struct {
	Model       string                 `json:"model"`
	Messages    []Message              `json:"messages"`
	MaxTokens   int                    `json:"max_tokens"`
	Temperature float64                `json:"temperature,omitempty"`
	System      string                 `json:"system,omitempty"`
	Stream      bool                   `json:"stream,omitempty"`
	Metadata    map[string]interface{} `json:"metadata,omitempty"`
	// EnableCaching enables prompt caching for the system prompt and conversation history.
	// This adds cache_control markers to reduce costs on repeated context.
	// Only enable from the 2nd message to avoid 25% cache write overhead on single questions.
	EnableCaching bool `json:"-"`
}

// CreateMessageResponse represents a response from creating a message.
type CreateMessageResponse struct {
	ID           string    `json:"id"`
	Type         string    `json:"type"`
	Role         string    `json:"role"`
	Content      []Content `json:"content"`
	Model        string    `json:"model"`
	StopReason   string    `json:"stop_reason,omitempty"`
	StopSequence string    `json:"stop_sequence,omitempty"`
	Usage        *Usage    `json:"usage,omitempty"`
}

// Usage represents token usage information.
type Usage struct {
	InputTokens            int `json:"input_tokens"`
	OutputTokens           int `json:"output_tokens"`
	CacheCreationTokens    int `json:"cache_creation_input_tokens,omitempty"`
	CacheReadTokens        int `json:"cache_read_input_tokens,omitempty"`
}

// StreamEvent represents an event in a streaming response.
type StreamEvent struct {
	Type      string                 `json:"type"` // "message_start", "content_block_delta", "message_stop", "error", etc.
	Index     int                    `json:"index,omitempty"`
	Delta     *ContentDelta          `json:"delta,omitempty"`
	Message   *CreateMessageResponse `json:"message,omitempty"`
	Model     string                 `json:"model,omitempty"`      // Actual model used (for sidecar responses)
	SessionID string                 `json:"session_id,omitempty"` // Agent SDK session ID (for sidecar - stored in bridge DB)
	Usage     *Usage                 `json:"usage,omitempty"`
	Error     *StreamError           `json:"error,omitempty"` // Error details for "error" type events
}

// StreamError represents an error in a streaming response.
type StreamError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

// ContentDelta represents incremental content in a streaming response.
type ContentDelta struct {
	Type string `json:"type"` // "text_delta"
	Text string `json:"text"`
}

// APIError represents an error from the Claude API.
// This struct is populated from API error responses and includes retry guidance.
type APIError struct {
	// Type is the error type from the API (e.g., "rate_limit_error", "invalid_request_error")
	Type string `json:"type"`
	// Message is the human-readable error message from the API
	Message string `json:"message"`
	// RetryAfter is the suggested wait time in seconds before retrying.
	// This value comes from the HTTP Retry-After header when present (e.g., on 429 responses).
	// Use GetRetryAfter() helper function to extract this from errors.
	// Zero means no retry guidance was provided.
	RetryAfter int `json:"-"`
}

// Error implements the error interface.
func (e *APIError) Error() string {
	return e.Type + ": " + e.Message
}

// IsRateLimitError checks if an error is a rate limit error (429).
func IsRateLimitError(err error) bool {
	if err == nil {
		return false
	}

	var apiErr *anthropic.Error
	if errors.As(err, &apiErr) {
		return apiErr.StatusCode == http.StatusTooManyRequests
	}

	// Fallback to string matching for other error types
	errStr := err.Error()
	return strings.Contains(errStr, "rate_limit") || strings.Contains(errStr, "429")
}

// IsAuthError checks if an error is an authentication error (401, 403).
func IsAuthError(err error) bool {
	if err == nil {
		return false
	}

	var apiErr *anthropic.Error
	if errors.As(err, &apiErr) {
		return apiErr.StatusCode == http.StatusUnauthorized ||
			apiErr.StatusCode == http.StatusForbidden
	}

	// Fallback to string matching for other error types
	errStr := err.Error()
	return strings.Contains(errStr, "authentication") ||
		strings.Contains(errStr, "unauthorized") ||
		strings.Contains(errStr, "invalid_api_key")
}

// IsOverloadedError checks if an error is an overloaded/server error (5xx, 529).
func IsOverloadedError(err error) bool {
	if err == nil {
		return false
	}

	var apiErr *anthropic.Error
	if errors.As(err, &apiErr) {
		return apiErr.StatusCode >= 500 ||
			apiErr.StatusCode == 529 // Overloaded
	}

	// Fallback to string matching for other error types
	errStr := err.Error()
	return strings.Contains(errStr, "overloaded") ||
		strings.Contains(errStr, "server_error")
}

// IsInvalidRequestError checks if an error is an invalid request error (400).
func IsInvalidRequestError(err error) bool {
	if err == nil {
		return false
	}

	var apiErr *anthropic.Error
	if errors.As(err, &apiErr) {
		return apiErr.StatusCode == http.StatusBadRequest
	}

	// Fallback to string matching for other error types
	errStr := err.Error()
	return strings.Contains(errStr, "invalid_request") ||
		strings.Contains(errStr, "bad request")
}

// GetRetryAfter attempts to extract retry-after duration from an error.
// Returns 0 if no retry-after information is available.
func GetRetryAfter(err error) time.Duration {
	if err == nil {
		return 0
	}

	var apiErr *anthropic.Error
	if errors.As(err, &apiErr) {
		// Check for Retry-After header if response is available
		if apiErr.Response != nil {
			if retryAfter := apiErr.Response.Header.Get("Retry-After"); retryAfter != "" {
				// Try to parse as seconds
				if seconds, parseErr := time.ParseDuration(retryAfter + "s"); parseErr == nil {
					return seconds
				}
			}
		}
	}

	// Default retry time for rate limits
	if IsRateLimitError(err) {
		return 30 * time.Second
	}

	return 0
}
