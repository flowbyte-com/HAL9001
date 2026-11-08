#!/usr/bin/env python3
"""A local provider that answers with a hostile model catalogue.

The listing carries every trick a broken or hostile endpoint can use:

* an identifier that is an ANSI colour sequence;
* an identifier with surrounding whitespace;
* the same identifier twice;
* an identifier with an embedded newline;
* an identifier with a bell;
* an identifier that is far too long;
* a display name with an ANSI sequence, a bell, a newline and an OSC
  hyperlink.

HAL must show the catalogue without letting any of it reach the
terminal: no escape sequence, no forged row, and the identifiers that
are legitimate must survive exactly.

The chat endpoint answers normally, so the interface can be driven past
the catalogue.

Usage:

    HAL9001_HOSTILE_PORT=8263 python3 tools/ptycheck/hostile_provider.py
"""
import json
import os
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

PORT = int(os.environ.get("HAL9001_HOSTILE_PORT", "8263"))

# The identifiers that must never reach the interface.
POISONED_IDS = (
    "\x1b[38;5;196mred-id",      # an ANSI colour sequence
    " padded-id ",               # surrounding whitespace
    "newline-id\nsecond-line",   # an embedded newline
    "bell-id\x07",               # a bell
    "very-long-id-" + "x" * 400, # far too long
)

# The name that tries to rewrite the screen and to forge a row.
POISONED_LABEL = (
    "Model\x1b[38;5;196mRed\x07\n"
    "FORGED ROW\x1b]8;;http://evil.example\x07"
)

# The identifiers that must survive, byte for byte.
GOOD_IDS = ("good-model", "vendor/Model-V2:latest")


def catalogue():
    """The hostile listing, as the endpoint would send it."""
    data = [{"id": "good-model", "display_name": "Good Model"}]
    for poisoned in POISONED_IDS:
        data.append({"id": poisoned})
    data.append({"id": "good-model", "display_name": "Good Model"})
    data.append({"id": "label-hostile", "display_name": POISONED_LABEL})
    data.append({"id": "vendor/Model-V2:latest", "display_name": "Exact Identifier"})
    return {"data": data, "has_more": False}


class Handler(BaseHTTPRequestHandler):
    def log_message(self, *args):
        pass

    def do_GET(self):
        if self.path.rstrip("/").endswith("/models"):
            body = json.dumps(catalogue()).encode()
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)
            return
        self.send_response(404)
        self.end_headers()

    def event(self, payload):
        self.wfile.write(("data: " + json.dumps(payload) + "\n\n").encode())
        self.wfile.flush()

    def do_POST(self):
        length = int(self.headers.get("Content-Length", 0))
        try:
            request = json.loads(self.rfile.read(length))
        except Exception:
            request = {}
        last = ""
        for message in request.get("messages", []):
            if message.get("role") == "user":
                last = message.get("content", "")
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.send_header("Cache-Control", "no-cache")
        self.end_headers()
        for piece in ("hostile catalogue ", "answered"):
            time.sleep(0.05)
            self.event({"choices": [{"delta": {"content": piece}, "index": 0}]})
        self.event({"choices": [{"delta": {}, "finish_reason": "stop"}]})


def main():
    server = ThreadingHTTPServer(("127.0.0.1", PORT), Handler)
    server.serve_forever()


if __name__ == "__main__":
    main()
