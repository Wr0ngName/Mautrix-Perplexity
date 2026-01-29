// Package perplexityapi provides model definitions for the Perplexity API.
package perplexityapi

import (
	"strings"
)

// Perplexity model families and their variants.
const (
	ModelSonar           = "sonar"
	ModelSonarPro        = "sonar-pro"
	ModelSonarReasoning  = "sonar-reasoning"
	ModelSonarReasoningPro = "sonar-reasoning-pro"
)

// ModelFamilies contains all available model families for Perplexity.
var ModelFamilies = []string{
	ModelSonar,
	ModelSonarPro,
	ModelSonarReasoning,
	ModelSonarReasoningPro,
}

// ValidModels maps model names to whether they are valid.
var ValidModels = map[string]bool{
	ModelSonar:             true,
	ModelSonarPro:          true,
	ModelSonarReasoning:    true,
	ModelSonarReasoningPro: true,
}

// ModelContextSizes maps models to their context window sizes.
var ModelContextSizes = map[string]int{
	ModelSonar:             128000,
	ModelSonarPro:          200000,
	ModelSonarReasoning:    128000,
	ModelSonarReasoningPro: 128000,
}

// IsValidModel checks if a model name is valid.
func IsValidModel(model string) bool {
	return ValidModels[strings.ToLower(model)]
}

// GetModelFamily returns the model family for a given model ID.
// For Perplexity, the model ID is the family name.
func GetModelFamily(modelID string) string {
	id := strings.ToLower(modelID)

	// Check exact matches first
	if ValidModels[id] {
		return id
	}

	// Check prefixes for future-proofing (e.g., "sonar-pro-2025" would map to "sonar-pro")
	switch {
	case strings.HasPrefix(id, "sonar-reasoning-pro"):
		return ModelSonarReasoningPro
	case strings.HasPrefix(id, "sonar-reasoning"):
		return ModelSonarReasoning
	case strings.HasPrefix(id, "sonar-pro"):
		return ModelSonarPro
	case strings.HasPrefix(id, "sonar"):
		return ModelSonar
	default:
		return ModelSonar // Default to basic sonar
	}
}

// GetContextSize returns the context window size for a model.
func GetContextSize(model string) int {
	family := GetModelFamily(model)
	if size, ok := ModelContextSizes[family]; ok {
		return size
	}
	return 128000 // Default
}

// DefaultModel is the default model to use if none is specified.
const DefaultModel = ModelSonar
