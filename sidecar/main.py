#!/usr/bin/env python3
"""
Perplexity API Sidecar for mautrix-perplexity bridge.

Provides HTTP API for Go bridge to communicate with Perplexity using the official SDK.
"""

import asyncio
import json
import logging
import os
import time
from contextlib import asynccontextmanager
from dataclasses import dataclass, field
from typing import AsyncIterator, Dict, Optional

from fastapi import FastAPI, HTTPException
from fastapi.responses import StreamingResponse
from pydantic import BaseModel
from prometheus_client import Counter, Histogram, Gauge, generate_latest, CONTENT_TYPE_LATEST
from starlette.responses import Response

# Perplexity SDK imports
from perplexity import Perplexity, AsyncPerplexity

# Configure logging
logging.basicConfig(
    level=logging.INFO,
    format='%(asctime)s - %(name)s - %(levelname)s - %(message)s'
)
logger = logging.getLogger(__name__)

# Configuration
PORT = int(os.getenv("PERPLEXITY_SIDECAR_PORT", "8090"))
QUERY_TIMEOUT = int(os.getenv("PERPLEXITY_SIDECAR_QUERY_TIMEOUT", "300"))  # 5 minutes default
DEFAULT_MODEL = os.getenv("PERPLEXITY_SIDECAR_MODEL", "sonar")
SYSTEM_PROMPT = os.getenv("PERPLEXITY_SIDECAR_SYSTEM_PROMPT", "You are a helpful AI assistant.")
SESSION_TIMEOUT = int(os.getenv("PERPLEXITY_SIDECAR_SESSION_TIMEOUT", "3600"))  # 1 hour

# Input validation limits
MAX_MESSAGE_LENGTH = 100000  # ~100k chars
MAX_PORTAL_ID_LENGTH = 256

# Prometheus metrics
REQUESTS_TOTAL = Counter('perplexity_sidecar_requests_total', 'Total requests', ['endpoint', 'status'])
REQUEST_DURATION = Histogram('perplexity_sidecar_request_duration_seconds', 'Request duration')
ACTIVE_SESSIONS = Gauge('perplexity_sidecar_active_sessions', 'Number of active sessions')
TOKENS_USED = Counter('perplexity_sidecar_tokens_total', 'Total tokens used', ['type'])


@asynccontextmanager
async def lifespan(app: FastAPI):
    """Lifespan context manager for startup and shutdown events."""
    # Startup
    await session_manager.start()
    logger.info(f"Perplexity sidecar starting on port {PORT}")
    logger.info(f"Default model: {DEFAULT_MODEL}")
    logger.info("Perplexity sidecar ready")

    yield

    # Shutdown
    await session_manager.stop()
    logger.info("Perplexity sidecar stopped")


# FastAPI app
app = FastAPI(
    title="Perplexity API Sidecar",
    description="HTTP API for mautrix-perplexity bridge",
    version="1.0.0",
    lifespan=lifespan
)


@dataclass
class Session:
    """Represents a conversation session with message history."""
    session_id: str
    portal_id: str
    created_at: float = field(default_factory=time.time)
    last_used: float = field(default_factory=time.time)
    message_count: int = 0
    input_tokens: int = 0
    output_tokens: int = 0
    # Message history for multi-turn conversations
    # Each entry is a dict with 'role' and 'content' keys
    messages: list = field(default_factory=list)

    def add_message(self, role: str, content: str | list) -> None:
        """Add a message to the history."""
        self.messages.append({"role": role, "content": content})
        self.message_count += 1

    def clear_history(self) -> None:
        """Clear the message history."""
        self.messages = []
        self.message_count = 0


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


class WebSearchOptions(BaseModel):
    """Perplexity web search options."""
    search_domain_filter: Optional[list[str]] = None
    search_recency_filter: Optional[str] = None  # "day", "week", "month", "year"


class ChatRequest(BaseModel):
    """Request body for chat endpoint."""
    portal_id: str
    api_key: str  # Per-user Perplexity API key
    user_id: Optional[str] = None  # Matrix user ID (for logging)
    message: str  # Text message
    content: Optional[list[ContentBlock]] = None  # Structured content (for images)
    system_prompt: Optional[str] = None
    model: Optional[str] = None
    stream: bool = False
    web_search_options: Optional[WebSearchOptions] = None
    max_tokens: Optional[int] = None
    temperature: Optional[float] = None
    conversation_mode: bool = False  # Enable multi-turn history (default: off)

    def has_images(self) -> bool:
        """Check if this request contains images."""
        if not self.content:
            return False
        return any(block.type == "image" for block in self.content)

    def validate_input(self) -> None:
        """Validate input fields. Raises HTTPException on invalid input."""
        if not self.portal_id or len(self.portal_id) > MAX_PORTAL_ID_LENGTH:
            raise HTTPException(
                status_code=400,
                detail=f"Invalid portal_id: must be 1-{MAX_PORTAL_ID_LENGTH} characters"
            )
        if not all(c.isalnum() or c in '_-:!.' for c in self.portal_id):
            raise HTTPException(
                status_code=400,
                detail="Invalid portal_id: contains invalid characters"
            )
        if not self.api_key:
            raise HTTPException(status_code=400, detail="API key is required")
        if not self.message and not (self.content and len(self.content) > 0):
            raise HTTPException(status_code=400, detail="Message cannot be empty")
        if self.message and len(self.message) > MAX_MESSAGE_LENGTH:
            raise HTTPException(
                status_code=400,
                detail=f"Message too long: {len(self.message)} chars (max {MAX_MESSAGE_LENGTH})"
            )


class UsageInfo(BaseModel):
    """Token usage information."""
    input_tokens: int = 0
    output_tokens: int = 0
    total_tokens: int = 0


class SearchResult(BaseModel):
    """Search result from Perplexity."""
    title: Optional[str] = None
    url: Optional[str] = None
    date: Optional[str] = None


class ChatResponse(BaseModel):
    """Response body for chat endpoint."""
    portal_id: str
    session_id: str
    response: str
    model: str
    tokens_used: Optional[int] = None
    usage: Optional[UsageInfo] = None
    search_results: Optional[list[SearchResult]] = None


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
                await asyncio.sleep(60)
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
        import uuid
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
                }
            return None


# Global session manager
session_manager = SessionManager()


def build_user_content(request: ChatRequest) -> str | list:
    """Build user message content from request."""
    if request.has_images() and request.content:
        # Multimodal message with images
        content_parts = []
        for block in request.content:
            if block.type == "text" and block.text:
                content_parts.append({"type": "text", "text": block.text})
            elif block.type == "image" and block.source:
                content_parts.append({
                    "type": "image_url",
                    "image_url": {
                        "url": f"data:{block.source.media_type};base64,{block.source.data}"
                    }
                })
        return content_parts
    else:
        # Text-only message
        return request.message


def build_messages(request: ChatRequest, session: Optional[Session] = None) -> list[dict]:
    """Build messages array for Perplexity API, including conversation history."""
    messages = []

    # Add system prompt if provided
    system_prompt = request.system_prompt or SYSTEM_PROMPT
    if system_prompt:
        messages.append({"role": "system", "content": system_prompt})

    # Add conversation history from session (if any)
    if session and session.messages:
        messages.extend(session.messages)

    # Build and add current user message
    user_content = build_user_content(request)
    messages.append({"role": "user", "content": user_content})

    return messages


@dataclass
class QueryResult:
    """Result of a Perplexity API query."""
    response_text: str = ""
    model: str = ""
    input_tokens: int = 0
    output_tokens: int = 0
    search_results: list = field(default_factory=list)


async def run_query(request: ChatRequest, session: Optional[Session] = None) -> QueryResult:
    """Run a query against Perplexity API."""
    result = QueryResult()

    # Build messages with conversation history
    messages = build_messages(request, session)

    # Determine model
    model = request.model or DEFAULT_MODEL
    result.model = model

    # Build API parameters
    params = {
        "model": model,
        "messages": messages,
    }

    if request.max_tokens:
        params["max_tokens"] = request.max_tokens
    if request.temperature is not None:
        params["temperature"] = request.temperature

    # Add web search options if provided
    if request.web_search_options:
        if request.web_search_options.search_domain_filter:
            params["search_domain_filter"] = request.web_search_options.search_domain_filter
        if request.web_search_options.search_recency_filter:
            params["search_recency_filter"] = request.web_search_options.search_recency_filter

    # Create async client with user's API key
    async with AsyncPerplexity(api_key=request.api_key) as client:
        response = await client.chat.completions.create(**params)

        # Extract response content
        if response.choices and len(response.choices) > 0:
            choice = response.choices[0]
            if choice.message and choice.message.content:
                result.response_text = choice.message.content

        # Extract model used
        if response.model:
            result.model = response.model

        # Extract usage
        if response.usage:
            result.input_tokens = response.usage.prompt_tokens or 0
            result.output_tokens = response.usage.completion_tokens or 0

        # Extract search results if available
        if hasattr(response, 'citations') and response.citations:
            for citation in response.citations:
                result.search_results.append({
                    "url": citation if isinstance(citation, str) else getattr(citation, 'url', None),
                })

    return result


async def run_query_stream(request: ChatRequest, session: Optional[Session] = None) -> AsyncIterator[dict]:
    """Run a streaming query against Perplexity API."""
    # Build messages with conversation history
    messages = build_messages(request, session)

    # Determine model
    model = request.model or DEFAULT_MODEL

    # Build API parameters
    params = {
        "model": model,
        "messages": messages,
        "stream": True,
    }

    if request.max_tokens:
        params["max_tokens"] = request.max_tokens
    if request.temperature is not None:
        params["temperature"] = request.temperature

    # Add web search options if provided
    if request.web_search_options:
        if request.web_search_options.search_domain_filter:
            params["search_domain_filter"] = request.web_search_options.search_domain_filter
        if request.web_search_options.search_recency_filter:
            params["search_recency_filter"] = request.web_search_options.search_recency_filter

    # Send session info first
    yield {"type": "session", "model": model}

    # Create async client with user's API key
    async with AsyncPerplexity(api_key=request.api_key) as client:
        stream = await client.chat.completions.create(**params)

        async for chunk in stream:
            if chunk.choices and len(chunk.choices) > 0:
                choice = chunk.choices[0]
                if choice.delta and choice.delta.content:
                    yield {"type": "text", "content": choice.delta.content}

                # Check for finish
                if choice.finish_reason:
                    yield {"type": "finish", "reason": choice.finish_reason}

    yield {"type": "done"}


@app.get("/health")
async def health():
    """Health check endpoint."""
    return {
        "status": "healthy",
        "sessions": len(session_manager.sessions),
        "authenticated": True,  # Always true - auth is per-request with API key
    }


@app.get("/metrics")
async def metrics():
    """Prometheus metrics endpoint."""
    return Response(content=generate_latest(), media_type=CONTENT_TYPE_LATEST)


class TestAuthRequest(BaseModel):
    """Request body for auth test endpoint."""
    user_id: str
    api_key: str


class TestAuthResponse(BaseModel):
    """Response body for auth test endpoint."""
    success: bool
    message: str


@app.post("/v1/auth/test", response_model=TestAuthResponse)
async def test_auth(request: TestAuthRequest):
    """
    Test user API key by making a minimal Perplexity API call.
    """
    if not request.user_id or not request.api_key:
        raise HTTPException(status_code=400, detail="user_id and api_key required")

    # Validate API key format
    if not request.api_key.startswith("pplx-"):
        return TestAuthResponse(
            success=False,
            message="Invalid API key format: Perplexity API keys start with 'pplx-'"
        )

    try:
        # Make a minimal test query
        async with AsyncPerplexity(api_key=request.api_key) as client:
            response = await client.chat.completions.create(
                model="sonar",
                messages=[{"role": "user", "content": "Say OK"}],
                max_tokens=10,
            )

            if response.choices and len(response.choices) > 0:
                logger.info(f"API key validated successfully for user {request.user_id[:20]}...")
                return TestAuthResponse(success=True, message="API key validated successfully")
            else:
                return TestAuthResponse(success=False, message="API key validation failed - no response")

    except Exception as e:
        logger.error(f"API key validation failed for user {request.user_id[:20]}...: {e}")
        error_msg = str(e)
        if "401" in error_msg or "unauthorized" in error_msg.lower():
            return TestAuthResponse(success=False, message="Invalid API key")
        if "402" in error_msg or "payment" in error_msg.lower():
            return TestAuthResponse(success=False, message="API key valid but insufficient credits")
        return TestAuthResponse(success=False, message=f"API key validation failed: {error_msg}")


@app.post("/v1/chat", response_model=ChatResponse)
async def chat(request: ChatRequest):
    """
    Send a message to Perplexity and get a response.
    Only maintains conversation history if conversation_mode is enabled.
    """
    start_time = time.time()

    # Validate input
    request.validate_input()

    # Get or create session
    session = await session_manager.get_or_create(request.portal_id)

    try:
        # Build user content for history (only used if conversation_mode enabled)
        user_content = build_user_content(request)

        # Run query with timeout
        # Only pass session for history if conversation_mode is enabled
        session_for_history = session if request.conversation_mode else None
        async with asyncio.timeout(QUERY_TIMEOUT):
            query_result = await run_query(request, session_for_history)

        # Only save to history if conversation_mode is enabled
        if request.conversation_mode:
            session.add_message("user", user_content)
            session.add_message("assistant", query_result.response_text)

        # Update session token stats (always track for billing purposes)
        session.input_tokens += query_result.input_tokens
        session.output_tokens += query_result.output_tokens

        # Update Prometheus metrics
        if query_result.input_tokens > 0:
            TOKENS_USED.labels(type='input').inc(query_result.input_tokens)
        if query_result.output_tokens > 0:
            TOKENS_USED.labels(type='output').inc(query_result.output_tokens)

        REQUESTS_TOTAL.labels(endpoint='/v1/chat', status='success').inc()
        REQUEST_DURATION.observe(time.time() - start_time)

        # Build response
        total_tokens = query_result.input_tokens + query_result.output_tokens
        usage_info = UsageInfo(
            input_tokens=query_result.input_tokens,
            output_tokens=query_result.output_tokens,
            total_tokens=total_tokens,
        )

        # Convert search results
        search_results = None
        if query_result.search_results:
            search_results = [
                SearchResult(url=sr.get("url"))
                for sr in query_result.search_results
            ]

        return ChatResponse(
            portal_id=request.portal_id,
            session_id=session.session_id,
            response=query_result.response_text,
            model=query_result.model,
            tokens_used=total_tokens if total_tokens > 0 else None,
            usage=usage_info,
            search_results=search_results,
        )

    except asyncio.TimeoutError:
        logger.error(f"Query timed out for portal {request.portal_id}")
        REQUESTS_TOTAL.labels(endpoint='/v1/chat', status='error').inc()
        raise HTTPException(status_code=504, detail=f"Request timed out after {QUERY_TIMEOUT}s")
    except Exception as e:
        logger.error(f"Error processing chat request for portal {request.portal_id}: {e}", exc_info=True)
        REQUESTS_TOTAL.labels(endpoint='/v1/chat', status='error').inc()
        raise HTTPException(status_code=500, detail=f"Error: {str(e)}")


@app.post("/v1/chat/stream")
async def chat_stream(request: ChatRequest):
    """
    Send a message to Perplexity and stream the response.
    Only maintains conversation history if conversation_mode is enabled.
    """
    # Validate input
    request.validate_input()

    # Get or create session
    session = await session_manager.get_or_create(request.portal_id)

    # Build user content for history (only used if conversation_mode enabled)
    user_content = build_user_content(request)

    # Only pass session for history if conversation_mode is enabled
    session_for_history = session if request.conversation_mode else None

    async def generate() -> AsyncIterator[str]:
        response_text = []
        try:
            async with asyncio.timeout(QUERY_TIMEOUT):
                async for event in run_query_stream(request, session_for_history):
                    yield f"data: {json.dumps(event)}\n\n"
                    # Collect response text for history
                    if event.get("type") == "text" and event.get("content"):
                        response_text.append(event["content"])

            # Only save to history if conversation_mode is enabled
            if request.conversation_mode:
                session.add_message("user", user_content)
                if response_text:
                    session.add_message("assistant", "".join(response_text))

        except asyncio.TimeoutError:
            logger.error(f"Stream query timed out for portal {request.portal_id}")
            yield f"data: {json.dumps({'type': 'error', 'message': f'Request timed out after {QUERY_TIMEOUT}s'})}\n\n"
        except Exception as e:
            logger.error(f"Error in stream for portal {request.portal_id}: {e}", exc_info=True)
            yield f"data: {json.dumps({'type': 'error', 'message': str(e)})}\n\n"

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


if __name__ == "__main__":
    import uvicorn
    uvicorn.run(app, host="0.0.0.0", port=PORT)
