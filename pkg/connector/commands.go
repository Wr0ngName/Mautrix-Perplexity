package connector

import (
	"context"
	"fmt"
	"strings"
	"time"

	"go.mau.fi/mautrix-perplexity/pkg/perplexityapi"
	"go.mau.fi/mautrix-perplexity/pkg/sidecar"
	"maunium.net/go/mautrix/bridgev2/commands"
	"maunium.net/go/mautrix/bridgev2/matrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

// resolveModelArg resolves a model argument (sonar, sonar-pro, etc.) to the actual model ID.
func (c *PerplexityConnector) resolveModelArg(ctx context.Context, ce *commands.Event, modelArg string) (string, error) {
	modelArg = strings.ToLower(modelArg)

	switch modelArg {
	case "sonar", "perplexity":
		return perplexityapi.ModelSonar, nil
	case "sonar-pro", "pro":
		return perplexityapi.ModelSonarPro, nil
	case "sonar-reasoning", "reasoning":
		return perplexityapi.ModelSonarReasoning, nil
	case "sonar-reasoning-pro", "reasoning-pro":
		return perplexityapi.ModelSonarReasoningPro, nil
	default:
		// Validate model ID format
		if err := ValidateModelID(modelArg); err != nil {
			return "", fmt.Errorf("invalid model ID: %w", err)
		}
		return modelArg, nil
	}
}

// swapGhosts ensures the correct ghost is in the room for the new model.
// If oldModel and newModel have different families, the old ghost is removed.
// The new ghost is always ensured to be in the room.
func (c *PerplexityConnector) swapGhosts(ctx context.Context, roomID id.RoomID, oldModel, newModel string) error {
	oldFamily := perplexityapi.GetModelFamily(oldModel)
	newFamily := perplexityapi.GetModelFamily(newModel)
	familyChanged := oldFamily != newFamily

	newGhostID := c.MakePerplexityGhostID(newModel)

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
		oldGhostID := c.MakePerplexityGhostID(oldModel)

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

// RegisterCommands registers custom commands for the Perplexity AI bridge.
func (c *PerplexityConnector) RegisterCommands(proc *commands.Processor) {
	proc.AddHandlers(
		&commands.FullHandler{
			Func:    c.cmdJoin,
			Name:    "join",
			Aliases: []string{"add", "invite"},
			Help: commands.HelpMeta{
				Section:     commands.HelpSectionGeneral,
				Description: "Add Perplexity to the current room (creates a bridge portal)",
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
				Description: "View or change the Perplexity model for this conversation",
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
				Description: "List available Perplexity models",
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
				Description: "View or set the temperature (0-2) for this conversation",
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
				Description: "Toggle mention-only mode (Perplexity only responds when @mentioned)",
				Args:        "[on|off]",
			},
			RequiresLogin:  true,
			RequiresPortal: true,
		},
		&commands.FullHandler{
			Func:    c.cmdConversation,
			Name:    "conversation",
			Aliases: []string{"conv", "history", "context"},
			Help: commands.HelpMeta{
				Section:     commands.HelpSectionGeneral,
				Description: "Toggle conversation mode (maintain message history for multi-turn conversations)",
				Args:        "[on|off|toggle]",
			},
			RequiresLogin:  true,
			RequiresPortal: true,
		},
		&commands.FullHandler{
			Func:    c.cmdWeb,
			Name:    "web",
			Aliases: []string{"search", "web-search"},
			Help: commands.HelpMeta{
				Section:     commands.HelpSectionGeneral,
				Description: "Configure web search options (domains, recency, dates, images, mode, location)",
				Args:        "[domains|recency|after|before|images|context|mode|location|clear] [value]",
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
func (c *PerplexityConnector) getAPIKeyFromLogin(ce *commands.Event) string {
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

// cmdModel views or changes the Perplexity model for a conversation.
func (c *PerplexityConnector) cmdModel(ce *commands.Event) {
	if ce.Portal == nil {
		ce.Reply("This command must be run in a Perplexity conversation room.")
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

		// Get context size for the model
		contextSize := perplexityapi.GetContextSize(currentModel)
		sb.WriteString(fmt.Sprintf("**Context window:** %d tokens\n", contextSize))

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

	ce.Reply("Model changed to `%s`", newModel)
}

// cmdModels lists available Perplexity models.
func (c *PerplexityConnector) cmdModels(ce *commands.Event) {
	var sb strings.Builder
	sb.WriteString("**Available Perplexity Models:**\n\n")

	defaultModel := c.Config.GetDefaultModel()

	sb.WriteString("**Sonar:**\n")
	sb.WriteString(fmt.Sprintf("• `sonar` - Perplexity Sonar (fast, cost-effective)"))
	if defaultModel == perplexityapi.ModelSonar {
		sb.WriteString(" *(default)*")
	}
	sb.WriteString("\n")
	sb.WriteString(fmt.Sprintf("  Context: %dk tokens\n\n", perplexityapi.ModelContextSizes[perplexityapi.ModelSonar]/1000))

	sb.WriteString("**Sonar Pro:**\n")
	sb.WriteString(fmt.Sprintf("• `sonar-pro` - Perplexity Sonar Pro (more capable)"))
	if defaultModel == perplexityapi.ModelSonarPro {
		sb.WriteString(" *(default)*")
	}
	sb.WriteString("\n")
	sb.WriteString(fmt.Sprintf("  Context: %dk tokens\n\n", perplexityapi.ModelContextSizes[perplexityapi.ModelSonarPro]/1000))

	sb.WriteString("**Sonar Reasoning:**\n")
	sb.WriteString(fmt.Sprintf("• `sonar-reasoning` - Perplexity Sonar Reasoning (chain-of-thought)"))
	if defaultModel == perplexityapi.ModelSonarReasoning {
		sb.WriteString(" *(default)*")
	}
	sb.WriteString("\n")
	sb.WriteString(fmt.Sprintf("  Context: %dk tokens\n\n", perplexityapi.ModelContextSizes[perplexityapi.ModelSonarReasoning]/1000))

	sb.WriteString("**Sonar Reasoning Pro:**\n")
	sb.WriteString(fmt.Sprintf("• `sonar-reasoning-pro` - Perplexity Sonar Reasoning Pro (most capable)"))
	if defaultModel == perplexityapi.ModelSonarReasoningPro {
		sb.WriteString(" *(default)*")
	}
	sb.WriteString("\n")
	sb.WriteString(fmt.Sprintf("  Context: %dk tokens\n\n", perplexityapi.ModelContextSizes[perplexityapi.ModelSonarReasoningPro]/1000))

	sb.WriteString("Use `model <model-id>` to switch models.\n")
	sb.WriteString("Shortcuts: `sonar`, `sonar-pro`, `sonar-reasoning`, `sonar-reasoning-pro`")

	ce.Reply(sb.String())
}

// cmdClear clears the conversation history.
func (c *PerplexityConnector) cmdClear(ce *commands.Event) {
	if ce.Portal == nil {
		ce.Reply("This command must be run in a Perplexity conversation room.")
		return
	}

	login := ce.User.GetDefaultLogin()
	if login == nil {
		ce.Reply("You are not logged in.")
		return
	}

	client, ok := login.Client.(*PerplexityClient)
	if !ok || client == nil {
		ce.Reply("Failed to get client.")
		return
	}

	// Get stats before clearing
	msgCount, tokens, _ := client.GetConversationStats(ce.Portal.PortalKey.ID)

	// Clear the conversation via sidecar
	client.ClearConversation(ce.Portal.PortalKey.ID)

	ce.Reply("Conversation cleared. Removed %d messages (~%d tokens).", msgCount, tokens)
}

// cmdStats shows conversation statistics.
func (c *PerplexityConnector) cmdStats(ce *commands.Event) {
	if ce.Portal == nil {
		ce.Reply("This command must be run in a Perplexity conversation room.")
		return
	}

	login := ce.User.GetDefaultLogin()
	if login == nil {
		ce.Reply("You are not logged in.")
		return
	}

	client, ok := login.Client.(*PerplexityClient)
	if !ok || client == nil {
		ce.Reply("Failed to get client.")
		return
	}

	meta, _ := ce.Portal.Metadata.(*PortalMetadata)

	var sb strings.Builder
	sb.WriteString("**Conversation Statistics:**\n\n")

	// Model info
	model := c.Config.GetDefaultModel()
	if meta != nil && meta.Model != "" {
		model = meta.Model
	}

	sb.WriteString(fmt.Sprintf("**Model:** `%s`\n", model))

	// Get stats from sidecar
	var msgCount int
	var inputTokens, outputTokens int64
	var lastUsed time.Time

	if sidecarClient, ok := client.MessageClient.(*sidecar.MessageClient); ok {
		ctx, cancel := context.WithTimeout(ce.Ctx, 5*time.Second)
		defer cancel()
		if stats, err := sidecarClient.GetSessionStats(ctx, string(ce.Portal.PortalKey.ID)); err == nil && stats != nil {
			msgCount = stats.MessageCount
			inputTokens = stats.InputTokens
			outputTokens = stats.OutputTokens
			if stats.LastUsed > 0 {
				lastUsed = time.Unix(int64(stats.LastUsed), 0)
			}
		}
	}

	// Conversation stats
	sb.WriteString(fmt.Sprintf("**Messages in context:** %d\n", msgCount))
	totalTokens := inputTokens + outputTokens
	sb.WriteString(fmt.Sprintf("**Tokens used:** %d (in: %d, out: %d)\n",
		totalTokens, inputTokens, outputTokens))

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

	// API metrics
	if metrics := client.GetMetrics(); metrics != nil {
		totalReqs := metrics.TotalRequests.Load()
		failedReqs := metrics.FailedRequests.Load()

		sb.WriteString(fmt.Sprintf("\n**API Stats (this session):**\n"))
		sb.WriteString(fmt.Sprintf("• Requests: %d (%d failed)\n", totalReqs, failedReqs))
		if avgDuration := metrics.GetAverageRequestDuration(); avgDuration > 0 {
			sb.WriteString(fmt.Sprintf("• Avg response time: %s\n", avgDuration.Round(time.Millisecond)))
		}
	}

	ce.Reply(sb.String())
}

// cmdSystem views or sets the system prompt.
func (c *PerplexityConnector) cmdSystem(ce *commands.Event) {
	if ce.Portal == nil {
		ce.Reply("This command must be run in a Perplexity conversation room.")
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
func (c *PerplexityConnector) cmdMention(ce *commands.Event) {
	if ce.Portal == nil {
		ce.Reply("This command must be run in a Perplexity conversation room.")
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
			ce.Reply("**Mention-only mode:** ON\n\nPerplexity only responds when @mentioned.\n\nUse `mention off` to respond to all messages.")
		} else {
			ce.Reply("**Mention-only mode:** OFF\n\nPerplexity responds to all messages.\n\nUse `mention on` to only respond when @mentioned.")
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
		ce.Reply("Mention-only mode **enabled**. Perplexity will only respond when @mentioned.")
	} else {
		ce.Reply("Mention-only mode **disabled**. Perplexity will respond to all messages.")
	}
}

// cmdConversation toggles conversation mode (multi-turn history).
func (c *PerplexityConnector) cmdConversation(ce *commands.Event) {
	if ce.Portal == nil {
		ce.Reply("This command must be run in a Perplexity conversation room.")
		return
	}

	meta, ok := ce.Portal.Metadata.(*PortalMetadata)
	if !ok || meta == nil {
		ce.Reply("Failed to get room metadata.")
		return
	}

	// If no argument, show current status
	if len(ce.Args) == 0 {
		if meta.ConversationMode {
			ce.Reply("**Conversation mode:** ON\n\nPerplexity maintains message history for multi-turn conversations.\n\nUse `conversation off` to disable (each message is independent).\nUse `clear` to reset the conversation history.")
		} else {
			ce.Reply("**Conversation mode:** OFF\n\nEach message is independent (no history). This is ideal for search queries.\n\nUse `conversation on` to enable multi-turn conversations.")
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
		newValue = !meta.ConversationMode
	default:
		ce.Reply("Invalid argument. Use `conversation on`, `conversation off`, or `conversation toggle`.")
		return
	}

	oldValue := meta.ConversationMode
	meta.ConversationMode = newValue
	if err := ce.Portal.Save(ce.Ctx); err != nil {
		meta.ConversationMode = oldValue
		ce.Reply("Failed to save setting: %v", err)
		return
	}

	if newValue {
		ce.Reply("Conversation mode **enabled**. Perplexity will remember previous messages in this room.\n\nUse `clear` to reset the conversation history.")
	} else {
		// Clear history when disabling conversation mode
		login := ce.User.GetDefaultLogin()
		if login != nil {
			if client, ok := login.Client.(*PerplexityClient); ok && client != nil {
				client.ClearConversation(ce.Portal.PortalKey.ID)
			}
		}
		ce.Reply("Conversation mode **disabled**. Each message is now independent (history cleared).")
	}
}

// cmdWeb configures web search options.
func (c *PerplexityConnector) cmdWeb(ce *commands.Event) {
	if ce.Portal == nil {
		ce.Reply("This command must be run in a Perplexity conversation room.")
		return
	}

	meta, ok := ce.Portal.Metadata.(*PortalMetadata)
	if !ok || meta == nil {
		ce.Reply("Failed to get room metadata.")
		return
	}

	// If no argument, show current settings
	if len(ce.Args) == 0 {
		var sb strings.Builder
		sb.WriteString("**Web Search Settings**\n\n")

		if len(meta.WebSearchDomains) > 0 {
			sb.WriteString("**Domain filter:** ")
			sb.WriteString(strings.Join(meta.WebSearchDomains, ", "))
			sb.WriteString("\n")
		} else {
			sb.WriteString("**Domain filter:** none (search all domains)\n")
		}

		if meta.WebSearchRecency != "" {
			sb.WriteString(fmt.Sprintf("**Recency filter:** %s\n", meta.WebSearchRecency))
		} else {
			sb.WriteString("**Recency filter:** none (all time)\n")
		}

		if meta.WebSearchAfterDate != "" || meta.WebSearchBeforeDate != "" {
			sb.WriteString("**Date range:** ")
			if meta.WebSearchAfterDate != "" {
				sb.WriteString(fmt.Sprintf("after %s", meta.WebSearchAfterDate))
			}
			if meta.WebSearchAfterDate != "" && meta.WebSearchBeforeDate != "" {
				sb.WriteString(", ")
			}
			if meta.WebSearchBeforeDate != "" {
				sb.WriteString(fmt.Sprintf("before %s", meta.WebSearchBeforeDate))
			}
			sb.WriteString("\n")
		}

		if meta.ReturnImages != nil && *meta.ReturnImages {
			sb.WriteString("**Images:** enabled\n")
		}

		if meta.SearchContextSize != "" {
			sb.WriteString(fmt.Sprintf("**Context size:** %s\n", meta.SearchContextSize))
		}

		if meta.SearchMode != "" {
			sb.WriteString(fmt.Sprintf("**Search mode:** %s\n", meta.SearchMode))
		}

		if meta.UserLocationCity != "" || meta.UserLocationCountry != "" {
			sb.WriteString("**Location:** ")
			parts := []string{}
			if meta.UserLocationCity != "" {
				parts = append(parts, meta.UserLocationCity)
			}
			if meta.UserLocationRegion != "" {
				parts = append(parts, meta.UserLocationRegion)
			}
			if meta.UserLocationCountry != "" {
				parts = append(parts, meta.UserLocationCountry)
			}
			sb.WriteString(strings.Join(parts, ", "))
			sb.WriteString("\n")
		}

		sb.WriteString("\n**Usage:**\n")
		sb.WriteString("- `web domains <domain1,domain2,...>` - Only search these domains\n")
		sb.WriteString("- `web recency day|week|month|year` - Limit to recent results\n")
		sb.WriteString("- `web after <MM/DD/YYYY>` - Search after date\n")
		sb.WriteString("- `web before <MM/DD/YYYY>` - Search before date\n")
		sb.WriteString("- `web images on|off` - Include images (Tier-2+)\n")
		sb.WriteString("- `web context low|medium|high` - Search context size\n")
		sb.WriteString("- `web mode academic|web` - Search mode\n")
		sb.WriteString("- `web location <city,region,country>` - Location for local results\n")
		sb.WriteString("- `web clear` - Remove all filters\n")
		ce.Reply(sb.String())
		return
	}

	subCmd := strings.ToLower(ce.Args[0])
	switch subCmd {
	case "domains", "domain", "sites", "site":
		if len(ce.Args) < 2 {
			if len(meta.WebSearchDomains) > 0 {
				ce.Reply("**Current domain filter:** %s\n\nTo change: `web domains domain1.com,domain2.org`\nTo clear: `web domains clear`", strings.Join(meta.WebSearchDomains, ", "))
			} else {
				ce.Reply("No domain filter set. To add: `web domains domain1.com,domain2.org`")
			}
			return
		}

		arg := ce.Args[1]
		if arg == "clear" || arg == "none" || arg == "off" {
			oldDomains := meta.WebSearchDomains
			meta.WebSearchDomains = nil
			if err := ce.Portal.Save(ce.Ctx); err != nil {
				meta.WebSearchDomains = oldDomains
				ce.Reply("Failed to save setting: %v", err)
				return
			}
			ce.Reply("Domain filter **cleared**. Perplexity will search all domains.")
			return
		}

		// Parse comma-separated domains
		domains := strings.Split(arg, ",")
		cleanDomains := make([]string, 0, len(domains))
		for _, d := range domains {
			d = strings.TrimSpace(strings.ToLower(d))
			if d != "" {
				cleanDomains = append(cleanDomains, d)
			}
		}

		if len(cleanDomains) == 0 {
			ce.Reply("No valid domains provided. Example: `web domains example.com,docs.example.org`")
			return
		}

		oldDomains := meta.WebSearchDomains
		meta.WebSearchDomains = cleanDomains
		if err := ce.Portal.Save(ce.Ctx); err != nil {
			meta.WebSearchDomains = oldDomains
			ce.Reply("Failed to save setting: %v", err)
			return
		}
		ce.Reply("Domain filter set to: **%s**\n\nPerplexity will only search these domains.", strings.Join(cleanDomains, ", "))

	case "recency", "recent", "time":
		if len(ce.Args) < 2 {
			if meta.WebSearchRecency != "" {
				ce.Reply("**Current recency filter:** %s\n\nTo change: `web recency day|week|month|year`\nTo clear: `web recency clear`", meta.WebSearchRecency)
			} else {
				ce.Reply("No recency filter set (searching all time). To add: `web recency day|week|month|year`")
			}
			return
		}

		arg := strings.ToLower(ce.Args[1])
		if arg == "clear" || arg == "none" || arg == "off" || arg == "all" {
			oldRecency := meta.WebSearchRecency
			meta.WebSearchRecency = ""
			if err := ce.Portal.Save(ce.Ctx); err != nil {
				meta.WebSearchRecency = oldRecency
				ce.Reply("Failed to save setting: %v", err)
				return
			}
			ce.Reply("Recency filter **cleared**. Perplexity will search all time.")
			return
		}

		validRecency := map[string]bool{"day": true, "week": true, "month": true, "year": true}
		if !validRecency[arg] {
			ce.Reply("Invalid recency value. Use: `day`, `week`, `month`, or `year`.")
			return
		}

		// Check for conflict with date filters (mutual exclusion)
		var clearedFilters []string
		if meta.WebSearchAfterDate != "" {
			clearedFilters = append(clearedFilters, fmt.Sprintf("after-date (%s)", meta.WebSearchAfterDate))
			meta.WebSearchAfterDate = ""
		}
		if meta.WebSearchBeforeDate != "" {
			clearedFilters = append(clearedFilters, fmt.Sprintf("before-date (%s)", meta.WebSearchBeforeDate))
			meta.WebSearchBeforeDate = ""
		}

		oldRecency := meta.WebSearchRecency
		meta.WebSearchRecency = arg
		if err := ce.Portal.Save(ce.Ctx); err != nil {
			meta.WebSearchRecency = oldRecency
			ce.Reply("Failed to save setting: %v", err)
			return
		}

		reply := fmt.Sprintf("Recency filter set to: **%s**\n\nPerplexity will prioritize results from the last %s.", arg, arg)
		if len(clearedFilters) > 0 {
			reply += fmt.Sprintf("\n\n⚠️ **Cleared conflicting filters:** %s (cannot combine recency with date filters)", strings.Join(clearedFilters, ", "))
		}
		ce.Reply(reply)

	case "after", "from", "since":
		if len(ce.Args) < 2 {
			if meta.WebSearchAfterDate != "" {
				ce.Reply("**Current after-date filter:** %s\n\nTo change: `web after MM/DD/YYYY`\nTo clear: `web after clear`", meta.WebSearchAfterDate)
			} else {
				ce.Reply("No after-date filter set. To add: `web after MM/DD/YYYY`")
			}
			return
		}

		arg := ce.Args[1]
		if arg == "clear" || arg == "none" || arg == "off" {
			oldDate := meta.WebSearchAfterDate
			meta.WebSearchAfterDate = ""
			if err := ce.Portal.Save(ce.Ctx); err != nil {
				meta.WebSearchAfterDate = oldDate
				ce.Reply("Failed to save setting: %v", err)
				return
			}
			ce.Reply("After-date filter **cleared**.")
			return
		}

		// Check for conflict with recency filter (mutual exclusion)
		var clearedRecency string
		if meta.WebSearchRecency != "" {
			clearedRecency = meta.WebSearchRecency
			meta.WebSearchRecency = ""
		}

		oldDate := meta.WebSearchAfterDate
		meta.WebSearchAfterDate = arg
		if err := ce.Portal.Save(ce.Ctx); err != nil {
			meta.WebSearchAfterDate = oldDate
			ce.Reply("Failed to save setting: %v", err)
			return
		}

		reply := fmt.Sprintf("After-date filter set to: **%s**\n\nPerplexity will search content after this date.", arg)
		if clearedRecency != "" {
			reply += fmt.Sprintf("\n\n⚠️ **Cleared conflicting filter:** recency (%s) (cannot combine date filters with recency)", clearedRecency)
		}
		ce.Reply(reply)

	case "before", "until", "to":
		if len(ce.Args) < 2 {
			if meta.WebSearchBeforeDate != "" {
				ce.Reply("**Current before-date filter:** %s\n\nTo change: `web before MM/DD/YYYY`\nTo clear: `web before clear`", meta.WebSearchBeforeDate)
			} else {
				ce.Reply("No before-date filter set. To add: `web before MM/DD/YYYY`")
			}
			return
		}

		arg := ce.Args[1]
		if arg == "clear" || arg == "none" || arg == "off" {
			oldDate := meta.WebSearchBeforeDate
			meta.WebSearchBeforeDate = ""
			if err := ce.Portal.Save(ce.Ctx); err != nil {
				meta.WebSearchBeforeDate = oldDate
				ce.Reply("Failed to save setting: %v", err)
				return
			}
			ce.Reply("Before-date filter **cleared**.")
			return
		}

		// Check for conflict with recency filter (mutual exclusion)
		var clearedRecency string
		if meta.WebSearchRecency != "" {
			clearedRecency = meta.WebSearchRecency
			meta.WebSearchRecency = ""
		}

		oldDate := meta.WebSearchBeforeDate
		meta.WebSearchBeforeDate = arg
		if err := ce.Portal.Save(ce.Ctx); err != nil {
			meta.WebSearchBeforeDate = oldDate
			ce.Reply("Failed to save setting: %v", err)
			return
		}

		reply := fmt.Sprintf("Before-date filter set to: **%s**\n\nPerplexity will search content before this date.", arg)
		if clearedRecency != "" {
			reply += fmt.Sprintf("\n\n⚠️ **Cleared conflicting filter:** recency (%s) (cannot combine date filters with recency)", clearedRecency)
		}
		ce.Reply(reply)

	case "images", "image", "img":
		if len(ce.Args) < 2 {
			if meta.ReturnImages != nil && *meta.ReturnImages {
				ce.Reply("**Images:** enabled\n\nTo disable: `web images off`")
			} else {
				ce.Reply("**Images:** disabled\n\nTo enable: `web images on` (requires Tier-2+ API access)")
			}
			return
		}

		arg := strings.ToLower(ce.Args[1])
		var newValue bool
		switch arg {
		case "on", "true", "yes", "1", "enable", "enabled":
			newValue = true
		case "off", "false", "no", "0", "disable", "disabled", "clear":
			newValue = false
		default:
			ce.Reply("Invalid value. Use: `web images on` or `web images off`")
			return
		}

		oldValue := meta.ReturnImages
		meta.ReturnImages = &newValue
		if err := ce.Portal.Save(ce.Ctx); err != nil {
			meta.ReturnImages = oldValue
			ce.Reply("Failed to save setting: %v", err)
			return
		}
		if newValue {
			ce.Reply("Images **enabled**. Perplexity will include images in responses (requires Tier-2+ API access).")
		} else {
			ce.Reply("Images **disabled**.")
		}

	case "context", "contextsize", "size":
		if len(ce.Args) < 2 {
			if meta.SearchContextSize != "" {
				ce.Reply("**Current context size:** %s\n\nTo change: `web context low|medium|high`\nTo clear: `web context clear`", meta.SearchContextSize)
			} else {
				ce.Reply("No context size set (using default). To set: `web context low|medium|high`")
			}
			return
		}

		arg := strings.ToLower(ce.Args[1])
		if arg == "clear" || arg == "none" || arg == "off" || arg == "default" {
			oldSize := meta.SearchContextSize
			meta.SearchContextSize = ""
			if err := ce.Portal.Save(ce.Ctx); err != nil {
				meta.SearchContextSize = oldSize
				ce.Reply("Failed to save setting: %v", err)
				return
			}
			ce.Reply("Context size **cleared** (using default).")
			return
		}

		validSizes := map[string]bool{"low": true, "medium": true, "high": true}
		if !validSizes[arg] {
			ce.Reply("Invalid context size. Use: `low`, `medium`, or `high`.")
			return
		}

		oldSize := meta.SearchContextSize
		meta.SearchContextSize = arg
		if err := ce.Portal.Save(ce.Ctx); err != nil {
			meta.SearchContextSize = oldSize
			ce.Reply("Failed to save setting: %v", err)
			return
		}
		ce.Reply("Context size set to: **%s**", arg)

	case "mode", "searchmode":
		if len(ce.Args) < 2 {
			if meta.SearchMode != "" {
				ce.Reply("**Current search mode:** %s\n\nTo change: `web mode academic|web`\nTo clear: `web mode clear`", meta.SearchMode)
			} else {
				ce.Reply("No search mode set (using default). To set: `web mode academic|web`")
			}
			return
		}

		arg := strings.ToLower(ce.Args[1])
		if arg == "clear" || arg == "none" || arg == "off" || arg == "default" {
			oldMode := meta.SearchMode
			meta.SearchMode = ""
			if err := ce.Portal.Save(ce.Ctx); err != nil {
				meta.SearchMode = oldMode
				ce.Reply("Failed to save setting: %v", err)
				return
			}
			ce.Reply("Search mode **cleared** (using default).")
			return
		}

		validModes := map[string]bool{"academic": true, "web": true}
		if !validModes[arg] {
			ce.Reply("Invalid search mode. Use: `academic` or `web`.")
			return
		}

		oldMode := meta.SearchMode
		meta.SearchMode = arg
		if err := ce.Portal.Save(ce.Ctx); err != nil {
			meta.SearchMode = oldMode
			ce.Reply("Failed to save setting: %v", err)
			return
		}
		ce.Reply("Search mode set to: **%s**", arg)

	case "location", "loc":
		if len(ce.Args) < 2 {
			if meta.UserLocationCity != "" || meta.UserLocationCountry != "" {
				parts := []string{}
				if meta.UserLocationCity != "" {
					parts = append(parts, meta.UserLocationCity)
				}
				if meta.UserLocationRegion != "" {
					parts = append(parts, meta.UserLocationRegion)
				}
				if meta.UserLocationCountry != "" {
					parts = append(parts, meta.UserLocationCountry)
				}
				ce.Reply("**Current location:** %s\n\nTo change: `web location city,region,country`\nTo clear: `web location clear`", strings.Join(parts, ", "))
			} else {
				ce.Reply("No location set. To add: `web location city,region,country`\n\nExamples:\n- `web location Berlin,Berlin,Germany`\n- `web location ,California,US`\n- `web location ,,US`")
			}
			return
		}

		arg := ce.Args[1]
		if arg == "clear" || arg == "none" || arg == "off" {
			meta.UserLocationCity = ""
			meta.UserLocationRegion = ""
			meta.UserLocationCountry = ""
			meta.UserLocationTimezone = ""
			if err := ce.Portal.Save(ce.Ctx); err != nil {
				ce.Reply("Failed to save setting: %v", err)
				return
			}
			ce.Reply("Location **cleared**.")
			return
		}

		// Parse city,region,country format
		parts := strings.SplitN(arg, ",", 3)
		if len(parts) > 0 {
			meta.UserLocationCity = strings.TrimSpace(parts[0])
		}
		if len(parts) > 1 {
			meta.UserLocationRegion = strings.TrimSpace(parts[1])
		}
		if len(parts) > 2 {
			meta.UserLocationCountry = strings.TrimSpace(parts[2])
		}

		if err := ce.Portal.Save(ce.Ctx); err != nil {
			ce.Reply("Failed to save setting: %v", err)
			return
		}

		locParts := []string{}
		if meta.UserLocationCity != "" {
			locParts = append(locParts, meta.UserLocationCity)
		}
		if meta.UserLocationRegion != "" {
			locParts = append(locParts, meta.UserLocationRegion)
		}
		if meta.UserLocationCountry != "" {
			locParts = append(locParts, meta.UserLocationCountry)
		}
		ce.Reply("Location set to: **%s**\n\nPerplexity will use this for location-aware results.", strings.Join(locParts, ", "))

	case "clear", "reset":
		meta.WebSearchDomains = nil
		meta.WebSearchRecency = ""
		meta.WebSearchAfterDate = ""
		meta.WebSearchBeforeDate = ""
		meta.ReturnImages = nil
		meta.SearchContextSize = ""
		meta.SearchMode = ""
		meta.UserLocationCity = ""
		meta.UserLocationRegion = ""
		meta.UserLocationCountry = ""
		meta.UserLocationTimezone = ""
		if err := ce.Portal.Save(ce.Ctx); err != nil {
			ce.Reply("Failed to save setting: %v", err)
			return
		}
		ce.Reply("All web search settings **cleared**.")

	default:
		ce.Reply("Unknown subcommand. Use:\n- `web domains <domain1,domain2,...>`\n- `web recency day|week|month|year`\n- `web after <MM/DD/YYYY>`\n- `web before <MM/DD/YYYY>`\n- `web images on|off`\n- `web context low|medium|high`\n- `web mode academic|web`\n- `web location city,region,country`\n- `web clear`")
	}
}

// cmdJoin adds Perplexity to the current room by creating a bridge portal.
// If Perplexity is already in the room, this re-configures the relay.
func (c *PerplexityConnector) cmdJoin(ce *commands.Event) {
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
				ce.Reply("Failed to resolve model: %v\n\nUse `sonar`, `sonar-pro`, `sonar-reasoning`, or `sonar-reasoning-pro`.", err)
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
		portalMeta.ConversationName = fmt.Sprintf("Perplexity (%s)", model)
		portalMeta.Model = model
		ce.Portal.Metadata = portalMeta

		if err := ce.Portal.Save(ctx); err != nil {
			ce.Reply("Failed to save portal: %v", err)
			return
		}

		// Swap ghosts if model changed (uses shared helper)
		if err := c.swapGhosts(ctx, ce.RoomID, oldModel, model); err != nil {
			ce.Reply("Failed to update Perplexity ghost: %v", err)
			return
		}

		// Re-set relay if enabled
		if c.br.Config.Relay.Enabled {
			if err := ce.Portal.SetRelay(ctx, login); err != nil {
				ce.Reply("Failed to set relay: %v", err)
				return
			}
		}

		if oldModel != model {
			ce.Reply("✓ **Perplexity %s** has joined the room! (replaced %s)\n\nUse `model` to change models, `mention on` for mention-only mode, or `clear` to reset conversation.", model, oldModel)
		} else {
			ce.Reply("✓ **Perplexity %s** is in the room! Relay updated.\n\nUse `model` to change models, `mention on` for mention-only mode, or `clear` to reset conversation.", model)
		}
		return
	}
	c.Log.Debug().Msg("Join command: no existing portal, creating new one")

	login := ce.User.GetDefaultLogin()
	if login == nil {
		ce.Reply("You are not logged in.")
		return
	}

	client, ok := login.Client.(*PerplexityClient)
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
		case "sonar", "perplexity":
			model = perplexityapi.ModelSonar
		case "sonar-pro", "pro":
			model = perplexityapi.ModelSonarPro
		case "sonar-reasoning", "reasoning":
			model = perplexityapi.ModelSonarReasoning
		case "sonar-reasoning-pro", "reasoning-pro":
			model = perplexityapi.ModelSonarReasoningPro
		default:
			// Assume it's a full model ID
			if perplexityapi.IsValidModel(requestedModel) {
				model = requestedModel
			} else {
				ce.Reply("Unknown model: %s. Use `sonar`, `sonar-pro`, `sonar-reasoning`, `sonar-reasoning-pro`, or a full model ID.", requestedModel)
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
		Msg("Join command: adding Perplexity to room")

	// Create a unique conversation/portal ID based on the room
	conversationID := fmt.Sprintf("room_%s", roomID)
	portalKey := MakePerplexityPortalKey(conversationID)

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
	ghostID := c.MakePerplexityGhostID(model)
	c.Log.Debug().
		Str("model", model).
		Str("ghost_id", string(ghostID)).
		Msg("Join command: resolved ghost ID from model")

	ghost, err := c.GetOrUpdateGhost(ctx, ghostID, model)
	if err != nil {
		ce.Reply("Failed to get Perplexity ghost: %v", err)
		return
	}

	c.Log.Debug().
		Str("ghost_mxid", ghost.Intent.GetMXID().String()).
		Msg("Join command: got ghost intent")

	// Set up portal metadata - always update the model even if portal exists
	chatName := fmt.Sprintf("Perplexity (%s)", model)

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
			ce.Reply("Failed to invite Perplexity to this room: %v\n\nMake sure the bot has permission to invite users.", err)
			return
		}

		c.Log.Debug().Msg("Join command: invite succeeded, attempting join")
		err = ghost.Intent.EnsureJoined(ctx, roomID)
		if err != nil {
			c.Log.Error().Err(err).Msg("Join command: ghost failed to join after invite")
			ce.Reply("Perplexity was invited but failed to join: %v", err)
			return
		}
	}
	c.Log.Debug().Msg("Join command: ghost successfully joined room")

	// Auto-set relay so other users in the room can also talk to Perplexity
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

	if c.br.Config.Relay.Enabled {
		ce.Reply("✓ **Perplexity %s** has joined the room!\n\nAll users in this room can now chat with Perplexity (messages relayed through your account).\n\nUse `model` to change models, `system` to set a custom prompt, `mention on` for mention-only mode, or `clear` to reset conversation.", model)
	} else {
		ce.Reply("✓ **Perplexity %s** has joined the room!\n\n⚠️ **Note:** Relay mode is disabled. Only you can talk to Perplexity. Enable `relay.enabled: true` in bridge config for multi-user support.\n\nUse `model` to change models, `system` to set a custom prompt, or `clear` to reset the conversation.", model)
	}

	c.Log.Info().
		Str("room_id", string(roomID)).
		Str("model", model).
		Str("ghost_id", string(ghostID)).
		Bool("relay_enabled", c.br.Config.Relay.Enabled).
		Msg("Successfully added Perplexity to room")
}

// cmdTemperature views or sets the temperature.
func (c *PerplexityConnector) cmdTemperature(ce *commands.Event) {
	if ce.Portal == nil {
		ce.Reply("This command must be run in a Perplexity conversation room.")
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
			ce.Reply("**Current temperature:** %.2f\n\nUse `temperature <0-2>` to change, or `temperature reset` to use default.", *meta.Temperature)
		} else {
			ce.Reply("**Current temperature:** %.2f (default)\n\nUse `temperature <0-2>` to change.", c.Config.GetTemperature())
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
		ce.Reply("Invalid temperature value. Use a number between 0 and 2.")
		return
	}

	if temp < 0 || temp > 2 {
		ce.Reply("Temperature must be between 0 and 2.")
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
func (c *PerplexityConnector) cmdRemoveGhost(ce *commands.Event) {
	if len(ce.Args) == 0 {
		ce.Reply("Usage: `remove-ghost <@user:server>`\n\nExample: `remove-ghost @perplexity_unknown:example.com`")
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
