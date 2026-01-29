// Package connector provides the Matrix bridge connector for Perplexity API.
package connector

import (
	"context"
	"fmt"
	"time"

	"github.com/rs/zerolog"
	"go.mau.fi/util/configupgrade"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/commands"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"

	"go.mau.fi/mautrix-perplexity/pkg/perplexityapi"
	"go.mau.fi/mautrix-perplexity/pkg/sidecar"
)

// PerplexityConnector implements the bridgev2.NetworkConnector interface for Perplexity API.
type PerplexityConnector struct {
	br     *bridgev2.Bridge
	Config Config
	Log    zerolog.Logger
}

var (
	_ bridgev2.NetworkConnector            = (*PerplexityConnector)(nil)
	_ bridgev2.MaxFileSizeingNetwork       = (*PerplexityConnector)(nil)
	_ bridgev2.IdentifierValidatingNetwork = (*PerplexityConnector)(nil)
	_ bridgev2.ConfigValidatingNetwork     = (*PerplexityConnector)(nil)
)

// NewConnector creates a new Perplexity API bridge connector.
func NewConnector() *PerplexityConnector {
	return &PerplexityConnector{}
}

// Init initializes the connector with the bridge.
func (c *PerplexityConnector) Init(bridge *bridgev2.Bridge) {
	c.br = bridge
	c.Log = bridge.Log.With().Str("connector", "perplexity").Logger()
}

// Start starts the connector.
func (c *PerplexityConnector) Start(ctx context.Context) error {
	c.Log.Info().Msg("Perplexity API connector starting")

	// Log loaded config
	c.Log.Info().
		Str("default_model", c.Config.GetDefaultModel()).
		Int("max_tokens", c.Config.GetMaxTokens()).
		Float64("temperature", c.Config.GetTemperature()).
		Str("system_prompt_preview", truncateString(c.Config.GetSystemPrompt(), 50)).
		Int("conversation_max_age_hours", c.Config.ConversationMaxAge).
		Int("rate_limit_per_minute", c.Config.GetRateLimitPerMinute()).
		Str("sidecar_url", c.Config.Sidecar.GetURL()).
		Msg("Loaded connector config")

	// Validate sidecar connectivity (required for Perplexity)
	sidecarURL := c.Config.Sidecar.GetURL()
	sidecarTimeout := c.Config.Sidecar.GetTimeout()
	c.Log.Info().Str("url", sidecarURL).Msg("Checking sidecar connectivity")
	client := sidecar.NewClient(sidecarURL, time.Duration(sidecarTimeout)*time.Second, c.Log)
	health, err := client.Health(ctx)
	if err != nil {
		c.Log.Warn().Err(err).Msg("Sidecar health check failed - login will not be available until sidecar is running")
	} else {
		c.Log.Info().
			Str("status", health.Status).
			Int("sessions", health.Sessions).
			Msg("Sidecar is healthy")
	}

	// Register custom commands
	if proc, ok := c.br.Commands.(*commands.Processor); ok {
		c.RegisterCommands(proc)
		c.Log.Debug().Msg("Registered custom bridge commands")
	}

	return nil
}

// truncateString truncates a string to maxLen runes (not bytes).
func truncateString(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return string(runes[:maxLen]) + "..."
}

// getSidecarClient returns a sidecar MessageClient for the connector.
func (c *PerplexityConnector) getSidecarClient() perplexityapi.MessageClient {
	return sidecar.NewMessageClient(
		c.Config.Sidecar.GetURL(),
		time.Duration(c.Config.Sidecar.GetTimeout())*time.Second,
		c.Log,
	)
}

// GetName returns the name of the network.
func (c *PerplexityConnector) GetName() bridgev2.BridgeName {
	return bridgev2.BridgeName{
		DisplayName:      "Perplexity AI",
		NetworkURL:       "https://www.perplexity.ai",
		NetworkIcon:      "mxc://maunium.net/perplexity",
		NetworkID:        "perplexity",
		BeeperBridgeType: "go.mau.fi/mautrix-perplexity",
		DefaultPort:      29321,
	}
}

// GetDBMetaTypes returns the database meta types for the connector.
func (c *PerplexityConnector) GetDBMetaTypes() database.MetaTypes {
	return database.MetaTypes{
		Ghost:     func() any { return &GhostMetadata{} },
		Message:   func() any { return &MessageMetadata{} },
		Portal:    func() any { return &PortalMetadata{} },
		Reaction:  nil,
		UserLogin: func() any { return &UserLoginMetadata{} },
	}
}

// GetCapabilities returns the capabilities of the connector.
func (c *PerplexityConnector) GetCapabilities() *bridgev2.NetworkGeneralCapabilities {
	return &bridgev2.NetworkGeneralCapabilities{
		DisappearingMessages: false,
		AggressiveUpdateInfo: false,
	}
}

// GetBridgeInfoVersion returns version numbers for bridge info and room capabilities.
func (c *PerplexityConnector) GetBridgeInfoVersion() (info, capabilities int) {
	return 1, 1
}

// GetConfig returns the connector configuration.
func (c *PerplexityConnector) GetConfig() (example string, data any, upgrader configupgrade.Upgrader) {
	return ExampleConfig, &c.Config, configupgrade.SimpleUpgrader(upgradeConfig)
}

// upgradeConfig copies config values from the user's config file to the base config.
func upgradeConfig(helper configupgrade.Helper) {
	helper.Copy(configupgrade.Str, "default_model")
	helper.Copy(configupgrade.Int, "max_tokens")
	helper.Copy(configupgrade.Float, "temperature")
	helper.Copy(configupgrade.Str, "system_prompt")
	helper.Copy(configupgrade.Int, "conversation_max_age_hours")
	helper.Copy(configupgrade.Int, "rate_limit_per_minute")
	helper.Copy(configupgrade.Str, "sidecar", "url")
	helper.Copy(configupgrade.Int, "sidecar", "timeout")
}

// ValidateConfig validates the loaded configuration.
func (c *PerplexityConnector) ValidateConfig() error {
	return c.Config.Validate()
}

// SetMaxFileSize sets the maximum file size for uploads.
func (c *PerplexityConnector) SetMaxFileSize(maxSize int64) {
	// Perplexity supports images
}

// GetLoginFlows returns the available login flows.
func (c *PerplexityConnector) GetLoginFlows() []bridgev2.LoginFlow {
	return []bridgev2.LoginFlow{
		{
			Name:        "API Key",
			Description: "Log in with your Perplexity API key from perplexity.ai/settings/api",
			ID:          "api_key",
		},
	}
}

// CreateLogin creates a new login handler.
func (c *PerplexityConnector) CreateLogin(ctx context.Context, user *bridgev2.User, flowID string) (bridgev2.LoginProcess, error) {
	switch flowID {
	case "api_key":
		return &APIKeyLogin{
			User:      user,
			Connector: c,
		}, nil
	default:
		return nil, fmt.Errorf("unknown login flow: %s", flowID)
	}
}

// LoadUserLogin loads an existing user login.
func (c *PerplexityConnector) LoadUserLogin(ctx context.Context, login *bridgev2.UserLogin) error {
	metadata, ok := login.Metadata.(*UserLoginMetadata)
	if !ok || metadata == nil {
		c.Log.Error().
			Bool("type_assertion_ok", ok).
			Interface("metadata_type", fmt.Sprintf("%T", login.Metadata)).
			Msg("Failed to cast user login metadata to expected type")
		return fmt.Errorf("invalid user login metadata")
	}

	log := c.Log.With().Str("user", string(login.UserMXID)).Logger()

	if metadata.APIKey == "" {
		return fmt.Errorf("no API key found in login metadata")
	}

	log.Info().Msg("Initializing Perplexity client via sidecar")
	messageClient := sidecar.NewMessageClient(
		c.Config.Sidecar.GetURL(),
		time.Duration(c.Config.Sidecar.GetTimeout())*time.Second,
		log,
	)

	perplexityClient := &PerplexityClient{
		MessageClient: messageClient,
		UserLogin:     login,
		Connector:     c,
		rateLimiter:   NewRateLimiter(c.Config.GetRateLimitPerMinute()),
	}

	login.Client = perplexityClient

	return nil
}

// GhostMetadata contains Perplexity-specific ghost user metadata.
type GhostMetadata struct {
	Model string `json:"model"` // Which Perplexity model this "ghost" represents
}

// MessageMetadata contains Perplexity-specific message metadata.
type MessageMetadata struct {
	PerplexityMessageID string `json:"perplexity_message_id"`
	TokensUsed          int    `json:"tokens_used"`
}

// PortalMetadata contains Perplexity-specific portal/room metadata.
type PortalMetadata struct {
	ConversationName string   `json:"conversation_name"`
	Model            string   `json:"model"`                   // Selected model for this room
	SystemPrompt     string   `json:"system_prompt,omitempty"` // Custom system prompt
	Temperature      *float64 `json:"temperature,omitempty"`   // Custom temperature
	MentionOnly      bool     `json:"mention_only,omitempty"`  // Only respond when mentioned
}

// GetTemperature returns the temperature for this portal, or the default if not set.
func (p *PortalMetadata) GetTemperature(defaultTemp float64) float64 {
	if p.Temperature == nil {
		return defaultTemp
	}
	temp := *p.Temperature
	if temp < 0 || temp > 2 {
		return defaultTemp
	}
	return temp
}

// UserLoginMetadata contains Perplexity-specific user login metadata.
type UserLoginMetadata struct {
	APIKey string `json:"api_key"`
}

// ValidateUserID validates that a user ID is a valid Perplexity ghost ID.
func (c *PerplexityConnector) ValidateUserID(id networkid.UserID) bool {
	switch string(id) {
	case "sonar", "sonar-pro", "sonar-reasoning", "sonar-reasoning-pro", "error":
		return true
	default:
		return false
	}
}

// MakePerplexityGhostID creates a network user ID from a model name.
func (c *PerplexityConnector) MakePerplexityGhostID(model string) networkid.UserID {
	family := perplexityapi.GetModelFamily(model)
	if family == "" {
		// Fallback to default model's family from config
		defaultModel := c.Config.GetDefaultModel()
		family = perplexityapi.GetModelFamily(defaultModel)
		if family == "" {
			// Last resort: default to sonar
			family = "sonar"
			c.Log.Warn().
				Str("model", model).
				Str("default_model", defaultModel).
				Msg("Could not determine model family, defaulting to sonar")
		}
	}
	return networkid.UserID(family)
}

// MakePerplexityPortalKey creates a portal key from a conversation identifier.
func MakePerplexityPortalKey(conversationID string) networkid.PortalKey {
	return networkid.PortalKey{
		ID: networkid.PortalID(conversationID),
	}
}

// MakePerplexityMessageID creates a message ID from a Perplexity message ID.
func MakePerplexityMessageID(messageID string) networkid.MessageID {
	return networkid.MessageID(messageID)
}
