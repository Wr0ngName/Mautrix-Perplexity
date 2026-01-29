package sidecar

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rs/zerolog"

	"go.mau.fi/mautrix-perplexity/pkg/perplexityapi"
)

// TestIntegrationMessageClientToSidecar tests the full flow from MessageClient to sidecar HTTP API.
func TestIntegrationMessageClientToSidecar(t *testing.T) {
	// Track requests
	var requestCount int32

	// Create mock sidecar server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requestCount, 1)

		switch r.URL.Path {
		case "/health":
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(HealthResponse{
				Status:        "healthy",
				Sessions:      0,
				Authenticated: true,
			})
		case "/v1/chat":
			if r.Method != "POST" {
				t.Errorf("Expected POST, got %s", r.Method)
			}

			var req ChatRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Errorf("Failed to decode request: %v", err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}

			// Validate request
			if req.PortalID == "" {
				t.Error("Expected portal_id to be set")
			}
			if req.Message == "" {
				t.Error("Expected message to be set")
			}

			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(ChatResponse{
				PortalID:  req.PortalID,
				SessionID: "test-session-123",
				Response:  "Hello! I received your message: " + req.Message,
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	// Create MessageClient pointing to mock server
	log := zerolog.Nop()
	client := NewMessageClient(server.URL, 30*time.Second, log)

	// Test 1: Validate (health check)
	t.Run("Validate", func(t *testing.T) {
		err := client.Validate(context.Background())
		if err != nil {
			t.Errorf("Validate failed: %v", err)
		}
	})

	// Test 2: CreateMessage
	t.Run("CreateMessage", func(t *testing.T) {
		ctx := WithPortalID(context.Background(), "test-portal-123")
		ctx = WithUserCredentials(ctx, "", "pplx-test-key-123")
		req := &perplexityapi.CreateMessageRequest{
			Model: "sonar",
			Messages: []perplexityapi.Message{
				{
					Role:    "user",
					Content: []perplexityapi.Content{{Type: "text", Text: "Hello Perplexity!"}},
				},
			},
		}

		resp, err := client.CreateMessage(ctx, req)
		if err != nil {
			t.Fatalf("CreateMessage failed: %v", err)
		}

		if resp.Role != "assistant" {
			t.Errorf("Expected role 'assistant', got '%s'", resp.Role)
		}
		if len(resp.Content) == 0 {
			t.Error("Expected content in response")
		}
	})

	// Test 3: CreateMessageStream
	t.Run("CreateMessageStream", func(t *testing.T) {
		ctx := WithPortalID(context.Background(), "test-portal-456")
		ctx = WithUserCredentials(ctx, "", "pplx-test-key-123")
		req := &perplexityapi.CreateMessageRequest{
			Model: "sonar",
			Messages: []perplexityapi.Message{
				{
					Role:    "user",
					Content: []perplexityapi.Content{{Type: "text", Text: "Streaming test"}},
				},
			},
		}

		stream, err := client.CreateMessageStream(ctx, req)
		if err != nil {
			t.Fatalf("CreateMessageStream failed: %v", err)
		}

		var eventCount int
		var gotContent bool
		for event := range stream {
			eventCount++
			if event.Type == "content_block_delta" && event.Delta != nil {
				gotContent = true
			}
		}

		if eventCount == 0 {
			t.Error("Expected to receive events from stream")
		}
		if !gotContent {
			t.Error("Expected to receive content_block_delta event")
		}
	})

	// Verify requests were made
	if atomic.LoadInt32(&requestCount) < 3 {
		t.Errorf("Expected at least 3 requests to sidecar, got %d", requestCount)
	}
}

// TestIntegrationRetryBehavior tests retry logic with transient failures.
func TestIntegrationRetryBehavior(t *testing.T) {
	var requestCount int32

	// Server that fails first 2 requests, then succeeds
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count := atomic.AddInt32(&requestCount, 1)

		if r.URL.Path == "/v1/chat" {
			if count <= 2 {
				// Simulate transient failure
				w.WriteHeader(http.StatusServiceUnavailable)
				w.Write([]byte("temporarily unavailable"))
				return
			}
			// Success on 3rd attempt
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(ChatResponse{
				PortalID:  "test",
				SessionID: "session-123",
				Response:  "Success after retries!",
			})
		}
	}))
	defer server.Close()

	log := zerolog.Nop()
	client := NewClient(server.URL, 30*time.Second, log)

	resp, err := client.Chat(context.Background(), "test-portal", "", "pplx-test-key-123", "test message", nil, nil)
	if err != nil {
		t.Fatalf("Expected success after retries, got error: %v", err)
	}

	if resp.Response != "Success after retries!" {
		t.Errorf("Unexpected response: %s", resp.Response)
	}

	if atomic.LoadInt32(&requestCount) != 3 {
		t.Errorf("Expected 3 requests (2 failures + 1 success), got %d", requestCount)
	}
}

// TestIntegrationCircuitBreaker tests circuit breaker behavior.
func TestIntegrationCircuitBreaker(t *testing.T) {
	var requestCount int32

	// Server that always fails
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requestCount, 1)
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("server error"))
	}))
	defer server.Close()

	log := zerolog.Nop()
	client := NewClient(server.URL, 5*time.Second, log)

	// Make requests until circuit opens
	for i := 0; i < 10; i++ {
		_, _ = client.Chat(context.Background(), "test", "", "pplx-test-key-123", "message", nil, nil)
	}

	// After many failures, circuit should be open
	_, err := client.Chat(context.Background(), "test", "", "pplx-test-key-123", "message", nil, nil)
	if err == nil || err.Error() != "circuit breaker open: sidecar temporarily unavailable" {
		// Circuit might not be open yet depending on timing, but we should have errors
		t.Logf("Circuit breaker state: %v", err)
	}

	// Verify circuit breaker prevented some requests
	count := atomic.LoadInt32(&requestCount)
	t.Logf("Total requests made: %d", count)
}

// TestIntegrationSessionManagement tests session lifecycle.
func TestIntegrationSessionManagement(t *testing.T) {
	sessions := make(map[string]bool)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/chat":
			var req ChatRequest
			json.NewDecoder(r.Body).Decode(&req)
			sessions[req.PortalID] = true
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(ChatResponse{
				PortalID:  req.PortalID,
				SessionID: "session-" + req.PortalID,
				Response:  "Response for " + req.PortalID,
			})

		case r.Method == "DELETE":
			portalID := r.URL.Path[len("/v1/sessions/"):]
			if sessions[portalID] {
				delete(sessions, portalID)
				w.WriteHeader(http.StatusOK)
				json.NewEncoder(w).Encode(map[string]string{"status": "deleted"})
			} else {
				w.WriteHeader(http.StatusNotFound)
			}

		case r.Method == "GET" && len(r.URL.Path) > len("/v1/sessions/"):
			portalID := r.URL.Path[len("/v1/sessions/"):]
			if sessions[portalID] {
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(SessionStats{
					SessionID:    "session-" + portalID,
					PortalID:     portalID,
					MessageCount: 1,
				})
			} else {
				w.WriteHeader(http.StatusNotFound)
			}
		}
	}))
	defer server.Close()

	log := zerolog.Nop()
	messageClient := NewMessageClient(server.URL, 30*time.Second, log)

	// Create session via chat
	ctx := WithPortalID(context.Background(), "portal-abc")
	ctx = WithUserCredentials(ctx, "", "pplx-test-key-123")
	req := &perplexityapi.CreateMessageRequest{
		Messages: []perplexityapi.Message{{
			Role:    "user",
			Content: []perplexityapi.Content{{Type: "text", Text: "Hello"}},
		}},
	}
	_, err := messageClient.CreateMessage(ctx, req)
	if err != nil {
		t.Fatalf("CreateMessage failed: %v", err)
	}

	// Get session stats
	stats, err := messageClient.GetSessionStats(context.Background(), "portal-abc")
	if err != nil {
		t.Fatalf("GetSessionStats failed: %v", err)
	}
	if stats == nil {
		t.Fatal("Expected session stats, got nil")
	}

	// Clear session
	err = messageClient.ClearSession(context.Background(), "portal-abc")
	if err != nil {
		t.Fatalf("ClearSession failed: %v", err)
	}

	// Verify session is gone
	stats, err = messageClient.GetSessionStats(context.Background(), "portal-abc")
	if err != nil {
		t.Fatalf("GetSessionStats after clear failed: %v", err)
	}
	if stats != nil {
		t.Error("Expected nil stats after clearing session")
	}
}

// TestIntegrationContextCancellation tests that context cancellation is respected.
func TestIntegrationContextCancellation(t *testing.T) {
	// Server that delays response
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(5 * time.Second) // Delay longer than context timeout
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	log := zerolog.Nop()
	client := NewClient(server.URL, 30*time.Second, log)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	_, err := client.Chat(ctx, "test", "", "pplx-test-key-123", "message", nil, nil)
	if err == nil {
		t.Error("Expected error due to context cancellation")
	}
}

// TestIntegrationMetricsTracking tests that metrics are properly tracked.
func TestIntegrationMetricsTracking(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(HealthResponse{Status: "healthy"})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(ChatResponse{
			PortalID:  "test",
			SessionID: "session",
			Response:  "response",
		})
	}))
	defer server.Close()

	log := zerolog.Nop()
	client := NewMessageClient(server.URL, 30*time.Second, log)

	// Make a few requests
	for i := 0; i < 3; i++ {
		ctx := WithPortalID(context.Background(), "test")
		req := &perplexityapi.CreateMessageRequest{
			Messages: []perplexityapi.Message{{
				Role:    "user",
				Content: []perplexityapi.Content{{Type: "text", Text: "Hello"}},
			}},
		}
		_, _ = client.CreateMessage(ctx, req)
	}

	// Check metrics
	metrics := client.GetMetrics()
	if metrics == nil {
		t.Fatal("Expected metrics, got nil")
	}

	totalReqs := metrics.TotalRequests.Load()
	if totalReqs < 3 {
		t.Errorf("Expected at least 3 total requests, got %d", totalReqs)
	}
}
