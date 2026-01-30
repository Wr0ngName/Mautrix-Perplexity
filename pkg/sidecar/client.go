// Package sidecar provides a client for the Perplexity API sidecar.
// This allows the bridge to use the official Perplexity Python SDK.
package sidecar

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/rs/zerolog"
)

// Retry and circuit breaker configuration.
const (
	maxRetries       = 3
	initialBackoff   = 100 * time.Millisecond
	maxBackoff       = 5 * time.Second
	circuitThreshold = 5
	circuitTimeout   = 30 * time.Second
)

// CircuitState represents the state of the circuit breaker.
type CircuitState int

const (
	CircuitClosed CircuitState = iota
	CircuitOpen
	CircuitHalfOpen
)

// Client is an HTTP client for the Perplexity API sidecar.
type Client struct {
	baseURL    string
	httpClient *http.Client
	log        zerolog.Logger

	mu               sync.Mutex
	circuitState     CircuitState
	consecutiveFails int
	lastFailTime     time.Time
}

// ContentBlock represents a content block (text or image) for multimodal messages.
type ContentBlock struct {
	Type   string       `json:"type"`             // "text" or "image"
	Text   string       `json:"text,omitempty"`   // For text blocks
	Source *ImageSource `json:"source,omitempty"` // For image blocks
}

// ImageSource represents an image source for multimodal messages.
type ImageSource struct {
	Type      string `json:"type"`       // "base64"
	MediaType string `json:"media_type"` // "image/jpeg", "image/png", etc.
	Data      string `json:"data"`       // Base64-encoded image data
}

// UserLocation represents user location for location-aware search results.
type UserLocation struct {
	City     string `json:"city,omitempty"`
	Region   string `json:"region,omitempty"`
	Country  string `json:"country,omitempty"`
	Timezone string `json:"timezone,omitempty"`
}

// WebSearchOptions represents Perplexity web search options.
type WebSearchOptions struct {
	SearchDomainFilter     []string      `json:"search_domain_filter,omitempty"`
	SearchRecencyFilter    string        `json:"search_recency_filter,omitempty"`      // "day", "week", "month", "year"
	SearchAfterDateFilter  string        `json:"search_after_date_filter,omitempty"`   // MM/DD/YYYY format
	SearchBeforeDateFilter string        `json:"search_before_date_filter,omitempty"`  // MM/DD/YYYY format
	ReturnImages           *bool         `json:"return_images,omitempty"`              // Include images in results (Tier-2+)
	SearchContextSize      string        `json:"search_context_size,omitempty"`        // "low", "medium", "high"
	SearchMode             string        `json:"search_mode,omitempty"`                // "academic" or "web"
	UserLocation           *UserLocation `json:"user_location,omitempty"`              // Location for local results
}

// ChatRequest is the request body for the chat endpoint.
type ChatRequest struct {
	PortalID         string            `json:"portal_id"`
	APIKey           string            `json:"api_key"`                      // Perplexity API key
	UserID           string            `json:"user_id,omitempty"`            // Matrix user ID (for logging)
	Message          string            `json:"message"`                      // Text-only message
	Content          []ContentBlock    `json:"content,omitempty"`            // Structured content with images
	SystemPrompt     *string           `json:"system_prompt,omitempty"`
	Model            *string           `json:"model,omitempty"`
	SessionID        string            `json:"session_id,omitempty"`         // Session ID for resume after restart
	Stream           bool              `json:"stream"`
	WebSearchOptions *WebSearchOptions `json:"web_search_options,omitempty"`
	MaxTokens        *int              `json:"max_tokens,omitempty"`
	Temperature      *float64          `json:"temperature,omitempty"`
	ConversationMode bool              `json:"conversation_mode,omitempty"` // Enable multi-turn history (default: false)
}

// SearchResult represents a search result from Perplexity.
type SearchResult struct {
	Title string `json:"title,omitempty"`
	URL   string `json:"url,omitempty"`
	Date  string `json:"date,omitempty"`
}

// ImageResult represents an image result from Perplexity.
type ImageResult struct {
	URL       string `json:"url,omitempty"`
	OriginURL string `json:"origin_url,omitempty"`
	Height    int    `json:"height,omitempty"`
	Width     int    `json:"width,omitempty"`
}

// ChatResponse is the response body from the chat endpoint.
type ChatResponse struct {
	PortalID      string         `json:"portal_id"`
	SessionID     string         `json:"session_id"`
	Response      string         `json:"response"`
	Model         string         `json:"model"`
	TokensUsed    *int           `json:"tokens_used,omitempty"`
	SearchResults []SearchResult `json:"search_results,omitempty"`
	Images        []ImageResult  `json:"images,omitempty"`
}

// SessionStats contains statistics about a session.
type SessionStats struct {
	SessionID    string  `json:"session_id"`
	PortalID     string  `json:"portal_id"`
	CreatedAt    float64 `json:"created_at"`
	LastUsed     float64 `json:"last_used"`
	MessageCount int     `json:"message_count"`
	AgeSeconds   float64 `json:"age_seconds"`
	InputTokens  int64   `json:"input_tokens"`
	OutputTokens int64   `json:"output_tokens"`
}

// HealthResponse is the response from the health endpoint.
type HealthResponse struct {
	Status        string `json:"status"`        // "healthy"
	Sessions      int    `json:"sessions"`      // Active session count
	Authenticated bool   `json:"authenticated"` // Always true for Perplexity (per-request auth)
}

// TestAuthRequest is the request body for testing user API key.
type TestAuthRequest struct {
	UserID string `json:"user_id"`
	APIKey string `json:"api_key"`
}

// TestAuthResponse is the response from the auth test endpoint.
type TestAuthResponse struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
}

// NewClient creates a new sidecar client with the specified timeout.
func NewClient(baseURL string, timeout time.Duration, log zerolog.Logger) *Client {
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	return &Client{
		baseURL: baseURL,
		httpClient: &http.Client{
			Timeout: timeout,
		},
		log:          log.With().Str("component", "sidecar-client").Logger(),
		circuitState: CircuitClosed,
	}
}

func (c *Client) checkCircuit() bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	switch c.circuitState {
	case CircuitOpen:
		if time.Since(c.lastFailTime) >= circuitTimeout {
			c.circuitState = CircuitHalfOpen
			c.log.Info().Msg("Circuit breaker: half-open, allowing test request")
			return true
		}
		return false
	case CircuitHalfOpen, CircuitClosed:
		return true
	}
	return true
}

func (c *Client) recordSuccess() {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.consecutiveFails = 0
	if c.circuitState == CircuitHalfOpen {
		c.circuitState = CircuitClosed
		c.log.Info().Msg("Circuit breaker: closed (recovered)")
	}
}

func (c *Client) recordFailure() {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.consecutiveFails++
	c.lastFailTime = time.Now()

	if c.consecutiveFails >= circuitThreshold && c.circuitState != CircuitOpen {
		c.circuitState = CircuitOpen
		c.log.Warn().Int("failures", c.consecutiveFails).Msg("Circuit breaker: opened due to consecutive failures")
	}
}

func isRetryable(err error, statusCode int) bool {
	if err != nil {
		return true
	}
	return statusCode >= 500 || statusCode == 429
}

func backoff(attempt int) time.Duration {
	delay := initialBackoff * time.Duration(1<<uint(attempt))
	if delay > maxBackoff {
		delay = maxBackoff
	}
	return delay
}

// Health checks if the sidecar is healthy.
func (c *Client) Health(ctx context.Context) (*HealthResponse, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", c.baseURL+"/health", nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to make request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("health check failed: %s - %s", resp.Status, string(body))
	}

	var health HealthResponse
	if err := json.NewDecoder(resp.Body).Decode(&health); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return &health, nil
}

// TestAuth tests user API key by making a minimal Perplexity API call.
func (c *Client) TestAuth(ctx context.Context, userID, apiKey string) (*TestAuthResponse, error) {
	reqBody := TestAuthRequest{
		UserID: userID,
		APIKey: apiKey,
	}

	jsonBody, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/v1/auth/test", bytes.NewReader(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to make request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("auth test failed: %s - %s", resp.Status, string(body))
	}

	var authResp TestAuthResponse
	if err := json.NewDecoder(resp.Body).Decode(&authResp); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return &authResp, nil
}

// Chat sends a message to Perplexity and returns the response.
func (c *Client) Chat(ctx context.Context, portalID, userID, apiKey, message string, systemPrompt, model *string) (*ChatResponse, error) {
	return c.ChatWithContent(ctx, portalID, userID, apiKey, message, nil, systemPrompt, model, nil, false, "")
}

// ChatWithContent sends a message to Perplexity with optional structured content (for images).
func (c *Client) ChatWithContent(ctx context.Context, portalID, userID, apiKey, message string, content []ContentBlock, systemPrompt, model *string, webSearchOptions *WebSearchOptions, conversationMode bool, sessionID string) (*ChatResponse, error) {
	// Validate required fields early to avoid unnecessary network calls
	if apiKey == "" {
		return nil, fmt.Errorf("API key is required")
	}
	if portalID == "" {
		return nil, fmt.Errorf("portal ID is required")
	}
	if message == "" && len(content) == 0 {
		return nil, fmt.Errorf("message or content is required")
	}

	if !c.checkCircuit() {
		return nil, fmt.Errorf("circuit breaker open: sidecar temporarily unavailable")
	}

	reqBody := ChatRequest{
		PortalID:         portalID,
		APIKey:           apiKey,
		UserID:           userID,
		Message:          message,
		Content:          content,
		SystemPrompt:     systemPrompt,
		Model:            model,
		SessionID:        sessionID,
		Stream:           false,
		WebSearchOptions: webSearchOptions,
		ConversationMode: conversationMode,
	}

	jsonBody, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	c.log.Debug().
		Str("portal_id", portalID).
		Str("message_preview", truncate(message, 50)).
		Msg("Sending chat request to sidecar")

	var lastErr error
	startTime := time.Now()

	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			delay := backoff(attempt - 1)
			c.log.Debug().Int("attempt", attempt+1).Dur("backoff", delay).Msg("Retrying chat request")
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(delay):
			}
		}

		req, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/v1/chat", bytes.NewReader(jsonBody))
		if err != nil {
			return nil, fmt.Errorf("failed to create request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")

		resp, err := c.httpClient.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("failed to make request: %w", err)
			if isRetryable(err, 0) && attempt < maxRetries {
				continue
			}
			c.recordFailure()
			return nil, lastErr
		}

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			lastErr = fmt.Errorf("chat request failed: %s - %s", resp.Status, string(body))
			if isRetryable(nil, resp.StatusCode) && attempt < maxRetries {
				continue
			}
			c.recordFailure()
			return nil, lastErr
		}

		var chatResp ChatResponse
		if err := json.NewDecoder(resp.Body).Decode(&chatResp); err != nil {
			resp.Body.Close()
			return nil, fmt.Errorf("failed to decode response: %w", err)
		}
		resp.Body.Close()

		c.recordSuccess()

		c.log.Debug().
			Str("portal_id", portalID).
			Str("session_id", chatResp.SessionID).
			Dur("duration", time.Since(startTime)).
			Int("attempts", attempt+1).
			Str("response_preview", truncate(chatResp.Response, 50)).
			Msg("Received chat response from sidecar")

		return &chatResp, nil
	}

	c.recordFailure()
	return nil, lastErr
}

// DeleteSession clears the conversation history for a portal.
func (c *Client) DeleteSession(ctx context.Context, portalID string) error {
	req, err := http.NewRequestWithContext(ctx, "DELETE", c.baseURL+"/v1/sessions/"+portalID, nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to make request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNotFound {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("delete session failed: %s - %s", resp.Status, string(body))
	}

	c.log.Debug().
		Str("portal_id", portalID).
		Msg("Deleted sidecar session")

	return nil
}

// GetSession gets statistics about a session.
func (c *Client) GetSession(ctx context.Context, portalID string) (*SessionStats, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", c.baseURL+"/v1/sessions/"+portalID, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to make request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("get session failed: %s - %s", resp.Status, string(body))
	}

	var stats SessionStats
	if err := json.NewDecoder(resp.Body).Decode(&stats); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return &stats, nil
}

func truncate(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return string(runes[:maxLen]) + "..."
}
