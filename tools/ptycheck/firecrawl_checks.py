#!/usr/bin/env python3
"""Isolated pseudo-terminal checks for Firecrawl web research.

The suite owns a fake OpenAI-compatible provider, a fake Firecrawl v2
server, a temporary config/home/store/workspace and its own ports. It
never touches the operator's real Firecrawl key or MPM store.

Usage:

    go build -o /tmp/opencode/hal9001 ./cmd/hal9001
    HAL9001_BINARY=/tmp/opencode/hal9001 python3 tools/ptycheck/firecrawl_checks.py
"""
import json
import os
import pty
import select
import signal
import socket
import struct
import subprocess
import sys
import tempfile
import termios
import time
import fcntl

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, HERE)
from termsim import render  # noqa: E402

BINARY = os.environ.get("HAL9001_BINARY", "/tmp/opencode/hal9001")
PROVIDER_PORT = int(os.environ.get("HAL9001_FIRECRAWL_TEST_PROVIDER_PORT", "8118"))
FIRECRAWL_PORT = int(os.environ.get("HAL9001_FIRECRAWL_TEST_PORT", "8123"))
FAILURES = []
SECRET = "fc-pty-test-key"


def check(name, ok, detail=""):
    print(("[ok  ] " if ok else "[FAIL] ") + name + ("" if ok else "  " + detail))
    if not ok:
        FAILURES.append(name)


def free_port(port):
    sock = socket.socket()
    sock.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    try:
        sock.bind(("127.0.0.1", port))
        return True
    except OSError:
        return False
    finally:
        sock.close()


def wait_for_port(port, timeout=6.0):
    deadline = time.time() + timeout
    while time.time() < deadline:
        try:
            with socket.create_connection(("127.0.0.1", port), timeout=0.2):
                return True
        except OSError:
            time.sleep(0.05)
    return False


def start(script, port, extra_env=None):
    env = dict(os.environ)
    env.update(extra_env or {})
    proc = subprocess.Popen(
        [sys.executable, script], env=env,
        stdout=subprocess.DEVNULL, stderr=subprocess.PIPE, text=True)
    if not wait_for_port(port):
        try:
            proc.terminate()
            detail = (proc.communicate(timeout=2)[1] or "").strip()
        except Exception:
            proc.kill()
            detail = ""
        raise RuntimeError("server did not start on %d: %s" % (port, detail))
    return proc


def reaper(pid, fd):
    try:
        os.close(fd)
    except OSError:
        pass
    for _ in range(10):
        try:
            got, _ = os.waitpid(pid, os.WNOHANG)
        except ChildProcessError:
            return
        if got == pid:
            return
        time.sleep(0.02)
    try:
        os.kill(pid, signal.SIGTERM)
    except ProcessLookupError:
        pass
    for _ in range(50):
        try:
            got, _ = os.waitpid(pid, os.WNOHANG)
        except ChildProcessError:
            return
        if got == pid:
            return
        time.sleep(0.02)
    try:
        os.kill(pid, signal.SIGKILL)
    except ProcessLookupError:
        pass
    try:
        os.waitpid(pid, 0)
    except ChildProcessError:
        pass


def run(script, home, workspace, store, config, session="firecrawl",
        cols=120, rows=40, timeout=20.0, settle=1.0, keep_session=False,
        extra_env=None):
    path = os.path.join(store, session + ".jsonl")
    if not keep_session:
        try:
            os.remove(path)
        except FileNotFoundError:
            pass
    pid, fd = pty.fork()
    if pid == 0:
        os.environ["TERM"] = "xterm-256color"
        os.environ["HOME"] = home
        os.environ["HAL9001_CONFIG"] = config
        os.environ["HAL9001_HOME"] = os.path.dirname(config)
        os.environ["HAL9001_WORKSPACE"] = workspace
        os.environ["HAL9001_MPM_BIN"] = os.path.join(home, "missing-mpm")
        os.environ["FIRECRAWL_API_KEY"] = SECRET
        os.environ["FIRECRAWL_API_URL"] = "http://127.0.0.1:%d/v2" % FIRECRAWL_PORT
        os.environ["HAL9001_FIRECRAWL_TIMEOUT_SECONDS"] = "2"
        os.environ["HAL9001_FIRECRAWL_SEARCH_LIMIT"] = "5"
        os.environ["HAL9001_FIRECRAWL_MAX_SEARCHES"] = "3"
        os.environ["HAL9001_FIRECRAWL_MAX_SCRAPES"] = "2"
        os.environ["HAL9001_FIRECRAWL_MAX_TURN_BYTES"] = "65536"
        for key, value in (extra_env or {}).items():
            if value is None:
                os.environ.pop(key, None)
            else:
                os.environ[key] = value
        argv = [BINARY, "--session", session, "--store", store,
                "--workspace", workspace]
        os.execv(BINARY, argv)

    fcntl.ioctl(fd, termios.TIOCSWINSZ, struct.pack("HHHH", rows, cols, 0, 0))
    out = bytearray()
    start_at = time.time()
    idx = 0
    next_at = start_at + (script[0][0] if script else 0)
    last_action = start_at
    try:
        while time.time() - start_at < timeout:
            ready, _, _ = select.select([fd], [], [], 0.05)
            if ready:
                try:
                    data = os.read(fd, 65536)
                except OSError:
                    break
                if not data:
                    break
                out.extend(data)
            if idx < len(script) and time.time() >= next_at:
                os.write(fd, script[idx][1])
                idx += 1
                last_action = time.time()
                next_at = last_action + (script[idx][0] if idx < len(script) else 0)
            if idx == len(script) and time.time() - last_action >= settle:
                break
    finally:
        reaper(pid, fd)
    return bytes(out)


def capture(script, cols=120, rows=40, **kwargs):
    return render(run(script, cols=cols, rows=rows, **kwargs), cols, rows)


def prompt_script(prompt, settle=2.5):
    return [(1.8, b""), (0.1, prompt.encode() + b"\r"), (settle, b"")]


def screen_text(screen):
    return screen.text()


def read_audit(config_dir):
    path = os.path.join(config_dir, "audit.jsonl")
    records = []
    try:
        with open(path) as fh:
            for line in fh:
                line = line.strip()
                if line:
                    records.append(json.loads(line))
    except FileNotFoundError:
        pass
    return records, path


def main():
    if not os.path.isfile(BINARY):
        print("missing HAL9001 binary: %s" % BINARY)
        return 2
    for port, label in ((PROVIDER_PORT, "provider"), (FIRECRAWL_PORT, "firecrawl")):
        if not free_port(port):
            print("port %d is already in use; refusing to touch another %s" % (port, label))
            return 2

    provider = start(os.path.join(HERE, "fake_provider.py"), PROVIDER_PORT,
                     {"HAL9001_FAKE_PORT": str(PROVIDER_PORT)})
    firecrawl = start(os.path.join(HERE, "fake_firecrawl.py"), FIRECRAWL_PORT,
                      {"HAL9001_FAKE_FIRECRAWL_PORT": str(FIRECRAWL_PORT)})
    try:
        base = os.environ.get("HAL9001_PTY_TMPDIR", "/tmp/opencode")
        os.makedirs(base, exist_ok=True)
        home = tempfile.mkdtemp(prefix="hal9001-firecrawl-", dir=base)
        config_dir = os.path.join(home, "config")
        workspace = os.path.join(home, "HAL")
        store = os.path.join(config_dir, "sessions")
        os.makedirs(workspace, exist_ok=True)
        os.makedirs(store, exist_ok=True)
        config = os.path.join(config_dir, "config.json")
        with open(config, "w") as fh:
            json.dump({
                "provider": "openai-compatible",
                "endpoint": "http://127.0.0.1:%d/v1" % PROVIDER_PORT,
                "api_key": "test-key",
                "models": [{"id": "MiniMax-M3.1-Flash"}],
                "active_model": "MiniMax-M3.1-Flash",
                "theme": "HAL Classic",
            }, fh)

        kwargs = dict(home=home, workspace=workspace, store=store, config=config)

        # Search success: ranked count, source, and the 444 footer.
        screen = capture(prompt_script("[[fcsearch latest release]]"), **kwargs)
        text = screen_text(screen)
        check("search: provider received the ranked result",
              "SEARCH RESULT: 3 results; first: Release notes" in text, text)
        check("search: compact activity line reports the count",
              "⚙ firecrawl_search · completed · 3 results · 444" in text, text)
        check("search: footer stays 444", "444 READ ONLY" in text, text)

        # Empty results are a successful, honest outcome.
        screen = capture(prompt_script("[[fcsearch empty query]]"), **kwargs)
        text = screen_text(screen)
        check("search: empty results are reported",
              "SEARCH RESULT: 0 results" in text and "0 results · 444" in text, text)

        # Scrape success, with the page title carried back to the model.
        screen = capture(prompt_script("[[fcscrape https://example.com/release]]"), **kwargs)
        text = screen_text(screen)
        check("scrape: provider received the page title",
              "SCRAPE RESULT: Release notes" in text, text)
        check("scrape: compact activity line reports bytes",
              "⚙ firecrawl_scrape https://example.com/release" in text and
              "444" in text, text)
        check("scrape: footer stays 444", "444 READ ONLY" in text, text)

        # A large page is truncated and the line says so.
        screen = capture(prompt_script("[[fcscrape https://example.com/long]]"), **kwargs)
        text = screen_text(screen)
        check("scrape: large page is truncated, not silently clipped",
              "truncated=true" in text and "truncated · 444" in text, text)
        check("scrape: truncation reports the returned size",
              "KiB" in text, text)

        # Failures keep their own safe labels and never show the key.
        cases = [
            ("[[fcsearch ratelimit]]", "rate limited", "SEARCH ERROR: rate_limited"),
            ("[[fcsearch authfail]]", "authentication failed", "SEARCH ERROR: authentication"),
            ("[[fcsearch forbidden]]", "forbidden", "SEARCH ERROR: forbidden"),
            ("[[fcsearch serverfail]]", "upstream error", "SEARCH ERROR: upstream"),
            ("[[fcsearch malformed]]", "malformed response", "SEARCH ERROR: malformed_response"),
            ("[[fcscrape http://example.com/no-https]]", "invalid URL", "SCRAPE ERROR: invalid_url"),
        ]
        for prompt, label, expected in cases:
            screen = capture(prompt_script(prompt), **kwargs)
            text = screen_text(screen)
            check("failure: %s is reported" % label,
                  label in text and expected in text, text)
            check("failure: %s keeps 444 and hides the key" % label,
                  "444 READ ONLY" in text and SECRET not in text, text)

        # A page that tries to change the rules is data, not an instruction.
        screen = capture(prompt_script("[[fcscrape https://example.com/inject]]"), **kwargs)
        text = screen_text(screen)
        check("injection: the page is returned as untrusted data",
              "SCRAPE RESULT: Release notes" in text, text)
        check("injection: the footer never becomes 666",
              "444 READ ONLY" in text and "666 UNRESTRICTED" not in text, text)

        # /diag documents availability and the trust boundary.
        screen = capture([(1.8, b""), (0.1, b"/diag\r"), (1.0, b"")], **kwargs)
        text = screen_text(screen)
        check("diag: Firecrawl is reported ready without the key",
              "firecrawl · ready" in text and SECRET not in text, text)
        check("diag: the current session is 444", "444" in text, text)

        # /help documents both tools and the untrusted-content rule. A
        # tall terminal shows the whole reference without scrolling.
        screen = capture([(1.8, b""), (0.1, b"/help\r"), (1.0, b"")],
                         cols=120, rows=60, **kwargs)
        text = screen_text(screen)
        check("help: documents Firecrawl research in 444",
              "Firecrawl web research" in text, text)

        # The audit trail records only Firecrawl tools, modes and no key.
        records, audit_path = read_audit(config_dir)
        tools = [r for r in records if r.get("operation") == "tool"]
        check("audit: Firecrawl tool records exist",
              bool(tools) and all(str(r.get("tool", "")).startswith("firecrawl_")
                                  for r in tools), str(records))
        check("audit: every record is 444",
              bool(tools) and all(r.get("mode") == "444" for r in tools), str(records))
        raw = ""
        try:
            raw = open(audit_path).read()
        except FileNotFoundError:
            pass
        check("audit: no key or page text is stored",
              SECRET not in raw and "IGNORE ALL PREVIOUS" not in raw, raw)

        # A missing key degrades to a clear, safe message.
        missing = dict(kwargs)
        screen = capture(prompt_script("[[fcsearch latest release]]"),
                         extra_env={"FIRECRAWL_API_KEY": None}, **missing)
        text = screen_text(screen)
        check("missing key: the tool reports not configured",
              "not configured" in text and SECRET not in text, text)
        screen = capture([(1.8, b""), (0.1, b"/diag\r"), (1.0, b"")],
                         extra_env={"FIRECRAWL_API_KEY": None}, **missing)
        text = screen_text(screen)
        check("missing key: /diag names the missing variable",
              "FIRECRAWL_API_KEY is not set" in text, text)

        # Session resume keeps the verified Firecrawl ledger row.
        resume = dict(kwargs)
        resume["session"] = "resume"
        capture(prompt_script("[[fcsearch resume check]]"), **resume)
        screen = capture([(1.8, b"")], keep_session=True, **resume)
        text = screen_text(screen)
        check("resume: the compact search record survives",
              "⚙ firecrawl_search · completed · 3 results · 444" in text, text)
    finally:
        for proc in (firecrawl, provider):
            proc.terminate()
            try:
                proc.wait(timeout=3)
            except subprocess.TimeoutExpired:
                proc.kill()
                proc.wait()

    print()
    if FAILURES:
        print("FAILED: " + ", ".join(FAILURES))
        return 1
    print("ALL FIRECRAWL PTY CHECKS PASSED")
    return 0


if __name__ == "__main__":
    sys.exit(main())
