# Migration Plan: mautrix-claude → mautrix-perplexity

## Overview

This plan outlines the migration from Claude API to Perplexity API for the Matrix bridge.

### Key Findings from Research

1. **Perplexity API is publicly available** - No Pro subscription required. Pay-as-you-go credits system.
2. **OpenAI-compatible API** - Uses same chat completions format as OpenAI
3. **Official Python SDK exists** - `pip install perplexityai` (version 0.23.0+)
4. **No sidecar needed** - Direct API access is sufficient (simpler than Claude)

### Architecture Decision

**Approach: Direct API via Go HTTP Client (OpenAI-compatible)**

Since Perplexity API is:
- OpenAI-compatible (same request/response format)
- Publicly accessible with simple API key auth
- Supports streaming via SSE

We can use a **simpler architecture**:
- Remove the Python sidecar entirely
- Implement direct HTTP client in Go
- Reuse existing conversation management

This is simpler than Claude because:
- No OAuth flows (just API key)
- No Pro/Max tier complexity
- No Agent SDK needed
- Standard OpenAI format

### Available Models (2025)

| Model | Input Cost | Output Cost | Context |
|-------|------------|-------------|---------|
| `sonar` | $1/M | $1/M | 128k |
| `sonar-pro` | $3/M | $15/M | 200k |
| `sonar-reasoning` | - | - | 128k |
| `sonar-reasoning-pro` | - | - | 128k |

## Scope of Changes

### Files to Create

1. **`pkg/perplexityapi/client.go`** (~350 lines)
   - HTTP client for Perplexity API
   - Implements `MessageClient` interface
   - SSE streaming support
   - OpenAI-compatible request/response handling

2. **`pkg/perplexityapi/types.go`** (~150 lines)
   - Request/response types (OpenAI format)
   - Perplexity-specific fields (search_results, web_search_options)

3. **`pkg/perplexityapi/models.go`** (~100 lines)
   - Model constants and validation
   - Family resolution (sonar → sonar, etc.)

### Files to Modify

1. **`pkg/connector/config.go`**
   - Change model validation (sonar family instead of claude)
   - Add `web_search_options` config
   - Remove sidecar config (optional, not required)

2. **`pkg/connector/login.go`**
   - Update API key format validation (`pplx-*` prefix)
   - Update validation endpoint
   - Remove OAuth/sidecar login flow

3. **`pkg/connector/connector.go`**
   - Rename to PerplexityConnector
   - Update bridge metadata
   - Update model family handling

4. **`pkg/connector/client.go`**
   - Use perplexityapi.Client instead of claudeapi.Client
   - Remove sidecar code paths
   - Update ghost user handling for new models

5. **`pkg/connector/commands.go`**
   - Update model list and help text
   - Update command prefix suggestions

6. **`pkg/connector/queryhandler.go`**
   - Update model family detection for ghost users

7. **`go.mod`**
   - Remove anthropic-sdk-go dependency
   - Module name already correct

8. **`cmd/mautrix-claude/main.go`** → **`cmd/mautrix-perplexity/main.go`**
   - Rename directory
   - Update imports and metadata

9. **`example-config.yaml`**
   - Update default model, command prefix, etc.

### Files to Delete

1. **`pkg/claudeapi/`** - Entire directory (client.go, types.go, models.go, etc.)
2. **`pkg/sidecar/`** - Entire directory (not needed for Perplexity)
3. **`sidecar/`** - Python sidecar directory (not needed)

### Files to Keep (with minimal changes)

1. **`pkg/claudeapi/conversations.go`** → Move to connector or keep shared
   - ConversationManager works for any LLM
   - Token counting may need adjustment

2. **`pkg/claudeapi/interface.go`** → Move to perplexityapi or shared
   - MessageClient interface stays the same

3. **`pkg/claudeapi/metrics.go`** → Move to perplexityapi
   - Prometheus metrics stay similar

## Implementation Phases

### Phase 1: Create perplexityapi Package

**1.1 Create `pkg/perplexityapi/types.go`**

```go
package perplexityapi

// ChatCompletionRequest follows OpenAI format
type ChatCompletionRequest struct {
    Model            string           `json:"model"`
    Messages         []Message        `json:"messages"`
    MaxTokens        int              `json:"max_tokens,omitempty"`
    Temperature      *float64         `json:"temperature,omitempty"`
    TopP             *float64         `json:"top_p,omitempty"`
    Stream           bool             `json:"stream,omitempty"`
    // Perplexity-specific
    WebSearchOptions *WebSearchOptions `json:"web_search_options,omitempty"`
}

type Message struct {
    Role    string `json:"role"`
    Content string `json:"content"`
}

type WebSearchOptions struct {
    SearchDomainFilter  []string `json:"search_domain_filter,omitempty"`
    SearchRecencyFilter string   `json:"search_recency_filter,omitempty"`
}

type ChatCompletionResponse struct {
    ID      string   `json:"id"`
    Object  string   `json:"object"`
    Created int64    `json:"created"`
    Model   string   `json:"model"`
    Choices []Choice `json:"choices"`
    Usage   *Usage   `json:"usage,omitempty"`
    // Perplexity-specific
    SearchResults []SearchResult `json:"search_results,omitempty"`
}

type Choice struct {
    Index        int      `json:"index"`
    Message      *Message `json:"message,omitempty"`
    Delta        *Message `json:"delta,omitempty"`
    FinishReason string   `json:"finish_reason,omitempty"`
}

type Usage struct {
    PromptTokens     int `json:"prompt_tokens"`
    CompletionTokens int `json:"completion_tokens"`
    TotalTokens      int `json:"total_tokens"`
}

type SearchResult struct {
    Title string `json:"title"`
    URL   string `json:"url"`
    Date  string `json:"date,omitempty"`
}
```

**1.2 Create `pkg/perplexityapi/client.go`**

```go
package perplexityapi

import (
    "bufio"
    "bytes"
    "context"
    "encoding/json"
    "fmt"
    "io"
    "net/http"
    "strings"
)

const (
    BaseURL = "https://api.perplexity.ai"
)

type Client struct {
    apiKey     string
    httpClient *http.Client
    baseURL    string
    metrics    *Metrics
}

func NewClient(apiKey string) *Client {
    return &Client{
        apiKey:     apiKey,
        httpClient: &http.Client{},
        baseURL:    BaseURL,
        metrics:    NewMetrics(),
    }
}

func (c *Client) CreateMessage(ctx context.Context, req *CreateMessageRequest) (*CreateMessageResponse, error) {
    // Implementation
}

func (c *Client) CreateMessageStream(ctx context.Context, req *CreateMessageRequest) (<-chan StreamEvent, error) {
    // SSE streaming implementation
}

func (c *Client) Validate(ctx context.Context) error {
    // Make minimal request to validate API key
}
```

**1.3 Create `pkg/perplexityapi/models.go`**

```go
package perplexityapi

var ValidModels = map[string]bool{
    "sonar":               true,
    "sonar-pro":           true,
    "sonar-reasoning":     true,
    "sonar-reasoning-pro": true,
}

var ModelFamilies = []string{"sonar", "sonar-pro", "sonar-reasoning", "sonar-reasoning-pro"}

func GetModelFamily(modelID string) string {
    id := strings.ToLower(modelID)
    switch {
    case strings.HasPrefix(id, "sonar-reasoning-pro"):
        return "sonar-reasoning-pro"
    case strings.HasPrefix(id, "sonar-reasoning"):
        return "sonar-reasoning"
    case strings.HasPrefix(id, "sonar-pro"):
        return "sonar-pro"
    case strings.HasPrefix(id, "sonar"):
        return "sonar"
    default:
        return "sonar"
    }
}
```

### Phase 2: Update Connector Package

**2.1 Update `pkg/connector/config.go`**

- Remove sidecar config (or make optional)
- Update model validation for Perplexity models
- Add web_search_options config

**2.2 Update `pkg/connector/login.go`**

- Simplify to API key only (no OAuth)
- Validate key format: `pplx-*`
- Use perplexityapi.Client.Validate()

**2.3 Update `pkg/connector/connector.go`**

- Rename ClaudeConnector → PerplexityConnector
- Update GetName() metadata
- Update model family handling

**2.4 Update `pkg/connector/client.go`**

- Replace claudeapi.Client with perplexityapi.Client
- Remove sidecar code paths
- Update message handling for OpenAI format

### Phase 3: Clean Up

**3.1 Delete old packages**
```bash
rm -rf pkg/claudeapi/
rm -rf pkg/sidecar/
rm -rf sidecar/
```

**3.2 Rename cmd directory**
```bash
mv cmd/mautrix-claude cmd/mautrix-perplexity
```

**3.3 Update go.mod**
- Remove anthropic-sdk-go
- Run `go mod tidy`

### Phase 4: Update Configuration & Documentation

**4.1 Update `example-config.yaml`**
```yaml
network:
  default_model: sonar
  max_tokens: 4096
  # ... rest of config
```

**4.2 Update README.md**

**4.3 Update Dockerfile if exists**

### Phase 5: Testing

**5.1 Unit Tests**
- Test perplexityapi client
- Test config validation
- Test model resolution

**5.2 Integration Tests**
- Test full message flow
- Test streaming
- Test error handling

**5.3 Manual Testing**
- Build and run
- Login with Perplexity API key
- Send messages
- Verify responses

## Success Criteria

- [ ] Bridge starts without errors
- [ ] Login with Perplexity API key works
- [ ] Messages are sent and responses received
- [ ] Streaming works correctly
- [ ] Model switching works
- [ ] Ghost users appear with correct names
- [ ] All existing tests pass (with updates)
- [ ] No Claude-specific code remains

## Migration Notes

### For Existing Users

If migrating from mautrix-claude:
1. Get a Perplexity API key from https://www.perplexity.ai/settings/api
2. Re-login with the new API key
3. Update config file (model names, command prefix)

### Breaking Changes

- Model families change: `sonnet/opus/haiku` → `sonar/sonar-pro/sonar-reasoning/sonar-reasoning-pro`
- API key format changes: `sk-ant-*` → `pplx-*`
- Command prefix suggested change: `!claude` → `!perplexity`
- Sidecar no longer needed

## Sources

- [Perplexity API Documentation](https://docs.perplexity.ai/)
- [Perplexity Pricing](https://docs.perplexity.ai/getting-started/pricing)
- [OpenAI Compatibility Guide](https://docs.perplexity.ai/guides/chat-completions-guide)
- [Perplexity Python SDK](https://github.com/perplexityai/perplexity-py)
