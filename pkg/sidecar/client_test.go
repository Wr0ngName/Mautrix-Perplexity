package sidecar

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"
)

func TestNewClient(t *testing.T) {
	log := zerolog.Nop()

	t.Run("with custom timeout", func(t *testing.T) {
		client := NewClient("http://localhost:8090", 30*time.Second, log)

		if client == nil {
			t.Fatal("NewClient returned nil")
		}

		if client.baseURL != "http://localhost:8090" {
			t.Errorf("Expected baseURL 'http://localhost:8090', got '%s'", client.baseURL)
		}

		if client.httpClient == nil {
			t.Error("httpClient should not be nil")
		}

		if client.httpClient.Timeout != 30*time.Second {
			t.Errorf("Expected timeout 30s, got %v", client.httpClient.Timeout)
		}
	})

	t.Run("with zero timeout uses default", func(t *testing.T) {
		client := NewClient("http://localhost:8090", 0, log)

		if client.httpClient.Timeout != 5*time.Minute {
			t.Errorf("Expected default timeout 5m, got %v", client.httpClient.Timeout)
		}
	})

	t.Run("with negative timeout uses default", func(t *testing.T) {
		client := NewClient("http://localhost:8090", -1*time.Second, log)

		if client.httpClient.Timeout != 5*time.Minute {
			t.Errorf("Expected default timeout 5m, got %v", client.httpClient.Timeout)
		}
	})
}

func TestHealth(t *testing.T) {
	tests := []struct {
		name           string
		serverResponse func(w http.ResponseWriter, r *http.Request)
		wantErr        bool
		wantStatus     string
		wantSessions   int
	}{
		{
			name: "healthy response",
			serverResponse: func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/health" {
					t.Errorf("Expected path '/health', got '%s'", r.URL.Path)
				}
				if r.Method != "GET" {
					t.Errorf("Expected GET method, got '%s'", r.Method)
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				json.NewEncoder(w).Encode(HealthResponse{
					Status:   "healthy",
					Sessions: 5,
				})
			},
			wantErr:      false,
			wantStatus:   "healthy",
			wantSessions: 5,
		},
		{
			name: "unhealthy response",
			serverResponse: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusServiceUnavailable)
				w.Write([]byte("service unavailable"))
			},
			wantErr: true,
		},
		{
			name: "invalid json response",
			serverResponse: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
				w.Write([]byte("not json"))
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(tt.serverResponse))
			defer server.Close()

			client := NewClient(server.URL, 30*time.Second, zerolog.Nop())
			ctx := context.Background()

			resp, err := client.Health(ctx)

			if (err != nil) != tt.wantErr {
				t.Errorf("Health() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if !tt.wantErr {
				if resp.Status != tt.wantStatus {
					t.Errorf("Status = %s, want %s", resp.Status, tt.wantStatus)
				}
				if resp.Sessions != tt.wantSessions {
					t.Errorf("Sessions = %d, want %d", resp.Sessions, tt.wantSessions)
				}
			}
		})
	}
}

func TestHealthContextCancellation(t *testing.T) {
	// Create a server that delays response
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(100 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(HealthResponse{Status: "healthy"})
	}))
	defer server.Close()

	client := NewClient(server.URL, 30*time.Second, zerolog.Nop())
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	_, err := client.Health(ctx)
	if err == nil {
		t.Error("Expected error due to context cancellation")
	}
}

func TestChat(t *testing.T) {
	tests := []struct {
		name           string
		portalID       string
		message        string
		systemPrompt   *string
		model          *string
		serverResponse func(w http.ResponseWriter, r *http.Request)
		wantErr        bool
		wantResponse   string
		wantSessionID  string
	}{
		{
			name:     "successful chat",
			portalID: "portal123",
			message:  "Hello Perplexity",
			serverResponse: func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/chat" {
					t.Errorf("Expected path '/v1/chat', got '%s'", r.URL.Path)
				}
				if r.Method != "POST" {
					t.Errorf("Expected POST method, got '%s'", r.Method)
				}
				if r.Header.Get("Content-Type") != "application/json" {
					t.Errorf("Expected Content-Type 'application/json', got '%s'", r.Header.Get("Content-Type"))
				}

				// Verify request body
				var req ChatRequest
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Errorf("Failed to decode request: %v", err)
				}
				if req.PortalID != "portal123" {
					t.Errorf("Expected portal_id 'portal123', got '%s'", req.PortalID)
				}
				if req.Message != "Hello Perplexity" {
					t.Errorf("Expected message 'Hello Perplexity', got '%s'", req.Message)
				}
				if req.Stream != false {
					t.Error("Expected stream to be false")
				}

				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				json.NewEncoder(w).Encode(ChatResponse{
					PortalID:   "portal123",
					SessionID:  "session456",
					Response:   "Hello! How can I help?",
					TokensUsed: intPtr(50),
				})
			},
			wantErr:       false,
			wantResponse:  "Hello! How can I help?",
			wantSessionID: "session456",
		},
		{
			name:         "with system prompt and model",
			portalID:     "portal123",
			message:      "Test message",
			systemPrompt: strPtr("You are a helpful assistant"),
			model:        strPtr("sonar-pro"),
			serverResponse: func(w http.ResponseWriter, r *http.Request) {
				var req ChatRequest
				json.NewDecoder(r.Body).Decode(&req)

				if req.SystemPrompt == nil || *req.SystemPrompt != "You are a helpful assistant" {
					t.Error("System prompt not properly sent")
				}
				if req.Model == nil || *req.Model != "sonar-pro" {
					t.Error("Model not properly sent")
				}

				w.WriteHeader(http.StatusOK)
				json.NewEncoder(w).Encode(ChatResponse{
					PortalID:  "portal123",
					SessionID: "session456",
					Response:  "Response",
				})
			},
			wantErr:       false,
			wantResponse:  "Response",
			wantSessionID: "session456",
		},
		{
			name:     "server error",
			portalID: "portal123",
			message:  "Hello",
			serverResponse: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusInternalServerError)
				w.Write([]byte("internal server error"))
			},
			wantErr: true,
		},
		{
			name:     "invalid json response",
			portalID: "portal123",
			message:  "Hello",
			serverResponse: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
				w.Write([]byte("not json"))
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(tt.serverResponse))
			defer server.Close()

			client := NewClient(server.URL, 30*time.Second, zerolog.Nop())
			ctx := context.Background()

			resp, err := client.Chat(ctx, tt.portalID, "", "pplx-test-key-123", tt.message, tt.systemPrompt, tt.model)

			if (err != nil) != tt.wantErr {
				t.Errorf("Chat() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if !tt.wantErr {
				if resp.Response != tt.wantResponse {
					t.Errorf("Response = %s, want %s", resp.Response, tt.wantResponse)
				}
				if resp.SessionID != tt.wantSessionID {
					t.Errorf("SessionID = %s, want %s", resp.SessionID, tt.wantSessionID)
				}
				if resp.PortalID != tt.portalID {
					t.Errorf("PortalID = %s, want %s", resp.PortalID, tt.portalID)
				}
			}
		})
	}
}

func TestChatContextCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(100 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(ChatResponse{Response: "too late"})
	}))
	defer server.Close()

	client := NewClient(server.URL, 30*time.Second, zerolog.Nop())
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	_, err := client.Chat(ctx, "portal123", "", "pplx-test-key-123", "message", nil, nil)
	if err == nil {
		t.Error("Expected error due to context cancellation")
	}
}

func TestDeleteSession(t *testing.T) {
	tests := []struct {
		name           string
		portalID       string
		serverResponse func(w http.ResponseWriter, r *http.Request)
		wantErr        bool
	}{
		{
			name:     "successful delete",
			portalID: "portal123",
			serverResponse: func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/sessions/portal123" {
					t.Errorf("Expected path '/v1/sessions/portal123', got '%s'", r.URL.Path)
				}
				if r.Method != "DELETE" {
					t.Errorf("Expected DELETE method, got '%s'", r.Method)
				}
				w.WriteHeader(http.StatusOK)
			},
			wantErr: false,
		},
		{
			name:     "session not found (should not error)",
			portalID: "nonexistent",
			serverResponse: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusNotFound)
			},
			wantErr: false, // 404 is acceptable for delete
		},
		{
			name:     "server error",
			portalID: "portal123",
			serverResponse: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusInternalServerError)
				w.Write([]byte("internal error"))
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(tt.serverResponse))
			defer server.Close()

			client := NewClient(server.URL, 30*time.Second, zerolog.Nop())
			ctx := context.Background()

			err := client.DeleteSession(ctx, tt.portalID)

			if (err != nil) != tt.wantErr {
				t.Errorf("DeleteSession() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

// Note: DeleteUser is not applicable to Perplexity sidecar as it uses API key auth,
// not per-user credential storage.

func TestGetSession(t *testing.T) {
	tests := []struct {
		name           string
		portalID       string
		serverResponse func(w http.ResponseWriter, r *http.Request)
		wantErr        bool
		wantStats      *SessionStats
		wantNil        bool
	}{
		{
			name:     "successful get",
			portalID: "portal123",
			serverResponse: func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/sessions/portal123" {
					t.Errorf("Expected path '/v1/sessions/portal123', got '%s'", r.URL.Path)
				}
				if r.Method != "GET" {
					t.Errorf("Expected GET method, got '%s'", r.Method)
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				json.NewEncoder(w).Encode(SessionStats{
					SessionID:    "session456",
					PortalID:     "portal123",
					CreatedAt:    1234567890.0,
					LastUsed:     1234567900.0,
					MessageCount: 10,
					AgeSeconds:   100.0,
				})
			},
			wantErr: false,
			wantStats: &SessionStats{
				SessionID:    "session456",
				PortalID:     "portal123",
				CreatedAt:    1234567890.0,
				LastUsed:     1234567900.0,
				MessageCount: 10,
				AgeSeconds:   100.0,
			},
			wantNil: false,
		},
		{
			name:     "session not found (returns nil)",
			portalID: "nonexistent",
			serverResponse: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusNotFound)
			},
			wantErr: false,
			wantNil: true,
		},
		{
			name:     "server error",
			portalID: "portal123",
			serverResponse: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusInternalServerError)
				w.Write([]byte("internal error"))
			},
			wantErr: true,
		},
		{
			name:     "invalid json response",
			portalID: "portal123",
			serverResponse: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
				w.Write([]byte("not json"))
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(tt.serverResponse))
			defer server.Close()

			client := NewClient(server.URL, 30*time.Second, zerolog.Nop())
			ctx := context.Background()

			stats, err := client.GetSession(ctx, tt.portalID)

			if (err != nil) != tt.wantErr {
				t.Errorf("GetSession() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if tt.wantNil {
				if stats != nil {
					t.Errorf("Expected nil stats, got %+v", stats)
				}
			} else if !tt.wantErr {
				if stats == nil {
					t.Fatal("Expected non-nil stats")
				}
				if stats.SessionID != tt.wantStats.SessionID {
					t.Errorf("SessionID = %s, want %s", stats.SessionID, tt.wantStats.SessionID)
				}
				if stats.PortalID != tt.wantStats.PortalID {
					t.Errorf("PortalID = %s, want %s", stats.PortalID, tt.wantStats.PortalID)
				}
				if stats.MessageCount != tt.wantStats.MessageCount {
					t.Errorf("MessageCount = %d, want %d", stats.MessageCount, tt.wantStats.MessageCount)
				}
			}
		})
	}
}

func TestTruncate(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		maxLen int
		want   string
	}{
		{
			name:   "short string",
			input:  "hello",
			maxLen: 10,
			want:   "hello",
		},
		{
			name:   "exact length",
			input:  "hello world",
			maxLen: 11,
			want:   "hello world",
		},
		{
			name:   "needs truncation",
			input:  "hello world this is a long string",
			maxLen: 10,
			want:   "hello worl...",
		},
		{
			name:   "empty string",
			input:  "",
			maxLen: 10,
			want:   "",
		},
		{
			name:   "zero max length",
			input:  "hello",
			maxLen: 0,
			want:   "...",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := truncate(tt.input, tt.maxLen)
			if got != tt.want {
				t.Errorf("truncate() = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestChatRequestMarshaling(t *testing.T) {
	systemPrompt := "You are helpful"
	model := "sonar-pro"

	req := ChatRequest{
		PortalID:     "portal123",
		Message:      "Hello",
		SystemPrompt: &systemPrompt,
		Model:        &model,
		Stream:       true,
	}

	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("Failed to marshal: %v", err)
	}

	var decoded ChatRequest
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Failed to unmarshal: %v", err)
	}

	if decoded.PortalID != req.PortalID {
		t.Errorf("PortalID mismatch")
	}
	if decoded.Message != req.Message {
		t.Errorf("Message mismatch")
	}
	if *decoded.SystemPrompt != *req.SystemPrompt {
		t.Errorf("SystemPrompt mismatch")
	}
	if *decoded.Model != *req.Model {
		t.Errorf("Model mismatch")
	}
	if decoded.Stream != req.Stream {
		t.Errorf("Stream mismatch")
	}
}

func TestHTTPClientTimeout(t *testing.T) {
	// Create a server that never responds
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(10 * time.Second) // Longer than test timeout
	}))
	defer server.Close()

	client := NewClient(server.URL, 30*time.Second, zerolog.Nop())
	// Override timeout for test
	client.httpClient.Timeout = 50 * time.Millisecond

	ctx := context.Background()
	_, err := client.Health(ctx)

	if err == nil {
		t.Error("Expected timeout error")
	}
	if !strings.Contains(err.Error(), "deadline") && !strings.Contains(err.Error(), "timeout") {
		t.Errorf("Expected timeout/deadline error, got: %v", err)
	}
}

func TestClientInvalidURL(t *testing.T) {
	client := NewClient("http://invalid-host-that-does-not-exist:9999", 30*time.Second, zerolog.Nop())
	ctx := context.Background()

	_, err := client.Health(ctx)
	if err == nil {
		t.Error("Expected error for invalid URL")
	}
}

func TestChatEmptyMessage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req ChatRequest
		json.NewDecoder(r.Body).Decode(&req)

		if req.Message == "" {
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte("empty message"))
			return
		}

		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(ChatResponse{Response: "ok"})
	}))
	defer server.Close()

	client := NewClient(server.URL, 30*time.Second, zerolog.Nop())
	ctx := context.Background()

	// Client should reject empty message (now validated client-side)
	_, err := client.Chat(ctx, "portal123", "", "pplx-test-key-123", "", nil, nil)
	if err == nil {
		t.Error("Expected error for empty message")
	}
}

// Helper functions
func strPtr(s string) *string {
	return &s
}

func intPtr(i int) *int {
	return &i
}

// Benchmark tests
func BenchmarkHealth(b *testing.B) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(HealthResponse{Status: "healthy", Sessions: 0})
	}))
	defer server.Close()

	client := NewClient(server.URL, 30*time.Second, zerolog.Nop())
	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = client.Health(ctx)
	}
}

func BenchmarkChat(b *testing.B) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(ChatResponse{
			PortalID:  "portal123",
			SessionID: "session456",
			Response:  "Hello!",
		})
	}))
	defer server.Close()

	client := NewClient(server.URL, 30*time.Second, zerolog.Nop())
	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = client.Chat(ctx, "portal123", "", "pplx-test-key-123", "Hello", nil, nil)
	}
}
