package connector

import (
	"context"
	"fmt"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"

	"go.mau.fi/mautrix-claude/pkg/claudeapi"
)

// GetOrUpdateGhost gets a ghost by ID and ensures its metadata is properly populated.
// This fixes the issue where ghosts are created with empty Model metadata.
func (c *ClaudeConnector) GetOrUpdateGhost(ctx context.Context, ghostID networkid.UserID, model string) (*bridgev2.Ghost, error) {
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

	return ghost, nil
}

// GetUserInfo returns information about a ghost user (Claude model).
func (c *ClaudeClient) GetUserInfo(ctx context.Context, ghost *bridgev2.Ghost) (*bridgev2.UserInfo, error) {
	meta, ok := ghost.Metadata.(*GhostMetadata)
	if !ok || meta == nil {
		return nil, fmt.Errorf("invalid ghost metadata")
	}

	modelName := meta.Model
	if modelName == "" {
		// Try to derive from ghost ID (ghost ID is the model family like "sonnet")
		modelName = string(ghost.ID)
		if modelName == "" {
			modelName = c.Connector.Config.GetDefaultModel()
		}
	}
	displayName := fmt.Sprintf("Claude (%s)", modelName)

	// Get model info for better display name
	if info := claudeapi.GetModelInfo(modelName); info != nil && info.DisplayName != "" {
		displayName = info.DisplayName
	}

	isBot := true

	return &bridgev2.UserInfo{
		Name:        &displayName,
		IsBot:       &isBot,
		Identifiers: []string{fmt.Sprintf("claude:%s", modelName)},
	}, nil
}
