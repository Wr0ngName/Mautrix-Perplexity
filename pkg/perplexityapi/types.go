// Package perplexityapi provides types for the Perplexity API.
package perplexityapi

import (
	"net/http"
	"strings"
	"time"
)

// Message represents a message in a conversation.
type Message struct {
	Role    string    `json:"role"`    // "user", "assistant", or "system"
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

// WebSearchOptions represents Perplexity-specific web search options.
type WebSearchOptions struct {
	SearchDomainFilter  []string `json:"search_domain_filter,omitempty"`
	SearchRecencyFilter string   `json:"search_recency_filter,omitempty"` // "day", "week", "month", "year"
}

// CreateMessageRequest represents a request to create a message.
type CreateMessageRequest struct {
	Model            string            `json:"model"`
	Messages         []Message         `json:"messages"`
	MaxTokens        int               `json:"max_tokens"`
	Temperature      float64           `json:"temperature,omitempty"`
	System           string            `json:"system,omitempty"`
	Stream           bool              `json:"stream,omitempty"`
	WebSearchOptions *WebSearchOptions `json:"web_search_options,omitempty"`
}

// SearchResult represents a search result from Perplexity.
type SearchResult struct {
	Title string `json:"title,omitempty"`
	URL   string `json:"url,omitempty"`
	Date  string `json:"date,omitempty"`
}

// CreateMessageResponse represents a response from creating a message.
type CreateMessageResponse struct {
	ID            string         `json:"id"`
	Type          string         `json:"type"`
	Role          string         `json:"role"`
	Content       []Content      `json:"content"`
	Model         string         `json:"model"`
	StopReason    string         `json:"stop_reason,omitempty"`
	Usage         *Usage         `json:"usage,omitempty"`
	SearchResults []SearchResult `json:"search_results,omitempty"`
}

// Usage represents token usage information.
type Usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// StreamEvent represents an event in a streaming response.
type StreamEvent struct {
	Type      string                 `json:"type"` // "message_start", "content_block_delta", "message_stop", "error", "citations", etc.
	Index     int                    `json:"index,omitempty"`
	Delta     *ContentDelta          `json:"delta,omitempty"`
	Message   *CreateMessageResponse `json:"message,omitempty"`
	Model     string                 `json:"model,omitempty"`      // Actual model used
	SessionID string                 `json:"session_id,omitempty"` // Session ID from sidecar
	Usage     *Usage                 `json:"usage,omitempty"`
	Error     *StreamError           `json:"error,omitempty"`    // Error details for "error" type events
	Citations []SearchResult         `json:"citations,omitempty"` // Citations from Perplexity search
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

// APIError represents an error from the Perplexity API.
type APIError struct {
	Type       string `json:"type"`
	Message    string `json:"message"`
	StatusCode int    `json:"-"`
	RetryAfter int    `json:"-"`
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

	if apiErr, ok := err.(*APIError); ok {
		return apiErr.StatusCode == http.StatusTooManyRequests
	}

	errStr := err.Error()
	return strings.Contains(errStr, "rate_limit") || strings.Contains(errStr, "429")
}

// IsAuthError checks if an error is an authentication error (401, 403).
func IsAuthError(err error) bool {
	if err == nil {
		return false
	}

	if apiErr, ok := err.(*APIError); ok {
		return apiErr.StatusCode == http.StatusUnauthorized ||
			apiErr.StatusCode == http.StatusForbidden
	}

	errStr := err.Error()
	return strings.Contains(errStr, "authentication") ||
		strings.Contains(errStr, "unauthorized") ||
		strings.Contains(errStr, "invalid") && strings.Contains(errStr, "key")
}

// IsInsufficientCreditsError checks if an error indicates insufficient credits (402).
func IsInsufficientCreditsError(err error) bool {
	if err == nil {
		return false
	}

	if apiErr, ok := err.(*APIError); ok {
		return apiErr.StatusCode == http.StatusPaymentRequired
	}

	errStr := err.Error()
	return strings.Contains(errStr, "insufficient") ||
		strings.Contains(errStr, "credits") ||
		strings.Contains(errStr, "402")
}

// IsOverloadedError checks if an error is an overloaded/server error (5xx).
func IsOverloadedError(err error) bool {
	if err == nil {
		return false
	}

	if apiErr, ok := err.(*APIError); ok {
		return apiErr.StatusCode >= 500
	}

	errStr := err.Error()
	return strings.Contains(errStr, "overloaded") ||
		strings.Contains(errStr, "server_error")
}

// IsInvalidRequestError checks if an error is an invalid request error (400).
func IsInvalidRequestError(err error) bool {
	if err == nil {
		return false
	}

	if apiErr, ok := err.(*APIError); ok {
		return apiErr.StatusCode == http.StatusBadRequest
	}

	errStr := err.Error()
	return strings.Contains(errStr, "invalid_request") ||
		strings.Contains(errStr, "bad request")
}

// GetRetryAfter attempts to extract retry-after duration from an error.
func GetRetryAfter(err error) time.Duration {
	if err == nil {
		return 0
	}

	if apiErr, ok := err.(*APIError); ok && apiErr.RetryAfter > 0 {
		return time.Duration(apiErr.RetryAfter) * time.Second
	}

	if IsRateLimitError(err) {
		return 30 * time.Second
	}

	return 0
}
