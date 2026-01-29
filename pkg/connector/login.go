package connector

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"

	"go.mau.fi/mautrix-perplexity/pkg/perplexityapi"
	"go.mau.fi/mautrix-perplexity/pkg/sidecar"
)

// APIKeyLogin handles API key-based login for Perplexity.
type APIKeyLogin struct {
	User      *bridgev2.User
	Connector *PerplexityConnector
}

var (
	_ bridgev2.LoginProcess          = (*APIKeyLogin)(nil)
	_ bridgev2.LoginProcessUserInput = (*APIKeyLogin)(nil)
)

// Start begins the API key login flow.
func (a *APIKeyLogin) Start(ctx context.Context) (*bridgev2.LoginStep, error) {
	return &bridgev2.LoginStep{
		Type:         bridgev2.LoginStepTypeUserInput,
		StepID:       "api_key",
		Instructions: "Enter your Perplexity API key. Get one from: https://www.perplexity.ai/settings/api",
		UserInputParams: &bridgev2.LoginUserInputParams{
			Fields: []bridgev2.LoginInputDataField{
				{
					Type:        bridgev2.LoginInputFieldTypePassword,
					ID:          "api_key",
					Name:        "API Key",
					Description: "Your Perplexity API key (pplx-...)",
				},
			},
		},
	}, nil
}

// SubmitUserInput processes the submitted API key.
func (a *APIKeyLogin) SubmitUserInput(ctx context.Context, input map[string]string) (*bridgev2.LoginStep, error) {
	apiKey := input["api_key"]

	// Validate API key format first (can be tested without full context)
	if !isValidAPIKeyFormat(apiKey) {
		return nil, fmt.Errorf("invalid API key format: Perplexity API keys start with 'pplx-'")
	}

	// Validate state (required for actual login)
	if a.User == nil {
		return nil, fmt.Errorf("login state error: user not set")
	}
	if a.Connector == nil {
		return nil, fmt.Errorf("login state error: connector not set")
	}

	// Get sidecar client and test the API key
	client := a.Connector.getSidecarClient()
	sidecarClient, ok := client.(*sidecar.MessageClient)
	if !ok {
		return nil, fmt.Errorf("sidecar client not available - make sure sidecar is enabled in config")
	}

	// Test the API key via sidecar
	if err := sidecarClient.TestAuth(ctx, string(a.User.MXID), apiKey); err != nil {
		if perplexityapi.IsAuthError(err) {
			return nil, fmt.Errorf("invalid API key: authentication failed")
		}
		return nil, fmt.Errorf("failed to validate API key: %w", err)
	}

	// Create user login with hashed API key (for privacy - no raw key material in ID)
	hash := sha256.Sum256([]byte(apiKey))
	loginID := networkid.UserLoginID(fmt.Sprintf("perplexity_%s", hex.EncodeToString(hash[:10])))
	userLogin, err := a.User.NewLogin(ctx, &database.UserLogin{
		ID:         loginID,
		RemoteName: "Perplexity API User",
		Metadata: &UserLoginMetadata{
			APIKey: apiKey,
		},
	}, nil)
	if err != nil {
		return nil, err
	}

	// Set up client with rate limiter
	perplexityClient := &PerplexityClient{
		MessageClient: client,
		UserLogin:     userLogin,
		Connector:     a.Connector,
		rateLimiter:   NewRateLimiter(a.Connector.Config.GetRateLimitPerMinute()),
	}
	userLogin.Client = perplexityClient

	return &bridgev2.LoginStep{
		Type:         bridgev2.LoginStepTypeComplete,
		StepID:       "complete",
		Instructions: "Successfully authenticated with Perplexity API",
		CompleteParams: &bridgev2.LoginCompleteParams{
			UserLogin: userLogin,
		},
	}, nil
}

// Cancel cancels the login process.
func (a *APIKeyLogin) Cancel() {}

// isValidAPIKeyFormat checks if an API key has a valid format.
func isValidAPIKeyFormat(apiKey string) bool {
	if apiKey == "" {
		return false
	}

	// Perplexity API keys start with "pplx-"
	if !strings.HasPrefix(apiKey, "pplx-") {
		return false
	}

	// Must be longer than just the prefix
	if len(apiKey) <= len("pplx-") {
		return false
	}

	return true
}
