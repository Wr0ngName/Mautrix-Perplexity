package connector

import (
	"fmt"
	"strings"
)

// DefaultTemperature is the default temperature when not specified.
const DefaultTemperature = 1.0

// ErrorMessagePrefix is the emoji/prefix used for error messages sent to Matrix rooms.
// This provides visual distinction for error notices.
const ErrorMessagePrefix = "⚠️ "

// Input validation limits to prevent abuse and excessive API costs.
const (
	// MaxMessageLength is the maximum allowed message length in characters.
	// Claude models support ~100k tokens, but we limit to prevent abuse.
	MaxMessageLength = 100000

	// MaxModelIDLength is the maximum length for model identifiers.
	MaxModelIDLength = 100

	// MinRateLimitPerMinute is the minimum rate limit to prevent abuse.
	// Setting to 0 in config means "use default", not "unlimited".
	MinRateLimitPerMinute = 1

	// DefaultRateLimitPerMinute is used when rate limit is not set or set to 0.
	DefaultRateLimitPerMinute = 60
)

// Config contains the configuration for the Claude connector.
type Config struct {
	// DefaultModel is the default Claude model to use
	DefaultModel string `yaml:"default_model"`

	// MaxTokens is the maximum tokens for responses
	MaxTokens int `yaml:"max_tokens"`

	// Temperature controls randomness (0.0-1.0)
	// Using a pointer allows distinguishing between "not set" and "set to 0"
	Temperature *float64 `yaml:"temperature,omitempty"`

	// SystemPrompt is the default system prompt
	SystemPrompt string `yaml:"system_prompt"`

	// ConversationMaxAge is the maximum conversation age in hours (0 = unlimited)
	ConversationMaxAge int `yaml:"conversation_max_age_hours"`

	// RateLimitPerMinute is the rate limit (0 = unlimited)
	RateLimitPerMinute int `yaml:"rate_limit_per_minute"`

	// Sidecar configuration for Pro/Max subscription support
	Sidecar SidecarConfig `yaml:"sidecar"`
}

// SidecarConfig contains configuration for the Agent SDK sidecar.
type SidecarConfig struct {
	// Enabled enables the sidecar backend instead of direct API
	// When enabled, uses Pro/Max subscription via Agent SDK instead of API credits
	Enabled bool `yaml:"enabled"`

	// URL is the sidecar service URL (default: http://localhost:8090)
	URL string `yaml:"url"`

	// Timeout is the request timeout in seconds (default: 300)
	Timeout int `yaml:"timeout"`
}

// Default sidecar configuration values.
const (
	// DefaultSidecarURL is the default URL for the sidecar service.
	DefaultSidecarURL = "http://localhost:8090"
	// DefaultSidecarTimeout is the default timeout for sidecar requests in seconds.
	DefaultSidecarTimeout = 300
)

// GetURL returns the sidecar URL, using the default if not set.
func (c *SidecarConfig) GetURL() string {
	if c.URL == "" {
		return DefaultSidecarURL
	}
	return c.URL
}

// GetTimeout returns the sidecar timeout in seconds, using the default if not set.
func (c *SidecarConfig) GetTimeout() int {
	if c.Timeout <= 0 {
		return DefaultSidecarTimeout
	}
	return c.Timeout
}

// ExampleConfig is the example configuration for the connector.
// Note: Do NOT add section headers here - framework adds "network:" wrapper automatically.
const ExampleConfig = `# Default Claude model to use
# Use family names (sonnet, opus, haiku) to automatically use the latest version
# Or specify a full model ID for a specific version
# Run the "models" command after login to see all available models
default_model: sonnet

# Maximum tokens for responses (depends on model, typically 4096-64000)
max_tokens: 4096

# Temperature controls randomness (0.0-1.0, default 1.0)
# Lower = more focused and deterministic
# Higher = more creative and varied
# Set to 0 for most deterministic responses
temperature: 1.0

# Default system prompt (can be overridden per room)
system_prompt: "You are a helpful AI assistant."

# Maximum conversation age in hours (0 = unlimited)
# Older conversations will be cleared from context
conversation_max_age_hours: 24

# Rate limiting (requests per minute, 0 = unlimited)
# Helps prevent API rate limit errors
rate_limit_per_minute: 60

# Sidecar mode for Pro/Max subscription support
# When enabled, uses the Claude Agent SDK sidecar instead of direct API
# This allows using Pro/Max subscriptions instead of API credits
sidecar:
    enabled: false
    # URL of the sidecar service (default: http://localhost:8090)
    url: "http://localhost:8090"
    # Request timeout in seconds (default: 300)
    timeout: 300
`

// Validate validates the configuration.
// Note: Model validation is done at runtime via API, not at config load time.
func (c *Config) Validate() error {
	// Allow family names (sonnet, opus, haiku) or full Claude model IDs
	if c.DefaultModel != "" {
		model := strings.ToLower(c.DefaultModel)
		isFamily := model == "sonnet" || model == "opus" || model == "haiku"
		isClaudeModel := strings.Contains(model, "claude")
		if !isFamily && !isClaudeModel {
			return fmt.Errorf("invalid model format: %s (must be a family name like 'sonnet' or a Claude model ID)", c.DefaultModel)
		}
	}

	// Validate temperature if set
	if c.Temperature != nil {
		if *c.Temperature < 0 || *c.Temperature > 1 {
			return fmt.Errorf("temperature must be between 0 and 1, got %f", *c.Temperature)
		}
	}

	// Validate max tokens
	if c.MaxTokens < 0 {
		return fmt.Errorf("max_tokens must be non-negative, got %d", c.MaxTokens)
	}

	// Check against reasonable max (models vary, but 128k is a safe upper bound)
	if c.MaxTokens > 128000 {
		return fmt.Errorf("max_tokens (%d) exceeds reasonable maximum (128000)", c.MaxTokens)
	}

	// Validate conversation max age
	if c.ConversationMaxAge < 0 {
		return fmt.Errorf("conversation_max_age_hours must be non-negative, got %d", c.ConversationMaxAge)
	}

	// Validate rate limit
	if c.RateLimitPerMinute < 0 {
		return fmt.Errorf("rate_limit_per_minute must be non-negative, got %d", c.RateLimitPerMinute)
	}

	return nil
}

// GetDefaultModel returns the configured default model.
// This may be a family name (sonnet, opus, haiku) that needs resolution.
func (c *Config) GetDefaultModel() string {
	if c.DefaultModel == "" {
		return "sonnet" // Default to latest sonnet
	}
	return c.DefaultModel
}

// IsModelFamily checks if a model string is a family name that needs resolution.
func IsModelFamily(model string) bool {
	switch strings.ToLower(model) {
	case "sonnet", "opus", "haiku", "claude-sonnet", "claude-opus", "claude-haiku":
		return true
	}
	return false
}

// GetModelFamily extracts the family name from a model string.
func GetModelFamilyName(model string) string {
	model = strings.ToLower(model)
	switch model {
	case "sonnet", "claude-sonnet":
		return "sonnet"
	case "opus", "claude-opus":
		return "opus"
	case "haiku", "claude-haiku":
		return "haiku"
	}
	return ""
}

// GetMaxTokens returns the max tokens, using a default if not set.
func (c *Config) GetMaxTokens() int {
	if c.MaxTokens == 0 {
		return 4096
	}
	return c.MaxTokens
}

// GetTemperature returns the temperature, using a default if not set.
// This correctly handles the case where temperature is explicitly set to 0.
func (c *Config) GetTemperature() float64 {
	if c.Temperature == nil {
		return DefaultTemperature
	}
	return *c.Temperature
}

// GetSystemPrompt returns the system prompt, using a default if not set.
func (c *Config) GetSystemPrompt() string {
	if c.SystemPrompt == "" {
		return "You are a helpful AI assistant."
	}
	return c.SystemPrompt
}

// GetRateLimitPerMinute returns the rate limit, enforcing a minimum.
// A configured value of 0 means "use default", not "unlimited".
func (c *Config) GetRateLimitPerMinute() int {
	if c.RateLimitPerMinute <= 0 {
		return DefaultRateLimitPerMinute
	}
	if c.RateLimitPerMinute < MinRateLimitPerMinute {
		return MinRateLimitPerMinute
	}
	return c.RateLimitPerMinute
}

// ValidateMessageLength checks if a message is within allowed limits.
func ValidateMessageLength(msg string) error {
	if len(msg) > MaxMessageLength {
		return fmt.Errorf("message too long: %d characters (max %d)", len(msg), MaxMessageLength)
	}
	return nil
}

// ValidateModelID checks if a model ID is valid.
func ValidateModelID(modelID string) error {
	if len(modelID) > MaxModelIDLength {
		return fmt.Errorf("model ID too long: %d characters (max %d)", len(modelID), MaxModelIDLength)
	}
	if modelID != "" && !strings.Contains(strings.ToLower(modelID), "claude") {
		return fmt.Errorf("invalid model ID format: must be a Claude model")
	}
	return nil
}

// TemperaturePtr is a helper to create a pointer to a float64.
// Useful for setting temperature in config.
func TemperaturePtr(t float64) *float64 {
	return &t
}
