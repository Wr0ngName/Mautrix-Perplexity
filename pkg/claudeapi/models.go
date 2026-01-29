// Package claudeapi provides a wrapper around the official Anthropic Go SDK.
package claudeapi

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

// ModelInfo contains information about a Claude model.
type ModelInfo struct {
	ID          string    `json:"id"`
	DisplayName string    `json:"display_name"`
	CreatedAt   time.Time `json:"created_at"`
	Family      string    `json:"family"` // opus, sonnet, haiku
}

// ModelCache caches model information from the API.
type ModelCache struct {
	models    []ModelInfo
	byID      map[string]*ModelInfo
	mu        sync.RWMutex
	fetchMu   sync.Mutex // Prevents thundering herd on cache refresh
	lastFetch time.Time
	ttl       time.Duration
}

// NewModelCache creates a new model cache with the given TTL.
func NewModelCache(ttl time.Duration) *ModelCache {
	return &ModelCache{
		byID: make(map[string]*ModelInfo),
		ttl:  ttl,
	}
}

// globalModelCache is a package-level cache for model info.
var globalModelCache = NewModelCache(15 * time.Minute)

// FetchModels fetches the list of available models from the API.
func FetchModels(ctx context.Context, apiKey string) ([]ModelInfo, error) {
	client := anthropic.NewClient(option.WithAPIKey(apiKey))

	var allModels []ModelInfo

	// Use auto-paging to get all models
	pager := client.Models.ListAutoPaging(ctx, anthropic.ModelListParams{})
	for pager.Next() {
		m := pager.Current()
		info := ModelInfo{
			ID:          m.ID,
			DisplayName: m.DisplayName,
			CreatedAt:   m.CreatedAt,
			Family:      inferModelFamily(m.ID),
		}
		allModels = append(allModels, info)
	}

	if err := pager.Err(); err != nil {
		return nil, err
	}

	// Check for empty response - this could indicate API issues or invalid key
	if len(allModels) == 0 {
		return nil, fmt.Errorf("no models returned from API - check API key permissions")
	}

	// Update global cache
	globalModelCache.Update(allModels)

	return allModels, nil
}

// GetModel fetches information about a specific model from the API.
func GetModel(ctx context.Context, apiKey string, modelID string) (*ModelInfo, error) {
	client := anthropic.NewClient(option.WithAPIKey(apiKey))

	m, err := client.Models.Get(ctx, modelID, anthropic.ModelGetParams{})
	if err != nil {
		return nil, err
	}

	return &ModelInfo{
		ID:          m.ID,
		DisplayName: m.DisplayName,
		CreatedAt:   m.CreatedAt,
		Family:      inferModelFamily(m.ID),
	}, nil
}

// ValidateModel checks if a model ID is valid by querying the API.
func ValidateModel(ctx context.Context, apiKey string, modelID string) error {
	_, err := GetModel(ctx, apiKey, modelID)
	return err
}

// inferModelFamily infers the model family from the model ID.
// Returns empty string if family cannot be determined from the model name.
// Valid Claude models always contain "opus", "sonnet", or "haiku" in their name.
// Note: Unknown models are not logged here to avoid log spam during bulk operations.
// Callers should handle and log unknown models at the appropriate level.
func inferModelFamily(modelID string) string {
	id := strings.ToLower(modelID)
	switch {
	case strings.Contains(id, "opus"):
		return "opus"
	case strings.Contains(id, "sonnet"):
		return "sonnet"
	case strings.Contains(id, "haiku"):
		return "haiku"
	default:
		// Return empty string for unrecognized models - caller must handle this
		// This includes future Claude models that may have different naming
		return ""
	}
}

// Update updates the cache with new model data.
// Makes a deep copy of the input to ensure thread safety.
func (c *ModelCache) Update(models []ModelInfo) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Deep copy to avoid storing references to caller's data
	c.models = make([]ModelInfo, len(models))
	copy(c.models, models)
	c.byID = make(map[string]*ModelInfo, len(c.models))
	for i := range c.models {
		c.byID[c.models[i].ID] = &c.models[i]
	}
	c.lastFetch = time.Now()
}

// GetAll returns all cached models.
func (c *ModelCache) GetAll() []ModelInfo {
	c.mu.RLock()
	defer c.mu.RUnlock()

	result := make([]ModelInfo, len(c.models))
	copy(result, c.models)
	return result
}

// Get returns a cached model by ID.
func (c *ModelCache) Get(id string) *ModelInfo {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if info, ok := c.byID[id]; ok {
		// Return a copy
		result := *info
		return &result
	}
	return nil
}

// IsStale returns true if the cache is stale.
func (c *ModelCache) IsStale() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return time.Since(c.lastFetch) > c.ttl
}

// IsEmpty returns true if the cache is empty.
func (c *ModelCache) IsEmpty() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return len(c.models) == 0
}

// GetCachedModels returns cached models, fetching from API if cache is empty or stale.
// Uses double-checked locking to prevent thundering herd on cache refresh.
func GetCachedModels(ctx context.Context, apiKey string) ([]ModelInfo, error) {
	// First check without fetch lock (fast path)
	if !globalModelCache.IsEmpty() && !globalModelCache.IsStale() {
		return globalModelCache.GetAll(), nil
	}

	// Acquire fetch lock to prevent multiple concurrent fetches
	globalModelCache.fetchMu.Lock()
	defer globalModelCache.fetchMu.Unlock()

	// Double-check after acquiring lock (another goroutine may have refreshed)
	if !globalModelCache.IsEmpty() && !globalModelCache.IsStale() {
		return globalModelCache.GetAll(), nil
	}

	return FetchModels(ctx, apiKey)
}

// GetCachedModelInfo returns cached model info by ID.
func GetCachedModelInfo(id string) *ModelInfo {
	return globalModelCache.Get(id)
}

// GetModelFamily returns the family for a model ID.
func GetModelFamily(modelID string) string {
	// Check cache first
	if info := globalModelCache.Get(modelID); info != nil {
		return info.Family
	}
	// Fallback to inference
	return inferModelFamily(modelID)
}

// GetModelDisplayName returns the display name for a model.
func GetModelDisplayName(modelID string) string {
	if info := globalModelCache.Get(modelID); info != nil {
		return info.DisplayName
	}
	return modelID
}

// ResolveModelAlias resolves a friendly alias to a model ID.
// This queries the API to get the actual model ID.
func ResolveModelAlias(ctx context.Context, apiKey string, alias string) (string, error) {
	// The API supports model aliases - just validate and return the resolved ID
	info, err := GetModel(ctx, apiKey, alias)
	if err != nil {
		return "", err
	}
	return info.ID, nil
}

// GetLatestModelByFamily returns the most recent model ID for a given family.
// Requires models to be cached first via FetchModels or GetCachedModels.
func GetLatestModelByFamily(family string) string {
	family = strings.ToLower(family)
	models := globalModelCache.GetAll()

	var latest *ModelInfo
	for i := range models {
		m := &models[i]
		if m.Family == family {
			if latest == nil || m.CreatedAt.After(latest.CreatedAt) {
				latest = m
			}
		}
	}

	if latest != nil {
		return latest.ID
	}
	return ""
}

// GetLatestModelByFamilyFromAPI fetches models and returns the latest for a family.
// Use this when you need to ensure fresh data from the API.
func GetLatestModelByFamilyFromAPI(ctx context.Context, apiKey string, family string) (string, error) {
	// Fetch fresh model list
	_, err := FetchModels(ctx, apiKey)
	if err != nil {
		return "", fmt.Errorf("failed to fetch models: %w", err)
	}

	modelID := GetLatestModelByFamily(family)
	if modelID == "" {
		return "", fmt.Errorf("no models found for family: %s", family)
	}

	return modelID, nil
}

// GetDefaultModelID returns a sensible default model ID.
// If cache is available, returns the latest sonnet. Otherwise returns a fallback.
func GetDefaultModelID() string {
	// Try to get latest sonnet from cache
	if modelID := GetLatestModelByFamily("sonnet"); modelID != "" {
		return modelID
	}
	// Fallback only used if cache is empty (first run before any API calls)
	// The API will resolve this or fail with a helpful error
	return "claude-sonnet-4-5-20250929"
}

// EstimateMaxTokens returns estimated max tokens for a model based on family.
// These are approximations - actual limits come from API responses.
func EstimateMaxTokens(modelID string) (inputTokens, outputTokens int) {
	family := GetModelFamily(modelID)
	switch family {
	case "opus":
		return 200000, 32000
	case "sonnet":
		return 200000, 64000
	case "haiku":
		return 200000, 64000
	default:
		return 200000, 64000
	}
}

// GetModelMaxTokens returns the estimated max input tokens for a model.
func GetModelMaxTokens(modelID string) int {
	inputTokens, _ := EstimateMaxTokens(modelID)
	return inputTokens
}

// GetModelInfo returns cached model info, or creates a basic info struct from the model ID.
// This is for backward compatibility - prefer GetCachedModelInfo for cached data.
func GetModelInfo(modelID string) *ModelInfo {
	// Check cache first
	if info := globalModelCache.Get(modelID); info != nil {
		return info
	}

	// Return basic info inferred from model ID
	return &ModelInfo{
		ID:          modelID,
		DisplayName: GetModelDisplayName(modelID),
		Family:      inferModelFamily(modelID),
	}
}
