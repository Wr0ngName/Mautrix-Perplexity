#!/usr/bin/env python3
"""
Claude Agent SDK Sidecar for mautrix-claude bridge.

Provides HTTP API for Go bridge to communicate with Claude using Pro/Max subscription.
"""

import asyncio
import fcntl
import hashlib
import json
import logging
import os
import pty
import re
import secrets
import select
import shutil
import struct
import subprocess
import tempfile
import termios
import time
import uuid
from contextlib import asynccontextmanager
from dataclasses import dataclass, field
from pathlib import Path
from typing import AsyncIterator, Dict, Optional

from fastapi import FastAPI, HTTPException
from fastapi.responses import StreamingResponse
from pydantic import BaseModel
from prometheus_client import Counter, Histogram, Gauge, generate_latest, CONTENT_TYPE_LATEST
from starlette.responses import Response

# Agent SDK imports
from claude_agent_sdk import query, ClaudeAgentOptions, ClaudeSDKClient, AssistantMessage, ResultMessage, TextBlock

# Configure logging
logging.basicConfig(
    level=logging.INFO,
    format='%(asctime)s - %(name)s - %(levelname)s - %(message)s'
)
logger = logging.getLogger(__name__)

# Configuration
PORT = int(os.getenv("CLAUDE_SIDECAR_PORT", "8090"))
QUERY_TIMEOUT = int(os.getenv("CLAUDE_SIDECAR_QUERY_TIMEOUT", "300"))  # 5 minutes default

# SECURITY: Whitelist of safe tools that are allowed in multi-user chat
# NEVER add file access, bash, or code editing tools here
SAFE_TOOLS_WHITELIST = frozenset([
    "WebSearch",
    "WebFetch",
    "AskUserQuestion",
])

# SECURITY: Tools that must NEVER be enabled (file access, code execution)
DANGEROUS_TOOLS = frozenset([
    "Bash", "Read", "Write", "Edit", "MultiEdit",
    "Glob", "Grep", "LS", "NotebookEdit",
    "Task", "TodoWrite", "TodoRead",
])

def validate_tools(tools: list[str]) -> list[str]:
    """Validate and filter tool list against whitelist. SECURITY-CRITICAL."""
    if not tools:
        return []
    validated = []
    for tool in tools:
        tool = tool.strip()
        if tool in DANGEROUS_TOOLS:
            logger.warning(f"SECURITY: Blocked attempt to enable dangerous tool: {tool}")
            continue
        if tool not in SAFE_TOOLS_WHITELIST:
            logger.warning(f"SECURITY: Ignoring unknown tool not in whitelist: {tool}")
            continue
        validated.append(tool)
    return validated

# Parse and validate allowed tools from environment
_raw_tools = os.getenv("CLAUDE_SIDECAR_ALLOWED_TOOLS", "WebSearch,WebFetch,AskUserQuestion").split(",")
ALLOWED_TOOLS = validate_tools(_raw_tools)

SYSTEM_PROMPT = os.getenv("CLAUDE_SIDECAR_SYSTEM_PROMPT", "You are a helpful AI assistant.")
MODEL = os.getenv("CLAUDE_SIDECAR_MODEL", "sonnet")
SESSION_TIMEOUT = int(os.getenv("CLAUDE_SIDECAR_SESSION_TIMEOUT", "3600"))  # 1 hour

# Input validation limits
MAX_MESSAGE_LENGTH = 100000  # ~100k chars, matches Go bridge limit
MAX_PORTAL_ID_LENGTH = 256   # Reasonable limit for portal IDs

# Prometheus metrics
REQUESTS_TOTAL = Counter('claude_sidecar_requests_total', 'Total requests', ['endpoint', 'status'])
REQUEST_DURATION = Histogram('claude_sidecar_request_duration_seconds', 'Request duration')
ACTIVE_SESSIONS = Gauge('claude_sidecar_active_sessions', 'Number of active sessions')
TOKENS_USED = Counter('claude_sidecar_tokens_total', 'Total tokens used', ['type'])

# Track auth status globally
_auth_validated = False


@asynccontextmanager
async def lifespan(app: FastAPI):
    """Lifespan context manager for startup and shutdown events."""
    global _auth_validated

    # Startup
    await session_manager.start()
    logger.info(f"Claude sidecar starting on port {PORT}")
    logger.info(f"Allowed tools: {ALLOWED_TOOLS or 'none (chat only)'}")
    logger.info(f"Model: {MODEL}")

    # Verify CLI and Node.js are available (for OAuth and Agent SDK)
    try:
        claude_cli = _find_claude_cli()
        cli_result = subprocess.run([claude_cli, '--version'], capture_output=True, text=True, timeout=10)
        logger.info(f"Claude CLI: {claude_cli} (exit={cli_result.returncode})")
        if cli_result.stdout.strip():
            logger.info(f"CLI version: {cli_result.stdout.strip()}")
        if cli_result.returncode != 0 and cli_result.stderr:
            logger.warning(f"CLI stderr: {cli_result.stderr.strip()}")

        node_result = subprocess.run(['node', '--version'], capture_output=True, text=True, timeout=5)
        logger.info(f"Node.js: {node_result.stdout.strip()}")
    except Exception as e:
        logger.error(f"CLI verification failed: {e}")

    # Validate Claude Code authentication
    _auth_validated = await validate_claude_auth()
    if not _auth_validated:
        logger.error("WARNING: Claude Code is not authenticated!")
        logger.error("Run 'claude' to authenticate before using sidecar mode")
    else:
        logger.info("Claude sidecar ready")

    yield

    # Shutdown
    await session_manager.stop()
    await credentials_manager.cleanup_all()
    logger.info("Claude sidecar stopped")


# FastAPI app
app = FastAPI(
    title="Claude Agent SDK Sidecar",
    description="HTTP API for mautrix-claude bridge",
    version="1.0.0",
    lifespan=lifespan
)


@dataclass
class Session:
    """Represents a conversation session."""
    session_id: str
    portal_id: str
    created_at: float = field(default_factory=time.time)
    last_used: float = field(default_factory=time.time)
    message_count: int = 0
    input_tokens: int = 0
    output_tokens: int = 0
    # Cache token tracking (for prompt caching)
    cache_creation_tokens: int = 0
    cache_read_tokens: int = 0
    # Compaction tracking
    compaction_count: int = 0
    last_compaction_time: Optional[float] = None


class ImageSource(BaseModel):
    """Image source for multimodal messages."""
    type: str  # "base64"
    media_type: str  # "image/jpeg", "image/png", etc.
    data: str  # Base64-encoded image data


class ContentBlock(BaseModel):
    """Content block for multimodal messages."""
    type: str  # "text" or "image"
    text: Optional[str] = None  # For text blocks
    source: Optional[ImageSource] = None  # For image blocks


class ChatRequest(BaseModel):
    """Request body for chat endpoint."""
    portal_id: str
    user_id: Optional[str] = None  # Matrix user ID for per-user sessions
    credentials_json: Optional[str] = None  # User's Claude credentials JSON
    message: str  # Text-only message (backward compat)
    content: Optional[list[ContentBlock]] = None  # Structured content with images (multimodal)
    system_prompt: Optional[str] = None
    model: Optional[str] = None
    session_id: Optional[str] = None  # Agent SDK session ID for resume (stored in bridge DB)
    stream: bool = False

    def has_images(self) -> bool:
        """Check if this request contains images."""
        if not self.content:
            return False
        return any(block.type == "image" for block in self.content)

    def validate_input(self) -> None:
        """Validate input fields. Raises HTTPException on invalid input."""
        # Validate portal_id
        if not self.portal_id or len(self.portal_id) > MAX_PORTAL_ID_LENGTH:
            raise HTTPException(
                status_code=400,
                detail=f"Invalid portal_id: must be 1-{MAX_PORTAL_ID_LENGTH} characters"
            )
        # Basic portal_id format check (alphanumeric and common ID chars allowed)
        if not all(c.isalnum() or c in '_-:!.' for c in self.portal_id):
            raise HTTPException(
                status_code=400,
                detail="Invalid portal_id: contains invalid characters"
            )
        # Validate message - either text message or content blocks required
        has_text = bool(self.message)
        has_content = bool(self.content and len(self.content) > 0)
        if not has_text and not has_content:
            raise HTTPException(status_code=400, detail="Message cannot be empty")
        if has_text and len(self.message) > MAX_MESSAGE_LENGTH:
            raise HTTPException(
                status_code=400,
                detail=f"Message too long: {len(self.message)} chars (max {MAX_MESSAGE_LENGTH})"
            )


class UsageInfo(BaseModel):
    """Detailed token usage information."""
    input_tokens: int = 0
    output_tokens: int = 0
    cache_creation_tokens: int = 0
    cache_read_tokens: int = 0
    total_tokens: int = 0


class ChatResponse(BaseModel):
    """Response body for chat endpoint."""
    portal_id: str
    session_id: str
    response: str
    model: str  # Actual model used for this request
    tokens_used: Optional[int] = None
    usage: Optional[UsageInfo] = None  # Detailed usage breakdown
    compacted: bool = False  # Whether compaction occurred during this request


class SessionManager:
    """Manages conversation sessions per portal."""

    def __init__(self):
        self.sessions: Dict[str, Session] = {}
        self._lock = asyncio.Lock()
        self._cleanup_task: Optional[asyncio.Task] = None

    async def start(self):
        """Start background cleanup task."""
        self._cleanup_task = asyncio.create_task(self._cleanup_loop())
        logger.info("Session manager started")

    async def stop(self):
        """Stop background cleanup task."""
        if self._cleanup_task:
            self._cleanup_task.cancel()
            try:
                await self._cleanup_task
            except asyncio.CancelledError:
                pass
        logger.info("Session manager stopped")

    async def _cleanup_loop(self):
        """Periodically clean up expired sessions."""
        while True:
            try:
                await asyncio.sleep(60)  # Check every minute
                await self._cleanup_expired()
            except asyncio.CancelledError:
                break
            except Exception as e:
                logger.error(f"Error in cleanup loop: {e}")

    async def _cleanup_expired(self):
        """Remove sessions older than timeout."""
        async with self._lock:
            now = time.time()
            expired = [
                portal_id for portal_id, session in self.sessions.items()
                if now - session.last_used > SESSION_TIMEOUT
            ]
            for portal_id in expired:
                del self.sessions[portal_id]
                logger.info(f"Cleaned up expired session for portal {portal_id}")

            ACTIVE_SESSIONS.set(len(self.sessions))

    async def get_or_create(self, portal_id: str) -> Session:
        """Get existing session or create new one."""
        async with self._lock:
            if portal_id not in self.sessions:
                session = Session(
                    session_id=str(uuid.uuid4()),
                    portal_id=portal_id
                )
                self.sessions[portal_id] = session
                logger.info(f"Created new session {session.session_id} for portal {portal_id}")
                ACTIVE_SESSIONS.set(len(self.sessions))

            session = self.sessions[portal_id]
            session.last_used = time.time()
            return session

    async def delete(self, portal_id: str) -> bool:
        """Delete a session."""
        async with self._lock:
            if portal_id in self.sessions:
                del self.sessions[portal_id]
                ACTIVE_SESSIONS.set(len(self.sessions))
                logger.info(f"Deleted session for portal {portal_id}")
                return True
            return False

    async def get_stats(self, portal_id: str) -> Optional[dict]:
        """Get session statistics."""
        async with self._lock:
            if portal_id in self.sessions:
                session = self.sessions[portal_id]
                return {
                    "session_id": session.session_id,
                    "portal_id": session.portal_id,
                    "created_at": session.created_at,
                    "last_used": session.last_used,
                    "message_count": session.message_count,
                    "age_seconds": time.time() - session.created_at,
                    "input_tokens": session.input_tokens,
                    "output_tokens": session.output_tokens,
                    "cache_creation_tokens": session.cache_creation_tokens,
                    "cache_read_tokens": session.cache_read_tokens,
                    "compaction_count": session.compaction_count,
                    "last_compaction_time": session.last_compaction_time,
                }
            return None


# Global session manager
session_manager = SessionManager()


class CredentialsManager:
    """
    Manages per-user Claude credentials.

    Creates temporary directories with user credentials and provides
    the config directory path to use for Claude SDK queries.
    """

    def __init__(self, base_dir: Optional[str] = None):
        """Initialize credentials manager with base temp directory."""
        self._base_dir = Path(base_dir) if base_dir else Path(tempfile.gettempdir()) / "claude-creds"
        self._base_dir.mkdir(parents=True, exist_ok=True)
        self._lock = asyncio.Lock()
        logger.info(f"Credentials manager initialized at {self._base_dir}")

    def _get_user_dir(self, user_id: str) -> Path:
        """Get the credentials directory for a user (hashed for privacy)."""
        # Hash user ID to avoid path issues with special chars in Matrix IDs
        user_hash = hashlib.sha256(user_id.encode()).hexdigest()[:16]
        return self._base_dir / user_hash

    async def setup_credentials(self, user_id: str, credentials_json: str) -> str:
        """
        Set up credentials for a user and return the config directory path.

        Args:
            user_id: Matrix user ID
            credentials_json: JSON string of Claude credentials

        Returns:
            Path to the config directory to use for CLAUDE_CONFIG_DIR
        """
        async with self._lock:
            user_dir = self._get_user_dir(user_id)
            user_dir.mkdir(parents=True, exist_ok=True)

            # Write credentials file
            creds_file = user_dir / ".credentials.json"
            try:
                # Validate JSON before writing
                creds_data = json.loads(credentials_json)
                creds_file.write_text(json.dumps(creds_data, indent=2))
                logger.debug(f"Set up credentials for user {user_id[:20]}...")
            except json.JSONDecodeError:
                # Don't log exception details as they may contain credential fragments
                logger.error(f"Invalid credentials JSON format for user {user_id[:20]}...")
                raise ValueError("Invalid credentials JSON format")

            # Create minimal settings.json - Claude CLI requires this
            settings_file = user_dir / "settings.json"
            settings_data = {
                "hasCompletedOnboarding": True,
                "autoUpdaterStatus": "disabled",
                "hasAcknowledgedCostThreshold": True,
            }
            settings_file.write_text(json.dumps(settings_data, indent=2))

            return str(user_dir)

    async def cleanup_user(self, user_id: str) -> None:
        """Remove credentials for a user."""
        async with self._lock:
            user_dir = self._get_user_dir(user_id)
            if user_dir.exists():
                shutil.rmtree(user_dir, ignore_errors=True)
                logger.debug(f"Cleaned up credentials for user {user_id[:20]}...")

    async def cleanup_all(self) -> None:
        """Remove all cached credentials."""
        async with self._lock:
            if self._base_dir.exists():
                shutil.rmtree(self._base_dir, ignore_errors=True)
                self._base_dir.mkdir(parents=True, exist_ok=True)
                logger.info("Cleaned up all cached credentials")


# Global credentials manager
credentials_manager = CredentialsManager()


# ============================================================================
# OAuth Login Flow (using claude setup-token subprocess)
# ============================================================================

# Pending OAuth flows (state -> {user_id, master_fd, proc, config_dir, created_at})
_oauth_pending: Dict[str, dict] = {}
_oauth_lock = asyncio.Lock()
OAUTH_PENDING_TIMEOUT = 600  # 10 minutes


class OAuthStartRequest(BaseModel):
    user_id: str


class OAuthStartResponse(BaseModel):
    auth_url: str
    state: str


class OAuthCompleteRequest(BaseModel):
    user_id: str
    state: str
    code: str


class OAuthCompleteResponse(BaseModel):
    success: bool
    credentials_json: Optional[str] = None
    message: str


def _find_claude_cli() -> str:
    """
    Find the Claude CLI executable path.

    Checks in order:
    1. Bundled CLI from claude-agent-sdk package
    2. 'claude' in PATH

    Returns the path to the CLI executable.
    Raises RuntimeError if not found.
    """
    # Try bundled CLI from agent SDK
    try:
        import claude_agent_sdk
        sdk_dir = Path(claude_agent_sdk.__file__).parent
        bundled = sdk_dir / "_bundled" / "claude"
        if bundled.exists():
            logger.debug(f"Using bundled Claude CLI: {bundled}")
            return str(bundled)
    except Exception as e:
        logger.debug(f"Could not find bundled CLI: {e}")

    # Try system PATH
    import shutil as shutil_mod
    claude_path = shutil_mod.which("claude")
    if claude_path:
        logger.debug(f"Using system Claude CLI: {claude_path}")
        return claude_path

    raise RuntimeError("Claude CLI not found. Install claude-agent-sdk or add claude to PATH.")


def _run_setup_token_and_get_url(config_dir: str) -> tuple[str, int, any]:
    """
    Run claude setup-token in a PTY and capture the OAuth URL.

    Returns (auth_url, master_fd, process) tuple.
    The master_fd and process are kept alive to later send the auth code.
    """
    # Create pseudo-terminal
    master, slave = pty.openpty()

    # Set terminal size to 500 columns to prevent URL line-wrapping
    # struct winsize: unsigned short rows, cols, xpixel, ypixel
    winsize = struct.pack('HHHH', 24, 500, 0, 0)
    fcntl.ioctl(slave, termios.TIOCSWINSZ, winsize)

    # Environment without browser
    env = os.environ.copy()
    env['BROWSER'] = '/bin/false'
    env.pop('DISPLAY', None)
    env['CLAUDE_CONFIG_DIR'] = config_dir

    # Find Claude CLI path
    claude_cli = _find_claude_cli()

    # Run claude setup-token
    proc = subprocess.Popen(
        [claude_cli, 'setup-token'],
        stdin=slave,
        stdout=slave,
        stderr=slave,
        preexec_fn=os.setsid,
        env=env
    )

    os.close(slave)

    # Read output until we find the URL and "Paste code" prompt
    output = b''
    start_time = time.time()

    while time.time() - start_time < 30:  # 30 second timeout
        ready, _, _ = select.select([master], [], [], 0.5)
        if ready:
            try:
                data = os.read(master, 4096)
                if data:
                    output += data
            except:
                break

        # Check if we have the URL and prompt
        decoded = output.decode('utf-8', errors='ignore')
        if 'https://claude.ai/oauth/authorize' in decoded and 'Paste code' in decoded:
            break

    # Parse the URL from output
    decoded = output.decode('utf-8', errors='ignore')
    # Remove ALL ANSI escape codes (CSI, OSC, private sequences)
    clean = re.sub(r'\x1b\[[0-9;?]*[a-zA-Z]', '', decoded)  # CSI sequences including ?
    clean = re.sub(r'\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)', '', clean)  # OSC sequences
    clean = re.sub(r'\x1b[PX^_][^\x1b]*\x1b\\', '', clean)  # DCS, SOS, PM, APC
    clean = re.sub(r'\x1b.', '', clean)  # Any remaining single-char escapes

    # Find the OAuth URL
    url_match = re.search(r'(https://claude\.ai/oauth/authorize\S+)', clean)
    if not url_match:
        os.close(master)
        proc.terminate()
        logger.error(f"Failed to find OAuth URL in output. Clean length: {len(clean)}")
        logger.debug(f"Clean output (last 500 chars): {clean[-500:]}")
        raise RuntimeError("Failed to get OAuth URL from claude setup-token")

    auth_url = url_match.group(1)
    logger.info(f"Extracted OAuth URL: length={len(auth_url)}, ends_with=...{auth_url[-30:]}")
    return auth_url, master, proc


# Global lock for CLAUDE_CONFIG_DIR manipulation
# SECURITY: This prevents race conditions where concurrent requests could
# cause User A's query to run with User B's credentials
_env_lock = asyncio.Lock()


async def validate_claude_auth() -> bool:
    """Validate that Claude Code is authenticated by making a test query."""
    try:
        logger.info("Validating Claude Code authentication...")
        options = ClaudeAgentOptions(
            allowed_tools=[],  # No tools for validation
            disallowed_tools=list(DANGEROUS_TOOLS),  # SECURITY: block dangerous tools
            permission_mode="bypassPermissions",
            model=MODEL,
            max_turns=1,  # Single turn only
        )

        # Simple test query - MUST consume all messages to avoid cancel scope errors
        got_result = False
        async for message in query(prompt="Say 'OK' and nothing else.", options=options):
            if hasattr(message, 'result'):
                got_result = True
                # Don't break/return - let the generator complete naturally

        if got_result:
            logger.info("Claude Code authentication validated successfully")
            return True

        logger.warning("Auth validation: no result received")
        return False
    except Exception as e:
        logger.error(f"Claude Code authentication failed: {e}")
        return False


@app.get("/health")
async def health():
    """
    Health check endpoint.

    Always returns 200 OK so the sidecar is considered "running".
    The "authenticated" field indicates whether Claude Code auth is valid.
    The "status" field is "healthy" only when authenticated.

    Consumers should check the "authenticated" field to determine if
    the sidecar can actually process requests.
    """
    return {
        "status": "healthy" if _auth_validated else "degraded",
        "sessions": len(session_manager.sessions),
        "authenticated": _auth_validated,
        "message": None if _auth_validated else "Claude Code not authenticated - run 'claude' to authenticate"
    }


@app.get("/metrics")
async def metrics():
    """Prometheus metrics endpoint."""
    return Response(content=generate_latest(), media_type=CONTENT_TYPE_LATEST)


class TestAuthRequest(BaseModel):
    """Request body for auth test endpoint."""
    user_id: str
    credentials_json: str


class TestAuthResponse(BaseModel):
    """Response body for auth test endpoint."""
    success: bool
    message: str


@app.post("/v1/auth/test", response_model=TestAuthResponse)
async def test_auth(request: TestAuthRequest):
    """
    Test user credentials by making a minimal Claude API call.

    This validates that the provided credentials are valid and can
    communicate with Claude before completing the login flow.
    """
    if not request.user_id or not request.credentials_json:
        raise HTTPException(status_code=400, detail="user_id and credentials_json required")

    # Validate credentials JSON format
    try:
        creds = json.loads(request.credentials_json)
        if "claudeAiOauth" not in creds and "access_token" not in creds and "oauthToken" not in creds:
            return TestAuthResponse(
                success=False,
                message="Invalid credentials format: missing authentication data"
            )
    except json.JSONDecodeError as e:
        return TestAuthResponse(success=False, message=f"Invalid JSON: {e}")

    # SECURITY: Use global lock when manipulating environment variables
    async with _env_lock:
        config_dir = None
        original_config_dir = os.environ.get("CLAUDE_CONFIG_DIR")
        original_oauth_token = os.environ.get("CLAUDE_CODE_OAUTH_TOKEN")

        try:
            # Check if this is an OAuth token (from setup-token)
            if "oauthToken" in creds:
                # Use CLAUDE_CODE_OAUTH_TOKEN env var for setup-token tokens
                os.environ["CLAUDE_CODE_OAUTH_TOKEN"] = creds["oauthToken"]
                logger.info(f"Testing OAuth token for user {request.user_id[:20]}...")
            else:
                # Set up user credentials file for other formats
                config_dir = await credentials_manager.setup_credentials(
                    request.user_id, request.credentials_json
                )
                os.environ["CLAUDE_CONFIG_DIR"] = config_dir
                logger.info(f"Testing credentials for user {request.user_id[:20]}...")

            # Make a minimal test query
            options = ClaudeAgentOptions(
                allowed_tools=[],  # No tools for test
                disallowed_tools=list(DANGEROUS_TOOLS),  # SECURITY: block dangerous tools
                permission_mode="bypassPermissions",
                model="haiku",  # Use cheapest/fastest model for test
                max_turns=1,
            )

            # Simple test prompt - consume all messages to avoid cleanup errors
            got_response = False
            async for message in query(prompt="Say 'OK'", options=options):
                if hasattr(message, 'result'):
                    got_response = True
                    # Don't break - let the generator complete naturally

            if got_response:
                logger.info(f"Credentials validated successfully for user {request.user_id[:20]}...")
                return TestAuthResponse(success=True, message="Credentials validated successfully")
            else:
                logger.warning(f"Credentials test failed for user {request.user_id[:20]}... - no response")
                return TestAuthResponse(success=False, message="Authentication failed - no response from Claude")

        except Exception as e:
            logger.error(f"Credentials test failed for user {request.user_id[:20]}...: {e}")
            error_msg = str(e)
            if "authentication" in error_msg.lower() or "unauthorized" in error_msg.lower():
                return TestAuthResponse(success=False, message="Invalid or expired credentials")
            return TestAuthResponse(success=False, message=f"Authentication failed: {error_msg}")
        finally:
            # Restore original environment variables
            if original_config_dir is not None:
                os.environ["CLAUDE_CONFIG_DIR"] = original_config_dir
            elif config_dir is not None and "CLAUDE_CONFIG_DIR" in os.environ:
                del os.environ["CLAUDE_CONFIG_DIR"]
            # Restore CLAUDE_CODE_OAUTH_TOKEN
            if original_oauth_token is not None:
                os.environ["CLAUDE_CODE_OAUTH_TOKEN"] = original_oauth_token
            elif "CLAUDE_CODE_OAUTH_TOKEN" in os.environ and "oauthToken" in creds:
                del os.environ["CLAUDE_CODE_OAUTH_TOKEN"]


@dataclass
class QueryResult:
    """Result of a query with detailed usage information."""
    response_text: str = ""
    session_id: str = ""
    input_tokens: int = 0
    output_tokens: int = 0
    cache_creation_tokens: int = 0
    cache_read_tokens: int = 0
    compacted: bool = False


def _build_multimodal_content(content_blocks: list[ContentBlock]) -> list[dict]:
    """
    Build multimodal content blocks for Agent SDK streaming input.

    Converts our ContentBlock model to the format expected by ClaudeSDKClient:
    - Text: {"type": "text", "text": "..."}
    - Image: {"type": "image", "source": {"type": "base64", "media_type": "...", "data": "..."}}
    """
    result = []
    for block in content_blocks:
        if block.type == "text" and block.text:
            result.append({
                "type": "text",
                "text": block.text
            })
        elif block.type == "image" and block.source:
            result.append({
                "type": "image",
                "source": {
                    "type": block.source.type,
                    "media_type": block.source.media_type,
                    "data": block.source.data
                }
            })
    return result


async def _run_multimodal_query_with_timeout(
    content_blocks: list[ContentBlock],
    options: ClaudeAgentOptions,
    timeout_seconds: int,
    portal_id: str,
) -> QueryResult:
    """
    Run a multimodal query (with images) using ClaudeSDKClient streaming input.

    This uses the ClaudeSDKClient with an async generator to support image uploads,
    as the simple query() function doesn't support multimodal content.

    Returns QueryResult with response, session_id, and detailed token usage.
    Raises TimeoutError if the query takes too long.
    """
    result = QueryResult()

    # Build the multimodal content structure
    content = _build_multimodal_content(content_blocks)

    # Count images for logging
    image_count = sum(1 for b in content_blocks if b.type == "image")
    logger.info(f"Running multimodal query for portal {portal_id} with {image_count} image(s)")

    async def message_generator():
        """Generate a single message with multimodal content."""
        yield {
            "type": "user",
            "message": {
                "role": "user",
                "content": content
            }
        }

    try:
        async with asyncio.timeout(timeout_seconds):
            async with ClaudeSDKClient(options) as client:
                # Send the multimodal message
                await client.query(message_generator())

                # Process responses
                async for message in client.receive_response():
                    # Capture session ID from SystemMessage init
                    if hasattr(message, 'subtype') and message.subtype == 'init':
                        if hasattr(message, 'data') and 'session_id' in message.data:
                            result.session_id = message.data['session_id']
                            logger.debug(f"Got session_id from Agent SDK: {result.session_id}")

                    # Detect compaction events
                    if hasattr(message, 'subtype') and message.subtype == 'compact':
                        result.compacted = True
                        logger.info(f"Context compaction occurred for portal {portal_id}")

                    # Capture result from ResultMessage
                    if isinstance(message, ResultMessage):
                        if message.result:
                            result.response_text = message.result
                        # Capture token usage
                        if message.usage:
                            usage = message.usage
                            if isinstance(usage, dict):
                                result.input_tokens += usage.get('input_tokens', 0)
                                result.output_tokens += usage.get('output_tokens', 0)
                                result.cache_creation_tokens += usage.get('cache_creation_input_tokens', 0)
                                result.cache_read_tokens += usage.get('cache_read_input_tokens', 0)

                    # Also capture text from AssistantMessage content blocks
                    if isinstance(message, AssistantMessage):
                        for block in message.content:
                            if isinstance(block, TextBlock):
                                # Accumulate text (result may come in parts)
                                if not result.response_text:
                                    result.response_text = block.text
                                else:
                                    result.response_text += block.text

        # Update Prometheus metrics
        if result.input_tokens > 0:
            TOKENS_USED.labels(type='input').inc(result.input_tokens)
        if result.output_tokens > 0:
            TOKENS_USED.labels(type='output').inc(result.output_tokens)

    except asyncio.TimeoutError:
        logger.error(f"Multimodal query timed out after {timeout_seconds}s for portal {portal_id}")
        raise

    return result


async def _run_query_with_timeout(
    prompt: str,
    options: ClaudeAgentOptions,
    timeout_seconds: int,
    portal_id: str,
) -> QueryResult:
    """
    Run a query with timeout protection.

    Returns QueryResult with response, session_id, and detailed token usage.
    Raises TimeoutError if the query takes too long.
    """
    result = QueryResult()

    try:
        async with asyncio.timeout(timeout_seconds):
            async for message in query(prompt=prompt, options=options):
                # Capture session ID on init (returned to bridge for storage)
                if hasattr(message, 'subtype') and message.subtype == 'init':
                    if hasattr(message, 'data') and 'session_id' in message.data:
                        result.session_id = message.data['session_id']
                        logger.debug(f"Got session_id from Agent SDK: {result.session_id}")

                # Detect compaction events via SystemMessage
                if hasattr(message, 'subtype') and message.subtype == 'compact':
                    result.compacted = True
                    logger.info(f"Context compaction occurred for portal {portal_id}")

                # Capture result from ResultMessage
                if hasattr(message, 'result') and message.result:
                    result.response_text = message.result

                # Capture detailed token usage from ResultMessage.usage dict
                if hasattr(message, 'usage') and message.usage:
                    usage = message.usage
                    # Handle both dict and object-style access
                    if isinstance(usage, dict):
                        result.input_tokens += usage.get('input_tokens', 0)
                        result.output_tokens += usage.get('output_tokens', 0)
                        result.cache_creation_tokens += usage.get('cache_creation_input_tokens', 0)
                        result.cache_read_tokens += usage.get('cache_read_input_tokens', 0)
                    else:
                        if hasattr(usage, 'input_tokens'):
                            result.input_tokens += usage.input_tokens
                        if hasattr(usage, 'output_tokens'):
                            result.output_tokens += usage.output_tokens
                        if hasattr(usage, 'cache_creation_input_tokens'):
                            result.cache_creation_tokens += usage.cache_creation_input_tokens
                        if hasattr(usage, 'cache_read_input_tokens'):
                            result.cache_read_tokens += usage.cache_read_input_tokens

                    # Update Prometheus metrics
                    TOKENS_USED.labels(type='input').inc(result.input_tokens)
                    TOKENS_USED.labels(type='output').inc(result.output_tokens)
    except asyncio.TimeoutError:
        logger.error(f"Query timed out after {timeout_seconds}s for portal {portal_id}")
        raise

    return result


@app.post("/v1/chat", response_model=ChatResponse)
async def chat(request: ChatRequest):
    """
    Send a message to Claude and get a response.

    Maintains conversation context per portal_id.
    Supports per-user credentials via user_id and credentials_json.
    """
    start_time = time.time()

    # Validate input before processing
    request.validate_input()

    # Get or create session for this portal (outside lock - session manager has own lock)
    session = await session_manager.get_or_create(request.portal_id)

    # SECURITY: Use global lock when manipulating environment variables to prevent
    # race conditions that could cause credential leakage between users.
    # This serializes requests with per-user credentials (performance trade-off for security).
    async with _env_lock:
        config_dir = None
        original_config_dir = os.environ.get("CLAUDE_CONFIG_DIR")
        original_oauth_token = os.environ.get("CLAUDE_CODE_OAUTH_TOKEN")
        using_oauth_token = False

        try:
            if request.user_id and request.credentials_json:
                try:
                    creds = json.loads(request.credentials_json)
                    if "oauthToken" in creds:
                        # Use CLAUDE_CODE_OAUTH_TOKEN env var for setup-token tokens
                        os.environ["CLAUDE_CODE_OAUTH_TOKEN"] = creds["oauthToken"]
                        using_oauth_token = True
                        logger.debug(f"Using OAuth token for user {request.user_id[:20]}...")
                    else:
                        config_dir = await credentials_manager.setup_credentials(
                            request.user_id, request.credentials_json
                        )
                        os.environ["CLAUDE_CONFIG_DIR"] = config_dir
                        logger.debug(f"Using per-user credentials from {config_dir}")
                except ValueError as e:
                    raise HTTPException(status_code=400, detail=str(e))

            # Determine actual model to use
            actual_model = request.model or MODEL

            # Build options
            # SECURITY: Use disallowed_tools (not allowed_tools) because allowed_tools
            # is ignored for built-in tools per GitHub issue #361
            options = ClaudeAgentOptions(
                allowed_tools=ALLOWED_TOOLS if ALLOWED_TOOLS else [],
                disallowed_tools=list(DANGEROUS_TOOLS),  # CRITICAL: blocks Read, Write, Bash, etc.
                permission_mode="bypassPermissions",  # No interactive prompts
                model=actual_model,
            )

            # Resume session if session_id provided by bridge (stored in bridge DB)
            session_id_to_use = request.session_id
            if session_id_to_use:
                options.resume = session_id_to_use
                logger.debug(f"Resuming session {session_id_to_use} for portal {request.portal_id}")

            # Set system prompt
            system_prompt = request.system_prompt or SYSTEM_PROMPT
            if system_prompt:
                options.system_prompt = system_prompt

            # Query Claude with timeout
            # If resume fails/times out, retry without session_id
            query_result: QueryResult

            # Check if we have images - use multimodal query if so
            has_images = request.has_images()

            try:
                if has_images and request.content:
                    # Use ClaudeSDKClient with streaming input for images
                    query_result = await _run_multimodal_query_with_timeout(
                        content_blocks=request.content,
                        options=options,
                        timeout_seconds=QUERY_TIMEOUT,
                        portal_id=request.portal_id,
                    )
                else:
                    # Use simple query() for text-only messages
                    query_result = await _run_query_with_timeout(
                        prompt=request.message,
                        options=options,
                        timeout_seconds=QUERY_TIMEOUT,
                        portal_id=request.portal_id,
                    )
            except asyncio.TimeoutError:
                # If we were trying to resume a session and it timed out, retry without resume
                if session_id_to_use:
                    logger.warning(f"Session resume timed out for {request.portal_id}, retrying without session_id")
                    options.resume = None
                    if has_images and request.content:
                        query_result = await _run_multimodal_query_with_timeout(
                            content_blocks=request.content,
                            options=options,
                            timeout_seconds=QUERY_TIMEOUT,
                            portal_id=request.portal_id,
                        )
                    else:
                        query_result = await _run_query_with_timeout(
                            prompt=request.message,
                            options=options,
                            timeout_seconds=QUERY_TIMEOUT,
                            portal_id=request.portal_id,
                        )
                else:
                    # No session to retry without, propagate the timeout
                    raise HTTPException(
                        status_code=504,
                        detail=f"Request timed out after {QUERY_TIMEOUT}s"
                    )

            # Estimate tokens if Agent SDK didn't provide them (~4 chars per token)
            if query_result.input_tokens == 0 and request.message:
                query_result.input_tokens = max(1, len(request.message) // 4)
                TOKENS_USED.labels(type='input').inc(query_result.input_tokens)
            if query_result.output_tokens == 0 and query_result.response_text:
                query_result.output_tokens = max(1, len(query_result.response_text) // 4)
                TOKENS_USED.labels(type='output').inc(query_result.output_tokens)

            # Update session with detailed usage
            session.message_count += 1
            session.last_used = time.time()
            session.input_tokens += query_result.input_tokens
            session.output_tokens += query_result.output_tokens
            session.cache_creation_tokens += query_result.cache_creation_tokens
            session.cache_read_tokens += query_result.cache_read_tokens

            # Track compaction
            if query_result.compacted:
                session.compaction_count += 1
                session.last_compaction_time = time.time()

            REQUESTS_TOTAL.labels(endpoint='/v1/chat', status='success').inc()
            REQUEST_DURATION.observe(time.time() - start_time)

            # Build detailed usage info
            total_tokens = query_result.input_tokens + query_result.output_tokens
            usage_info = UsageInfo(
                input_tokens=query_result.input_tokens,
                output_tokens=query_result.output_tokens,
                cache_creation_tokens=query_result.cache_creation_tokens,
                cache_read_tokens=query_result.cache_read_tokens,
                total_tokens=total_tokens,
            )

            return ChatResponse(
                portal_id=request.portal_id,
                session_id=query_result.session_id or "",  # Return session_id for bridge to store
                response=query_result.response_text,
                model=actual_model,
                tokens_used=total_tokens if total_tokens > 0 else None,
                usage=usage_info,
                compacted=query_result.compacted,
            )

        except HTTPException:
            # Re-raise HTTP exceptions (validation errors, etc.)
            raise
        except asyncio.TimeoutError:
            # Timeout without session retry (already tried)
            logger.error(f"Query timed out for portal {request.portal_id}")
            REQUESTS_TOTAL.labels(endpoint='/v1/chat', status='error').inc()
            raise HTTPException(status_code=504, detail=f"Request timed out after {QUERY_TIMEOUT}s")
        except Exception as e:
            # Log full error but don't expose details to client (security)
            logger.error(f"Error processing chat request for portal {request.portal_id}: {e}", exc_info=True)
            REQUESTS_TOTAL.labels(endpoint='/v1/chat', status='error').inc()
            raise HTTPException(status_code=500, detail="Internal error processing request")
        finally:
            # Restore original environment variables
            if original_config_dir is not None:
                os.environ["CLAUDE_CONFIG_DIR"] = original_config_dir
            elif config_dir is not None and "CLAUDE_CONFIG_DIR" in os.environ:
                del os.environ["CLAUDE_CONFIG_DIR"]
            # Restore CLAUDE_CODE_OAUTH_TOKEN
            if original_oauth_token is not None:
                os.environ["CLAUDE_CODE_OAUTH_TOKEN"] = original_oauth_token
            elif using_oauth_token and "CLAUDE_CODE_OAUTH_TOKEN" in os.environ:
                del os.environ["CLAUDE_CODE_OAUTH_TOKEN"]


@app.post("/v1/chat/stream")
async def chat_stream(request: ChatRequest):
    """
    Send a message to Claude and stream the response.

    Returns Server-Sent Events (SSE) stream.
    Supports per-user credentials via user_id and credentials_json.
    """
    # Validate input before processing
    request.validate_input()

    # Get or create session for this portal (outside lock - session manager has own lock)
    session = await session_manager.get_or_create(request.portal_id)

    async def generate() -> AsyncIterator[str]:
        # SECURITY: Use global lock when manipulating environment variables
        async with _env_lock:
            config_dir = None
            original_config_dir = os.environ.get("CLAUDE_CONFIG_DIR")
            original_oauth_token = os.environ.get("CLAUDE_CODE_OAUTH_TOKEN")
            using_oauth_token = False

            try:
                if request.user_id and request.credentials_json:
                    try:
                        creds = json.loads(request.credentials_json)
                        if "oauthToken" in creds:
                            os.environ["CLAUDE_CODE_OAUTH_TOKEN"] = creds["oauthToken"]
                            using_oauth_token = True
                            logger.debug(f"Using OAuth token for user {request.user_id[:20]}...")
                        else:
                            config_dir = await credentials_manager.setup_credentials(
                                request.user_id, request.credentials_json
                            )
                            os.environ["CLAUDE_CONFIG_DIR"] = config_dir
                            logger.debug(f"Using per-user credentials from {config_dir}")
                    except ValueError as e:
                        yield f"data: {json.dumps({'type': 'error', 'message': str(e)})}\n\n"
                        return

                # Determine actual model to use
                actual_model = request.model or MODEL

                # SECURITY: Use disallowed_tools per GitHub issue #361
                options = ClaudeAgentOptions(
                    allowed_tools=ALLOWED_TOOLS if ALLOWED_TOOLS else [],
                    disallowed_tools=list(DANGEROUS_TOOLS),
                    permission_mode="bypassPermissions",
                    model=actual_model,
                )

                # Use session_id from request (bridge DB) if provided, otherwise fall back to local session
                session_id_to_use = request.session_id
                if session_id_to_use:
                    options.resume = session_id_to_use
                    logger.debug(f"Resuming session {session_id_to_use} for portal {request.portal_id} (stream)")
                elif session.message_count > 0:
                    options.resume = session.session_id

                system_prompt = request.system_prompt or SYSTEM_PROMPT
                if system_prompt:
                    options.system_prompt = system_prompt

                request_input_tokens = 0
                request_output_tokens = 0
                response_text = ""
                timed_out = False

                try:
                    async with asyncio.timeout(QUERY_TIMEOUT):
                        async for message in query(prompt=request.message, options=options):
                            # Capture session ID
                            if hasattr(message, 'subtype') and message.subtype == 'init':
                                if hasattr(message, 'data') and 'session_id' in message.data:
                                    session.session_id = message.data['session_id']
                                    yield f"data: {json.dumps({'type': 'session', 'session_id': session.session_id, 'model': actual_model})}\n\n"

                            # Stream assistant messages
                            if hasattr(message, 'type') and message.type == 'assistant':
                                if hasattr(message, 'message') and message.message:
                                    for block in message.message.content:
                                        if hasattr(block, 'text'):
                                            yield f"data: {json.dumps({'type': 'text', 'content': block.text})}\n\n"

                            # Stream final result
                            if hasattr(message, 'result'):
                                response_text = message.result
                                yield f"data: {json.dumps({'type': 'result', 'content': message.result})}\n\n"

                            # Capture token usage if available
                            if hasattr(message, 'usage'):
                                if hasattr(message.usage, 'input_tokens'):
                                    request_input_tokens += message.usage.input_tokens
                                    TOKENS_USED.labels(type='input').inc(message.usage.input_tokens)
                                if hasattr(message.usage, 'output_tokens'):
                                    request_output_tokens += message.usage.output_tokens
                                    TOKENS_USED.labels(type='output').inc(message.usage.output_tokens)
                except asyncio.TimeoutError:
                    timed_out = True
                    logger.error(f"Stream query timed out after {QUERY_TIMEOUT}s for portal {request.portal_id}")

                    # If we were resuming a session and it timed out, retry without session
                    if session_id_to_use or (session.message_count > 0):
                        logger.warning(f"Retrying without session resume for {request.portal_id}")
                        options.resume = None
                        try:
                            async with asyncio.timeout(QUERY_TIMEOUT):
                                async for message in query(prompt=request.message, options=options):
                                    if hasattr(message, 'subtype') and message.subtype == 'init':
                                        if hasattr(message, 'data') and 'session_id' in message.data:
                                            session.session_id = message.data['session_id']
                                            yield f"data: {json.dumps({'type': 'session', 'session_id': session.session_id, 'model': actual_model})}\n\n"
                                    if hasattr(message, 'type') and message.type == 'assistant':
                                        if hasattr(message, 'message') and message.message:
                                            for block in message.message.content:
                                                if hasattr(block, 'text'):
                                                    yield f"data: {json.dumps({'type': 'text', 'content': block.text})}\n\n"
                                    if hasattr(message, 'result'):
                                        response_text = message.result
                                        yield f"data: {json.dumps({'type': 'result', 'content': message.result})}\n\n"
                                    if hasattr(message, 'usage'):
                                        if hasattr(message.usage, 'input_tokens'):
                                            request_input_tokens += message.usage.input_tokens
                                            TOKENS_USED.labels(type='input').inc(message.usage.input_tokens)
                                        if hasattr(message.usage, 'output_tokens'):
                                            request_output_tokens += message.usage.output_tokens
                                            TOKENS_USED.labels(type='output').inc(message.usage.output_tokens)
                            timed_out = False  # Retry succeeded
                        except asyncio.TimeoutError:
                            logger.error(f"Stream query retry also timed out for portal {request.portal_id}")
                            yield f"data: {json.dumps({'type': 'error', 'message': f'Request timed out after {QUERY_TIMEOUT}s'})}\n\n"
                            return
                    else:
                        yield f"data: {json.dumps({'type': 'error', 'message': f'Request timed out after {QUERY_TIMEOUT}s'})}\n\n"
                        return

                # Estimate tokens if Agent SDK didn't provide them (~4 chars per token)
                if request_input_tokens == 0 and request.message:
                    request_input_tokens = max(1, len(request.message) // 4)
                    TOKENS_USED.labels(type='input').inc(request_input_tokens)
                if request_output_tokens == 0 and response_text:
                    request_output_tokens = max(1, len(response_text) // 4)
                    TOKENS_USED.labels(type='output').inc(request_output_tokens)

                session.message_count += 1
                session.last_used = time.time()
                session.input_tokens += request_input_tokens
                session.output_tokens += request_output_tokens

                yield "data: {\"type\": \"done\"}\n\n"

            except Exception as e:
                # Log full error but don't expose details to client (security)
                logger.error(f"Error in stream for portal {request.portal_id}: {e}", exc_info=True)
                yield f"data: {json.dumps({'type': 'error', 'message': 'Internal error processing request'})}\n\n"
            finally:
                # Restore original environment variables
                if original_config_dir is not None:
                    os.environ["CLAUDE_CONFIG_DIR"] = original_config_dir
                elif config_dir is not None and "CLAUDE_CONFIG_DIR" in os.environ:
                    del os.environ["CLAUDE_CONFIG_DIR"]
                # Restore CLAUDE_CODE_OAUTH_TOKEN
                if original_oauth_token is not None:
                    os.environ["CLAUDE_CODE_OAUTH_TOKEN"] = original_oauth_token
                elif using_oauth_token and "CLAUDE_CODE_OAUTH_TOKEN" in os.environ:
                    del os.environ["CLAUDE_CODE_OAUTH_TOKEN"]

    return StreamingResponse(
        generate(),
        media_type="text/event-stream",
        headers={
            "Cache-Control": "no-cache",
            "Connection": "keep-alive",
        }
    )


@app.delete("/v1/sessions/{portal_id}")
async def delete_session(portal_id: str):
    """Delete a session (clear conversation history)."""
    deleted = await session_manager.delete(portal_id)
    if deleted:
        return {"status": "deleted", "portal_id": portal_id}
    else:
        raise HTTPException(status_code=404, detail="Session not found")


@app.get("/v1/sessions/{portal_id}")
async def get_session(portal_id: str):
    """Get session statistics."""
    stats = await session_manager.get_stats(portal_id)
    if stats:
        return stats
    else:
        raise HTTPException(status_code=404, detail="Session not found")


def _cleanup_oauth_flow(flow_data: dict) -> None:
    """Clean up resources from an OAuth flow (PTY, process, config dir)."""
    # Close PTY master fd
    try:
        os.close(flow_data.get("master_fd", -1))
    except:
        pass
    # Terminate process
    try:
        proc = flow_data.get("proc")
        if proc:
            proc.terminate()
            proc.wait(timeout=2)
    except:
        pass
    # Remove config directory
    try:
        config_dir = flow_data.get("config_dir")
        if config_dir:
            shutil.rmtree(config_dir, ignore_errors=True)
            logger.debug(f"Cleaned up OAuth config dir: {config_dir}")
    except:
        pass


@app.post("/v1/auth/oauth/start", response_model=OAuthStartResponse)
async def oauth_start(request: OAuthStartRequest):
    """
    Start OAuth login flow using claude setup-token subprocess.

    Returns an authorization URL that the user should visit in their browser.
    After authenticating, they'll see a code to paste back to complete login.
    """
    if not request.user_id:
        raise HTTPException(status_code=400, detail="user_id required")

    # Generate state for this flow (used for both CSRF protection and unique directory)
    state = secrets.token_urlsafe(32)

    # Create temp config dir using state (unique per flow, not per user)
    # This allows same user to have multiple concurrent login attempts
    config_dir = str(Path(tempfile.gettempdir()) / f"claude-oauth-{state[:16]}")
    Path(config_dir).mkdir(parents=True, exist_ok=True)

    try:
        # Run claude setup-token and get the OAuth URL
        auth_url, master_fd, proc = _run_setup_token_and_get_url(config_dir)

        # Store pending OAuth flow
        async with _oauth_lock:
            # Clean up expired pending flows
            now = time.time()
            expired_states = [s for s, data in _oauth_pending.items()
                             if now - data["created_at"] > OAUTH_PENDING_TIMEOUT]
            for s in expired_states:
                _cleanup_oauth_flow(data)
                del _oauth_pending[s]

            # Store this flow
            _oauth_pending[state] = {
                "user_id": request.user_id,
                "master_fd": master_fd,
                "proc": proc,
                "config_dir": config_dir,
                "created_at": now,
            }

        logger.info(f"Started OAuth flow for user {request.user_id[:20]}... URL length: {len(auth_url)}")
        logger.debug(f"OAuth URL: {auth_url}")
        return OAuthStartResponse(auth_url=auth_url, state=state)

    except Exception as e:
        # Clean up on failure
        shutil.rmtree(config_dir, ignore_errors=True)
        logger.error(f"Failed to start OAuth flow: {e}")
        raise HTTPException(status_code=500, detail=f"Failed to start OAuth: {e}")


@app.post("/v1/auth/oauth/complete", response_model=OAuthCompleteResponse)
async def oauth_complete(request: OAuthCompleteRequest):
    """
    Complete OAuth login flow by sending the code to claude setup-token.

    The code is displayed to the user after they complete authentication in their browser.
    Returns credentials_json that can be used for subsequent requests.
    """
    if not request.user_id or not request.state or not request.code:
        raise HTTPException(status_code=400, detail="user_id, state, and code required")

    # Get and validate pending flow
    async with _oauth_lock:
        flow_data = _oauth_pending.get(request.state)
        if not flow_data:
            return OAuthCompleteResponse(
                success=False,
                message="Invalid or expired OAuth state. Please start the login flow again."
            )

        # Validate user matches - if mismatch, this could be a hijack attempt
        # Clean up and reject to prevent resource leak
        if flow_data["user_id"] != request.user_id:
            _cleanup_oauth_flow(flow_data)
            del _oauth_pending[request.state]
            return OAuthCompleteResponse(
                success=False,
                message="User mismatch. Please start the login flow again."
            )

        # Check expiration
        if time.time() - flow_data["created_at"] > OAUTH_PENDING_TIMEOUT:
            _cleanup_oauth_flow(flow_data)
            del _oauth_pending[request.state]
            return OAuthCompleteResponse(
                success=False,
                message="OAuth flow expired. Please start the login flow again."
            )

        # Remove from pending (we'll handle cleanup after completion)
        del _oauth_pending[request.state]

    master_fd = flow_data["master_fd"]
    proc = flow_data["proc"]
    config_dir = flow_data["config_dir"]

    # Send code to claude setup-token
    try:
        # Check if process is still running
        poll_result = proc.poll()
        if poll_result is not None:
            logger.error(f"OAuth process already exited with code {poll_result} before receiving code")
            # Try to read any remaining output
            try:
                remaining = os.read(master_fd, 16384)
                logger.error(f"Remaining output before exit: {remaining.decode('utf-8', errors='ignore')[-500:]}")
            except:
                pass
            shutil.rmtree(config_dir, ignore_errors=True)
            return OAuthCompleteResponse(
                success=False,
                message="Login session expired. The authentication process ended before receiving your code. Please start login again."
            )

        logger.info(f"OAuth process still running (pid={proc.pid}), sending code...")

        # Drain ALL pending output before sending code
        # Read aggressively with longer timeout to get everything
        drained_total = 0
        try:
            for _ in range(50):  # Try up to 50 reads (5 seconds max)
                ready, _, _ = select.select([master_fd], [], [], 0.1)
                if not ready:
                    break
                data = os.read(master_fd, 16384)  # Larger buffer
                if not data:
                    break
                drained_total += len(data)
                decoded = data.decode('utf-8', errors='replace')
                logger.debug(f"Drained {len(data)} bytes: {decoded[:80]!r}...")
        except Exception as e:
            logger.debug(f"Drain exception: {e}")
        logger.info(f"Drained {drained_total} bytes of pending output before sending code")

        # Wait a moment for any remaining terminal output to settle
        time.sleep(0.5)

        # Drain again after the pause to catch any late output
        extra_drained = 0
        try:
            for _ in range(10):
                ready, _, _ = select.select([master_fd], [], [], 0.1)
                if not ready:
                    break
                data = os.read(master_fd, 16384)
                if not data:
                    break
                extra_drained += len(data)
        except:
            pass
        if extra_drained > 0:
            logger.info(f"Drained {extra_drained} more bytes after pause")

        # NOTE: Don't send ESC - it would cancel the input prompt!

        # Log terminal state
        try:
            attrs = termios.tcgetattr(master_fd)
            logger.debug(f"Terminal attrs: iflag={attrs[0]:#x}, oflag={attrs[1]:#x}, cflag={attrs[2]:#x}, lflag={attrs[3]:#x}")
        except Exception as e:
            logger.debug(f"Could not get terminal attrs: {e}")

        # Write the code to the PTY character by character
        # Ink/Node.js terminal UIs often need to see individual keystrokes
        logger.info(f"Typing code to PTY character by character (length={len(request.code)})")
        for i, char in enumerate(request.code):
            os.write(master_fd, char.encode())
            time.sleep(0.01)  # 10ms between characters (100 chars/sec typing speed)

        # Send Enter to submit - try both CR and LF
        # Different terminal modes/readline implementations handle these differently
        os.write(master_fd, b"\r")  # Carriage return (Enter key in raw mode)
        time.sleep(0.1)
        os.write(master_fd, b"\n")  # Line feed
        logger.info(f"Finished typing code, sent CR+LF")

        # Wait for CLI to process the code and write credentials
        # The CLI needs to make an API call to Anthropic to validate the code
        # It may exit silently after writing credentials, or show a new prompt
        output = b''
        start_time = time.time()
        creds_file = Path(config_dir) / ".credentials.json"
        default_creds = Path.home() / ".claude" / ".credentials.json"
        success_detected = False
        error_detected = False

        logger.info("Waiting for CLI to process code (checking for credentials file or process exit)...")

        while time.time() - start_time < 45:  # 45 second timeout for API call
            # Check if credentials file appeared (success!)
            if creds_file.exists():
                logger.info(f"Credentials file appeared at {creds_file}")
                success_detected = True
                break
            if default_creds.exists():
                # Check if it was modified recently (within last 60 seconds)
                try:
                    mtime = default_creds.stat().st_mtime
                    if time.time() - mtime < 60:
                        logger.info(f"Credentials file appeared at default location {default_creds}")
                        success_detected = True
                        break
                except:
                    pass

            # Read any available output (non-blocking)
            ready, _, _ = select.select([master_fd], [], [], 0.5)
            if ready:
                try:
                    data = os.read(master_fd, 4096)
                    if data:
                        output += data
                        decoded_chunk = data.decode('utf-8', errors='replace')
                        logger.debug(f"OAuth read {len(data)} bytes (total: {len(output)}): {decoded_chunk[:100]!r}")
                except OSError as e:
                    logger.debug(f"OAuth read OSError: {e}")

            # Check if process completed
            poll_result = proc.poll()
            if poll_result is not None:
                logger.info(f"OAuth process exited with code {poll_result}")
                # Read any remaining output
                try:
                    while True:
                        ready, _, _ = select.select([master_fd], [], [], 0.1)
                        if not ready:
                            break
                        data = os.read(master_fd, 4096)
                        if not data:
                            break
                        output += data
                except:
                    pass
                # Give a moment for file system to sync
                time.sleep(0.5)
                break

            # Check output for error indicators
            decoded = output.decode('utf-8', errors='ignore')
            clean = re.sub(r'\x1b\[[0-9;?]*[a-zA-Z]', '', decoded)
            if 'error' in clean.lower() or 'failed' in clean.lower() or 'invalid' in clean.lower():
                logger.warning("OAuth error indicator detected in output")
                error_detected = True
                break

        # Log final state
        elapsed = time.time() - start_time
        logger.info(f"OAuth wait complete: {len(output)} bytes in {elapsed:.1f}s, success={success_detected}, error={error_detected}")

        # Close PTY and wait for process
        try:
            os.close(master_fd)
        except:
            pass
        try:
            proc.wait(timeout=5)
        except:
            proc.terminate()

        # Log what files exist in config dir for debugging
        try:
            config_files = list(Path(config_dir).iterdir())
            logger.debug(f"Files in OAuth config dir: {[f.name for f in config_files]}")
        except Exception as e:
            logger.debug(f"Could not list config dir: {e}")

        # Parse output for token or check for credentials file
        # setup-token outputs the token to stdout, not to a file
        credentials_json = None
        creds_source = None

        # Clean output for parsing
        decoded_output = output.decode('utf-8', errors='ignore')
        clean_output = re.sub(r'\x1b\[[0-9;?]*[a-zA-Z]', '', decoded_output)  # CSI sequences
        clean_output = re.sub(r'\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)', '', clean_output)  # OSC sequences
        clean_output = re.sub(r'\x1b.', '', clean_output)  # Other escapes

        # Log full output for debugging (first 2000 chars, this is where token would be)
        logger.debug(f"OAuth full output (first 2000 chars): {clean_output[:2000]}")

        # Look for the actual OAuth token in output
        # Token format: sk-ant-oat01-... (about 90+ chars)
        token_patterns = [
            # sk-ant-oat01-xxx format (the actual OAuth token format)
            r'(sk-ant-oat01-[A-Za-z0-9_\-]+)',
            # Generic long token on its own line
            r'\n([A-Za-z0-9_\-]{50,})\n',
            # export CLAUDE_CODE_OAUTH_TOKEN=actualtoken
            r'CLAUDE_CODE_OAUTH_TOKEN=([A-Za-z0-9_\-\.]{50,})',
        ]

        for pattern in token_patterns:
            match = re.search(pattern, clean_output)
            if match:
                oauth_token = match.group(1)
                # Validate it's a real token (sk-ant-oat01- tokens are ~91 chars)
                if oauth_token and oauth_token.startswith('sk-ant-') and len(oauth_token) > 80:
                    # Store token in format that indicates it's an OAuth token for env var
                    # The setup-token output is meant for CLAUDE_CODE_OAUTH_TOKEN env var
                    credentials_json = json.dumps({
                        "oauthToken": oauth_token  # Raw token for CLAUDE_CODE_OAUTH_TOKEN env var
                    })
                    creds_source = "stdout_token"
                    logger.info(f"Extracted OAuth token from CLI output (length={len(oauth_token)})")
                    break

        # Fallback: check credentials file (some versions may write to file)
        if not credentials_json and creds_file.exists():
            credentials_json = creds_file.read_text()
            creds_source = "custom"
        elif not credentials_json and default_creds.exists():
            try:
                mtime = default_creds.stat().st_mtime
                if time.time() - mtime < 120:
                    credentials_json = default_creds.read_text()
                    creds_source = "default"
                    logger.warning(f"Credentials found at default location instead of {config_dir}")
            except:
                pass

        if credentials_json:
            # Validate it's proper JSON
            json.loads(credentials_json)

            # Clean up config directory
            shutil.rmtree(config_dir, ignore_errors=True)
            logger.debug(f"Cleaned up OAuth config dir: {config_dir}")

            logger.info(f"OAuth completed successfully (from {creds_source}) for user {request.user_id[:20]}...")
            return OAuthCompleteResponse(
                success=True,
                credentials_json=credentials_json,
                message="Login successful! Your credentials have been saved."
            )
        else:
            # Log debugging info - no token found and no credentials file
            logger.error(f"OAuth failed - no token in output and no credentials file")
            logger.error(f"Config dir: {config_dir}, creds_file exists: {creds_file.exists()}")
            logger.error(f"Output length: {len(output)} bytes, cleaned: {len(clean_output)} chars")
            # Log full output to help debug token format
            logger.error(f"OAuth output (first 1500 chars): {clean_output[:1500]}")
            logger.error(f"OAuth output (last 500 chars): {clean_output[-500:]}")

            # Clean up config directory
            shutil.rmtree(config_dir, ignore_errors=True)

            # Provide helpful error message
            if error_detected:
                return OAuthCompleteResponse(
                    success=False,
                    message="Authentication failed. The code may be invalid or expired. Please try again."
                )
            elif not output:
                return OAuthCompleteResponse(
                    success=False,
                    message="Authentication process did not respond. Please try starting login again."
                )
            else:
                return OAuthCompleteResponse(
                    success=False,
                    message="Authentication failed. Please check your code and try again."
                )

    except Exception as e:
        logger.error(f"OAuth completion error: {e}")
        # Clean up everything on error
        try:
            os.close(master_fd)
        except:
            pass
        try:
            proc.terminate()
        except:
            pass
        shutil.rmtree(config_dir, ignore_errors=True)
        return OAuthCompleteResponse(
            success=False,
            message=f"Authentication error: {e}"
        )


@app.delete("/v1/users/{user_id}")
async def delete_user(user_id: str):
    """
    Remove all stored credentials for a user (logout).

    This cleans up the user's credential directory used for Pro/Max authentication.
    Should be called when a user logs out from the bridge.
    """
    try:
        await credentials_manager.cleanup_user(user_id)
        logger.info(f"Cleaned up credentials for user: {user_id}")
        return {"status": "deleted", "user_id": user_id}
    except Exception as e:
        logger.error(f"Failed to cleanup user {user_id}: {e}")
        raise HTTPException(status_code=500, detail=f"Failed to cleanup user: {str(e)}")


if __name__ == "__main__":
    import uvicorn
    uvicorn.run(app, host="0.0.0.0", port=PORT)
