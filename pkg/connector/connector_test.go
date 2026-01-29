// Package connector provides tests for the Claude bridge connector.
package connector

import (
	"testing"

	"maunium.net/go/mautrix/bridgev2"
)

func TestNewConnector(t *testing.T) {
	t.Run("creates new connector", func(t *testing.T) {
		connector := NewConnector()

		if connector == nil {
			t.Fatal("NewConnector returned nil")
		}
	})
}

func TestGetName(t *testing.T) {
	t.Run("returns Claude bridge info", func(t *testing.T) {
		connector := NewConnector()
		name := connector.GetName()

		if name.DisplayName != "Claude AI" {
			t.Errorf("expected DisplayName 'Claude AI', got %q", name.DisplayName)
		}

		if name.NetworkID != "claude" {
			t.Errorf("expected NetworkID 'claude', got %q", name.NetworkID)
		}

		if name.NetworkURL == "" {
			t.Error("NetworkURL should not be empty")
		}

		if name.BeeperBridgeType == "" {
			t.Error("BeeperBridgeType should not be empty")
		}

		if name.DefaultPort == 0 {
			t.Error("DefaultPort should not be zero")
		}

		// Should be different from candy bridge port
		if name.DefaultPort == 29350 {
			t.Error("DefaultPort should be different from candy bridge")
		}
	})
}

func TestGetLoginFlows(t *testing.T) {
	t.Run("returns API key login flow", func(t *testing.T) {
		connector := NewConnector()
		flows := connector.GetLoginFlows()

		if len(flows) == 0 {
			t.Fatal("GetLoginFlows returned empty slice")
		}

		// Should have at least API key flow
		hasAPIKeyFlow := false
		for _, flow := range flows {
			if flow.ID == "api_key" {
				hasAPIKeyFlow = true

				if flow.Name == "" {
					t.Error("flow Name should not be empty")
				}

				if flow.Description == "" {
					t.Error("flow Description should not be empty")
				}
			}
		}

		if !hasAPIKeyFlow {
			t.Error("should have 'api_key' login flow")
		}
	})

	t.Run("does not have password or cookie flows", func(t *testing.T) {
		connector := NewConnector()
		flows := connector.GetLoginFlows()

		for _, flow := range flows {
			if flow.ID == "password" {
				t.Error("should not have password login flow")
			}
			if flow.ID == "cookie" {
				t.Error("should not have cookie login flow")
			}
		}
	})
}

func TestGetDBMetaTypes(t *testing.T) {
	t.Run("returns correct metadata types", func(t *testing.T) {
		connector := NewConnector()
		metaTypes := connector.GetDBMetaTypes()

		if metaTypes.Ghost == nil {
			t.Error("Ghost metadata constructor should not be nil")
		}

		if metaTypes.Message == nil {
			t.Error("Message metadata constructor should not be nil")
		}

		if metaTypes.Portal == nil {
			t.Error("Portal metadata constructor should not be nil")
		}

		if metaTypes.UserLogin == nil {
			t.Error("UserLogin metadata constructor should not be nil")
		}

		// Test constructors return correct types
		ghostMeta := metaTypes.Ghost()
		if _, ok := ghostMeta.(*GhostMetadata); !ok {
			t.Error("Ghost constructor should return *GhostMetadata")
		}

		msgMeta := metaTypes.Message()
		if _, ok := msgMeta.(*MessageMetadata); !ok {
			t.Error("Message constructor should return *MessageMetadata")
		}

		portalMeta := metaTypes.Portal()
		if _, ok := portalMeta.(*PortalMetadata); !ok {
			t.Error("Portal constructor should return *PortalMetadata")
		}

		userLoginMeta := metaTypes.UserLogin()
		if _, ok := userLoginMeta.(*UserLoginMetadata); !ok {
			t.Error("UserLogin constructor should return *UserLoginMetadata")
		}
	})
}

func TestGetCapabilities(t *testing.T) {
	t.Run("returns network capabilities", func(t *testing.T) {
		connector := NewConnector()
		caps := connector.GetCapabilities()

		if caps == nil {
			t.Fatal("GetCapabilities returned nil")
		}

		// Claude doesn't support disappearing messages
		if caps.DisappearingMessages {
			t.Error("should not support disappearing messages")
		}
	})
}

func TestMetadataStructures(t *testing.T) {
	t.Run("GhostMetadata has model field", func(t *testing.T) {
		meta := &GhostMetadata{
			Model: "claude-3-5-sonnet-20241022",
		}

		if meta.Model == "" {
			t.Error("Model field should be set")
		}
	})

	t.Run("PortalMetadata has required fields", func(t *testing.T) {
		temp := 0.7
		meta := &PortalMetadata{
			ConversationName: "Test Chat",
			Model:            "claude-3-5-sonnet-20241022",
			SystemPrompt:     "You are helpful",
			Temperature:      &temp,
		}

		if meta.ConversationName == "" {
			t.Error("ConversationName should be set")
		}

		if meta.Model == "" {
			t.Error("Model should be set")
		}

		if meta.Temperature == nil || *meta.Temperature <= 0 {
			t.Error("Temperature should be positive")
		}
	})

	t.Run("MessageMetadata has ClaudeMessageID", func(t *testing.T) {
		meta := &MessageMetadata{
			ClaudeMessageID: "msg_123",
			TokensUsed:      150,
		}

		if meta.ClaudeMessageID == "" {
			t.Error("ClaudeMessageID should be set")
		}

		if meta.TokensUsed <= 0 {
			t.Error("TokensUsed should be positive")
		}
	})

	t.Run("UserLoginMetadata has APIKey field", func(t *testing.T) {
		meta := &UserLoginMetadata{
			APIKey: "sk-ant-test-key",
			Email:  "user@example.com",
		}

		if meta.APIKey == "" {
			t.Error("APIKey should be set")
		}
	})
}

func TestImplementsInterfaces(t *testing.T) {
	t.Run("implements NetworkConnector", func(t *testing.T) {
		var _ bridgev2.NetworkConnector = (*ClaudeConnector)(nil)
	})
}

func TestMakeClaudePortalKey(t *testing.T) {
	t.Run("creates portal key from conversation ID without Receiver", func(t *testing.T) {
		conversationID := "conv_123"

		key := MakeClaudePortalKey(conversationID)

		if key.ID == "" {
			t.Error("portal key ID should not be empty")
		}

		// Portal key should contain conversation identifier
		if string(key.ID) != conversationID {
			t.Errorf("portal key ID = %v, want %v", key.ID, conversationID)
		}

		// Receiver should be empty to allow set-preferred-login to work
		if key.Receiver != "" {
			t.Errorf("portal key Receiver should be empty, got %v", key.Receiver)
		}
	})
}

func TestMakeClaudeMessageID(t *testing.T) {
	t.Run("creates message ID from Claude message ID", func(t *testing.T) {
		claudeMessageID := "msg_abc123"

		msgID := MakeClaudeMessageID(claudeMessageID)

		if msgID == "" {
			t.Error("message ID should not be empty")
		}

		idStr := string(msgID)
		if idStr == "" {
			t.Error("message ID string should not be empty")
		}
	})
}
