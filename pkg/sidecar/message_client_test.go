package sidecar

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/rs/zerolog"

	"go.mau.fi/mautrix-claude/pkg/claudeapi"
)

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
		request        *claudeapi.CreateMessageRequest
		serverResponse func(w http.ResponseWriter, r *http.Request)
		wantErr        bool
		wantResponse   string
	}{
		{
			name: "successful message",
			request: &claudeapi.CreateMessageRequest{
				Model:     "claude-3-opus-20240229",
				MaxTokens: 1024,
				Messages: []claudeapi.Message{
					{
						Role: "user",
						Content: []claudeapi.Content{
							{Type: "text", Text: "Hello Claude"},
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
			request: &claudeapi.CreateMessageRequest{
				Model:     "claude-3-sonnet-20240229",
				MaxTokens: 1024,
				System:    "You are a helpful assistant",
				Messages: []claudeapi.Message{
					{
						Role: "user",
						Content: []claudeapi.Content{
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
			request: &claudeapi.CreateMessageRequest{
				Model:     "claude-3-opus-20240229",
				MaxTokens: 1024,
				Messages:  []claudeapi.Message{},
			},
			serverResponse: func(w http.ResponseWriter, r *http.Request) {
				t.Error("Should not reach server with empty message")
			},
			wantErr: true,
		},
		{
			name: "server error",
			request: &claudeapi.CreateMessageRequest{
				Model:     "claude-3-opus-20240229",
				MaxTokens: 1024,
				Messages: []claudeapi.Message{
					{
						Role: "user",
						Content: []claudeapi.Content{
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
			ctx := context.Background()

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
	ctx := WithPortalID(context.Background(), "custom-portal-123")

	req := &claudeapi.CreateMessageRequest{
		Model:     "claude-3-opus-20240229",
		MaxTokens: 1024,
		Messages: []claudeapi.Message{
			{
				Role: "user",
				Content: []claudeapi.Content{
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
		request        *claudeapi.CreateMessageRequest
		serverResponse func(w http.ResponseWriter, r *http.Request)
		wantErr        bool
		wantEvents     int
		checkEvents    func(t *testing.T, events []claudeapi.StreamEvent)
	}{
		{
			name: "successful stream",
			request: &claudeapi.CreateMessageRequest{
				Model:     "claude-3-opus-20240229",
				MaxTokens: 1024,
				Messages: []claudeapi.Message{
					{
						Role: "user",
						Content: []claudeapi.Content{
							{Type: "text", Text: "Hello Claude"},
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
			checkEvents: func(t *testing.T, events []claudeapi.StreamEvent) {
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
			request: &claudeapi.CreateMessageRequest{
				Model:     "claude-3-opus-20240229",
				MaxTokens: 1024,
				Messages:  []claudeapi.Message{},
			},
			serverResponse: func(w http.ResponseWriter, r *http.Request) {
				t.Error("Should not reach server")
			},
			wantErr:    false, // Stream creation succeeds, error comes through channel
			wantEvents: 1,     // error event
			checkEvents: func(t *testing.T, events []claudeapi.StreamEvent) {
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
			request: &claudeapi.CreateMessageRequest{
				Model:     "claude-3-opus-20240229",
				MaxTokens: 1024,
				Messages: []claudeapi.Message{
					{
						Role: "user",
						Content: []claudeapi.Content{
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
			checkEvents: func(t *testing.T, events []claudeapi.StreamEvent) {
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
			ctx := context.Background()

			eventChan, err := client.CreateMessageStream(ctx, tt.request)

			if (err != nil) != tt.wantErr {
				t.Errorf("CreateMessageStream() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if err == nil {
				// Collect all events
				var events []claudeapi.StreamEvent
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

func TestCreateMessageStreamMetrics(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(ChatResponse{
			PortalID:   "default",
			SessionID:  "session123",
			Response:   "Response text here",
			TokensUsed: intPtr(100),
		})
	}))
	defer server.Close()

	client := NewMessageClient(server.URL, 5*time.Minute, zerolog.Nop())
	ctx := context.Background()

	req := &claudeapi.CreateMessageRequest{
		Model:     "claude-3-opus-20240229",
		MaxTokens: 1024,
		Messages: []claudeapi.Message{
			{
				Role: "user",
				Content: []claudeapi.Content{
					{Type: "text", Text: "Hello"},
				},
			},
		},
	}

	metrics := client.GetMetrics()
	initialRequests := metrics.TotalRequests.Load()
	initialSuccessful := metrics.SuccessfulRequests.Load()

	eventChan, err := client.CreateMessageStream(ctx, req)
	if err != nil {
		t.Fatalf("CreateMessageStream() error = %v", err)
	}

	// Consume all events - this ensures the goroutine completes
	for range eventChan {
	}

	// The goroutine updates metrics after closing the channel, but there can be
	// a small race. Wait for metrics to be updated with a reasonable timeout.
	// CreateMessageStream increments TotalRequests once at start and RecordRequest increments it again
	expectedRequests := initialRequests + 2
	for i := 0; i < 100; i++ {
		if metrics.TotalRequests.Load() >= expectedRequests {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Check metrics were updated
	if metrics.TotalRequests.Load() != expectedRequests {
		t.Errorf("Total requests = %d, want %d", metrics.TotalRequests.Load(), expectedRequests)
	}

	expectedSuccessful := initialSuccessful + 1
	if metrics.SuccessfulRequests.Load() != expectedSuccessful {
		t.Errorf("Successful requests = %d, want %d", metrics.SuccessfulRequests.Load(), expectedSuccessful)
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
		messages []claudeapi.Message
		want     string
	}{
		{
			name: "single user message",
			messages: []claudeapi.Message{
				{
					Role: "user",
					Content: []claudeapi.Content{
						{Type: "text", Text: "Hello"},
					},
				},
			},
			want: "Hello",
		},
		{
			name: "multiple messages, extract last user",
			messages: []claudeapi.Message{
				{
					Role: "user",
					Content: []claudeapi.Content{
						{Type: "text", Text: "First message"},
					},
				},
				{
					Role: "assistant",
					Content: []claudeapi.Content{
						{Type: "text", Text: "Response"},
					},
				},
				{
					Role: "user",
					Content: []claudeapi.Content{
						{Type: "text", Text: "Second message"},
					},
				},
			},
			want: "Second message",
		},
		{
			name: "no user messages",
			messages: []claudeapi.Message{
				{
					Role: "assistant",
					Content: []claudeapi.Content{
						{Type: "text", Text: "Response"},
					},
				},
			},
			want: "",
		},
		{
			name: "empty content",
			messages: []claudeapi.Message{
				{
					Role:    "user",
					Content: []claudeapi.Content{},
				},
			},
			want: "",
		},
		{
			name: "non-text content",
			messages: []claudeapi.Message{
				{
					Role: "user",
					Content: []claudeapi.Content{
						{Type: "image"},
					},
				},
			},
			want: "",
		},
		{
			name: "multiple content blocks",
			messages: []claudeapi.Message{
				{
					Role: "user",
					Content: []claudeapi.Content{
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

func TestExtractMessageContent(t *testing.T) {
	tests := []struct {
		name        string
		messages    []claudeapi.Message
		wantText    string
		wantContent []ContentBlock
	}{
		{
			name: "text only - no content returned",
			messages: []claudeapi.Message{
				{
					Role: "user",
					Content: []claudeapi.Content{
						{Type: "text", Text: "Hello world"},
					},
				},
			},
			wantText:    "Hello world",
			wantContent: nil, // No content for text-only (backward compat)
		},
		{
			name: "image only",
			messages: []claudeapi.Message{
				{
					Role: "user",
					Content: []claudeapi.Content{
						{
							Type: "image",
							Source: &claudeapi.ImageSource{
								Type:      "base64",
								MediaType: "image/jpeg",
								Data:      "base64data",
							},
						},
					},
				},
			},
			wantText: "",
			wantContent: []ContentBlock{
				{
					Type: "image",
					Source: &ImageSource{
						Type:      "base64",
						MediaType: "image/jpeg",
						Data:      "base64data",
					},
				},
			},
		},
		{
			name: "image with text caption",
			messages: []claudeapi.Message{
				{
					Role: "user",
					Content: []claudeapi.Content{
						{
							Type: "image",
							Source: &claudeapi.ImageSource{
								Type:      "base64",
								MediaType: "image/png",
								Data:      "pngdata",
							},
						},
						{Type: "text", Text: "What's in this image?"},
					},
				},
			},
			wantText: "What's in this image?",
			wantContent: []ContentBlock{
				{
					Type: "image",
					Source: &ImageSource{
						Type:      "base64",
						MediaType: "image/png",
						Data:      "pngdata",
					},
				},
				{
					Type: "text",
					Text: "What's in this image?",
				},
			},
		},
		{
			name: "multiple images with text",
			messages: []claudeapi.Message{
				{
					Role: "user",
					Content: []claudeapi.Content{
						{
							Type: "image",
							Source: &claudeapi.ImageSource{
								Type:      "base64",
								MediaType: "image/jpeg",
								Data:      "img1data",
							},
						},
						{
							Type: "image",
							Source: &claudeapi.ImageSource{
								Type:      "base64",
								MediaType: "image/jpeg",
								Data:      "img2data",
							},
						},
						{Type: "text", Text: "Compare these two"},
					},
				},
			},
			wantText: "Compare these two",
			wantContent: []ContentBlock{
				{
					Type: "image",
					Source: &ImageSource{
						Type:      "base64",
						MediaType: "image/jpeg",
						Data:      "img1data",
					},
				},
				{
					Type: "image",
					Source: &ImageSource{
						Type:      "base64",
						MediaType: "image/jpeg",
						Data:      "img2data",
					},
				},
				{
					Type: "text",
					Text: "Compare these two",
				},
			},
		},
		{
			name: "image with nil source - ignored",
			messages: []claudeapi.Message{
				{
					Role: "user",
					Content: []claudeapi.Content{
						{Type: "image", Source: nil},
						{Type: "text", Text: "Some text"},
					},
				},
			},
			wantText:    "Some text",
			wantContent: nil, // No valid images, so nil content
		},
		{
			name: "empty message",
			messages: []claudeapi.Message{
				{
					Role:    "user",
					Content: []claudeapi.Content{},
				},
			},
			wantText:    "",
			wantContent: nil,
		},
		{
			name: "multiple text blocks",
			messages: []claudeapi.Message{
				{
					Role: "user",
					Content: []claudeapi.Content{
						{Type: "text", Text: "First part"},
						{Type: "text", Text: "Second part"},
					},
				},
			},
			wantText:    "First part\nSecond part",
			wantContent: nil, // No images, so nil content
		},
		{
			name: "text and image with multiple text blocks",
			messages: []claudeapi.Message{
				{
					Role: "user",
					Content: []claudeapi.Content{
						{Type: "text", Text: "Look at this:"},
						{
							Type: "image",
							Source: &claudeapi.ImageSource{
								Type:      "base64",
								MediaType: "image/gif",
								Data:      "gifdata",
							},
						},
						{Type: "text", Text: "And tell me what it shows"},
					},
				},
			},
			wantText: "Look at this:\nAnd tell me what it shows",
			wantContent: []ContentBlock{
				{Type: "text", Text: "Look at this:"},
				{
					Type: "image",
					Source: &ImageSource{
						Type:      "base64",
						MediaType: "image/gif",
						Data:      "gifdata",
					},
				},
				{Type: "text", Text: "And tell me what it shows"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotText, gotContent := extractMessageContent(tt.messages)

			if gotText != tt.wantText {
				t.Errorf("extractMessageContent() text = %q, want %q", gotText, tt.wantText)
			}

			if len(gotContent) != len(tt.wantContent) {
				t.Errorf("extractMessageContent() content length = %d, want %d", len(gotContent), len(tt.wantContent))
				return
			}

			for i, got := range gotContent {
				want := tt.wantContent[i]
				if got.Type != want.Type {
					t.Errorf("content[%d].Type = %q, want %q", i, got.Type, want.Type)
				}
				if got.Text != want.Text {
					t.Errorf("content[%d].Text = %q, want %q", i, got.Text, want.Text)
				}
				if (got.Source == nil) != (want.Source == nil) {
					t.Errorf("content[%d].Source nil mismatch", i)
				}
				if got.Source != nil && want.Source != nil {
					if got.Source.Type != want.Source.Type {
						t.Errorf("content[%d].Source.Type = %q, want %q", i, got.Source.Type, want.Source.Type)
					}
					if got.Source.MediaType != want.Source.MediaType {
						t.Errorf("content[%d].Source.MediaType = %q, want %q", i, got.Source.MediaType, want.Source.MediaType)
					}
					if got.Source.Data != want.Source.Data {
						t.Errorf("content[%d].Source.Data = %q, want %q", i, got.Source.Data, want.Source.Data)
					}
				}
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
	// Compile-time check that MessageClient implements claudeapi.MessageClient
	var _ claudeapi.MessageClient = (*MessageClient)(nil)
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
	ctx := context.Background()

	req := &claudeapi.CreateMessageRequest{
		Model:     "claude-3-opus-20240229",
		MaxTokens: 1024,
		Messages: []claudeapi.Message{
			{
				Role: "user",
				Content: []claudeapi.Content{
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

	req := &claudeapi.CreateMessageRequest{
		Model:     "claude-3-opus-20240229",
		MaxTokens: 1024,
		Messages: []claudeapi.Message{
			{
				Role: "user",
				Content: []claudeapi.Content{
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

	req := &claudeapi.CreateMessageRequest{
		Model:     "claude-3-opus-20240229",
		MaxTokens: 1024,
		Messages: []claudeapi.Message{
			{
				Role: "user",
				Content: []claudeapi.Content{
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
	messages := []claudeapi.Message{
		{
			Role: "user",
			Content: []claudeapi.Content{
				{Type: "text", Text: "Hello"},
			},
		},
		{
			Role: "assistant",
			Content: []claudeapi.Content{
				{Type: "text", Text: "Hi there!"},
			},
		},
		{
			Role: "user",
			Content: []claudeapi.Content{
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
