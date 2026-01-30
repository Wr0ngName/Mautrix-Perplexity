package sidecar

import (
	"context"
	"testing"
)

func TestWithConversationMode(t *testing.T) {
	t.Run("enabled conversation mode", func(t *testing.T) {
		ctx := context.Background()
		ctx = WithConversationMode(ctx, true)

		val := ctx.Value(conversationModeKey)
		if val == nil {
			t.Fatal("Expected conversation mode value in context")
		}

		enabled, ok := val.(bool)
		if !ok {
			t.Fatalf("Expected bool type, got %T", val)
		}

		if !enabled {
			t.Error("Expected conversation mode to be true")
		}
	})

	t.Run("disabled conversation mode", func(t *testing.T) {
		ctx := context.Background()
		ctx = WithConversationMode(ctx, false)

		val := ctx.Value(conversationModeKey)
		if val == nil {
			t.Fatal("Expected conversation mode value in context")
		}

		enabled, ok := val.(bool)
		if !ok {
			t.Fatalf("Expected bool type, got %T", val)
		}

		if enabled {
			t.Error("Expected conversation mode to be false")
		}
	})

	t.Run("default context has no conversation mode", func(t *testing.T) {
		ctx := context.Background()
		val := ctx.Value(conversationModeKey)
		if val != nil {
			t.Error("Expected nil for conversation mode in fresh context")
		}
	})
}

func TestWithWebSearchOptions(t *testing.T) {
	t.Run("with domain filter", func(t *testing.T) {
		ctx := context.Background()
		opts := &WebSearchOptions{
			SearchDomainFilter: []string{"example.com", "docs.example.org"},
		}
		ctx = WithWebSearchOptions(ctx, opts)

		val := ctx.Value(webSearchOptionsKey)
		if val == nil {
			t.Fatal("Expected web search options in context")
		}

		retrieved, ok := val.(*WebSearchOptions)
		if !ok {
			t.Fatalf("Expected *WebSearchOptions type, got %T", val)
		}

		if len(retrieved.SearchDomainFilter) != 2 {
			t.Errorf("Expected 2 domains, got %d", len(retrieved.SearchDomainFilter))
		}

		if retrieved.SearchDomainFilter[0] != "example.com" {
			t.Errorf("First domain = %q, want %q", retrieved.SearchDomainFilter[0], "example.com")
		}
	})

	t.Run("with recency filter", func(t *testing.T) {
		ctx := context.Background()
		opts := &WebSearchOptions{
			SearchRecencyFilter: "week",
		}
		ctx = WithWebSearchOptions(ctx, opts)

		val := ctx.Value(webSearchOptionsKey)
		if val == nil {
			t.Fatal("Expected web search options in context")
		}

		retrieved, ok := val.(*WebSearchOptions)
		if !ok {
			t.Fatalf("Expected *WebSearchOptions type, got %T", val)
		}

		if retrieved.SearchRecencyFilter != "week" {
			t.Errorf("SearchRecencyFilter = %q, want %q", retrieved.SearchRecencyFilter, "week")
		}
	})

	t.Run("with both filters", func(t *testing.T) {
		ctx := context.Background()
		opts := &WebSearchOptions{
			SearchDomainFilter:  []string{"github.com"},
			SearchRecencyFilter: "month",
		}
		ctx = WithWebSearchOptions(ctx, opts)

		val := ctx.Value(webSearchOptionsKey)
		retrieved := val.(*WebSearchOptions)

		if len(retrieved.SearchDomainFilter) != 1 || retrieved.SearchDomainFilter[0] != "github.com" {
			t.Error("Domain filter not preserved correctly")
		}
		if retrieved.SearchRecencyFilter != "month" {
			t.Error("Recency filter not preserved correctly")
		}
	})

	t.Run("nil options", func(t *testing.T) {
		ctx := context.Background()
		ctx = WithWebSearchOptions(ctx, nil)

		val := ctx.Value(webSearchOptionsKey)
		// nil value should be stored, not omitted
		if val != nil {
			retrieved := val.(*WebSearchOptions)
			if retrieved != nil {
				t.Error("Expected nil options to be stored as nil")
			}
		}
	})

	t.Run("default context has no web search options", func(t *testing.T) {
		ctx := context.Background()
		val := ctx.Value(webSearchOptionsKey)
		if val != nil {
			t.Error("Expected nil for web search options in fresh context")
		}
	})
}

func TestWithUserCredentials(t *testing.T) {
	t.Run("sets user ID and API key", func(t *testing.T) {
		ctx := context.Background()
		ctx = WithUserCredentials(ctx, "@user:example.com", "pplx-test-key-123")

		userID := ctx.Value(userIDKey)
		apiKey := ctx.Value(apiKeyContextKey)

		if userID != "@user:example.com" {
			t.Errorf("userID = %v, want @user:example.com", userID)
		}

		if apiKey != "pplx-test-key-123" {
			t.Errorf("apiKey = %v, want pplx-test-key-123", apiKey)
		}
	})

	t.Run("empty values", func(t *testing.T) {
		ctx := context.Background()
		ctx = WithUserCredentials(ctx, "", "")

		userID := ctx.Value(userIDKey)
		apiKey := ctx.Value(apiKeyContextKey)

		// Empty strings should still be set
		if userID != "" {
			t.Errorf("userID = %v, want empty string", userID)
		}
		if apiKey != "" {
			t.Errorf("apiKey = %v, want empty string", apiKey)
		}
	})
}

func TestWithPortalID(t *testing.T) {
	t.Run("sets portal ID", func(t *testing.T) {
		ctx := context.Background()
		ctx = WithPortalID(ctx, "portal-test-123")

		val := ctx.Value(portalIDKey)
		if val != "portal-test-123" {
			t.Errorf("portalID = %v, want portal-test-123", val)
		}
	})

	t.Run("default context has no portal ID", func(t *testing.T) {
		ctx := context.Background()
		val := ctx.Value(portalIDKey)
		if val != nil {
			t.Error("Expected nil for portal ID in fresh context")
		}
	})
}

func TestContextCombinations(t *testing.T) {
	t.Run("all context values together", func(t *testing.T) {
		ctx := context.Background()

		// Add all context values
		ctx = WithPortalID(ctx, "portal-abc")
		ctx = WithUserCredentials(ctx, "@alice:matrix.org", "pplx-key-xyz")
		ctx = WithConversationMode(ctx, true)
		ctx = WithWebSearchOptions(ctx, &WebSearchOptions{
			SearchDomainFilter:  []string{"docs.example.com"},
			SearchRecencyFilter: "day",
		})

		// Verify all values are retrievable
		if ctx.Value(portalIDKey) != "portal-abc" {
			t.Error("Portal ID not preserved")
		}
		if ctx.Value(userIDKey) != "@alice:matrix.org" {
			t.Error("User ID not preserved")
		}
		if ctx.Value(apiKeyContextKey) != "pplx-key-xyz" {
			t.Error("API key not preserved")
		}
		if ctx.Value(conversationModeKey) != true {
			t.Error("Conversation mode not preserved")
		}

		webOpts := ctx.Value(webSearchOptionsKey).(*WebSearchOptions)
		if len(webOpts.SearchDomainFilter) != 1 || webOpts.SearchDomainFilter[0] != "docs.example.com" {
			t.Error("Web search domain filter not preserved")
		}
		if webOpts.SearchRecencyFilter != "day" {
			t.Error("Web search recency filter not preserved")
		}
	})

	t.Run("context values are independent", func(t *testing.T) {
		ctx1 := context.Background()
		ctx1 = WithPortalID(ctx1, "portal1")

		ctx2 := context.Background()
		ctx2 = WithPortalID(ctx2, "portal2")

		// Verify contexts are independent
		if ctx1.Value(portalIDKey) == ctx2.Value(portalIDKey) {
			t.Error("Context values should be independent")
		}
	})
}
