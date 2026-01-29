#!/usr/bin/env python3
"""
Analyze captured candy.ai traffic to understand the API structure.

Usage:
    python analyze_capture.py candy_captured.jsonl
"""

import json
import sys
from collections import defaultdict
from pathlib import Path


def analyze_capture(filepath: str):
    """Analyze captured traffic and print summary."""

    entries = []
    with open(filepath) as f:
        for line in f:
            if line.strip():
                entries.append(json.loads(line))

    print(f"\n{'='*60}")
    print(f"CANDY.AI TRAFFIC ANALYSIS")
    print(f"{'='*60}")
    print(f"Total entries captured: {len(entries)}\n")

    # Group by type
    by_type = defaultdict(list)
    for e in entries:
        by_type[e["type"]].append(e)

    print("Entry types:")
    for t, items in sorted(by_type.items()):
        print(f"  {t}: {len(items)}")

    # Analyze endpoints
    print(f"\n{'-'*60}")
    print("API ENDPOINTS")
    print(f"{'-'*60}")

    endpoints = defaultdict(lambda: {"methods": set(), "count": 0, "example_response": None})
    for e in entries:
        if e["type"] == "request":
            path = e.get("path", "").split("?")[0]
            endpoints[path]["methods"].add(e.get("method", "?"))
            endpoints[path]["count"] += 1
        elif e["type"] == "response":
            path = e.get("url", "").split("?")[0]
            for ep in endpoints:
                if ep in path:
                    if endpoints[ep]["example_response"] is None and e.get("body"):
                        endpoints[ep]["example_response"] = e["body"]

    for path, info in sorted(endpoints.items(), key=lambda x: -x[1]["count"]):
        methods = ", ".join(sorted(info["methods"]))
        print(f"\n  [{methods}] {path}")
        print(f"       Calls: {info['count']}")
        if info["example_response"]:
            resp = json.dumps(info["example_response"], indent=2)
            # Truncate long responses
            if len(resp) > 500:
                resp = resp[:500] + "\n       ... (truncated)"
            for line in resp.split("\n")[:20]:
                print(f"       {line}")

    # Analyze WebSocket messages
    ws_messages = [e for e in entries if e["type"] == "websocket"]
    if ws_messages:
        print(f"\n{'-'*60}")
        print("WEBSOCKET MESSAGES")
        print(f"{'-'*60}")

        client_msgs = [e for e in ws_messages if e["direction"] == "client_to_server"]
        server_msgs = [e for e in ws_messages if e["direction"] == "server_to_client"]

        print(f"  Client → Server: {len(client_msgs)}")
        print(f"  Server → Client: {len(server_msgs)}")

        # Show message structure examples
        print("\n  Example client messages:")
        for msg in client_msgs[:3]:
            content = json.dumps(msg.get("content", ""), indent=4)
            for line in content.split("\n")[:10]:
                print(f"    {line}")
            print()

        print("\n  Example server messages:")
        for msg in server_msgs[:3]:
            content = json.dumps(msg.get("content", ""), indent=4)
            for line in content.split("\n")[:10]:
                print(f"    {line}")
            print()

    # Analyze authentication
    print(f"\n{'-'*60}")
    print("AUTHENTICATION HEADERS")
    print(f"{'-'*60}")

    auth_headers = set()
    for e in entries:
        if e["type"] == "request":
            headers = e.get("headers", {})
            for h in headers:
                hl = h.lower()
                if any(x in hl for x in ["auth", "token", "cookie", "session", "bearer", "x-"]):
                    auth_headers.add(h)

    for h in sorted(auth_headers):
        print(f"  {h}")

    # Extract cookies
    print(f"\n{'-'*60}")
    print("COOKIES USED")
    print(f"{'-'*60}")

    all_cookies = set()
    for e in entries:
        if e["type"] == "request":
            cookies = e.get("cookies", {})
            all_cookies.update(cookies.keys())

    for c in sorted(all_cookies):
        print(f"  {c}")

    # Export summary for bridge development
    print(f"\n{'-'*60}")
    print("EXPORT FOR BRIDGE DEVELOPMENT")
    print(f"{'-'*60}")

    summary = {
        "endpoints": {path: {"methods": list(info["methods"]), "count": info["count"]}
                     for path, info in endpoints.items()},
        "auth_headers": list(auth_headers),
        "cookies": list(all_cookies),
        "websocket_url": None,
    }

    # Find WebSocket URL
    for e in entries:
        if e["type"] == "websocket_start":
            summary["websocket_url"] = e.get("url")
            break

    summary_path = Path(filepath).with_suffix(".summary.json")
    with open(summary_path, "w") as f:
        json.dump(summary, f, indent=2)
    print(f"  Summary written to: {summary_path}")


if __name__ == "__main__":
    if len(sys.argv) < 2:
        print("Usage: python analyze_capture.py <captured_traffic.jsonl>")
        sys.exit(1)

    analyze_capture(sys.argv[1])
