#!/usr/bin/env python3
"""Behaviour checks for HAL9001 in a real pseudo-terminal.

Requires a built binary and a provider that answers locally. The default
setup points HAL9001 at a local OpenAI-compatible stub:

    python3 tools/ptycheck/fake_provider.py &
    HAL9001_CONFIG=/tmp/hal9001-test.json \\
    HAL9001_BINARY=./hal9001 python3 tools/ptycheck/checks.py

Every check prints ok or FAIL; the exit status is non-zero on failure.
"""
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from pty_drive import run
from termsim import render

FAILURES = []


def check(name, ok, detail=""):
    print(("[ok  ] " if ok else "[FAIL] ") + name + ("" if ok else "  " + detail))
    if not ok:
        FAILURES.append(name)


def screen(raw, cols=80, rows=24):
    return render(raw, cols, rows)


def main():
    # Start-up: the alternate screen, a fixed header and a fixed footer.
    s = screen(run([(2.0, b"")], timeout=4.0))
    lines = s.text().split("\n")
    check("starts in the alternate screen", s.alt)
    check("header on the first row", "HAL9001" in lines[0], repr(lines[0]))
    check("footer at the bottom", "444 READ ONLY" in lines[-2], repr(lines[-2:]))
    check("bracketed paste enabled", "bracketed-paste" in s.modes)
    check("the frame fits the terminal", len(lines) == 24, str(len(lines)))

    # Ctrl+C leaves the alternate screen and restores the terminal state.
    raw = run([(2.0, b"\x03")], timeout=5.0)
    text = raw.decode("utf-8", "replace")
    check("alternate screen reset on exit", "\x1b[?1049l" in text)
    check("bracketed paste reset on exit", "\x1b[?2004l" in text)
    check("cursor shown again on exit", "\x1b[?25h" in text)
    check("mouse never captured",
          "\x1b[?1000h" not in text and "\x1b[?1006h" not in text)

    # Enter sends the message.
    text = screen(run([(2.0, b"hello hal\r")], timeout=6.0)).text()
    check("Enter sends", "You > hello hal" in text, text)

    # Ctrl+J breaks the line instead of sending.
    text = screen(run([(2.0, b"first\x0a"), (0.5, b"second")], timeout=5.0)).text()
    check("Ctrl+J keeps the text in the composer",
          "first" in text and "second" in text, text)
    check("Ctrl+J does not send", "You >" not in text, text)

    # Alt+Enter (ESC CR) breaks the line too.
    text = screen(run([(2.0, b"first\x1b\r"), (0.5, b"second")], timeout=5.0)).text()
    check("Alt+Enter does not send", "You >" not in text, text)

    # Ctrl+S still sends.
    text = screen(run([(2.0, b"via ctrl s\x13")], timeout=6.0)).text()
    check("Ctrl+S sends", "You > via ctrl s" in text, text)

    # Slash commands run locally.
    text = screen(run([(2.0, b"/help\r")], timeout=5.0)).text()
    check("/help opens on Enter", "Keys and commands" in text, text)
    check("/help lists the commands", "/copy" in text and "Ctrl+J" in text, text)
    text = screen(run([(2.0, b"/help\r"), (1.5, b"\x1b")], timeout=6.0)).text()
    check("Esc closes the help panel", "Keys and commands" not in text, text)

    # Ctrl+K opens the model picker.
    text = screen(run([(2.0, b"\x0b")], timeout=5.0)).text()
    check("Ctrl+K opens the model picker", "Models (Enter switches" in text, text)

    # A pasted block stays in the composer.
    paste = b"\x1b[200~line one\nline two\n\n  indented\x1b[201~"
    text = screen(run([(2.0, paste)], timeout=5.0)).text()
    check("paste stays in the composer", "line one" in text and "indented" in text, text)
    check("paste does not submit", "You >" not in text, text)

    # Common terminal sizes keep the frame the right size.
    for cols, rows in [(120, 40), (160, 50)]:
        s = screen(run([(2.0, b"")], cols=cols, rows=rows, timeout=4.0), cols, rows)
        lines = s.text().split("\n")
        check(f"{cols}x{rows}: header stays on row one", "HAL9001" in lines[0], repr(lines[0]))
        check(f"{cols}x{rows}: footer stays at the bottom",
              "444 READ ONLY" in lines[-2], repr(lines[-2:]))
        check(f"{cols}x{rows}: no terminal scroll", len(lines) == rows, str(len(lines)))

    # A resize keeps the layout intact.
    s = screen(run([(2.0, b"hi"), (1.5, ("resize", 60, 15))], timeout=9.0), 60, 15)
    lines = s.text().split("\n")
    check("resize keeps the header", "HAL9001" in lines[0], repr(lines[0]))
    check("resize keeps the footer", "444 READ ONLY" in lines[-2], repr(lines[-2:]))
    check("resize leaves no leftover rows", len(lines) == 15, str(len(lines)))

    print()
    if FAILURES:
        print("FAILED: " + ", ".join(FAILURES))
        return 1
    print("ALL PTY CHECKS PASSED")
    return 0


if __name__ == "__main__":
    sys.exit(main())
