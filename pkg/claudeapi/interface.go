// Package claudeapi provides a wrapper around the official Anthropic Go SDK.
package claudeapi

import (
	"context"
)

// MessageClient is the interface for sending messages to Claude.
type MessageClient interface {
	// CreateMessageStream sends a message and returns a channel of streaming events.
	CreateMessageStream(ctx context.Context, req *CreateMessageRequest) (<-chan StreamEvent, error)

	// CreateMessage sends a message and returns the complete response (non-streaming).
	CreateMessage(ctx context.Context, req *CreateMessageRequest) (*CreateMessageResponse, error)

	// Validate checks if the client credentials are valid.
	Validate(ctx context.Context) error

	// GetMetrics returns the metrics collector for this client.
	GetMetrics() *Metrics

	// GetClientType returns the type of client.
	GetClientType() string
}

// ClientType constant.
const (
	ClientTypeAPI = "api"
)
