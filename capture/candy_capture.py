"""
mitmproxy addon to capture candy.ai traffic.

Usage:
    mitmweb -s candy_capture.py --set candy_output=./captured_traffic.jsonl

This script captures all requests/responses to candy.ai domains and saves them
in a structured JSONL format for protocol analysis.
"""

import json
import time
from datetime import datetime
from mitmproxy import ctx, http, websocket
from pathlib import Path


class CandyCapture:
    def __init__(self):
        self.output_file = None
        self.domains = ["candy.ai", "api.candy.ai", "cdn.candy.ai"]
        self.captured_count = 0

    def load(self, loader):
        loader.add_option(
            name="candy_output",
            typespec=str,
            default="./candy_captured.jsonl",
            help="Output file for captured traffic"
        )

    def configure(self, updates):
        if "candy_output" in updates:
            self.output_file = Path(ctx.options.candy_output)
            self.output_file.parent.mkdir(parents=True, exist_ok=True)
            ctx.log.info(f"Candy capture output: {self.output_file}")

    def _matches_domain(self, host: str) -> bool:
        """Check if host matches any candy.ai domain."""
        if not host:
            return False
        return any(d in host.lower() for d in self.domains)

    def _write_entry(self, entry: dict):
        """Write a captured entry to the output file."""
        if not self.output_file:
            return
        with open(self.output_file, "a") as f:
            f.write(json.dumps(entry, default=str) + "\n")
        self.captured_count += 1
        ctx.log.info(f"[candy] Captured #{self.captured_count}: {entry.get('type', 'unknown')} - {entry.get('url', entry.get('info', ''))[:80]}")

    def request(self, flow: http.HTTPFlow):
        """Capture outgoing requests."""
        if not self._matches_domain(flow.request.host):
            return

        entry = {
            "type": "request",
            "timestamp": datetime.utcnow().isoformat(),
            "method": flow.request.method,
            "url": flow.request.pretty_url,
            "host": flow.request.host,
            "path": flow.request.path,
            "headers": dict(flow.request.headers),
            "cookies": dict(flow.request.cookies),
            "content_type": flow.request.headers.get("content-type", ""),
        }

        # Capture body for non-GET requests
        if flow.request.method != "GET" and flow.request.content:
            try:
                if b"json" in flow.request.headers.get("content-type", "").encode():
                    entry["body"] = json.loads(flow.request.content)
                else:
                    entry["body_raw"] = flow.request.content.decode("utf-8", errors="replace")
            except Exception as e:
                entry["body_raw"] = flow.request.content.decode("utf-8", errors="replace")
                entry["body_parse_error"] = str(e)

        self._write_entry(entry)

    def response(self, flow: http.HTTPFlow):
        """Capture responses."""
        if not self._matches_domain(flow.request.host):
            return

        entry = {
            "type": "response",
            "timestamp": datetime.utcnow().isoformat(),
            "url": flow.request.pretty_url,
            "status_code": flow.response.status_code,
            "headers": dict(flow.response.headers),
            "content_type": flow.response.headers.get("content-type", ""),
        }

        # Capture response body
        if flow.response.content:
            try:
                content_type = flow.response.headers.get("content-type", "")
                if "json" in content_type:
                    entry["body"] = json.loads(flow.response.content)
                elif "text" in content_type or "javascript" in content_type:
                    body = flow.response.content.decode("utf-8", errors="replace")
                    # Truncate large text responses
                    if len(body) > 50000:
                        entry["body_truncated"] = body[:50000]
                        entry["body_full_size"] = len(body)
                    else:
                        entry["body"] = body
                else:
                    entry["body_size"] = len(flow.response.content)
                    entry["body_type"] = "binary"
            except Exception as e:
                entry["body_parse_error"] = str(e)

        self._write_entry(entry)

    def websocket_message(self, flow: http.HTTPFlow):
        """Capture WebSocket messages."""
        if not self._matches_domain(flow.request.host):
            return

        assert flow.websocket is not None
        message = flow.websocket.messages[-1]

        entry = {
            "type": "websocket",
            "timestamp": datetime.utcnow().isoformat(),
            "url": flow.request.pretty_url,
            "direction": "client_to_server" if message.from_client else "server_to_client",
            "is_text": message.is_text,
        }

        if message.is_text:
            try:
                entry["content"] = json.loads(message.text)
            except:
                entry["content"] = message.text
        else:
            entry["content_size"] = len(message.content)
            entry["content_hex"] = message.content[:500].hex() if len(message.content) <= 500 else message.content[:500].hex() + "..."

        self._write_entry(entry)

    def websocket_start(self, flow: http.HTTPFlow):
        """Log WebSocket connection start."""
        if not self._matches_domain(flow.request.host):
            return

        entry = {
            "type": "websocket_start",
            "timestamp": datetime.utcnow().isoformat(),
            "url": flow.request.pretty_url,
            "headers": dict(flow.request.headers),
        }
        self._write_entry(entry)

    def websocket_end(self, flow: http.HTTPFlow):
        """Log WebSocket connection end."""
        if not self._matches_domain(flow.request.host):
            return

        entry = {
            "type": "websocket_end",
            "timestamp": datetime.utcnow().isoformat(),
            "url": flow.request.pretty_url,
        }
        self._write_entry(entry)


addons = [CandyCapture()]
