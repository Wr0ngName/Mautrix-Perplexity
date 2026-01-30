package connector

import (
	"encoding/json"
	"testing"
)

func TestPortalMetadataWebSearchFields(t *testing.T) {
	t.Run("WebSearchDomains JSON serialization", func(t *testing.T) {
		meta := &PortalMetadata{
			WebSearchDomains: []string{"example.com", "docs.example.org"},
		}

		data, err := json.Marshal(meta)
		if err != nil {
			t.Fatalf("Failed to marshal: %v", err)
		}

		var decoded PortalMetadata
		if err := json.Unmarshal(data, &decoded); err != nil {
			t.Fatalf("Failed to unmarshal: %v", err)
		}

		if len(decoded.WebSearchDomains) != 2 {
			t.Errorf("Expected 2 domains, got %d", len(decoded.WebSearchDomains))
		}

		if decoded.WebSearchDomains[0] != "example.com" {
			t.Errorf("First domain = %q, want %q", decoded.WebSearchDomains[0], "example.com")
		}

		if decoded.WebSearchDomains[1] != "docs.example.org" {
			t.Errorf("Second domain = %q, want %q", decoded.WebSearchDomains[1], "docs.example.org")
		}
	})

	t.Run("WebSearchRecency JSON serialization", func(t *testing.T) {
		testCases := []string{"day", "week", "month", "year"}

		for _, recency := range testCases {
			meta := &PortalMetadata{
				WebSearchRecency: recency,
			}

			data, err := json.Marshal(meta)
			if err != nil {
				t.Fatalf("Failed to marshal with recency %q: %v", recency, err)
			}

			var decoded PortalMetadata
			if err := json.Unmarshal(data, &decoded); err != nil {
				t.Fatalf("Failed to unmarshal with recency %q: %v", recency, err)
			}

			if decoded.WebSearchRecency != recency {
				t.Errorf("WebSearchRecency = %q, want %q", decoded.WebSearchRecency, recency)
			}
		}
	})

	t.Run("empty WebSearchDomains omitted from JSON", func(t *testing.T) {
		meta := &PortalMetadata{
			ConversationName: "Test",
		}

		data, err := json.Marshal(meta)
		if err != nil {
			t.Fatalf("Failed to marshal: %v", err)
		}

		// Check that web_search_domains is not in the JSON
		var raw map[string]interface{}
		if err := json.Unmarshal(data, &raw); err != nil {
			t.Fatalf("Failed to unmarshal to map: %v", err)
		}

		if _, exists := raw["web_search_domains"]; exists {
			t.Error("Expected web_search_domains to be omitted when empty")
		}
	})

	t.Run("empty WebSearchRecency omitted from JSON", func(t *testing.T) {
		meta := &PortalMetadata{
			ConversationName: "Test",
		}

		data, err := json.Marshal(meta)
		if err != nil {
			t.Fatalf("Failed to marshal: %v", err)
		}

		// Check that web_search_recency is not in the JSON
		var raw map[string]interface{}
		if err := json.Unmarshal(data, &raw); err != nil {
			t.Fatalf("Failed to unmarshal to map: %v", err)
		}

		if _, exists := raw["web_search_recency"]; exists {
			t.Error("Expected web_search_recency to be omitted when empty")
		}
	})

	t.Run("full PortalMetadata with all fields", func(t *testing.T) {
		temp := 0.8
		meta := &PortalMetadata{
			ConversationName: "Test Chat",
			Model:            "sonar-pro",
			SystemPrompt:     "You are helpful",
			Temperature:      &temp,
			MentionOnly:      true,
			ConversationMode: true,
			WebSearchDomains: []string{"github.com", "stackoverflow.com"},
			WebSearchRecency: "week",
		}

		data, err := json.Marshal(meta)
		if err != nil {
			t.Fatalf("Failed to marshal: %v", err)
		}

		var decoded PortalMetadata
		if err := json.Unmarshal(data, &decoded); err != nil {
			t.Fatalf("Failed to unmarshal: %v", err)
		}

		// Verify all fields
		if decoded.ConversationName != "Test Chat" {
			t.Errorf("ConversationName = %q, want %q", decoded.ConversationName, "Test Chat")
		}
		if decoded.Model != "sonar-pro" {
			t.Errorf("Model = %q, want %q", decoded.Model, "sonar-pro")
		}
		if decoded.SystemPrompt != "You are helpful" {
			t.Errorf("SystemPrompt = %q, want %q", decoded.SystemPrompt, "You are helpful")
		}
		if decoded.Temperature == nil || *decoded.Temperature != 0.8 {
			t.Error("Temperature not preserved correctly")
		}
		if !decoded.MentionOnly {
			t.Error("MentionOnly should be true")
		}
		if !decoded.ConversationMode {
			t.Error("ConversationMode should be true")
		}
		if len(decoded.WebSearchDomains) != 2 {
			t.Errorf("Expected 2 domains, got %d", len(decoded.WebSearchDomains))
		}
		if decoded.WebSearchRecency != "week" {
			t.Errorf("WebSearchRecency = %q, want %q", decoded.WebSearchRecency, "week")
		}
	})
}

func TestPortalMetadataGetTemperature(t *testing.T) {
	defaultTemp := 0.5

	t.Run("returns portal temperature when set", func(t *testing.T) {
		temp := 0.9
		meta := &PortalMetadata{Temperature: &temp}

		result := meta.GetTemperature(defaultTemp)
		if result != 0.9 {
			t.Errorf("GetTemperature() = %f, want %f", result, 0.9)
		}
	})

	t.Run("returns default when temperature is nil", func(t *testing.T) {
		meta := &PortalMetadata{}

		result := meta.GetTemperature(defaultTemp)
		if result != defaultTemp {
			t.Errorf("GetTemperature() = %f, want %f", result, defaultTemp)
		}
	})

	t.Run("returns default when temperature is out of range (negative)", func(t *testing.T) {
		temp := -0.5
		meta := &PortalMetadata{Temperature: &temp}

		result := meta.GetTemperature(defaultTemp)
		if result != defaultTemp {
			t.Errorf("GetTemperature() = %f, want %f (negative should use default)", result, defaultTemp)
		}
	})

	t.Run("returns default when temperature is out of range (too high)", func(t *testing.T) {
		temp := 2.5
		meta := &PortalMetadata{Temperature: &temp}

		result := meta.GetTemperature(defaultTemp)
		if result != defaultTemp {
			t.Errorf("GetTemperature() = %f, want %f (>2 should use default)", result, defaultTemp)
		}
	})

	t.Run("accepts boundary values", func(t *testing.T) {
		// Test 0
		temp := 0.0
		meta := &PortalMetadata{Temperature: &temp}
		if result := meta.GetTemperature(defaultTemp); result != 0.0 {
			t.Errorf("GetTemperature(0) = %f, want 0.0", result)
		}

		// Test 2
		temp = 2.0
		meta = &PortalMetadata{Temperature: &temp}
		if result := meta.GetTemperature(defaultTemp); result != 2.0 {
			t.Errorf("GetTemperature(2) = %f, want 2.0", result)
		}
	})
}

func TestConversationModeDefault(t *testing.T) {
	t.Run("ConversationMode defaults to false", func(t *testing.T) {
		meta := &PortalMetadata{}

		if meta.ConversationMode {
			t.Error("ConversationMode should default to false")
		}
	})

	t.Run("ConversationMode preserved after JSON round-trip", func(t *testing.T) {
		meta := &PortalMetadata{
			ConversationMode: true,
		}

		data, err := json.Marshal(meta)
		if err != nil {
			t.Fatalf("Failed to marshal: %v", err)
		}

		var decoded PortalMetadata
		if err := json.Unmarshal(data, &decoded); err != nil {
			t.Fatalf("Failed to unmarshal: %v", err)
		}

		if !decoded.ConversationMode {
			t.Error("ConversationMode should be true after round-trip")
		}
	})
}
