#!/usr/bin/env python3
"""Isolated PTY checks for the terminal presentation polish.

This suite owns its config, home, store, workspace and session fixture.
It needs no provider: the conversation is loaded from a static session
with a verified tool ledger. It never shares a temporary path with
checks.py or help_checks.py.

Usage:

    go build -o /tmp/opencode/hal9001 ./cmd/hal9001
    HAL9001_BINARY=/tmp/opencode/hal9001 python3 tools/ptycheck/polish_checks.py
"""
import json
import os
import shutil
import sys
import tempfile
import unicodedata

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, HERE)
import pty_drive  # noqa: E402
from termsim import render  # noqa: E402

FAILURES = []
BINARY = os.environ.get("HAL9001_BINARY", "/tmp/opencode/hal9001")
THEMES = ["HAL Classic", "Midnight", "Amber", "Paper", "Monochrome"]
SIZES = [(60, 20), (80, 24), (120, 30), (170, 38), (200, 45)]


def check(name, ok, detail=""):
    print(("[ok  ] " if ok else "[FAIL] ") + name + ("" if ok else "  " + str(detail)))
    if not ok:
        FAILURES.append(name)


def cell_width(text):
    width = 0
    for ch in text:
        if unicodedata.combining(ch) or ch in ("\u200d", "\ufe0f"):
            continue
        width += 2 if unicodedata.east_asian_width(ch) in ("W", "F") else 1
    return width


def cell_column(text, fragment):
    at = text.find(fragment)
    if at < 0:
        return -1
    return cell_width(text[:at])


def content_column(cols):
    # The two-cell red eye plus one separator sit in the gutter.
    return 3


def content_width(cols):
    # Content starts after the three-cell gutter; two cells stay at
    # the right edge.
    return cols - 5


def row_with(screen, fragment):
    for line in screen.text().split("\n"):
        if fragment in line:
            return line
    return ""


def blank_between_indices(lines, a, b):
    if a < 0 or b <= a:
        return -1
    return sum(1 for line in lines[a + 1:b] if not line.strip())


def blank_between(lines, first, second):
    try:
        a = next(i for i, line in enumerate(lines) if first in line)
        b = next(i for i, line in enumerate(lines) if i > a and second in line)
    except StopIteration:
        return -1
    return blank_between_indices(lines, a, b)


def no_overflow(screen, cols):
    return all(cell_width(line) <= cols for line in screen.text().split("\n"))


def ansi_before(raw, text, span=96):
    at = raw.find(text.encode())
    if at < 0:
        return b""
    return raw[max(0, at - span):at]


def has_ansi_colour(raw, text):
    window = ansi_before(raw, text)
    return b"\x1b[38;5;" in window or b"\x1b[38;2;" in window


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


def write_fixture(home, store):
    ledger = {
        "version": 1,
        "session_id": "polish",
        "turn_id": "turn-2",
        "executions": [
            {"turn_id": "turn-2", "call_id": "call-1", "tool": "write",
             "action": "write", "target": "notes/plan.md", "mode": "666",
             "status": "success", "duration_ms": 12, "bytes": 13,
             "summary": "13 bytes"},
            {"turn_id": "turn-2", "call_id": "call-2", "tool": "bash",
             "action": "bash", "mode": "666", "status": "failure",
             "exit_code": 17, "duration_ms": 41, "bytes": 64,
             "summary": "exit 17"},
            {"turn_id": "turn-2", "call_id": "call-3", "tool": "bash",
             "action": "bash", "mode": "666", "status": "cancelled",
             "duration_ms": 900, "summary": "cancelled"},
            {"turn_id": "turn-2", "call_id": "call-4", "tool": "bash",
             "action": "bash", "mode": "444", "status": "refused",
             "summary": "refused"},
        ],
    }
    attribution = {
        "turn_id": "turn-2", "source": "auto",
        "skills": [{
            "id": "skill:system-prompt-writer-v1.0.1",
            "name": "system-prompt-writer", "version": "1.0.1",
            "source": "auto",
        }],
    }

    def message(role, content, turn, extra=None):
        out = {"role": role, "content": content,
               "ts": "2026-11-05T10:00:00Z", "turn_id": turn}
        if extra:
            out.update(extra)
        return out

    first = (
        "## Plan\n\n"
        "A short paragraph with **strong** text and `code`, "
        "plus 日本語 and e\u0301 for display-cell alignment.\n\n"
        "- first item\n- second item\n\n"
        "```go\nfmt.Println(\"HAL\")\n```\n\n"
        "| Stage | State |\n|---|---|\n"
        "| Foundation | verified |\n| Polish | in progress |"
    )
    second = "The command completed. One step failed and one was cancelled."
    with open(os.path.join(store, "polish.jsonl"), "w") as fh:
        for item in [
            message("user", "Show me the plan.", "turn-1"),
            message("assistant", first, "turn-1"),
            message("user", "What ran?", "turn-2"),
            message("assistant", second, "turn-2",
                    {"tool_ledger": ledger, "attribution": attribution}),
        ]:
            fh.write(json.dumps(item) + "\n")
    with open(os.path.join(store, "widthtest.jsonl"), "w") as fh:
        for item in [
            message("user", "width test", "width-1"),
            message("assistant", "Ruler:\n\n" + "X" * 240, "width-1"),
        ]:
            fh.write(json.dumps(item) + "\n")
    with open(os.path.join(home, "usage.json"), "w") as fh:
        json.dump({"sessions": {"polish": {
            "input_tokens": 5100, "output_tokens": 13100,
            "completed_turns": 2,
            "history": [
                {"input_tokens": 4200, "output_tokens": 12800,
                 "duration_ms": 140000, "reported": True},
                {"input_tokens": 900, "output_tokens": 300,
                 "duration_ms": 2500, "reported": True},
            ],
        }}}, fh)


def main():
    if not os.path.isfile(BINARY):
        print("missing HAL9001 binary:", BINARY)
        return 2
    base = os.environ.get("HAL9001_PTY_TMPDIR", "/tmp/opencode")
    os.makedirs(base, exist_ok=True)
    root = tempfile.mkdtemp(prefix="hal9001-polish-", dir=base)
    home = os.path.join(root, "config")
    store = os.path.join(root, "sessions")
    workspace = os.path.join(root, "HAL")
    os.makedirs(home)
    os.makedirs(store)
    os.makedirs(workspace)
    config = os.path.join(home, "config.json")
    write_config(config, "http://127.0.0.1:9/v1")
    write_fixture(home, store)

    pty_drive.BINARY = BINARY
    pty_drive.CONFIG = config
    pty_drive.HOME = home
    pty_drive.STORE = store
    pty_drive.SESSION = "polish"
    os.environ["HAL9001_MPM_BIN"] = os.path.join(root, "missing-mpm")

    def capture(script, cols, rows, theme="HAL Classic", session="polish"):
        write_config(config, "http://127.0.0.1:9/v1")
        with open(config) as fh:
            data = json.load(fh)
        data["theme"] = theme
        with open(config, "w") as fh:
            json.dump(data, fh)
        raw = pty_drive.run(script, cols=cols, rows=rows, timeout=5.0,
                            fresh=False, session=session,
                            args=["--workspace", workspace])
        return raw, render(raw, cols, rows)

    try:
        for cols, rows in SIZES:
            _, top = capture([(2.2, b"\x1b[H"), (0.7, b"")], cols, rows)
            _, bottom = capture([(2.2, b"")], cols, rows)
            ccol, cwid = content_column(cols), content_width(cols)
            label = "%dx%d" % (cols, rows)

            plain_top = top.text()
            plain_bottom = bottom.text()
            check("%s: no speaker labels" % label,
                  "You >" not in plain_top + plain_bottom and
                  "HAL >" not in plain_top + plain_bottom,
                  plain_top + plain_bottom)
            check("%s: rows fit the terminal" % label,
                  no_overflow(top, cols) and no_overflow(bottom, cols),
                  plain_top + plain_bottom)
            _, ruler = capture([(2.2, b"")], cols, rows, session="widthtest")
            ruler_rows = [line for line in ruler.text().split("\n")
                          if line.strip() and set(line.strip()) == {"X"}]
            expected_width = content_width(cols)
            check("%s: long unbroken text uses the full content width" % label,
                  any(cell_width(line.strip()) == expected_width for line in ruler_rows),
                  ruler.text())

            user = row_with(top, "Show me the plan.")
            if user:
                check("%s: user text starts at the content column" % label,
                      cell_column(user, "Show me the plan.") == ccol, user)
            else:
                check("%s: first user turn is visible at the top" % label, False, plain_top)

            for fragment, expected, name in [
                ("Plan", ccol, "assistant heading"),
                ("A short paragraph", ccol, "assistant paragraph"),
                ("first item", ccol + 2, "assistant list item"),
                ("fmt.Println", ccol, "assistant code"),
                ("Stage", ccol + 1, "assistant table"),
            ]:
                row = row_with(top, fragment)
                if row:
                    check("%s: %s starts at cell %d" % (label, name, expected),
                          cell_column(row, fragment) == expected, row)
                elif fragment in ("fmt.Println", "Stage") and cols == 60:
                    pass
                else:
                    check("%s: %s is visible" % (label, name), False, plain_top)

            eye_row = row_with(top, "🔴")
            check("%s: assistant eye is in the gutter" % label,
                  bool(eye_row) and cell_column(eye_row, "🔴") == ccol - 3, plain_top)
            expected_eyes = 1
            if "The command completed." in plain_top:
                expected_eyes += 1
            check("%s: one eye for each visible assistant turn" % label,
                  plain_top.count("\n🔴") == expected_eyes, plain_top)

            question = row_with(bottom, "What ran?")
            answer = row_with(bottom, "The command completed.")
            tool = row_with(bottom, "📝 write")
            meta = row_with(bottom, "2s · 900 in / 300 out · skill: system-prompt-writer")
            check("%s: later user text keeps the column" % label,
                  bool(question) and cell_column(question, "What ran?") == ccol, plain_bottom)
            check("%s: later assistant text keeps the column" % label,
                  bool(answer) and cell_column(answer, "The command completed.") == ccol, plain_bottom)
            check("%s: tool activity sits under the answer" % label,
                  bool(tool) and cell_column(tool, "📝 write") == ccol and
                  plain_bottom.find("The command completed.") <
                  plain_bottom.find("📝 write"), plain_bottom)
            check("%s: failed, cancelled and refused tools keep their outcome" % label,
                  "🚨 bash · failed · exit 17 · 666" in plain_bottom and
                  "💻 bash · cancelled · 666" in plain_bottom and
                  "💻 bash · refused · 444" in plain_bottom, plain_bottom)
            if meta:
                check("%s: statistics end at the response edge" % label,
                      cell_column(meta, meta.strip()) + cell_width(meta.strip()) ==
                      ccol + cwid, meta)
            else:
                check("%s: statistics line is visible" % label, False, plain_bottom)
            check("%s: statistics carry the skill credit" % label,
                  bool(meta) and "skill: system-prompt-writer" in meta, plain_bottom)

            if (cols, rows) == (80, 24):
                lines = plain_bottom.split("\n")
                tool_indices = [i for i, line in enumerate(lines)
                                if line.strip().startswith(
                                    ("⚙", "🔎", "📄", "💡", "📝", "✏️",
                                     "📦", "💻", "🔪", "🚨", "📍"))]
                check("80x24: one blank between user and answer",
                      blank_between(lines, "What ran?", "The command completed.") == 1,
                      plain_bottom)
                check("80x24: one blank between answer and tool activity",
                      blank_between(lines, "The command completed.", "📝") == 1,
                      plain_bottom)
                check("80x24: tool rows have no blank rows between them",
                      len(tool_indices) == 4 and
                      all(blank_between_indices(lines, tool_indices[i], tool_indices[i + 1]) == 0
                          for i in range(len(tool_indices) - 1)),
                      plain_bottom)
                check("80x24: statistics follow the last tool immediately",
                      blank_between(lines, "💻 bash · refused",
                                    "2s · 900 in / 300 out") == 0,
                      plain_bottom)

            if (cols, rows) == (170, 38):
                lines = plain_top.split("\n")
                check("170x38: one blank between user and assistant",
                      blank_between(lines, "Show me the plan.", "Plan") == 1,
                      plain_top)
                check("170x38: list items remain compact",
                      blank_between(lines, "first item", "second item") == 0,
                      plain_top)
                check("170x38: one blank between turns",
                      blank_between(lines, "2m 20s", "What ran?") == 1,
                      plain_top)

        # A live resize must recalculate width, wrapping, tables, tool
        # truncation and statistics without restarting the application.
        raw80 = pty_drive.run(
            [(2.2, b""), (0.5, ("resize", 80, 24)), (0.8, b"")],
            cols=170, rows=38, timeout=5.5, fresh=False, session="widthtest",
            args=["--workspace", workspace])
        frame80 = render(raw80, 80, 24)
        text80 = frame80.text()
        ruler80 = [line for line in text80.split("\n")
                   if line.strip() and set(line.strip()) == {"X"}]
        check("resize: 170 -> 80 reflows to 75 content cells",
              no_overflow(frame80, 80) and
              any(cell_width(line.strip()) == content_width(80) for line in ruler80), text80)
        check("resize: 170 -> 80 keeps the footer and composer",
              "444 READ ONLY" in text80 and "Enter send" in text80, text80)

        raw170 = pty_drive.run(
            [(2.2, b""), (0.5, ("resize", 80, 24)),
             (0.5, ("resize", 170, 38)), (0.8, b"")],
            cols=170, rows=38, timeout=5.5, fresh=False, session="widthtest",
            args=["--workspace", workspace])
        frame170 = render(raw170, 170, 38)
        text170 = frame170.text()
        ruler170 = [line for line in text170.split("\n")
                    if line.strip() and set(line.strip()) == {"X"}]
        check("resize: 170 -> 80 -> 170 returns to full width",
              no_overflow(frame170, 170) and
              any(cell_width(line.strip()) == content_width(170) for line in ruler170), text170)
        check("resize: live round-trip keeps the left content column",
              any(cell_column(line, "X") == content_column(170) for line in ruler170), text170)
        check("resize: live round-trip keeps footer and composer",
              "444 READ ONLY" in text170 and "Enter send" in text170, text170)

        # Theme-aware red eye and the same readable transcript on every theme.
        for theme in THEMES:
            raw, frame = capture([(2.2, b"")], 80, 24, theme)
            text = frame.text()
            check("theme %s: transcript renders" % theme,
                  "🔴" in text and "💻 bash" in text and "What ran?" in text, text)
            check("theme %s: the eye is coloured" % theme,
                  has_ansi_colour(raw, "🔴"), theme)
            check("theme %s: rows fit" % theme, no_overflow(frame, 80), text)

        # Slash suggestions: open, dismiss, keep the draft, reopen on edit.
        _, opened = capture([(2.2, b"/"), (0.6, b"")], 80, 24)
        check("suggestions: / opens the panel",
              "Show available commands" in opened.text(), opened.text())
        _, closed = capture([(2.2, b"/"), (0.4, b"\x1b"), (0.6, b"")], 80, 24)
        check("suggestions: Esc closes the panel",
              "Show available commands" not in closed.text(), closed.text())
        check("suggestions: Esc keeps the draft",
              any(line.strip() == "/" for line in closed.text().split("\n")),
              closed.text())
        _, edited = capture([(2.2, b"/"), (0.4, b"\x1b"), (0.2, b"h"), (0.6, b"")], 80, 24)
        check("suggestions: editing reopens the panel",
              "Show available commands" in edited.text(), edited.text())
        _, edited_closed = capture(
            [(2.2, b"/"), (0.4, b"\x1b"), (0.2, b"h"), (0.4, b"\x1b"), (0.6, b"")],
            80, 24)
        check("suggestions: the second Esc also closes",
              "Show available commands" not in edited_closed.text(), edited_closed.text())
        check("suggestions: the edited draft survives",
              any(line.strip() == "/h" for line in edited_closed.text().split("\n")),
              edited_closed.text())

        _, help_open = capture([(2.2, b"/help\r"), (0.7, b"")], 80, 24)
        check("/help: opens the reference", "Keys and commands" in help_open.text(), help_open.text())
        _, help_closed = capture([(2.2, b"/help\r"), (0.6, b"\x1b"), (0.6, b"")], 80, 24)
        check("/help: Esc closes it",
              "Keys and commands" not in help_closed.text(), help_closed.text())

        # The permission footer still switches, and its red warning is intact.
        raw, enabled = capture([(2.2, b"\x10"), (0.5, b"\x19"), (0.7, b"")], 80, 24)
        check("666: enabling changes the footer",
              "666 UNRESTRICTED" in enabled.text(), enabled.text())
        check("666: the indicator is coloured",
              has_ansi_colour(raw, "666 UNRESTRICTED"), "colour missing")
        _, disabled = capture(
            [(2.2, b"\x10"), (0.5, b"\x19"), (0.4, b"\x10"), (0.4, b"\x0e"), (0.7, b"")],
            80, 24)
        check("666: disabling returns the footer to 444",
              "444 READ ONLY" in disabled.text(), disabled.text())
    finally:
        shutil.rmtree(root, ignore_errors=True)

    print()
    if FAILURES:
        print("FAILED: " + ", ".join(FAILURES))
        return 1
    print("ALL POLISH PTY CHECKS PASSED")
    return 0


if __name__ == "__main__":
    sys.exit(main())
