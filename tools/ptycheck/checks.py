#!/usr/bin/env python3
"""Behaviour checks for HAL9001 in a real pseudo-terminal.

Requires a built binary and a provider that answers locally. The default
setup points HAL9001 at a local OpenAI-compatible stub:

    python3 tools/ptycheck/fake_provider.py &
    HAL9001_CONFIG=/tmp/hal9001-test.json \\
    HAL9001_BINARY=./hal9001 python3 tools/ptycheck/checks.py

Every check prints ok or FAIL; the exit status is non-zero on failure.
"""
import json
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from pty_drive import HOME, run
from termsim import render

FAILURES = []


def check(name, ok, detail=""):
    print(("[ok  ] " if ok else "[FAIL] ") + name + ("" if ok else "  " + detail))
    if not ok:
        FAILURES.append(name)


def screen(raw, cols=80, rows=24):
    return render(raw, cols, rows)


def write_scroll_fixture():
    """A conversation long enough to scroll, stored on disk so the capture
    is a static frame rather than a stream of re-renders."""
    body = "\n\n".join(
        "## Section %d\n\n%s" % (i, ("This is paragraph %d of a deliberately long reply that exists to test scrolling. " % i) * 3)
        for i in range(1, 16)
    )
    os.makedirs(os.path.join(HOME, "sessions"), exist_ok=True)
    path = os.path.join(HOME, "sessions", "scrollfixture.jsonl")
    with open(path, "w") as fh:
        fh.write(json.dumps({"role": "user", "content": "long answer please"}) + "\n")
        fh.write(json.dumps({"role": "assistant", "content": body}) + "\n")


def first_text(text, skip=2):
    lines = [l.strip() for l in text.split("\n") if l.strip()]
    return lines[skip] if len(lines) > skip else ""


def ui_checks():
    # Typing "/" opens the suggestion panel; nothing is submitted and the
    # conversation behind it stays visible.
    panel = screen(run([(2.0, b"hello\r"), (8.0, b"/"), (0.5, b"")], timeout=12.0)).text()
    check("commands: / opens suggestions", "Show available commands" in panel, panel)
    check("commands: the conversation stays visible",
          "I read your message" in panel, panel)
    check("commands: nothing submitted", "You > /" not in panel, panel)

    filtered = screen(run([(2.0, b"/ef")], timeout=5.0)).text()
    check("commands: typing filters", "/effort" in filtered and "/theme" not in filtered, filtered)

    completed = screen(run([(2.0, b"/ef"), (0.4, b"\t")], timeout=5.0)).text()
    check("commands: Tab completes", "/effort" in completed, completed)

    dismissed = screen(run([(2.0, b"/ef"), (0.4, b"\x1b")], timeout=5.0)).text()
    check("commands: Esc closes the panel", "Show available commands" not in dismissed, dismissed)
    check("commands: Esc keeps the draft", "/ef" in dismissed, dismissed)

    pasted = screen(run([(2.0, b"\x1b[200~/clear\x1b[201~")], timeout=5.0)).text()
    check("commands: pasted command is not executed",
          "Conversation cleared" not in pasted, pasted)




def scroll_checks():
    write_scroll_fixture()

    def capture(script, final=(80, 24), start=(80, 24), timeout=8.0):
        raw = run(script, cols=start[0], rows=start[1], timeout=timeout,
                  fresh=False, session="scrollfixture")
        return screen(raw, *final).text()

    bottom = capture([(2.0, b"")])
    check("scroll: starts at the end of the conversation",
          "Section 15" in bottom, bottom[:400])

    # Composer geometry against a full viewport: border, three text rows,
    # exactly one blank row between the conversation and the border.
    lines = bottom.split("\n")
    top = next((i for i, l in enumerate(lines) if l.startswith("╭")), -1)
    end = next((i for i, l in enumerate(lines) if l.startswith("╰")), -1)
    check("composer: bordered", top > 0 and end > top, bottom)
    check("composer: three text rows", end - top - 1 == 3, str(end - top - 1))
    check("composer: one blank row above",
          top > 0 and lines[top - 1].strip() == "" and lines[top - 2].strip() != "",
          "\n".join(lines[max(0, top - 3):top + 1]))

    top = capture([(2.0, b"\x1b[H")])
    check("scroll: Home reaches the beginning", "Section 1" in top, top[:400])
    check("scroll: the end is no longer visible", "Section 15" not in top, top[:400])

    end = capture([(2.0, b"\x1b[H"), (0.5, b"\x1b[F")])
    check("scroll: End returns to the end", "Section 15" in end, end[:400])

    paged = capture([(2.0, b"\x1b[5~\x1b[5~")])
    check("scroll: PgUp moves up", first_text(paged) != first_text(bottom), paged[:400])
    check("scroll: PgUp does not reach the top", "Section 1\n" not in paged, paged[:400])

    before = capture([(2.0, b"\x1b[5~\x1b[5~")])
    after = capture([(2.0, b"\x1b[5~\x1b[5~"), (0.5, ("resize", 60, 14)), (1.5, b"")],
                    final=(60, 14))
    check("scroll: shrinking keeps the reading position",
          first_text(before) == first_text(after),
          f"{first_text(before)!r} -> {first_text(after)!r}")


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

    ui_checks()
    scroll_checks()

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
