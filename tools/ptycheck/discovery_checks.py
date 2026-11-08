#!/usr/bin/env python3
"""Headless PTY checks for hostile discovery metadata.

The suite runs the real binary against a local provider that answers
with a hostile model catalogue: identifiers that are escape sequences,
identifiers with whitespace, duplicates, and a display name that tries
to rewrite the screen and to forge a row.

What is checked is the raw byte stream, not the rendered screen: an
escape sequence that reaches the terminal is an escape sequence,
whether or not the emulator happened to draw something sensible.

Usage:

    go build -o /tmp/opencode/hal9001 ./cmd/hal9001
    HAL9001_BINARY=/tmp/opencode/hal9001 python3 tools/ptycheck/discovery_checks.py
"""
import json
import os
import shutil
import socket
import subprocess
import sys
import tempfile
import time

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, HERE)
import pty_drive  # noqa: E402
from termsim import render  # noqa: E402

FAILURES = []
BINARY = os.environ.get("HAL9001_BINARY", "/tmp/opencode/hal9001")
PORT = int(os.environ.get("HAL9001_HOSTILE_PORT", "8263"))
SESSION = "discovery"

# The colour sequence the hostile label carries. The interface paints
# with true colour, so a 256-colour sequence is never its own.
INJECTED_COLOUR = "\x1b[38;5;196m"

# The host the hostile label tries to turn into a hyperlink.
INJECTED_HOST = "evil.example"

# The row the hostile label tries to forge.
FORGED_ROW = "FORGED"


def check(name, ok, detail=""):
    print(("[ok  ] " if ok else "[FAIL] ") + name + ("" if ok else "  " + str(detail)))
    if not ok:
        FAILURES.append(name)


def wait_for_server():
    deadline = time.time() + 5.0
    while time.time() < deadline:
        try:
            with socket.create_connection(("127.0.0.1", PORT), timeout=0.2):
                return True
        except OSError:
            time.sleep(0.05)
    return False


def main():
    if not os.path.isfile(BINARY):
        print("missing HAL9001 binary:", BINARY)
        return 2
    base = os.environ.get("HAL9001_PTY_TMPDIR", "/tmp/opencode")
    os.makedirs(base, exist_ok=True)
    root = tempfile.mkdtemp(prefix="hal-discovery-", dir=base)
    home = os.path.join(root, "home")
    store = os.path.join(home, "sessions")
    workspace = os.path.join(root, "HAL")
    os.makedirs(store)
    os.makedirs(workspace)
    config = os.path.join(home, "config.json")
    # Only the endpoint is configured: the interface opens its setup
    # form, which is where the operator asks the endpoint what it
    # offers.
    with open(config, "w") as fh:
        json.dump({
            "provider": "openai-compatible",
            "endpoint": "http://127.0.0.1:%d/v1" % PORT,
            "theme": "HAL Classic",
        }, fh)

    pty_drive.BINARY = BINARY
    pty_drive.CONFIG = config
    pty_drive.HOME = home
    pty_drive.STORE = store
    pty_drive.SESSION = SESSION

    env = os.environ.copy()
    env["HAL9001_HOSTILE_PORT"] = str(PORT)
    provider = subprocess.Popen(
        [sys.executable, os.path.join(HERE, "hostile_provider.py")],
        stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, env=env)

    def capture(script, cols=160, rows=40):
        timeout = sum(delay for delay, _ in script) + 2.0
        raw = pty_drive.run(script, cols=cols, rows=rows, timeout=timeout,
                            fresh=False, session=SESSION,
                            args=["--workspace", workspace])
        return raw, render(raw, cols, rows).text()

    try:
        if not wait_for_server():
            print("the hostile provider did not start")
            return 2

        # 1. The operator asks the endpoint what it offers. The hostile
        #    catalogue is answered with the models that are safe to show.
        raw, screen = capture([
            (2.0, ("http://127.0.0.1:%d/v1" % PORT).encode()),
            (0.3, b"\t"),
            (0.3, b"good-model"),
            (0.3, b"\t"),
            (0.3, b"test-key"),
            (0.5, b"\x14"),
            (1.5, b""),
        ])
        text = raw.decode("utf-8", "replace")
        check("the catalogue reached the interface", "reachable" in text,
              screen)
        check("the refused identifiers are not listed", "3 models" in text,
              screen)
        check("no injected colour sequence reached the terminal",
              INJECTED_COLOUR not in text, repr(text[:400]))
        check("no injected hyperlink reached the terminal",
              INJECTED_HOST not in text, repr(text[:400]))
        rows = [line.strip() for line in screen.split("\n")]
        forged = [line for line in rows if line.startswith(FORGED_ROW)]
        check("no forged row was created", not forged, forged)
        check("the sanitised name is shown on one line",
              any("ModelRed" in line and FORGED_ROW in line for line in rows),
              rows)

        # 2. The interface still works after the hostile catalogue: the
        #    operator saves the setup and sends a message.
        raw, screen = capture([
            (2.0, ("http://127.0.0.1:%d/v1" % PORT).encode()),
            (0.3, b"\t"),
            (0.3, b"good-model"),
            (0.3, b"\t"),
            (0.3, b"test-key"),
            (0.5, b"\x13"),
            (1.5, b"hello there\r"),
            (2.0, b""),
        ])
        check("the conversation still works",
              "hostile catalogue answered" in screen, screen)
    finally:
        provider.terminate()
        try:
            provider.wait(timeout=5)
        except subprocess.TimeoutExpired:
            provider.kill()
        shutil.rmtree(root, ignore_errors=True)

    if FAILURES:
        print("%d DISCOVERY PTY CHECKS FAILED" % len(FAILURES))
        return 1
    print("ALL DISCOVERY PTY CHECKS PASSED")
    return 0


if __name__ == "__main__":
    sys.exit(main())
