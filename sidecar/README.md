# Claude Agent SDK Sidecar

Internal Python service that provides Claude AI capabilities using the official Agent SDK.

## Overview

This sidecar runs inside the mautrix-claude container and enables Pro/Max subscription support via the Claude Agent SDK instead of API credits.

**Note**: This is an internal component. Users don't need to configure or run it separately - it's automatically started when `ENABLE_SIDECAR=true` is set.

## How It Works

```
┌─────────────────────────────────────────────────────────────┐
│                  mautrix-claude Container                    │
├─────────────────────────────────────────────────────────────┤
│  ┌─────────────────┐         ┌─────────────────────────┐   │
│  │  Go Bridge      │  HTTP   │   Python Sidecar        │   │
│  │  (mautrix-      │◄───────►│   (Agent SDK)           │   │
│  │   claude)       │ :8090   │                         │   │
│  └────────┬────────┘         └───────────┬─────────────┘   │
│           │                              │                  │
│           │ Matrix                       │ Claude API       │
│           ▼                              ▼ (via Pro/Max)    │
│     Homeserver                      Anthropic               │
└─────────────────────────────────────────────────────────────┘
```

## Features

- **Per-room sessions**: Each Matrix room has isolated conversation context
- **Tool restrictions**: Only safe tools enabled (WebSearch, WebFetch, AskUserQuestion)
- **Health checks**: Prometheus metrics at `/metrics`
- **Graceful shutdown**: Proper cleanup of sessions

## Configuration

Environment variables (set when running the container):

| Variable | Default | Description |
|----------|---------|-------------|
| `ENABLE_SIDECAR` | `false` | Enable sidecar mode |
| `CLAUDE_SIDECAR_ALLOWED_TOOLS` | `WebSearch,WebFetch,AskUserQuestion` | Allowed tools |
| `CLAUDE_SIDECAR_SYSTEM_PROMPT` | `You are a helpful AI assistant.` | Default prompt |
| `CLAUDE_SIDECAR_MODEL` | `sonnet` | Model to use |
| `CLAUDE_SIDECAR_SESSION_TIMEOUT` | `3600` | Session timeout (seconds) |

## Security: Allowed Tools

Only safe tools are enabled by default:
- `WebSearch` - Search the web
- `WebFetch` - Fetch web pages
- `AskUserQuestion` - Ask clarifying questions

**Explicitly blocked** (hardcoded, cannot be enabled):
- Read, Write, Edit, MultiEdit
- Bash, Glob, Grep, LS
- Task, TodoWrite, TodoRead
- NotebookEdit

## Setup: Claude Code Authentication

Before using sidecar mode, you must authenticate Claude Code on the host machine:

### Step 1: Install Claude Code CLI

```bash
npm install -g @anthropic-ai/claude-code
```

### Step 2: Authenticate

Run the Claude Code CLI and complete the authentication:

```bash
claude
```

This will:
1. Open a browser for Anthropic authentication
2. Link your Pro/Max subscription
3. Save credentials to `~/.claude/`

### Step 3: Verify Authentication

```bash
echo "Hello" | claude -p "Say hi"
```

If this works, authentication is set up correctly.

## Usage

### Docker Run

```bash
# Copy Claude Code credentials to data directory
cp -r ~/.claude/* ./data/.claude/

# Run the bridge
docker run -v ./data:/data mautrix-claude
```

The credentials are read from `/data/.claude/` inside the container (via `CLAUDE_CONFIG_DIR` env var).

### Docker Compose

```yaml
services:
  mautrix-claude:
    image: mautrix-claude
    volumes:
      - ./data:/data
    restart: unless-stopped
```

Copy credentials before starting: `cp -r ~/.claude/* ./data/.claude/`

### Configuration

Enable sidecar mode in your `config.yaml`:

```yaml
claude:
  sidecar:
    enabled: true
```

## Troubleshooting

### "Claude Code is not authenticated"

If you see this error on startup:
1. Verify `~/.claude` exists and contains credentials
2. Re-run `claude` on the host to re-authenticate
3. Ensure the volume mount is correct (`:ro` for read-only)

### "Circuit breaker open"

The sidecar has detected repeated failures:
1. Check Claude Code authentication
2. Verify Anthropic API status
3. Wait 30 seconds for circuit to reset

### "Sidecar health check failed"

The Go bridge cannot reach the sidecar:
1. Check container logs for Python errors
2. Check for port conflicts on 8090
3. Verify the sidecar URL in config (default: http://localhost:8090)

## API Endpoints (Internal)

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/health` | GET | Health check |
| `/metrics` | GET | Prometheus metrics |
| `/v1/chat` | POST | Send message, get response |
| `/v1/sessions/{id}` | DELETE | Clear session |
