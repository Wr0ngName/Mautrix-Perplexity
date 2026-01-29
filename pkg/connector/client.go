package connector

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/bridgev2/simplevent"
	"maunium.net/go/mautrix/bridgev2/status"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/format"
	"maunium.net/go/mautrix/id"

	"go.mau.fi/mautrix-claude/pkg/claudeapi"
	"go.mau.fi/mautrix-claude/pkg/sidecar"
)

// Supported image MIME types for Claude Vision API.
var supportedImageTypes = map[string]bool{
	"image/jpeg": true,
	"image/png":  true,
	"image/gif":  true,
	"image/webp": true,
}

// isImageSupported checks if a MIME type is supported by Claude Vision.
func isImageSupported(mimeType string) bool {
	return supportedImageTypes[mimeType]
}

// getMessageText extracts the message text from a Matrix message content.
// If FormattedBody (HTML) is available, it parses it to preserve mention display names.
// Otherwise, it falls back to the plain Body (which contains raw Matrix user IDs).
func getMessageText(content *event.MessageEventContent) string {
	// If we have formatted HTML body, parse it to get display names for mentions
	if content.FormattedBody != "" && content.Format == event.FormatHTML {
		return format.HTMLToText(content.FormattedBody)
	}
	return content.Body
}

// downloadAndEncodeImage downloads an image from Matrix and converts it to base64.
func (c *ClaudeClient) downloadAndEncodeImage(ctx context.Context, content *event.MessageEventContent) (*claudeapi.Content, error) {
	// Get the content URI
	uri := content.URL
	if uri == "" && content.File != nil {
		uri = content.File.URL
	}
	if uri == "" {
		return nil, fmt.Errorf("no image URL found")
	}

	// Get MIME type
	mimeType := "image/jpeg" // Default
	if content.Info != nil && content.Info.MimeType != "" {
		mimeType = content.Info.MimeType
	}

	// Check if image type is supported
	if !isImageSupported(mimeType) {
		return nil, fmt.Errorf("unsupported image type: %s (supported: jpeg, png, gif, webp)", mimeType)
	}

	// Download the image using the bridge bot's Matrix API
	imageData, err := c.Connector.br.Bot.DownloadMedia(ctx, uri, content.File)
	if err != nil {
		return nil, fmt.Errorf("failed to download image: %w", err)
	}

	// Convert to base64
	base64Data := base64.StdEncoding.EncodeToString(imageData)

	c.Connector.Log.Debug().
		Str("mime_type", mimeType).
		Int("size_bytes", len(imageData)).
		Msg("Downloaded and encoded image for Claude Vision")

	return &claudeapi.Content{
		Type: "image",
		Source: &claudeapi.ImageSource{
			Type:      "base64",
			MediaType: mimeType,
			Data:      base64Data,
		},
	}, nil
}

// recentMention tracks when a user mentioned Claude in a portal.
type recentMention struct {
	userID    id.UserID
	portalID  networkid.PortalID
	timestamp time.Time
}

// recentMentionWindow is how long after a mention we still process images from that user.
// Set to 5 seconds to allow for network delays. Only 1 image is allowed per mention.
const recentMentionWindow = 5 * time.Second

// ClaudeClient represents a client connection to Claude (API or Web).
type ClaudeClient struct {
	MessageClient claudeapi.MessageClient // Can be *claudeapi.Client or *claudeapi.WebClient
	UserLogin     *bridgev2.UserLogin
	Connector     *ClaudeConnector
	conversations map[networkid.PortalID]*claudeapi.ConversationManager
	convMu        sync.RWMutex

	// Rate limiting
	rateLimiter *RateLimiter

	// Recent mention tracking for images following mentions
	recentMentions []recentMention
	mentionMu      sync.Mutex

	// Graceful shutdown support
	wg     sync.WaitGroup
	ctx    context.Context
	cancel context.CancelFunc
}

// RateLimiter implements a simple sliding window rate limiter.
type RateLimiter struct {
	mu           sync.Mutex
	maxRequests  int
	windowSize   time.Duration
	requestTimes []time.Time
}

// NewRateLimiter creates a new rate limiter with the given requests per minute.
// If requestsPerMinute is 0 or negative, rate limiting is disabled.
func NewRateLimiter(requestsPerMinute int) *RateLimiter {
	if requestsPerMinute <= 0 {
		return nil
	}
	return &RateLimiter{
		maxRequests:  requestsPerMinute,
		windowSize:   time.Minute,
		requestTimes: make([]time.Time, 0, requestsPerMinute),
	}
}

// Allow checks if a request is allowed and records it if so.
// Returns true if the request is allowed, false if rate limited.
func (r *RateLimiter) Allow() bool {
	if r == nil {
		return true
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	now := time.Now()
	windowStart := now.Add(-r.windowSize)

	// Remove expired entries
	validTimes := make([]time.Time, 0, len(r.requestTimes))
	for _, t := range r.requestTimes {
		if t.After(windowStart) {
			validTimes = append(validTimes, t)
		}
	}
	r.requestTimes = validTimes

	// Check if we're at the limit
	if len(r.requestTimes) >= r.maxRequests {
		return false
	}

	// Record this request
	r.requestTimes = append(r.requestTimes, now)
	return true
}

// WaitTime returns how long to wait before the next request will be allowed.
// Returns 0 if a request is allowed immediately.
func (r *RateLimiter) WaitTime() time.Duration {
	if r == nil {
		return 0
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	now := time.Now()
	windowStart := now.Add(-r.windowSize)

	// Remove expired entries and count valid ones
	validCount := 0
	var oldestValid time.Time
	for _, t := range r.requestTimes {
		if t.After(windowStart) {
			validCount++
			if oldestValid.IsZero() || t.Before(oldestValid) {
				oldestValid = t
			}
		}
	}

	if validCount < r.maxRequests {
		return 0
	}

	// Calculate when the oldest request will expire
	return oldestValid.Add(r.windowSize).Sub(now)
}

var (
	_ bridgev2.NetworkAPI                    = (*ClaudeClient)(nil)
	_ bridgev2.IdentifierResolvingNetworkAPI = (*ClaudeClient)(nil)
	_ bridgev2.MembershipHandlingNetworkAPI  = (*ClaudeClient)(nil)
)

// Connect is called when the client should connect.
func (c *ClaudeClient) Connect(ctx context.Context) {
	// Create a cancellable context derived from parent for proper propagation
	c.ctx, c.cancel = context.WithCancel(ctx)

	// Start conversation cleanup goroutine if max age is configured
	if c.Connector.Config.ConversationMaxAge > 0 {
		c.wg.Add(1)
		go c.conversationCleanupLoop()
	}

	c.Connector.Log.Info().Msg("Claude client ready")
	c.UserLogin.BridgeState.Send(status.BridgeState{
		StateEvent: status.StateConnected,
	})
}

// Disconnect is called when the client should disconnect.
func (c *ClaudeClient) Disconnect() {
	// Cancel context to stop all goroutines
	if c.cancel != nil {
		c.cancel()
	}

	// Wait for all goroutines to finish
	c.wg.Wait()

	c.Connector.Log.Info().Msg("Claude client disconnected")
}

// conversationCleanupLoop periodically cleans up expired conversations.
func (c *ClaudeClient) conversationCleanupLoop() {
	defer c.wg.Done()
	defer func() {
		if r := recover(); r != nil {
			c.Connector.Log.Error().Interface("panic", r).Msg("Panic in conversation cleanup goroutine")
		}
	}()

	maxAge := time.Duration(c.Connector.Config.ConversationMaxAge) * time.Hour
	ticker := time.NewTicker(10 * time.Minute) // Check every 10 minutes
	defer ticker.Stop()

	c.Connector.Log.Debug().
		Dur("max_age", maxAge).
		Msg("Conversation cleanup loop started")

	for {
		select {
		case <-c.ctx.Done():
			c.Connector.Log.Debug().Msg("Conversation cleanup loop stopped")
			return
		case <-ticker.C:
			c.cleanupExpiredConversations(maxAge)
		}
	}
}

// cleanupExpiredConversations removes conversations that have exceeded the max age.
func (c *ClaudeClient) cleanupExpiredConversations(maxAge time.Duration) {
	c.convMu.Lock()
	defer c.convMu.Unlock()

	expired := make([]networkid.PortalID, 0)

	for portalID, cm := range c.conversations {
		if cm.IsExpired(maxAge) {
			expired = append(expired, portalID)
		}
	}

	for _, portalID := range expired {
		delete(c.conversations, portalID)
		c.Connector.Log.Debug().
			Str("portal_id", string(portalID)).
			Msg("Cleaned up expired conversation")
	}

	if len(expired) > 0 {
		c.Connector.Log.Info().
			Int("count", len(expired)).
			Msg("Cleaned up expired conversations")
	}
}

// IsLoggedIn checks if the client is logged in.
func (c *ClaudeClient) IsLoggedIn() bool {
	return c.MessageClient != nil
}

// LogoutRemote logs out from the remote service.
// For API key logins: no remote cleanup needed (key remains valid until revoked at console.anthropic.com).
// For sidecar logins: cleans up stored Claude Code credentials on the sidecar.
func (c *ClaudeClient) LogoutRemote(ctx context.Context) {
	log := c.Connector.Log.With().
		Str("user", string(c.UserLogin.UserMXID)).
		Str("login_id", string(c.UserLogin.ID)).
		Logger()

	// Check if this is a sidecar login
	meta, ok := c.UserLogin.Metadata.(*UserLoginMetadata)
	if !ok || meta == nil {
		return
	}

	// For sidecar logins, clean up stored credentials
	if meta.CredentialsJSON != "" && c.Connector.Config.Sidecar.Enabled {
		sidecarClient := sidecar.NewClient(
			c.Connector.Config.Sidecar.GetURL(),
			time.Duration(c.Connector.Config.Sidecar.GetTimeout())*time.Second,
			c.Connector.Log,
		)

		// Use the Matrix user ID as the user identifier (same as used during login)
		userID := string(c.UserLogin.UserMXID)
		if err := sidecarClient.DeleteUser(ctx, userID); err != nil {
			log.Warn().Err(err).Msg("Failed to clean up sidecar credentials during logout")
		} else {
			log.Info().Msg("Cleaned up sidecar credentials")
		}
	}
}

// getAPIKey returns the API key from the user login metadata.
func (c *ClaudeClient) getAPIKey() string {
	if c.UserLogin == nil {
		return ""
	}
	meta, ok := c.UserLogin.Metadata.(*UserLoginMetadata)
	if !ok || meta == nil {
		return ""
	}
	return meta.APIKey
}

// IsThisUser checks if a user ID belongs to this logged-in user.
func (c *ClaudeClient) IsThisUser(ctx context.Context, userID networkid.UserID) bool {
	// All Claude ghosts belong to the system, not individual users
	return false
}

// isClaudeMentioned checks if the Claude ghost is mentioned in the message.
func (c *ClaudeClient) isClaudeMentioned(msg *bridgev2.MatrixMessage) bool {
	// Check formatted body for mentions (HTML format)
	if msg.Content.FormattedBody != "" {
		// Look for mention pill: <a href="https://matrix.to/#/@claude_
		if strings.Contains(msg.Content.FormattedBody, "/@claude_") {
			return true
		}
		// Also check for the ghost's MXID directly
		model := c.Connector.Config.GetDefaultModel()
		if meta, ok := msg.Portal.Metadata.(*PortalMetadata); ok && meta != nil && meta.Model != "" {
			model = meta.Model
		}
		ghostID := c.Connector.MakeClaudeGhostID(model)
		ghostMXID := fmt.Sprintf("@claude_%s:", ghostID)
		if strings.Contains(msg.Content.FormattedBody, ghostMXID) {
			return true
		}
	}

	// Check plain text body for @claude mentions
	body := strings.ToLower(msg.Content.Body)
	if strings.Contains(body, "@claude") {
		return true
	}

	// Check for display name mentions (case insensitive)
	if strings.Contains(body, "claude") && (strings.HasPrefix(body, "claude") || strings.Contains(body, " claude") || strings.Contains(body, "@claude")) {
		return true
	}

	return false
}

// isMentionOnlyMessage checks if the message contains only a mention with no real content.
// Used to detect messages like "@claude_sonnet:server.com" that are just triggering image processing.
func (c *ClaudeClient) isMentionOnlyMessage(msg *bridgev2.MatrixMessage) bool {
	// Get the plain text body
	body := strings.TrimSpace(msg.Content.Body)

	// Remove Matrix MXIDs that start with @claude (e.g., @claude_sonnet:server.com)
	// These are the actual mention format in Matrix
	cleaned := body
	for {
		atIdx := strings.Index(strings.ToLower(cleaned), "@claude")
		if atIdx == -1 {
			break
		}
		// Find the end of this MXID (space, newline, or end of string)
		// MXIDs are @localpart:server.com format
		endIdx := atIdx + 1
		for endIdx < len(cleaned) {
			ch := cleaned[endIdx]
			if ch == ' ' || ch == '\n' || ch == '\t' {
				break
			}
			endIdx++
		}
		cleaned = cleaned[:atIdx] + cleaned[endIdx:]
	}

	// Remove standalone "Claude" (display name mention)
	cleaned = strings.ReplaceAll(cleaned, "Claude", "")
	cleaned = strings.ReplaceAll(cleaned, "claude", "")

	// Remove common punctuation that might follow a mention
	cleaned = strings.TrimSpace(cleaned)
	cleaned = strings.Trim(cleaned, ":,;.!?")
	cleaned = strings.TrimSpace(cleaned)

	// If nothing meaningful remains, it's a mention-only message
	return len(cleaned) == 0
}

// recordMention records that a user mentioned Claude in a portal.
// This allows subsequent images from the same user to be processed.
func (c *ClaudeClient) recordMention(userID id.UserID, portalID networkid.PortalID) {
	c.mentionMu.Lock()
	defer c.mentionMu.Unlock()

	now := time.Now()

	// Clean up old mentions
	validMentions := make([]recentMention, 0, len(c.recentMentions))
	for _, m := range c.recentMentions {
		if now.Sub(m.timestamp) < recentMentionWindow {
			validMentions = append(validMentions, m)
		}
	}

	// Add new mention
	validMentions = append(validMentions, recentMention{
		userID:    userID,
		portalID:  portalID,
		timestamp: now,
	})

	c.recentMentions = validMentions
}

// consumeRecentMention checks if a user has recently mentioned Claude in a portal.
// If found, it removes the mention (allowing only 1 image per mention).
// Used to allow images that immediately follow a mention message.
func (c *ClaudeClient) consumeRecentMention(userID id.UserID, portalID networkid.PortalID) bool {
	c.mentionMu.Lock()
	defer c.mentionMu.Unlock()

	now := time.Now()
	for i, m := range c.recentMentions {
		if m.userID == userID && m.portalID == portalID && now.Sub(m.timestamp) < recentMentionWindow {
			// Remove this mention (consume it - only 1 image per mention)
			c.recentMentions = append(c.recentMentions[:i], c.recentMentions[i+1:]...)
			return true
		}
	}
	return false
}

// getConversationManager gets or creates a conversation manager for a portal.
func (c *ClaudeClient) getConversationManager(portal *bridgev2.Portal) *claudeapi.ConversationManager {
	c.convMu.Lock()
	defer c.convMu.Unlock()

	portalID := portal.PortalKey.ID

	if cm, ok := c.conversations[portalID]; ok {
		return cm
	}

	// Create new conversation manager with max tokens from config
	maxTokens := claudeapi.GetModelMaxTokens(c.Connector.Config.GetDefaultModel())
	cm := claudeapi.NewConversationManager(maxTokens)
	c.conversations[portalID] = cm

	return cm
}

// HandleMatrixMessage handles a message sent from Matrix to Claude.
func (c *ClaudeClient) HandleMatrixMessage(ctx context.Context, msg *bridgev2.MatrixMessage) (*bridgev2.MatrixMessageResponse, error) {
	// Get portal metadata, use defaults if not available
	meta, _ := msg.Portal.Metadata.(*PortalMetadata)
	if meta == nil {
		meta = &PortalMetadata{} // Use empty metadata with defaults
	}

	bodyPreview := msg.Content.Body
	if len(bodyPreview) > 50 {
		bodyPreview = bodyPreview[:50]
	}

	c.Connector.Log.Debug().
		Str("portal_id", string(msg.Portal.PortalKey.ID)).
		Str("sender", string(msg.Event.Sender)).
		Str("msg_type", string(msg.Content.MsgType)).
		Str("body", bodyPreview).
		Bool("mention_only", meta.MentionOnly).
		Msg("Handling Matrix message")

	// Check mention-only mode
	if meta.MentionOnly {
		mentioned := c.isClaudeMentioned(msg)
		isImage := msg.Content.MsgType == event.MsgImage

		if mentioned {
			// Check if this is a mention-only message (e.g., just "@claude" with no real content)
			// These are typically sent as captions for images, so don't send to Claude - wait for the image
			// BUT if this message IS already an image (phone sends image+mention as single message), process it now
			if c.isMentionOnlyMessage(msg) && !isImage {
				// Only record mention for image-waiting when we're actually waiting for an image
				c.recordMention(msg.Event.Sender, msg.Portal.PortalKey.ID)
				c.Connector.Log.Debug().Msg("Mention-only mode: Mention-only message (no content), waiting for image")
				return &bridgev2.MatrixMessageResponse{}, nil
			}
			c.Connector.Log.Debug().Msg("Mention-only mode: Claude mentioned, processing message")
		} else if isImage && c.consumeRecentMention(msg.Event.Sender, msg.Portal.PortalKey.ID) {
			// Image immediately following a mention - process it (one image per mention)
			c.Connector.Log.Debug().Msg("Mention-only mode: Image following recent mention, processing")
		} else {
			c.Connector.Log.Debug().Msg("Mention-only mode: Claude not mentioned, ignoring message")
			// Return empty response to indicate message was handled but no action taken
			return &bridgev2.MatrixMessageResponse{}, nil
		}
	}

	// Check rate limit before processing
	if !c.rateLimiter.Allow() {
		waitTime := c.rateLimiter.WaitTime()
		c.Connector.Log.Warn().
			Dur("wait_time", waitTime).
			Msg("Rate limited, rejecting message")
		// Record rate limit rejection in metrics
		if metrics := c.GetMetrics(); metrics != nil {
			metrics.RecordLocalRateLimitReject()
		}
		errMsg := fmt.Sprintf("Rate limit exceeded. Please wait %s before sending another message.", waitTime.Round(time.Second))
		c.sendErrorToRoom(ctx, msg.Portal, errMsg)
		return nil, fmt.Errorf("%s", errMsg)
	}

	// Get or create conversation manager for this portal
	convMgr := c.getConversationManager(msg.Portal)

	// Get sender display name for multi-user awareness
	senderName := msg.Event.Sender.String() // Fallback to MXID
	if memberInfo, err := c.Connector.br.Matrix.GetMemberInfo(ctx, msg.Portal.MXID, msg.Event.Sender); err == nil && memberInfo != nil && memberInfo.Displayname != "" {
		senderName = memberInfo.Displayname
	}

	// Build content array based on message type
	userMsgID := string(msg.Event.ID)
	var messageContent []claudeapi.Content

	// Handle different message types
	switch msg.Content.MsgType {
	case event.MsgImage:
		// Image message - download and encode the image
		imageContent, err := c.downloadAndEncodeImage(ctx, msg.Content)
		if err != nil {
			c.Connector.Log.Warn().Err(err).Msg("Failed to process image")
			errMsg := fmt.Sprintf("Failed to process image: %v", err)
			c.sendErrorToRoom(ctx, msg.Portal, errMsg)
			return nil, fmt.Errorf("%s", errMsg)
		}
		messageContent = append(messageContent, *imageContent)

		// Add caption/body text if present (with sender name)
		if msg.Content.Body != "" && msg.Content.Body != msg.Content.FileName {
			// Use getMessageText to preserve display names in mentions
			captionText := getMessageText(msg.Content)
			messageContent = append(messageContent, claudeapi.Content{
				Type: "text",
				Text: fmt.Sprintf("[%s]: %s", senderName, captionText),
			})
		} else {
			// Add a default prompt for image analysis (with sender name)
			messageContent = append(messageContent, claudeapi.Content{
				Type: "text",
				Text: fmt.Sprintf("[%s]: What's in this image?", senderName),
			})
		}

		c.Connector.Log.Info().
			Int("content_parts", len(messageContent)).
			Msg("Processing image message with Claude Vision")

	default:
		// Text message (or other text-based types)
		if msg.Content.Body == "" {
			return nil, fmt.Errorf("empty message body")
		}
		// Use getMessageText to preserve display names in mentions
		messageText := getMessageText(msg.Content)
		// Validate message length to prevent abuse
		if err := ValidateMessageLength(messageText); err != nil {
			return nil, err
		}
		// Prepend sender name so Claude knows who's talking
		textWithSender := fmt.Sprintf("[%s]: %s", senderName, messageText)
		messageContent = append(messageContent, claudeapi.Content{
			Type: "text",
			Text: textWithSender,
		})
	}

	// Prepare API request - use portal-specific or connector defaults
	model := meta.Model
	if model == "" {
		model = c.Connector.Config.GetDefaultModel()
	}

	// Check if we're in sidecar mode (must do this before model resolution)
	isSidecarMode := c.MessageClient.GetClientType() == "sidecar"

	// Resolve family names (sonnet, opus, haiku) to actual model IDs
	// For sidecar mode, skip API resolution - the Agent SDK handles family names directly
	if IsModelFamily(model) && !isSidecarMode {
		family := GetModelFamilyName(model)
		apiKey := c.getAPIKey()
		if apiKey == "" {
			errMsg := "No API key configured"
			c.sendErrorToRoom(ctx, msg.Portal, errMsg)
			return nil, errors.New(errMsg)
		}

		resolvedModel, err := claudeapi.GetLatestModelByFamilyFromAPI(ctx, apiKey, family)
		if err != nil {
			c.Connector.Log.Error().Err(err).Str("family", family).Msg("Failed to resolve model family")
			errMsg := fmt.Sprintf("Failed to resolve model '%s': %v", model, err)
			c.sendErrorToRoom(ctx, msg.Portal, errMsg)
			return nil, errors.New(errMsg)
		}
		c.Connector.Log.Debug().
			Str("family", family).
			Str("resolved", resolvedModel).
			Msg("Resolved model family to latest version")
		model = resolvedModel
	}

	temperature := meta.GetTemperature(c.Connector.Config.GetTemperature())

	systemPrompt := meta.SystemPrompt
	if systemPrompt == "" {
		systemPrompt = c.Connector.Config.GetSystemPrompt()
	}

	// Build messages for API request
	// For sidecar mode: only send current message (sidecar handles history via session resume)
	// For API mode: send existing history + new user message
	userMessage := claudeapi.Message{
		Role:    "user",
		Content: messageContent,
	}

	var messagesForAPI []claudeapi.Message
	var enableCaching bool

	if isSidecarMode {
		// Sidecar handles conversation history via Agent SDK session resume
		messagesForAPI = []claudeapi.Message{userMessage}
	} else {
		// API mode: include local conversation history
		// Record first message time for caching window tracking
		convMgr.RecordFirstMessage()

		// Check if compaction is needed before adding new message
		if convMgr.NeedsCompaction() {
			c.Connector.Log.Info().
				Int("estimated_tokens", convMgr.EstimatedTokens()).
				Int("max_tokens", convMgr.GetMaxTokens()).
				Int("compaction_count", convMgr.CompactionCount()).
				Msg("Context approaching limit, triggering compaction")

			// Get conversation text for summarization
			conversationText := convMgr.GetMessagesForCompaction()

			// Call Claude to generate a summary (using the direct API client)
			if apiClient, ok := c.MessageClient.(*claudeapi.Client); ok {
				summary, err := apiClient.CompactConversation(ctx, model, conversationText)
				if err != nil {
					c.Connector.Log.Error().Err(err).Msg("Failed to compact conversation, continuing with full history")
				} else {
					// Apply compaction, keeping the last 2 messages (most recent exchange)
					convMgr.ApplyCompaction(summary, 2)
					c.Connector.Log.Info().
						Int("new_message_count", convMgr.MessageCount()).
						Int("new_estimated_tokens", convMgr.EstimatedTokens()).
						Msg("Conversation compacted successfully")

					// Notify user that compaction occurred
					c.sendCompactionNotice(ctx, msg.Portal)
				}
			}
		}

		existingMessages := convMgr.GetMessages()
		messagesForAPI = append(existingMessages, userMessage)

		// Enable caching if conditions are met (2nd+ message within 5 min window, enough tokens)
		enableCaching = convMgr.ShouldEnableCaching()
		if enableCaching {
			c.Connector.Log.Debug().
				Int("message_count", convMgr.MessageCount()).
				Int("estimated_tokens", convMgr.EstimatedTokens()).
				Msg("Enabling prompt caching for this request")
		}
	}

	req := &claudeapi.CreateMessageRequest{
		Model:         model,
		Messages:      messagesForAPI,
		MaxTokens:     c.Connector.Config.GetMaxTokens(),
		Temperature:   temperature,
		System:        systemPrompt,
		Stream:        true, // Use streaming for better UX
		EnableCaching: enableCaching,
	}

	// Send to Claude API (add portal ID context for sidecar session isolation)
	ctx = sidecar.WithPortalID(ctx, string(msg.Portal.PortalKey.ID))

	// Add user credentials for per-user sidecar sessions
	if metadata, ok := c.UserLogin.Metadata.(*UserLoginMetadata); ok && metadata.CredentialsJSON != "" {
		ctx = sidecar.WithUserCredentials(ctx, string(c.UserLogin.UserMXID), metadata.CredentialsJSON)
	}

	// Add session ID for sidecar resume (stored in bridge DB portal metadata)
	if isSidecarMode && meta.SidecarSessionID != "" {
		ctx = sidecar.WithSessionID(ctx, meta.SidecarSessionID)
		c.Connector.Log.Debug().
			Str("session_id", meta.SidecarSessionID).
			Msg("Resuming sidecar session from bridge DB")
	}

	// Create a context with timeout for the sidecar call to prevent hanging forever
	// Use sidecar timeout config (defaults to 5 minutes)
	streamTimeout := time.Duration(c.Connector.Config.Sidecar.GetTimeout()) * time.Second
	if streamTimeout <= 0 {
		streamTimeout = 5 * time.Minute
	}

	// Get ghost intent for typing notification
	ghostID := c.Connector.MakeClaudeGhostID(model)
	ghost, err := c.Connector.GetOrUpdateGhost(ctx, ghostID, model)
	if err != nil {
		c.Connector.Log.Warn().Err(err).Str("ghost_id", string(ghostID)).Msg("Failed to get ghost for typing indicator")
	}

	// Send typing indicator with timeout matching sidecar timeout
	if ghost != nil {
		if err := ghost.Intent.MarkTyping(ctx, msg.Portal.MXID, bridgev2.TypingTypeText, streamTimeout); err != nil {
			c.Connector.Log.Debug().Err(err).Msg("Failed to send typing indicator")
		}
	}

	// Helper to stop typing
	stopTyping := func() {
		if ghost != nil {
			_ = ghost.Intent.MarkTyping(ctx, msg.Portal.MXID, bridgev2.TypingTypeText, 0)
		}
	}
	streamCtx, streamCancel := context.WithTimeout(ctx, streamTimeout)
	defer streamCancel()

	stream, err := c.MessageClient.CreateMessageStream(streamCtx, req)
	if err != nil {
		stopTyping()
		c.Connector.Log.Error().Err(err).Msg("Failed to create message stream")
		friendlyErr := c.formatUserFriendlyError(err)
		c.sendErrorToRoom(ctx, msg.Portal, friendlyErr.Error())
		return nil, friendlyErr
	}
	if stream == nil {
		stopTyping()
		errMsg := "received nil stream from Claude API"
		c.sendErrorToRoom(ctx, msg.Portal, errMsg)
		return nil, errors.New(errMsg)
	}

	// Collect response
	var responseText strings.Builder
	var claudeMessageID string
	var inputTokens, outputTokens int
	var streamError error
	var newSessionID string // Agent SDK session ID from sidecar (for resume)

	for event := range stream {
		switch event.Type {
		case "message_start":
			if event.Message != nil {
				claudeMessageID = event.Message.ID
				if event.Message.Usage != nil && event.Message.Usage.InputTokens > 0 {
					inputTokens = event.Message.Usage.InputTokens
				}
			}
		case "content_block_delta":
			if event.Delta != nil && event.Delta.Text != "" {
				responseText.WriteString(event.Delta.Text)
			}
		case "message_delta":
			if event.Usage != nil {
				outputTokens = event.Usage.OutputTokens
			}
			if event.SessionID != "" {
				newSessionID = event.SessionID
			}
		case "error":
			c.Connector.Log.Error().Interface("event", event).Msg("Error in stream")
			if event.Error != nil {
				streamError = fmt.Errorf("streaming error: %s - %s", event.Error.Type, event.Error.Message)
			} else {
				streamError = fmt.Errorf("unknown streaming error")
			}
		}
	}

	// Stop typing indicator now that streaming is complete
	stopTyping()

	// Check if context timed out
	if streamCtx.Err() == context.DeadlineExceeded {
		errMsg := fmt.Sprintf("Request timed out after %s. The sidecar may be overloaded or unresponsive.", streamTimeout)
		c.Connector.Log.Error().Dur("timeout", streamTimeout).Msg("Sidecar request timed out")
		c.sendErrorToRoom(ctx, msg.Portal, errMsg)
		return nil, errors.New(errMsg)
	} else if streamCtx.Err() == context.Canceled {
		errMsg := "Request was cancelled"
		c.Connector.Log.Warn().Msg("Sidecar request was cancelled")
		return nil, errors.New(errMsg)
	}

	// Check for streaming errors
	if streamError != nil {
		// Format user-friendly and send error to Matrix room
		friendlyErr := c.formatUserFriendlyError(streamError)
		c.sendErrorToRoom(ctx, msg.Portal, friendlyErr.Error())
		return nil, friendlyErr
	}

	if claudeMessageID == "" {
		claudeMessageID = fmt.Sprintf("msg_%d", time.Now().UnixNano())
	}

	// Validate response content
	responseContent := responseText.String()
	if responseContent == "" {
		errMsg := "received empty response from Claude"
		c.sendErrorToRoom(ctx, msg.Portal, errMsg)
		return nil, errors.New(errMsg)
	}

	// Store sidecar session ID for resume (persisted in bridge DB)
	if isSidecarMode && newSessionID != "" && newSessionID != meta.SidecarSessionID {
		meta.SidecarSessionID = newSessionID
		msg.Portal.Metadata = meta
		if err := msg.Portal.Save(ctx); err != nil {
			c.Connector.Log.Warn().Err(err).
				Str("session_id", newSessionID).
				Msg("Failed to save sidecar session ID to portal metadata")
		} else {
			c.Connector.Log.Debug().
				Str("session_id", newSessionID).
				Msg("Saved sidecar session ID to bridge DB for resume")
		}
	}

	// Only track conversation history locally for API mode
	// Sidecar mode handles history via Agent SDK session resume
	if !isSidecarMode {
		// Add messages to conversation history AFTER successful API response
		// This prevents conversation state corruption if API fails
		convMgr.AddMessageWithContent("user", messageContent, userMsgID)
		convMgr.AddMessageWithID("assistant", responseContent, claudeMessageID)

		c.Connector.Log.Debug().
			Str("portal_id", string(msg.Portal.PortalKey.ID)).
			Int("message_count", convMgr.MessageCount()).
			Int("estimated_tokens", convMgr.EstimatedTokens()).
			Msg("Added messages to conversation history")

		// Trim conversation if needed
		if err := convMgr.TrimToTokenLimit(); err != nil {
			c.Connector.Log.Warn().Err(err).Msg("Failed to trim conversation")
		}
	}

	// Queue the assistant's response as an incoming message
	// Use a goroutine with WaitGroup for graceful shutdown
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		defer func() {
			if r := recover(); r != nil {
				c.Connector.Log.Error().Interface("panic", r).Msg("Panic in assistant response goroutine")
			}
		}()
		c.queueAssistantResponse(msg.Portal, responseContent, claudeMessageID, inputTokens+outputTokens)
	}()

	// Return response metadata for the outgoing (user -> Claude) message
	// Use a unique ID based on user's Matrix event ID to avoid collision with Claude's response ID
	return &bridgev2.MatrixMessageResponse{
		DB: &database.Message{
			ID:        networkid.MessageID("user_" + userMsgID),
			Timestamp: time.Now(),
			Metadata: &MessageMetadata{
				ClaudeMessageID: "user_" + userMsgID,
				TokensUsed:      0, // User messages don't have token usage
			},
		},
	}, nil
}

// formatUserFriendlyError converts API errors to user-friendly messages.
func (c *ClaudeClient) formatUserFriendlyError(err error) error {
	if err == nil {
		return nil
	}

	errStr := strings.ToLower(err.Error())

	// Check for sidecar auth errors (credentials expired/invalid)
	if strings.Contains(errStr, "credentials") && (strings.Contains(errStr, "expired") || strings.Contains(errStr, "re-login")) {
		return fmt.Errorf("your Claude credentials have expired. Please use the 'logout' command, then log in again with fresh credentials")
	}
	if strings.Contains(errStr, "authentication failed") || strings.Contains(errStr, "401") {
		return fmt.Errorf("authentication failed. Your credentials may have expired - please use 'logout' then log in again")
	}

	// Check for specific error types
	if claudeapi.IsRateLimitError(err) {
		retryAfter := claudeapi.GetRetryAfter(err)
		if retryAfter > 0 {
			return fmt.Errorf("rate limit exceeded. Please wait %s and try again", retryAfter.Round(time.Second))
		}
		return fmt.Errorf("rate limit exceeded. Please wait a moment and try again")
	}

	if claudeapi.IsAuthError(err) {
		return fmt.Errorf("authentication failed. Please check your API key is valid and has sufficient permissions")
	}

	if claudeapi.IsOverloadedError(err) {
		return fmt.Errorf("Claude is currently overloaded. Please try again in a few moments")
	}

	if claudeapi.IsInvalidRequestError(err) {
		// Don't leak full error details to user - log internally instead
		c.Connector.Log.Debug().Err(err).Msg("Invalid request error details")
		return fmt.Errorf("invalid request: please check your message and try again")
	}

	// Generic error - don't leak internal details to users
	c.Connector.Log.Debug().Err(err).Msg("API error details")
	return fmt.Errorf("failed to send message to Claude. Please try again later")
}

// sendCompactionNotice sends a notice to the Matrix room that context was compacted.
func (c *ClaudeClient) sendCompactionNotice(ctx context.Context, portal *bridgev2.Portal) {
	if ctx == nil || ctx.Err() != nil {
		return
	}

	notice := "ℹ️ Context limit approaching. Conversation history has been summarized to continue."

	c.UserLogin.QueueRemoteEvent(&simplevent.Message[*MessageMetadata]{
		EventMeta: simplevent.EventMeta{
			Type: bridgev2.RemoteEventMessage,
			LogContext: func(lc zerolog.Context) zerolog.Context {
				return lc.Str("compaction_notice", "true")
			},
			PortalKey: portal.PortalKey,
			Sender:    bridgev2.EventSender{Sender: c.Connector.MakeClaudeGhostID("system")},
			Timestamp: time.Now(),
		},
		ID: networkid.MessageID(fmt.Sprintf("compaction_%d", time.Now().UnixNano())),
		Data: &MessageMetadata{
			ClaudeMessageID: "compaction",
		},
		ConvertMessageFunc: func(ctx context.Context, portal *bridgev2.Portal, intent bridgev2.MatrixAPI, data *MessageMetadata) (*bridgev2.ConvertedMessage, error) {
			return &bridgev2.ConvertedMessage{
				Parts: []*bridgev2.ConvertedMessagePart{
					{
						Type: event.EventMessage,
						Content: &event.MessageEventContent{
							MsgType: event.MsgNotice,
							Body:    notice,
						},
					},
				},
			}, nil
		},
	})
}

// sendErrorToRoom sends an error message to the Matrix room so the user knows what happened.
func (c *ClaudeClient) sendErrorToRoom(ctx context.Context, portal *bridgev2.Portal, errorMsg string) {
	if ctx == nil || ctx.Err() != nil {
		return
	}

	// Create a notice message for the error
	c.UserLogin.QueueRemoteEvent(&simplevent.Message[*MessageMetadata]{
		EventMeta: simplevent.EventMeta{
			Type: bridgev2.RemoteEventMessage,
			LogContext: func(lc zerolog.Context) zerolog.Context {
				return lc.Str("error_notice", "true")
			},
			PortalKey: portal.PortalKey,
			Sender:    bridgev2.EventSender{Sender: c.Connector.MakeClaudeGhostID("error")},
			Timestamp: time.Now(),
		},
		ID: networkid.MessageID(fmt.Sprintf("error_%d", time.Now().UnixNano())),
		Data: &MessageMetadata{
			ClaudeMessageID: "error",
		},
		ConvertMessageFunc: func(ctx context.Context, portal *bridgev2.Portal, intent bridgev2.MatrixAPI, data *MessageMetadata) (*bridgev2.ConvertedMessage, error) {
			return &bridgev2.ConvertedMessage{
				Parts: []*bridgev2.ConvertedMessagePart{
					{
						Type: event.EventMessage,
						Content: &event.MessageEventContent{
							MsgType: event.MsgNotice,
							Body:    ErrorMessagePrefix + errorMsg,
						},
					},
				},
			}, nil
		},
	})
}

// MaxMessageSize is the initial target size for splitting plaintext messages.
// We start at 32KB and recursively split smaller if HTML expansion exceeds limits.
const MaxMessageSize = 32000

// MinMessageSize is the smallest we'll split to avoid infinite loops.
// If a single chunk at this size still exceeds Matrix limits, we truncate.
const MinMessageSize = 2000

// queueAssistantResponse sends the assistant's message to the Matrix room.
// If the message is too large (M_TOO_LARGE), it recursively splits and retries.
func (c *ClaudeClient) queueAssistantResponse(portal *bridgev2.Portal, text, messageID string, tokensUsed int) {
	model := c.Connector.Config.GetDefaultModel()
	if meta, ok := portal.Metadata.(*PortalMetadata); ok && meta != nil && meta.Model != "" {
		model = meta.Model
	}

	ghostID := c.Connector.MakeClaudeGhostID(model)

	// Get the ghost to send messages
	ctx := context.Background()
	ghost, err := c.Connector.GetOrUpdateGhost(ctx, ghostID, model)
	if err != nil {
		c.Connector.Log.Error().Err(err).Str("ghost_id", string(ghostID)).Msg("Failed to get ghost for message sending")
		return
	}

	// Send with retry on M_TOO_LARGE
	c.sendMessageWithRetry(ctx, portal, ghost, text, messageID, tokensUsed, MaxMessageSize)
}

// sendMessageWithRetry sends a message, and if it gets M_TOO_LARGE, splits and retries.
func (c *ClaudeClient) sendMessageWithRetry(ctx context.Context, portal *bridgev2.Portal, ghost *bridgev2.Ghost, text, messageID string, tokensUsed int, maxSize int) {
	// Split message at current size limit
	parts := splitMessage(text, maxSize)

	for i, part := range parts {
		partID := messageID
		if len(parts) > 1 {
			partID = fmt.Sprintf("%s_part%d", messageID, i+1)
		}

		// Render markdown to HTML
		content := format.RenderMarkdown(part, true, true)
		content.MsgType = event.MsgText

		// Try to send via Intent
		resp, err := ghost.Intent.SendMessage(ctx, portal.MXID, event.EventMessage, &event.Content{
			Parsed: &content,
		}, nil)

		if err != nil {
			// Check if it's M_TOO_LARGE error
			var respErr mautrix.RespError
			if errors.As(err, &respErr) && respErr.ErrCode == "M_TOO_LARGE" {
				c.Connector.Log.Warn().
					Int("part_size", len(part)).
					Int("max_size", maxSize).
					Str("part_id", partID).
					Msg("Message too large, splitting smaller and retrying")

				// Reduce size and retry this part
				newMaxSize := maxSize / 2
				if newMaxSize < MinMessageSize {
					// Can't split smaller, send error notice
					c.Connector.Log.Error().
						Int("part_size", len(part)).
						Str("part_id", partID).
						Msg("Message part too large even at minimum size, sending error notice")
					c.sendSizeErrorNotice(ctx, portal, ghost)
					return
				}
				// Retry this part with smaller size
				partTokens := 0
				if i == 0 {
					partTokens = tokensUsed
				}
				c.sendMessageWithRetry(ctx, portal, ghost, part, partID, partTokens, newMaxSize)
				continue
			}

			// Other error - log and send notice
			c.Connector.Log.Error().Err(err).Str("part_id", partID).Msg("Failed to send message to Matrix")
			c.sendSizeErrorNotice(ctx, portal, ghost)
			return
		}

		c.Connector.Log.Debug().
			Stringer("event_id", resp.EventID).
			Str("part_id", partID).
			Int("part_size", len(part)).
			Msg("Sent message part to Matrix")
	}

	if len(parts) > 1 {
		c.Connector.Log.Info().
			Int("parts", len(parts)).
			Int("total_size", len(text)).
			Int("max_size", maxSize).
			Msg("Split large Claude response into multiple messages")
	}
}

// sendSizeErrorNotice sends an error notice when a message is too large to send
// even after splitting to minimum size. This is rare and indicates unusual content.
func (c *ClaudeClient) sendSizeErrorNotice(ctx context.Context, portal *bridgev2.Portal, ghost *bridgev2.Ghost) {
	notice := "⚠️ Part of Claude's response could not be delivered due to Matrix size limits."
	content := &event.MessageEventContent{
		MsgType: event.MsgNotice,
		Body:    notice,
	}
	_, err := ghost.Intent.SendMessage(ctx, portal.MXID, event.EventMessage, &event.Content{
		Parsed: content,
	}, nil)
	if err != nil {
		c.Connector.Log.Error().Err(err).Msg("Failed to send size error notice")
	}
}

// splitMessage splits a message into chunks that fit within the size limit.
// It tries to split on paragraph boundaries, then sentence boundaries, then word boundaries.
func splitMessage(text string, maxSize int) []string {
	if len(text) <= maxSize {
		return []string{text}
	}

	var parts []string
	remaining := text

	for len(remaining) > 0 {
		if len(remaining) <= maxSize {
			parts = append(parts, remaining)
			break
		}

		// Find a good split point
		splitPoint := findSplitPoint(remaining, maxSize)
		parts = append(parts, strings.TrimSpace(remaining[:splitPoint]))
		remaining = strings.TrimSpace(remaining[splitPoint:])
	}

	return parts
}

// findSplitPoint finds a good point to split the text, preferring paragraph, sentence, or word boundaries.
func findSplitPoint(text string, maxSize int) int {
	// Try to find a paragraph break (double newline)
	for i := maxSize; i > maxSize/2; i-- {
		if i < len(text) && text[i] == '\n' && i > 0 && text[i-1] == '\n' {
			return i
		}
	}

	// Try to find a single newline
	for i := maxSize; i > maxSize/2; i-- {
		if i < len(text) && text[i] == '\n' {
			return i + 1
		}
	}

	// Try to find a sentence boundary (. ! ?)
	for i := maxSize; i > maxSize/2; i-- {
		if i < len(text) {
			ch := text[i-1]
			if (ch == '.' || ch == '!' || ch == '?') && (i >= len(text) || text[i] == ' ' || text[i] == '\n') {
				return i
			}
		}
	}

	// Try to find a word boundary (space)
	for i := maxSize; i > maxSize/2; i-- {
		if i < len(text) && text[i] == ' ' {
			return i + 1
		}
	}

	// Last resort: hard cut at maxSize
	return maxSize
}

// GetCapabilities returns the capabilities for a specific portal.
func (c *ClaudeClient) GetCapabilities(ctx context.Context, portal *bridgev2.Portal) *event.RoomFeatures {
	return &event.RoomFeatures{
		Formatting: event.FormattingFeatureMap{
			event.FmtBold:          event.CapLevelFullySupported,
			event.FmtItalic:        event.CapLevelFullySupported,
			event.FmtStrikethrough: event.CapLevelFullySupported,
			event.FmtInlineCode:    event.CapLevelFullySupported,
			event.FmtCodeBlock:     event.CapLevelFullySupported,
		},
		File: event.FileFeatureMap{
			// Claude Vision supports these image types
			event.MsgImage: {
				MaxSize: 20 * 1024 * 1024, // 20MB max for Claude Vision
				MimeTypes: map[string]event.CapabilitySupportLevel{
					"image/jpeg": event.CapLevelFullySupported,
					"image/png":  event.CapLevelFullySupported,
					"image/gif":  event.CapLevelFullySupported,
					"image/webp": event.CapLevelFullySupported,
				},
				Caption: event.CapLevelFullySupported, // Support image captions
			},
		},
		MaxTextLength:       100000, // Claude has large context window
		Edit:                event.CapLevelFullySupported,
		Delete:              event.CapLevelFullySupported,
		Reaction:            event.CapLevelUnsupported,
		Reply:               event.CapLevelPartialSupport, // Could implement as conversation context
		ReadReceipts:        false,
		TypingNotifications: true, // Claude shows "typing" while processing
	}
}

// HandleMatrixEdit handles an edit to a Matrix message.
// When a user edits a message, we update the conversation history and remove
// any subsequent messages (since the conversation flow has changed).
func (c *ClaudeClient) HandleMatrixEdit(ctx context.Context, msg *bridgev2.MatrixEdit) error {
	// Get the conversation manager for this portal
	convMgr := c.getConversationManager(msg.Portal)

	// Get the original message ID being edited
	originalMsgID := string(msg.EditTarget.ID)

	// Get the new content (preserve display names in mentions)
	newContent := getMessageText(msg.Content)

	// Try to edit by the original message ID
	if convMgr.EditMessageByID(originalMsgID, newContent) {
		c.Connector.Log.Debug().
			Str("message_id", originalMsgID).
			Str("new_content", newContent[:min(50, len(newContent))]).
			Msg("Edited message in conversation history")
		return nil
	}

	// If message not found by ID, try to edit the last user message
	// This handles cases where the message ID wasn't tracked
	if err := convMgr.EditLastUserMessage(newContent); err != nil {
		c.Connector.Log.Warn().
			Str("message_id", originalMsgID).
			Err(err).
			Msg("Could not find message to edit")
		return fmt.Errorf("message not found in conversation history")
	}

	c.Connector.Log.Debug().
		Str("message_id", originalMsgID).
		Msg("Edited last user message in conversation history")
	return nil
}

// HandleMatrixMessageRemove handles a deletion of a Matrix message.
// When a user deletes a message, we remove it from the conversation history
// along with any subsequent messages (since the conversation flow is broken).
func (c *ClaudeClient) HandleMatrixMessageRemove(ctx context.Context, msg *bridgev2.MatrixMessageRemove) error {
	// Get the conversation manager for this portal
	convMgr := c.getConversationManager(msg.Portal)

	// Get the message ID being deleted
	deletedMsgID := string(msg.TargetMessage.ID)

	// Try to delete by message ID
	if convMgr.DeleteMessageByID(deletedMsgID) {
		c.Connector.Log.Debug().
			Str("message_id", deletedMsgID).
			Msg("Deleted message from conversation history")
		return nil
	}

	// If message not found by ID, try to delete the last user message
	// This handles cases where the message ID wasn't tracked
	if err := convMgr.DeleteLastUserMessage(); err != nil {
		c.Connector.Log.Warn().
			Str("message_id", deletedMsgID).
			Err(err).
			Msg("Could not find message to delete")
		return fmt.Errorf("message not found in conversation history")
	}

	c.Connector.Log.Debug().
		Str("message_id", deletedMsgID).
		Msg("Deleted last user message from conversation history")
	return nil
}

// HandleMatrixReaction handles a reaction to a Matrix message (not supported).
func (c *ClaudeClient) HandleMatrixReaction(ctx context.Context, msg *bridgev2.MatrixReaction) error {
	return fmt.Errorf("reactions are not supported")
}

// HandleMatrixReactionRemove handles removal of a reaction (not supported).
func (c *ClaudeClient) HandleMatrixReactionRemove(ctx context.Context, msg *bridgev2.MatrixReactionRemove) error {
	return fmt.Errorf("reactions are not supported")
}

// HandleMatrixReadReceipt handles a read receipt (not supported).
func (c *ClaudeClient) HandleMatrixReadReceipt(ctx context.Context, msg *bridgev2.MatrixReadReceipt) error {
	// Silently ignore read receipts
	return nil
}

// HandleMatrixTyping handles a typing notification (not supported).
func (c *ClaudeClient) HandleMatrixTyping(ctx context.Context, msg *bridgev2.MatrixTyping) error {
	// Silently ignore typing notifications
	return nil
}

// HandleMatrixMembership handles membership changes including ghost invitations.
// This allows users to invite Claude ghost users to rooms directly.
func (c *ClaudeClient) HandleMatrixMembership(ctx context.Context, msg *bridgev2.MatrixMembershipChange) (*bridgev2.MatrixMembershipResult, error) {
	// We only care about invites to ghost users
	if msg.Type != bridgev2.Invite {
		return nil, nil
	}

	// Check if the target is a ghost (Claude bot being invited)
	// GhostOrUserLogin is an interface - use type assertion to check if it's a Ghost
	ghost, isGhost := msg.Target.(*bridgev2.Ghost)
	if !isGhost || ghost == nil {
		return nil, nil
	}

	c.Connector.Log.Info().
		Str("ghost_id", string(ghost.ID)).
		Str("room_id", string(msg.Portal.MXID)).
		Msg("Claude ghost invited to room, accepting invitation")

	// Accept the invitation - the bridge framework handles the actual join
	// Return nil to indicate success and let the framework process it
	return nil, nil
}

// PreHandleMatrixMessage is called before handling a Matrix message.
func (c *ClaudeClient) PreHandleMatrixMessage(ctx context.Context, msg *bridgev2.MatrixMessage) (bridgev2.MatrixMessageResponse, error) {
	// No pre-processing needed
	return bridgev2.MatrixMessageResponse{}, nil
}

// GetMetrics returns the API client metrics.
func (c *ClaudeClient) GetMetrics() *claudeapi.Metrics {
	if c.MessageClient == nil {
		return nil
	}
	return c.MessageClient.GetMetrics()
}

// ClearConversation clears the conversation history for a portal.
func (c *ClaudeClient) ClearConversation(portalID networkid.PortalID) {
	c.convMu.Lock()
	defer c.convMu.Unlock()

	if cm, ok := c.conversations[portalID]; ok {
		cm.Clear()
		c.Connector.Log.Debug().
			Str("portal_id", string(portalID)).
			Msg("Cleared conversation history")
	}
}

// GetConversationStats returns stats for a portal's conversation.
func (c *ClaudeClient) GetConversationStats(portalID networkid.PortalID) (messageCount, estimatedTokens int, lastUsed time.Time) {
	c.convMu.RLock()
	defer c.convMu.RUnlock()

	c.Connector.Log.Debug().
		Str("portal_id", string(portalID)).
		Int("total_conversations", len(c.conversations)).
		Msg("Getting conversation stats")

	if cm, ok := c.conversations[portalID]; ok {
		count := cm.MessageCount()
		tokens := cm.EstimatedTokens()
		c.Connector.Log.Debug().
			Int("message_count", count).
			Int("estimated_tokens", tokens).
			Msg("Found conversation")
		return count, tokens, cm.LastUsedAt()
	}

	// Log available portal IDs for debugging
	var ids []string
	for id := range c.conversations {
		ids = append(ids, string(id))
	}
	c.Connector.Log.Debug().
		Strs("available_portals", ids).
		Msg("Conversation not found for portal")

	return 0, 0, time.Time{}
}

// GetConversationFullStats returns full stats for a portal's conversation including compaction info.
func (c *ClaudeClient) GetConversationFullStats(portalID networkid.PortalID) (messageCount, estimatedTokens, maxTokens, compactionCount int, isCompacted bool) {
	c.convMu.RLock()
	defer c.convMu.RUnlock()

	if cm, ok := c.conversations[portalID]; ok {
		return cm.MessageCount(), cm.EstimatedTokens(), cm.GetMaxTokens(), cm.CompactionCount(), cm.IsCompacted()
	}

	return 0, 0, 0, 0, false
}

// ResolveIdentifier resolves an identifier to start a new chat.
// Supported identifiers: "claude", "opus", "sonnet", "haiku", or full model names.
func (c *ClaudeClient) ResolveIdentifier(ctx context.Context, identifier string, createChat bool) (*bridgev2.ResolveIdentifierResponse, error) {
	c.Connector.Log.Debug().
		Str("identifier", identifier).
		Bool("create_chat", createChat).
		Msg("Resolving identifier")

	// Parse identifier to determine the model
	model := c.parseModelIdentifier(identifier)
	if model == "" {
		return nil, fmt.Errorf("unknown identifier: %s (try 'opus', 'sonnet', 'haiku', or a full model name)", identifier)
	}

	ghostID := c.Connector.MakeClaudeGhostID(model)

	// Get display name for the model
	displayName := fmt.Sprintf("Claude (%s)", model)
	if info := claudeapi.GetModelInfo(model); info != nil && info.DisplayName != "" {
		displayName = info.DisplayName
	}
	isBot := true

	// Create user info for the ghost
	ghostUserInfo := &bridgev2.UserInfo{
		Name:        &displayName,
		IsBot:       &isBot,
		Identifiers: []string{fmt.Sprintf("claude:%s", model)},
	}

	roomType := database.RoomTypeDM
	chatName := fmt.Sprintf("Conversation with %s", displayName)

	// Generate a unique conversation ID
	conversationID := fmt.Sprintf("conv_%s_%d", claudeapi.GetModelFamily(model), time.Now().UnixNano())
	portalKey := MakeClaudePortalKey(conversationID)

	c.Connector.Log.Info().
		Str("identifier", identifier).
		Str("model", model).
		Str("conversation_id", conversationID).
		Str("ghost_id", string(ghostID)).
		Msg("Resolved identifier for portal")

	resp := &bridgev2.ResolveIdentifierResponse{
		UserID:   ghostID,
		UserInfo: ghostUserInfo,
		Chat: &bridgev2.CreateChatResponse{
			PortalKey: portalKey,
			PortalInfo: &bridgev2.ChatInfo{
				Name: &chatName,
				Type: &roomType,
				Members: &bridgev2.ChatMemberList{
					IsFull: true,
					Members: []bridgev2.ChatMember{
						{
							// The user who is starting the chat - they will be invited
							EventSender: bridgev2.EventSender{
								IsFromMe: true,
							},
						},
						{
							// The Claude ghost - include UserInfo for proper setup
							EventSender: bridgev2.EventSender{
								IsFromMe: false,
								Sender:   ghostID,
							},
							UserInfo: ghostUserInfo,
						},
					},
				},
				// ExtraUpdates callback to properly set portal metadata after creation
				ExtraUpdates: func(ctx context.Context, p *bridgev2.Portal) bool {
					pm, ok := p.Metadata.(*PortalMetadata)
					if !ok {
						c.Connector.Log.Error().Msg("Portal metadata type assertion failed in ResolveIdentifier")
						return false
					}
					pm.ConversationName = chatName
					pm.Model = model
					c.Connector.Log.Debug().
						Str("model", model).
						Str("chat_name", chatName).
						Msg("Set portal metadata via ExtraUpdates")
					return true
				},
			},
		},
	}

	c.Connector.Log.Info().
		Str("identifier", identifier).
		Str("model", model).
		Str("conversation_id", conversationID).
		Msg("Created chat response")

	return resp, nil
}

// parseModelIdentifier parses an identifier and returns the full model name.
// This uses friendly aliases that map to actual model IDs.
func (c *ClaudeClient) parseModelIdentifier(identifier string) string {
	identifier = strings.ToLower(strings.TrimSpace(identifier))

	// Map friendly names to default model aliases
	// These will be validated against the API when used
	switch identifier {
	case "claude", "sonnet", "claude-sonnet":
		return c.Connector.Config.GetDefaultModel()
	case "opus", "claude-opus":
		return "claude-opus-4-5-20251101"
	case "haiku", "claude-haiku":
		return "claude-haiku-4-5-20251001"
	}

	// Check if it's a model family name (e.g., "claude_opus" ghost ID format)
	if strings.HasPrefix(identifier, "claude_") {
		family := strings.TrimPrefix(identifier, "claude_")
		switch family {
		case "opus":
			return "claude-opus-4-5-20251101"
		case "sonnet":
			return c.Connector.Config.GetDefaultModel()
		case "haiku":
			return "claude-haiku-4-5-20251001"
		}
	}

	// Assume it's a direct model ID - let the API validate it
	if strings.Contains(identifier, "claude") {
		return identifier
	}

	// No match found
	return ""
}
