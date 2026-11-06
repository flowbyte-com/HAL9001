#!/usr/bin/env python3
"""Headless PTY checks for immediate multiline-paste visibility.

The suite owns its config, home and store. It sends a bracketed paste and
renders the first frame after it, without sending an arrow key or any other
follow-up event.

Usage:

    go build -o /tmp/opencode/hal9001 ./cmd/hal9001
    HAL9001_BINARY=/tmp/opencode/hal9001 python3 tools/ptycheck/composer_checks.py
"""
import json
import os
import shutil
import sys
import tempfile

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, HERE)
import pty_drive  # noqa: E402
from termsim import render  # noqa: E402

FAILURES = []
BINARY = os.environ.get("HAL9001_BINARY", "/tmp/opencode/hal9001")


def check(name, ok, detail=""):
    print(("[ok  ] " if ok else "[FAIL] ") + name + ("" if ok else "  " + str(detail)))
    if not ok:
        FAILURES.append(name)


def write_config(path):
    with open(path, "w") as fh:
        json.dump({
            "provider": "openai-compatible",
            "endpoint": "http://127.0.0.1:9/v1",
            "api_key": "test-key",
            "models": [{"id": "MiniMax-M3.1-Flash"}],
            "active_model": "MiniMax-M3.1-Flash",
            "theme": "HAL Classic",
        }, fh)


def numbered(n):
    return "\n".join("line %02d of pasted text" % i for i in range(1, n + 1))


def frame_after_paste(body, home, config, store):
    pty_drive.BINARY = BINARY
    pty_drive.CONFIG = config
    pty_drive.HOME = home
    pty_drive.STORE = store
    raw = pty_drive.run(
        [(2.0, b""), (0.05, b"\x1b[200~" + body.encode() + b"\x1b[201~")],
        cols=60, rows=30, timeout=2.3, fresh=True, session="paste")
    return render(raw, 60, 30).text()


def main():
    if not os.path.isfile(BINARY):
        print("missing HAL9001 binary: %s" % BINARY)
        return 2
    base = os.environ.get("HAL9001_PTY_TMPDIR", "/tmp/opencode")
    os.makedirs(base, exist_ok=True)
    home = tempfile.mkdtemp(prefix="hal9001-composer-", dir=base)
    try:
        config = os.path.join(home, "config.json")
        store = os.path.join(home, "sessions")
        os.makedirs(store, exist_ok=True)
        write_config(config)

        cases = [
            ("two lines", "alpha\nbravo", ["alpha", "bravo"]),
            ("three lines", "alpha\nbravo\ncharlie", ["alpha", "bravo", "charlie"]),
            ("five lines", numbered(5), ["line 01", "line 02", "line 03", "line 04", "line 05"]),
            ("six lines", numbered(6),
             ["line 02", "line 03", "line 04", "line 05", "line 06"]),
            ("long wrapped link",
             "intro\n[Example](https://example.com/a/very/long/path/that/wraps/over/the/width) and more\n",
             ["intro", "[Example]", "over/the/width)"]),
        ]
        for name, body, wants in cases:
            text = frame_after_paste(body, home, config, store)
            for want in wants:
                check("%s: %s visible without a keypress" % (name, want),
                      want in text, text)
            check("%s: footer is still present" % name,
                  "Enter send" in text, text)
    finally:
        shutil.rmtree(home, ignore_errors=True)

    print()
    if FAILURES:
        print("FAILED: " + ", ".join(FAILURES))
        return 1
    print("ALL COMPOSER PTY CHECKS PASSED")
    return 0


if __name__ == "__main__":
    sys.exit(main())
