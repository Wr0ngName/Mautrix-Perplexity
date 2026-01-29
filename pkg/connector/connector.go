// Package connector provides the Matrix bridge connector for Claude API.
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

	"go.mau.fi/mautrix-claude/pkg/claudeapi"
	"go.mau.fi/mautrix-claude/pkg/sidecar"
)

// ClaudeConnector implements the bridgev2.NetworkConnector interface for Claude API.
type ClaudeConnector struct {
	br     *bridgev2.Bridge
	Config Config
	Log    zerolog.Logger
}

var (
	_ bridgev2.NetworkConnector            = (*ClaudeConnector)(nil)
	_ bridgev2.MaxFileSizeingNetwork       = (*ClaudeConnector)(nil)
	_ bridgev2.IdentifierValidatingNetwork = (*ClaudeConnector)(nil)
	_ bridgev2.ConfigValidatingNetwork     = (*ClaudeConnector)(nil)
)

// NewConnector creates a new Claude API bridge connector.
func NewConnector() *ClaudeConnector {
	return &ClaudeConnector{}
}

// Init initializes the connector with the bridge.
func (c *ClaudeConnector) Init(bridge *bridgev2.Bridge) {
	c.br = bridge
	c.Log = bridge.Log.With().Str("connector", "claude").Logger()
}

// Start starts the connector.
func (c *ClaudeConnector) Start(ctx context.Context) error {
	c.Log.Info().Msg("Claude API connector starting")

	// Log loaded config (Info level to always show sidecar status)
	c.Log.Info().
		Str("default_model", c.Config.GetDefaultModel()).
		Int("max_tokens", c.Config.GetMaxTokens()).
		Float64("temperature", c.Config.GetTemperature()).
		Str("system_prompt_preview", truncateString(c.Config.GetSystemPrompt(), 50)).
		Int("conversation_max_age_hours", c.Config.ConversationMaxAge).
		Int("rate_limit_per_minute", c.Config.GetRateLimitPerMinute()).
		Bool("sidecar_enabled", c.Config.Sidecar.Enabled).
		Msg("Loaded connector config")

	// Validate sidecar connectivity if enabled
	if c.Config.Sidecar.Enabled {
		sidecarURL := c.Config.Sidecar.GetURL()
		sidecarTimeout := c.Config.Sidecar.GetTimeout()
		c.Log.Info().Str("url", sidecarURL).Msg("Sidecar mode enabled, checking connectivity")
		client := sidecar.NewClient(sidecarURL, time.Duration(sidecarTimeout)*time.Second, c.Log)
		health, err := client.Health(ctx)
		if err != nil {
			c.Log.Warn().Err(err).Msg("Sidecar health check failed - Pro/Max login will not be available")
			// Don't fail startup - API key login can still work, and sidecar might come up later
		} else if !health.Authenticated {
			c.Log.Warn().
				Str("status", health.Status).
				Int("sessions", health.Sessions).
				Msg("Sidecar running but Claude Code not authenticated - Pro/Max login will fail until credentials are configured")
		} else {
			c.Log.Info().
				Str("status", health.Status).
				Int("sessions", health.Sessions).
				Bool("authenticated", health.Authenticated).
				Msg("Sidecar is healthy and authenticated")
		}
	}

	// Register custom commands
	if proc, ok := c.br.Commands.(*commands.Processor); ok {
		c.RegisterCommands(proc)
		c.Log.Debug().Msg("Registered custom bridge commands")
	}

	return nil
}

// truncateString truncates a string to maxLen runes (not bytes).
// This ensures proper UTF-8 handling and won't split multi-byte characters.
func truncateString(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return string(runes[:maxLen]) + "..."
}

// getSidecarClient returns a sidecar MessageClient for the connector.
func (c *ClaudeConnector) getSidecarClient() claudeapi.MessageClient {
	return sidecar.NewMessageClient(
		c.Config.Sidecar.GetURL(),
		time.Duration(c.Config.Sidecar.GetTimeout())*time.Second,
		c.Log,
	)
}

// GetName returns the name of the network.
func (c *ClaudeConnector) GetName() bridgev2.BridgeName {
	return bridgev2.BridgeName{
		DisplayName:      "Claude AI",
		NetworkURL:       "https://console.anthropic.com",
		NetworkIcon:      "mxc://maunium.net/claude",
		NetworkID:        "claude",
		BeeperBridgeType: "go.mau.fi/mautrix-claude",
		DefaultPort:      29320,
	}
}

// GetDBMetaTypes returns the database meta types for the connector.
func (c *ClaudeConnector) GetDBMetaTypes() database.MetaTypes {
	return database.MetaTypes{
		Ghost:     func() any { return &GhostMetadata{} },
		Message:   func() any { return &MessageMetadata{} },
		Portal:    func() any { return &PortalMetadata{} },
		Reaction:  nil,
		UserLogin: func() any { return &UserLoginMetadata{} },
	}
}

// GetCapabilities returns the capabilities of the connector.
func (c *ClaudeConnector) GetCapabilities() *bridgev2.NetworkGeneralCapabilities {
	return &bridgev2.NetworkGeneralCapabilities{
		DisappearingMessages: false,
		AggressiveUpdateInfo: false,
	}
}

// GetBridgeInfoVersion returns version numbers for bridge info and room capabilities.
func (c *ClaudeConnector) GetBridgeInfoVersion() (info, capabilities int) {
	return 1, 1
}

// GetConfig returns the connector configuration.
func (c *ClaudeConnector) GetConfig() (example string, data any, upgrader configupgrade.Upgrader) {
	return ExampleConfig, &c.Config, configupgrade.SimpleUpgrader(upgradeConfig)
}

// upgradeConfig copies config values from the user's config file to the base config.
// This ensures user values are preserved when the config file is updated.
func upgradeConfig(helper configupgrade.Helper) {
	helper.Copy(configupgrade.Str, "default_model")
	helper.Copy(configupgrade.Int, "max_tokens")
	helper.Copy(configupgrade.Float, "temperature")
	helper.Copy(configupgrade.Str, "system_prompt")
	helper.Copy(configupgrade.Int, "conversation_max_age_hours")
	helper.Copy(configupgrade.Int, "rate_limit_per_minute")
	helper.Copy(configupgrade.Bool, "sidecar", "enabled")
	helper.Copy(configupgrade.Str, "sidecar", "url")
	helper.Copy(configupgrade.Int, "sidecar", "timeout")
}

// ValidateConfig validates the loaded configuration.
func (c *ClaudeConnector) ValidateConfig() error {
	return c.Config.Validate()
}

// SetMaxFileSize sets the maximum file size for uploads.
func (c *ClaudeConnector) SetMaxFileSize(maxSize int64) {
	// Claude API supports images up to 20MB
}

// GetLoginFlows returns the available login flows.
func (c *ClaudeConnector) GetLoginFlows() []bridgev2.LoginFlow {
	flows := []bridgev2.LoginFlow{
		{
			Name:        "API Key",
			Description: "Log in with your own Claude API key from console.anthropic.com",
			ID:          "api_key",
		},
	}

	// Add sidecar option when enabled
	if c.Config.Sidecar.Enabled {
		flows = append([]bridgev2.LoginFlow{
			{
				Name:        "Pro/Max Subscription",
				Description: "Use the bridge's shared Claude Pro/Max subscription (no API key needed)",
				ID:          "sidecar",
			},
		}, flows...)
	}

	return flows
}

// CreateLogin creates a new login handler.
func (c *ClaudeConnector) CreateLogin(ctx context.Context, user *bridgev2.User, flowID string) (bridgev2.LoginProcess, error) {
	switch flowID {
	case "api_key":
		return &APIKeyLogin{
			User:      user,
			Connector: c,
		}, nil
	case "sidecar":
		return &SidecarLogin{
			User:      user,
			Connector: c,
		}, nil
	default:
		return nil, fmt.Errorf("unknown login flow: %s", flowID)
	}
}

// LoadUserLogin loads an existing user login.
func (c *ClaudeConnector) LoadUserLogin(ctx context.Context, login *bridgev2.UserLogin) error {
	metadata, ok := login.Metadata.(*UserLoginMetadata)
	if !ok || metadata == nil {
		c.Log.Error().
			Bool("type_assertion_ok", ok).
			Interface("metadata_type", fmt.Sprintf("%T", login.Metadata)).
			Msg("Failed to cast user login metadata to expected type")
		return fmt.Errorf("invalid user login metadata")
	}

	log := c.Log.With().Str("user", string(login.UserMXID)).Logger()

	var messageClient claudeapi.MessageClient

	// Choose backend based on login type (credentials present), not global config
	if metadata.CredentialsJSON != "" && c.Config.Sidecar.Enabled {
		// Sidecar login with Pro/Max credentials
		log.Info().Msg("Using sidecar backend for Pro/Max subscription")
		messageClient = sidecar.NewMessageClient(
			c.Config.Sidecar.GetURL(),
			time.Duration(c.Config.Sidecar.GetTimeout())*time.Second,
			log,
		)
	} else if metadata.APIKey != "" {
		// API key login
		log.Info().Msg("Using direct API backend")
		messageClient = claudeapi.NewClient(metadata.APIKey, log)
	} else if metadata.CredentialsJSON != "" && !c.Config.Sidecar.Enabled {
		return fmt.Errorf("sidecar login but sidecar is disabled in config")
	} else {
		return fmt.Errorf("no API key or credentials found in login metadata")
	}

	claudeClient := &ClaudeClient{
		MessageClient: messageClient,
		UserLogin:     login,
		Connector:     c,
		conversations: make(map[networkid.PortalID]*claudeapi.ConversationManager),
		rateLimiter:   NewRateLimiter(c.Config.GetRateLimitPerMinute()),
	}

	login.Client = claudeClient

	return nil
}

// GhostMetadata contains Claude-specific ghost user metadata.
type GhostMetadata struct {
	Model string `json:"model"` // Which Claude model this "ghost" represents
}

// MessageMetadata contains Claude-specific message metadata.
type MessageMetadata struct {
	ClaudeMessageID string `json:"claude_message_id"`
	TokensUsed      int    `json:"tokens_used"`
}

// PortalMetadata contains Claude-specific portal/room metadata.
type PortalMetadata struct {
	ConversationName string   `json:"conversation_name"`
	Model            string   `json:"model"`                        // Selected model for this room
	SystemPrompt     string   `json:"system_prompt,omitempty"`      // Custom system prompt
	Temperature      *float64 `json:"temperature,omitempty"`        // Custom temperature
	MentionOnly      bool     `json:"mention_only,omitempty"`       // Only respond when mentioned
	SidecarSessionID string   `json:"sidecar_session_id,omitempty"` // Agent SDK session ID for resume
}

// GetTemperature returns the temperature for this portal, or the default if not set.
func (p *PortalMetadata) GetTemperature(defaultTemp float64) float64 {
	if p.Temperature == nil {
		return defaultTemp
	}
	temp := *p.Temperature
	if temp < 0 || temp > 1 {
		return defaultTemp
	}
	return temp
}

// UserLoginMetadata contains Claude-specific user login metadata.
type UserLoginMetadata struct {
	APIKey          string `json:"api_key"`
	Email           string `json:"email,omitempty"`
	CredentialsJSON string `json:"credentials_json,omitempty"` // For Pro/Max sidecar mode
}

// ValidateUserID validates that a user ID is a valid Claude ghost ID.
// This is called by the framework during ghost DM invite handling.
func (c *ClaudeConnector) ValidateUserID(id networkid.UserID) bool {
	switch string(id) {
	case "sonnet", "opus", "haiku", "error":
		return true
	default:
		return false
	}
}

// MakeClaudeGhostID creates a network user ID from a model name.
// Returns just the family name (e.g., "sonnet", "opus", "haiku") since the
// username_template in config already adds the "claude_" prefix.
// Uses the default model from config as fallback if family cannot be determined.
func (c *ClaudeConnector) MakeClaudeGhostID(model string) networkid.UserID {
	family := claudeapi.GetModelFamily(model)
	if family == "" {
		// Fallback to default model's family from config
		defaultModel := c.Config.GetDefaultModel()
		family = claudeapi.GetModelFamily(defaultModel)
		if family == "" {
			// Last resort: default to sonnet if even config model is unrecognizable
			family = "sonnet"
			c.Log.Warn().
				Str("model", model).
				Str("default_model", defaultModel).
				Msg("Could not determine model family, defaulting to sonnet")
		}
	}
	return networkid.UserID(family)
}

// MakeClaudePortalKey creates a portal key from a conversation identifier.
// Receiver is intentionally not set so that FindPreferredLogin works properly
// and users can switch between logins using set-preferred-login command.
func MakeClaudePortalKey(conversationID string) networkid.PortalKey {
	return networkid.PortalKey{
		ID: networkid.PortalID(conversationID),
	}
}

// MakeClaudeMessageID creates a message ID from a Claude message ID.
func MakeClaudeMessageID(messageID string) networkid.MessageID {
	return networkid.MessageID(messageID)
}
