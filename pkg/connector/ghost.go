package connector

import (
	"context"
	"fmt"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"
)

// GetOrUpdateGhost gets a ghost by ID and ensures its metadata and Matrix profile are properly populated.
// This fixes the issue where ghosts are created with empty Model metadata and no display name.
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

	// Track if we need to update
	needsUpdate := false

	// Set model if not already set
	if meta.Model == "" && model != "" {
		meta.Model = model
		needsUpdate = true
	}

	// Update Matrix profile if name is not set or metadata changed
	if ghost.Name == "" || needsUpdate {
		modelName := meta.Model
		if modelName == "" {
			modelName = string(ghost.ID)
			if modelName == "" {
				modelName = c.Config.GetDefaultModel()
			}
		}

		displayName := fmt.Sprintf("Perplexity (%s)", modelName)

		isBot := true
		userInfo := &bridgev2.UserInfo{
			Name:        &displayName,
			IsBot:       &isBot,
			Identifiers: []string{fmt.Sprintf("perplexity:%s", modelName)},
		}

		ghost.UpdateInfo(ctx, userInfo)
		c.Log.Debug().
			Str("ghost_id", string(ghostID)).
			Str("display_name", displayName).
			Msg("Updated ghost display name")
	}

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

	displayName := fmt.Sprintf("Perplexity (%s)", modelName)

	isBot := true

	return &bridgev2.UserInfo{
		Name:        &displayName,
		IsBot:       &isBot,
		Identifiers: []string{fmt.Sprintf("perplexity:%s", modelName)},
	}, nil
}
