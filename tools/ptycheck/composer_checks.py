#!/usr/bin/env python3
"""Headless PTY checks for immediate multiline-paste visibility.

Every case renders the first frame after the paste, without sending an
arrow key or any other follow-up event. The suite also renders the frame
after Up so the two can be compared, and it exercises the short-terminal
frame budget where the reported defect lived.

It covers both true bracketed paste and the alternate input paths a
terminal can use: a raw payload with LF line endings and a bracketed
payload with CRLF line endings.

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
SESSION = "paste"

FIRST = "First line: testing HAL's multiline composer."
SECOND = "Second line: both lines should appear immediately."


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


def paste_payload(body, bracketed=True, crlf=False):
    if crlf:
        body = body.replace("\n", "\r\n")
    data = body.encode()
    if bracketed:
        return b"\x1b[200~" + data + b"\x1b[201~"
    return data


def frame_after_paste(body, home, config, store, cols=60, rows=30,
                      bracketed=True, crlf=False, prefix=b"", up=False,
                      args=None):
    """Return the rendered screen after a paste and optional Up.

    With up=False the screen is the first frame after the paste: nothing
    else is sent before it is captured.
    """
    pty_drive.BINARY = BINARY
    pty_drive.CONFIG = config
    pty_drive.HOME = home
    pty_drive.STORE = store
    payload = paste_payload(body, bracketed=bracketed, crlf=crlf)
    script = [(2.0, b"")]
    if prefix:
        script.append((0.05, prefix))
    script.append((0.05, payload))
    script.append((0.35, b""))
    if up:
        script += [(0.05, b"\x1b[A"), (0.35, b"")]
    raw = pty_drive.run(script, cols=cols, rows=rows, timeout=3.0,
                        fresh=True, session=SESSION, args=args)
    return render(raw, cols, rows).text()


def check_visible(name, text, wants, detail=None):
    for want in wants:
        check("%s: %s visible without a keypress" % (name, want),
              want in text, text if detail is None else detail)


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

        # Full-size terminal: the original matrix.
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
            check_visible(name, text, wants)
            check("%s: footer is still present" % name,
                  "Enter send" in text, text)

        # The exact field report on a short terminal. The frame has to
        # trade optional chrome for the draft's own rows; the first frame
        # must already show both lines.
        short_cases = [
            ("short 8 rows", 8, False),
            ("short 9 rows", 9, False),
            ("short 9 rows trailing newline", 9, True),
            ("short 10 rows trailing newline", 10, True),
        ]
        for name, rows, trailing in short_cases:
            body = FIRST + "\n" + SECOND + ("\n" if trailing else "")
            text = frame_after_paste(body, home, config, store,
                                     cols=80, rows=rows)
            check_visible(name, text, [FIRST, SECOND])

        # Wrapped text at the short size: the first line and the wrapped
        # link both fit inside the five-row composer.
        wrapped = frame_after_paste(
            FIRST + "\n[Example](https://example.com/" + "segment/" * 12 + ")",
            home, config, store, cols=80, rows=9)
        check_visible("short 9 rows wrapped", wrapped,
                      ["First line", "[Example]", "segment/"])

        # Same paste, Up afterwards: the first frame already showed both,
        # and Up must not make the first line disappear.
        before = frame_after_paste(FIRST + "\n" + SECOND, home, config, store,
                                   cols=80, rows=9)
        after = frame_after_paste(FIRST + "\n" + SECOND, home, config, store,
                                  cols=80, rows=9, up=True)
        check_visible("short 9 rows before Up", before, [FIRST, SECOND])
        check_visible("short 9 rows after Up", after, [FIRST, SECOND])

        # Alternate input paths at the same short size.
        raw = frame_after_paste(FIRST + "\n" + SECOND, home, config, store,
                                cols=80, rows=9, bracketed=False)
        check_visible("raw LF path", raw, [FIRST, SECOND])
        crlf = frame_after_paste(FIRST + "\n" + SECOND, home, config, store,
                                 cols=80, rows=9, crlf=True)
        check_visible("bracketed CRLF path", crlf, [FIRST, SECOND])

        # Existing draft at the short size.
        existing = frame_after_paste("one\ntwo", home, config, store,
                                     cols=80, rows=9,
                                     prefix=b"start ")
        check_visible("existing draft", existing, ["start one", "two"])

        # A long paste keeps the caret's window at the short size.
        five = frame_after_paste(numbered(5), home, config, store,
                                 cols=80, rows=9)
        check_visible("five lines at 80x9", five,
                      ["line 01", "line 02", "line 03", "line 04", "line 05"])
        ten = frame_after_paste(numbered(10), home, config, store,
                                cols=80, rows=9)
        check_visible("ten lines at 80x9 keeps the caret window", ten,
                      ["line 06", "line 07", "line 08", "line 09", "line 10"])
        check("ten lines at 80x9: first line is outside the caret window",
              "line 01 of pasted text" not in ten, ten)
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
