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

	"go.mau.fi/mautrix-claude/pkg/claudeapi"
	"go.mau.fi/mautrix-claude/pkg/sidecar"
)

// APIKeyLogin handles API key-based login.
type APIKeyLogin struct {
	User      *bridgev2.User
	Connector *ClaudeConnector
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
		Instructions: "Enter your Claude API key. Get one from: https://console.anthropic.com/settings/keys",
		UserInputParams: &bridgev2.LoginUserInputParams{
			Fields: []bridgev2.LoginInputDataField{
				{
					Type:        bridgev2.LoginInputFieldTypePassword,
					ID:          "api_key",
					Name:        "API Key",
					Description: "Your Claude API key (sk-ant-...)",
				},
			},
		},
	}, nil
}

// SubmitUserInput processes the submitted API key.
func (a *APIKeyLogin) SubmitUserInput(ctx context.Context, input map[string]string) (*bridgev2.LoginStep, error) {
	apiKey := input["api_key"]

	// Validate API key format
	if !isValidAPIKeyFormat(apiKey) {
		return nil, fmt.Errorf("invalid API key format")
	}

	// Test the API key
	client := claudeapi.NewClient(apiKey, a.Connector.Log)
	if err := client.Validate(ctx); err != nil {
		if claudeapi.IsAuthError(err) {
			return nil, fmt.Errorf("invalid API key: authentication failed")
		}
		return nil, fmt.Errorf("failed to validate API key: %w", err)
	}

	// Create user login with hashed API key (for privacy - no raw key material in ID)
	hash := sha256.Sum256([]byte(apiKey))
	loginID := networkid.UserLoginID(fmt.Sprintf("claude_%s", hex.EncodeToString(hash[:10])))
	userLogin, err := a.User.NewLogin(ctx, &database.UserLogin{
		ID:         loginID,
		RemoteName: "Claude API User",
		Metadata: &UserLoginMetadata{
			APIKey: apiKey,
		},
	}, nil)
	if err != nil {
		return nil, err
	}

	// Set up client with rate limiter
	claudeClient := &ClaudeClient{
		MessageClient: client,
		UserLogin:     userLogin,
		Connector:     a.Connector,
		conversations: make(map[networkid.PortalID]*claudeapi.ConversationManager),
		rateLimiter:   NewRateLimiter(a.Connector.Config.GetRateLimitPerMinute()),
	}
	userLogin.Client = claudeClient

	return &bridgev2.LoginStep{
		Type:         bridgev2.LoginStepTypeComplete,
		StepID:       "complete",
		Instructions: "Successfully authenticated with Claude API",
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

	// Claude API keys start with "sk-ant-"
	if !strings.HasPrefix(apiKey, "sk-ant-") {
		return false
	}

	// Must be longer than just the prefix
	if len(apiKey) <= len("sk-ant-") {
		return false
	}

	return true
}

// SidecarLogin handles login when sidecar mode is enabled.
// Uses OAuth 2.0 flow: user visits URL, gets code, pastes code back.
type SidecarLogin struct {
	User      *bridgev2.User
	Connector *ClaudeConnector

	// OAuth flow state (stored between steps)
	oauthState string
}

var (
	_ bridgev2.LoginProcess          = (*SidecarLogin)(nil)
	_ bridgev2.LoginProcessUserInput = (*SidecarLogin)(nil)
)

// Start begins the sidecar OAuth login flow.
func (s *SidecarLogin) Start(ctx context.Context) (*bridgev2.LoginStep, error) {
	// Verify sidecar is running first
	client := s.Connector.getSidecarClient()
	sidecarClient, ok := client.(*sidecar.MessageClient)
	if !ok {
		return nil, fmt.Errorf("sidecar client not available")
	}
	if _, err := sidecarClient.GetHealth(ctx); err != nil {
		return nil, fmt.Errorf("sidecar not available: %w", err)
	}

	// Start OAuth flow to get the authorization URL
	oauthResp, err := sidecarClient.OAuthStart(ctx, string(s.User.MXID))
	if err != nil {
		return nil, fmt.Errorf("failed to start OAuth flow: %w", err)
	}

	// Store state for later validation
	s.oauthState = oauthResp.State

	return &bridgev2.LoginStep{
		Type:   bridgev2.LoginStepTypeUserInput,
		StepID: "oauth_code",
		Instructions: fmt.Sprintf(
			"To connect your Claude Pro/Max subscription:\n\n"+
				"1. Open this URL in your browser:\n   %s\n\n"+
				"2. Log in with your Anthropic account\n"+
				"3. Copy the code shown after login\n"+
				"4. Paste the code below\n\n"+
				"(The code expires in 10 minutes)",
			oauthResp.AuthURL,
		),
		UserInputParams: &bridgev2.LoginUserInputParams{
			Fields: []bridgev2.LoginInputDataField{
				{
					Type:        bridgev2.LoginInputFieldTypePassword,
					ID:          "auth_code",
					Name:        "Authorization Code",
					Description: "Code shown after completing browser authentication",
				},
			},
		},
	}, nil
}

// SubmitUserInput processes the submitted OAuth authorization code.
func (s *SidecarLogin) SubmitUserInput(ctx context.Context, input map[string]string) (*bridgev2.LoginStep, error) {
	authCode := input["auth_code"]

	if authCode == "" {
		return nil, fmt.Errorf("authorization code is required")
	}

	if s.oauthState == "" {
		return nil, fmt.Errorf("OAuth flow expired, please start login again")
	}

	// Get sidecar client
	client := s.Connector.getSidecarClient()
	sidecarClient, ok := client.(*sidecar.MessageClient)
	if !ok {
		return nil, fmt.Errorf("sidecar client not available")
	}

	// Complete OAuth flow - exchange code for credentials
	oauthResp, err := sidecarClient.OAuthComplete(ctx, string(s.User.MXID), s.oauthState, authCode)
	if err != nil {
		return nil, fmt.Errorf("OAuth error: %w", err)
	}

	if !oauthResp.Success {
		return nil, fmt.Errorf("authentication failed: %s", oauthResp.Message)
	}

	if oauthResp.CredentialsJSON == nil || *oauthResp.CredentialsJSON == "" {
		return nil, fmt.Errorf("authentication failed: no credentials received")
	}

	credentialsJSON := *oauthResp.CredentialsJSON

	// Test the credentials by making a real API call
	if err := sidecarClient.TestAuth(ctx, string(s.User.MXID), credentialsJSON); err != nil {
		return nil, fmt.Errorf("credentials validation failed: %w", err)
	}

	// Generate a unique login ID based on credentials hash
	hash := sha256.Sum256([]byte(credentialsJSON))
	loginID := networkid.UserLoginID(fmt.Sprintf("sidecar_%s", hex.EncodeToString(hash[:10])))

	userLogin, err := s.User.NewLogin(ctx, &database.UserLogin{
		ID:         loginID,
		RemoteName: "Claude (Pro/Max)",
		Metadata: &UserLoginMetadata{
			APIKey:          "", // No API key for sidecar
			CredentialsJSON: credentialsJSON,
		},
	}, nil)
	if err != nil {
		return nil, err
	}

	// Set up client with sidecar backend
	claudeClient := &ClaudeClient{
		MessageClient: client,
		UserLogin:     userLogin,
		Connector:     s.Connector,
		conversations: make(map[networkid.PortalID]*claudeapi.ConversationManager),
		rateLimiter:   NewRateLimiter(s.Connector.Config.GetRateLimitPerMinute()),
	}
	userLogin.Client = claudeClient

	return &bridgev2.LoginStep{
		Type:         bridgev2.LoginStepTypeComplete,
		StepID:       "complete",
		Instructions: "Successfully connected to Claude via Pro/Max subscription",
		CompleteParams: &bridgev2.LoginCompleteParams{
			UserLogin: userLogin,
		},
	}, nil
}

// Cancel cancels the login process.
func (s *SidecarLogin) Cancel() {}
