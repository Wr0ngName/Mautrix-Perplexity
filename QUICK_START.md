# Quick Start Guide: Converting to mautrix-claude

## What You Have Now
- A working Matrix bridge for Candy.ai
- ~3,000 lines of well-structured Go code
- mautrix framework integration (handles all Matrix complexity)
- Docker setup ready

## What You're Building
A Matrix bridge for Claude API that lets users chat with Claude AI models through Matrix rooms.

## Why This Is Easier Than You Think
1. **80% of code stays**: mautrix integration, project structure, config system
2. **20% to replace**: Just the API client layer (no more web scraping!)
3. **Actually simpler**: Claude API is cleaner than Candy.ai's WebSocket + HTML

## Three Documents to Read

### 1. ANALYSIS_SUMMARY.md (5 min read)
- What I found in the codebase
- Key differences between Candy.ai and Claude API
- Migration strategy overview
- **Read this first**

### 2. plan.md (20 min read)
- Complete implementation plan
- Phase-by-phase breakdown
- Code examples and structures
- All 9 phases detailed
- **Your main reference**

### 3. This file (you're reading it!)
- Quick start steps

## Getting Started (Right Now)

### Step 1: Verify You Have
- [ ] Go 1.23+ installed
- [ ] Claude API key (get from https://console.anthropic.com/settings/keys)
- [ ] Git repository access
- [ ] Text editor/IDE

### Step 2: Test Current Bridge (Optional)
```bash
cd /mnt/data/git/mautrix-claude
go build ./cmd/mautrix-candy
./mautrix-candy --help
```

### Step 3: Create Feature Branch
```bash
git checkout -b feature/claude-api-migration
```

### Step 4: Start with Phase 1 (API Client)
Follow plan.md Phase 1 to build the Claude API client:

1. Create directory:
   ```bash
   mkdir -p pkg/claudeapi
   ```

2. Create these files in order:
   - `pkg/claudeapi/types.go` (data structures)
   - `pkg/claudeapi/client.go` (HTTP client)
   - `pkg/claudeapi/streaming.go` (SSE support)
   - `pkg/claudeapi/conversations.go` (context management)
   - `pkg/claudeapi/errors.go` (error handling)
   - `pkg/claudeapi/models.go` (model definitions)

3. Test as you go:
   ```bash
   go test ./pkg/claudeapi/...
   ```

### Step 5: Follow the Plan
Open `/mnt/data/git/mautrix-claude/plan.md` and follow phases 2-9.

## Key Files to Understand First

### Before You Start Coding
1. Read: `pkg/connector/connector.go` (bridge interface)
2. Read: `pkg/connector/client.go` (message handling pattern)
3. Read: `pkg/candygo/client.go` (HTTP client pattern to replace)

### As Reference While Coding
- `example-config.yaml` - config structure
- `pkg/connector/login.go` - login flow pattern
- `pkg/candygo/types.go` - data structure examples

## Common Questions

### Q: Do I need to understand Matrix protocol?
**A:** No! The mautrix framework handles everything Matrix-related. You only need to understand the Claude API.

### Q: What about the database?
**A:** The mautrix framework handles it. You just define metadata structs (see plan.md Phase 2.1).

### Q: How do I test without breaking things?
**A:** Work in the feature branch. Test each phase independently. The old code stays until you're ready.

### Q: What if I get stuck?
**A:** 
1. Check plan.md for that specific component
2. Look at similar code in pkg/connector/ for patterns
3. Read Claude API docs: https://docs.anthropic.com/claude/reference/
4. Check mautrix docs: https://pkg.go.dev/maunium.net/go/mautrix

## Estimated Timeline

- **Day 1 (4-6 hours)**: Phase 1-2 (API client + connector updates)
- **Day 2 (3-4 hours)**: Phase 3-5 (commands, config, cleanup)
- **Day 3 (2-3 hours)**: Phase 6-9 (testing, docs, polish)

**Total: 2-3 days** for experienced Go developer

## Success Checkpoints

After each phase, you should be able to:

- **Phase 1**: Call Claude API and get responses
- **Phase 2**: Bridge compiles with new connector
- **Phase 3**: Commands work in Matrix
- **Phase 4**: Clean codebase, no old Candy.ai code
- **Phase 5**: Bridge runs with config file
- **Phase 6**: All tests pass
- **Phase 7**: Documentation complete
- **Phase 8**: Docker build works
- **Phase 9**: Released and usable

## Files You'll Create (New)
```
pkg/claudeapi/
  ├── types.go          # Data structures
  ├── client.go         # HTTP client
  ├── streaming.go      # SSE parsing
  ├── conversations.go  # Context management
  ├── errors.go         # Error handling
  └── models.go         # Model definitions

pkg/connector/
  ├── commands.go       # Bridge commands
  ├── conversation.go   # Conversation persistence
  └── ratelimit.go      # Rate limiting

docs/
  ├── setup.md          # Setup guide
  └── commands.md       # Command reference

README.md               # User documentation
```

## Files You'll Modify (Existing)
```
cmd/mautrix-claude/main.go        # Update names
pkg/connector/connector.go        # Update metadata, methods
pkg/connector/config.go           # New config schema
pkg/connector/login.go            # API key login
pkg/connector/client.go           # Claude API integration
pkg/connector/chatinfo.go         # Update chat info
pkg/connector/ghost.go            # Update ghost info
example-config.yaml               # Claude config
Dockerfile                        # Update binary name
docker-compose.yaml               # Update service name
go.mod                            # Update module name
```

## Files You'll Delete
```
pkg/candygo/turbostream.go        # No HTML parsing
pkg/candygo/csrf.go               # No CSRF tokens
pkg/candygo/websocket.go          # No WebSocket
(The rest of candygo/ gets replaced by claudeapi/)
```

## Your First Test

After completing Phase 1, test the API client:

```go
package main

import (
    "context"
    "fmt"
    "go.mau.fi/mautrix-claude/pkg/claudeapi"
    "github.com/rs/zerolog/log"
)

func main() {
    client := claudeapi.NewClient("your-api-key-here", log.Logger)
    
    req := &claudeapi.CreateMessageRequest{
        Model: claudeapi.ModelSonnet3_5,
        Messages: []claudeapi.Message{
            {Role: "user", Content: []claudeapi.Content{{Type: "text", Text: "Hello!"}}},
        },
        MaxTokens: 1024,
    }
    
    resp, err := client.CreateMessage(context.Background(), req)
    if err != nil {
        panic(err)
    }
    
    fmt.Println(resp.Content[0].Text)
}
```

## Important Reminders

1. **Read plan.md carefully** - it has all the details
2. **Test incrementally** - don't wait until the end
3. **Keep the old code** - work in a feature branch
4. **Document as you go** - add comments while coding
5. **Handle errors properly** - Claude API can fail, handle gracefully

## Need Help?

- **Claude API Docs**: https://docs.anthropic.com/claude/reference/
- **mautrix Docs**: https://pkg.go.dev/maunium.net/go/mautrix
- **Matrix Spec**: https://spec.matrix.org/ (probably won't need this)

## You're Ready!

1. Open `plan.md`
2. Start with Phase 1, Step 1
3. Build incrementally
4. Test frequently
5. Ship it!

Good luck! The plan is comprehensive, the foundation is solid, and Claude API is actually simpler than what you're replacing.
