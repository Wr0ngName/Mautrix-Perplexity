package connector

import (
	"context"
	"fmt"
	"strings"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"

	"go.mau.fi/mautrix-perplexity/pkg/perplexityapi"
)

// GetOrUpdateGhost gets a ghost by ID and ensures its metadata is properly populated.
// This fixes the issue where ghosts are created with empty Model metadata.
func (c *PerplexityConnector) GetOrUpdateGhost(ctx context.Context, ghostID networkid.UserID, model string) (*bridgev2.Ghost, error) {
	ghost, err := c.br.GetGhostByID(ctx, ghostID)
	if err != nil {
		return nil, err
	}

	// Ensure metadata is properly populated
	meta, ok := ghost.Metadata.(*GhostMetadata)
	if !ok {
		meta = &GhostMetadata{}
		ghost.Metadata = meta
	}

	// Set model if not already set
	if meta.Model == "" && model != "" {
		meta.Model = model
		// Save to database using the bridge's DB accessor
		if err := ghost.Bridge.DB.Ghost.Update(ctx, ghost.Ghost); err != nil {
			c.Log.Warn().Err(err).Str("ghost_id", string(ghostID)).Msg("Failed to save ghost metadata")
			// Non-fatal - continue with in-memory value
		}
	}

	// Update the ghost's display name on Matrix
	family := perplexityapi.GetModelFamily(model)
	displayName := fmt.Sprintf("Perplexity (%s)", model)
	if family != "" {
		displayName = fmt.Sprintf("Perplexity %s", strings.Title(strings.ReplaceAll(family, "-", " ")))
	}
	isBot := true
	userInfo := &bridgev2.UserInfo{
		Name:        &displayName,
		IsBot:       &isBot,
		Identifiers: []string{fmt.Sprintf("perplexity:%s", model)},
	}
	ghost.UpdateInfo(ctx, userInfo)

	return ghost, nil
}

// GetUserInfo returns information about a ghost user (Perplexity model).
func (c *PerplexityClient) GetUserInfo(ctx context.Context, ghost *bridgev2.Ghost) (*bridgev2.UserInfo, error) {
	meta, ok := ghost.Metadata.(*GhostMetadata)
	if !ok || meta == nil {
		return nil, fmt.Errorf("invalid ghost metadata")
	}

	modelName := meta.Model
	if modelName == "" {
		// Try to derive from ghost ID (ghost ID is the model family like "sonar")
		modelName = string(ghost.ID)
		if modelName == "" {
			modelName = c.Connector.Config.GetDefaultModel()
		}
	}

	// Create display name from model
	displayName := fmt.Sprintf("Perplexity (%s)", modelName)
	family := perplexityapi.GetModelFamily(modelName)
	if family != "" {
		displayName = fmt.Sprintf("Perplexity %s", strings.Title(strings.ReplaceAll(family, "-", " ")))
	}

	isBot := true

	return &bridgev2.UserInfo{
		Name:        &displayName,
		IsBot:       &isBot,
		Identifiers: []string{fmt.Sprintf("perplexity:%s", modelName)},
	}, nil
}
