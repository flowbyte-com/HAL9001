#!/usr/bin/env python3
"""Isolated pseudo-terminal checks for /help and slash suggestions.

This suite owns every path it uses: a fresh temporary config, home,
session store and session name are created before the child starts and
removed afterwards. It never touches the shared fixtures used by
checks.py, so it can run beside another suite. Every child process is
terminated and reaped before the script exits.

Usage:

    go build -o /tmp/opencode/hal9001 ./cmd/hal9001
    HAL9001_BINARY=/tmp/opencode/hal9001 python3 tools/ptycheck/help_checks.py
"""
import fcntl
import json
import os
import pty
import select
import shutil
import signal
import struct
import sys
import tempfile
import termios
import time
import unicodedata

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, HERE)
from pty_session import Session  # noqa: E402
from termsim import render  # noqa: E402

FAILURES = []
BINARY = os.environ.get("HAL9001_BINARY", "/tmp/opencode/hal9001")
THEMES = ["HAL Classic", "Midnight", "Amber", "Paper", "Monochrome"]


def check(name, ok, detail=""):
    print(("[ok  ] " if ok else "[FAIL] ") + name + ("" if ok else "  " + detail))
    if not ok:
        FAILURES.append(name)


def cell_width(text):
    """Display cells used by one line, without ANSI sequences."""
    width = 0
    for ch in text:
        if unicodedata.combining(ch) or ch in ("\u200d", "\ufe0f"):
            continue
        width += 2 if unicodedata.east_asian_width(ch) in ("W", "F") else 1
    return width


def cell_column(text, fragment):
    """The display cell where fragment starts, or -1."""
    at = text.find(fragment)
    if at < 0:
        return -1
    return cell_width(text[:at])


def make_temp_home():
    base = os.environ.get("HAL9001_PTY_TMPDIR", "/tmp/opencode")
    os.makedirs(base, exist_ok=True)
    return tempfile.mkdtemp(prefix="hal9001-help-", dir=base)


def write_config(path, theme, endpoint):
    with open(path, "w") as fh:
        json.dump({
            "provider": "openai-compatible",
            "endpoint": endpoint,
            "api_key": "test-key",
            "models": [{"id": "MiniMax-M3.1-Flash"}],
            "active_model": "MiniMax-M3.1-Flash",
            "theme": theme,
        }, fh)


def reap(pid, fd):
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


def run(script, cols=80, rows=24, timeout=10.0, settle=0.45,
        config=None, home=None, store=None, session="helpcheck"):
    """Run the binary in a pty and return the captured byte stream.

    The child is terminated and reaped in a finally block, whether the
    script completed, timed out or raised.
    """
    session_path = os.path.join(store, session + ".jsonl")
    try:
        os.remove(session_path)
    except FileNotFoundError:
        pass

    pid, fd = pty.fork()
    if pid == 0:
        os.environ["TERM"] = "xterm-256color"
        os.environ["HAL9001_CONFIG"] = config
        os.environ["HAL9001_HOME"] = home
        argv = [BINARY, "--session", session, "--store", store]
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
                if isinstance(action, tuple) and action and action[0] == "resize":
                    fcntl.ioctl(fd, termios.TIOCSWINSZ,
                                struct.pack("HHHH", action[2], action[1], 0, 0))
                    os.kill(pid, signal.SIGWINCH)
                else:
                    os.write(fd, action)
                idx += 1
                last_action = time.time()
                next_at = last_action + (script[idx][0] if idx < len(script) else 0)
            if idx == len(script) and time.time() - last_action >= settle:
                break
    finally:
        reap(pid, fd)
    return bytes(out)


def capture(script, cols, rows, **kwargs):
    return render(run(script, cols=cols, rows=rows, **kwargs), cols, rows)


def no_overflow(screen, cols):
    return all(cell_width(line) <= cols for line in screen.text().split("\n"))


def title_visible(text):
    return "Help" in text and "Esc close" in text


def read_help_section(title, cols, rows, **common):
    """Scroll the help reference to a section and read that section.

    The reference grows as commands are added, so a section is located
    by scrolling to it rather than by counting scrolls. How many scrolls
    put a section in view depends on how much help there is, so a fixed
    count silently stops working the moment the content grows, and the
    check then fails for a reason that has nothing to do with the
    controls it is about.

    Every viewport from the section onwards is collected, so a control
    that lies below the section's first viewport is still read. The
    panel is read at the size the operator has, so the check still
    proves that every control of the section can be reached on a narrow
    terminal.

    Returns (found, text). The text is empty when the section was never
    reached.
    """
    workspace = os.path.join(common["home"], "workspace")
    os.makedirs(workspace, exist_ok=True)
    session = Session(BINARY, common["config"], common["home"],
                      common["store"], "help-section", workspace,
                      cols=cols, rows=rows)
    session.start()
    try:
        # The interface has to be up before a key is sent, or the keys
        # are read before the terminal is in the mode that delivers
        # them.
        session.wait_for("Enter send", 20.0)
        session.send(b"/help\r")
        session.wait(1.2)
        found = False
        pages = []
        previous = None
        for _ in range(80):
            screen = session.screen()
            if title in screen:
                found = True
            if found:
                pages.append(screen)
            if previous is not None and screen == previous:
                # The panel stopped moving: the end of the document.
                break
            previous = screen
            session.send(b"\x1b[B" * 4)
            session.wait(0.3)
        return found, "\n".join(pages)
    finally:
        session.close()


def main():
    if not os.path.isfile(BINARY):
        print("missing HAL9001 binary: %s" % BINARY)
        return 2
    home = make_temp_home()
    store = os.path.join(home, "sessions")
    os.makedirs(store, exist_ok=True)
    config = os.path.join(home, "config.json")
    # The help surface never calls the provider, so the endpoint can be
    # inert. It only has to be a valid configuration at start-up.
    write_config(config, "HAL Classic", "http://127.0.0.1:9/v1")
    common = {"config": config, "home": home, "store": store}
    try:
        sizes = [(60, 20), (80, 24), (120, 30), (170, 38)]
        for cols, rows in sizes:
            s = capture([(2.0, b"/help\r")], cols, rows, **common)
            text = s.text()
            check("%dx%d: /help opens the grouped reference" % (cols, rows),
                  "Conversation and editing" in text, text)
            check("%dx%d: the exit instruction is visible" % (cols, rows),
                  title_visible(text), text)
            check("%dx%d: no row overflows" % (cols, rows),
                  no_overflow(s, cols), text)

        # The sectioned reference is one vertical list at every width,
        # and labels and descriptions occupy fixed display columns.
        section_titles = ["Conversation and editing", "Navigation and clipboard",
                          "Sessions and preferences", "MPM skills and diagnostics",
                          "Permissions and file writes"]
        for cols, rows in ((120, 30), (170, 38)):
            text = capture([(2.0, b"/help\r")], cols, rows, **common).text()
            lines = text.split("\n")
            title_cells = []
            shared_row = False
            for title in section_titles:
                hits = [line for line in lines if title in line]
                for line in hits:
                    title_cells.append(cell_column(line, title))
            for line in lines:
                if sum(1 for title in section_titles if title in line) > 1:
                    shared_row = True
            check("%dx%d: help keeps sections in one vertical list" % (cols, rows),
                  len(title_cells) >= 2 and all(col == 2 for col in title_cells)
                  and not shared_row,
                  text)
            desc_cells = []
            for fragment in ("Show available commands", "Start a fresh conversation"):
                line = next((l for l in lines if fragment in l), "")
                if line:
                    desc_cells.append(cell_column(line, fragment))
            check("%dx%d: help descriptions share one display column" % (cols, rows),
                  len(desc_cells) >= 2 and len(set(desc_cells)) == 1,
                  text)

        # The body scrolls while the title and exit instruction stay put.
        s = capture([(2.0, b"/help\r"), (0.5, b"\x1b[B" * 100), (0.5, b"")],
                    60, 24, **common)
        text = s.text()
        check("the title and exit instruction survive scrolling",
              title_visible(text), text)
        check("scrolling help does not overflow", no_overflow(s, 60), text)

        # The permission controls live in the Permissions section. The
        # section is located by scrolling to it, and the controls are
        # read there, so the check does not depend on how much help
        # there is above it.
        found, section = read_help_section("Permissions and file writes",
                                           60, 24, **common)
        check("narrow help reaches the permissions section", found, section)
        check("narrow help scrolls to the 666 reference",
              "666" in section, section)
        check("narrow help explains the session-scoped 666 control",
              "666" in section and "only you" in section
              and "disable 666" in section, section)
        check("narrow help shows the permission controls",
              "Ctrl+P" in section and "Ctrl+Y" in section
              and "Ctrl+N" in section, section)
        check("narrow help keeps the one-off write distinct",
              "one-off write" in section, section)

        # The suggestion panel is capped, aligned and says that more
        # results exist. Scrolling reaches the MPM commands at the end.
        initial = capture([(2.0, b"/")], 80, 24, **common)
        text = initial.text()
        check("suggestions: / opens a capped list",
              "Show available commands" in text and "more" in text, text)
        check("suggestions: the extra-results hint is present",
              "scroll" in text, text)
        check("suggestions: no row overflows", no_overflow(initial, 80), text)

        # The list is longer than the panel, so the operator scrolls to
        # the end of it and back. The number of commands is not fixed,
        # so the check scrolls to the end and one page back rather than
        # counting rows.
        scrolled = capture([(2.0, b"/"), (0.5, b"\x1b[B" * 40),
                            (0.5, b"\x1b[A" * 7), (0.5, b"")],
                           80, 24, **common)
        text = scrolled.text()
        check("suggestions: scrolling reaches /skills",
              "/skills" in text, text)
        check("suggestions: scrolling reaches /skill",
              "/skill" in text, text)
        check("suggestions: scrolling reports earlier results",
              "above" in text, text)
        check("suggestions: the scrolled list does not overflow",
              no_overflow(scrolled, 80), text)

        filtered = capture([(2.0, b"/sk")], 60, 20, **common)
        text = filtered.text()
        check("suggestions: /sk filters to both MPM commands",
              "/skills" in text and "/skill" in text
              and "Pin a skill" in text, text)
        check("suggestions: a narrow list stays inside the terminal",
              no_overflow(filtered, 60), text)
        lines = text.split("\n")
        skills_line = next((l for l in lines if "List the available skills" in l), "")
        skill_line = next((l for l in lines if "Pin a skill" in l), "")
        check("suggestions: long syntax and descriptions share one column",
              bool(skills_line) and bool(skill_line)
              and cell_column(skills_line, "List the available skills") ==
              cell_column(skill_line, "Pin a skill"),
              text)

        completed = capture([(2.0, b"/sk"), (0.5, b"\x1b[B"), (0.5, b"\t")],
                            60, 20, **common)
        text = completed.text()
        check("suggestions: Tab completes the selected command",
              "/skill" in text and "Pin a skill" not in text, text)

        cancelled = capture([(2.0, b"/sk"), (0.5, b"\x1b[B"), (0.5, b"\x1b")],
                            60, 20, **common)
        text = cancelled.text()
        check("suggestions: Esc closes the list",
              "Pin a skill" not in text, text)
        check("suggestions: Esc keeps the draft", "/sk" in text, text)

        # Esc leaves help and restores the conversation frame.
        restored = capture([(2.0, b"/help\r"), (0.5, b"\x1b")], 80, 24, **common)
        text = restored.text()
        check("/help: Esc restores the conversation",
              "Conversation and editing" not in text
              and "Enter send" in text, text)
        check("/help: the composer is back after Esc",
              "444 READ ONLY" in text, text)

        # Every theme renders the same legible reference.
        for theme in THEMES:
            write_config(config, theme, "http://127.0.0.1:9/v1")
            s = capture([(2.0, b"/help\r")], 80, 24, **common)
            text = s.text()
            check("theme %s: help is visible" % theme,
                  "Conversation and editing" in text
                  and "Esc close" in text, text)
            check("theme %s: help does not overflow" % theme,
                  no_overflow(s, 80), text)
    finally:
        shutil.rmtree(home, ignore_errors=True)

    print()
    if FAILURES:
        print("FAILED: " + ", ".join(FAILURES))
        return 1
    print("ALL HELP PTY CHECKS PASSED")
    return 0


if __name__ == "__main__":
    sys.exit(main())
