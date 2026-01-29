package connector

import (
	"testing"

	"go.mau.fi/mautrix-perplexity/pkg/perplexityapi"
)

// Note: Sidecar is now mandatory for the Perplexity bridge, so there's no Enabled field to test.

func TestSidecarConstants(t *testing.T) {
	if DefaultSidecarURL != "http://localhost:8090" {
		t.Errorf("DefaultSidecarURL = %s, want http://localhost:8090", DefaultSidecarURL)
	}

	if DefaultSidecarTimeout != 300 {
		t.Errorf("DefaultSidecarTimeout = %d, want 300", DefaultSidecarTimeout)
	}
}

func TestSidecarConfigGetters(t *testing.T) {
	t.Run("URL defaults when empty", func(t *testing.T) {
		config := SidecarConfig{}
		if config.GetURL() != DefaultSidecarURL {
			t.Errorf("GetURL() = %s, want %s", config.GetURL(), DefaultSidecarURL)
		}
	})

	t.Run("URL returns custom value when set", func(t *testing.T) {
		config := SidecarConfig{URL: "http://custom:9090"}
		if config.GetURL() != "http://custom:9090" {
			t.Errorf("GetURL() = %s, want http://custom:9090", config.GetURL())
		}
	})

	t.Run("Timeout defaults when zero", func(t *testing.T) {
		config := SidecarConfig{}
		if config.GetTimeout() != DefaultSidecarTimeout {
			t.Errorf("GetTimeout() = %d, want %d", config.GetTimeout(), DefaultSidecarTimeout)
		}
	})

	t.Run("Timeout returns custom value when set", func(t *testing.T) {
		config := SidecarConfig{Timeout: 600}
		if config.GetTimeout() != 600 {
			t.Errorf("GetTimeout() = %d, want 600", config.GetTimeout())
		}
	})
}

func TestConfigValidate(t *testing.T) {
	tests := []struct {
		name    string
		config  Config
		wantErr bool
	}{
		{
			name: "valid config",
			config: Config{
				DefaultModel:       "sonar",
				MaxTokens:          4096,
				Temperature:        floatPtr(1.0),
				SystemPrompt:       "You are helpful",
				ConversationMaxAge: 24,
				RateLimitPerMinute: 60,
				Sidecar:            SidecarConfig{},
			},
			wantErr: false,
		},
		{
			name: "valid config with sonar-pro",
			config: Config{
				DefaultModel:       "sonar-pro",
				MaxTokens:          1024,
				Temperature:        nil,
				ConversationMaxAge: 0,
				RateLimitPerMinute: 0,
				Sidecar:            SidecarConfig{},
			},
			wantErr: false,
		},
		{
			name: "temperature too low",
			config: Config{
				DefaultModel: "sonar",
				Temperature:  floatPtr(-0.1),
			},
			wantErr: true,
		},
		{
			name: "temperature too high for Perplexity (max 2.0)",
			config: Config{
				DefaultModel: "sonar",
				Temperature:  floatPtr(2.1),
			},
			wantErr: true,
		},
		{
			name: "temperature zero is valid",
			config: Config{
				DefaultModel: "sonar",
				Temperature:  floatPtr(0.0),
			},
			wantErr: false,
		},
		{
			name: "temperature 2.0 is valid for Perplexity",
			config: Config{
				DefaultModel: "sonar",
				Temperature:  floatPtr(2.0),
			},
			wantErr: false,
		},
		{
			name: "negative max tokens",
			config: Config{
				DefaultModel: "sonar",
				MaxTokens:    -1,
			},
			wantErr: true,
		},
		{
			name: "excessive max tokens",
			config: Config{
				DefaultModel: "sonar",
				MaxTokens:    200001,
			},
			wantErr: true,
		},
		{
			name: "negative conversation age",
			config: Config{
				DefaultModel:       "sonar",
				ConversationMaxAge: -1,
			},
			wantErr: true,
		},
		{
			name: "negative rate limit",
			config: Config{
				DefaultModel:       "sonar",
				RateLimitPerMinute: -1,
			},
			wantErr: true,
		},
		{
			name: "invalid model format",
			config: Config{
				DefaultModel: "gpt-4",
			},
			wantErr: true,
		},
		{
			name: "empty model is valid (will use default)",
			config: Config{
				DefaultModel: "",
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestGetDefaultModel(t *testing.T) {
	tests := []struct {
		name   string
		config Config
		want   string
	}{
		{
			name:   "has default model",
			config: Config{DefaultModel: "sonar-pro"},
			want:   "sonar-pro",
		},
		{
			name:   "empty default model uses sonar",
			config: Config{DefaultModel: ""},
			want:   perplexityapi.DefaultModel,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.config.GetDefaultModel()
			if got != tt.want {
				t.Errorf("GetDefaultModel() = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestIsModelFamily(t *testing.T) {
	tests := []struct {
		name  string
		model string
		want  bool
	}{
		{name: "sonar", model: "sonar", want: true},
		{name: "sonar-pro", model: "sonar-pro", want: true},
		{name: "sonar-reasoning", model: "sonar-reasoning", want: true},
		{name: "sonar-reasoning-pro", model: "sonar-reasoning-pro", want: true},
		{name: "uppercase sonar", model: "SONAR", want: true},
		{name: "unknown model", model: "unknown", want: false},
		{name: "empty", model: "", want: false},
		{name: "non-perplexity model", model: "gpt-4", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsModelFamily(tt.model)
			if got != tt.want {
				t.Errorf("IsModelFamily(%s) = %v, want %v", tt.model, got, tt.want)
			}
		})
	}
}

func TestGetModelFamilyName(t *testing.T) {
	tests := []struct {
		name  string
		model string
		want  string
	}{
		{name: "sonar", model: "sonar", want: "sonar"},
		{name: "sonar-pro", model: "sonar-pro", want: "sonar-pro"},
		{name: "sonar-reasoning", model: "sonar-reasoning", want: "sonar-reasoning"},
		{name: "sonar-reasoning-pro", model: "sonar-reasoning-pro", want: "sonar-reasoning-pro"},
		{name: "uppercase", model: "SONAR", want: "sonar"},
		{name: "unknown defaults to sonar", model: "unknown", want: "sonar"},
		{name: "empty defaults to sonar", model: "", want: "sonar"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := GetModelFamilyName(tt.model)
			if got != tt.want {
				t.Errorf("GetModelFamilyName(%s) = %s, want %s", tt.model, got, tt.want)
			}
		})
	}
}

func TestGetMaxTokens(t *testing.T) {
	tests := []struct {
		name   string
		config Config
		want   int
	}{
		{
			name:   "has max tokens",
			config: Config{MaxTokens: 8192},
			want:   8192,
		},
		{
			name:   "zero max tokens uses default",
			config: Config{MaxTokens: 0},
			want:   4096,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.config.GetMaxTokens()
			if got != tt.want {
				t.Errorf("GetMaxTokens() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestGetTemperature(t *testing.T) {
	tests := []struct {
		name   string
		config Config
		want   float64
	}{
		{
			name:   "has temperature",
			config: Config{Temperature: floatPtr(0.5)},
			want:   0.5,
		},
		{
			name:   "temperature zero should return 0",
			config: Config{Temperature: floatPtr(0.0)},
			want:   0.0,
		},
		{
			name:   "nil temperature uses default",
			config: Config{Temperature: nil},
			want:   DefaultTemperature,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.config.GetTemperature()
			if got != tt.want {
				t.Errorf("GetTemperature() = %f, want %f", got, tt.want)
			}
		})
	}
}

func TestGetSystemPrompt(t *testing.T) {
	tests := []struct {
		name   string
		config Config
		want   string
	}{
		{
			name:   "has system prompt",
			config: Config{SystemPrompt: "Custom prompt"},
			want:   "Custom prompt",
		},
		{
			name:   "empty system prompt uses default",
			config: Config{SystemPrompt: ""},
			want:   "You are a helpful AI assistant.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.config.GetSystemPrompt()
			if got != tt.want {
				t.Errorf("GetSystemPrompt() = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestGetRateLimitPerMinute(t *testing.T) {
	tests := []struct {
		name   string
		config Config
		want   int
	}{
		{
			name:   "has rate limit",
			config: Config{RateLimitPerMinute: 100},
			want:   100,
		},
		{
			name:   "zero rate limit uses default",
			config: Config{RateLimitPerMinute: 0},
			want:   DefaultRateLimitPerMinute,
		},
		{
			name:   "below minimum uses minimum",
			config: Config{RateLimitPerMinute: 0},
			want:   DefaultRateLimitPerMinute,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.config.GetRateLimitPerMinute()
			if got != tt.want {
				t.Errorf("GetRateLimitPerMinute() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestValidateMessageLength(t *testing.T) {
	tests := []struct {
		name    string
		message string
		wantErr bool
	}{
		{
			name:    "empty message",
			message: "",
			wantErr: false,
		},
		{
			name:    "short message",
			message: "Hello, world!",
			wantErr: false,
		},
		{
			name:    "max length message",
			message: string(make([]byte, MaxMessageLength)),
			wantErr: false,
		},
		{
			name:    "too long message",
			message: string(make([]byte, MaxMessageLength+1)),
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateMessageLength(tt.message)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateMessageLength() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestValidateModelID(t *testing.T) {
	tests := []struct {
		name    string
		modelID string
		wantErr bool
	}{
		{
			name:    "valid model id sonar",
			modelID: "sonar",
			wantErr: false,
		},
		{
			name:    "valid model id sonar-pro",
			modelID: "sonar-pro",
			wantErr: false,
		},
		{
			name:    "valid model id sonar-reasoning",
			modelID: "sonar-reasoning",
			wantErr: false,
		},
		{
			name:    "empty model id",
			modelID: "",
			wantErr: false, // Empty is allowed, will use default
		},
		{
			name:    "invalid model id (not perplexity)",
			modelID: "gpt-4",
			wantErr: true,
		},
		{
			name:    "too long model id",
			modelID: string(make([]byte, MaxModelIDLength+1)),
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateModelID(tt.modelID)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateModelID() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestTemperaturePtr(t *testing.T) {
	temp := 0.7
	ptr := TemperaturePtr(temp)

	if ptr == nil {
		t.Fatal("TemperaturePtr returned nil")
	}

	if *ptr != temp {
		t.Errorf("TemperaturePtr() = %f, want %f", *ptr, temp)
	}
}

func TestDefaultTemperature(t *testing.T) {
	if DefaultTemperature != 1.0 {
		t.Errorf("DefaultTemperature = %f, want 1.0", DefaultTemperature)
	}
}

func TestConfigConstants(t *testing.T) {
	// Verify constants are set to expected values
	tests := []struct {
		name  string
		value int
		want  int
	}{
		{"MaxMessageLength", MaxMessageLength, 100000},
		{"MaxModelIDLength", MaxModelIDLength, 100},
		{"MinRateLimitPerMinute", MinRateLimitPerMinute, 1},
		{"DefaultRateLimitPerMinute", DefaultRateLimitPerMinute, 60},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.value != tt.want {
				t.Errorf("%s = %d, want %d", tt.name, tt.value, tt.want)
			}
		})
	}
}

func TestExampleConfig(t *testing.T) {
	if ExampleConfig == "" {
		t.Error("ExampleConfig should not be empty")
	}

	// Check that example config contains key sections
	requiredSections := []string{
		"default_model",
		"max_tokens",
		"temperature",
		"system_prompt",
		"conversation_max_age_hours",
		"rate_limit_per_minute",
		"sidecar",
	}

	for _, section := range requiredSections {
		if !contains(ExampleConfig, section) {
			t.Errorf("ExampleConfig missing section: %s", section)
		}
	}
}

func TestSidecarConfigInConfig(t *testing.T) {
	config := Config{
		DefaultModel: "sonar",
		Sidecar: SidecarConfig{
			URL:     "http://localhost:9090",
			Timeout: 120,
		},
	}

	// Sidecar is mandatory, so we just test that URL and Timeout are set correctly
	if config.Sidecar.URL != "http://localhost:9090" {
		t.Errorf("Sidecar URL should be http://localhost:9090, got %s", config.Sidecar.URL)
	}
	if config.Sidecar.Timeout != 120 {
		t.Errorf("Sidecar Timeout should be 120, got %d", config.Sidecar.Timeout)
	}
}

// Helper functions
func floatPtr(f float64) *float64 {
	return &f
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(substr) == 0 ||
		(len(s) > 0 && (s[0:len(substr)] == substr || contains(s[1:], substr))))
}
