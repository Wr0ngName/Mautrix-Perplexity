package connector

import (
	"fmt"
	"strings"

	"go.mau.fi/mautrix-perplexity/pkg/perplexityapi"
)

// DefaultTemperature is the default temperature when not specified.
const DefaultTemperature = 1.0

// ErrorMessagePrefix is the emoji/prefix used for error messages sent to Matrix rooms.
const ErrorMessagePrefix = "⚠️ "

// Input validation limits to prevent abuse and excessive API costs.
const (
	// MaxMessageLength is the maximum allowed message length in characters.
	MaxMessageLength = 100000

	// MaxModelIDLength is the maximum length for model identifiers.
	MaxModelIDLength = 100

	// MinRateLimitPerMinute is the minimum rate limit to prevent abuse.
	MinRateLimitPerMinute = 1

	// DefaultRateLimitPerMinute is used when rate limit is not set or set to 0.
	DefaultRateLimitPerMinute = 60
)

// Config contains the configuration for the Perplexity connector.
type Config struct {
	// DefaultModel is the default Perplexity model to use
	DefaultModel string `yaml:"default_model"`

	// MaxTokens is the maximum tokens for responses
	MaxTokens int `yaml:"max_tokens"`

	// Temperature controls randomness (0.0-2.0 for Perplexity)
	Temperature *float64 `yaml:"temperature,omitempty"`

	// SystemPrompt is the default system prompt
	SystemPrompt string `yaml:"system_prompt"`

	// ConversationMaxAge is the maximum conversation age in hours (0 = unlimited)
	ConversationMaxAge int `yaml:"conversation_max_age_hours"`

	// RateLimitPerMinute is the rate limit (0 = unlimited)
	RateLimitPerMinute int `yaml:"rate_limit_per_minute"`

	// Sidecar configuration for Perplexity SDK
	Sidecar SidecarConfig `yaml:"sidecar"`

	// WebSearchOptions contains default web search options
	WebSearchOptions *WebSearchConfig `yaml:"web_search_options,omitempty"`
}

// WebSearchConfig contains web search options for Perplexity.
type WebSearchConfig struct {
	// SearchDomainFilter limits search to specific domains
	SearchDomainFilter []string `yaml:"search_domain_filter,omitempty"`
	// SearchRecencyFilter limits search to recent results ("day", "week", "month", "year")
	SearchRecencyFilter string `yaml:"search_recency_filter,omitempty"`
}

// SidecarConfig contains configuration for the Perplexity SDK sidecar.
// The sidecar is mandatory for the Perplexity bridge as it uses the official Perplexity Python SDK.
type SidecarConfig struct {
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
const ExampleConfig = `# Default Perplexity model to use
# Available models: sonar, sonar-pro, sonar-reasoning, sonar-reasoning-pro
# Run the "models" command after login to see all available models
default_model: sonar

# Maximum tokens for responses (depends on model)
max_tokens: 4096

# Temperature controls randomness (0.0-2.0, default 1.0)
# Lower = more focused and deterministic
# Higher = more creative and varied
temperature: 1.0

# Default system prompt (can be overridden per room)
system_prompt: "You are a helpful AI assistant."

# Maximum conversation age in hours (0 = unlimited)
# Older conversations will be cleared from context
conversation_max_age_hours: 24

# Rate limiting (requests per minute, 0 = unlimited)
# Helps prevent API rate limit errors
rate_limit_per_minute: 60

# Perplexity-specific web search options (optional)
# web_search_options:
#     # Limit search to specific domains (e.g., ["wikipedia.org", "arxiv.org"])
#     search_domain_filter: []
#     # Limit search to recent results: "day", "week", "month", "year"
#     search_recency_filter: ""

# Sidecar configuration (mandatory for Perplexity bridge)
# The sidecar uses the official Perplexity Python SDK
sidecar:
    # URL of the sidecar service (default: http://localhost:8090)
    url: "http://localhost:8090"
    # Request timeout in seconds (default: 300)
    timeout: 300
`

// Validate validates the configuration.
func (c *Config) Validate() error {
	// Validate model - allow Perplexity model names
	if c.DefaultModel != "" {
		model := strings.ToLower(c.DefaultModel)
		if !perplexityapi.IsValidModel(model) && !IsModelFamily(model) {
			return fmt.Errorf("invalid model: %s (available: %s)", c.DefaultModel, strings.Join(perplexityapi.ModelFamilies, ", "))
		}
	}

	// Validate temperature if set (Perplexity allows 0-2)
	if c.Temperature != nil {
		if *c.Temperature < 0 || *c.Temperature > 2 {
			return fmt.Errorf("temperature must be between 0 and 2, got %f", *c.Temperature)
		}
	}

	// Validate max tokens
	if c.MaxTokens < 0 {
		return fmt.Errorf("max_tokens must be non-negative, got %d", c.MaxTokens)
	}

	// Check against reasonable max
	if c.MaxTokens > 200000 {
		return fmt.Errorf("max_tokens (%d) exceeds reasonable maximum (200000)", c.MaxTokens)
	}

	// Validate conversation max age
	if c.ConversationMaxAge < 0 {
		return fmt.Errorf("conversation_max_age_hours must be non-negative, got %d", c.ConversationMaxAge)
	}

	// Validate rate limit
	if c.RateLimitPerMinute < 0 {
		return fmt.Errorf("rate_limit_per_minute must be non-negative, got %d", c.RateLimitPerMinute)
	}

	// Validate web search options
	if c.WebSearchOptions != nil && c.WebSearchOptions.SearchRecencyFilter != "" {
		valid := map[string]bool{"day": true, "week": true, "month": true, "year": true}
		if !valid[c.WebSearchOptions.SearchRecencyFilter] {
			return fmt.Errorf("invalid search_recency_filter: %s (must be day, week, month, or year)", c.WebSearchOptions.SearchRecencyFilter)
		}
	}

	return nil
}

// GetDefaultModel returns the configured default model.
func (c *Config) GetDefaultModel() string {
	if c.DefaultModel == "" {
		return perplexityapi.DefaultModel
	}
	return c.DefaultModel
}

// IsModelFamily checks if a model string is a family name.
func IsModelFamily(model string) bool {
	model = strings.ToLower(model)
	for _, family := range perplexityapi.ModelFamilies {
		if model == family {
			return true
		}
	}
	return false
}

// GetModelFamilyName extracts the family name from a model string.
func GetModelFamilyName(model string) string {
	return perplexityapi.GetModelFamily(model)
}

// GetMaxTokens returns the max tokens, using a default if not set.
func (c *Config) GetMaxTokens() int {
	if c.MaxTokens == 0 {
		return 4096
	}
	return c.MaxTokens
}

// GetTemperature returns the temperature, using a default if not set.
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
	if modelID != "" && !perplexityapi.IsValidModel(modelID) {
		return fmt.Errorf("invalid model: %s (available: %s)", modelID, strings.Join(perplexityapi.ModelFamilies, ", "))
	}
	return nil
}

// TemperaturePtr is a helper to create a pointer to a float64.
func TemperaturePtr(t float64) *float64 {
	return &t
}
