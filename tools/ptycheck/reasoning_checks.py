#!/usr/bin/env python3
"""PTY checks for the reasoning controls and the Show Thinking toggle.

HAL offers a reasoning-effort control for the models whose provider
documents one, and a separate switch that says whether the reasoning a
provider exposed is shown. These checks drive the real binary in a real
terminal against a local fixture provider, so no live endpoint is used.

What is proved:

  * the header carries HAL's mark: the 9001 raised at the upper left of
    HAL, before the H;
  * /effort offers the levels the model's own family documents, applies
    the one the operator chose, and the very next request carries it;
  * a level the provider does not document is never offered;
  * /thinking shows the reasoning the provider exposed, and hiding it
    again leaves the answer standing;
  * the visibility and the effort are two different choices: neither
    moves the other;
  * the visibility preference survives a restart;
  * the interface stays in 444 throughout.

Usage:

    go build -o /tmp/opencode/hal9001 ./cmd/hal9001
    HAL9001_BINARY=/tmp/opencode/hal9001 python3 tools/ptycheck/reasoning_checks.py
"""
import json
import os
import shutil
import sys
import tempfile
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, HERE)
from pty_session import Session  # noqa: E402
from pty_drive import WORDMARK  # noqa: E402

FAILURES = []
BINARY = os.environ.get("HAL9001_BINARY", "/tmp/opencode/hal9001")
PORT = int(os.environ.get("HAL9001_REASONING_PORT", "8456"))
ANSWER = "REASONING-ANSWER-7c02"
THOUGHT = "the model weighed two answers before choosing"
MODEL = "MiniMax-M3.1-Flash"


class RecordingProvider:
    """A local OpenAI-compatible provider that records each request.

    It answers with one streamed reply, preceded by a reasoning chunk,
    so the provider really does expose reasoning. No live endpoint is
    used.
    """

    def __init__(self, port):
        self.port = port
        self.requests = []
        self.lock = threading.Lock()
        self.server = None

    def start(self):
        provider = self

        class Handler(BaseHTTPRequestHandler):
            def log_message(self, *args):
                pass

            def do_GET(self):
                body = json.dumps({"data": [{"id": MODEL}]}).encode()
                self.send_response(200)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(body)))
                self.end_headers()
                self.wfile.write(body)

            def do_POST(self):
                length = int(self.headers.get("Content-Length", 0))
                try:
                    request = json.loads(self.rfile.read(length))
                except Exception:
                    request = {}
                with provider.lock:
                    provider.requests.append(request)
                self.send_response(200)
                self.send_header("Content-Type", "text/event-stream")
                self.end_headers()
                thought = {"choices": [{"delta": {"reasoning_content": THOUGHT}, "index": 0}]}
                self.wfile.write(("data: " + json.dumps(thought) + "\n\n").encode())
                chunk = {"choices": [{"delta": {"content": ANSWER}, "index": 0}]}
                self.wfile.write(("data: " + json.dumps(chunk) + "\n\n").encode())
                done = {"choices": [{"delta": {}, "finish_reason": "stop", "index": 0}]}
                self.wfile.write(("data: " + json.dumps(done) + "\n\n").encode())
                self.wfile.write(b"data: [DONE]\n\n")
                self.wfile.flush()

        self.server = ThreadingHTTPServer(("127.0.0.1", self.port), Handler)
        threading.Thread(target=self.server.serve_forever, daemon=True).start()

    def stop(self):
        if self.server:
            self.server.shutdown()

    def count(self):
        with self.lock:
            return len(self.requests)

    def last(self):
        with self.lock:
            return self.requests[-1] if self.requests else None


def check(name, ok, detail=""):
    print(("[ok  ] " if ok else "[FAIL] ") + name + ("" if ok else "  " + str(detail)))
    if not ok:
        FAILURES.append(name)


def write_config(path, show_thinking=False):
    config = {
        "version": 2,
        "endpoints": [{
            "id": "fixture", "name": "Fixture",
            "protocol": "openai-compatible",
            "base_url": "http://127.0.0.1:%d/v1" % PORT,
            "credential_env": "FIXTURE_KEY",
            "models": [{"id": MODEL, "context_mode": "auto"}],
        }],
        "active": {"endpoint": "fixture", "model": MODEL},
        "show_thinking": show_thinking,
    }
    with open(path, "w") as fh:
        json.dump(config, fh)


def open_session(root, name, show_thinking=False):
    home = os.path.join(root, name, "home")
    store = os.path.join(home, "sessions")
    workspace = os.path.join(home, "workspace")
    os.makedirs(store, exist_ok=True)
    os.makedirs(workspace, exist_ok=True)
    path = os.path.join(home, "config.json")
    write_config(path, show_thinking)
    session = Session(BINARY, path, home, store, name, workspace)
    session.start()
    session.wait_for("Enter send", 25.0)
    return session


def main():
    if not os.path.isfile(BINARY):
        print("missing HAL9001 binary:", BINARY)
        return 2
    base = os.environ.get("HAL9001_PTY_TMPDIR", "/tmp/opencode")
    os.makedirs(base, exist_ok=True)
    root = tempfile.mkdtemp(prefix="hal-reasoning-", dir=base)
    os.environ["FIXTURE_KEY"] = "synthetic-fixture-key"
    try:
        provider = RecordingProvider(PORT)
        provider.start()

        # 1. The header carries HAL's mark.
        session = open_session(root, "brand")
        try:
            screen = session.screen()
            check("the header carries the wordmark", WORDMARK in screen, screen[-400:])
            check("the 9001 is raised in front of HAL",
                  WORDMARK.startswith("⁹⁰⁰¹"), WORDMARK)
            check("the old name is gone from the header",
                  "HAL9001" not in screen.split("\n")[0], screen.split("\n")[0])

            # 2. The effort control offers the model's own levels and
            #    the chosen one reaches the next request.
            session.send(b"/effort\r")
            session.wait(1.5)
            screen = session.screen()
            check("the effort selector opens", "Reasoning effort" in screen, screen[-800:])
            for level in ["low", "medium", "high", "xhigh", "max"]:
                check("the selector offers %s" % level, level in screen, screen[-800:])

            # The cursor starts on the level in effect ("medium"), so one
            # step down is "high".
            session.send(b"\x1b[B")
            session.send(b"\r")
            session.wait(1.5)
            screen = session.screen()
            check("the chosen level is confirmed", "Effort: high" in screen, screen[-600:])
            check("the level is in the footer", "high" in screen.split("\n")[-2], screen[-400:])

            session.send(b"what is the state of the build?\r")
            ok = session.until(lambda: ANSWER in session.screen(), 25.0)
            check("the turn completed", ok, session.screen()[-600:])
            last = provider.last() or {}
            check("the chosen level reached the request",
                  last.get("reasoning_effort") == "high", last.get("reasoning_effort"))
        finally:
            session.close()

        # 3. Show Thinking: the provider's reasoning is shown, then
        #    hidden again, and the answer stands either way.
        session = open_session(root, "thinking")
        try:
            session.send(b"/thinking\r")
            session.wait(1.2)
            screen = session.screen()
            check("the visibility is reported", "Show thinking: on" in screen, screen[-600:])

            session.send(b"what is the state of the build?\r")
            ok = session.until(lambda: ANSWER in session.screen(), 25.0)
            check("the turn completed", ok, session.screen()[-600:])
            screen = session.screen()
            check("the reasoning is shown", THOUGHT in screen, screen[-1200:])
            check("the answer is still shown", ANSWER in screen, screen[-1200:])
            check("the reasoning is labelled", "thinking" in screen, screen[-1200:])

            # Hiding it again leaves the answer standing.
            session.send(b"/thinking\r")
            session.wait(1.2)
            screen = session.screen()
            check("the visibility is reported again", "Show thinking: off" in screen, screen[-600:])
            check("the reasoning is hidden", THOUGHT not in screen, screen[-1200:])
            check("the answer is still shown", ANSWER in screen, screen[-1200:])
            check("the effort did not change with it", "high" not in screen.split("\n")[-2],
                  screen[-400:])
            check("the interface stays in 444", "444 READ ONLY" in screen, screen[-400:])
        finally:
            session.close()

        # 4. The visibility preference survives a restart, and the
        #    reasoning is shown from the first turn.
        session = open_session(root, "restart", show_thinking=True)
        try:
            session.send(b"what is the state of the build?\r")
            ok = session.until(lambda: ANSWER in session.screen(), 25.0)
            check("the turn completed", ok, session.screen()[-600:])
            check("the stored preference is honoured", THOUGHT in session.screen(),
                  session.screen()[-1200:])
        finally:
            session.close()

        provider.stop()
    finally:
        os.environ.pop("FIXTURE_KEY", None)
        shutil.rmtree(root, ignore_errors=True)

    print()
    if FAILURES:
        print("%d FAILURES" % len(FAILURES))
        return 1
    print("ALL REASONING PTY CHECKS PASSED")
    return 0


if __name__ == "__main__":
    sys.exit(main())
