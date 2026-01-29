# mautrix-claude: Matrix bridge for Claude AI
#
# Supports both API mode and sidecar mode (Pro/Max subscription).
# Mode is controlled by config.yaml: network.sidecar.enabled
#
# For sidecar mode, copy Claude Code credentials to ./data/.claude/:
#   cp -r ~/.claude/* ./data/.claude/
#   docker run -v ./data:/data mautrix-claude

# ============== Stage 1: Build Go binary ==============
# Use Debian-based image to match runtime libc (glibc)
FROM golang:1.24-bookworm AS builder

RUN apt-get update && apt-get install -y --no-install-recommends \
    git ca-certificates build-essential libsqlite3-dev \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG COMMIT_HASH
ARG BUILD_TIME
ARG VERSION=0.1.0

RUN CGO_ENABLED=1 go build -tags "goolm" -o /usr/bin/mautrix-claude \
    -ldflags "-s -w \
        -X main.Tag=${VERSION} \
        -X main.Commit=${COMMIT_HASH:-$(git rev-parse HEAD 2>/dev/null || echo unknown)} \
        -X 'main.BuildTime=${BUILD_TIME:-$(date -Iseconds)}'" \
    ./cmd/mautrix-claude

# ============== Stage 2: Get Node.js binary ==============
FROM node:20-slim AS node

# ============== Stage 3: Final image ==============
# Use minimal Debian base with Python
FROM debian:bookworm-slim

ENV UID=1337 \
    GID=1337 \
    CLAUDE_CONFIG_DIR=/data/.claude

# Install system dependencies (Python for sidecar, minimal runtime)
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    bash \
    curl \
    gosu \
    libsqlite3-0 \
    python3 \
    python3-pip \
    python3-venv \
    && rm -rf /var/lib/apt/lists/* \
    && ln -s /usr/bin/python3 /usr/bin/python

# Copy Node.js from node stage (needed for Claude Agent SDK CLI)
COPY --from=node /usr/local/bin/node /usr/local/bin/node
COPY --from=node /usr/local/lib/node_modules /usr/local/lib/node_modules
RUN ln -s /usr/local/bin/node /usr/local/bin/nodejs

# Install yq for YAML processing
RUN curl -sL https://github.com/mikefarah/yq/releases/latest/download/yq_linux_amd64 \
    -o /usr/bin/yq && chmod +x /usr/bin/yq

# Create bridge user
RUN useradd -m -u 1337 bridge && \
    mkdir -p /data /data/.claude /app/sidecar && \
    chown -R bridge:bridge /data /app

WORKDIR /app

# Copy Go binary
COPY --from=builder /usr/bin/mautrix-claude /usr/bin/mautrix-claude

# Copy and install Python sidecar
COPY sidecar/requirements.txt /app/sidecar/
RUN pip install --no-cache-dir --break-system-packages -r /app/sidecar/requirements.txt

COPY sidecar/main.py /app/sidecar/

# Copy startup script
COPY docker-run.sh /docker-run.sh
RUN chmod +x /docker-run.sh

# Volume for data
VOLUME /data
WORKDIR /data

# Health check using mautrix built-in ready endpoint
HEALTHCHECK --interval=30s --timeout=10s --start-period=30s --retries=3 \
    CMD curl -sf http://localhost:29320/_matrix/mau/ready || exit 1

# Run startup script
CMD ["/docker-run.sh"]
