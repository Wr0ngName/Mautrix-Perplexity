# Candy.ai Protocol Documentation

Analysis based on captured network traffic from mitmproxy session.

## Tech Stack

- **Backend**: Ruby on Rails
- **Real-time**: ActionCable (WebSocket) + Turbo Streams (Hotwire)
- **Auth**: Devise (cookie-based sessions)
- **CSRF**: Rails authenticity tokens required

## Authentication

### Login Flow

```
POST /users/sign_in
Content-Type: application/x-www-form-urlencoded

authenticity_token=<csrf_token>
user[email]=<email>
user[password]=<password>
```

**Required Headers:**
```
X-CSRF-Token: <csrf_token>
X-Turbo-Request-Id: <uuid>
Turbo-Frame: sign-in-modal
```

**Response:** Sets `_chat_chat_session` cookie (Rails encrypted session)

### Session Management

- Session cookie: `_chat_chat_session` (encrypted, HttpOnly)
- CSRF tokens obtained from HTML meta tags: `<meta name="csrf-token" content="...">`
- Tokens rotate - must be refreshed from page loads

## Identifiers

Rails Global IDs (base64-encoded):
- User: `gid://candy-ai/User/<user_id>` → e.g., `28074991`
- Conversation: `gid://candy-ai/Conversation/<conv_id>` → e.g., `65367055`
- Profile (AI character): `<profile_id>` → e.g., `305669999`

## WebSocket Protocol

### Connection

```
WSS: wss://candy.ai/cable
Protocol: actioncable-v1-json
```

**Required Cookie:** `_chat_chat_session`

### ActionCable Message Format

```json
// Server welcome
{"type": "welcome"}

// Subscribe to channel
{
  "command": "subscribe",
  "identifier": "{\"channel\":\"Turbo::StreamsChannel\",\"signed_stream_name\":\"<signed_name>\"}"
}

// Subscription confirmed
{
  "identifier": "<identifier>",
  "type": "confirm_subscription"
}

// Keep-alive ping (every 3 seconds)
{"type": "ping", "message": <timestamp>}

// Actual message data
{
  "identifier": "<identifier>",
  "message": "<turbo_stream_html>"
}
```

### Channel Types (signed_stream_name decoded)

| Channel Suffix | Purpose |
|---------------|---------|
| `:message_stream` | Real-time AI responses (main chat) |
| `:conversation_stream` | Conversation metadata updates |
| `:token_balance` | Token balance changes |
| `:notification` | General notifications |
| `:voice_button` | Voice feature state |
| `:phone_call_feedback` | Call feedback |
| `:pfp_banner` | Profile picture updates |

### Signed Stream Name Format

```
Base64("<gid>:<channel_suffix>")--<hmac_signature>
```

Example:
```
IloybGtPaTh2WTJGdVpIa3RZV2t2UTI5dWRtVnljMkYwYVc5dUx6WTFNelkzTURVMTptZXNzYWdlX3N0cmVhbSI=
→ "gid://candy-ai/Conversation/65367055:message_stream"
```

The signature is HMAC-SHA256 with Rails secret key (not recoverable, must use from page).

## Sending Messages

### Endpoint

```
POST /messages
Accept: text/vnd.turbo-stream.html, text/html, application/xhtml+xml
Content-Type: multipart/form-data
X-CSRF-Token: <csrf_token>
X-Requested-With: XMLHttpRequest
X-Turbo-Request-Id: <uuid>
```

### Form Fields

| Field | Required | Description |
|-------|----------|-------------|
| `authenticity_token` | Yes | CSRF token |
| `message[profile_id]` | Yes | AI character profile ID |
| `message[body]` | Yes | Message text |
| `image_gen_toggle` | No | Image generation flag (0/1) |
| `gen_ai_suggestion[number_of_images]` | No | Number of images to generate |
| `gen_ai_suggestion[gen_ai_suggestion_id]` | No | Suggestion ID if using preset |
| `gen_ai_suggestion[gen_ai_prompt_id]` | No | Prompt ID if using preset |

### Response

Returns Turbo Stream HTML that updates the UI. AI response comes separately via WebSocket.

## Receiving Messages

AI responses arrive via WebSocket as Turbo Stream HTML:

```html
<turbo-stream action="append" target="messages-list">
  <template>
    <div id="message_id_<id>" class="user-response ...">
      <!-- Message content HTML -->
    </div>
  </template>
</turbo-stream>
```

The actual text is embedded in the HTML structure. Key data attributes:
- `id`: `message_id_<message_id>`
- `data-message-scroll-lazy-loaded`: Pagination marker
- Message text in nested `<div>` elements

## Loading Conversation History

```
GET /messages/load.turbo_stream?conversation_id=<id>&before_message_id=<id>
Accept: text/vnd.turbo-stream.html
```

Returns Turbo Stream HTML with historical messages.

## Conversation List

```
GET /conversations.turbo_stream
Accept: text/vnd.turbo-stream.html
```

## Profile/Character Pages

```
GET /ai-girlfriend/<slug>
```

Character profiles use URL slugs (e.g., `harriet-seagrave`).

## Image Generation

Triggered via message with `image_gen_toggle=1` and suggestion parameters.
Images delivered via Turbo Stream updates.

## Token System

Users have a token balance that decreases with AI interactions.
Balance updates via `:token_balance` WebSocket channel.

## Key Observations for Bridge Development

1. **No JSON API**: Everything is HTML-over-WebSocket (Turbo Streams)
2. **Parsing Required**: Must parse HTML to extract message content
3. **Signed Channels**: Stream names are signed - must extract from page HTML
4. **CSRF Rotation**: Tokens change, need periodic refresh
5. **Session Cookies**: Standard Rails encrypted cookies
6. **Real-time via WS**: All live updates through single ActionCable connection

## Session Bootstrap Flow

1. GET homepage → extract CSRF token from meta tag
2. POST `/users/sign_in` with credentials → get session cookie
3. GET conversation page → extract signed stream names from HTML
4. Connect WebSocket to `/cable`
5. Subscribe to relevant channels using signed stream names
6. Send messages via POST `/messages`
7. Receive responses via WebSocket Turbo Streams
