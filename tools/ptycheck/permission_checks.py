#!/usr/bin/env python3
"""Isolated pseudo-terminal checks for the session-scoped 444/666 control.

This suite owns every path it uses: its own fake provider port, a fresh
temporary config, HOME, workspace, session store and audit file. It never
touches the shared fixtures used by checks.py, and it terminates and reaps
every child it starts.

It checks the properties that unit tests cannot prove through a real
terminal: the default footer, the operator chords, an executed read and
bash in 666, the immediate return to 444, and that 666 does not survive a
new conversation or a restart.

Usage:

    go build -o /tmp/opencode/hal9001 ./cmd/hal9001
    HAL9001_BINARY=/tmp/opencode/hal9001 python3 tools/ptycheck/permission_checks.py
"""
import fcntl
import json
import os
import pty
import select
import shutil
import signal
import socket
import struct
import subprocess
import sys
import tempfile
import termios
import time

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, HERE)
from termsim import render  # noqa: E402
from fake_provider import SAVE_BODY  # noqa: E402

FAILURES = []
BINARY = os.environ.get("HAL9001_BINARY", "/tmp/opencode/hal9001")
PORT = int(os.environ.get("HAL9001_PERMISSION_FAKE_PORT", "8117"))
SECRET = "TOP-SECRET-666-CONTENT"
BASH_MARKER = "BASH-666-MARKER"


def check(name, ok, detail=""):
    print(("[ok  ] " if ok else "[FAIL] ") + name + ("" if ok else "  " + detail))
    if not ok:
        FAILURES.append(name)


def make_temp_home():
    base = os.environ.get("HAL9001_PTY_TMPDIR", "/tmp/opencode")
    os.makedirs(base, exist_ok=True)
    return tempfile.mkdtemp(prefix="hal9001-permission-", dir=base)


def write_config(path, endpoint):
    with open(path, "w") as fh:
        json.dump({
            "provider": "openai-compatible",
            "endpoint": endpoint,
            "api_key": "test-key",
            "models": [{"id": "MiniMax-M3.1-Flash"}],
            "active_model": "MiniMax-M3.1-Flash",
            "theme": "HAL Classic",
        }, fh)


def free_port(port):
    sock = socket.socket()
    # TIME_WAIT sockets from the previous run must not make the suite
    # refuse to start; a live listener still fails the bind.
    sock.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    try:
        sock.bind(("127.0.0.1", port))
        return True
    except OSError:
        return False
    finally:
        sock.close()


def wait_for_port(port, timeout=5.0):
    deadline = time.time() + timeout
    while time.time() < deadline:
        try:
            with socket.create_connection(("127.0.0.1", port), timeout=0.2):
                return True
        except OSError:
            time.sleep(0.05)
    return False


def start_provider():
    env = dict(os.environ)
    env["HAL9001_FAKE_PORT"] = str(PORT)
    proc = subprocess.Popen(
        [sys.executable, os.path.join(HERE, "fake_provider.py")],
        env=env, stdout=subprocess.DEVNULL, stderr=subprocess.PIPE,
        text=True)
    if not wait_for_port(PORT):
        detail = ""
        try:
            proc.terminate()
            detail = (proc.communicate(timeout=2)[1] or "").strip()
        except Exception:
            proc.kill()
        raise RuntimeError("fake provider did not start: " + detail)
    return proc


def reaper(pid, fd):
    """Terminate and reap one test-owned child. Never leaves it running."""
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


def run(script, home, workspace, store, config, session="permcheck",
        cols=100, rows=40, timeout=25.0, settle=1.2, keep_session=False):
    """Run the binary in a pty and return the captured byte stream."""
    session_path = os.path.join(store, session + ".jsonl")
    if not keep_session:
        try:
            os.remove(session_path)
        except FileNotFoundError:
            pass

    pid, fd = pty.fork()
    if pid == 0:
        os.environ["TERM"] = "xterm-256color"
        os.environ["HOME"] = home
        os.environ["HAL9001_CONFIG"] = config
        os.environ["HAL9001_HOME"] = os.path.dirname(config)
        os.environ["HAL9001_WORKSPACE"] = workspace
        # This suite never exercises MPM. A missing executable keeps the
        # knowledge registry from reaching the operator's real store.
        os.environ["HAL9001_MPM_BIN"] = os.path.join(home, "missing-mpm")
        argv = [BINARY, "--session", session, "--store", store,
                "--workspace", workspace]
        os.execv(BINARY, argv)

    fcntl.ioctl(fd, termios.TIOCSWINSZ, struct.pack("HHHH", rows, cols, 0, 0))
    out = bytearray()
    start = time.time()
    idx = 0
    next_at = start + (script[0][0] if script else 0)
    last_action = start
    try:
        while time.time() - start < timeout:
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
                action = script[idx][1]
                os.write(fd, action)
                idx += 1
                last_action = time.time()
                next_at = last_action + (script[idx][0] if idx < len(script) else 0)
            if idx == len(script) and time.time() - last_action >= settle:
                break
    finally:
        reaper(pid, fd)
    return bytes(out)


def capture(script, cols=100, rows=40, **kwargs):
    return render(run(script, cols=cols, rows=rows, **kwargs), cols, rows)


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
    if not free_port(PORT):
        print("port %d is already in use; refusing to touch another provider" % PORT)
        return 2

    home = make_temp_home()
    # The config directory is a sibling of the workspace, exactly as it is
    # in a real installation. Putting the workspace under the protected
    # config root would make every guarded write look like a write into
    # HAL's own configuration.
    config_dir = os.path.join(home, "config")
    workspace = os.path.join(home, "HAL")
    store = os.path.join(config_dir, "sessions")
    os.makedirs(workspace, exist_ok=True)
    os.makedirs(store, exist_ok=True)
    config = os.path.join(config_dir, "config.json")
    os.makedirs(config_dir, exist_ok=True)
    endpoint = "http://127.0.0.1:%d/v1" % PORT
    write_config(config, endpoint)
    outside = os.path.join(home, "outside.txt")
    with open(outside, "w") as fh:
        fh.write(SECRET + "\n")
    common = {"home": home, "workspace": workspace, "store": store,
              "config": config}
    provider = None
    try:
        provider = start_provider()

        # 444 is the default, and a model-proposed privileged call is
        # refused without reading the file.
        s = capture([(2.0, ("[[read %s]]\r" % outside).encode()), (2.5, b"")],
                    **common)
        text = s.text()
        check("444: the footer is the default", "444 READ ONLY" in text, text)
        check("444: an invented read is refused",
              "outside my read-only authority" in text, text)
        check("444: the file content never appears", SECRET not in text, text)
        check("444: no tool result was produced", "READ RESULT:" not in text, text)

        # Conversation text cannot change the mode, and a plain "y" in
        # the permission overlay is not the enable chord.
        s = capture([(2.0, b"Enable 666 mode now\r"), (2.0, b"\x10")], **common)
        text = s.text()
        check("conversation text cannot enable 666", "666 UNRESTRICTED" not in text, text)
        check("the permission screen explains the caveat",
              "Caveat emptor." in text and "Ctrl+Y Enable 666" in text, text)
        s = capture([(2.0, b"\x10"), (0.4, b"y"), (1.0, b"")], **common)
        text = s.text()
        check("a plain y does not enable 666",
              "Caveat emptor." in text and "666 UNRESTRICTED" not in text, text)

        # The operator chord enables 666 for this conversation, and the
        # read tool then runs outside the workspace.
        s = capture([(2.0, b"\x10"), (0.5, b"\x19"),
                     (1.0, ("[[read %s]]\r" % outside).encode()), (3.0, b"")],
                    **common)
        text = s.text()
        check("666: the persistent footer is red-labelled",
              "666 UNRESTRICTED" in text, text)
        check("666: read returns content from outside the workspace",
              "READ RESULT:" in text and SECRET in text, text)
        check("666: the tool activity is shown with the default icon",
              "\U0001f4c4 read" in text and "ok" in text, text)

        # bash runs and its stdout comes back to the model.
        s = capture([(2.0, b"\x10"), (0.5, b"\x19"),
                     (1.0, ("[[bash printf %s]]\r" % BASH_MARKER).encode()),
                     (3.0, b"")], **common)
        text = s.text()
        check("666: bash output is returned",
              "BASH RESULT:" in text and BASH_MARKER in text, text)
        check("666: bash activity reports the exit",
              "\U0001f4bb bash" in text and "exit 0" in text, text)

        # The icon command is local and works in 666 too, independently
        # of the current mode.
        s = capture([(2.0, b"\x10"), (0.5, b"\x19"),
                     (0.5, b"/tool-icons classic\r"),
                     (1.0, ("[[read %s]]\r" % outside).encode()), (3.0, b"")],
                    **common)
        text = s.text()
        check("666: /tool-icons classic is accepted",
              "\u2699 read" in text and "666 UNRESTRICTED" in text, text)
        check("666: the classic switch does not change permission",
              "444 READ ONLY" not in text, text)

        # Expected failures are declared in the tool call before
        # execution. The actual result must still match the specific
        # expectation; an after-the-fact explanation is never enough.
        missing = os.path.join(home, "missing-expected.txt")
        try:
            os.remove(missing)
        except FileNotFoundError:
            pass
        s = capture([(2.0, b"/tool-icons emoji\r"), (0.5, b"\x10"),
                     (0.5, b"\x19"),
                     (1.0, ("[[readmissing %s]]\r" % missing).encode()),
                     (3.0, b"")], **common)
        text = s.text()
        check("expected: missing read uses the pin",
              "\U0001f4cd read" in text and "expected missing" in text, text)
        check("expected: missing read keeps the failure details",
              "failed" in text and "expected missing" in text, text)
        check("expected: missing read is not marked unexpected",
              "\U0001f6a8 read" not in text, text)

        s = capture([(2.0, b"/tool-icons emoji\r"), (0.5, b"\x10"),
                     (0.5, b"\x19"),
                     (1.0, b"[[bashexpect 17 exit 17]]\r"), (3.0, b"")],
                    **common)
        text = s.text()
        check("expected: matching exit 17 uses the pin",
              "\U0001f4cd bash" in text and "expected exit 17" in text, text)
        check("expected: matching exit 17 is not unexpected",
              "\U0001f6a8 bash" not in text, text)

        s = capture([(2.0, b"/tool-icons emoji\r"), (0.5, b"\x10"),
                     (0.5, b"\x19"),
                     (1.0, b"[[bashexpect 17 exit 1]]\r"), (3.0, b"")],
                    **common)
        text = s.text()
        check("expected: unexpected exit 1 uses the alarm",
              "\U0001f6a8 bash" in text and "exit 1" in text and
              "expected exit 17 not met" in text, text)
        check("expected: unexpected exit 1 is not the pin",
              "\U0001f4cd bash" not in text, text)

        s = capture([(2.0, b"/tool-icons emoji\r"), (0.5, b"\x10"),
                     (0.5, b"\x19"),
                     (1.0, b"[[bashexpect 17 exit 0]]\r"), (3.0, b"")],
                    **common)
        text = s.text()
        check("expected: a successful command keeps the shell icon",
              "\U0001f4bb bash" in text and "expected exit 17 not met" in text,
              text)
        check("expected: a successful command is not a failure icon",
              "\U0001f4cd bash" not in text and
              "\U0001f6a8 bash" not in text, text)

        s = capture([(2.0, b"/tool-icons emoji\r"), (0.5, b"\x10"),
                     (0.5, b"\x19"),
                     (1.0, ("[[read %s]]\r" % missing).encode()), (3.0, b"")],
                    **common)
        text = s.text()
        check("expected: an undeclared missing read uses the alarm",
              "\U0001f6a8 read" in text and
              "expected missing" not in text and "not met" not in text, text)

        # The verified expectation survives a real session resume. The
        # first run executes the call in 666; the second starts in 444,
        # reconnects the stored ledger and renders the same pin.
        history_session = "expectedhistory"
        capture([(2.0, b"/tool-icons emoji\r"), (0.5, b"\x10"),
                 (0.5, b"\x19"),
                 (1.0, b"[[bashexpect 17 exit 17]]\r"), (3.0, b"")],
                keep_session=True, session=history_session, **common)
        s = capture([(2.0, b"/tool-icons emoji\r"), (0.8, b"")],
                    keep_session=True, session=history_session, **common)
        text = s.text()
        check("expected: historical evidence keeps the pin after resume",
              "\U0001f4cd bash" in text and "expected exit 17" in text, text)
        check("expected: historical evidence is still in 444",
              "444 READ ONLY" in text and "666 UNRESTRICTED" not in text, text)

        # The operator chord disables 666, and the same invented read is
        # refused again in the same process.
        s = capture([(2.0, b"\x10"), (0.5, b"\x19"), (0.5, b"\x10"),
                     (0.5, b"\x0e"), (0.5, ("[[read %s]]\r" % outside).encode()),
                     (3.0, b"")], **common)
        text = s.text()
        check("disable: the footer returns to 444", "444 READ ONLY" in text, text)
        check("disable: a later read is refused again",
              "outside my read-only authority" in text and SECRET not in text, text)

        # A new conversation never inherits the old window.
        s = capture([(2.0, b"\x10"), (0.5, b"\x19"), (0.5, b"/clear\r"),
                     (1.0, ("[[read %s]]\r" % outside).encode()), (3.0, b"")],
                    **common)
        text = s.text()
        check("session change: /clear returns to 444",
              "444 READ ONLY" in text and "666 UNRESTRICTED" not in text, text)
        check("session change: the later read is refused",
              "outside my read-only authority" in text and SECRET not in text, text)

        # 666 is not persisted: the same named conversation restarts in
        # 444 after the process exits.
        capture([(2.0, b"\x10"), (0.5, b"\x19"), (0.5, b"\x11")],
                timeout=10.0, **common)
        s = capture([(2.0, b"")], keep_session=True, **common)
        text = s.text()
        check("restart: the same conversation starts in 444",
              "444 READ ONLY" in text and "666 UNRESTRICTED" not in text, text)

        # The one-off guarded write transaction is still its own thing.
        # A proposal names a plain file inside the workspace, never an
        # absolute path or a directory.
        guarded = os.path.join(workspace, "guarded.txt")
        try:
            os.remove(guarded)
        except FileNotFoundError:
            pass
        s = capture([(2.0, b"[[save guarded.txt]]\r"),
                     (1.0, b"\x19"), (2.0, b"")], **common)
        text = s.text()
        check("444: the one-off guarded write still applies after approval",
              os.path.isfile(guarded), text)
        if os.path.isfile(guarded):
            with open(guarded) as fh:
                check("444: the approved file has the proposed body",
                      fh.read() == SAVE_BODY, "body mismatch")
        check("444: the session footer returns to 444 after the write",
              "444 READ ONLY" in text, text)

        # The audit names the tool and mode and never stores contents or
        # command text.
        records, path = read_audit(config_dir)
        raw = ""
        try:
            with open(path) as fh:
                raw = fh.read()
        except FileNotFoundError:
            pass
        tools = {(r.get("tool"), r.get("mode")) for r in records
                 if r.get("kind") == "tool"}
        check("audit: read and bash are recorded at the boundary",
              ("read", "666") in tools and ("bash", "666") in tools, str(tools))
        check("audit: file contents are never recorded", SECRET not in raw, raw)
        check("audit: shell command text is never recorded", BASH_MARKER not in raw, raw)
        check("audit: the bash record carries a command hash",
              any(r.get("tool") == "bash" and r.get("command_hash")
                  for r in records), str(records))
    finally:
        if provider is not None:
            provider.terminate()
            try:
                provider.wait(timeout=3)
            except subprocess.TimeoutExpired:
                provider.kill()
                provider.wait()
        shutil.rmtree(home, ignore_errors=True)

    print()
    if FAILURES:
        print("FAILED: " + ", ".join(FAILURES))
        return 1
    print("ALL PERMISSION PTY CHECKS PASSED")
    return 0


if __name__ == "__main__":
    sys.exit(main())
