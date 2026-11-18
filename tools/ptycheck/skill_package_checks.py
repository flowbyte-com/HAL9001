#!/usr/bin/env python3
"""PTY checks for packaged MPM skills.

HAL reads skills from MPM's command line, and the answer to a read may
carry package identity: the skill was projected from a multi-file
package, and only its entrypoint was loaded. These checks drive the real
binary in a real terminal against a fixture that speaks the documented
`mpm call mpm_skills` contract, so no MPM build and no real skill store
are involved.

What is proved:

  * /skills lists a packaged skill and names its package, its
    entrypoint and the fact that only the entrypoint was loaded;
  * the panel never claims the whole package was loaded, and never shows
    a supporting file's path or content;
  * /skill pins the packaged skill, and the pin is reported;
  * a store that does not answer degrades gracefully: HAL still starts,
    /skills explains, and the conversation continues;
  * the interface stays in 444 throughout.

Usage:

    go build -o /tmp/opencode/hal9001 ./cmd/hal9001
    HAL9001_BINARY=/tmp/opencode/hal9001 python3 tools/ptycheck/skill_package_checks.py
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

FAILURES = []
BINARY = os.environ.get("HAL9001_BINARY", "/tmp/opencode/hal9001")
PORT = int(os.environ.get("HAL9001_SKILLPKG_PORT", "8455"))
ANSWER = "SKILLPKG-ANSWER-4a91"


class RecordingProvider:
    """A local OpenAI-compatible provider that records each request.

    It answers with one streamed reply, so the turn completes and the
    interface can report what was loaded. No live endpoint is used.
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
                body = json.dumps({"data": [{"id": "fixture-model"}]}).encode()
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

    def last(self):
        with self.lock:
            return self.requests[-1] if self.requests else None

# The fixture binary answers list and read with one packaged skill.
FIXTURE = """#!/bin/sh
case "$*" in
*'"action":"list"'*|*'"action": "list"'*)
  printf '%s\\n' '{"success":true,"count":1,"scope":"all","skills":[{"ID":"skill:review-pack-v2.0.0","Name":"review-pack","Version":"2.0.0","WhenToUse":"Reviewing a diff. Triggers: \\"review\\", \\"diff\\".","IsGlobal":false,"Weight":5}]}'
  ;;
*'"action":"read"'*|*'"action": "read"'*)
  printf '%s\\n' '{"success":true,"id":"skill:review-pack-v2.0.0","name":"review-pack","version":"2.0.0","when_to_use":"Reviewing a diff.","domain":"code","constraints":["Never approve untested code."],"body":"# Review pack\\n\\nRead the entrypoint, then the checklist.","package":{"skill_id":"skill:review-pack-v2.0.0","entrypoint":"SKILL.md","entrypoint_sha256":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","package_hash":"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","files":[{"path":"checklists/go.md","sha256":"sha256:cccc","size_bytes":2048,"role":"reference"},{"path":"scripts/lint.sh","sha256":"sha256:dddd","size_bytes":512,"role":"script"}]}}'
  ;;
*)
  printf '%s\\n' '{"success":false,"error":"unknown action"}'
  ;;
esac
"""


def check(name, ok, detail=""):
    print(("[ok  ] " if ok else "[FAIL] ") + name + ("" if ok else "  " + str(detail)))
    if not ok:
        FAILURES.append(name)


def write_config(path, endpoint):
    config = {
        "version": 2,
        "endpoints": [{
            "id": "fixture", "name": "Fixture",
            "protocol": "openai-compatible",
            "base_url": endpoint,
            "credential_env": "FIXTURE_KEY",
            "models": [{"id": "fixture-model", "context_mode": "auto"}],
        }],
        "active": {"endpoint": "fixture", "model": "fixture-model"},
    }
    with open(path, "w") as fh:
        json.dump(config, fh)


def open_session(root, name, mpm_bin):
    home = os.path.join(root, name, "home")
    store = os.path.join(home, "sessions")
    workspace = os.path.join(home, "workspace")
    os.makedirs(store, exist_ok=True)
    os.makedirs(workspace, exist_ok=True)
    path = os.path.join(home, "config.json")
    write_config(path, "http://127.0.0.1:%d/v1" % PORT)
    os.environ["HAL9001_MPM_BIN"] = mpm_bin
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
    root = tempfile.mkdtemp(prefix="hal-skillpkg-", dir=base)
    os.environ["FIXTURE_KEY"] = "synthetic-fixture-key"
    fixture = os.path.join(root, "mpm-fixture")
    with open(fixture, "w") as fh:
        fh.write(FIXTURE)
    os.chmod(fixture, 0o755)
    try:
        provider = RecordingProvider(PORT)
        provider.start()

        # 1. A turn routed to the packaged skill carries its entrypoint
        #    guidance, and only that.
        session = open_session(root, "pkg", fixture)
        try:
            session.send(b"Write a review of this diff.\r")
            ok = session.until(lambda: ANSWER in session.screen(), 25.0)
            check("the turn completed", ok, session.screen()[-600:])
            last = provider.last()
            check("the provider received the request", last is not None,
                  len(provider.requests))
            body = json.dumps(last or {})
            check("the entrypoint guidance reached the request",
                  "Read the entrypoint, then the checklist." in body, body[-800:])
            check("the request says only the entrypoint was loaded",
                  "Only the entrypoint guidance was loaded" in body, body[-800:])
            check("no supporting-file content reached the request",
                  "scripts/lint.sh" not in body and "#!/bin/sh" not in body, body[-800:])

            session.send(b"/skills\r")
            session.wait(1.5)
            screen = session.screen()
            check("the skills panel opens", "Skills (Esc back)" in screen, screen[-800:])
            check("the packaged skill is listed", "review-pack" in screen, screen[-800:])
            check("the package is named",
                  "skill:review-pack-v2.0.0" in screen, screen[-1200:])
            check("the entrypoint is named", "SKILL.md" in screen, screen[-1200:])
            check("the panel says only the entrypoint was loaded",
                  "entrypoint only" in screen, screen[-1200:])
            check("the panel never claims the whole package was loaded",
                  "package loaded" not in screen, screen[-1200:])
            check("no supporting-file path is shown",
                  "checklists/go.md" not in screen and "scripts/lint.sh" not in screen,
                  screen[-1200:])
            check("the interface stays in 444", "444 READ ONLY" in screen, screen[-400:])

            # 2. The packaged skill can be pinned by the operator.
            session.send(b"\x1b")
            session.wait(0.5)
            session.send(b"/skill review-pack\r")
            session.wait(1.2)
            screen = session.screen()
            check("the packaged skill can be pinned",
                  "Pinned skill: review-pack" in screen, screen[-600:])
            check("pinning does not change the authority", "444 READ ONLY" in screen,
                  screen[-400:])
            session.send(b"/skill auto\r")
            session.wait(1.0)
            check("routing can be restored",
                  "routed automatically again" in session.screen(), session.screen()[-400:])
        finally:
            session.close()
            provider.stop()

        # 3. A store that cannot be reached degrades gracefully.
        missing = os.path.join(root, "no-such-mpm")
        session = open_session(root, "missing", missing)
        try:
            session.send(b"/skills\r")
            session.wait(1.5)
            screen = session.screen()
            check("an unavailable store is explained",
                  "not available" in screen or "no skills" in screen.lower(), screen[-900:])
            check("HAL keeps running without the store",
                  "444 READ ONLY" in screen, screen[-400:])
            session.send(b"\x1b")
            session.wait(0.4)
            session.send(b"hello\r")
            session.wait(1.2)
            check("the conversation still works", "Enter send" in session.screen(),
                  session.screen()[-400:])
        finally:
            session.close()
    finally:
        os.environ.pop("HAL9001_MPM_BIN", None)
        os.environ.pop("FIXTURE_KEY", None)
        shutil.rmtree(root, ignore_errors=True)

    print()
    if FAILURES:
        print("%d FAILURES" % len(FAILURES))
        return 1
    print("ALL SKILL PACKAGE PTY CHECKS PASSED")
    return 0


if __name__ == "__main__":
    sys.exit(main())
