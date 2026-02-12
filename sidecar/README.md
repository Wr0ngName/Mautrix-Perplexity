# Perplexity SDK Sidecar

Internal Python service that provides Perplexity AI capabilities using the official Python SDK.

## Overview

This sidecar runs inside the mautrix-perplexity container and handles all communication with the Perplexity API. It is **mandatory** for the bridge to function since the official Perplexity SDK is Python-only.

**Note**: This is an internal component. Users don't need to configure or run it separately -- it's automatically started by the container's entrypoint script.

## How It Works

```
┌─────────────────────────────────────────────────────────────┐
│              mautrix-perplexity Container                    │
├─────────────────────────────────────────────────────────────┤
│  ┌─────────────────┐         ┌─────────────────────────┐   │
│  │  Go Bridge      │  HTTP   │   Python Sidecar        │   │
│  │  (mautrix-      │◄───────►│   (Perplexity SDK)      │   │
│  │   perplexity)   │ :8090   │                         │   │
│  └────────┬────────┘         └───────────┬─────────────┘   │
│           │                              │                  │
│           │ Matrix                       │ Perplexity API   │
│           ▼                              ▼                  │
│     Homeserver                    cloud.perplexity.ai       │
└─────────────────────────────────────────────────────────────┘
```

## Features

- **Per-room sessions**: Each Matrix room has isolated conversation context
- **Session persistence**: Sessions survive bridge restarts via session IDs
- **Token tracking**: Prometheus metrics for input/output token usage
- **Health checks**: `/health` endpoint for container health monitoring
- **Prometheus metrics**: Full metrics at `/metrics`

## Configuration

Environment variables (set in the container):

| Variable | Default | Description |
|----------|---------|-------------|
| `PERPLEXITY_SIDECAR_PORT` | `8090` | Port to listen on |
| `PERPLEXITY_SIDECAR_QUERY_TIMEOUT` | `300` | Query timeout in seconds |
| `PERPLEXITY_SIDECAR_MODEL` | `sonar` | Default model |
| `PERPLEXITY_SIDECAR_SYSTEM_PROMPT` | `You are a helpful AI assistant.` | Default system prompt |
| `PERPLEXITY_SIDECAR_SESSION_TIMEOUT` | `3600` | Session timeout in seconds |

## API Endpoints (Internal)

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/health` | GET | Health check |
| `/metrics` | GET | Prometheus metrics |
| `/v1/chat` | POST | Send message, get response |
| `/v1/sessions/{id}` | DELETE | Clear session |
| `/v1/sessions/{id}/stats` | GET | Session statistics |
