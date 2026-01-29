package sidecar

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/rs/zerolog"

	"go.mau.fi/mautrix-perplexity/pkg/perplexityapi"
)

// extractMessageText is a test helper that extracts the last user message text.
func extractMessageText(messages []perplexityapi.Message) string {
	var lastUserText string
	for _, msg := range messages {
		if msg.Role == "user" {
			for _, c := range msg.Content {
				if c.Type == "text" {
					lastUserText = c.Text
				}
			}
		}
	}
	return lastUserText
}

func TestNewMessageClient(t *testing.T) {
	log := zerolog.Nop()
	client := NewMessageClient("http://localhost:8090", 5*time.Minute, log)

	if client == nil {
		t.Fatal("NewMessageClient returned nil")
	}

	if client.client == nil {
		t.Error("client should not be nil")
	}

	if client.metrics == nil {
		t.Error("metrics should not be nil")
	}
}

func TestMessageClientGetClientType(t *testing.T) {
	client := NewMessageClient("http://localhost:8090", 5*time.Minute, zerolog.Nop())

	if clientType := client.GetClientType(); clientType != "sidecar" {
		t.Errorf("Expected client type 'sidecar', got '%s'", clientType)
	}
}

func TestMessageClientGetMetrics(t *testing.T) {
	client := NewMessageClient("http://localhost:8090", 5*time.Minute, zerolog.Nop())

	metrics := client.GetMetrics()
	if metrics == nil {
		t.Error("GetMetrics returned nil")
	}
}

func TestMessageClientValidate(t *testing.T) {
	tests := []struct {
		name           string
		serverResponse func(w http.ResponseWriter, r *http.Request)
		wantErr        bool
	}{
		{
			name: "healthy sidecar",
			serverResponse: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				json.NewEncoder(w).Encode(HealthResponse{
					Status:        "healthy",
					Sessions:      0,
					Authenticated: true,
				})
			},
			wantErr: false,
		},
		{
			name: "unhealthy sidecar",
			serverResponse: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				json.NewEncoder(w).Encode(HealthResponse{
					Status:   "degraded",
					Sessions: 0,
				})
			},
			wantErr: true,
		},
		{
			name: "sidecar error",
			serverResponse: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusServiceUnavailable)
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(tt.serverResponse))
			defer server.Close()

			client := NewMessageClient(server.URL, 5*time.Minute, zerolog.Nop())
			ctx := context.Background()

			err := client.Validate(ctx)

			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestCreateMessage(t *testing.T) {
	tests := []struct {
		name           string
		request        *perplexityapi.CreateMessageRequest
		serverResponse func(w http.ResponseWriter, r *http.Request)
		wantErr        bool
		wantResponse   string
	}{
		{
			name: "successful message",
			request: &perplexityapi.CreateMessageRequest{
				Model:     "sonar",
				MaxTokens: 1024,
				Messages: []perplexityapi.Message{
					{
						Role: "user",
						Content: []perplexityapi.Content{
							{Type: "text", Text: "Hello Perplexity"},
						},
					},
				},
			},
			serverResponse: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				json.NewEncoder(w).Encode(ChatResponse{
					PortalID:   "default",
					SessionID:  "session123",
					Response:   "Hello! How can I help you?",
					TokensUsed: intPtr(50),
				})
			},
			wantErr:      false,
			wantResponse: "Hello! How can I help you?",
		},
		{
			name: "with system prompt",
			request: &perplexityapi.CreateMessageRequest{
				Model:     "sonar-pro",
				MaxTokens: 1024,
				System:    "You are a helpful assistant",
				Messages: []perplexityapi.Message{
					{
						Role: "user",
						Content: []perplexityapi.Content{
							{Type: "text", Text: "Test"},
						},
					},
				},
			},
			serverResponse: func(w http.ResponseWriter, r *http.Request) {
				var chatReq ChatRequest
				json.NewDecoder(r.Body).Decode(&chatReq)

				if chatReq.SystemPrompt == nil {
					t.Error("SystemPrompt should be set")
				}
				if *chatReq.SystemPrompt != "You are a helpful assistant" {
					t.Errorf("Expected system prompt 'You are a helpful assistant', got '%s'", *chatReq.SystemPrompt)
				}

				w.WriteHeader(http.StatusOK)
				json.NewEncoder(w).Encode(ChatResponse{
					PortalID:  "default",
					SessionID: "session123",
					Response:  "Response",
				})
			},
			wantErr:      false,
			wantResponse: "Response",
		},
		{
			name: "empty message",
			request: &perplexityapi.CreateMessageRequest{
				Model:     "sonar",
				MaxTokens: 1024,
				Messages:  []perplexityapi.Message{},
			},
			serverResponse: func(w http.ResponseWriter, r *http.Request) {
				t.Error("Should not reach server with empty message")
			},
			wantErr: true,
		},
		{
			name: "server error",
			request: &perplexityapi.CreateMessageRequest{
				Model:     "sonar",
				MaxTokens: 1024,
				Messages: []perplexityapi.Message{
					{
						Role: "user",
						Content: []perplexityapi.Content{
							{Type: "text", Text: "Hello"},
						},
					},
				},
			},
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

			client := NewMessageClient(server.URL, 5*time.Minute, zerolog.Nop())
			// Add API key to context for validation
			ctx := WithUserCredentials(context.Background(), "", "pplx-test-key-123")

			resp, err := client.CreateMessage(ctx, tt.request)

			if (err != nil) != tt.wantErr {
				t.Errorf("CreateMessage() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if !tt.wantErr {
				if len(resp.Content) == 0 {
					t.Error("Expected non-empty content")
				} else if resp.Content[0].Text != tt.wantResponse {
					t.Errorf("Response = %s, want %s", resp.Content[0].Text, tt.wantResponse)
				}
				if resp.Role != "assistant" {
					t.Errorf("Expected role 'assistant', got '%s'", resp.Role)
				}
				if resp.Model != tt.request.Model {
					t.Errorf("Model = %s, want %s", resp.Model, tt.request.Model)
				}
			}
		})
	}
}

func TestCreateMessageWithPortalID(t *testing.T) {
	receivedPortalID := ""

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var chatReq ChatRequest
		json.NewDecoder(r.Body).Decode(&chatReq)
		receivedPortalID = chatReq.PortalID

		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(ChatResponse{
			PortalID:  chatReq.PortalID,
			SessionID: "session123",
			Response:  "Response",
		})
	}))
	defer server.Close()

	client := NewMessageClient(server.URL, 5*time.Minute, zerolog.Nop())
	// Add both portal ID and API key to context
	ctx := WithPortalID(context.Background(), "custom-portal-123")
	ctx = WithUserCredentials(ctx, "", "pplx-test-key-123")

	req := &perplexityapi.CreateMessageRequest{
		Model:     "sonar",
		MaxTokens: 1024,
		Messages: []perplexityapi.Message{
			{
				Role: "user",
				Content: []perplexityapi.Content{
					{Type: "text", Text: "Hello"},
				},
			},
		},
	}

	_, err := client.CreateMessage(ctx, req)
	if err != nil {
		t.Fatalf("CreateMessage() error = %v", err)
	}

	if receivedPortalID != "custom-portal-123" {
		t.Errorf("Expected portal ID 'custom-portal-123', got '%s'", receivedPortalID)
	}
}

func TestCreateMessageStream(t *testing.T) {
	tests := []struct {
		name           string
		request        *perplexityapi.CreateMessageRequest
		serverResponse func(w http.ResponseWriter, r *http.Request)
		wantErr        bool
		wantEvents     int
		checkEvents    func(t *testing.T, events []perplexityapi.StreamEvent)
	}{
		{
			name: "successful stream",
			request: &perplexityapi.CreateMessageRequest{
				Model:     "sonar",
				MaxTokens: 1024,
				Messages: []perplexityapi.Message{
					{
						Role: "user",
						Content: []perplexityapi.Content{
							{Type: "text", Text: "Hello Perplexity"},
						},
					},
				},
			},
			serverResponse: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
				json.NewEncoder(w).Encode(ChatResponse{
					PortalID:   "default",
					SessionID:  "session123",
					Response:   "Hello! How can I help?",
					TokensUsed: intPtr(50),
				})
			},
			wantErr:    false,
			wantEvents: 4, // message_start, content_block_delta, message_delta, message_stop
			checkEvents: func(t *testing.T, events []perplexityapi.StreamEvent) {
				if len(events) != 4 {
					t.Fatalf("Expected 4 events, got %d", len(events))
				}

				if events[0].Type != "message_start" {
					t.Errorf("First event should be message_start, got %s", events[0].Type)
				}

				if events[1].Type != "content_block_delta" {
					t.Errorf("Second event should be content_block_delta, got %s", events[1].Type)
				}
				if events[1].Delta == nil || events[1].Delta.Text != "Hello! How can I help?" {
					t.Error("content_block_delta should contain response text")
				}

				if events[2].Type != "message_delta" {
					t.Errorf("Third event should be message_delta, got %s", events[2].Type)
				}
				if events[2].Usage == nil {
					t.Error("message_delta should contain usage")
				}

				if events[3].Type != "message_stop" {
					t.Errorf("Fourth event should be message_stop, got %s", events[3].Type)
				}
			},
		},
		{
			name: "empty message error",
			request: &perplexityapi.CreateMessageRequest{
				Model:     "sonar",
				MaxTokens: 1024,
				Messages:  []perplexityapi.Message{},
			},
			serverResponse: func(w http.ResponseWriter, r *http.Request) {
				t.Error("Should not reach server")
			},
			wantErr:    false, // Stream creation succeeds, error comes through channel
			wantEvents: 1,     // error event
			checkEvents: func(t *testing.T, events []perplexityapi.StreamEvent) {
				if len(events) != 1 {
					t.Fatalf("Expected 1 error event, got %d", len(events))
				}
				if events[0].Type != "error" {
					t.Errorf("Expected error event, got %s", events[0].Type)
				}
				if events[0].Error == nil {
					t.Error("Error event should have error details")
				}
			},
		},
		{
			name: "sidecar error",
			request: &perplexityapi.CreateMessageRequest{
				Model:     "sonar",
				MaxTokens: 1024,
				Messages: []perplexityapi.Message{
					{
						Role: "user",
						Content: []perplexityapi.Content{
							{Type: "text", Text: "Hello"},
						},
					},
				},
			},
			serverResponse: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusInternalServerError)
				w.Write([]byte("internal error"))
			},
			wantErr:    false,
			wantEvents: 2, // message_start, error
			checkEvents: func(t *testing.T, events []perplexityapi.StreamEvent) {
				if len(events) < 1 {
					t.Fatal("Expected at least 1 event")
				}
				// Last event should be error
				lastEvent := events[len(events)-1]
				if lastEvent.Type != "error" {
					t.Errorf("Expected error event, got %s", lastEvent.Type)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(tt.serverResponse))
			defer server.Close()

			client := NewMessageClient(server.URL, 5*time.Minute, zerolog.Nop())
			// Add API key to context for validation
			ctx := WithUserCredentials(context.Background(), "", "pplx-test-key-123")

			eventChan, err := client.CreateMessageStream(ctx, tt.request)

			if (err != nil) != tt.wantErr {
				t.Errorf("CreateMessageStream() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if err == nil {
				// Collect all events
				var events []perplexityapi.StreamEvent
				for event := range eventChan {
					events = append(events, event)
				}

				if tt.checkEvents != nil {
					tt.checkEvents(t, events)
				}
			}
		})
	}
}

func TestClearSession(t *testing.T) {
	called := false

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		if r.Method != "DELETE" {
			t.Errorf("Expected DELETE method, got %s", r.Method)
		}
		if r.URL.Path != "/v1/sessions/portal123" {
			t.Errorf("Expected path '/v1/sessions/portal123', got %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := NewMessageClient(server.URL, 5*time.Minute, zerolog.Nop())
	ctx := context.Background()

	err := client.ClearSession(ctx, "portal123")
	if err != nil {
		t.Errorf("ClearSession() error = %v", err)
	}

	if !called {
		t.Error("Server was not called")
	}
}

func TestGetSessionStats(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			t.Errorf("Expected GET method, got %s", r.Method)
		}
		if r.URL.Path != "/v1/sessions/portal123" {
			t.Errorf("Expected path '/v1/sessions/portal123', got %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(SessionStats{
			SessionID:    "session456",
			PortalID:     "portal123",
			CreatedAt:    1234567890.0,
			LastUsed:     1234567900.0,
			MessageCount: 5,
			AgeSeconds:   100.0,
		})
	}))
	defer server.Close()

	client := NewMessageClient(server.URL, 5*time.Minute, zerolog.Nop())
	ctx := context.Background()

	stats, err := client.GetSessionStats(ctx, "portal123")
	if err != nil {
		t.Fatalf("GetSessionStats() error = %v", err)
	}

	if stats == nil {
		t.Fatal("Expected non-nil stats")
	}

	if stats.SessionID != "session456" {
		t.Errorf("SessionID = %s, want session456", stats.SessionID)
	}
	if stats.MessageCount != 5 {
		t.Errorf("MessageCount = %d, want 5", stats.MessageCount)
	}
}

func TestExtractMessageText(t *testing.T) {
	tests := []struct {
		name     string
		messages []perplexityapi.Message
		want     string
	}{
		{
			name: "single user message",
			messages: []perplexityapi.Message{
				{
					Role: "user",
					Content: []perplexityapi.Content{
						{Type: "text", Text: "Hello"},
					},
				},
			},
			want: "Hello",
		},
		{
			name: "multiple messages, extract last user",
			messages: []perplexityapi.Message{
				{
					Role: "user",
					Content: []perplexityapi.Content{
						{Type: "text", Text: "First message"},
					},
				},
				{
					Role: "assistant",
					Content: []perplexityapi.Content{
						{Type: "text", Text: "Response"},
					},
				},
				{
					Role: "user",
					Content: []perplexityapi.Content{
						{Type: "text", Text: "Second message"},
					},
				},
			},
			want: "Second message",
		},
		{
			name: "no user messages",
			messages: []perplexityapi.Message{
				{
					Role: "assistant",
					Content: []perplexityapi.Content{
						{Type: "text", Text: "Response"},
					},
				},
			},
			want: "",
		},
		{
			name: "empty content",
			messages: []perplexityapi.Message{
				{
					Role:    "user",
					Content: []perplexityapi.Content{},
				},
			},
			want: "",
		},
		{
			name: "non-text content",
			messages: []perplexityapi.Message{
				{
					Role: "user",
					Content: []perplexityapi.Content{
						{Type: "image"},
					},
				},
			},
			want: "",
		},
		{
			name: "multiple content blocks",
			messages: []perplexityapi.Message{
				{
					Role: "user",
					Content: []perplexityapi.Content{
						{Type: "image"},
						{Type: "text", Text: "Describe this"},
					},
				},
			},
			want: "Describe this",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractMessageText(tt.messages)
			if got != tt.want {
				t.Errorf("extractMessageText() = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestEstimateTokens(t *testing.T) {
	tests := []struct {
		name string
		text string
		want int
	}{
		{
			name: "empty string",
			text: "",
			want: 0,
		},
		{
			name: "short text",
			text: "Hi",
			want: 0, // 2 chars / 4 = 0.5 -> 0
		},
		{
			name: "exact 4 chars",
			text: "test",
			want: 1,
		},
		{
			name: "16 chars",
			text: "Hello, world!!!",
			want: 3, // 15 chars / 4 = 3.75 -> 3
		},
		{
			name: "longer text",
			text: "This is a longer piece of text that should result in more tokens.",
			want: 16, // 67 chars / 4 = 16.75 -> 16
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := estimateTokens(tt.text)
			if got != tt.want {
				t.Errorf("estimateTokens() = %d, want %d (text length: %d)", got, tt.want, len(tt.text))
			}
		})
	}
}

func TestWithPortalID(t *testing.T) {
	ctx := context.Background()

	// Without portal ID
	if val := ctx.Value(portalIDKey); val != nil {
		t.Error("Expected nil portal ID in base context")
	}

	// With portal ID
	ctx = WithPortalID(ctx, "test-portal-123")
	val := ctx.Value(portalIDKey)

	if val == nil {
		t.Error("Expected non-nil portal ID")
	}

	portalID, ok := val.(string)
	if !ok {
		t.Error("Portal ID should be string type")
	}

	if portalID != "test-portal-123" {
		t.Errorf("Expected portal ID 'test-portal-123', got '%s'", portalID)
	}
}

func TestMessageClientImplementsInterface(t *testing.T) {
	// Compile-time check that MessageClient implements perplexityapi.MessageClient
	var _ perplexityapi.MessageClient = (*MessageClient)(nil)
}

func TestCreateMessageMetrics(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(ChatResponse{
			PortalID:   "default",
			SessionID:  "session123",
			Response:   "Test response",
			TokensUsed: intPtr(100),
		})
	}))
	defer server.Close()

	client := NewMessageClient(server.URL, 5*time.Minute, zerolog.Nop())
	// Add API key to context for validation
	ctx := WithUserCredentials(context.Background(), "", "pplx-test-key-123")

	req := &perplexityapi.CreateMessageRequest{
		Model:     "sonar",
		MaxTokens: 1024,
		Messages: []perplexityapi.Message{
			{
				Role: "user",
				Content: []perplexityapi.Content{
					{Type: "text", Text: "Hello"},
				},
			},
		},
	}

	metrics := client.GetMetrics()
	initialRequests := metrics.TotalRequests.Load()
	initialSuccessful := metrics.SuccessfulRequests.Load()

	_, err := client.CreateMessage(ctx, req)
	if err != nil {
		t.Fatalf("CreateMessage() error = %v", err)
	}

	// CreateMessage increments TotalRequests once at start and RecordRequest increments it again
	// So we expect +2 total
	expectedRequests := initialRequests + 2
	if metrics.TotalRequests.Load() != expectedRequests {
		t.Errorf("Total requests = %d, want %d", metrics.TotalRequests.Load(), expectedRequests)
	}

	expectedSuccessful := initialSuccessful + 1
	if metrics.SuccessfulRequests.Load() != expectedSuccessful {
		t.Errorf("Successful requests = %d, want %d", metrics.SuccessfulRequests.Load(), expectedSuccessful)
	}
}

func TestCreateMessageErrorMetrics(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("error"))
	}))
	defer server.Close()

	client := NewMessageClient(server.URL, 5*time.Minute, zerolog.Nop())
	ctx := context.Background()

	req := &perplexityapi.CreateMessageRequest{
		Model:     "sonar",
		MaxTokens: 1024,
		Messages: []perplexityapi.Message{
			{
				Role: "user",
				Content: []perplexityapi.Content{
					{Type: "text", Text: "Hello"},
				},
			},
		},
	}

	metrics := client.GetMetrics()
	initialFailed := metrics.FailedRequests.Load()

	_, err := client.CreateMessage(ctx, req)
	if err == nil {
		t.Fatal("Expected error from CreateMessage")
	}

	if metrics.FailedRequests.Load() != initialFailed+1 {
		t.Error("Failed requests should have increased by 1")
	}
}

// Benchmark tests
func BenchmarkCreateMessage(b *testing.B) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(ChatResponse{
			PortalID:  "default",
			SessionID: "session123",
			Response:  "Response",
		})
	}))
	defer server.Close()

	client := NewMessageClient(server.URL, 5*time.Minute, zerolog.Nop())
	ctx := context.Background()

	req := &perplexityapi.CreateMessageRequest{
		Model:     "sonar",
		MaxTokens: 1024,
		Messages: []perplexityapi.Message{
			{
				Role: "user",
				Content: []perplexityapi.Content{
					{Type: "text", Text: "Hello"},
				},
			},
		},
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = client.CreateMessage(ctx, req)
	}
}

func BenchmarkExtractMessageText(b *testing.B) {
	messages := []perplexityapi.Message{
		{
			Role: "user",
			Content: []perplexityapi.Content{
				{Type: "text", Text: "Hello"},
			},
		},
		{
			Role: "assistant",
			Content: []perplexityapi.Content{
				{Type: "text", Text: "Hi there!"},
			},
		},
		{
			Role: "user",
			Content: []perplexityapi.Content{
				{Type: "text", Text: "How are you?"},
			},
		},
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = extractMessageText(messages)
	}
}

func BenchmarkEstimateTokens(b *testing.B) {
	text := "This is a sample text that we will use to benchmark the token estimation function."

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = estimateTokens(text)
	}
}
