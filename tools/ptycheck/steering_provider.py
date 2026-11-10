#!/usr/bin/env python3
"""A local OpenAI-compatible stub for the steering and queue checks.

The stub does two things the ordinary stub cannot.

It can hold a turn open: a prompt whose last user message carries the
``[[hold]]`` marker is answered only after a delay, so the operator has
time to write while HAL is working.

It records every request it receives: the request that was already sent
must not be rewritten when the operator steers, and a message that was
written while HAL was working must not appear in that request. The
recorded requests are the evidence.

Usage:

    HAL9001_FAKE_PORT=8131 HAL9001_STEER_LOG=/tmp/requests.jsonl \
        python3 tools/ptycheck/steering_provider.py
"""
import json
import os
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

PORT = int(os.environ.get("HAL9001_FAKE_PORT", "8131"))

# The log the requests are recorded in.
LOG = os.environ.get("HAL9001_STEER_LOG", "")

# How long a held turn is held open, in seconds.
HOLD = float(os.environ.get("HAL9001_STEER_HOLD", "4.0"))

# The marker that asks for a held turn.
HOLD_MARKER = "[[hold]]"

# The lock that keeps the log in request order.
LOG_LOCK = threading.Lock()


def last_user_message(request):
    """Return the content of the last user message of a request."""
    last = ""
    for message in request.get("messages", []):
        if message.get("role") == "user":
            last = message.get("content", "")
    return last


def record(request):
    """Write one request to the log, in the order it arrived."""
    if not LOG:
        return
    line = json.dumps({
        "messages": request.get("messages", []),
        "last_user": last_user_message(request),
    }, sort_keys=True)
    with LOG_LOCK:
        with open(LOG, "a") as fh:
            fh.write(line + "\n")
            fh.flush()


def read_log():
    """Return the recorded requests."""
    if not LOG or not os.path.isfile(LOG):
        return []
    out = []
    with open(LOG) as fh:
        for line in fh:
            line = line.strip()
            if not line:
                continue
            try:
                out.append(json.loads(line))
            except ValueError:
                continue
    return out


class Handler(BaseHTTPRequestHandler):
    def log_message(self, *args):
        pass

    def event(self, payload):
        self.wfile.write(("data: " + json.dumps(payload) + "\n\n").encode())
        self.wfile.flush()

    def do_POST(self):
        length = int(self.headers.get("Content-Length", 0))
        try:
            request = json.loads(self.rfile.read(length))
        except Exception:
            request = {}
        record(request)
        last = last_user_message(request)

        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.send_header("Cache-Control", "no-cache")
        self.end_headers()

        if HOLD_MARKER in last:
            # The turn is held open, so the operator can write while HAL
            # is working.
            time.sleep(HOLD)

        # The answer names the message the model received, so the screen
        # shows which message the turn actually carried.
        text = last.replace(HOLD_MARKER, "").strip()
        for piece in ("GOT ", text[:44]):
            time.sleep(0.05)
            self.event({"choices": [{"delta": {"content": piece}, "index": 0}]})
        self.event({"choices": [{"delta": {}, "finish_reason": "stop"}]})
        self.event({
            "choices": [],
            "usage": {
                "prompt_tokens": 120, "completion_tokens": 80, "total_tokens": 200,
            },
        })
        self.wfile.write(b"data: [DONE]\n\n")
        self.wfile.flush()


if __name__ == "__main__":
    server = ThreadingHTTPServer(("127.0.0.1", PORT), Handler)
    print("listening on", PORT, flush=True)
    server.serve_forever()
