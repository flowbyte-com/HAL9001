#!/usr/bin/env python3
"""A local provider that holds a request and records every one.

The held request waits for the client to go away: that is what an
operator's /stop looks like from the provider's side. Every request is
appended to the file named by HAL9001_REQUEST_LOG, one JSON document
per line, so the checks can see what actually left the machine.

Modes:

    plain   the first request is held, every later request is answered;
    guard   the first request proposes a command, the second is held,
            the third proposes the same command again, and the fourth is
            answered;
    fail    the first request fails with a server error, every later
            request is answered;
    release the first request waits until the release file appears,
            every later request is answered;
    release-stream
            like release, but the answer arrives in pieces, so the
            deltas of one answer reach the interface one by one;
    release-tool
            like release, but the first request proposes a tool call
            instead of answering;
    release-fail
            like release, but the first request fails with a server
            error instead of answering.

The guard mode is what proves that a continuation never replays an
external side effect: the same command is proposed twice, and the
checks see that it ran only once.

The fail mode is what proves that a failed exchange can be continued:
the operator's message stays unanswered, and /resume answers it.

The release mode is what lets a check decide when the answer is given:
the request waits for the file named by HAL9001_HOLDING_RELEASE to
appear, so the operator can open a panel while the answer is on its
way. The second request waits for HAL9001_HOLDING_RELEASE2 when that
name is set, so a check can hold the second request as well.

Usage:

    HAL9001_HOLDING_PORT=8266 \\
    HAL9001_REQUEST_LOG=/tmp/opencode/holding-requests.jsonl \\
    python3 tools/ptycheck/holding_provider.py
"""
import json
import os
import select
import time
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
# The answer of every request after the first one. It is the same text as
# ANSWER unless a check says otherwise, so the checks that only care
# about one exchange are unaffected.
ANSWER2 = os.environ.get("HAL9001_HOLDING_ANSWER2", ANSWER)
# The command the guard mode proposes. It must be the same string in
# both proposals: that is what identifies the operation.
COMMAND = os.environ.get("HAL9001_HOLDING_COMMAND", "echo guard")
# The file whose appearance releases the held request of the release
# mode. It is how a check says "now".
RELEASE = os.environ.get("HAL9001_HOLDING_RELEASE", "")
# The file whose appearance releases the second request.
RELEASE2 = os.environ.get("HAL9001_HOLDING_RELEASE2", "")
TOOL = os.environ.get("HAL9001_HOLDING_TOOL", "bash")


def tool_arguments(name):
    """The arguments of the proposed tool call."""
    if name == "read":
        return {"path": "panel-notes.txt"}
    return {"command": COMMAND}


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
        if MODE == "fail":
            self.fail(count)
            return
        if MODE.startswith("release"):
            self.release(count)
            return
        if count == 1 and HOLD_FIRST:
            self.hold()
            return
        self.answer()

    def await_release(self, path):
        """Wait until the release file appears, or the client goes away.

        The wait ends early when the socket closes: a turn that was
        stopped leaves the provider with a closed connection, and the
        answer would have nowhere to go.
        """
        if not path:
            return True
        deadline = time.monotonic() + 120
        while time.monotonic() < deadline:
            if os.path.exists(path):
                return True
            try:
                ready, _, _ = select.select([self.connection], [], [], 0.05)
            except OSError:
                return False
            if not ready:
                continue
            try:
                if not self.connection.recv(4096):
                    # The client is gone: the turn was stopped.
                    return False
            except OSError:
                return False
        return False

    def release(self, count):
        """Hold the request until its release file appears.

        The first request is the one the check holds: the operator can
        open a panel while the answer is on its way. The second request
        is held as well when a second release file is named, so a tool
        call can be seen before the answer that follows it.
        """
        if count == 1:
            if not self.await_release(RELEASE):
                return
            if MODE == "release-tool":
                self.propose_tool(TOOL, tool_arguments(TOOL), ANSWER)
                return
            if MODE == "release-stream":
                self.stream_answer(ANSWER)
                return
            if MODE == "release-fail":
                self.fail_request()
                return
            self.answer(ANSWER)
            return
        if count == 2 and RELEASE2:
            if not self.await_release(RELEASE2):
                return
            self.answer(ANSWER2)
            return
        self.answer(ANSWER2)

    def fail(self, count):
        """Fail the first request with a server error."""
        if count == 1:
            self.fail_request()
            return
        self.answer()

    def fail_request(self):
        """Answer with a server error."""
        body = b"the provider is unwell"
        self.send_response(503)
        self.send_header("Content-Type", "text/plain")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

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

    def answer(self, text=None):
        """Stream one short, complete visible answer."""
        if text is None:
            text = ANSWER
        self.begin()
        self.event({"choices": [{"delta": {"content": text}, "index": 0}]})
        self.event({"choices": [{"delta": {}, "finish_reason": "stop"}]})

    def stream_answer(self, text):
        """Stream the answer in pieces.

        The deltas arrive one by one, so the interface has to keep up
        with a half-written answer instead of one finished block.
        """
        self.begin()
        step = max(1, len(text) // 4)
        for i in range(0, len(text), step):
            self.event({"choices": [{"delta": {"content": text[i:i + step]}, "index": 0}]})
            time.sleep(0.15)
        self.event({"choices": [{"delta": {}, "finish_reason": "stop"}]})

    def propose_tool(self, name, arguments, content=None):
        """Stream one tool call whose arguments arrive in two pieces.

        The call is only a request. Whether it runs is the engine's
        decision, and the checks look at what really ran.
        """
        if content is None:
            content = "Let me look."
        payload = json.dumps(arguments)
        half = len(payload) // 2
        self.begin()
        self.event({"choices": [{"delta": {"content": content}, "index": 0}]})
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
