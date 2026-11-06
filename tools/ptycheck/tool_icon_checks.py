#!/usr/bin/env python3
"""Headless PTY checks for the reversible tool-icon experiment.

A fixture session supplies genuine historical HAL tool records, so the
checks exercise the same activity renderer as a resumed conversation
without executing a tool. The suite compares the default classic rows
with emoji rows, checks the gutter and failure priority, switches back,
and renders a narrow 80x9 frame.

Usage:

    go build -o /tmp/opencode/hal9001 ./cmd/hal9001
    HAL9001_BINARY=/tmp/opencode/hal9001 python3 tools/ptycheck/tool_icon_checks.py
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
SESSION = "icons"
NARROW_SESSION = "iconsnarrow"
ICONS = ("🔎", "📄", "💡", "📝", "✏️", "📦", "💻", "🔪", "🚨")


def check(name, ok, detail=""):
    print(("[ok  ] " if ok else "[FAIL] ") + name + ("" if ok else "  " + str(detail)))
    if not ok:
        FAILURES.append(name)


def write_config(path, theme="HAL Classic"):
    with open(path, "w") as fh:
        json.dump({
            "provider": "openai-compatible",
            "endpoint": "http://127.0.0.1:9/v1",
            "api_key": "test-key",
            "models": [{"id": "MiniMax-M3.1-Flash"}],
            "active_model": "MiniMax-M3.1-Flash",
            "theme": theme,
        }, fh)


def message(role, content, turn, extra=None):
    out = {"role": role, "content": content,
           "ts": "2026-11-06T10:00:00Z", "turn_id": turn}
    if extra:
        out.update(extra)
    return out


def execution(tool, action, status, mode="666", target="", exit_code=None,
              summary="", duration_ms=12, size=0, turn_id="tools"):
    out = {
        "turn_id": turn_id,
        "call_id": "call-" + tool + "-" + action + "-" + status,
        "tool": tool, "action": action, "mode": mode, "status": status,
        "duration_ms": duration_ms, "bytes": size, "summary": summary,
    }
    if target:
        out["target"] = target
    if exit_code is not None:
        out["exit_code"] = exit_code
    return out


def write_fixtures(store):
    ledger = {
        "version": 1,
        "session_id": SESSION,
        "turn_id": "tools",
        "executions": [
            execution("web_search", "web_search", "success", mode="444",
                      summary="5 results · firecrawl", size=512),
            execution("firecrawl_scrape", "firecrawl_scrape", "success",
                      mode="444", target="https://example.com/article",
                      size=15124),
            execution("mpm_memory", "query", "success", mode="444",
                      summary="3 results"),
            execution("read", "read", "success", target="README.md",
                      size=2048, summary="2.0 KiB"),
            execution("write", "write", "success", target="notes.md",
                      size=13, summary="13 bytes", duration_ms=2000),
            execution("edit", "edit", "success", target="config.yaml",
                      size=9, summary="9 bytes"),
            execution("bash", "bash", "success", exit_code=0,
                      target="/tmp/work", summary="exit 0", size=64),
            execution("bash", "bash", "failure", exit_code=17,
                      target="/tmp/work", summary="exit 17", size=64),
        ],
    }
    with open(os.path.join(store, SESSION + ".jsonl"), "w") as fh:
        fh.write(json.dumps(message("user", "What ran?", "tools")) + "\n")
        fh.write(json.dumps(message("assistant", "The tools ran.", "tools",
                                    {"tool_ledger": ledger})) + "\n")

    narrow = {
        "version": 1,
        "session_id": NARROW_SESSION,
        "turn_id": "narrow",
        "executions": [
            execution("edit", "edit", "success", target="notes.md",
                      size=8, summary="8 bytes", turn_id="narrow"),
        ],
    }
    with open(os.path.join(store, NARROW_SESSION + ".jsonl"), "w") as fh:
        fh.write(json.dumps(message("user", "Edit it.", "narrow")) + "\n")
        fh.write(json.dumps(message("assistant", "Edited.", "narrow",
                                    {"tool_ledger": narrow})) + "\n")


def display_width(text):
    """Terminal cells for the fixture's text, including ✏️'s VS16."""
    width = 0
    for ch in text:
        if unicodedata.combining(ch) or ch in ("\u200d", "\ufe0f"):
            continue
        width += 2 if unicodedata.east_asian_width(ch) in ("W", "F") else 1
    # ✏ is East Asian Neutral but becomes two cells with VS16.
    if "\ufe0f" in text and "✏" in text:
        width += 1
    return width


def column(text, fragment):
    at = text.find(fragment)
    if at < 0:
        return -1
    return display_width(text[:at])


def capture(script, cols, rows, config, theme="HAL Classic",
            session=SESSION):
    write_config(config, theme)
    raw = pty_drive.run(script, cols=cols, rows=rows, timeout=5.0,
                        fresh=False, session=session,
                        args=["--workspace", WORKSPACE])
    return render(raw, cols, rows).text()


def known_rows(text, names):
    lines = text.split("\n")
    out = []
    for line in lines:
        for name in names:
            if name in line:
                out.append((name, line))
                break
    return out


def main():
    global WORKSPACE
    if not os.path.isfile(BINARY):
        print("missing HAL9001 binary:", BINARY)
        return 2
    base = os.environ.get("HAL9001_PTY_TMPDIR", "/tmp/opencode")
    os.makedirs(base, exist_ok=True)
    root = tempfile.mkdtemp(prefix="hal9001-tool-icons-", dir=base)
    home = os.path.join(root, "config")
    store = os.path.join(root, "sessions")
    WORKSPACE = os.path.join(root, "HAL")
    os.makedirs(home)
    os.makedirs(store)
    os.makedirs(WORKSPACE)
    config = os.path.join(home, "config.json")
    write_config(config)
    write_fixtures(store)

    pty_drive.BINARY = BINARY
    pty_drive.CONFIG = config
    pty_drive.HOME = home
    pty_drive.STORE = store
    os.environ["HAL9001_MPM_BIN"] = os.path.join(root, "missing-mpm")

    try:
        classic = capture([(2.0, b"")], 80, 24, config)
        for name in ("web_search", "firecrawl_scrape", "mpm_memory", "read",
                     "write", "edit", "bash"):
            check("classic: %s keeps the gear" % name,
                  "⚙ " + name in classic, classic)
        check("classic: no emoji icons",
              not any(icon in classic for icon in ICONS), classic)

        emoji = capture([(2.0, b"/tool-icons emoji\r"), (0.8, b"")],
                        80, 24, config)
        cases = [
            ("web_search", "🔎"), ("firecrawl_scrape", "📄"),
            ("mpm_memory", "💡"), ("read", "📄"), ("write", "📝"),
            ("edit", "✏️"), ("bash", "💻"),
        ]
        for name, icon in cases:
            # The unknown fallback is a one-cell gear padded to the same
            # two-cell icon gutter as the emoji.
            gap = "  " if icon == "⚙" else " "
            check("emoji: %s uses %s" % (name, icon),
                  icon + gap + name in emoji, emoji)
        bash_lines = [line for line in emoji.split("\n") if "bash" in line]
        failure_line = next((line for line in bash_lines if "🚨 bash" in line), "")
        success_line = next((line for line in bash_lines if "💻 bash" in line), "")
        check("emoji: failed bash gets the failure icon",
              "failed" in failure_line and "exit 17" in failure_line and
              "completed" in success_line and
              not any("💻 bash" in line and "failed" in line
                      for line in bash_lines), emoji)
        check("emoji: status details survive",
              "failed" in emoji and "exit 17" in emoji and
              "completed" in emoji and "444" in emoji and "666" in emoji and
              "5 results" in emoji and "firecrawl" in emoji and
              "14.8 KiB" in emoji and "2s" in emoji, emoji)

        known = known_rows(emoji, [name for name, _ in cases])
        bad = []
        for name, line in known:
            if column(line, name) != 5:
                bad.append((name, column(line, name), line))
        check("emoji: names start in the same gutter", not bad, str(bad))
        for name, line in known:
            if display_width(line) > 80:
                bad.append((name, display_width(line), line))
        check("emoji: rows fit 80 columns", not bad, str(bad))

        # The current setting is shown without an argument.
        status = capture([(2.0, b"/tool-icons emoji\r"),
                          (0.8, b"/tool-icons\r"), (0.8, b"")],
                         80, 24, config)
        check("no argument displays the current setting",
              "Tool icons: emoji" in status, status)

        # Switching back immediately restores the classic rows.
        back = capture([(2.0, b"/tool-icons emoji\r"),
                        (0.6, b"/tool-icons classic\r"), (0.8, b"")],
                       80, 24, config)
        check("classic restores the gear",
              "⚙ write" in back and "📝 write" not in back, back)
        check("classic restores the failure gear",
              "⚙ bash" in back and "🚨 bash" not in back, back)

        # Narrow frame: one edit row, with its variation selector intact.
        narrow = capture([(2.0, b"/tool-icons emoji\r"), (0.8, b"")],
                         80, 9, config, session=NARROW_SESSION)
        check("80x9: the edit icon is visible", "✏️ edit" in narrow, narrow)
        check("80x9: the variation selector survives", "\u270f\ufe0f" in narrow,
              narrow)
        narrow_row = next((line for line in narrow.split("\n")
                           if "✏️ edit" in line), "")
        check("80x9: the row fits", display_width(narrow_row) <= 80, narrow_row)

        # Themes: the glyphs and the alignment survive every palette.
        for theme in ("HAL Classic", "Midnight", "Amber", "Paper",
                      "Monochrome"):
            themed = capture([(2.0, b"/tool-icons emoji\r"), (0.8, b"")],
                             80, 24, config, theme=theme)
            check("theme %s: icons render" % theme,
                  "📝 write" in themed and "🚨 bash" in themed, themed)
            rows = known_rows(themed, ["write", "edit", "read"])
            check("theme %s: names keep the gutter" % theme,
                  all(column(line, name) == 5 for name, line in rows),
                  str(rows))
    finally:
        shutil.rmtree(root, ignore_errors=True)

    print()
    if FAILURES:
        print("FAILED: " + ", ".join(FAILURES))
        return 1
    print("ALL TOOL-ICON PTY CHECKS PASSED")
    return 0


if __name__ == "__main__":
    sys.exit(main())
