#!/usr/bin/env python3
"""Headless PTY checks for the red-eye activity heartbeat.

The suite runs the real binary against a local OpenAI-compatible stub. It
checks the stationary red marker, independently moving clock frames, the
static fallback from /animation off, the five-second silence suffix,
completion, 80x9 layout and all five themes.

Usage:

    go build -o /tmp/opencode/hal9001 ./cmd/hal9001
    HAL9001_BINARY=/tmp/opencode/hal9001 python3 tools/ptycheck/heartbeat_checks.py
"""
import json
import os
import shutil
import socket
import subprocess
import sys
import tempfile
import time
import unicodedata

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, HERE)
import pty_drive  # noqa: E402
from termsim import render  # noqa: E402

FAILURES = []
BINARY = os.environ.get("HAL9001_BINARY", "/tmp/opencode/hal9001")
PORT = 8127
SESSION = "heartbeat"
CLOCKS = "🕛🕧🕐🕜🕑🕝🕒🕞🕓🕟🕔🕠🕕🕡🕖🕢🕗🕣🕘🕤🕙🕥🕚🕦"


def check(name, ok, detail=""):
    print(("[ok  ] " if ok else "[FAIL] ") + name + ("" if ok else "  " + str(detail)))
    if not ok:
        FAILURES.append(name)


def write_config(path, theme="HAL Classic"):
    with open(path, "w") as fh:
        json.dump({
            "provider": "openai-compatible",
            "endpoint": "http://127.0.0.1:%d/v1" % PORT,
            "api_key": "test-key",
            "models": [{"id": "MiniMax-M3.1-Flash"}],
            "active_model": "MiniMax-M3.1-Flash",
            "theme": theme,
        }, fh)


def display_width(text):
    width = 0
    for ch in text:
        if unicodedata.combining(ch) or ch == "\ufe0f":
            continue
        width += 2 if unicodedata.east_asian_width(ch) in ("W", "F") else 1
    return width


def capture(script, cols=80, rows=24, theme="HAL Classic", timeout=9.0):
    write_config(CONFIG, theme)
    raw = pty_drive.run(script, cols=cols, rows=rows, timeout=timeout,
                        fresh=False, session=SESSION,
                        args=["--workspace", WORKSPACE])
    return raw, render(raw, cols, rows).text()


def clocks_in(raw):
    text = raw.decode("utf-8", "replace")
    return {frame for frame in CLOCKS if frame in text}


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
    global CONFIG, WORKSPACE
    if not os.path.isfile(BINARY):
        print("missing HAL9001 binary:", BINARY)
        return 2
    base = os.environ.get("HAL9001_PTY_TMPDIR", "/tmp/opencode")
    os.makedirs(base, exist_ok=True)
    root = tempfile.mkdtemp(prefix="hal9001-heartbeat-", dir=base)
    home = os.path.join(root, "config")
    store = os.path.join(root, "sessions")
    WORKSPACE = os.path.join(root, "HAL")
    os.makedirs(home)
    os.makedirs(store)
    os.makedirs(WORKSPACE)
    CONFIG = os.path.join(home, "config.json")
    write_config(CONFIG)

    pty_drive.BINARY = BINARY
    pty_drive.CONFIG = CONFIG
    pty_drive.HOME = home
    pty_drive.STORE = store
    os.environ["HAL9001_MPM_BIN"] = os.path.join(root, "missing-mpm")

    env = os.environ.copy()
    env["HAL9001_FAKE_PORT"] = str(PORT)
    provider = subprocess.Popen(
        [sys.executable, os.path.join(HERE, "fake_provider.py")],
        stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, env=env)
    try:
        if not wait_for_server():
            print("fake provider did not start")
            return 2

        status = capture([(2.0, b"/animation\r"), (0.6, b"")], timeout=2.7)[1]
        check("status reports animation on", "Animation: on." in status, status)

        raw, text = capture([(2.0, b"hold the line\r"), (3.3, b"")],
                            timeout=7.0)
        frames = clocks_in(raw)
        check("red identity marker is stationary", "🔴 HAL9001" in text, text)
        check("assistant eye is red", "\n🔴 " in text, text)
        check("clock advances independently of tokens", len(frames) >= 3,
              "".join(sorted(frames)))
        check("completion replaces the clock", "✓ Complete" in text, text)
        check("no row overflows during the heartbeat",
              all(display_width(row) <= 80 for row in text.split("\n")), text)

        raw, text = capture([(2.0, b"/animation off\r"),
                             (0.5, b"hold the line\r"), (3.2, b"")],
                            timeout=7.0)
        frames = clocks_in(raw)
        check("animation off freezes the clock", frames == {"🕛"},
              "".join(sorted(frames)))
        check("animation off keeps the turn running",
              "Streaming" in text or "Complete" in text, text)

        raw, text = capture([(2.0, b"/animation off\r"),
                             (0.3, b"/animation on\r"),
                             (0.4, b"hold the line\r"), (3.2, b"")],
                            timeout=7.5)
        frames = clocks_in(raw)
        check("animation on resumes the clock", len(frames) >= 3,
              "".join(sorted(frames)))

        _, text = capture([(2.0, b"hold the line long\r"), (5.4, b"")],
                          timeout=7.5)
        check("quiet provider shows no-data elapsed time",
              "no new data for 5s" in text, text)
        check("quiet provider is not reported as an error",
              "went silent" not in text and "error:" not in text, text)

        raw, text = capture([(2.0, b"hold the line\r"), (1.8, b"")],
                            cols=80, rows=9, timeout=6.0)
        rows = text.split("\n")
        check("80x9 keeps exactly nine rows", len(rows) == 9, text)
        check("80x9 keeps every row inside the terminal",
              all(display_width(row) <= 80 for row in rows), text)

        for theme in ("HAL Classic", "Midnight", "Amber", "Paper",
                      "Monochrome"):
            raw, text = capture([(2.0, b"hi\r"), (1.2, b"")],
                                theme=theme, timeout=6.0)
            rows = text.split("\n")
            check("theme %s: red eye renders" % theme,
                  "🔴 HAL9001" in text and "\n🔴 " in text, text)
            check("theme %s: no overflow" % theme,
                  all(display_width(row) <= 80 for row in rows), text)
    finally:
        provider.terminate()
        try:
            provider.wait(timeout=3)
        except subprocess.TimeoutExpired:
            provider.kill()
            provider.wait()
        shutil.rmtree(root, ignore_errors=True)

    print()
    if FAILURES:
        print("FAILED: " + ", ".join(FAILURES))
        return 1
    print("ALL HEARTBEAT PTY CHECKS PASSED")
    return 0


if __name__ == "__main__":
    sys.exit(main())
