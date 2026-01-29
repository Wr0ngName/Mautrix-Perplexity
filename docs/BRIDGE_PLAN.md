# mautrix-candy Bridge Implementation Plan

## Overview

A Matrix-Candy.ai puppeting bridge based on mautrix/go framework, following the architecture pattern established by mautrix-linkedin.

## Architecture

```
┌─────────────────────────────────────────────────────────────┐
│                      mautrix-candy                          │
├─────────────────────────────────────────────────────────────┤
│  pkg/connector/          │  pkg/candygo/                    │
│  ├── connector.go        │  ├── client.go      (HTTP)      │
│  ├── client.go           │  ├── auth.go        (Login)     │
│  ├── login.go            │  ├── websocket.go   (ActionCable)│
│  ├── handlecandy.go      │  ├── messages.go    (Send/Recv) │
│  ├── handlematrix.go     │  ├── conversations.go           │
│  ├── tomatrix.go         │  ├── turbostream.go (HTML Parse)│
│  ├── chatsync.go         │  ├── types.go       (Structs)   │
│  └── config.go           │  └── csrf.go        (Token Mgmt)│
└─────────────────────────────────────────────────────────────┘
                              │
                    Uses mautrix/go framework
                              │
                    ┌─────────┴─────────┐
                    │   Matrix Server   │
                    └───────────────────┘
```

## Implementation Phases

### Phase 1: Protocol Client (`pkg/candygo/`)

#### 1.1 HTTP Client & Auth (`client.go`, `auth.go`)

```go
type Client struct {
    HTTP        *http.Client
    BaseURL     string
    Session     *Session
    CSRFToken   string
    UserID      int64
    EventHandler func(Event)
}

type Session struct {
    Cookie      string  // _chat_chat_session
    CSRFToken   string
    UserGID     string  // gid://candy-ai/User/<id>
}

func (c *Client) Login(email, password string) error
func (c *Client) RefreshCSRF() error
```

#### 1.2 ActionCable WebSocket (`websocket.go`)

```go
type ActionCableClient struct {
    conn        *websocket.Conn
    client      *Client
    channels    map[string]*Channel
    msgHandler  func(ChannelMessage)
}

type Channel struct {
    Identifier       string
    SignedStreamName string
    Subscribed       bool
}

func (ac *ActionCableClient) Connect() error
func (ac *ActionCableClient) Subscribe(signedName string) error
func (ac *ActionCableClient) Unsubscribe(signedName string) error
func (ac *ActionCableClient) handleMessage(msg []byte)
```

#### 1.3 Turbo Stream Parser (`turbostream.go`)

```go
type TurboStreamAction struct {
    Action   string // append, prepend, replace, remove
    Target   string
    Template string // HTML content
}

type ParsedMessage struct {
    ID        int64
    Body      string
    Timestamp time.Time
    IsUser    bool
    ProfileID int64
}

func ParseTurboStream(html string) ([]TurboStreamAction, error)
func ExtractMessageFromHTML(html string) (*ParsedMessage, error)
func ExtractSignedStreamNames(pageHTML string) (map[string]string, error)
```

#### 1.4 Messages API (`messages.go`)

```go
type SendMessageRequest struct {
    ProfileID       int64
    Body            string
    ImageGenToggle  bool
    NumImages       int
}

func (c *Client) SendMessage(req *SendMessageRequest) error
func (c *Client) LoadMessages(convID int64, beforeMsgID int64) ([]ParsedMessage, error)
```

#### 1.5 Conversations (`conversations.go`)

```go
type Conversation struct {
    ID          int64
    ProfileID   int64
    ProfileSlug string
    ProfileName string
    LastMessage string
    UpdatedAt   time.Time
}

func (c *Client) GetConversations() ([]Conversation, error)
func (c *Client) GetConversationBySlug(slug string) (*Conversation, error)
```

### Phase 2: Bridge Connector (`pkg/connector/`)

#### 2.1 Configuration (`config.go`)

```yaml
bridge:
    username_template: "candy_{{.ProfileSlug}}"
    displayname_template: "{{.ProfileName}} (Candy.ai)"

candy:
    base_url: "https://candy.ai"
    user_agent: "Mozilla/5.0 ..."
```

#### 2.2 Login Flow (`login.go`)

Using mautrix bridge login framework:
1. User sends `login` command to bridge bot
2. Bot prompts for email/password
3. Bridge authenticates with candy.ai
4. Stores encrypted credentials in database
5. Establishes WebSocket connection

#### 2.3 Event Handlers

**Candy → Matrix (`handlecandy.go`, `tomatrix.go`):**
```go
func (c *Connector) HandleCandyMessage(msg *candygo.ParsedMessage) {
    // 1. Find/create Matrix room for conversation
    // 2. Convert message to Matrix format
    // 3. Send to Matrix room via intent
}
```

**Matrix → Candy (`handlematrix.go`):**
```go
func (c *Connector) HandleMatrixMessage(evt *event.Event) {
    // 1. Extract profile ID from room
    // 2. Convert Matrix message to Candy format
    // 3. Send via candygo client
}
```

#### 2.4 Chat Sync (`chatsync.go`)

```go
func (c *Connector) SyncConversations() error {
    // 1. Fetch all conversations from candy.ai
    // 2. Create/update Matrix rooms for each
    // 3. Optionally backfill message history
}
```

### Phase 3: Main Application (`cmd/mautrix-candy/`)

```go
func main() {
    br := bridge.New()
    br.Name = "mautrix-candy"
    br.Connector = connector.NewConnector()
    br.Run()
}
```

## Key Technical Challenges

### 1. HTML Parsing

Candy.ai uses Turbo Streams (HTML fragments) instead of JSON. Need robust HTML parsing:

```go
// Using goquery or similar
doc, _ := goquery.NewDocumentFromReader(strings.NewReader(html))
doc.Find("div.user-response").Each(func(i int, s *goquery.Selection) {
    msgID, _ := s.Attr("id")
    body := s.Find(".message-body").Text()
    // ...
})
```

### 2. Signed Stream Names

The WebSocket channel identifiers are signed and can't be forged. Must extract from page HTML:

```go
// Extract from script tags or data attributes
re := regexp.MustCompile(`signed_stream_name":"([^"]+)"`)
matches := re.FindAllStringSubmatch(pageHTML, -1)
```

### 3. CSRF Token Management

Rails CSRF tokens rotate. Need to:
1. Extract from page meta tags on initial load
2. Refresh periodically or on 422 errors
3. Include in all POST requests

### 4. Session Keep-Alive

- Respond to WebSocket pings
- Periodically refresh page to keep session active
- Handle session expiry and re-login

### 5. Rate Limiting

Candy.ai likely has rate limits. Implement:
- Request throttling
- Exponential backoff on errors
- Queue for outgoing messages

## Room Mapping

| Candy.ai Concept | Matrix Equivalent |
|-----------------|-------------------|
| Conversation | Matrix Room |
| AI Character | Ghost user (@candy_<slug>:server) |
| User | Matrix user (puppeted) |
| Message | Matrix message |

## Database Schema (additions to mautrix standard)

```sql
-- Candy conversations
CREATE TABLE candy_conversation (
    user_mxid TEXT NOT NULL,
    conversation_id BIGINT NOT NULL,
    profile_id BIGINT NOT NULL,
    profile_slug TEXT NOT NULL,
    room_id TEXT,
    last_message_id BIGINT,
    PRIMARY KEY (user_mxid, conversation_id)
);

-- Session storage
CREATE TABLE candy_session (
    user_mxid TEXT PRIMARY KEY,
    email TEXT NOT NULL,
    session_cookie TEXT NOT NULL,
    csrf_token TEXT,
    user_id BIGINT,
    last_sync TIMESTAMP
);
```

## Testing Strategy

1. **Unit tests** for HTML parsing (with captured samples)
2. **Integration tests** with mock HTTP server
3. **End-to-end tests** with real candy.ai (manual/CI)

## File Structure

```
mautrix-candy/
├── cmd/
│   └── mautrix-candy/
│       └── main.go
├── pkg/
│   ├── candygo/
│   │   ├── client.go
│   │   ├── auth.go
│   │   ├── websocket.go
│   │   ├── messages.go
│   │   ├── conversations.go
│   │   ├── turbostream.go
│   │   ├── turbostream_test.go
│   │   ├── csrf.go
│   │   └── types.go
│   └── connector/
│       ├── connector.go
│       ├── client.go
│       ├── login.go
│       ├── handlecandy.go
│       ├── handlematrix.go
│       ├── tomatrix.go
│       ├── chatsync.go
│       ├── config.go
│       └── ids.go
├── capture/
│   ├── candy_capture.py
│   ├── analyze_capture.py
│   └── README.md
├── docs/
│   ├── PROTOCOL.md
│   └── BRIDGE_PLAN.md
├── go.mod
├── go.sum
├── Dockerfile
└── example-config.yaml
```

## Dependencies

```go
require (
    maunium.net/go/mautrix v0.x.x
    github.com/gorilla/websocket v1.x.x
    github.com/PuerkitoBio/goquery v1.x.x
    golang.org/x/net v0.x.x
)
```

## Next Steps

1. [ ] Initialize Go module and import mautrix/go
2. [ ] Implement `pkg/candygo/client.go` - basic HTTP client
3. [ ] Implement `pkg/candygo/auth.go` - login flow
4. [ ] Implement `pkg/candygo/turbostream.go` - HTML parsing
5. [ ] Implement `pkg/candygo/websocket.go` - ActionCable client
6. [ ] Implement `pkg/candygo/messages.go` - message operations
7. [ ] Implement `pkg/connector/` - bridge layer
8. [ ] Create example config and Dockerfile
9. [ ] Test with real account
10. [ ] Documentation and polish
