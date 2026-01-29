# Migration Plan: Claude to Perplexity API

## Overview

This document outlines the comprehensive plan to migrate the mautrix-claude bridge from Anthropic's Claude API to Perplexity's API. The migration leverages Perplexity's OpenAI-compatible API design and the existing clean MessageClient abstraction in the codebase.

## Executive Summary

**Good News**:
- Perplexity API is OpenAI-compatible (similar request/response format)
- Clean MessageClient interface makes this straightforward
- No sidecar needed (Perplexity API is available to everyone)
- Existing conversation management, rate limiting, and streaming can be reused

**Key Changes**:
- Replace `pkg/claudeapi/` with `pkg/perplexityapi/`
- Update model families: opus/sonnet/haiku → sonar/sonar-pro/sonar-reasoning
- Remove sidecar components (not needed for Perplexity)
- Update all references from "claude" to "perplexity"
- Adapt to OpenAI-style request/response format

## Requirements

### Functional Requirements
- [ ] Support Perplexity API authentication (API key)
- [ ] Implement streaming responses (SSE)
- [ ] Implement non-streaming responses
- [ ] Support all Perplexity models (sonar, sonar-pro, sonar-reasoning variants)
- [ ] Preserve conversation context management
- [ ] Support web search options (search_domain_filter, search_recency_filter)
- [ ] Handle search_results in responses
- [ ] Support image inputs if available in Perplexity API

### Non-Functional Requirements
- [ ] Maintain existing rate limiting
- [ ] Preserve metrics collection
- [ ] Keep all existing tests structure
- [ ] Maintain backward-compatible configuration where possible
- [ ] Clear error messages for Perplexity-specific errors

## Perplexity API Specifics

### Endpoint
```
https://api.perplexity.ai/chat/completions
```

### Authentication
```
Authorization: Bearer <API_KEY>
```

### Request Format (OpenAI-compatible)
```json
{
  "model": "sonar",
  "messages": [
    {"role": "system", "content": "You are a helpful assistant."},
    {"role": "user", "content": "Hello!"}
  ],
  "max_tokens": 4096,
  "temperature": 0.7,
  "stream": true,
  "web_search_options": {
    "search_domain_filter": ["example.com"],
    "search_recency_filter": "month"
  }
}
```

### Response Format
```json
{
  "id": "cmpl-xxx",
  "model": "sonar",
  "created": 1234567890,
  "choices": [{
    "index": 0,
    "message": {
      "role": "assistant",
      "content": "Response text"
    },
    "finish_reason": "stop"
  }],
  "usage": {
    "prompt_tokens": 10,
    "completion_tokens": 20,
    "total_tokens": 30
  },
  "search_results": [
    {"url": "https://...", "title": "..."}
  ]
}
```

### Available Models (2025)
- `sonar` - $1/M input, $1/M output, 128k context
- `sonar-pro` - $3/M input, $15/M output, 200k context  
- `sonar-reasoning` - 128k context
- `sonar-reasoning-pro` - 128k context

## Phase 1: Package Creation - perplexityapi

### 1.1 Create pkg/perplexityapi/interface.go

**File**: `/mnt/data/git/mautrix-perplexity/pkg/perplexityapi/interface.go`

```go
// Package perplexityapi provides a client for the Perplexity API.
package perplexityapi

import (
    "context"
)

// MessageClient is the interface for sending messages to Perplexity.
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
```

### 1.2 Create pkg/perplexityapi/types.go

**File**: `/mnt/data/git/mautrix-perplexity/pkg/perplexityapi/types.go`

Key structures needed:
- `Message` - OpenAI-compatible message format
- `Content` - Text content (images if supported)
- `CreateMessageRequest` - Request structure with web_search_options
- `CreateMessageResponse` - Response with search_results
- `Usage` - Token usage (simpler than Claude's)
- `StreamEvent` - SSE events
- `WebSearchOptions` - Perplexity-specific options
- Error types

**Key Differences from Claude**:
- No image support in initial version (TBD if Perplexity supports)
- No prompt caching (remove EnableCaching field)
- Add WebSearchOptions struct
- Add SearchResults field to response
- Simpler usage tracking (no cache tokens)

### 1.3 Create pkg/perplexityapi/client.go

**File**: `/mnt/data/git/mautrix-perplexity/pkg/perplexityapi/client.go`

Implementation approach:
- Use standard HTTP client (no SDK available)
- POST to `https://api.perplexity.ai/chat/completions`
- Implement SSE parsing for streaming
- Handle OpenAI-format responses
- Convert to our internal StreamEvent format

**Methods to implement**:
```go
type Client struct {
    apiKey     string
    httpClient *http.Client
    baseURL    string
    Log        zerolog.Logger
    Metrics    *Metrics
}

func NewClient(apiKey string, log zerolog.Logger) *Client
func (c *Client) Validate(ctx context.Context) error
func (c *Client) CreateMessage(ctx context.Context, req *CreateMessageRequest) (*CreateMessageResponse, error)
func (c *Client) CreateMessageStream(ctx context.Context, req *CreateMessageRequest) (<-chan StreamEvent, error)
func (c *Client) GetClientType() string
func (c *Client) GetMetrics() *Metrics
```

**Streaming Implementation**:
- Use `bufio.Scanner` to read SSE lines
- Parse "data: {json}" format
- Handle "[DONE]" event
- Convert OpenAI delta format to our StreamEvent

### 1.4 Create pkg/perplexityapi/models.go

**File**: `/mnt/data/git/mautrix-perplexity/pkg/perplexityapi/models.go`

```go
type ModelInfo struct {
    ID          string
    DisplayName string
    Family      string // sonar, sonar-pro, sonar-reasoning
    ContextSize int
}
```

**Model families**:
- `sonar` → latest sonar model
- `sonar-pro` → sonar-pro model
- `sonar-reasoning` → sonar-reasoning model
- `sonar-reasoning-pro` → sonar-reasoning-pro model

**Functions**:
- `inferModelFamily(modelID string) string` - Extract family from model ID
- `GetModelInfo(modelID string) *ModelInfo` - Get model metadata
- `GetDefaultModelID() string` - Return "sonar"
- `EstimateMaxTokens(modelID string) (input, output int)` - Return context limits

**Note**: Perplexity doesn't have a models API endpoint, so we'll use static model definitions.

### 1.5 Create pkg/perplexityapi/metrics.go

**File**: `/mnt/data/git/mautrix-perplexity/pkg/perplexityapi/metrics.go`

Copy from claudeapi with simplifications:
- Remove cache-related metrics
- Keep: request count, token usage, errors, latency

### 1.6 Create pkg/perplexityapi/conversations.go

**File**: `/mnt/data/git/mautrix-perplexity/pkg/perplexityapi/conversations.go`

**Decision**: Can reuse most of Claude's conversation management logic since it's format-agnostic.

Copy from `pkg/claudeapi/conversations.go` and adapt:
- Remove caching-related code
- Keep conversation history management
- Keep token counting logic (update for OpenAI format)
- Keep age-based pruning
- Keep compaction logic (if we still want it)

## Phase 2: Connector Package Updates

### 2.1 Update pkg/connector/config.go

**File**: `/mnt/data/git/mautrix-perplexity/pkg/connector/config.go`

**Changes**:
```go
type Config struct {
    // Update default models
    DefaultModel string `yaml:"default_model"` // sonar, sonar-pro, etc.
    
    // Keep existing
    MaxTokens              int      `yaml:"max_tokens"`
    Temperature            *float64 `yaml:"temperature,omitempty"`
    SystemPrompt           string   `yaml:"system_prompt"`
    ConversationMaxAge     int      `yaml:"conversation_max_age_hours"`
    RateLimitPerMinute     int      `yaml:"rate_limit_per_minute"`
    
    // NEW: Perplexity-specific options
    WebSearchOptions *WebSearchConfig `yaml:"web_search_options,omitempty"`
    
    // REMOVE: Sidecar (not needed)
    // Sidecar SidecarConfig `yaml:"sidecar"`
}

type WebSearchConfig struct {
    SearchDomainFilter   []string `yaml:"search_domain_filter,omitempty"`
    SearchRecencyFilter  string   `yaml:"search_recency_filter,omitempty"` // hour, day, week, month, year
}
```

**Update functions**:
- `IsModelFamily()` - Update for sonar/sonar-pro/sonar-reasoning
- `GetModelFamilyName()` - Update for new families
- `Validate()` - Remove Claude-specific validation
- Update `ExampleConfig` constant

### 2.2 Update pkg/connector/login.go

**File**: `/mnt/data/git/mautrix-perplexity/pkg/connector/login.go`

**Changes**:
```go
// Update APIKeyLogin
type APIKeyLogin struct {
    User      *bridgev2.User
    Connector *PerplexityConnector // Renamed
}

// Update Start() instructions
func (a *APIKeyLogin) Start(ctx context.Context) (*bridgev2.LoginStep, error) {
    return &bridgev2.LoginStep{
        Instructions: "Enter your Perplexity API key. Get one from: https://www.perplexity.ai/settings/api",
        // ...
    }
}

// Update isValidAPIKeyFormat
func isValidAPIKeyFormat(apiKey string) bool {
    // Perplexity API keys format (TBD - check actual format)
    // May be "pplx-" prefix or standard format
    return len(apiKey) > 10 // Adjust based on actual format
}

// REMOVE: SidecarLogin (entire struct and methods)
```

### 2.3 Update pkg/connector/connector.go

**File**: `/mnt/data/git/mautrix-perplexity/pkg/connector/connector.go`

**Changes**:
```go
// Rename struct
type PerplexityConnector struct {
    br     *bridgev2.Bridge
    Config Config
    Log    zerolog.Logger
}

// Update imports
import (
    "go.mau.fi/mautrix-perplexity/pkg/perplexityapi"
    // Remove: "go.mau.fi/mautrix-claude/pkg/sidecar"
)

// Update GetName()
func (c *PerplexityConnector) GetName() bridgev2.BridgeName {
    return bridgev2.BridgeName{
        DisplayName:      "Perplexity AI",
        NetworkURL:       "https://www.perplexity.ai",
        NetworkIcon:      "mxc://maunium.net/perplexity",
        NetworkID:        "perplexity",
        BeeperBridgeType: "go.mau.fi/mautrix-perplexity",
        DefaultPort:      29321, // Different port from Claude
    }
}

// Remove getSidecarClient() method
// Remove sidecar health check in Start()
```

### 2.4 Update pkg/connector/client.go

**File**: `/mnt/data/git/mautrix-perplexity/pkg/connector/client.go`

**Changes**:
```go
// Rename and update struct
type PerplexityClient struct {
    MessageClient perplexityapi.MessageClient
    UserLogin     *bridgev2.UserLogin
    Connector     *PerplexityConnector
    conversations map[networkid.PortalID]*perplexityapi.ConversationManager
    // ... rest stays similar
}

// Update all references from claudeapi to perplexityapi
// Update model resolution logic for new model families
// Remove image support initially (unless Perplexity supports it)
// Add web search options handling
// Update error messages to be Perplexity-specific
```

### 2.5 Update pkg/connector/ghost.go

**File**: `/mnt/data/git/mautrix-perplexity/pkg/connector/ghost.go`

**Changes**:
```go
// Update model family handling
func GetOrCreateGhost(ctx context.Context, modelName string, portal *bridgev2.Portal) (*bridgev2.Ghost, error) {
    // Update for sonar/sonar-pro/sonar-reasoning families
    family := perplexityapi.GetModelFamily(modelName)
    // ...
}

// Update identifiers
Identifiers: []string{fmt.Sprintf("perplexity:%s", modelName)},
```

### 2.6 Update pkg/connector/commands.go

**File**: `/mnt/data/git/mautrix-perplexity/pkg/connector/commands.go`

**Changes**:
- Update `resolveModelAlias()` for new model families
- Update `cmdModel` to show Perplexity models
- Update `cmdModels` to list available Perplexity models
- Update help text and examples
- Remove sidecar-related commands

### 2.7 Update pkg/connector/metadata.go

**File**: Create if doesn't exist, or update existing metadata structs

**Changes**:
- Update metadata structures for Perplexity-specific data
- Remove Claude-specific fields (session IDs, etc.)

## Phase 3: Remove Sidecar Components

### 3.1 Delete pkg/sidecar/

**Action**: Remove entire directory
```bash
rm -rf /mnt/data/git/mautrix-perplexity/pkg/sidecar/
```

**Files to delete**:
- message_client.go
- client.go
- message_client_test.go
- client_test.go
- integration_test.go

### 3.2 Delete sidecar/

**Action**: Remove entire Python sidecar directory
```bash
rm -rf /mnt/data/git/mautrix-perplexity/sidecar/
```

**Files to delete**:
- main.py
- requirements.txt
- README.md
- __pycache__/

### 3.3 Update Documentation

Remove sidecar references from:
- QUICK_START.md
- Any review/analysis documents that mention sidecar

## Phase 4: Project-Wide Updates

### 4.1 Update go.mod

**File**: `/mnt/data/git/mautrix-perplexity/go.mod`

**Changes**:
```go
module go.mau.fi/mautrix-perplexity  // Already updated

// Remove:
// github.com/anthropics/anthropic-sdk-go v1.19.0

// May need to add:
// Additional HTTP/SSE parsing libraries if needed
```

### 4.2 Rename cmd/mautrix-claude/ → cmd/mautrix-perplexity/

**Actions**:
```bash
mv cmd/mautrix-claude cmd/mautrix-perplexity
```

**Update cmd/mautrix-perplexity/main.go**:
```go
// Update package comment
// mautrix-perplexity is a Matrix-Perplexity API puppeting bridge.
package main

import (
    "go.mau.fi/mautrix-perplexity/pkg/connector"
    "maunium.net/go/mautrix/bridgev2/matrix/mxmain"
)

var m mxmain.BridgeMain

func main() {
    c := connector.NewConnector()
    m = mxmain.BridgeMain{
        Name:        "mautrix-perplexity",
        URL:         "https://github.com/mautrix/perplexity",
        Description: "A Matrix-Perplexity API bridge",
        Version:     "0.1.0",
        Connector:   c,
        PostInit:    postInit,
    }

    m.InitVersion(Tag, Commit, BuildTime)
    m.Run()
}
```

### 4.3 Update example-config.yaml

**File**: `/mnt/data/git/mautrix-perplexity/example-config.yaml`

**Changes**:
```yaml
# mautrix-perplexity configuration

# Application service
appservice:
    id: perplexity
    bot_username: perplexitybot
    bot_displayname: Perplexity AI bridge bot
    bot_avatar: mxc://maunium.net/perplexity
    database: sqlite:mautrix-perplexity.db

# Bridge config
bridge:
    # Localpart template of MXIDs for Perplexity models
    # {{.}} is replaced with the model family (e.g., sonar, sonar-pro)
    username_template: perplexity_{{.}}
    
    # Displayname template
    displayname_template: "{{.ProfileName}} (Perplexity AI)"
    
    # Command prefix
    command_prefix: "!perplexity"
    
    # Management room messages
    management_room_text:
        welcome: "Hello, I'm a Perplexity AI bridge bot."
        additional_help: "Get your API key from: https://www.perplexity.ai/settings/api"

# Network (Perplexity) connector options
network:
    # Default model: sonar, sonar-pro, sonar-reasoning, sonar-reasoning-pro
    default_model: sonar
    
    # Maximum tokens for responses
    max_tokens: 4096
    
    # Temperature (0.0-1.0)
    temperature: 1.0
    
    # Default system prompt
    system_prompt: "You are a helpful AI assistant."
    
    # Conversation age limit (hours)
    conversation_max_age_hours: 24
    
    # Rate limiting (requests per minute)
    rate_limit_per_minute: 60
    
    # Web search options (optional)
    web_search_options:
        # Restrict search to specific domains
        search_domain_filter: []
        # Restrict search by recency: hour, day, week, month, year
        search_recency_filter: ""
```

### 4.4 Update Build Files

**Dockerfile** - Update references:
```dockerfile
# Update binary name
COPY mautrix-perplexity /usr/local/bin/
# Remove Python/sidecar dependencies
```

**docker-compose.yaml** - Update:
```yaml
services:
  perplexity-bridge:
    container_name: mautrix-perplexity
    image: mautrix-perplexity:latest
    # Remove sidecar service
```

**.gitlab-ci.yml** - Update build/test jobs

### 4.5 Update README and Documentation

**Action**: Update all documentation files

**Files to update**:
- Create new README.md for Perplexity
- Update QUICK_START.md
- Archive Claude-specific review documents (optional)

**New README.md structure**:
```markdown
# mautrix-perplexity

A Matrix bridge for Perplexity AI.

## Features

- Chat with Perplexity AI models through Matrix
- Support for all Perplexity models (sonar, sonar-pro, sonar-reasoning)
- Web search integration with source citations
- Conversation context management
- Per-room model and prompt configuration
- Rate limiting and error handling

## Requirements

- Go 1.24+
- Matrix homeserver (Synapse, Dendrite, Conduit)
- Perplexity API key (get from https://www.perplexity.ai/settings/api)

## Installation

[Installation steps...]

## Configuration

[Configuration guide...]

## Usage

[Usage examples...]

## Available Models

- **sonar** - Fast, general-purpose model ($1/M tokens)
- **sonar-pro** - Advanced model with extended context ($3/M input, $15/M output)
- **sonar-reasoning** - Reasoning-focused model
- **sonar-reasoning-pro** - Advanced reasoning model

## License

[License info...]
```

## Phase 5: Testing Strategy

### 5.1 Unit Tests

**Create/Update Test Files**:

1. **pkg/perplexityapi/client_test.go**
   - Test API key validation
   - Test non-streaming requests
   - Test streaming requests
   - Test SSE parsing
   - Test error handling

2. **pkg/perplexityapi/models_test.go**
   - Test model family inference
   - Test model info retrieval
   - Test token estimation

3. **pkg/perplexityapi/types_test.go**
   - Test request serialization
   - Test response deserialization
   - Test error type detection

4. **pkg/connector/config_test.go**
   - Test config validation
   - Test model family resolution
   - Test web search options

5. **pkg/connector/client_test.go**
   - Test message handling
   - Test conversation management
   - Test rate limiting
   - Test error formatting

### 5.2 Integration Tests

**Test Scenarios**:
- [ ] Login with Perplexity API key
- [ ] Send text message and receive response
- [ ] Test streaming responses
- [ ] Test non-streaming responses
- [ ] Switch between models
- [ ] Test conversation context
- [ ] Test rate limiting
- [ ] Test error handling (invalid key, rate limits, etc.)
- [ ] Test web search options
- [ ] Test search results in responses

### 5.3 Manual Testing Checklist

- [ ] Bridge starts successfully
- [ ] Login flow works
- [ ] API key validation works
- [ ] Can create chat with default model
- [ ] Can send messages and receive responses
- [ ] Streaming works properly
- [ ] Can switch models
- [ ] Can list available models
- [ ] Rate limiting prevents spam
- [ ] Errors are user-friendly
- [ ] Ghost users display correctly
- [ ] Web search results show up (if enabled)

## Phase 6: Migration Steps (Execution Order)

### Step 1: Preparation (1 hour)
1. [ ] Review existing code and understand all touch points
2. [ ] Set up test Perplexity API account
3. [ ] Create development branch: `git checkout -b feature/perplexity-migration`

### Step 2: Create perplexityapi Package (4-6 hours)
1. [ ] Create pkg/perplexityapi/interface.go
2. [ ] Create pkg/perplexityapi/types.go
3. [ ] Create pkg/perplexityapi/client.go (largest file)
4. [ ] Create pkg/perplexityapi/models.go
5. [ ] Create pkg/perplexityapi/metrics.go
6. [ ] Create pkg/perplexityapi/conversations.go (adapt from Claude)
7. [ ] Write unit tests for each file

### Step 3: Update Connector Package (3-4 hours)
1. [ ] Update pkg/connector/config.go
2. [ ] Update pkg/connector/login.go
3. [ ] Update pkg/connector/connector.go
4. [ ] Update pkg/connector/client.go
5. [ ] Update pkg/connector/ghost.go
6. [ ] Update pkg/connector/commands.go
7. [ ] Update/create pkg/connector/metadata.go
8. [ ] Update connector tests

### Step 4: Remove Sidecar (30 minutes)
1. [ ] Delete pkg/sidecar/ directory
2. [ ] Delete sidecar/ directory
3. [ ] Remove sidecar imports from all files

### Step 5: Project-Wide Updates (2-3 hours)
1. [ ] Update go.mod
2. [ ] Rename cmd/mautrix-claude → cmd/mautrix-perplexity
3. [ ] Update cmd/mautrix-perplexity/main.go
4. [ ] Update example-config.yaml
5. [ ] Update Dockerfile
6. [ ] Update docker-compose.yaml
7. [ ] Update .gitlab-ci.yml
8. [ ] Global find/replace: "claude" → "perplexity" (carefully!)
9. [ ] Global find/replace: "Claude" → "Perplexity"

### Step 6: Documentation (1-2 hours)
1. [ ] Write new README.md
2. [ ] Update QUICK_START.md
3. [ ] Create MIGRATION.md (for existing Claude bridge users)
4. [ ] Update inline code comments

### Step 7: Testing (4-6 hours)
1. [ ] Run all unit tests: `go test ./...`
2. [ ] Fix any failing tests
3. [ ] Build binary: `go build ./cmd/mautrix-perplexity`
4. [ ] Test with real Perplexity API
5. [ ] Manual testing checklist
6. [ ] Integration testing
7. [ ] Performance testing

### Step 8: Cleanup and Polish (1-2 hours)
1. [ ] Remove unused imports
2. [ ] Run `go fmt ./...`
3. [ ] Run `go vet ./...`
4. [ ] Run linter if available
5. [ ] Review all error messages for clarity
6. [ ] Check log messages are appropriate
7. [ ] Verify all TODOs are addressed

### Step 9: Final Review (1 hour)
1. [ ] Code review checklist
2. [ ] Security review (API key handling, input validation)
3. [ ] Performance review (no blocking calls, proper timeouts)
4. [ ] Documentation completeness check

### Step 10: Deployment (1-2 hours)
1. [ ] Create release notes
2. [ ] Tag version: `v0.1.0-perplexity`
3. [ ] Build Docker image
4. [ ] Deploy to test environment
5. [ ] Verify in production-like setting
6. [ ] Merge to master

## Estimated Timeline

**Total Time**: 18-28 hours (2.5-4 days of focused work)

**Breakdown**:
- Package creation: 4-6 hours
- Connector updates: 3-4 hours
- Removal/cleanup: 2-3 hours
- Testing: 4-6 hours
- Documentation: 1-2 hours
- Deployment: 1-2 hours
- Buffer for issues: 3-5 hours

## Risks and Mitigations

### Risk 1: Perplexity API Differences
**Risk**: OpenAI compatibility may not be 100%, edge cases may exist
**Mitigation**: 
- Thorough testing with real API
- Implement comprehensive error handling
- Document known issues

### Risk 2: Missing Features
**Risk**: Perplexity API may not support all Claude features (images, etc.)
**Mitigation**:
- Document unsupported features clearly
- Provide graceful fallbacks or clear error messages
- Consider hybrid approach if needed

### Risk 3: Model Naming Changes
**Risk**: Perplexity may change model names/families
**Mitigation**:
- Use flexible model resolution
- Cache model info
- Make it easy to update model list

### Risk 4: Rate Limiting Differences
**Risk**: Perplexity rate limits may differ from Claude
**Mitigation**:
- Implement adaptive rate limiting
- Monitor API response headers
- Make rate limits configurable

### Risk 5: Breaking Changes for Existing Users
**Risk**: Users running mautrix-claude need migration path
**Mitigation**:
- Create detailed migration guide
- Consider data migration script
- Version clearly (v0.1.0-perplexity)

## Configuration Changes

### Old Config (Claude)
```yaml
network:
  default_model: sonnet  # opus, sonnet, haiku
  sidecar:
    enabled: true
```

### New Config (Perplexity)
```yaml
network:
  default_model: sonar  # sonar, sonar-pro, sonar-reasoning
  web_search_options:
    search_domain_filter: []
    search_recency_filter: ""
  # No sidecar section
```

## Data Migration

### User Logins
- Old: stored with "claude_" prefix
- New: store with "perplexity_" prefix
- Action: Users need to re-login (no automated migration)

### Conversations
- Format is mostly compatible
- May need to clear old conversations on first run
- Or: provide migration script to update metadata

### Ghost Users
- Old: claude:opus, claude:sonnet, etc.
- New: perplexity:sonar, perplexity:sonar-pro, etc.
- Action: Will create new ghost users (old ones become inactive)

## Success Criteria

- [ ] All unit tests pass
- [ ] Integration tests pass
- [ ] Bridge starts without errors
- [ ] Can login with Perplexity API key
- [ ] Can send and receive messages
- [ ] Streaming works correctly
- [ ] All models accessible
- [ ] Rate limiting functions
- [ ] Error messages are clear
- [ ] Documentation is complete
- [ ] No security issues
- [ ] Performance is acceptable (< 1s for simple queries)

## Open Questions

1. **Q**: Does Perplexity API support images/multimodal input?
   **A**: TBD - Need to test. If not, remove image support initially.

2. **Q**: What's the exact API key format for Perplexity?
   **A**: TBD - Update `isValidAPIKeyFormat()` after checking.

3. **Q**: Are there any usage quotas or limits we should enforce?
   **A**: TBD - Check Perplexity docs and add to rate limiter.

4. **Q**: Should we support OpenAI SDK compatibility layer?
   **A**: Decision: No, use direct HTTP for simplicity and control.

5. **Q**: How to handle search_results in UI?
   **A**: Decision: Include in response text or as separate notice.

## Dependencies

### External
- Perplexity API account and key
- Go 1.24+
- Matrix homeserver

### Internal (Preserved)
- mautrix bridgev2 framework
- zerolog for logging
- Standard library HTTP/JSON

### Removed
- anthropic-sdk-go
- Python sidecar dependencies
- Claude Agent SDK

## Post-Migration Tasks

### Immediate
- [ ] Monitor error rates
- [ ] Collect user feedback
- [ ] Fix any discovered bugs

### Short-term (1-2 weeks)
- [ ] Optimize performance
- [ ] Add advanced features (web search UI)
- [ ] Improve error messages based on feedback

### Long-term (1-3 months)
- [ ] Add image support if Perplexity adds it
- [ ] Consider advanced features (citations UI, search filters UI)
- [ ] Performance optimizations
- [ ] Enhanced metrics and monitoring

## Rollback Plan

If critical issues are found:

1. Keep old mautrix-claude code in separate branch
2. Document rollback steps
3. Provide migration back to Claude (if needed)
4. Clear communication to users

## Communication Plan

### To Users
- Announce migration plans
- Provide migration timeline
- Document new features and changes
- Offer support for migration issues

### To Contributors
- Update contribution guidelines
- Document new architecture
- Provide development setup guide

## Appendix: File Change Summary

### Files to Create (16)
- pkg/perplexityapi/interface.go
- pkg/perplexityapi/types.go
- pkg/perplexityapi/client.go
- pkg/perplexityapi/models.go
- pkg/perplexityapi/metrics.go
- pkg/perplexityapi/conversations.go
- pkg/perplexityapi/client_test.go
- pkg/perplexityapi/types_test.go
- pkg/perplexityapi/models_test.go
- pkg/perplexityapi/metrics_test.go
- pkg/perplexityapi/conversations_test.go
- cmd/mautrix-perplexity/main.go (moved + updated)
- README.md (rewritten)
- QUICK_START.md (rewritten)
- MIGRATION.md (new)
- example-config.yaml (updated)

### Files to Modify (15)
- go.mod
- go.sum
- Dockerfile
- docker-compose.yaml
- .gitlab-ci.yml
- pkg/connector/config.go
- pkg/connector/login.go
- pkg/connector/connector.go
- pkg/connector/client.go
- pkg/connector/ghost.go
- pkg/connector/commands.go
- pkg/connector/config_test.go
- pkg/connector/login_test.go
- pkg/connector/client_test.go
- pkg/connector/connector_test.go

### Files to Delete (11)
- pkg/claudeapi/* (6 files - to be replaced)
- pkg/sidecar/* (5 files)
- sidecar/* (Python files)
- cmd/mautrix-claude/main.go (moved to perplexity)

### Files to Archive (Optional)
- Old review/analysis documents mentioning Claude
- Can keep for reference but mark as archived

## Conclusion

This migration plan provides a comprehensive roadmap for converting the mautrix-claude bridge to work with Perplexity's API. The clean architecture with the MessageClient interface makes this migration straightforward, primarily involving the creation of a new perplexityapi package and updates to connector configuration.

The estimated timeline of 2.5-4 days is realistic for a focused developer familiar with the codebase. The main work is in implementing the perplexityapi client with proper SSE streaming support.

Key advantages of this migration:
- Simpler architecture (no sidecar needed)
- OpenAI-compatible API is well-documented
- Direct API access for all users
- Web search integration built-in
- Cleaner codebase without dual API/sidecar paths

Next step: Begin with Phase 1 (perplexityapi package creation) and proceed sequentially through the phases.
