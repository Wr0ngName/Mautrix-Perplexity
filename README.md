# mautrix-perplexity

A [Matrix](https://matrix.org) bridge for [Perplexity AI](https://perplexity.ai), built on the [mautrix](https://github.com/mautrix) bridgev2 framework.

Chat with Perplexity AI models directly from any Matrix client. Supports multiple models, web search options, conversation history, multi-user relay, and per-room configuration.

## Features

- **Multiple models** -- Sonar, Sonar Pro, Sonar Reasoning, Sonar Reasoning Pro
- **Web search** -- built-in internet search with domain filtering, recency, date ranges, academic mode, and location-aware results
- **Conversation mode** -- optional multi-turn conversations with persistent history across restarts
- **Per-room settings** -- model, system prompt, temperature, web search options, and mention-only mode configurable per room
- **Multi-user relay** -- all users in a room can talk to Perplexity through a single bridge login
- **Citations** -- automatic source attribution from Perplexity search results
- **Image understanding** -- send images for Perplexity to analyze (vision API)
- **Encryption** -- optional end-to-bridge encryption support
- **Prometheus metrics** -- request counts, token usage, session tracking

## Architecture

The bridge consists of two components running in a single container:

```
Matrix Client
    |
Homeserver
    | (appservice API)
Go Bridge (mautrix-perplexity, port 29321)
    | (HTTP, port 8090)
Python Sidecar (official Perplexity SDK)
    |
Perplexity API
```

The Python sidecar is mandatory because the official Perplexity SDK is Python-only. It starts automatically inside the container.

## Quick Start (Docker)

### 1. Clone and start

```bash
git clone https://dev.web.wr0ng.name/wrongname/mautrix-perplexity.git
cd mautrix-perplexity
docker compose up -d
```

On first run, the container generates a default config at `./data/config.yaml` and exits.

### 2. Configure

Edit `./data/config.yaml`:

```yaml
homeserver:
    address: https://matrix.example.com
    domain: example.com

bridge:
    permissions:
        "*": relay
        "example.com": user
        "@admin:example.com": admin
```

See [example-config.yaml](example-config.yaml) for the full configuration reference.

### 3. Generate registration

Restart the container:

```bash
docker compose up -d
```

It will generate `./data/registration.yaml` and exit. Register this file with your homeserver:

- **Synapse**: Add the path to `app_service_config_files` in `homeserver.yaml`
- **Dendrite**: Add to `app_service_api.config_files` in `dendrite.yaml`

Restart your homeserver after registering.

### 4. Start the bridge

```bash
docker compose up -d
```

The bridge is now running and listening for Matrix events.

### 5. Log in

1. Start a chat with `@perplexitybot:example.com` (or your configured bot username)
2. Send `login`
3. Enter your Perplexity API key (get one at [perplexity.ai/settings/api](https://perplexity.ai/settings/api))

### 6. Start chatting

**In the management room:**

Send any message and Perplexity will respond.

**In any other room:**

1. Send `!perplexity join` (or `!perplexity join sonar-pro` for a specific model)
2. The Perplexity ghost user joins the room
3. All users in the room can now chat with Perplexity

## Commands

All commands work in management rooms directly. In other rooms, prefix with `!perplexity` (configurable).

| Command | Description |
|---------|-------------|
| `help` | Show available commands |
| `login` | Authenticate with Perplexity API key |
| `logout` | Remove authentication |
| `join [model]` | Add Perplexity to the current room |
| `model [name]` | View or change the AI model |
| `models` | List available models |
| `system [prompt]` | View or set the system prompt (`system clear` to reset) |
| `temperature [0-2]` | View or set temperature (`temperature reset` for default) |
| `conversation [on\|off]` | Toggle multi-turn conversation mode |
| `mention [on\|off]` | Toggle mention-only mode (respond only when @mentioned) |
| `web [option] [value]` | Configure web search (domains, recency, dates, images, mode, location) |
| `clear` | Clear conversation history |
| `stats` | Show conversation statistics and token usage |

### Web search examples

```
web domains docs.python.org,stackoverflow.com
web recency week
web after 01/01/2025
web mode academic
web location Berlin,Berlin,Germany
web images on
web clear
```

## Configuration

Key settings in the `network` section of `config.yaml`:

```yaml
network:
    default_model: sonar          # sonar, sonar-pro, sonar-reasoning, sonar-reasoning-pro
    max_tokens: 4096
    temperature: 1.0              # 0.0 - 2.0
    system_prompt: "You are a helpful AI assistant."
    conversation_max_age_hours: 24
    rate_limit_per_minute: 60
    sidecar:
        url: "http://localhost:8090"
        timeout: 300
```

### Database

SQLite is used by default (`sqlite:mautrix-perplexity.db`). PostgreSQL is also supported:

```yaml
appservice:
    database: postgres://user:password@host/mautrix_perplexity?sslmode=disable
```

An optional PostgreSQL service is included (commented out) in `docker-compose.yaml`.

## Building from source

### Docker

```bash
docker compose build
```

### Native

Requires Go 1.24+ and Python 3.11+.

```bash
# Build the Go bridge
go build -tags goolm -o mautrix-perplexity ./cmd/mautrix-perplexity

# Install Python sidecar dependencies
pip install -r sidecar/requirements.txt

# Run the sidecar (in background)
python sidecar/main.py &

# Run the bridge
./mautrix-perplexity -c config.yaml
```
