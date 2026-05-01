# mautrix-perplexity: Matrix bridge for Perplexity AI
#
# Uses the official Perplexity Python SDK via sidecar architecture.
# The sidecar is mandatory for this bridge.

# ============== Stage 1: Build Go binary ==============
# Use Debian-based image to match runtime libc (glibc)
FROM golang:1.25-bookworm AS builder

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

RUN CGO_ENABLED=1 go build -tags "goolm" -o /usr/bin/mautrix-perplexity \
    -ldflags "-s -w \
        -X main.Tag=${VERSION} \
        -X main.Commit=${COMMIT_HASH:-$(git rev-parse HEAD 2>/dev/null || echo unknown)} \
        -X 'main.BuildTime=${BUILD_TIME:-$(date -Iseconds)}'" \
    ./cmd/mautrix-perplexity

# ============== Stage 2: Final image ==============
# Use minimal Debian base with Python
FROM debian:bookworm-slim

ENV UID=1337 \
    GID=1337

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

# Install yq for YAML processing
ARG TARGETARCH
RUN curl -sL "https://github.com/mikefarah/yq/releases/latest/download/yq_linux_${TARGETARCH}" \
    -o /usr/bin/yq && chmod +x /usr/bin/yq

# Create bridge user
RUN useradd -m -u 1337 bridge && \
    mkdir -p /data /app/sidecar && \
    chown -R bridge:bridge /data /app

WORKDIR /app

# Copy Go binary
COPY --from=builder /usr/bin/mautrix-perplexity /usr/bin/mautrix-perplexity

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
    CMD curl -sf http://localhost:29321/_matrix/mau/ready || exit 1

# Run startup script
CMD ["/docker-run.sh"]
