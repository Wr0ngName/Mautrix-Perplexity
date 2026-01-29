# Migration Plan v2: Claude to Perplexity API (Python SDK via Sidecar)

## Overview

**REVISED APPROACH**: Use the Perplexity Python SDK (https://pypi.org/project/perplexityai/) with the existing sidecar infrastructure. This is MUCH simpler than implementing HTTP client in Go.

## Why Sidecar-Only Approach is Better

1. **Reuse existing infrastructure**: Sidecar architecture already exists and works
2. **Official SDK**: Perplexity has official Python SDK (perplexityai)
3. **Simpler migration**: Just swap Claude Agent SDK for Perplexity SDK
4. **Less code**: No need to implement HTTP/SSE parsing in Go
5. **Cleaner architecture**: Single code path (no dual API/sidecar complexity)
6. **Better maintained**: Official SDK gets updates automatically

## Key Changes from Original Plan

| Aspect | Original Plan | Revised Plan |
|--------|---------------|--------------|
| **Go API Package** | Create pkg/perplexityapi | Delete pkg/claudeapi |
| **Sidecar** | Delete sidecar/ | Keep and update sidecar/ |
| **SDK** | Implement HTTP client | Use perplexityai SDK |
| **Login Flow** | API key only | API key only (simpler) |
| **Complexity** | Medium (HTTP+SSE) | Low (SDK wrapper) |
| **Lines Changed** | ~2000 | ~500 |

## Requirements

### Functional Requirements
- [ ] Support Perplexity API authentication (API key)
- [ ] Implement streaming responses via SDK
- [ ] Implement non-streaming responses via SDK
- [ ] Support all Perplexity models
- [ ] Preserve conversation context management
- [ ] Support web search options
- [ ] Handle search_results in responses

### Non-Functional Requirements
- [ ] Maintain existing rate limiting
- [ ] Preserve metrics collection
- [ ] Keep all existing tests structure
- [ ] Clear error messages

## Phase 1: Update Python Sidecar (sidecar/)

### 1.1 Update requirements.txt

**File**: `/mnt/data/git/mautrix-perplexity/sidecar/requirements.txt`

```txt
# Replace claude_agent_sdk with perplexityai
perplexityai==0.27.0

# Keep existing dependencies
fastapi
uvicorn
pydantic
prometheus-client
```

### 1.2 Update sidecar/main.py

**File**: `/mnt/data/git/mautrix-perplexity/sidecar/main.py`

**Changes needed**:

```python
# REMOVE Claude Agent SDK imports
# from claude_agent_sdk import query, ClaudeAgentOptions, ClaudeSDKClient, ...

# ADD Perplexity SDK imports
from perplexityai import Perplexity, ChatCompletion

# UPDATE: Remove Claude CLI/OAuth code
# - Remove OAuth endpoints (/oauth/start, /oauth/complete)
# - Remove CLI integration
# - Remove session management (Perplexity is stateless)
# - Remove credentials manager

# UPDATE: Simplify to API key only
# Store API key per user (from Go bridge)

# UPDATE: ChatRequest model
class ChatRequest(BaseModel):
    portal_id: str
    user_id: Optional[str] = None
    api_key: str  # Per-user API key from Go bridge
    message: str
    content: Optional[list[ContentBlock]] = None  # Keep for images if supported
    system_prompt: Optional[str] = None
    model: Optional[str] = "sonar"  # Default to sonar
    stream: bool = False
    web_search_options: Optional[dict] = None  # NEW for Perplexity

# UPDATE: /v1/chat endpoint
@app.post("/v1/chat")
async def chat(request: ChatRequest):
    # Initialize Perplexity client with user's API key
    client = Perplexity(api_key=request.api_key)
    
    # Build messages array (OpenAI format)
    messages = []
    if request.system_prompt:
        messages.append({"role": "system", "content": request.system_prompt})
    
    # Add user message
    if request.content and any(b.type == "image" for b in request.content):
        # Multimodal message
        content_blocks = []
        for block in request.content:
            if block.type == "text":
                content_blocks.append({"type": "text", "text": block.text})
            elif block.type == "image":
                content_blocks.append({
                    "type": "image_url",
                    "image_url": {
                        "url": f"data:{block.source.media_type};base64,{block.source.data}"
                    }
                })
        messages.append({"role": "user", "content": content_blocks})
    else:
        # Text-only message
        messages.append({"role": "user", "content": request.message})
    
    # Make API call
    if request.stream:
        # Streaming response
        async def generate():
            stream = client.chat.completions.create(
                model=request.model,
                messages=messages,
                stream=True,
                max_tokens=4096,
                **request.web_search_options or {}
            )
            for chunk in stream:
                # Convert to SSE format expected by Go bridge
                yield f"data: {json.dumps(chunk.dict())}\n\n"
            yield "data: [DONE]\n\n"
        
        return StreamingResponse(generate(), media_type="text/event-stream")
    else:
        # Non-streaming response
        response = client.chat.completions.create(
            model=request.model,
            messages=messages,
            stream=False,
            max_tokens=4096,
            **request.web_search_options or {}
        )
        return {
            "response": response.choices[0].message.content,
            "model": response.model,
            "usage": {
                "input_tokens": response.usage.prompt_tokens,
                "output_tokens": response.usage.completion_tokens,
            },
            "search_results": getattr(response, 'search_results', [])
        }

# REMOVE: OAuth endpoints
# REMOVE: Session management
# REMOVE: CLI integration  
# REMOVE: Credentials manager

# SIMPLIFY: Health endpoint
@app.get("/v1/health")
async def health():
    return {
        "status": "healthy",
        "authenticated": True,  # Always true with API key
    }
```

**Key simplifications**:
- Remove ~500 lines of OAuth/CLI code
- Remove session management (Perplexity is stateless)
- Remove credentials manager
- Much simpler: just API key + SDK wrapper

## Phase 2: Update Go Connector

### 2.1 Delete pkg/claudeapi/

**Action**: Remove entire package
```bash
rm -rf /mnt/data/git/mautrix-perplexity/pkg/claudeapi/
```

We don't need this anymore - all API calls go through sidecar.

### 2.2 Update pkg/connector/config.go

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
    
    // Sidecar stays (but simplified)
    Sidecar SidecarConfig `yaml:"sidecar"`
}

type WebSearchConfig struct {
    SearchDomainFilter   []string `yaml:"search_domain_filter,omitempty"`
    SearchRecencyFilter  string   `yaml:"search_recency_filter,omitempty"`
}

// Update validation for new model families
func (c *Config) Validate() error {
    model := strings.ToLower(c.DefaultModel)
    isFamily := model == "sonar" || model == "sonar-pro" || 
                model == "sonar-reasoning" || model == "sonar-reasoning-pro"
    isPerplexityModel := strings.Contains(model, "sonar")
    if !isFamily && !isPerplexityModel {
        return fmt.Errorf("invalid model: must be sonar family")
    }
    // ... rest of validation
}
```

### 2.3 Update pkg/connector/login.go

**File**: `/mnt/data/git/mautrix-perplexity/pkg/connector/login.go`

**Changes**:
```go
// REMOVE: SidecarLogin (OAuth flow not needed)

// KEEP: APIKeyLogin (but simplified)
type APIKeyLogin struct {
    User      *bridgev2.User
    Connector *PerplexityConnector
}

func (a *APIKeyLogin) Start(ctx context.Context) (*bridgev2.LoginStep, error) {
    return &bridgev2.LoginStep{
        Instructions: "Enter your Perplexity API key. Get one from: https://www.perplexity.ai/settings/api",
        // ... rest of login flow
    }
}

// UPDATE: API key format validation
func isValidAPIKeyFormat(apiKey string) bool {
    // Perplexity API keys start with "pplx-"
    return strings.HasPrefix(apiKey, "pplx-") && len(apiKey) > 10
}

// UPDATE: Validation uses sidecar
func (a *APIKeyLogin) SubmitUserInput(ctx context.Context, input map[string]string) (*bridgev2.LoginStep, error) {
    apiKey := input["api_key"]
    
    if !isValidAPIKeyFormat(apiKey) {
        return nil, fmt.Errorf("invalid API key format")
    }
    
    // Test via sidecar
    client := sidecar.NewMessageClient(
        a.Connector.Config.Sidecar.GetURL(),
        time.Duration(a.Connector.Config.Sidecar.GetTimeout())*time.Second,
        a.Connector.Log,
    )
    
    // Simple validation: try to make a minimal request
    if err := client.Validate(ctx, apiKey); err != nil {
        return nil, fmt.Errorf("invalid API key: %w", err)
    }
    
    // Store API key in user metadata
    loginID := networkid.UserLoginID(fmt.Sprintf("perplexity_%s", hashAPIKey(apiKey)))
    userLogin, err := a.User.NewLogin(ctx, &database.UserLogin{
        ID:         loginID,
        RemoteName: "Perplexity API User",
        Metadata: &UserLoginMetadata{
            APIKey: apiKey,
        },
    }, nil)
    
    // ... rest of login flow
}
```

### 2.4 Update pkg/sidecar/message_client.go

**File**: `/mnt/data/git/mautrix-perplexity/pkg/sidecar/message_client.go`

**Changes**:
```go
// UPDATE: ChatRequest to include API key
type ChatRequest struct {
    PortalID        string              `json:"portal_id"`
    UserID          string              `json:"user_id,omitempty"`
    APIKey          string              `json:"api_key"`  // NEW: Per-user API key
    Message         string              `json:"message"`
    Content         []ContentBlock      `json:"content,omitempty"`
    SystemPrompt    *string             `json:"system_prompt,omitempty"`
    Model           *string             `json:"model,omitempty"`
    Stream          bool                `json:"stream"`
    WebSearchOptions *WebSearchOptions  `json:"web_search_options,omitempty"` // NEW
}

type WebSearchOptions struct {
    SearchDomainFilter  []string `json:"search_domain_filter,omitempty"`
    SearchRecencyFilter string   `json:"search_recency_filter,omitempty"`
}

// REMOVE: OAuth-related methods
// REMOVE: Session management methods

// UPDATE: CreateMessageStream to pass API key
func (c *MessageClient) CreateMessageStream(ctx context.Context, req *claudeapi.CreateMessageRequest, apiKey string) (<-chan StreamEvent, error) {
    chatReq := ChatRequest{
        PortalID:      getPortalIDFromContext(ctx),
        UserID:        getUserIDFromContext(ctx),
        APIKey:        apiKey,  // Pass user's API key
        Message:       extractMessageText(req.Messages),
        Content:       convertToContentBlocks(req.Messages),
        SystemPrompt:  &req.System,
        Model:         &req.Model,
        Stream:        true,
        WebSearchOptions: getWebSearchOptions(req),
    }
    
    // ... rest of implementation
}
```

### 2.5 Update pkg/connector/client.go

**File**: `/mnt/data/git/mautrix-perplexity/pkg/connector/client.go`

**Changes**:
```go
// UPDATE: Client struct
type PerplexityClient struct {
    SidecarClient sidecar.MessageClient  // Only sidecar, no direct API
    UserLogin     *bridgev2.UserLogin
    Connector     *PerplexityConnector
    conversations map[networkid.PortalID]*ConversationManager
    rateLimiter   *RateLimiter
}

// UPDATE: HandleMatrixMessage to pass API key to sidecar
func (c *PerplexityClient) HandleMatrixMessage(ctx context.Context, msg *bridgev2.MatrixMessage) (*bridgev2.MatrixMessageResponse, error) {
    // Get API key from user metadata
    metadata := c.UserLogin.Metadata.(*UserLoginMetadata)
    apiKey := metadata.APIKey
    
    // ... build request ...
    
    // Call sidecar with API key
    stream, err := c.SidecarClient.CreateMessageStream(ctx, req, apiKey)
    
    // ... handle response ...
}

// REMOVE: Direct API client code paths
// REMOVE: API vs sidecar switching logic
```

### 2.6 Update pkg/connector/connector.go

**File**: `/mnt/data/git/mautrix-perplexity/pkg/connector/connector.go`

**Changes**:
```go
// Rename struct
type PerplexityConnector struct {
    br     *bridgev2.Bridge
    Config Config
    Log    zerolog.Logger
}

// Update GetName()
func (c *PerplexityConnector) GetName() bridgev2.BridgeName {
    return bridgev2.BridgeName{
        DisplayName:      "Perplexity AI",
        NetworkURL:       "https://www.perplexity.ai",
        NetworkIcon:      "mxc://maunium.net/perplexity",
        NetworkID:        "perplexity",
        BeeperBridgeType: "go.mau.fi/mautrix-perplexity",
        DefaultPort:      29321,
    }
}

// UPDATE: Start() to validate sidecar
func (c *PerplexityConnector) Start(ctx context.Context) error {
    // Always check sidecar (it's required now)
    health, err := c.getSidecarClient().Health(ctx)
    if err != nil {
        return fmt.Errorf("sidecar not available: %w", err)
    }
    logger.Info("Perplexity sidecar ready")
    return nil
}
```

### 2.7 Update pkg/connector/ghost.go

**File**: `/mnt/data/git/mautrix-perplexity/pkg/connector/ghost.go`

**Changes**:
```go
// Update model family handling
func GetOrCreateGhost(ctx context.Context, modelName string, portal *bridgev2.Portal) (*bridgev2.Ghost, error) {
    family := inferModelFamily(modelName)
    // ... rest stays similar ...
}

func inferModelFamily(modelID string) string {
    id := strings.ToLower(modelID)
    switch {
    case strings.Contains(id, "sonar-pro"):
        return "sonar-pro"
    case strings.Contains(id, "sonar-reasoning-pro"):
        return "sonar-reasoning-pro"
    case strings.Contains(id, "sonar-reasoning"):
        return "sonar-reasoning"
    case strings.Contains(id, "sonar"):
        return "sonar"
    default:
        return ""
    }
}
```

### 2.8 Update pkg/connector/commands.go

**File**: `/mnt/data/git/mautrix-perplexity/pkg/connector/commands.go`

**Changes**:
```go
// Update model resolution
func resolveModelAlias(ctx context.Context, client sidecar.MessageClient, modelArg string, apiKey string) (string, error) {
    switch strings.ToLower(modelArg) {
    case "sonar":
        return "sonar", nil
    case "sonar-pro", "pro":
        return "sonar-pro", nil
    case "sonar-reasoning", "reasoning":
        return "sonar-reasoning", nil
    case "sonar-reasoning-pro", "reasoning-pro":
        return "sonar-reasoning-pro", nil
    default:
        // Validate model exists via sidecar
        return modelArg, client.ValidateModel(ctx, modelArg, apiKey)
    }
}

// Update help text
func cmdModels(ce *commands.Event) {
    ce.Reply("Available Perplexity models:\n\n" +
        "**sonar** - Fast, general-purpose ($1/M)\n" +
        "**sonar-pro** - Advanced with extended context ($3/M in, $15/M out)\n" +
        "**sonar-reasoning** - Reasoning-focused\n" +
        "**sonar-reasoning-pro** - Advanced reasoning\n\n" +
        "Use `!perplexity model <name>` to switch models")
}
```

## Phase 3: Project-Wide Updates

### 3.1 Update go.mod

**File**: `/mnt/data/git/mautrix-perplexity/go.mod`

```go
module go.mau.fi/mautrix-perplexity  // Already updated

// REMOVE:
// github.com/anthropics/anthropic-sdk-go v1.19.0

// Keep:
// All other dependencies (zerolog, mautrix, etc.)
```

### 3.2 Rename cmd directory

```bash
mv cmd/mautrix-claude cmd/mautrix-perplexity
```

### 3.3 Update cmd/mautrix-perplexity/main.go

```go
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

### 3.4 Update example-config.yaml

```yaml
# mautrix-perplexity configuration

appservice:
    id: perplexity
    bot_username: perplexitybot
    bot_displayname: Perplexity AI bridge bot
    database: sqlite:mautrix-perplexity.db

bridge:
    username_template: perplexity_{{.}}
    displayname_template: "{{.ProfileName}} (Perplexity AI)"
    command_prefix: "!perplexity"
    
    management_room_text:
        welcome: "Hello, I'm a Perplexity AI bridge bot."
        additional_help: "Get your API key from: https://www.perplexity.ai/settings/api"

network:
    default_model: sonar
    max_tokens: 4096
    temperature: 1.0
    system_prompt: "You are a helpful AI assistant."
    conversation_max_age_hours: 24
    rate_limit_per_minute: 60
    
    # Web search options (optional)
    web_search_options:
        search_domain_filter: []
        search_recency_filter: ""
    
    # Sidecar is REQUIRED (not optional)
    sidecar:
        enabled: true  # Always true
        url: "http://localhost:8090"
        timeout: 300
```

### 3.5 Update Dockerfile

```dockerfile
# Python stage for sidecar
FROM python:3.11-slim as python-builder

WORKDIR /app/sidecar

# Copy sidecar requirements and install
COPY sidecar/requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt

# Go stage
FROM golang:1.24 as go-builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=1 go build -o mautrix-perplexity ./cmd/mautrix-perplexity

# Final stage
FROM debian:bookworm-slim

# Install Python runtime and Node.js (if needed by SDK)
RUN apt-get update && apt-get install -y \
    python3 \
    python3-pip \
    && rm -rf /var/lib/apt/lists/*

# Copy sidecar
COPY --from=python-builder /app/sidecar /app/sidecar
COPY sidecar/ /app/sidecar/

# Copy Go binary
COPY --from=go-builder /app/mautrix-perplexity /usr/local/bin/

# Expose ports
EXPOSE 29321 8090

# Start both sidecar and bridge
CMD python3 /app/sidecar/main.py & \
    mautrix-perplexity
```

### 3.6 Update docker-compose.yaml

```yaml
version: '3.8'

services:
  perplexity-bridge:
    build: .
    container_name: mautrix-perplexity
    restart: unless-stopped
    volumes:
      - ./data:/data
      - ./logs:/logs
    environment:
      - PERPLEXITY_SIDECAR_PORT=8090
    ports:
      - "29321:29321"  # Bridge
      - "8090:8090"    # Sidecar
```

## Phase 4: Testing

### 4.1 Unit Tests

Update test files:
- pkg/sidecar/message_client_test.go
- pkg/connector/config_test.go
- pkg/connector/login_test.go
- pkg/connector/client_test.go

### 4.2 Integration Tests

Test scenarios:
- [ ] Sidecar starts successfully
- [ ] Login with Perplexity API key
- [ ] Send message, receive response
- [ ] Streaming works
- [ ] Model switching works
- [ ] Web search options work
- [ ] Rate limiting works
- [ ] Error handling works

### 4.3 Manual Testing

- [ ] Build: `go build ./cmd/mautrix-perplexity`
- [ ] Start sidecar: `python3 sidecar/main.py`
- [ ] Start bridge: `./mautrix-perplexity`
- [ ] Login with Perplexity API key
- [ ] Test full message flow

## Phase 5: Documentation

### 5.1 Update README.md

```markdown
# mautrix-perplexity

A Matrix bridge for Perplexity AI using the official Python SDK.

## Architecture

This bridge uses a **sidecar architecture**:
- **Go bridge**: Handles Matrix protocol and user management
- **Python sidecar**: Wraps Perplexity Python SDK

## Installation

1. Install dependencies:
   - Go 1.24+
   - Python 3.11+
   - Perplexity API key

2. Install Python dependencies:
   ```bash
   pip install -r sidecar/requirements.txt
   ```

3. Build Go bridge:
   ```bash
   go build ./cmd/mautrix-perplexity
   ```

4. Configure and start both components

## Usage

[Usage examples...]
```

### 5.2 Create MIGRATION.md

Document migration from Claude bridge for existing users.

## Migration Steps (Execution Order)

### Step 1: Update Python Sidecar (3-4 hours)
1. [ ] Update requirements.txt
2. [ ] Update main.py (swap SDK, remove OAuth/CLI)
3. [ ] Test sidecar standalone
4. [ ] Verify API key authentication works

### Step 2: Delete pkg/claudeapi (5 minutes)
1. [ ] rm -rf pkg/claudeapi/

### Step 3: Update Go Connector (2-3 hours)
1. [ ] Update config.go (model families, web search)
2. [ ] Update login.go (remove OAuth, simplify)
3. [ ] Update sidecar/message_client.go (add API key, web search)
4. [ ] Update connector.go (rename, update metadata)
5. [ ] Update client.go (remove dual paths)
6. [ ] Update ghost.go (model families)
7. [ ] Update commands.go (model lists, help text)

### Step 4: Project-Wide (1-2 hours)
1. [ ] Update go.mod
2. [ ] Rename cmd directory
3. [ ] Update main.go
4. [ ] Update example-config.yaml
5. [ ] Update Dockerfile
6. [ ] Update docker-compose.yaml

### Step 5: Testing (3-4 hours)
1. [ ] Run all tests
2. [ ] Integration testing
3. [ ] Manual testing

### Step 6: Documentation (1-2 hours)
1. [ ] Update README.md
2. [ ] Create MIGRATION.md
3. [ ] Update inline comments

## Estimated Timeline

**Total Time**: 10-15 hours (1.5-2 days)

**Breakdown**:
- Python sidecar update: 3-4 hours
- Go connector update: 2-3 hours
- Project-wide updates: 1-2 hours
- Testing: 3-4 hours
- Documentation: 1-2 hours

**This is 50% less time than the original plan!**

## Advantages of This Approach

1. **Less code to write**: ~500 LOC vs ~2000 LOC
2. **Reuse infrastructure**: Sidecar already works
3. **Official SDK**: Better maintained, automatic updates
4. **Simpler**: Single code path, no HTTP/SSE parsing
5. **Faster**: No learning curve for Perplexity API internals
6. **Tested**: Sidecar pattern already proven with Claude

## Success Criteria

- [ ] Sidecar starts without errors
- [ ] Bridge starts without errors
- [ ] Login with Perplexity API key works
- [ ] Messages send and receive correctly
- [ ] Streaming works
- [ ] All tests pass
- [ ] Documentation complete

## Next Steps

1. Install Perplexity Python SDK: `pip install perplexityai`
2. Start with Phase 1: Update Python sidecar
3. Test sidecar standalone before touching Go code
4. Proceed sequentially through phases

---

**This revised plan is MUCH simpler and faster than the original approach!**
