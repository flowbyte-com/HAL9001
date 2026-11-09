#!/usr/bin/env python3
"""A local provider that holds a request and records every one.

The held request waits for the client to go away: that is what an
operator's /stop looks like from the provider's side. Every request is
appended to the file named by HAL9001_REQUEST_LOG, one JSON document
per line, so the checks can see what actually left the machine.

Two modes:

    plain  the first request is held, every later request is answered;
    guard  the first request proposes a command, the second is held,
           the third proposes the same command again, and the fourth is
           answered.

The guard mode is what proves that a continuation never replays an
external side effect: the same command is proposed twice, and the
checks see that it ran only once.

Usage:

    HAL9001_HOLDING_PORT=8266 \\
    HAL9001_REQUEST_LOG=/tmp/opencode/holding-requests.jsonl \\
    python3 tools/ptycheck/holding_provider.py
"""
import json
import os
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

PORT = int(os.environ.get("HAL9001_HOLDING_PORT", "8266"))
LOG = os.environ.get("HAL9001_REQUEST_LOG", "/tmp/opencode/holding-requests.jsonl")
# Whether the first request is held. The checks that need an ordinary
# provider turn it off.
HOLD_FIRST = os.environ.get("HAL9001_HOLD_FIRST", "1") == "1"
# "plain" holds the first request; "guard" proposes a command, holds the
# second request and proposes the same command again.
MODE = os.environ.get("HAL9001_HOLDING_MODE", "plain")
ANSWER = os.environ.get("HAL9001_HOLDING_ANSWER", "the continued answer")
# The command the guard mode proposes. It must be the same string in
# both proposals: that is what identifies the operation.
COMMAND = os.environ.get("HAL9001_HOLDING_COMMAND", "echo guard")
TOOL = os.environ.get("HAL9001_HOLDING_TOOL", "bash")


def record(request):
    """Append the request to the log and return how many there are."""
    count = 0
    try:
        with open(LOG, "r", errors="replace") as fh:
            for line in fh:
                if line.strip():
                    count += 1
    except OSError:
        count = 0
    with open(LOG, "a") as fh:
        fh.write(json.dumps(request) + "\n")
        fh.flush()
    return count + 1


class Handler(BaseHTTPRequestHandler):
    def log_message(self, *args):
        pass

    def event(self, payload):
        self.wfile.write(("data: " + json.dumps(payload) + "\n\n").encode())
        self.wfile.flush()

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

    def do_POST(self):
        length = int(self.headers.get("Content-Length", 0))
        try:
            request = json.loads(self.rfile.read(length) or b"{}")
        except ValueError:
            request = {}
        count = record(request)
        if MODE == "guard":
            self.guard(count)
            return
        if count == 1 and HOLD_FIRST:
            self.hold()
            return
        self.answer()

    def hold(self):
        """Never answer: wait for the client to go away.

        This is what a stopped turn looks like from here. The client
        closes the connection when the operator stops the turn.
        """
        self.connection.settimeout(30)
        try:
            while self.connection.recv(4096):
                pass
        except OSError:
            pass

    def begin(self):
        """Open a streamed response."""
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.send_header("Cache-Control", "no-cache")
        self.end_headers()

    def answer(self):
        """Stream one short, complete visible answer."""
        self.begin()
        self.event({"choices": [{"delta": {"content": ANSWER}, "index": 0}]})
        self.event({"choices": [{"delta": {}, "finish_reason": "stop"}]})

    def propose_tool(self, name, arguments):
        """Stream one tool call whose arguments arrive in two pieces.

        The call is only a request. Whether it runs is the engine's
        decision, and the checks look at what really ran.
        """
        payload = json.dumps(arguments)
        half = len(payload) // 2
        self.begin()
        self.event({"choices": [{"delta": {"content": "Let me look."}, "index": 0}]})
        self.event({"choices": [{"delta": {"tool_calls": [{
            "index": 0,
            "id": "call_" + name,
            "type": "function",
            "function": {"name": name, "arguments": payload[:half]},
        }]}, "index": 0}]})
        self.event({"choices": [{"delta": {"tool_calls": [{
            "index": 0,
            "function": {"arguments": payload[half:]},
        }]}, "index": 0}]})
        self.event({"choices": [{"delta": {}, "finish_reason": "tool_calls"}]})

    def guard(self, count):
        """The sequence of the side-effect scenario.

        The first request proposes the command and it runs. The second
        request is held, so the operator can stop the turn with the
        command already done. The third request — the continuation —
        proposes the same command again: the engine must not run it a
        second time. The fourth request is answered.
        """
        if count == 1 or count == 3:
            self.propose_tool(TOOL, {"command": COMMAND})
            return
        if count == 2:
            self.hold()
            return
        self.answer()


def main():
    server = ThreadingHTTPServer(("127.0.0.1", PORT), Handler)
    server.daemon_threads = True
    server.serve_forever()


if __name__ == "__main__":
    main()
