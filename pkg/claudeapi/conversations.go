// Package claudeapi provides a client for the Claude API.
package claudeapi

import (
	"fmt"
	"sync"
	"time"
)

// Token estimation constants.
const (
	// ApproxCharsPerToken is the approximate number of characters per token.
	// Claude uses a similar tokenization to other LLMs where ~4 chars ≈ 1 token.
	ApproxCharsPerToken = 4

	// ContextTrimTargetPercent is the target percentage of max tokens to keep
	// when trimming context. This provides headroom for responses.
	ContextTrimTargetPercent = 80

	// MinMessagesToKeep is the minimum number of messages to keep in conversation
	// history, even when trimming for token limits.
	MinMessagesToKeep = 2
)

// TrackedMessage wraps a Message with an external ID for tracking.
type TrackedMessage struct {
	Message
	ExternalID string // External ID (e.g., Matrix message ID)
}

// Compaction constants
const (
	// CompactionThresholdPercent is the percentage of max tokens that triggers compaction.
	CompactionThresholdPercent = 75

	// CompactionTargetPercent is the target percentage after compaction.
	CompactionTargetPercent = 50

	// CachingWindowDuration is how long after the first message to enable caching.
	// Caching is only enabled from the 2nd message within this window to avoid
	// the 25% cache write overhead on single questions.
	CachingWindowDuration = 5 * time.Minute

	// MinMessagesForCaching is the minimum number of messages before enabling caching.
	MinMessagesForCaching = 2

	// MinTokensForCaching is the minimum token threshold for caching to be worthwhile.
	// Claude requires at least 1024 tokens for caching to work (2048 for Opus).
	MinTokensForCaching = 1024
)

// ConversationManager manages conversation history and context.
type ConversationManager struct {
	messages       []TrackedMessage
	maxTokens      int
	mu             sync.RWMutex
	createdAt      time.Time
	lastUsedAt     time.Time
	firstMessageAt time.Time     // When the first message was sent (for caching window)
	compactionCount int          // Number of times the conversation was compacted
	isCompacted    bool          // Whether the conversation has been compacted
}

// NewConversationManager creates a new conversation manager.
func NewConversationManager(maxTokens int) *ConversationManager {
	now := time.Now()
	return &ConversationManager{
		messages:   make([]TrackedMessage, 0),
		maxTokens:  maxTokens,
		createdAt:  now,
		lastUsedAt: now,
	}
}

// AddMessage adds a message to the conversation history.
func (cm *ConversationManager) AddMessage(role, content string) {
	cm.AddMessageWithID(role, content, "")
}

// AddMessageWithID adds a message with an external ID for tracking.
func (cm *ConversationManager) AddMessageWithID(role, content, externalID string) {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	message := TrackedMessage{
		Message: Message{
			Role: role,
			Content: []Content{
				{
					Type: "text",
					Text: content,
				},
			},
		},
		ExternalID: externalID,
	}

	cm.messages = append(cm.messages, message)
	cm.lastUsedAt = time.Now()
}

// AddMessageWithContent adds a message with arbitrary content (text, images, etc).
// Use this when you have content that includes images or mixed content types.
func (cm *ConversationManager) AddMessageWithContent(role string, content []Content, externalID string) {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	message := TrackedMessage{
		Message: Message{
			Role:    role,
			Content: content,
		},
		ExternalID: externalID,
	}

	cm.messages = append(cm.messages, message)
	cm.lastUsedAt = time.Now()
}

// GetMessages returns a copy of all messages in the conversation (without tracking info).
func (cm *ConversationManager) GetMessages() []Message {
	cm.mu.RLock()
	defer cm.mu.RUnlock()

	// Return plain Messages for API calls
	messagesCopy := make([]Message, len(cm.messages))
	for i, tm := range cm.messages {
		messagesCopy[i] = tm.Message
	}

	return messagesCopy
}

// EditMessageByID edits a message by its external ID.
// If the message is found, it updates the content and removes all subsequent messages
// (since changing a message invalidates all following responses).
// Returns true if the message was found and edited.
func (cm *ConversationManager) EditMessageByID(externalID, newContent string) bool {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	for i, tm := range cm.messages {
		if tm.ExternalID == externalID {
			// Update the message content
			cm.messages[i].Content = []Content{
				{
					Type: "text",
					Text: newContent,
				},
			}

			// Remove all messages after this one (they're now invalid)
			cm.messages = cm.messages[:i+1]

			cm.lastUsedAt = time.Now()
			return true
		}
	}

	return false
}

// DeleteMessageByID deletes a message by its external ID.
// Also removes all subsequent messages (since the conversation flow is broken).
// Returns true if the message was found and deleted.
func (cm *ConversationManager) DeleteMessageByID(externalID string) bool {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	for i, tm := range cm.messages {
		if tm.ExternalID == externalID {
			// Remove this message and all after it
			cm.messages = cm.messages[:i]

			cm.lastUsedAt = time.Now()
			return true
		}
	}

	return false
}

// EditLastUserMessage edits the most recent user message.
// Removes any assistant messages that came after it.
// Returns an error if there's no user message to edit.
func (cm *ConversationManager) EditLastUserMessage(newContent string) error {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	// Find the last user message
	for i := len(cm.messages) - 1; i >= 0; i-- {
		if cm.messages[i].Role == "user" {
			// Update content
			cm.messages[i].Content = []Content{
				{
					Type: "text",
					Text: newContent,
				},
			}

			// Remove all messages after this one
			cm.messages = cm.messages[:i+1]

			cm.lastUsedAt = time.Now()
			return nil
		}
	}

	return fmt.Errorf("no user message found to edit")
}

// DeleteLastUserMessage deletes the most recent user message and its response.
// Returns an error if there's no user message to delete.
func (cm *ConversationManager) DeleteLastUserMessage() error {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	// Find the last user message
	for i := len(cm.messages) - 1; i >= 0; i-- {
		if cm.messages[i].Role == "user" {
			// Remove this message and all after it
			cm.messages = cm.messages[:i]

			cm.lastUsedAt = time.Now()
			return nil
		}
	}

	return fmt.Errorf("no user message found to delete")
}

// Clear removes all messages from the conversation.
func (cm *ConversationManager) Clear() {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	cm.messages = make([]TrackedMessage, 0)
	cm.lastUsedAt = time.Now()
}

// TrimToTokenLimit trims old messages to stay within the token limit.
// This uses a simplified token estimation where ~4 characters equals ~1 token.
// See ApproxCharsPerToken constant for details.
func (cm *ConversationManager) TrimToTokenLimit() error {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	if cm.maxTokens <= 0 {
		// No limit
		return nil
	}

	// Estimate total tokens using the approximate chars-per-token ratio
	totalChars := 0
	for _, msg := range cm.messages {
		for _, content := range msg.Content {
			totalChars += len(content.Text)
		}
	}

	estimatedTokens := totalChars / ApproxCharsPerToken

	// If we're under the limit, no trimming needed
	if estimatedTokens < cm.maxTokens {
		return nil
	}

	// If we're at or over the limit, trim to target percentage of max to provide headroom
	targetChars := (cm.maxTokens * ApproxCharsPerToken * ContextTrimTargetPercent) / 100

	// Remove oldest messages until we're under the target
	// Keep at least the minimum number of messages (typically one user-assistant pair)
	for len(cm.messages) > MinMessagesToKeep && totalChars > targetChars {
		// Remove the oldest message
		removed := cm.messages[0]
		cm.messages = cm.messages[1:]

		// Update character count
		for _, content := range removed.Content {
			totalChars -= len(content.Text)
		}
	}

	return nil
}

// MessageCount returns the number of messages in the conversation.
func (cm *ConversationManager) MessageCount() int {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	return len(cm.messages)
}

// EstimatedTokens returns the estimated token count for the conversation.
func (cm *ConversationManager) EstimatedTokens() int {
	cm.mu.RLock()
	defer cm.mu.RUnlock()

	totalChars := 0
	for _, msg := range cm.messages {
		for _, content := range msg.Content {
			totalChars += len(content.Text)
		}
	}

	return totalChars / ApproxCharsPerToken
}

// GetMaxTokens returns the maximum token limit for this conversation.
func (cm *ConversationManager) GetMaxTokens() int {
	return cm.maxTokens
}

// SetMaxTokens sets a new maximum token limit.
func (cm *ConversationManager) SetMaxTokens(maxTokens int) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	cm.maxTokens = maxTokens
}

// CreatedAt returns when the conversation was created.
func (cm *ConversationManager) CreatedAt() time.Time {
	return cm.createdAt
}

// LastUsedAt returns when the conversation was last used.
func (cm *ConversationManager) LastUsedAt() time.Time {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	return cm.lastUsedAt
}

// Age returns the age of the conversation since creation.
func (cm *ConversationManager) Age() time.Duration {
	return time.Since(cm.createdAt)
}

// IdleTime returns how long since the conversation was last used.
func (cm *ConversationManager) IdleTime() time.Duration {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	return time.Since(cm.lastUsedAt)
}

// IsExpired checks if the conversation has exceeded the given max age.
// A max age of 0 means the conversation never expires.
func (cm *ConversationManager) IsExpired(maxAge time.Duration) bool {
	if maxAge <= 0 {
		return false
	}
	return cm.IdleTime() > maxAge
}

// HasMessages returns true if the conversation has any messages.
func (cm *ConversationManager) HasMessages() bool {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	return len(cm.messages) > 0
}

// LastMessageRole returns the role of the last message, or empty string if no messages.
func (cm *ConversationManager) LastMessageRole() string {
	cm.mu.RLock()
	defer cm.mu.RUnlock()

	if len(cm.messages) == 0 {
		return ""
	}
	return cm.messages[len(cm.messages)-1].Role
}

// ShouldEnableCaching returns true if prompt caching should be enabled.
// Caching is enabled when:
// 1. There are at least MinMessagesForCaching messages (to avoid overhead on single questions)
// 2. The first message was within CachingWindowDuration (cache TTL is 5 min)
// 3. Estimated tokens are at least MinTokensForCaching (Claude's minimum for caching)
func (cm *ConversationManager) ShouldEnableCaching() bool {
	cm.mu.RLock()
	defer cm.mu.RUnlock()

	// Need at least 2 messages (1 user + 1 assistant from previous turn)
	if len(cm.messages) < MinMessagesForCaching {
		return false
	}

	// Check if we're within the caching window
	if cm.firstMessageAt.IsZero() || time.Since(cm.firstMessageAt) > CachingWindowDuration {
		return false
	}

	// Check minimum token threshold
	return cm.estimatedTokensLocked() >= MinTokensForCaching
}

// NeedsCompaction returns true if the conversation should be compacted.
// This is triggered when estimated tokens exceed CompactionThresholdPercent of maxTokens.
func (cm *ConversationManager) NeedsCompaction() bool {
	cm.mu.RLock()
	defer cm.mu.RUnlock()

	if cm.maxTokens <= 0 {
		return false
	}

	threshold := (cm.maxTokens * CompactionThresholdPercent) / 100
	return cm.estimatedTokensLocked() >= threshold
}

// estimatedTokensLocked returns estimated tokens (caller must hold lock).
func (cm *ConversationManager) estimatedTokensLocked() int {
	totalChars := 0
	for _, msg := range cm.messages {
		for _, content := range msg.Content {
			totalChars += len(content.Text)
		}
	}
	return totalChars / ApproxCharsPerToken
}

// GetMessagesForCompaction returns all messages formatted for the compaction request.
func (cm *ConversationManager) GetMessagesForCompaction() string {
	cm.mu.RLock()
	defer cm.mu.RUnlock()

	var result string
	for _, msg := range cm.messages {
		role := msg.Role
		if role == "user" {
			role = "User"
		} else {
			role = "Assistant"
		}

		for _, content := range msg.Content {
			if content.Type == "text" && content.Text != "" {
				result += fmt.Sprintf("[%s]: %s\n\n", role, content.Text)
			}
		}
	}
	return result
}

// ApplyCompaction replaces the conversation history with a summary.
// The summary is stored as an assistant message, and the specified number of
// recent messages are preserved after it.
func (cm *ConversationManager) ApplyCompaction(summary string, keepRecentCount int) {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	// Determine how many recent messages to keep
	if keepRecentCount < 0 {
		keepRecentCount = 0
	}
	if keepRecentCount > len(cm.messages) {
		keepRecentCount = len(cm.messages)
	}

	// Get recent messages to preserve
	var recentMessages []TrackedMessage
	if keepRecentCount > 0 {
		recentMessages = make([]TrackedMessage, keepRecentCount)
		copy(recentMessages, cm.messages[len(cm.messages)-keepRecentCount:])
	}

	// Create the summary message as a "user" message (context for Claude)
	// followed by "assistant" acknowledgment for proper turn alternation
	summaryPrefix := "[CONVERSATION SUMMARY - Previous context has been compacted]\n\n"
	summaryUserMsg := TrackedMessage{
		Message: Message{
			Role: "user",
			Content: []Content{
				{Type: "text", Text: summaryPrefix + summary + "\n\n[Please continue the conversation from here, using this summary as context.]"},
			},
		},
		ExternalID: fmt.Sprintf("compaction_%d_user", cm.compactionCount+1),
	}

	summaryAsstMsg := TrackedMessage{
		Message: Message{
			Role: "assistant",
			Content: []Content{
				{Type: "text", Text: "I understand. I've reviewed the conversation summary and have full context of our previous discussion. I'm ready to continue helping you from where we left off."},
			},
		},
		ExternalID: fmt.Sprintf("compaction_%d_assistant", cm.compactionCount+1),
	}

	// Build new message list: summary messages + recent messages
	cm.messages = make([]TrackedMessage, 0, 2+len(recentMessages))
	cm.messages = append(cm.messages, summaryUserMsg, summaryAsstMsg)
	cm.messages = append(cm.messages, recentMessages...)

	cm.compactionCount++
	cm.isCompacted = true
	cm.lastUsedAt = time.Now()
}

// CompactionCount returns how many times this conversation was compacted.
func (cm *ConversationManager) CompactionCount() int {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	return cm.compactionCount
}

// IsCompacted returns whether the conversation has been compacted.
func (cm *ConversationManager) IsCompacted() bool {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	return cm.isCompacted
}

// RecordFirstMessage records when the first message was sent (for caching window).
func (cm *ConversationManager) RecordFirstMessage() {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	if cm.firstMessageAt.IsZero() {
		cm.firstMessageAt = time.Now()
	}
}

// ResetCachingWindow resets the caching window timer.
// Call this when a new conversation burst starts after a long pause.
func (cm *ConversationManager) ResetCachingWindow() {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	cm.firstMessageAt = time.Now()
}
