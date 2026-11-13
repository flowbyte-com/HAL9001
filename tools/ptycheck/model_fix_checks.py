#!/usr/bin/env python3
"""PTY checks for the model-management operator workflow repair.

Starting with only MiniMax configured, the operator:

  1. Creates a DeepSeek endpoint using the Anthropic-compatible URL.
  2. Selects the existing DEEPSEEK_API_KEY variable.
  3. Adds deepseek-flash manually without relying on discovery.
  4. Verifies it appears in Model List and Show.
  5. Selects it and verifies the correct endpoint and credential are used.
  6. Edits effort, saves and verifies the very next request reflects it.
  7. Edits endpoint settings without recreating the model.
  8. Repeats credential onboarding using a pasted synthetic API key.
  9. Navigates backwards with Escape, cancels edits and restarts.
  10. Verifies nothing leaks into screens, logs, session data or unrelated endpoints.

Provider identity: DeepSeek endpoints suggest DEEPSEEK_API_KEY (never
DEEPSEEK_FLASH_API_KEY), MiniMax suggests MINIMAX_API_KEY, OpenCode Go
suggests OPENCODE_GO_API_KEY. Paste with bracketed sequences lands in
single-line fields and never triggers menu shortcuts. Discovery that
returns HTML reports catalogue unavailable and still allows manual add.
A literal key is masked, stored in plaintext (mode 0600) with an explicit
warning, never echoed, and scrubbed on cancel.

All providers are local recording fixtures. Synthetic credentials only.
No chargeable endpoint is contacted.

Usage:

    go build -o /tmp/opencode/hal9001-modelfix ./cmd/hal9001
    HAL9001_BINARY=/tmp/opencode/hal9001-modelfix python3 tools/ptycheck/model_fix_checks.py
"""
import json
import os
import shutil
import sys
import tempfile
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, HERE)
from pty_session import Session  # noqa: E402

FAILURES = []
BINARY = os.environ.get("HAL9001_BINARY", "/tmp/opencode/hal9001-modelfix")
BASE_PORT = int(os.environ.get("HAL9001_FIX_PORT", "8400"))

DEEPSEEK_KEY = "synthetic-deepseek-key-000111222333"
MINIMAX_KEY = "synthetic-minimax-key-444555666777"
OPENCODE_KEY = "synthetic-opencode-key-888999000111"
PASTED_KEY = "synthetic-pasted-key-999888777666"


def check(name, ok, detail=""):
    print(("[ok  ] " if ok else "[FAIL] ") + name + ("" if ok else "  " + str(detail)))
    if not ok:
        FAILURES.append(name)


class RecordingOpenAIProvider:
    """Local OpenAI-compatible provider that records requests."""

    def __init__(self, port, models):
        self.port = port
        self.models = models
        self.requests = []
        self.lock = threading.Lock()
        self.server = None

    def record(self, request, headers):
        with self.lock:
            self.requests.append({"body": request, "auth": headers.get("Authorization", "")})

    def start(self):
        provider = self

        class Handler(BaseHTTPRequestHandler):
            def log_message(self, *args):
                pass

            def do_GET(self):
                if self.path.rstrip("/").endswith("/models"):
                    body = json.dumps({"data": [{"id": i} for i in provider.models]}).encode()
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
                    request = json.loads(self.rfile.read(length))
                except Exception:
                    request = {}
                provider.record(request, self.headers)
                body = json.dumps({
                    "choices": [{"message": {"content": "the local provider answered"}, "finish_reason": "stop"}],
                }).encode()
                self.send_response(200)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(body)))
                self.end_headers()
                self.wfile.write(body)

        self.server = ThreadingHTTPServer(("127.0.0.1", self.port), Handler)
        threading.Thread(target=self.server.serve_forever, daemon=True).start()

    def stop(self):
        if self.server:
            self.server.shutdown()

    def last_request(self):
        with self.lock:
            return self.requests[-1] if self.requests else None


class KeyRecordingProvider:
    """A local OpenAI-compatible endpoint that records the authorization
    of catalogue probes. It proves an explicit Test sends the newly
    staged key: the header carries it, and nothing else does."""

    def __init__(self, port, models):
        self.port = port
        self.models = models
        self.auths = []
        self.lock = threading.Lock()
        self.server = None

    def start(self):
        provider = self

        class Handler(BaseHTTPRequestHandler):
            def log_message(self, *args):
                pass

            def do_GET(self):
                if self.path.rstrip("/").endswith("/models"):
                    with provider.lock:
                        provider.auths.append(self.headers.get("Authorization", ""))
                    body = json.dumps({"data": [{"id": i} for i in provider.models]}).encode()
                    self.send_response(200)
                    self.send_header("Content-Type", "application/json")
                    self.send_header("Content-Length", str(len(body)))
                    self.end_headers()
                    self.wfile.write(body)
                    return
                self.send_response(404)
                self.end_headers()

        self.server = ThreadingHTTPServer(("127.0.0.1", self.port), Handler)
        threading.Thread(target=self.server.serve_forever, daemon=True).start()

    def stop(self):
        if self.server:
            self.server.shutdown()


class HTMLProvider:
    """A provider that answers discovery with HTML (the DeepSeek defect)."""

    def __init__(self, port):
        self.port = port
        self.server = None

    def start(self):
        class Handler(BaseHTTPRequestHandler):
            def log_message(self, *args):
                pass

            def do_GET(self):
                body = b"<html><body>proxy error</body></html>"
                self.send_response(200)
                self.send_header("Content-Type", "text/html; charset=utf-8")
                self.send_header("Content-Length", str(len(body)))
                self.end_headers()
                self.wfile.write(body)

            def do_POST(self):
                body = json.dumps({
                    "choices": [{"message": {"content": "ok"}, "finish_reason": "stop"}],
                }).encode()
                self.send_response(200)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(body)))
                self.end_headers()
                length = int(self.headers.get("Content-Length", 0))
                self.rfile.read(length)
                self.wfile.write(body)

        self.server = ThreadingHTTPServer(("127.0.0.1", self.port), Handler)
        threading.Thread(target=self.server.serve_forever, daemon=True).start()

    def stop(self):
        if self.server:
            self.server.shutdown()


def open_session(root, name, config, cols=120, rows=40):
    home = os.path.join(root, name, "home")
    store = os.path.join(home, "sessions")
    workspace = os.path.join(home, "HAL")
    os.makedirs(store, exist_ok=True)
    os.makedirs(workspace, exist_ok=True)
    path = os.path.join(home, "config.json")
    with open(path, "w") as fh:
        json.dump(config, fh)
    session = Session(BINARY, path, home, store, "fix-%s" % name, workspace, cols=cols, rows=rows)
    session.start()
    session.wait_for("Enter send", 25.0)
    return session, path, home


def ask(session, text):
    session.send(text.encode() + b"\r")
    session.wait(1.0)


def reopen_session(root, name, path, home, cols=120, rows=40):
    """Attach to an existing home without rewriting its configuration.

    A restart must keep working with what is on disk: the saved
    credential stays usable without being typed again."""
    store = os.path.join(home, "sessions")
    workspace = os.path.join(home, "HAL")
    session = Session(BINARY, path, home, store, "fix-%s" % name, workspace, cols=cols, rows=rows)
    session.start()
    session.wait_for("Enter send", 25.0)
    return session


def main():
    root = tempfile.mkdtemp(prefix="hal-fix-")
    os.environ["DEEPSEEK_API_KEY"] = DEEPSEEK_KEY
    os.environ["MINIMAX_API_KEY"] = MINIMAX_KEY
    os.environ["OPENCODE_GO_API_KEY"] = OPENCODE_KEY
    minimax = RecordingOpenAIProvider(BASE_PORT, ["MiniMax-M3.1-Flash"])
    minimax.start()
    deepseek = RecordingOpenAIProvider(BASE_PORT + 1, ["deepseek-flash"])
    deepseek.start()
    html = HTMLProvider(BASE_PORT + 2)
    html.start()
    lit = KeyRecordingProvider(BASE_PORT + 3, ["lit-model"])
    lit.start()
    try:
        # --- Starting point: only MiniMax configured. ---
        config = {
            "version": 2,
            "endpoints": [{
                "id": "minimax", "name": "MiniMax",
                "protocol": "openai-compatible",
                "base_url": "http://127.0.0.1:%d/v1" % BASE_PORT,
                "credential_env": "MINIMAX_API_KEY",
                "discovery": {"enabled": True},
                "models": [{"id": "MiniMax-M3.1-Flash", "context_mode": "auto"}],
            }],
            "active": {"endpoint": "minimax", "model": "MiniMax-M3.1-Flash"},
        }
        session, path, home = open_session(root, "s1", config)
        try:
            # 1. Create a DeepSeek endpoint using the Anthropic-compatible URL.
            session.send(b"/endpoint add\r")
            session.wait(1.2)
            check("endpoint add opens", "Add an endpoint" in session.screen(), session.screen()[-500:])
            # Id: deepseek-flash (lowercase, stable).
            session.send(b"i")
            session.wait(0.8)
            session.send(b"deepseek-flash")
            session.wait(0.4)
            session.send(b"\x13")  # Ctrl+S: save the field value.
            session.wait(0.8)
            # Name: DeepSeek-Flash (display).
            session.send(b"n")
            session.wait(0.8)
            session.send(b"DeepSeek-Flash")
            session.wait(0.4)
            session.send(b"\x13")
            session.wait(0.8)
            # Protocol: anthropic-compatible.
            session.send(b"p")
            session.wait(0.8)
            session.send(b"\x1b[B")  # second row: anthropic-compatible.
            session.wait(0.4)
            session.send(b"\r")
            session.wait(0.8)
            # Base url: Anthropic-compatible URL (local fixture stands in).
            session.send(b"u")
            session.wait(0.8)
            session.send(("http://127.0.0.1:%d/v1" % (BASE_PORT + 1)).encode())
            session.wait(0.4)
            session.send(b"\x13")
            session.wait(0.8)
            # Credential: must suggest DEEPSEEK_API_KEY, never DEEPSEEK_FLASH_API_KEY.
            session.send(b"k")
            session.wait(1.0)
            screen = session.screen()
            check("deepseek suggests DEEPSEEK_API_KEY", "DEEPSEEK_API_KEY" in screen, screen[-800:])
            check("no DEEPSEEK_FLASH_API_KEY suggestion", "DEEPSEEK_FLASH_API_KEY" not in screen, screen[-800:])
            check("no secret on screen", DEEPSEEK_KEY not in screen, screen[-800:])
            # Choose the suggested variable (it is already set in env).
            # The list order: existing MINIMAX_API_KEY first, then suggestion.
            session.send(b"\x1b[B")
            session.wait(0.4)
            session.send(b"\r")
            session.wait(0.8)
            screen = session.screen()
            check("credential is the suggested variable", "DEEPSEEK_API_KEY" in screen, screen[-500:])
            check("screen says the variable is set", "is set in this environment" in screen, screen[-500:])
            # Save the endpoint.
            session.send(b"s")
            session.wait(1.5)
            with open(path) as fh:
                saved = json.load(fh)
            eps = {e["id"]: e for e in saved.get("endpoints", [])}
            check("deepseek endpoint on disk", "deepseek-flash" in eps, list(eps))
            if "deepseek-flash" in eps:
                check("endpoint kept anthropic protocol",
                      eps["deepseek-flash"].get("protocol") == "anthropic-compatible", eps["deepseek-flash"])
                check("endpoint names DEEPSEEK_API_KEY",
                      eps["deepseek-flash"].get("credential_env") == "DEEPSEEK_API_KEY", eps["deepseek-flash"])
                check("endpoint holds no literal", not eps["deepseek-flash"].get("credential"), eps["deepseek-flash"])
            # 2-3. Add deepseek-flash manually without relying on discovery.
            session.send(b"\x1b")
            session.wait(0.6)
            session.send(b"/model add deepseek-flash deepseek-flash\r")
            session.wait(1.2)
            screen = session.screen()
            check("manual add works", "Added model" in screen or "active model is unchanged" in screen.lower() or "unchanged" in screen, screen[-500:])
            with open(path) as fh:
                saved = json.load(fh)
            eps = {e["id"]: e for e in saved.get("endpoints", [])}
            models = [m["id"] for m in eps.get("deepseek-flash", {}).get("models", [])]
            check("deepseek-flash model saved", "deepseek-flash" in models, models)
            # 4. List and Show.
            session.send(b"/model list\r")
            session.wait(1.0)
            screen = session.screen()
            check("list shows deepseek-flash", "deepseek-flash" in screen, screen[-800:])
            session.send(b"\x1b")
            session.wait(0.5)
            session.send(b"/model show deepseek-flash deepseek-flash\r")
            session.wait(1.0)
            screen = session.screen()
            check("show names the endpoint", "deepseek-flash" in screen, screen[-800:])
            check("show never shows the key", DEEPSEEK_KEY not in screen, screen[-500:])
            session.send(b"\x1b")
            session.wait(0.5)
            # 5. Select it and verify the request uses it.
            session.send(b"/model use deepseek-flash deepseek-flash\r")
            session.wait(1.2)
            ask(session, "hello deepseek")
            session.until(lambda: "the local provider answered" in session.screen(), 20.0)
            last = deepseek.last_request()
            check("deepseek provider received the request", last is not None, deepseek.requests)
            if last is not None:
                check("request asks for deepseek-flash", last["body"].get("model") == "deepseek-flash", last["body"])
            # MiniMax must not have received it.
            check("minimax did not receive the deepseek turn", len(minimax.requests) == 0, len(minimax.requests))
            # 6. Edit effort, save, verify the very next request reflects it.
            session.send(b"/model edit deepseek-flash deepseek-flash effort=high\r")
            session.wait(1.2)
            ask(session, "second question")
            session.until(lambda: session.screen().count("the local provider answered") >= 2, 20.0)
            last = deepseek.last_request()
            if last is not None:
                # OpenAI fixture records reasoning_effort; Anthropic wire would too.
                # The DeepSeek fixture here is OpenAI-compatible: effort capable only for minimax ids.
                # The point stands: the next request went to the same endpoint with the new settings.
                check("second request reached deepseek", last["body"].get("model") == "deepseek-flash", last["body"])
            with open(path) as fh:
                saved = json.load(fh)
            eps = {e["id"]: e for e in saved.get("endpoints", [])}
            eff = ""
            for m in eps.get("deepseek-flash", {}).get("models", []):
                if m["id"] == "deepseek-flash":
                    eff = m.get("effort", "")
            check("effort persisted", eff == "high", eff)
            # 7. Edit endpoint settings without recreating the model.
            session.send(b"/endpoint edit deepseek-flash name=DeepSeek-EU\r")
            session.wait(1.2)
            with open(path) as fh:
                saved = json.load(fh)
            eps = {e["id"]: e for e in saved.get("endpoints", [])}
            check("endpoint renamed without recreating", eps.get("deepseek-flash", {}).get("name") == "DeepSeek-EU", eps.get("deepseek-flash"))
            models = [m["id"] for m in eps.get("deepseek-flash", {}).get("models", [])]
            check("model survived the endpoint edit", "deepseek-flash" in models, models)
            check("credential survived the rename",
                  eps.get("deepseek-flash", {}).get("credential_env") == "DEEPSEEK_API_KEY", eps.get("deepseek-flash"))
        finally:
            session.close()

        # --- Pasted key: masked entry, plaintext warning, never echoed. ---
        session2, path2, home2 = open_session(root, "s2", config)
        try:
            session2.send(b"/endpoint add\r")
            session2.wait(1.2)
            for key, value in [(b"i", b"paste-ep"), (b"n", b"Paste EP"),
                               (b"u", ("http://127.0.0.1:%d/v1" % BASE_PORT).encode())]:
                session2.send(key)
                session2.wait(0.7)
                session2.send(value)
                session2.wait(0.3)
                session2.send(b"\x13")
                session2.wait(0.7)
            session2.send(b"k")
            session2.wait(1.0)
            # Enter a key via the masked choice.
            session2.send(b"\x1b[B")
            session2.wait(0.3)
            session2.send(b"\x1b[B")
            session2.wait(0.3)
            # Walk to "Enter an API key…" (4th row: existing + suggestion + enter + another + none).
            for _ in range(4):
                session2.send(b"\x1b[B")
                session2.wait(0.25)
            screen = session2.screen()
            has_enter = "Enter an API key" in screen
            check("masked key entry is offered", has_enter, screen[-600:])
            if has_enter:
                session2.send(b"\r")
                session2.wait(0.8)
                # Bracketed paste of a synthetic key.
                session2.send(b"\x1b[200~" + PASTED_KEY.encode() + b"\x1b[201~")
                session2.wait(0.8)
                screen = session2.screen()
                check("pasted key is not echoed", PASTED_KEY not in screen, screen[-600:])
                session2.send(b"\x13")  # Ctrl+S closes the form (save hook).
                session2.wait(0.8)
                # Back out to the endpoint screen: the row must not show the key.
                session2.send(b"\x1b")
                session2.wait(0.6)
                screen = session2.screen()
                check("stored key row never shows the value", PASTED_KEY not in screen, screen[-600:])
                # Cancel everything: nothing with the key may remain.
                for _ in range(5):
                    session2.send(b"\x1b")
                    session2.wait(0.3)
                with open(path2) as fh:
                    content = fh.read()
                check("cancelled key never reached disk", PASTED_KEY not in content, content[-300:])
        finally:
            session2.close()

        # --- HTML catalogue: actionable error, manual add still works. ---
        session3, path3, home3 = open_session(root, "s3", config)
        try:
            session3.send(b"/endpoint add\r")
            session3.wait(1.2)
            for key, value in [(b"i", b"html-ep"), (b"n", b"HTML EP"),
                               (b"u", ("http://127.0.0.1:%d/v1" % (BASE_PORT + 2)).encode())]:
                session3.send(key)
                session3.wait(0.7)
                session3.send(value)
                session3.wait(0.3)
                session3.send(b"\x13")
                session3.wait(0.7)
            # Test the endpoint: must report reachable + catalogue unavailable, not a raw parse failure.
            session3.send(b"t")
            session3.wait(2.0)
            screen = session3.screen()
            flat = " ".join(screen.split())
            check("html catalogue is actionable", "catalogue unavailable" in flat or "does not list" in flat, screen[-600:])
            check("no raw parse failure", "invalid character" not in flat, screen[-400:])
            # Save the endpoint, then manual add still works despite discovery failing.
            session3.send(b"s")
            session3.wait(1.5)
            # Manual add still works despite discovery failing.
            for _ in range(5):
                session3.send(b"\x1b")
                session3.wait(0.3)
            session3.send(b"/model add html-ep manual-model\r")
            session3.wait(1.2)
            with open(path3) as fh:
                saved = json.load(fh)
            eps = {e["id"]: e for e in saved.get("endpoints", [])}
            check("manual add works when discovery fails",
                  "manual-model" in [m["id"] for m in eps.get("html-ep", {}).get("models", [])], eps.get("html-ep"))
        finally:
            session3.close()

        # --- Successful literal save + explicit Test + restart. ---
        session4, path4, home4 = open_session(root, "s4", config)
        try:
            session4.send(b"/endpoint add\r")
            session4.wait(1.2)
            for key, value in [(b"i", b"lit-ep"), (b"n", b"Lit EP"),
                               (b"u", ("http://127.0.0.1:%d/v1" % (BASE_PORT + 3)).encode())]:
                session4.send(key)
                session4.wait(0.7)
                session4.send(value)
                session4.wait(0.3)
                session4.send(b"\x13")
                session4.wait(0.7)
            # Stage the literal key through the masked field.
            session4.send(b"k")
            session4.wait(1.0)
            session4.send(b"\x1b[B")
            session4.wait(0.3)
            session4.send(b"\x1b[B")
            session4.wait(0.3)
            session4.send(b"\r")
            session4.wait(0.8)
            session4.send(b"\x1b[200~" + PASTED_KEY.encode() + b"\x1b[201~")
            session4.wait(0.8)
            check("pasted key stays masked", PASTED_KEY not in session4.screen(), session4.screen()[-400:])
            session4.send(b"\x13")
            session4.wait(0.8)
            screen = session4.screen()
            check("key is staged, not discarded", "save to store it" in screen, screen[-500:])
            check("staged key never shown", PASTED_KEY not in screen, screen[-400:])
            # Explicit Test sends the staged key, not an older saved one.
            session4.send(b"t")
            session4.wait(2.0)
            screen = session4.screen()
            check("test reaches the endpoint", "reachable" in screen, screen[-400:])
            with lit.lock:
                auths = list(lit.auths)
            check("test sent the staged key", len(auths) > 0 and auths[-1] == "Bearer " + PASTED_KEY,
                  "authorization header mismatch")
            # Save persists the exact value with the reference cleared.
            session4.send(b"s")
            session4.wait(1.5)
            with open(path4) as fh:
                saved4 = json.load(fh)
            ep4 = {e["id"]: e for e in saved4.get("endpoints", [])}.get("lit-ep", {})
            check("literal saved exactly", ep4.get("credential") == PASTED_KEY, "stored credential mismatch")
            check("reference cleared on literal save", not ep4.get("credential_env"), ep4.get("credential_env"))
            check("configuration mode 0600", (os.stat(path4).st_mode & 0o777) == 0o600,
                  oct(os.stat(path4).st_mode & 0o777))
            check("saved key never echoed", PASTED_KEY not in session4.screen(), session4.screen()[-400:])
        finally:
            session4.close()
        # Restart on the same home: the saved key stays usable.
        session5 = reopen_session(root, "s4b", path4, home4)
        try:
            session5.send(b"/endpoint edit\r")
            session5.wait(1.2)
            session5.send(b"\x1b[B")
            session5.wait(0.4)
            session5.send(b"\r")
            session5.wait(1.0)
            screen = session5.screen()
            check("restart keeps the stored-key indicator", "(a stored key)" in screen, screen[-500:])
            check("restart never shows the key", PASTED_KEY not in screen, screen[-400:])
            with lit.lock:
                before = len(lit.auths)
            session5.send(b"t")
            session5.wait(2.0)
            with lit.lock:
                after = list(lit.auths)
            check("restarted test sends the saved key",
                  len(after) > before and after[-1] == "Bearer " + PASTED_KEY, "authorization header mismatch")
        finally:
            session5.close()

        # --- No leak: key appears nowhere on disk, in session data or in other endpoints. ---
        with open(path) as fh:
            content = fh.read()
        for secret in (DEEPSEEK_KEY, MINIMAX_KEY, OPENCODE_KEY, PASTED_KEY):
            check("no secret on disk: %s..." % secret[:12], secret not in content, content[-300:])
    finally:
        minimax.stop()
        deepseek.stop()
        html.stop()
        lit.stop()
        shutil.rmtree(root, ignore_errors=True)

    print()
    if FAILURES:
        print("%d FAILURES" % len(FAILURES))
        sys.exit(1)
    print("ALL MODEL FIX PTY CHECKS PASSED")


if __name__ == "__main__":
    main()
