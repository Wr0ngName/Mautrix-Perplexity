// Package connector provides tests for login flows.
package connector

import (
	"context"
	"strings"
	"testing"

	"github.com/rs/zerolog"
	"maunium.net/go/mautrix/bridgev2"
)

func TestAPIKeyLoginStart(t *testing.T) {
	t.Run("returns user input step", func(t *testing.T) {
		connector := &PerplexityConnector{
			Log: zerolog.Nop(),
		}

		login := &APIKeyLogin{
			Connector: connector,
		}

		step, err := login.Start(context.Background())

		if err != nil {
			t.Fatalf("Start failed: %v", err)
		}

		if step == nil {
			t.Fatal("Start returned nil step")
		}

		if step.Type != bridgev2.LoginStepTypeUserInput {
			t.Errorf("expected LoginStepTypeUserInput, got %v", step.Type)
		}

		if step.StepID != "api_key" {
			t.Errorf("expected StepID 'api_key', got %q", step.StepID)
		}

		if step.Instructions == "" {
			t.Error("Instructions should not be empty")
		}

		if step.UserInputParams == nil {
			t.Fatal("UserInputParams should not be nil")
		}

		if len(step.UserInputParams.Fields) == 0 {
			t.Fatal("should have at least one input field")
		}

		// Check for API key field
		hasAPIKeyField := false
		for _, field := range step.UserInputParams.Fields {
			if field.ID == "api_key" {
				hasAPIKeyField = true

				if field.Type != bridgev2.LoginInputFieldTypePassword {
					t.Error("API key field should be password type")
				}

				if field.Name == "" {
					t.Error("field Name should not be empty")
				}
			}
		}

		if !hasAPIKeyField {
			t.Error("should have 'api_key' input field")
		}
	})
}

func TestAPIKeyLoginSubmitUserInput(t *testing.T) {
	tests := []struct {
		name        string
		input       map[string]string
		expectError bool
		errorMsg    string
	}{
		{
			name: "valid API key format but fails without user context",
			input: map[string]string{
				"api_key": "pplx-valid-key-123",
			},
			expectError: true,
			errorMsg:    "user not set", // Valid format but test setup doesn't have full context
		},
		{
			name: "rejects invalid API key prefix",
			input: map[string]string{
				"api_key": "invalid-key-123",
			},
			expectError: true,
			errorMsg:    "invalid API key format",
		},
		{
			name: "rejects empty API key",
			input: map[string]string{
				"api_key": "",
			},
			expectError: true,
			errorMsg:    "invalid API key format",
		},
		{
			name:        "rejects missing API key",
			input:       map[string]string{},
			expectError: true,
			errorMsg:    "invalid API key format",
		},
		{
			name: "rejects API key with wrong format",
			input: map[string]string{
				"api_key": "sk-wrong-format",
			},
			expectError: true,
			errorMsg:    "invalid API key format",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			connector := &PerplexityConnector{
				Log: zerolog.Nop(),
			}

			login := &APIKeyLogin{
				Connector: connector,
			}

			_, err := login.SubmitUserInput(context.Background(), tt.input)

			if tt.expectError {
				if err == nil {
					t.Error("expected error, got nil")
				} else if tt.errorMsg != "" && !strings.Contains(err.Error(), tt.errorMsg) {
					t.Errorf("expected error containing %q, got %q", tt.errorMsg, err.Error())
				}
			}

			// Note: We can't test successful login without a real API
			// The implementation should validate the API key with Perplexity API
		})
	}
}

func TestAPIKeyValidation(t *testing.T) {
	tests := []struct {
		name    string
		apiKey  string
		isValid bool
	}{
		{
			name:    "valid API key with pplx- prefix",
			apiKey:  "pplx-valid-key-123",
			isValid: true,
		},
		{
			name:    "valid API key with longer suffix",
			apiKey:  "pplx-abcdefghijklmnopqrstuvwxyz123456",
			isValid: true,
		},
		{
			name:    "invalid prefix",
			apiKey:  "invalid-key",
			isValid: false,
		},
		{
			name:    "empty key",
			apiKey:  "",
			isValid: false,
		},
		{
			name:    "only prefix",
			apiKey:  "pplx-",
			isValid: false,
		},
		{
			name:    "wrong case",
			apiKey:  "PPLX-VALID-KEY",
			isValid: false,
		},
		{
			name:    "anthropic key format",
			apiKey:  "sk-ant-api03-test",
			isValid: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			isValid := isValidAPIKeyFormat(tt.apiKey)

			if isValid != tt.isValid {
				t.Errorf("expected isValidAPIKeyFormat(%q) = %v, got %v", tt.apiKey, tt.isValid, isValid)
			}
		})
	}
}

func TestCreateLogin(t *testing.T) {
	t.Run("creates API key login", func(t *testing.T) {
		connector := &PerplexityConnector{
			Log: zerolog.Nop(),
		}

		// Mock user (we just need non-nil)
		user := &bridgev2.User{}

		loginProcess, err := connector.CreateLogin(context.Background(), user, "api_key")

		if err != nil {
			t.Fatalf("CreateLogin failed: %v", err)
		}

		if loginProcess == nil {
			t.Fatal("CreateLogin returned nil")
		}

		// Should be APIKeyLogin type
		if _, ok := loginProcess.(*APIKeyLogin); !ok {
			t.Error("CreateLogin should return *APIKeyLogin")
		}
	})

	t.Run("rejects unknown flow ID", func(t *testing.T) {
		connector := &PerplexityConnector{
			Log: zerolog.Nop(),
		}

		user := &bridgev2.User{}

		_, err := connector.CreateLogin(context.Background(), user, "unknown_flow")

		if err == nil {
			t.Error("expected error for unknown flow ID")
		}
	})

	t.Run("does not support password login", func(t *testing.T) {
		connector := &PerplexityConnector{
			Log: zerolog.Nop(),
		}

		user := &bridgev2.User{}

		_, err := connector.CreateLogin(context.Background(), user, "password")

		if err == nil {
			t.Error("should not support password login")
		}
	})

	t.Run("does not support sidecar login", func(t *testing.T) {
		connector := &PerplexityConnector{
			Log: zerolog.Nop(),
		}

		user := &bridgev2.User{}

		_, err := connector.CreateLogin(context.Background(), user, "sidecar")

		if err == nil {
			t.Error("should not support sidecar login flow directly")
		}
	})
}

func TestAPIKeyStorage(t *testing.T) {
	t.Run("API key is stored in metadata", func(t *testing.T) {
		apiKey := "pplx-test-key-123"

		meta := &UserLoginMetadata{
			APIKey: apiKey,
		}

		if meta.APIKey != apiKey {
			t.Error("API key should be stored in metadata")
		}
	})
}

func TestLoginSecurity(t *testing.T) {
	t.Run("API key should not be logged", func(t *testing.T) {
		// This is a documentation test
		// Implementation should ensure API keys are never logged
		apiKey := "pplx-secret-key-123"

		// When logging errors or info, API key should be redacted
		// This test documents the requirement
		_ = apiKey
	})
}
