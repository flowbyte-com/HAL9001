#!/usr/bin/env python3
"""Isolated pseudo-terminal checks for unified web research.

The suite owns a fake OpenAI-compatible provider, fake Firecrawl, Exa and
DuckDuckGo Lite servers, a temporary config/home/store/workspace and its
own ports. It never touches the operator's real credentials or MPM store.

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
EXA_PORT = int(os.environ.get("HAL9001_EXA_TEST_PORT", "8124"))
DDG_PORT = int(os.environ.get("HAL9001_DDG_TEST_PORT", "8125"))
FAILURES = []
SECRET = "fc-pty-test-key"
EXA_SECRET = "exa-pty-test-key"


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
        os.environ["EXA_API_KEY"] = EXA_SECRET
        os.environ["EXA_API_URL"] = "http://127.0.0.1:%d" % EXA_PORT
        os.environ["HAL9001_DDG_URL"] = "http://127.0.0.1:%d/lite/" % DDG_PORT
        os.environ["HAL9001_DDG_MIN_INTERVAL_MS"] = "1"
        os.environ["HAL9001_FIRECRAWL_TIMEOUT_SECONDS"] = "2"
        os.environ["HAL9001_EXA_TIMEOUT_SECONDS"] = "2"
        os.environ["HAL9001_DDG_TIMEOUT_SECONDS"] = "2"
        os.environ["HAL9001_WEB_SEARCH_LIMIT"] = "5"
        os.environ["HAL9001_WEB_MAX_SEARCHES"] = "3"
        os.environ["HAL9001_WEB_MAX_SCRAPES"] = "2"
        os.environ["HAL9001_WEB_MAX_TURN_BYTES"] = "65536"
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
    ports = ((PROVIDER_PORT, "provider"), (FIRECRAWL_PORT, "firecrawl"),
             (EXA_PORT, "exa"), (DDG_PORT, "duckduckgo"))
    for port, label in ports:
        if not free_port(port):
            print("port %d is already in use; refusing to touch another %s" % (port, label))
            return 2

    provider = start(os.path.join(HERE, "fake_provider.py"), PROVIDER_PORT,
                     {"HAL9001_FAKE_PORT": str(PROVIDER_PORT)})
    firecrawl = start(os.path.join(HERE, "fake_firecrawl.py"), FIRECRAWL_PORT,
                      {"HAL9001_FAKE_FIRECRAWL_PORT": str(FIRECRAWL_PORT)})
    exa = start(os.path.join(HERE, "fake_exa.py"), EXA_PORT,
                {"HAL9001_FAKE_EXA_PORT": str(EXA_PORT)})
    ddg = start(os.path.join(HERE, "fake_ddg.py"), DDG_PORT,
                {"HAL9001_FAKE_DDG_PORT": str(DDG_PORT)})
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

        # Firecrawl is primary: ranked count, source and 444 footer.
        screen = capture(prompt_script("[[fcsearch latest release]]"), **kwargs)
        text = screen_text(screen)
        check("search: provider received the ranked result",
              "SEARCH RESULT: 3 results; first: Release notes" in text, text)
        check("search: primary attribution is firecrawl",
              "⚙ web_search · completed · 3 results · firecrawl · 444" in text, text)
        check("search: footer stays 444", "444 READ ONLY" in text, text)

        # A valid empty result is success, not a reason to try Exa or DDG.
        screen = capture(prompt_script("[[fcsearch empty query]]"), **kwargs)
        text = screen_text(screen)
        check("search: empty results are reported without fallback",
              "SEARCH RESULT: 0 results" in text and
              "0 results · firecrawl · 444" in text, text)

        # Exa fallback: Firecrawl is unavailable, Exa answers.
        screen = capture(prompt_script("[[fcsearch exafallback]]"), **kwargs)
        text = screen_text(screen)
        check("fallback: Firecrawl -> Exa returns results",
              "SEARCH RESULT: 3 results; first: Exa result 1" in text, text)
        check("fallback: activity names Exa",
              "web_search · completed · 3 results · exa · fallback" in text, text)

        # DuckDuckGo fallback: Firecrawl and Exa both fail.
        screen = capture(prompt_script("[[fcsearch ddgfallback]]"), **kwargs)
        text = screen_text(screen)
        check("fallback: Exa -> DuckDuckGo returns organic results",
              "SEARCH RESULT: 3 results; first: DDG result one" in text, text)
        check("fallback: activity names DuckDuckGo",
              "duckduckgo · fallback" in text, text)

        # A malformed primary response also moves to the next provider.
        screen = capture(prompt_script("[[fcsearch malformed]]"), **kwargs)
        text = screen_text(screen)
        check("fallback: malformed Firecrawl response reaches Exa",
              "SEARCH RESULT: 3 results; first: Exa result 1" in text and
              "exa · fallback" in text, text)

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

        # With every provider failing, the failure is honest and safe.
        screen = capture(prompt_script("[[fcsearch allfail]]"), **kwargs)
        text = screen_text(screen)
        check("failure: all providers failing is reported",
              "SEARCH ERROR: blocked" in text and
              "web_search · failed · all providers failed" in text and
              "444" in text, text)
        check("failure: the footer stays 444 and no key is shown",
              "444 READ ONLY" in text and SECRET not in text and EXA_SECRET not in text, text)

        # A scrape URL is still Firecrawl-only and must be public HTTPS.
        screen = capture(prompt_script("[[fcscrape http://example.com/no-https]]"), **kwargs)
        text = screen_text(screen)
        check("failure: invalid scrape URL is reported",
              "invalid URL" in text and "SCRAPE ERROR: invalid_url" in text, text)
        check("failure: invalid scrape keeps 444 and hides keys",
              "444 READ ONLY" in text and SECRET not in text and EXA_SECRET not in text, text)

        # A page that tries to change the rules is data, not an instruction.
        screen = capture(prompt_script("[[fcscrape https://example.com/inject]]"), **kwargs)
        text = screen_text(screen)
        check("injection: the page is returned as untrusted data",
              "SCRAPE RESULT: Release notes" in text, text)
        check("injection: the footer never becomes 666",
              "444 READ ONLY" in text and "666 UNRESTRICTED" not in text, text)

        # /diag documents the provider order and availability.
        screen = capture([(1.8, b""), (0.1, b"/diag\r"), (1.0, b"")], **kwargs)
        text = screen_text(screen)
        check("diag: all providers and the scraper are reported ready",
              "firecrawl=ready" in text and "exa=ready" in text and
              "duckduckgo=ready" in text and "scrape: firecrawl=ready" in text, text)
        check("diag: no credential is shown",
              SECRET not in text and EXA_SECRET not in text, text)
        check("diag: the current session is 444", "444" in text, text)

        # /help documents the web-research capability.
        screen = capture([(1.8, b""), (0.1, b"/help\r"), (1.0, b"")],
                         cols=120, rows=60, **kwargs)
        text = screen_text(screen)
        check("help: documents web research in 444",
              "web research and one-off write approval" in text, text)

        # The audit trail records only web tools, modes and no content.
        records, audit_path = read_audit(config_dir)
        tools = [r for r in records if r.get("operation") == "tool"]
        check("audit: web tool records exist",
              bool(tools) and all(r.get("tool") in ("web_search", "firecrawl_scrape")
                                  for r in tools), str(records))
        check("audit: every record is 444",
              bool(tools) and all(r.get("mode") == "444" for r in tools), str(records))
        raw = ""
        try:
            raw = open(audit_path).read()
        except FileNotFoundError:
            pass
        check("audit: no key or page text is stored",
              SECRET not in raw and EXA_SECRET not in raw and
              "IGNORE ALL PREVIOUS" not in raw, raw)

        # Firecrawl and Exa missing: the keyless DDG fallback still answers.
        missing = dict(kwargs)
        no_keys = {"FIRECRAWL_API_KEY": None, "EXA_API_KEY": None}
        screen = capture(prompt_script("[[fcsearch latest release]]"),
                         extra_env=no_keys, **missing)
        text = screen_text(screen)
        check("missing keys: DuckDuckGo answers without credentials",
              "SEARCH RESULT: 3 results; first: DDG result one" in text and
              "duckduckgo" in text and SECRET not in text and EXA_SECRET not in text, text)
        screen = capture([(1.8, b""), (0.1, b"/diag\r"), (1.0, b"")],
                         extra_env=no_keys, **missing)
        text = screen_text(screen)
        check("missing keys: /diag names both missing variables",
              "FIRECRAWL_API_KEY is not set" in text and
              "EXA_API_KEY is not set" in text and "duckduckgo=ready" in text, text)

        # Session resume keeps the verified search ledger row.
        resume = dict(kwargs)
        resume["session"] = "resume"
        capture(prompt_script("[[fcsearch resume check]]"), **resume)
        screen = capture([(1.8, b"")], keep_session=True, **resume)
        text = screen_text(screen)
        check("resume: the compact search record survives",
              "⚙ web_search · completed · 3 results · firecrawl · 444" in text, text)
    finally:
        for proc in (ddg, exa, firecrawl, provider):
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
    print("ALL WEB-RESEARCH PTY CHECKS PASSED")
    return 0


if __name__ == "__main__":
    sys.exit(main())
