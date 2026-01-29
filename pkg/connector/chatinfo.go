package connector

import (
	"context"
	"fmt"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
)

// GetChatInfo returns information about a chat.
func (c *PerplexityClient) GetChatInfo(ctx context.Context, portal *bridgev2.Portal) (*bridgev2.ChatInfo, error) {
	// Get portal metadata, use defaults if not available
	meta, _ := portal.Metadata.(*PortalMetadata)
	if meta == nil {
		meta = &PortalMetadata{}
	}

	roomType := database.RoomTypeDM
	model := meta.Model
	if model == "" {
		model = c.Connector.Config.GetDefaultModel()
	}
	ghostID := c.Connector.MakePerplexityGhostID(model)

	name := meta.ConversationName
	if name == "" {
		name = fmt.Sprintf("Conversation with Perplexity (%s)", model)
	}

	return &bridgev2.ChatInfo{
		Name: &name,
		Members: &bridgev2.ChatMemberList{
			IsFull: true,
			Members: []bridgev2.ChatMember{
				{
					EventSender: bridgev2.EventSender{
						IsFromMe: false,
						Sender:   ghostID,
					},
				},
			},
		},
		Type: &roomType,
	}, nil
}
