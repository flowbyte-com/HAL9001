#!/usr/bin/env python3
"""A local Exa /search stub for the provider-chain checks."""
import json
import os
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

PORT = int(os.environ.get("HAL9001_FAKE_EXA_PORT", "8124"))


class Handler(BaseHTTPRequestHandler):
    def log_message(self, *args):
        pass

    def send_json(self, status, payload):
        data = json.dumps(payload).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def do_POST(self):
        length = int(self.headers.get("Content-Length", 0))
        try:
            request = json.loads(self.rfile.read(length))
        except Exception:
            request = {}
        if self.path != "/search":
            self.send_json(404, {"error": "not found"})
            return
        if not self.headers.get("Authorization", "").startswith("Bearer "):
            self.send_json(401, {"error": "Unauthorized"})
            return
        query = str(request.get("query", ""))
        if "ddgfallback" in query or "allfail" in query:
            self.send_json(429, {"error": "Rate limit exceeded", "tag": "RATE_LIMIT"})
            return
        if "authfail" in query:
            self.send_json(401, {"error": "Unauthorized", "tag": "AUTH"})
            return
        if "serverfail" in query:
            self.send_json(503, {"error": "Service unavailable", "tag": "UNAVAILABLE"})
            return
        if "exaempty" in query:
            self.send_json(200, {"results": [], "costDollars": {"total": 0.001}})
            return
        results = []
        for i in range(1, 4):
            results.append({
                "title": "Exa result %d" % i,
                "url": "https://example.com/exa-%d" % i,
                "publishedDate": "2026-10-01T00:00:00.000Z",
                "highlights": ["Exa excerpt %d from the live index." % i],
            })
        self.send_json(200, {"results": results, "costDollars": {"total": 0.005}})


if __name__ == "__main__":
    server = ThreadingHTTPServer(("127.0.0.1", PORT), Handler)
    print("listening on", PORT, flush=True)
    server.serve_forever()
