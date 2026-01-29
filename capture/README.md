# Candy.ai Protocol Capture

Tools for reverse-engineering the candy.ai chat protocol using mitmproxy.

## Quick Start

### 1. Start mitmproxy with the capture addon

```bash
# Web UI (recommended - shows traffic in browser)
mitmweb -s candy_capture.py --set candy_output=./candy_captured.jsonl

# Or terminal UI
mitmproxy -s candy_capture.py --set candy_output=./candy_captured.jsonl

# Or headless (just capture to file)
mitmdump -s candy_capture.py --set candy_output=./candy_captured.jsonl
```

This starts a proxy on `localhost:8080` (default).

### 2. Configure your browser

**Option A: Firefox (easiest)**
1. Settings → Network Settings → Manual proxy configuration
2. HTTP Proxy: `127.0.0.1`, Port: `8080`
3. Check "Also use this proxy for HTTPS"

**Option B: Chrome with proxy flag**
```bash
google-chrome --proxy-server="http://127.0.0.1:8080"
```

**Option C: System-wide (affects all apps)**
```bash
export HTTP_PROXY=http://127.0.0.1:8080
export HTTPS_PROXY=http://127.0.0.1:8080
```

### 3. Install mitmproxy CA certificate

This is required to intercept HTTPS traffic.

1. With proxy running, visit: http://mitm.it
2. Download the certificate for your OS/browser
3. Install it as a trusted CA

**Firefox:** Settings → Privacy & Security → Certificates → View Certificates → Import

**Chrome/System:**
```bash
# Download cert
curl -o mitmproxy-ca.pem http://127.0.0.1:8080/cert/pem

# Linux: Add to system trust store
sudo cp mitmproxy-ca.pem /usr/local/share/ca-certificates/mitmproxy.crt
sudo update-ca-certificates
```

### 4. Browse candy.ai

1. Go to candy.ai and log in
2. Navigate conversations, send messages, use features
3. All traffic is captured to the JSONL file

### 5. Analyze captured traffic

```bash
python analyze_capture.py candy_captured.jsonl
```

This outputs:
- All API endpoints discovered
- Request/response examples
- WebSocket message patterns
- Authentication headers and cookies
- A summary JSON for bridge development

## What to Capture

For building the bridge, try to capture:

- [ ] Login/authentication flow
- [ ] Loading conversation list
- [ ] Opening a conversation
- [ ] Sending a text message
- [ ] Receiving AI response (watch for streaming/SSE)
- [ ] Any media upload/download
- [ ] WebSocket connection establishment
- [ ] Session refresh/keep-alive

## Output Files

- `candy_captured.jsonl` - Raw captured traffic (one JSON per line)
- `candy_captured.summary.json` - Analyzed summary for development

## Tips

- Keep the capture session focused (don't browse other sites)
- Note timestamps when you perform specific actions
- The WebSocket messages are the most important for real-time chat
- Look for patterns in authentication headers (JWT tokens, session cookies)
