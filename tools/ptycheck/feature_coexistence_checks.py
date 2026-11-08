#!/usr/bin/env python3
"""Integration checks: the two features' commands and help coexist.

Both feature branches added slash commands and a help section. This
check proves that the merged build carries all of them, that the
suggestion panel still finds them, that /quit stays in the first
window, and that the help sections appear in the agreed order.

A transient notice is looked for in the whole byte stream rather than
in the final screen: a notice disappears again, and what is checked is
that it was shown.

This check owns every path it uses: a fresh temporary config, home,
session store and session name.

Usage:

    go build -o /tmp/opencode/hal9001 ./cmd/hal9001
    HAL9001_BINARY=/tmp/opencode/hal9001 python3 tools/ptycheck/feature_coexistence_checks.py
"""
import json
import os
import shutil
import sys
import tempfile

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, HERE)

FAILURES = []


def check(name, ok, detail=""):
    if ok:
        print("[ok  ] %s" % name)
    else:
        print("[FAIL] %s" % name)
        FAILURES.append(name)
        if detail:
            print("       %s" % detail.replace("\n", "\n       ")[:1500])


def shown(raw, needle):
    """Report whether the text was ever put on the screen."""
    return needle in raw.decode("utf-8", "replace")


def main():
    base = os.environ.get("HAL9001_PTY_TMPDIR", "/tmp/opencode")
    root = tempfile.mkdtemp(prefix="hal9001-coexist-", dir=base)
    home = os.path.join(root, "home")
    store = os.path.join(home, "sessions")
    os.makedirs(store)
    os.makedirs(os.path.join(root, "HAL"))
    config = os.path.join(home, "config.json")
    with open(config, "w") as f:
        json.dump({
            "provider": "openai-compatible",
            "endpoint": "http://127.0.0.1:1/v1",
            "api_key": "test-key",
            "models": [{"id": "test-model"}],
            "active_model": "test-model",
            "theme": "dark",
        }, f)

    os.environ["HAL9001_CONFIG"] = config
    os.environ["HAL9001_HOME"] = home
    os.environ["HAL9001_SESSION"] = "coexist"

    import pty_drive
    from termsim import render
    pty_drive.CONFIG = config
    pty_drive.HOME = home
    pty_drive.STORE = store
    pty_drive.SESSION = "coexist"

    def capture(script, cols=80, rows=24):
        timeout = sum(delay for delay, _ in script) + 1.5
        raw = pty_drive.run(script, cols=cols, rows=rows, timeout=timeout,
                            fresh=False, session="coexist",
                            args=["--workspace", os.path.join(root, "HAL")])
        return raw, render(raw, cols, rows)

    try:
        # 1. The attachment commands work.
        src = os.path.join(root, "notes.md")
        with open(src, "w") as f:
            f.write("the operator's notes\n")
        raw, screen = capture([(2.0, ("/attach " + src + "\r").encode()), (0.8, b"")])
        check("/attach works", shown(raw, "Attached") and shown(raw, "notes.md"),
              screen.text())
        check("the pending row names the attached file",
              "notes.md" in screen.text(), screen.text())

        # The pending list lives in the running session, so the listing
        # and the removal happen in the same run as the attach.
        raw, screen = capture([(2.0, ("/attach " + src + "\r").encode()),
                               (0.6, b"/attachments\r"),
                               (0.6, b"/detach notes.md\r"), (0.8, b"")])
        check("/attachments works", shown(raw, "notes.md"), screen.text())
        check("/detach works", shown(raw, "Detached"), screen.text())

        # 2. The MCP command works.
        raw, screen = capture([(2.0, b"/mcp\r"), (0.8, b"")])
        check("/mcp opens", shown(raw, "MCP servers"), screen.text())
        raw, screen = capture([(2.0, b"/mcp status\r"), (0.8, b"")])
        check("/mcp status answers", shown(raw, "MCP servers"), screen.text())

        # 3. The suggestion panel finds every command of both features.
        for prefix, want in (("/att", "/attach"), ("/att", "/attachments"),
                             ("/det", "/detach"), ("/mcp", "/mcp")):
            raw, screen = capture([(2.0, prefix.encode()), (0.8, b"")])
            check("the suggestions find %s" % want, shown(raw, want), screen.text())

        # 4. /quit is still in the first window of the panel.
        raw, screen = capture([(2.0, b"/"), (0.8, b"")])
        check("/quit stays in the first suggestion window",
              shown(raw, "/quit"), screen.text())
        check("/help stays in the first suggestion window",
              shown(raw, "/help"), screen.text())

        # 5. The help carries both features' sections, in the agreed order.
        #    The order is read from the scroll: the section that appears
        #    first is the one above the others.
        titles = ("MPM skills and diagnostics", "Attachments", "MCP client",
                  "Permissions and file writes")
        depth_of = {}
        for depth in range(1, 16):
            script = [(2.0, b"/help\r")]
            for _ in range(depth):
                script.append((0.2, b"\x1b[6~"))
                script.append((0.2, b""))
            raw, screen = capture(script, 60, 24)
            for title in titles:
                if title not in depth_of and shown(raw, title):
                    depth_of[title] = depth
            if len(depth_of) == len(titles):
                break
        for title in titles:
            check("the help has the %r section" % title, title in depth_of,
                  str(depth_of))
        if len(depth_of) == len(titles):
            ordered = [t for t, _ in sorted(depth_of.items(), key=lambda kv: kv[1])]
            check("the help sections are in the agreed order",
                  ordered == list(titles), str(ordered))
        else:
            check("the help sections are in the agreed order", False,
                  "not every section was reached: %s" % depth_of)
    finally:
        shutil.rmtree(root, ignore_errors=True)

    if FAILURES:
        print("%d FEATURE COEXISTENCE CHECKS FAILED" % len(FAILURES))
        return 1
    print("ALL FEATURE COEXISTENCE PTY CHECKS PASSED")
    return 0


if __name__ == "__main__":
    sys.exit(main())
