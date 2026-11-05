#!/usr/bin/env python3
"""A local DuckDuckGo Lite stub with a stable organic-result fixture."""
import os
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import parse_qs

PORT = int(os.environ.get("HAL9001_FAKE_DDG_PORT", "8125"))

RESULTS = """<!DOCTYPE html><html><body><table>
<tr><td>1.</td><td><a rel="nofollow" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.com%2Fddg-1&amp;rut=x" class='result-link'>DDG result one</a></td></tr>
<tr><td>&nbsp;</td><td class='result-snippet'>First keyless excerpt.</td></tr>
<tr><td>&nbsp;</td><td><span class='link-text'>example.com/ddg-1</span></td></tr>
<tr><td>2.</td><td><a rel="nofollow" href="https://example.com/ddg-2" class='result-link'>DDG result two</a></td></tr>
<tr><td>&nbsp;</td><td class='result-snippet'>Second keyless excerpt.</td></tr>
<tr><td>3.</td><td><a rel="nofollow" href="https://example.com/ddg-3" class='result-link'>DDG result three</a></td></tr>
<tr><td>&nbsp;</td><td class='result-snippet'>Third keyless excerpt.</td></tr>
</table></body></html>"""
EMPTY = "<html><body><p>No results.</p></body></html>"
BLOCKED = "<html><body><p>If this persists, please email us.</p></body></html>"


class Handler(BaseHTTPRequestHandler):
    def log_message(self, *args):
        pass

    def send_html(self, status, body):
        data = body.encode()
        self.send_response(status)
        self.send_header("Content-Type", "text/html; charset=UTF-8")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def do_POST(self):
        length = int(self.headers.get("Content-Length", 0))
        raw = self.rfile.read(length).decode()
        query = parse_qs(raw).get("q", [""])[0]
        if "allfail" in query or "blocked" in query:
            self.send_html(403, BLOCKED)
            return
        if "ddgempty" in query:
            self.send_html(200, EMPTY)
            return
        if "malformed" in query:
            self.send_html(200, "<html><body>new layout</body></html>")
            return
        self.send_html(200, RESULTS)


if __name__ == "__main__":
    server = ThreadingHTTPServer(("127.0.0.1", PORT), Handler)
    print("listening on", PORT, flush=True)
    server.serve_forever()
