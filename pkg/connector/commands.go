package connector

import (
	"context"
	"fmt"
	"strings"
	"time"

	"go.mau.fi/mautrix-claude/pkg/claudeapi"
	"go.mau.fi/mautrix-claude/pkg/sidecar"
	"maunium.net/go/mautrix/bridgev2/commands"
	"maunium.net/go/mautrix/bridgev2/matrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

// resolveModelArg resolves a model argument (opus, sonnet, haiku, or full ID) to the actual model ID.
// For sidecar mode, it returns the family name. For API mode, it resolves to the latest version.
func (c *ClaudeConnector) resolveModelArg(ctx context.Context, ce *commands.Event, modelArg string) (string, error) {
	isSidecar := c.isSidecarLogin(ce)
	modelArg = strings.ToLower(modelArg)

	switch modelArg {
	case "opus", "claude-opus":
		if isSidecar {
			return "opus", nil
		}
		apiKey := c.getAPIKeyFromLogin(ce)
		if apiKey == "" {
			return "", fmt.Errorf("failed to get API credentials")
		}
		return claudeapi.GetLatestModelByFamilyFromAPI(ctx, apiKey, "opus")

	case "sonnet", "claude-sonnet":
		if isSidecar {
			return "sonnet", nil
		}
		apiKey := c.getAPIKeyFromLogin(ce)
		if apiKey == "" {
			return "", fmt.Errorf("failed to get API credentials")
		}
		return claudeapi.GetLatestModelByFamilyFromAPI(ctx, apiKey, "sonnet")

	case "haiku", "claude-haiku":
		if isSidecar {
			return "haiku", nil
		}
		apiKey := c.getAPIKeyFromLogin(ce)
		if apiKey == "" {
			return "", fmt.Errorf("failed to get API credentials")
		}
		return claudeapi.GetLatestModelByFamilyFromAPI(ctx, apiKey, "haiku")

	default:
		// Validate model ID format
		if err := ValidateModelID(modelArg); err != nil {
			return "", fmt.Errorf("invalid model ID: %w", err)
		}

		// For API mode, validate the model exists
		if !isSidecar {
			apiKey := c.getAPIKeyFromLogin(ce)
			if apiKey == "" {
				return "", fmt.Errorf("failed to get API credentials")
			}
			if err := claudeapi.ValidateModel(ctx, apiKey, modelArg); err != nil {
				return "", fmt.Errorf("invalid model: %w", err)
			}
		}
		return modelArg, nil
	}
}

// swapGhosts ensures the correct ghost is in the room for the new model.
// If oldModel and newModel have different families, the old ghost is removed.
// The new ghost is always ensured to be in the room.
func (c *ClaudeConnector) swapGhosts(ctx context.Context, roomID id.RoomID, oldModel, newModel string) error {
	oldFamily := claudeapi.GetModelFamily(oldModel)
	newFamily := claudeapi.GetModelFamily(newModel)
	familyChanged := oldFamily != newFamily

	newGhostID := c.MakeClaudeGhostID(newModel)

	// Get the new ghost and ensure it joins the room
	newGhost, err := c.GetOrUpdateGhost(ctx, newGhostID, newModel)
	if err != nil {
		return fmt.Errorf("failed to get new ghost: %w", err)
	}

	// Have the new ghost join
	if err := newGhost.Intent.EnsureJoined(ctx, roomID); err != nil {
		// Try invite + join
		if err := c.br.Bot.EnsureInvited(ctx, roomID, newGhost.Intent.GetMXID()); err != nil {
			return fmt.Errorf("failed to invite new ghost: %w", err)
		}
		if err := newGhost.Intent.EnsureJoined(ctx, roomID); err != nil {
			return fmt.Errorf("new ghost failed to join after invite: %w", err)
		}
	}

	// Only remove old ghost if family changed
	if familyChanged {
		oldGhostID := c.MakeClaudeGhostID(oldModel)

		// Have the old ghost leave the room
		oldGhost, err := c.br.GetExistingGhostByID(ctx, oldGhostID)
		if err == nil && oldGhost != nil {
			// Use the underlying appservice IntentAPI's LeaveRoom method
			if asIntent, ok := oldGhost.Intent.(*matrix.ASIntent); ok {
				if _, err := asIntent.Matrix.LeaveRoom(ctx, roomID); err != nil {
					c.Log.Warn().Err(err).Msg("Failed to leave old ghost from room")
				}
			} else {
				// Fallback to SendState approach
				leaveContent := &event.Content{Parsed: &event.MemberEventContent{Membership: event.MembershipLeave}}
				if _, err := oldGhost.Intent.SendState(ctx, roomID, event.StateMember, oldGhost.Intent.GetMXID().String(), leaveContent, time.Time{}); err != nil {
					c.Log.Warn().Err(err).Msg("Failed to send leave state for old ghost")
				}
			}
		}

		c.Log.Info().
			Str("old_ghost", string(oldGhostID)).
			Str("new_ghost", string(newGhostID)).
			Msg("Swapped ghosts for model change")
	}

	return nil
}

// RegisterCommands registers custom commands for the Claude AI bridge.
func (c *ClaudeConnector) RegisterCommands(proc *commands.Processor) {
	proc.AddHandlers(
		&commands.FullHandler{
			Func:    c.cmdJoin,
			Name:    "join",
			Aliases: []string{"add", "invite"},
			Help: commands.HelpMeta{
				Section:     commands.HelpSectionGeneral,
				Description: "Add Claude to the current room (creates a bridge portal)",
				Args:        "[model]",
			},
			RequiresLogin:  true,
			RequiresPortal: false, // Can be used in any room
		},
		&commands.FullHandler{
			Func:    c.cmdModel,
			Name:    "model",
			Aliases: []string{"set-model", "switch-model"},
			Help: commands.HelpMeta{
				Section:     commands.HelpSectionGeneral,
				Description: "View or change the Claude model for this conversation",
				Args:        "[model-name]",
			},
			RequiresLogin:  true,
			RequiresPortal: true,
		},
		&commands.FullHandler{
			Func:    c.cmdModels,
			Name:    "models",
			Aliases: []string{"list-models"},
			Help: commands.HelpMeta{
				Section:     commands.HelpSectionGeneral,
				Description: "List available Claude models",
			},
			RequiresLogin: true,
		},
		&commands.FullHandler{
			Func:    c.cmdClear,
			Name:    "clear",
			Aliases: []string{"reset", "clear-context"},
			Help: commands.HelpMeta{
				Section:     commands.HelpSectionGeneral,
				Description: "Clear the conversation history/context for this room",
			},
			RequiresLogin:  true,
			RequiresPortal: true,
		},
		&commands.FullHandler{
			Func:    c.cmdStats,
			Name:    "stats",
			Aliases: []string{"info", "status"},
			Help: commands.HelpMeta{
				Section:     commands.HelpSectionGeneral,
				Description: "Show conversation statistics for this room",
			},
			RequiresLogin:  true,
			RequiresPortal: true,
		},
		&commands.FullHandler{
			Func:    c.cmdSystem,
			Name:    "system",
			Aliases: []string{"set-system", "system-prompt"},
			Help: commands.HelpMeta{
				Section:     commands.HelpSectionGeneral,
				Description: "View or set the system prompt for this conversation",
				Args:        "[prompt]",
			},
			RequiresLogin:  true,
			RequiresPortal: true,
		},
		&commands.FullHandler{
			Func:    c.cmdTemperature,
			Name:    "temperature",
			Aliases: []string{"temp", "set-temp"},
			Help: commands.HelpMeta{
				Section:     commands.HelpSectionGeneral,
				Description: "View or set the temperature (0-1) for this conversation",
				Args:        "[value]",
			},
			RequiresLogin:  true,
			RequiresPortal: true,
		},
		&commands.FullHandler{
			Func:    c.cmdMention,
			Name:    "mention",
			Aliases: []string{"mentions", "mention-only"},
			Help: commands.HelpMeta{
				Section:     commands.HelpSectionGeneral,
				Description: "Toggle mention-only mode (Claude only responds when @mentioned)",
				Args:        "[on|off]",
			},
			RequiresLogin:  true,
			RequiresPortal: true,
		},
		&commands.FullHandler{
			Func: c.cmdRemoveGhost,
			Name: "remove-ghost",
			Help: commands.HelpMeta{
				Section:     commands.HelpSectionAdmin,
				Description: "Remove a bridge ghost from the current room (admin only)",
				Args:        "<@user:server>",
			},
			RequiresAdmin: true,
		},
	)
}

// getAPIKeyFromLogin extracts the API key from a user login.
func (c *ClaudeConnector) getAPIKeyFromLogin(ce *commands.Event) string {
	login := ce.User.GetDefaultLogin()
	if login == nil {
		return ""
	}
	meta, ok := login.Metadata.(*UserLoginMetadata)
	if !ok || meta == nil {
		return ""
	}
	return meta.APIKey
}

// isSidecarLogin checks if the user is logged in via sidecar (Pro/Max subscription).
func (c *ClaudeConnector) isSidecarLogin(ce *commands.Event) bool {
	login := ce.User.GetDefaultLogin()
	if login == nil {
		return false
	}
	meta, ok := login.Metadata.(*UserLoginMetadata)
	if !ok || meta == nil {
		return false
	}
	return meta.CredentialsJSON != "" && meta.APIKey == ""
}

// cmdModel views or changes the Claude model for a conversation.
func (c *ClaudeConnector) cmdModel(ce *commands.Event) {
	if ce.Portal == nil {
		ce.Reply("This command must be run in a Claude conversation room.")
		return
	}

	meta, ok := ce.Portal.Metadata.(*PortalMetadata)
	if !ok || meta == nil {
		ce.Reply("Failed to get room metadata.")
		return
	}

	// If no argument, show current model
	if len(ce.Args) == 0 {
		currentModel := meta.Model
		if currentModel == "" {
			currentModel = c.Config.GetDefaultModel()
		}

		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("**Current model:** `%s`\n\n", currentModel))

		// Try to get display name from cache
		displayName := claudeapi.GetModelDisplayName(currentModel)
		if displayName != currentModel {
			sb.WriteString(fmt.Sprintf("**Name:** %s\n", displayName))
		}

		inputTokens, outputTokens := claudeapi.EstimateMaxTokens(currentModel)
		sb.WriteString(fmt.Sprintf("**Max input tokens:** %d\n", inputTokens))
		sb.WriteString(fmt.Sprintf("**Max output tokens:** %d\n", outputTokens))

		sb.WriteString("\nUse `model <name>` to change. Run `models` to see available options.")
		ce.Reply(sb.String())
		return
	}

	// Set new model - resolve alias if needed
	ctx, cancel := context.WithTimeout(ce.Ctx, 15*time.Second)
	defer cancel()

	modelArg := strings.Join(ce.Args, "-")
	newModel, err := c.resolveModelArg(ctx, ce, modelArg)
	if err != nil {
		ce.Reply("Failed to resolve model: %v\n\nRun `models` to see available options.", err)
		return
	}

	// Get old model for ghost swap
	oldModel := meta.Model
	if oldModel == "" {
		oldModel = c.Config.GetDefaultModel()
	}

	// Update portal metadata
	meta.Model = newModel
	if err := ce.Portal.Save(ce.Ctx); err != nil {
		meta.Model = oldModel // Rollback in-memory state on save failure
		ce.Reply("Failed to save model change: %v", err)
		return
	}

	// Swap ghosts if family changed
	if ce.Portal.MXID != "" {
		if err := c.swapGhosts(ctx, ce.Portal.MXID, oldModel, newModel); err != nil {
			c.Log.Warn().Err(err).Msg("Failed to swap ghosts for model change")
		}
	}

	displayName := claudeapi.GetModelDisplayName(newModel)
	if displayName != newModel {
		ce.Reply("Model changed to **%s** (`%s`)", displayName, newModel)
	} else {
		ce.Reply("Model changed to `%s`", newModel)
	}
}

// cmdModels lists available Claude models by querying the API.
func (c *ClaudeConnector) cmdModels(ce *commands.Event) {
	// For sidecar mode, just show the available families
	if c.isSidecarLogin(ce) {
		ce.Reply("**Available Claude Models (Pro/Max):**\n\n" +
			"**Opus:**\n• `opus` - Claude Opus (most capable)\n\n" +
			"**Sonnet:**\n• `sonnet` - Claude Sonnet (balanced)\n\n" +
			"**Haiku:**\n• `haiku` - Claude Haiku (fastest)\n\n" +
			"Use `model <name>` to switch models.")
		return
	}

	// Get API key
	apiKey := c.getAPIKeyFromLogin(ce)
	if apiKey == "" {
		ce.Reply("Failed to get API credentials.")
		return
	}

	// Fetch models from API
	ctx, cancel := context.WithTimeout(ce.Ctx, 15*time.Second)
	defer cancel()

	models, err := claudeapi.FetchModels(ctx, apiKey)
	if err != nil {
		ce.Reply("Failed to fetch models from API: %v", err)
		return
	}

	if len(models) == 0 {
		ce.Reply("No models available.")
		return
	}

	var sb strings.Builder
	sb.WriteString("**Available Claude Models:**\n\n")

	defaultModel := c.Config.GetDefaultModel()

	// Group by family
	families := map[string][]claudeapi.ModelInfo{
		"opus":   {},
		"sonnet": {},
		"haiku":  {},
		"other":  {},
	}

	for _, model := range models {
		family := model.Family
		if family == "unknown" {
			family = "other"
		}
		families[family] = append(families[family], model)
	}

	// Display in order: opus, sonnet, haiku, other
	for _, family := range []string{"opus", "sonnet", "haiku", "other"} {
		familyModels := families[family]
		if len(familyModels) == 0 {
			continue
		}

		// Capitalize first letter (strings.Title is deprecated in Go 1.18+)
		capitalizedFamily := strings.ToUpper(family[:1]) + family[1:]
		sb.WriteString(fmt.Sprintf("**%s:**\n", capitalizedFamily))
		for _, model := range familyModels {
			isDefault := ""
			if model.ID == defaultModel {
				isDefault = " *(default)*"
			}

			sb.WriteString(fmt.Sprintf("• **%s**%s\n", model.DisplayName, isDefault))
			sb.WriteString(fmt.Sprintf("  `%s`\n", model.ID))
		}
		sb.WriteString("\n")
	}

	sb.WriteString("Use `model <model-id>` to switch models.\n")
	sb.WriteString("Shortcuts: `opus`, `sonnet`, `haiku`")

	ce.Reply(sb.String())
}

// cmdClear clears the conversation history.
func (c *ClaudeConnector) cmdClear(ce *commands.Event) {
	if ce.Portal == nil {
		ce.Reply("This command must be run in a Claude conversation room.")
		return
	}

	login := ce.User.GetDefaultLogin()
	if login == nil {
		ce.Reply("You are not logged in.")
		return
	}

	client, ok := login.Client.(*ClaudeClient)
	if !ok || client == nil {
		ce.Reply("Failed to get client.")
		return
	}

	// Get stats before clearing
	msgCount, tokens, _ := client.GetConversationStats(ce.Portal.PortalKey.ID)

	// Clear the conversation (local history for API mode)
	client.ClearConversation(ce.Portal.PortalKey.ID)

	// Clear sidecar session ID from portal metadata (for sidecar mode)
	// This ensures the next message starts a fresh Agent SDK session
	meta, _ := ce.Portal.Metadata.(*PortalMetadata)
	if meta != nil && meta.SidecarSessionID != "" {
		oldSessionID := meta.SidecarSessionID
		meta.SidecarSessionID = ""
		ce.Portal.Metadata = meta
		if err := ce.Portal.Save(ce.Ctx); err != nil {
			c.Log.Warn().Err(err).Msg("Failed to clear sidecar session ID from portal metadata")
		} else {
			c.Log.Debug().
				Str("old_session_id", oldSessionID).
				Str("portal_id", string(ce.Portal.PortalKey.ID)).
				Msg("Cleared sidecar session ID from portal metadata")
		}
	}

	ce.Reply("Conversation cleared. Removed %d messages (~%d tokens).", msgCount, tokens)
}

// cmdStats shows conversation statistics.
func (c *ClaudeConnector) cmdStats(ce *commands.Event) {
	if ce.Portal == nil {
		ce.Reply("This command must be run in a Claude conversation room.")
		return
	}

	login := ce.User.GetDefaultLogin()
	if login == nil {
		ce.Reply("You are not logged in.")
		return
	}

	client, ok := login.Client.(*ClaudeClient)
	if !ok || client == nil {
		ce.Reply("Failed to get client.")
		return
	}

	meta, _ := ce.Portal.Metadata.(*PortalMetadata)
	isSidecarMode := client.MessageClient.GetClientType() == "sidecar"

	var sb strings.Builder
	sb.WriteString("**Conversation Statistics:**\n\n")

	// Model info
	model := c.Config.GetDefaultModel()
	if meta != nil && meta.Model != "" {
		model = meta.Model
	}

	displayName := claudeapi.GetModelDisplayName(model)
	if displayName != model {
		sb.WriteString(fmt.Sprintf("**Model:** %s (`%s`)\n", displayName, model))
	} else {
		sb.WriteString(fmt.Sprintf("**Model:** `%s`\n", model))
	}

	// Get stats - different sources for sidecar vs API mode
	var msgCount int
	var inputTokens, outputTokens int64
	var cacheCreationTokens, cacheReadTokens int64
	var compactionCount int
	var lastCompactionTime *float64
	var lastUsed time.Time

	if isSidecarMode {
		// Get stats from sidecar
		if sidecarClient, ok := client.MessageClient.(*sidecar.MessageClient); ok {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if stats, err := sidecarClient.GetSessionStats(ctx, string(ce.Portal.PortalKey.ID)); err == nil && stats != nil {
				msgCount = stats.MessageCount
				inputTokens = stats.InputTokens
				outputTokens = stats.OutputTokens
				cacheCreationTokens = stats.CacheCreationTokens
				cacheReadTokens = stats.CacheReadTokens
				compactionCount = stats.CompactionCount
				lastCompactionTime = stats.LastCompactionTime
				if stats.LastUsed > 0 {
					lastUsed = time.Unix(int64(stats.LastUsed), 0)
				}
			}
		}
	} else {
		// Get local conversation stats for API mode
		var estimatedTokens int
		msgCount, estimatedTokens, lastUsed = client.GetConversationStats(ce.Portal.PortalKey.ID)
		// API mode uses estimated tokens (no actual input/output breakdown from local tracking)
		outputTokens = int64(estimatedTokens)
	}

	// Conversation stats
	sb.WriteString(fmt.Sprintf("**Messages in context:** %d\n", msgCount))
	if isSidecarMode {
		// Sidecar provides actual token counts from Agent SDK
		totalTokens := inputTokens + outputTokens
		sb.WriteString(fmt.Sprintf("**Tokens used:** %d (in: %d, out: %d)\n",
			totalTokens, inputTokens, outputTokens))

		// Cache stats (prompt caching)
		if cacheCreationTokens > 0 || cacheReadTokens > 0 {
			sb.WriteString(fmt.Sprintf("**Cache tokens:** created: %d, read: %d\n",
				cacheCreationTokens, cacheReadTokens))
		}

		// Compaction info
		if compactionCount > 0 {
			sb.WriteString(fmt.Sprintf("**Context compactions:** %d", compactionCount))
			if lastCompactionTime != nil && *lastCompactionTime > 0 {
				lastCompaction := time.Unix(int64(*lastCompactionTime), 0)
				sb.WriteString(fmt.Sprintf(" (last: %s ago)", time.Since(lastCompaction).Round(time.Second)))
			}
			sb.WriteString("\n")
		}
	} else {
		// API mode stats
		sb.WriteString(fmt.Sprintf("**Estimated tokens:** ~%d\n", outputTokens))

		// Show API mode compaction and context usage from conversation manager
		_, estTokens, maxTokens, apiCompactionCount, _ := client.GetConversationFullStats(ce.Portal.PortalKey.ID)
		if apiCompactionCount > 0 {
			sb.WriteString(fmt.Sprintf("**Context compactions:** %d\n", apiCompactionCount))
		}
		if maxTokens > 0 && estTokens > 0 {
			usagePercent := (estTokens * 100) / maxTokens
			sb.WriteString(fmt.Sprintf("**Context usage:** ~%d%% of %dk limit\n", usagePercent, maxTokens/1000))
		}
	}

	if !lastUsed.IsZero() {
		sb.WriteString(fmt.Sprintf("**Last active:** %s ago\n", time.Since(lastUsed).Round(time.Second)))
	}

	// System prompt info
	if meta != nil && meta.SystemPrompt != "" {
		promptPreview := meta.SystemPrompt
		if len(promptPreview) > 100 {
			promptPreview = promptPreview[:97] + "..."
		}
		sb.WriteString(fmt.Sprintf("**Custom system prompt:** %s\n", promptPreview))
	}

	// Temperature info
	if meta != nil && meta.Temperature != nil {
		sb.WriteString(fmt.Sprintf("**Temperature:** %.2f\n", *meta.Temperature))
	} else {
		sb.WriteString(fmt.Sprintf("**Temperature:** %.2f (default)\n", c.Config.GetTemperature()))
	}

	// API metrics (local bridge metrics)
	if metrics := client.GetMetrics(); metrics != nil {
		totalReqs := metrics.TotalRequests.Load()
		failedReqs := metrics.FailedRequests.Load()
		localInputTokens := metrics.TotalInputTokens.Load()
		localOutputTokens := metrics.TotalOutputTokens.Load()
		localCacheCreation := metrics.TotalCacheCreationTokens.Load()
		localCacheRead := metrics.TotalCacheReadTokens.Load()

		sb.WriteString(fmt.Sprintf("\n**API Stats (this session):**\n"))
		sb.WriteString(fmt.Sprintf("• Requests: %d (%d failed)\n", totalReqs, failedReqs))
		if !isSidecarMode {
			// Only show token breakdown for API mode (sidecar already shows above)
			sb.WriteString(fmt.Sprintf("• Total tokens: %d (in: %d, out: %d)\n",
				localInputTokens+localOutputTokens, localInputTokens, localOutputTokens))
			// Show cache tokens if any were used
			if localCacheCreation > 0 || localCacheRead > 0 {
				sb.WriteString(fmt.Sprintf("• Cache tokens: created: %d, read: %d\n",
					localCacheCreation, localCacheRead))
			}
		}
		if avgDuration := metrics.GetAverageRequestDuration(); avgDuration > 0 {
			sb.WriteString(fmt.Sprintf("• Avg response time: %s\n", avgDuration.Round(time.Millisecond)))
		}
	}

	ce.Reply(sb.String())
}

// cmdSystem views or sets the system prompt.
func (c *ClaudeConnector) cmdSystem(ce *commands.Event) {
	if ce.Portal == nil {
		ce.Reply("This command must be run in a Claude conversation room.")
		return
	}

	meta, ok := ce.Portal.Metadata.(*PortalMetadata)
	if !ok || meta == nil {
		ce.Reply("Failed to get room metadata.")
		return
	}

	// If no argument, show current system prompt
	if len(ce.Args) == 0 {
		currentPrompt := meta.SystemPrompt
		if currentPrompt == "" {
			currentPrompt = c.Config.GetSystemPrompt()
			if currentPrompt == "" {
				ce.Reply("No system prompt is set. Use `system <prompt>` to set one.")
			} else {
				ce.Reply("**Current system prompt (default):**\n\n%s", currentPrompt)
			}
		} else {
			ce.Reply("**Current system prompt:**\n\n%s\n\nUse `system clear` to reset to default.", currentPrompt)
		}
		return
	}

	// Check for clear command
	if strings.ToLower(ce.Args[0]) == "clear" {
		oldPrompt := meta.SystemPrompt
		meta.SystemPrompt = ""
		if err := ce.Portal.Save(ce.Ctx); err != nil {
			meta.SystemPrompt = oldPrompt // Rollback in-memory state on save failure
			ce.Reply("Failed to clear system prompt: %v", err)
			return
		}
		ce.Reply("System prompt cleared. Using default.")
		return
	}

	// Set new system prompt - save old value for rollback on failure
	newPrompt := strings.Join(ce.Args, " ")
	oldPrompt := meta.SystemPrompt
	meta.SystemPrompt = newPrompt
	if err := ce.Portal.Save(ce.Ctx); err != nil {
		meta.SystemPrompt = oldPrompt // Rollback in-memory state on save failure
		ce.Reply("Failed to save system prompt: %v", err)
		return
	}

	ce.Reply("System prompt updated.")
}

// cmdMention toggles mention-only mode.
func (c *ClaudeConnector) cmdMention(ce *commands.Event) {
	if ce.Portal == nil {
		ce.Reply("This command must be run in a Claude conversation room.")
		return
	}

	meta, ok := ce.Portal.Metadata.(*PortalMetadata)
	if !ok || meta == nil {
		ce.Reply("Failed to get room metadata.")
		return
	}

	// If no argument, show current status
	if len(ce.Args) == 0 {
		if meta.MentionOnly {
			ce.Reply("**Mention-only mode:** ON\n\nClaude only responds when @mentioned.\n\nUse `mention off` to respond to all messages.")
		} else {
			ce.Reply("**Mention-only mode:** OFF\n\nClaude responds to all messages.\n\nUse `mention on` to only respond when @mentioned.")
		}
		return
	}

	// Parse argument
	arg := strings.ToLower(ce.Args[0])
	var newValue bool
	switch arg {
	case "on", "true", "yes", "1", "enable", "enabled":
		newValue = true
	case "off", "false", "no", "0", "disable", "disabled":
		newValue = false
	case "toggle":
		newValue = !meta.MentionOnly
	default:
		ce.Reply("Invalid argument. Use `mention on`, `mention off`, or `mention toggle`.")
		return
	}

	oldValue := meta.MentionOnly
	meta.MentionOnly = newValue
	if err := ce.Portal.Save(ce.Ctx); err != nil {
		meta.MentionOnly = oldValue
		ce.Reply("Failed to save setting: %v", err)
		return
	}

	if newValue {
		ce.Reply("Mention-only mode **enabled**. Claude will only respond when @mentioned.")
	} else {
		ce.Reply("Mention-only mode **disabled**. Claude will respond to all messages.")
	}
}

// cmdJoin adds Claude to the current room by creating a bridge portal.
// If Claude is already in the room, this re-configures the relay.
func (c *ClaudeConnector) cmdJoin(ce *commands.Event) {
	c.Log.Debug().
		Bool("portal_exists", ce.Portal != nil).
		Strs("args", ce.Args).
		Msg("Join command: starting")

	// If already a portal, update model and ghost, then re-configure relay
	if ce.Portal != nil {
		c.Log.Debug().
			Str("portal_id", string(ce.Portal.PortalKey.ID)).
			Str("portal_mxid", string(ce.Portal.MXID)).
			Msg("Join command: portal already exists, updating model and relay")

		login := ce.User.GetDefaultLogin()
		if login == nil {
			ce.Reply("You are not logged in.")
			return
		}

		ctx, cancel := context.WithTimeout(ce.Ctx, 15*time.Second)
		defer cancel()

		// Get existing metadata to check current model
		portalMeta, _ := ce.Portal.Metadata.(*PortalMetadata)
		oldModel := ""
		if portalMeta != nil {
			oldModel = portalMeta.Model
		}
		if oldModel == "" {
			oldModel = c.Config.GetDefaultModel()
		}

		// Resolve model from args using shared helper
		model := c.Config.GetDefaultModel()
		if len(ce.Args) > 0 {
			modelArg := strings.Join(ce.Args, "-")
			resolved, err := c.resolveModelArg(ctx, ce, modelArg)
			if err != nil {
				ce.Reply("Failed to resolve model: %v\n\nUse `opus`, `sonnet`, `haiku`, or a full model ID.", err)
				return
			}
			model = resolved
		}

		c.Log.Debug().
			Str("old_model", oldModel).
			Str("new_model", model).
			Msg("Join command: updating model in existing portal")

		// Update portal metadata with new model
		if portalMeta == nil {
			portalMeta = &PortalMetadata{}
		}
		portalMeta.ConversationName = fmt.Sprintf("Claude (%s)", model)
		portalMeta.Model = model
		ce.Portal.Metadata = portalMeta

		if err := ce.Portal.Save(ctx); err != nil {
			ce.Reply("Failed to save portal: %v", err)
			return
		}

		// Swap ghosts if model changed (uses shared helper)
		if err := c.swapGhosts(ctx, ce.RoomID, oldModel, model); err != nil {
			ce.Reply("Failed to update Claude ghost: %v", err)
			return
		}

		// Re-set relay if enabled
		if c.br.Config.Relay.Enabled {
			if err := ce.Portal.SetRelay(ctx, login); err != nil {
				ce.Reply("Failed to set relay: %v", err)
				return
			}
		}

		displayName := claudeapi.GetModelDisplayName(model)
		if oldModel != model {
			ce.Reply("✓ **%s** has joined the room! (replaced %s)\n\nUse `model` to change models, `mention on` for mention-only mode, or `clear` to reset conversation.", displayName, claudeapi.GetModelDisplayName(oldModel))
		} else {
			ce.Reply("✓ **%s** is in the room! Relay updated.\n\nUse `model` to change models, `mention on` for mention-only mode, or `clear` to reset conversation.", displayName)
		}
		return
	}
	c.Log.Debug().Msg("Join command: no existing portal, creating new one")

	login := ce.User.GetDefaultLogin()
	if login == nil {
		ce.Reply("You are not logged in.")
		return
	}

	client, ok := login.Client.(*ClaudeClient)
	if !ok || client == nil {
		ce.Reply("Failed to get client.")
		return
	}

	// Determine model to use
	model := c.Config.GetDefaultModel()
	c.Log.Debug().
		Strs("args", ce.Args).
		Str("default_model", model).
		Msg("Join command: parsing model from args")

	if len(ce.Args) > 0 {
		requestedModel := strings.ToLower(strings.Join(ce.Args, "-"))
		c.Log.Debug().
			Str("requested_model", requestedModel).
			Msg("Join command: processing requested model")

		switch requestedModel {
		case "opus", "claude-opus":
			model = "opus"
		case "sonnet", "claude-sonnet":
			model = "sonnet"
		case "haiku", "claude-haiku":
			model = "haiku"
		default:
			// Assume it's a full model ID
			if strings.Contains(requestedModel, "claude") {
				model = requestedModel
			} else {
				ce.Reply("Unknown model: %s. Use `opus`, `sonnet`, `haiku`, or a full model ID.", requestedModel)
				return
			}
		}
		c.Log.Debug().
			Str("resolved_model", model).
			Msg("Join command: resolved model")
	}

	// Get the room ID from the event
	roomID := ce.RoomID
	if roomID == "" {
		ce.Reply("Could not determine room ID.")
		return
	}

	c.Log.Info().
		Str("room_id", string(roomID)).
		Str("model", model).
		Str("user", string(ce.User.MXID)).
		Msg("Join command: adding Claude to room")

	// Create a unique conversation/portal ID based on the room
	conversationID := fmt.Sprintf("room_%s", roomID)
	portalKey := MakeClaudePortalKey(conversationID)

	// Get or create the portal
	ctx := ce.Ctx
	portal, err := c.br.GetPortalByKey(ctx, portalKey)
	if err != nil {
		ce.Reply("Failed to get portal: %v", err)
		return
	}

	// Check if this portal already has a different room associated
	if portal.MXID != "" && portal.MXID != roomID {
		ce.Reply("This portal is associated with a different room. Please use a new conversation.")
		return
	}

	// Get the ghost for this model (with proper metadata)
	ghostID := c.MakeClaudeGhostID(model)
	c.Log.Debug().
		Str("model", model).
		Str("ghost_id", string(ghostID)).
		Msg("Join command: resolved ghost ID from model")

	ghost, err := c.GetOrUpdateGhost(ctx, ghostID, model)
	if err != nil {
		ce.Reply("Failed to get Claude ghost: %v", err)
		return
	}

	c.Log.Debug().
		Str("ghost_mxid", ghost.Intent.GetMXID().String()).
		Msg("Join command: got ghost intent")

	// Set up portal metadata - always update the model even if portal exists
	chatName := fmt.Sprintf("Claude (%s)", model)

	// Get existing metadata or create new
	portalMeta, _ := portal.Metadata.(*PortalMetadata)
	if portalMeta == nil {
		portalMeta = &PortalMetadata{}
	}
	portalMeta.ConversationName = chatName
	portalMeta.Model = model

	// Update the portal to use this room
	needsSave := false
	if portal.MXID == "" {
		// Link the existing Matrix room to this portal
		portal.MXID = roomID
		needsSave = true
	}

	// Always update metadata (model may have changed)
	portal.Metadata = portalMeta
	needsSave = true

	if needsSave {
		if err := portal.Save(ctx); err != nil {
			ce.Reply("Failed to save portal: %v", err)
			return
		}
		c.Log.Debug().
			Str("model", model).
			Str("portal_id", string(portal.PortalKey.ID)).
			Msg("Join command: saved portal metadata with model")
	}

	// Have the ghost join the room
	c.Log.Debug().
		Str("ghost_mxid", ghost.Intent.GetMXID().String()).
		Str("room_id", string(roomID)).
		Msg("Join command: attempting to have ghost join room")

	err = ghost.Intent.EnsureJoined(ctx, roomID)
	if err != nil {
		c.Log.Warn().Err(err).
			Str("ghost_mxid", ghost.Intent.GetMXID().String()).
			Str("room_id", string(roomID)).
			Msg("Failed to join room with ghost, trying invite first")

		// Try to invite and then join
		botIntent := c.br.Bot
		c.Log.Debug().
			Str("ghost_mxid", ghost.Intent.GetMXID().String()).
			Msg("Join command: attempting to invite ghost via bot")

		err = botIntent.EnsureInvited(ctx, roomID, ghost.Intent.GetMXID())
		if err != nil {
			c.Log.Error().Err(err).
				Str("ghost_mxid", ghost.Intent.GetMXID().String()).
				Msg("Join command: failed to invite ghost")
			ce.Reply("Failed to invite Claude to this room: %v\n\nMake sure the bot has permission to invite users.", err)
			return
		}

		c.Log.Debug().Msg("Join command: invite succeeded, attempting join")
		err = ghost.Intent.EnsureJoined(ctx, roomID)
		if err != nil {
			c.Log.Error().Err(err).Msg("Join command: ghost failed to join after invite")
			ce.Reply("Claude was invited but failed to join: %v", err)
			return
		}
	}
	c.Log.Debug().Msg("Join command: ghost successfully joined room")

	// Auto-set relay so other users in the room can also talk to Claude
	// This uses the joining user's login to relay messages from non-logged-in users
	if c.br.Config.Relay.Enabled {
		if err := portal.SetRelay(ctx, login); err != nil {
			c.Log.Warn().Err(err).Msg("Failed to set relay for portal")
			// Non-fatal - continue but warn user
		} else {
			c.Log.Debug().
				Str("relay_login", string(login.ID)).
				Msg("Auto-configured relay for portal")
		}
	}

	displayName := claudeapi.GetModelDisplayName(model)
	if c.br.Config.Relay.Enabled {
		ce.Reply("✓ **%s** has joined the room!\n\nAll users in this room can now chat with Claude (messages relayed through your account).\n\nUse `model` to change models, `system` to set a custom prompt, `mention on` for mention-only mode, or `clear` to reset conversation.", displayName)
	} else {
		ce.Reply("✓ **%s** has joined the room!\n\n⚠️ **Note:** Relay mode is disabled. Only you can talk to Claude. Enable `relay.enabled: true` in bridge config for multi-user support.\n\nUse `model` to change models, `system` to set a custom prompt, or `clear` to reset the conversation.", displayName)
	}

	c.Log.Info().
		Str("room_id", string(roomID)).
		Str("model", model).
		Str("ghost_id", string(ghostID)).
		Bool("relay_enabled", c.br.Config.Relay.Enabled).
		Msg("Successfully added Claude to room")
}

// cmdTemperature views or sets the temperature.
func (c *ClaudeConnector) cmdTemperature(ce *commands.Event) {
	if ce.Portal == nil {
		ce.Reply("This command must be run in a Claude conversation room.")
		return
	}

	meta, ok := ce.Portal.Metadata.(*PortalMetadata)
	if !ok || meta == nil {
		ce.Reply("Failed to get room metadata.")
		return
	}

	// If no argument, show current temperature
	if len(ce.Args) == 0 {
		if meta.Temperature != nil {
			ce.Reply("**Current temperature:** %.2f\n\nUse `temperature <0-1>` to change, or `temperature reset` to use default.", *meta.Temperature)
		} else {
			ce.Reply("**Current temperature:** %.2f (default)\n\nUse `temperature <0-1>` to change.", c.Config.GetTemperature())
		}
		return
	}

	// Check for reset command
	if strings.ToLower(ce.Args[0]) == "reset" || strings.ToLower(ce.Args[0]) == "clear" {
		oldTemp := meta.Temperature
		meta.Temperature = nil
		if err := ce.Portal.Save(ce.Ctx); err != nil {
			meta.Temperature = oldTemp // Rollback in-memory state on save failure
			ce.Reply("Failed to reset temperature: %v", err)
			return
		}
		ce.Reply("Temperature reset to default (%.2f).", c.Config.GetTemperature())
		return
	}

	// Parse temperature value
	var temp float64
	if _, err := fmt.Sscanf(ce.Args[0], "%f", &temp); err != nil {
		ce.Reply("Invalid temperature value. Use a number between 0 and 1.")
		return
	}

	if temp < 0 || temp > 1 {
		ce.Reply("Temperature must be between 0 and 1.")
		return
	}

	oldTemp := meta.Temperature
	meta.Temperature = &temp
	if err := ce.Portal.Save(ce.Ctx); err != nil {
		meta.Temperature = oldTemp // Rollback in-memory state on save failure
		ce.Reply("Failed to save temperature: %v", err)
		return
	}

	ce.Reply("Temperature set to %.2f.", temp)
}

// cmdRemoveGhost removes a bridge ghost from the current room.
// This is an admin-only command for cleaning up stale/buggy ghost users.
func (c *ClaudeConnector) cmdRemoveGhost(ce *commands.Event) {
	if len(ce.Args) == 0 {
		ce.Reply("Usage: `remove-ghost <@user:server>`\n\nExample: `remove-ghost @claude_unknown:example.com`")
		return
	}

	// Parse the Matrix user ID
	userIDStr := ce.Args[0]
	if !strings.HasPrefix(userIDStr, "@") {
		ce.Reply("Invalid Matrix user ID. Must start with @")
		return
	}

	userID := id.UserID(userIDStr)

	// Verify it's a ghost controlled by this bridge (matches appservice namespace)
	// The ghost should have localpart starting with the bridge's username template prefix
	if _, isGhost := c.br.Matrix.ParseGhostMXID(userID); !isGhost {
		ce.Reply("User `%s` is not a ghost controlled by this bridge.", userID)
		return
	}

	// Get the room ID - prefer portal MXID, fall back to event room ID
	roomID := ce.RoomID
	if ce.Portal != nil && ce.Portal.MXID != "" {
		roomID = ce.Portal.MXID
	}

	if roomID == "" {
		ce.Reply("Could not determine room ID.")
		return
	}

	// Get the ghost intent and make it leave
	// We need to use the appservice to impersonate the ghost
	matrixConn, ok := c.br.Matrix.(*matrix.Connector)
	if !ok {
		ce.Reply("Failed to access Matrix connector.")
		return
	}

	// Create an intent for the ghost user
	ghostIntent := matrixConn.AS.Intent(userID)

	ctx, cancel := context.WithTimeout(ce.Ctx, 30*time.Second)
	defer cancel()

	// Make the ghost leave the room
	if _, err := ghostIntent.LeaveRoom(ctx, roomID); err != nil {
		ce.Reply("Failed to remove ghost from room: %v", err)
		return
	}

	c.Log.Info().
		Str("ghost", string(userID)).
		Str("room", string(roomID)).
		Str("admin", string(ce.User.MXID)).
		Msg("Admin removed ghost from room")

	ce.Reply("Successfully removed `%s` from this room.", userID)
}
