# Codebase Analysis Summary: mautrix-candy → mautrix-claude

## What I Found

### Current State
This repository contains **mautrix-candy**, a fully functional Matrix bridge for Candy.ai (an AI character chat platform). The codebase is well-structured with approximately **3,000 lines of Go code**.

### Architecture Overview

```
mautrix-candy/
├── cmd/mautrix-candy/main.go          # Entry point (28 lines)
├── pkg/
│   ├── candygo/                       # API client layer (~1,500 lines)
│   │   ├── client.go                  # HTTP client with session management
│   │   ├── auth.go                    # Login/authentication (email/password, cookies)
│   │   ├── websocket.go               # ActionCable WebSocket for real-time events
│   │   ├── messages.go                # Message sending/receiving
│   │   ├── conversations.go           # Conversation management
│   │   ├── turbostream.go             # HTML/Turbo Stream parsing
│   │   ├── csrf.go                    # CSRF token management
│   │   └── types.go                   # Data structures
│   └── connector/                     # Bridge connector layer (~1,400 lines)
│       ├── connector.go               # Bridge interface implementation
│       ├── config.go                  # Configuration
│       ├── login.go                   # Login flows (password, cookie)
│       ├── client.go                  # Network API implementation
│       ├── chatinfo.go                # Chat/portal management
│       └── ghost.go                   # Ghost user (AI character) handling
├── example-config.yaml                # Configuration template
├── Dockerfile                         # Docker build (multi-stage)
└── docker-compose.yaml                # Docker Compose setup
```

### Key Technologies Used
- **Framework**: mautrix bridgev2 (official Matrix bridge framework)
- **Language**: Go 1.23
- **Database**: SQLite or PostgreSQL (via mautrix)
- **Protocol**: Matrix (via mautrix), ActionCable WebSocket (Candy.ai)
- **Authentication**: Session cookies, CSRF tokens
- **Message Format**: HTML parsing (Turbo Streams)

### What the Current Bridge Does
1. Users log in with Candy.ai credentials (email/password or session cookie)
2. Bridge syncs conversations with AI characters
3. Each conversation becomes a Matrix DM room
4. Messages are relayed bidirectionally:
   - Matrix → Candy.ai via HTTP POST
   - Candy.ai → Matrix via WebSocket push
5. Real-time updates via ActionCable WebSocket
6. Supports backfilling message history

### Candy.ai-Specific Complexity
- **HTML Parsing**: Turbo Stream responses (server-rendered HTML partials)
- **CSRF Protection**: Must extract and include CSRF tokens
- **WebSocket**: ActionCable protocol with signed stream names
- **Session Management**: Cookie-based authentication
- **Complex Auth Flow**: Multi-step login with CSRF token extraction

## Key Differences: Candy.ai vs Claude API

| Aspect | Candy.ai (Current) | Claude API (Target) |
|--------|-------------------|---------------------|
| **Protocol** | HTTP + WebSocket (ActionCable) | REST + SSE (Server-Sent Events) |
| **Authentication** | Email/password → session cookie | API key (x-api-key header) |
| **Message Format** | HTML (Turbo Streams) | JSON |
| **Real-time** | WebSocket push | SSE streaming (optional) |
| **Conversations** | Server-side (conversation IDs) | Client-side (send full history) |
| **CSRF Protection** | Yes (tokens in HTML) | No (API key auth) |
| **Parsing** | HTML parsing (goquery) | JSON parsing (standard library) |
| **Complexity** | High (web scraping-like) | Low (official API) |

## Migration Strategy

### What Can Be Reused (80%)
- ✅ **mautrix framework integration**: All bridgev2 interfaces and patterns
- ✅ **Project structure**: cmd/pkg layout
- ✅ **Connector layer**: Basic structure, metadata patterns
- ✅ **Database schema**: Via mautrix (Ghost, Portal, Message tables)
- ✅ **Configuration system**: YAML config, validation
- ✅ **Build system**: Dockerfile, docker-compose, CI/CD
- ✅ **Logging**: zerolog integration
- ✅ **Matrix integration**: Room management, message bridging

### What Must Be Replaced (20%)
- ❌ **API client layer** (`pkg/candygo/` → `pkg/claudeapi/`):
  - Remove: WebSocket, HTML parsing, CSRF, cookies
  - Add: REST API client, SSE streaming, conversation context management
- ❌ **Authentication** (`login.go`):
  - Remove: Password/cookie login
  - Add: API key validation
- ❌ **Message handling** (`client.go`):
  - Remove: WebSocket event handlers
  - Add: REST API calls with streaming
- ❌ **Conversation management**:
  - Remove: Server-side conversation tracking
  - Add: Client-side context window management

### Simplifications
1. **No WebSocket**: Claude API uses REST (simpler state management)
2. **No HTML parsing**: Pure JSON (no goquery dependency)
3. **No CSRF tokens**: API key authentication
4. **No session cookies**: Stateless API
5. **Official API**: Well-documented, stable (vs web scraping)

## Implementation Approach

### Phase 1: Build Claude API Client (NEW CODE)
Create `pkg/claudeapi/` from scratch:
- REST client with API key auth
- SSE streaming parser
- Conversation context manager
- Error handling

**Estimated**: 500-700 lines

### Phase 2: Adapt Connector Layer (MODIFY EXISTING)
Update `pkg/connector/` files:
- Replace WebSocket handlers with REST API calls
- Update metadata structures for Claude
- Simplify login to API key only
- Add conversation context persistence

**Estimated**: Modify 800 lines, add 200 lines

### Phase 3: Update Configuration (MODIFY EXISTING)
- Update config schema for Claude settings
- Update example config
- Update Docker files

**Estimated**: Modify 200 lines

### Phase 4: Cleanup (DELETE CODE)
- Remove unused WebSocket code
- Remove HTML parsing code
- Remove CSRF handling
- Remove goquery dependency

**Estimated**: Delete 600 lines

### Final Result
- **New codebase**: ~2,100 lines (30% reduction)
- **Simpler architecture**: No WebSocket, no HTML parsing
- **Same functionality**: Multi-room conversations with AI

## Why This Migration Is Feasible

### Advantages
1. **Simpler target**: Claude API is cleaner than web scraping Candy.ai
2. **Good foundation**: mautrix framework handles all Matrix complexity
3. **Similar patterns**: Both are AI chat bridges
4. **Well-defined API**: Official Claude API documentation
5. **No frontend needed**: Matrix clients handle UI

### Challenges
1. **Conversation context**: Must manage full history client-side
2. **Token limits**: Must implement context window trimming
3. **SSE streaming**: Need to parse Server-Sent Events
4. **Rate limiting**: Must respect API limits
5. **Cost tracking**: API usage has cost implications

### Risk Assessment
- **Technical Risk**: LOW (well-documented API, simpler than current)
- **Complexity Risk**: MEDIUM (conversation context management)
- **Dependency Risk**: LOW (minimal new dependencies)
- **Time Risk**: LOW (2-3 days for experienced dev)

## Recommended Next Steps

1. **Review the plan**: Read `/mnt/data/git/mautrix-claude/plan.md`
2. **Verify Claude API access**: Get a test API key
3. **Create feature branch**: `git checkout -b feature/claude-api-migration`
4. **Start Phase 1**: Build the Claude API client
5. **Iterative testing**: Test each phase with real API
6. **Document as you go**: Update README, add comments

## Files to Review

### Critical Files (understand first)
- `/mnt/data/git/mautrix-claude/pkg/connector/connector.go` - Bridge interface
- `/mnt/data/git/mautrix-claude/pkg/connector/client.go` - Message handling
- `/mnt/data/git/mautrix-claude/pkg/candygo/client.go` - API client pattern

### Reference Files (for patterns)
- `/mnt/data/git/mautrix-claude/pkg/connector/login.go` - Login flow structure
- `/mnt/data/git/mautrix-claude/pkg/candygo/types.go` - Data structure examples
- `/mnt/data/git/mautrix-claude/example-config.yaml` - Configuration format

## Conclusion

The existing mautrix-candy bridge is a **solid foundation** for building mautrix-claude. Approximately **80% of the code can be reused or easily adapted**, with only the API client layer needing complete replacement. The migration will actually **simplify the codebase** since Claude's official API is cleaner than web scraping Candy.ai.

The detailed implementation plan is available in `/mnt/data/git/mautrix-claude/plan.md`.
