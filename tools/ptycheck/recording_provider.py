#!/usr/bin/env python3
"""A local provider that records every request it receives.

Each request is appended to the file named by HAL9001_REQUEST_LOG, one
JSON document per line. The reply is an ordinary streamed answer, so
the interface behaves exactly as it does against any other provider.

Usage:

    HAL9001_RECORDER_PORT=8265 \\
    HAL9001_REQUEST_LOG=/tmp/opencode/requests.jsonl \\
    python3 tools/ptycheck/recording_provider.py
"""
import json
import os
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

PORT = int(os.environ.get("HAL9001_RECORDER_PORT", "8265"))
LOG = os.environ.get("HAL9001_REQUEST_LOG", "/tmp/opencode/requests.jsonl")


class Handler(BaseHTTPRequestHandler):
    def log_message(self, *args):
        pass

    def record(self, request):
        with open(LOG, "a") as fh:
            fh.write(json.dumps(request) + "\n")
            fh.flush()

    def do_GET(self):
        if self.path.rstrip("/").endswith("/models"):
            body = json.dumps({"data": []}).encode()
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
        self.record(request)
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.send_header("Cache-Control", "no-cache")
        self.end_headers()
        for piece in ("recorded ", "request"):
            time.sleep(0.05)
            self.event({"choices": [{"delta": {"content": piece}, "index": 0}]})
        self.event({"choices": [{"delta": {}, "finish_reason": "stop"}]})


def main():
    server = ThreadingHTTPServer(("127.0.0.1", PORT), Handler)
    server.serve_forever()


if __name__ == "__main__":
    main()
