#!/usr/bin/env python3
"""Behaviour checks for HAL9001 in a real pseudo-terminal.

Requires a built binary and a provider that answers locally. The default
setup points HAL9001 at a local OpenAI-compatible stub:

    python3 tools/ptycheck/fake_provider.py &
    HAL9001_CONFIG=/tmp/hal9001-test.json \\
    HAL9001_BINARY=./hal9001 python3 tools/ptycheck/checks.py

Every check prints ok or FAIL; the exit status is non-zero on failure.
"""
import base64
import json
import os
import re
import subprocess
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import pty_drive
from fake_provider import SAVE_BODY
from pty_drive import HOME, run
from termsim import render

FAILURES = []

# userPreviewRows is the most rows a collapsed prompt may show. It
# mirrors the interface's own limit.
userPreviewRows = 4


def check(name, ok, detail=""):
    print(("[ok  ] " if ok else "[FAIL] ") + name + ("" if ok else "  " + detail))
    if not ok:
        FAILURES.append(name)


def screen(raw, cols=80, rows=24):
    return render(raw, cols, rows)


def write_scroll_fixture(theme=None):
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


def composer_line(text):
    """The text inside the composer's border."""
    lines = text.split("\n")
    for i, line in enumerate(lines):
        if line.startswith("╭"):
            return lines[i + 1] if i + 1 < len(lines) else ""
    return ""


def composer_rows(text):
    """Every row inside the composer's border, top to bottom."""
    lines = text.split("\n")
    start = next((i for i, l in enumerate(lines) if l.startswith("╭")), -1)
    if start < 0:
        return []
    rows = []
    for line in lines[start + 1:]:
        if line.startswith("╰"):
            break
        rows.append(line.strip("│").strip())
    return rows


def reverse_cells(screen):
    """The cells drawn in reverse video, as (row, column) pairs."""
    out = []
    for y, marks in enumerate(screen.rev):
        for x, on in enumerate(marks):
            if on:
                out.append((y, x))
    return out


def bracket_paste(text):
    """The bytes a terminal sends for a bracketed paste."""
    return b"\x1b[200~" + text.encode() + b"\x1b[201~"


THEMES = [
    ("HAL Classic", "#121316"),
    ("Midnight", "#101c2d"),
    ("Amber", "#211b12"),
    ("Paper", "#f4f1e9"),
    ("Monochrome", "#111111"),
]


def write_theme_config(name, theme):
    path = os.path.join("/tmp", "hal9001-theme-%s.json" % re.sub(r"\W+", "-", name.lower()))
    with open(path, "w") as fh:
        json.dump({
            "provider": "openai-compatible",
            "endpoint": os.environ.get("HAL9001_ENDPOINT", "http://127.0.0.1:8099/v1"),
            "api_key": "test-key",
            "models": [{"id": "MiniMax-M3.1-Flash"}],
            "active_model": "MiniMax-M3.1-Flash",
            "theme": theme,
        }, fh)
    return path


def theme_checks():
    write_scroll_fixture(theme="HAL Classic")
    original = pty_drive.CONFIG
    backgrounds, palettes = {}, {}
    for name, bg in THEMES:
        pty_drive.CONFIG = write_theme_config(name, name)
        raw = run([(2.0, b"")], timeout=5.0, fresh=False, session="scrollfixture")
        text = raw.decode("utf-8", "replace")
        m = re.search(r"\x1b\]11;([^\x07\x1b]*)", text)
        backgrounds[name] = m.group(1).lower() if m else ""
        palettes[name] = tuple(sorted(set(re.findall(r"38;5;(\d+)", text)), key=int))
    pty_drive.CONFIG = DEFAULT_CONFIG

    for name, bg in THEMES:
        check("themes: %s sets its background" % name, backgrounds[name] == bg,
              backgrounds[name])
    unique_bg = len(set(backgrounds.values())) == len(THEMES)
    check("themes: five distinct backgrounds", unique_bg, str(backgrounds))
    unique_pal = len(set(palettes.values())) == len(THEMES)
    check("themes: five distinct colour palettes", unique_pal, str(palettes))


def layout_checks():
    write_short_fixture()
    text = run_fixture("shortchat")
    lines = text.split("\n")
    top = next((i for i, l in enumerate(lines) if l.startswith("╭")), -1)
    msg = next((i for i, l in enumerate(lines) if "You > a short question" in l), -1)
    check("layout: a short conversation sits above the composer",
          msg > 0 and top > 0 and 0 < top - msg <= 4, "\n".join(lines))

    write_long_prompt_fixture()
    text = run_fixture("longprompt")
    shown = text.count("museum thought experiment line")
    # A stored prompt is shown in full: the collapse that belonged to a
    # finished turn is not stored.
    check("layout: a reopened long prompt is shown in full",
          shown > userPreviewRows, str(shown))
    check("layout: a reopened long prompt is not abbreviated",
          "Ctrl+O to expand" not in text, text)


def session_checks():
    """The acceptance flow for active-session persistence, against real
    files on disk (no mocks)."""
    cfg = os.path.join(HOME, "session-config.json")
    with open(cfg, "w") as fh:
        json.dump({
            "provider": "openai-compatible",
            "endpoint": os.environ.get("HAL9001_ENDPOINT", "http://127.0.0.1:8099/v1"),
            "api_key": "test-key",
            "models": [{"id": "MiniMax-M3.1-Flash"}],
            "active_model": "MiniMax-M3.1-Flash",
        }, fh)
    pty_drive.CONFIG = cfg
    sessions = os.path.join(HOME, "sessions")
    for name in os.listdir(sessions) if os.path.isdir(sessions) else []:
        if name.endswith(".jsonl"):
            os.remove(os.path.join(sessions, name))

    def run_keys(script, timeout=12.0):
        # No --session: the application must choose from its own metadata.
        raw = run(script, timeout=timeout, fresh=False, session="")
        # Render the screen as it was before /q left the alternate buffer.
        end = raw.find(b"\x1b[?1049l")
        if end >= 0:
            raw = raw[:end]
        return render(raw).text()

    # 1. First launch, one exchange.
    first = run_keys([(2.0, b"first question\r"), (3.0, b"/q\r")], timeout=10.0)
    check("session: first conversation is active", "HAL9001" in first, first)

    # 2. Clear and quit. The confirmation is transient, so the screen is
    # captured while it is still there.
    cleared = run_keys([(2.0, b"/clear\r"), (1.0, b"")], timeout=3.6)
    check("session: /clear starts a fresh conversation",
          "Conversation cleared" in cleared, cleared)
    check("session: /clear does not show the old conversation",
          "first question" not in cleared, cleared)

    # 3. Relaunch: the cleared (empty) session is still active.
    relaunched = run_keys([(2.0, b"")], timeout=6.0)
    check("session: the cleared conversation survives a restart",
          "first question" not in relaunched, relaunched)

    # 4. Recover the original through /sessions and quit. The browser
    # lists the newest first, so move down to the older conversation.
    recovered = run_keys([(2.0, b"/sessions\r"), (0.6, b"\x1b[B"), (0.4, b"\r"), (1.0, b"/q\r")], timeout=10.0)
    check("session: the previous conversation is recoverable",
          "first question" in recovered, recovered)

    # 5. The selected older session is active on the next launch.
    again = run_keys([(2.0, b"")], timeout=6.0)
    check("session: the selected conversation resumes on restart",
          "first question" in again, again)


def token_checks():
    text = run([(2.0, b"count my tokens\r"), (3.0, b"")], timeout=10.0,
               fresh=False, session="tokenchat").decode("utf-8", "replace")
    rendered = render(text.encode()).text()
    check("tokens: the footer reports provider usage",
          "Tokens:" in rendered and " in / " in rendered, rendered)
    usage = run([(2.0, b"count my tokens\r"), (3.0, b"/usage\r")], timeout=10.0,
                fresh=False, session="tokenchat").decode("utf-8", "replace")
    panel = render(usage.encode()).text()
    for label in ("input", "output", "reasoning", "completed"):
        check("tokens: /usage shows %s" % label, label in panel, panel)


def rename_checks():
    # Seed the conversation: renaming requires an existing session file.
    run([(2.0, b"seed message\r"), (2.0, b"")], timeout=8.0,
        fresh=False, session="scrollfixture")
    raw = run([(2.0, b"/rename Museum Test\r"), (1.0, b"")], timeout=3.6,
              fresh=False, session="scrollfixture")
    text = render(raw).text()
    check("rename: confirmation shown", "renamed" in text.lower(), text)
    check("rename: header shows the new title", "Museum Test" in text, text)

    raw = run([(2.0, b"/rename\r")], timeout=6.0, fresh=False, session="scrollfixture")
    text = render(raw).text()
    check("rename: overlay opens", "Rename this conversation" in text, text)

    browser = run([(2.0, b"/sessions\r")], timeout=6.0, fresh=False,
                  session="scrollfixture")
    items = render(browser).text()
    check("rename: browser lists the title", "Museum Test" in items, items)


def mouse_checks():
    """Mouse reporting is on by default: HAL asks the terminal for mouse
    events, so the wheel scrolls and a drag selects. --no-mouse (and
    HAL9001_MOUSE=0) turns it off, and the terminal's own selection is
    left in charge."""
    write_scroll_fixture()

    # The default: reporting is requested.
    raw = run([(2.0, b"")], timeout=5.0, fresh=False, session="scrollfixture")
    on = raw.decode("utf-8", "replace")
    check("mouse: reporting is on by default", "\x1b[?1002h" in on, on[:200])
    check("mouse: SGR reporting is on by default", "\x1b[?1006h" in on, on[:200])

    # --no-mouse: nothing is requested.
    raw = run([(2.0, b"")], timeout=5.0, fresh=False, session="scrollfixture",
              args=["--no-mouse"])
    off = raw.decode("utf-8", "replace")
    check("mouse: --no-mouse leaves reporting off",
          "\x1b[?1002h" not in off and "\x1b[?1006h" not in off, off[:200])

    # The environment says the same thing.
    os.environ["HAL9001_MOUSE"] = "0"
    try:
        raw = run([(2.0, b"")], timeout=5.0, fresh=False,
                  session="scrollfixture")
        off = raw.decode("utf-8", "replace")
        check("mouse: HAL9001_MOUSE=0 leaves reporting off",
              "\x1b[?1002h" not in off and "\x1b[?1006h" not in off, off[:200])
    finally:
        os.environ.pop("HAL9001_MOUSE", None)

    # The wheel scrolls the conversation.
    before = run_fixture_text("scrollfixture")
    # SGR wheel events (column 40, row 10, 1-based). Five ups move
    # away from the bottom; five downs return to exactly the same
    # screen, which a lossy emulator cannot blur.
    moved = run([(2.5, b"\x1b[<64;40;10M" * 5)], timeout=7.0,
                 fresh=False, session="scrollfixture")
    moved_text = render(moved).text()
    check("mouse: wheel-up scrolls the conversation",
          first_text(moved_text) != first_text(before), moved_text[:400])
    back = run([(2.5, b"\x1b[<64;40;10M" * 5), (0.5, b"\x1b[<65;40;10M" * 5)],
               timeout=8.0, fresh=False, session="scrollfixture")
    back_text = render(back).text()
    check("mouse: wheel-down scrolls back symmetrically",
          first_text(back_text) == first_text(before), back_text[:400])

    # The terminal is left as it was found.
    raw = run([(2.0, b"/q\r")], timeout=6.0, fresh=False,
              session="scrollfixture")
    quit_text = raw.decode("utf-8", "replace")
    check("mouse: the modes are reset on a clean exit",
          "\x1b[?1002l" in quit_text, quit_text[-200:])

    raw = run([(2.5, ("signal", "SIGINT"))], timeout=7.0, fresh=False,
              session="scrollfixture")
    signal_text = raw.decode("utf-8", "replace")
    check("mouse: SIGINT ends the program",
          "\x1b[?1049l" in signal_text, signal_text[-200:])
    check("mouse: the modes are reset on SIGINT",
          "\x1b[?1002l" in signal_text, signal_text[-200:])


def flag_checks():
    """The mouse flags are resolved in the documented order, and a
    contradiction is refused rather than silently resolved."""
    env = dict(os.environ)
    env["HAL9001_CONFIG"] = pty_drive.CONFIG
    env["HAL9001_HOME"] = pty_drive.HOME

    proc = subprocess.run([pty_drive.BINARY, "--mouse", "--no-mouse"],
                          capture_output=True, env=env, timeout=20)
    err = proc.stderr.decode("utf-8", "replace")
    check("flags: --mouse with --no-mouse fails",
          proc.returncode != 0, str(proc.returncode))
    check("flags: the error names the flags", "--no-mouse" in err, err)

    proc = subprocess.run([pty_drive.BINARY, "--help"], capture_output=True,
                          env=env, timeout=20)
    usage = proc.stdout.decode("utf-8", "replace") + \
        proc.stderr.decode("utf-8", "replace")
    check("flags: --help lists --mouse", "-mouse" in usage, usage[-400:])
    check("flags: --help lists --no-mouse", "-no-mouse" in usage,
          usage[-400:])


def env_checks():
    """A malformed HAL9001_MOUSE is refused with a clear message, and a
    flag that decides is not blocked by it."""
    env = dict(os.environ)
    env["HAL9001_CONFIG"] = pty_drive.CONFIG
    env["HAL9001_HOME"] = pty_drive.HOME
    env["HAL9001_MOUSE"] = "maybe"

    proc = subprocess.run([pty_drive.BINARY, "--list-sessions"],
                          capture_output=True, env=env, timeout=20)
    err = proc.stderr.decode("utf-8", "replace")
    check("env: a malformed HAL9001_MOUSE fails",
          proc.returncode != 0, str(proc.returncode))
    check("env: the error names the variable", "HAL9001_MOUSE" in err, err)
    check("env: the error names the value", "maybe" in err, err)
    check("env: the error holds no secret",
          "test-key" not in err, err)

    # A flag decides on its own, so the malformed value is irrelevant.
    proc = subprocess.run([pty_drive.BINARY, "--list-sessions", "--mouse"],
                          capture_output=True, env=env, timeout=20)
    check("env: a valid flag is not blocked by a malformed value",
          proc.returncode == 0, proc.stderr.decode("utf-8", "replace"))

    # Every accepted form works.
    for value in ("1", "true", "yes", "on", "0", "false", "no", "off"):
        env["HAL9001_MOUSE"] = value
        proc = subprocess.run([pty_drive.BINARY, "--list-sessions"],
                              capture_output=True, env=env, timeout=20)
        if proc.returncode != 0:
            check("env: HAL9001_MOUSE=%s is accepted" % value, False,
                  proc.stderr.decode("utf-8", "replace"))


def run_fixture_text(session):
    return run_fixture(session)


def run_until(script, timeout=12.0, fresh=True, session=None):
    """Run the interface and return the screen as it was left.

    The bytes that restore the terminal after the program exits are
    dropped, so the screen shows the conversation and not the shell.
    """
    raw = run(script, timeout=timeout, fresh=fresh, session=session)
    end = raw.find(b"\x1b[?1049l")
    if end >= 0:
        raw = raw[:end]
    return render(raw).text()


def marked_spans(text):
    """The runs of text the terminal was told to draw in reverse video.

    The stream is walked the way a terminal reads it: a cursor move sets
    the position, a newline moves to the next row, and every other
    character takes a cell. The renderer merges the attributes of a run
    of cells into one escape sequence, so a marked run is a run of
    cells with the reverse-video attribute.

    Each span is (row, column, text), with the row and the column as
    the terminal counts them: both start at one.
    """
    spans = []
    row, col = 1, 1
    reverse = False
    marked = {}
    csi = re.compile(r"\x1b\[([0-9;?]*)([A-Za-z])")
    i = 0
    while i < len(text):
        ch = text[i]
        if ch == "\x1b":
            m = csi.match(text, i)
            if not m:
                i += 1
                continue
            params, final = m.group(1), m.group(2)
            i = m.end()
            if final == "m":
                for p in params.split(";"):
                    if p == "7":
                        reverse = True
                    elif p in ("27", "0", ""):
                        reverse = False
            elif final in "Hf":
                parts = [p for p in params.split(";") if p]
                row = int(parts[0]) if parts else 1
                col = int(parts[1]) if len(parts) > 1 else 1
            elif final == "J":
                marked = {}
                row, col = 1, 1
            continue
        if ch == "\r":
            col = 1
            i += 1
            continue
        if ch == "\n":
            row += 1
            col = 1
            i += 1
            continue
        if reverse:
            marked.setdefault(row, []).append((col, ch))
        col += 1
        i += 1
    for r in sorted(marked):
        cells = marked[r]
        spans.append((r, cells[0][0], "".join(c for _, c in cells).rstrip()))
    return spans


def selection_checks():
    """A drag over the conversation selects text, and the release copies
    it through OSC 52. The copied text must be the reply's own text: no
    interface labels, no colours."""
    write_scroll_fixture()
    # SGR press and drag over three rows of the stored reply: row 12
    # holds the "## Section 15" heading, rows 13 to 16 the paragraph
    # below it.
    dragging = run([(2.5, b"\x1b[<0;9;12M"), (0.8, b"\x1b[<32;40;15M")],
                   timeout=9.0, fresh=False, session="scrollfixture")
    spans = marked_spans(dragging.decode("utf-8", "replace"))
    conv = [s for s in spans if 12 <= s[0] <= 16]
    check("mouse: the selection is highlighted while it is dragged",
          bool(conv), str(spans[:6]))
    check("mouse: the mark never starts in the margin",
          all(s[1] > 1 for s in conv), str(spans[:6]))
    check("mouse: the mark covers the whole styled line",
          any("Section 15" in s[2] for s in conv), str(spans[:6]))
    raw = run([(2.5, b"\x1b[<0;9;12M"), (0.5, b"\x1b[<32;40;15M"),
               (0.5, b"\x1b[<0;40;15m")], timeout=9.0,
              fresh=False, session="scrollfixture")
    text = raw.decode("utf-8", "replace")
    found = re.search(r"\x1b\]52;c;([A-Za-z0-9+/=]+)\x07", text)
    check("mouse: a drag copies the selection", found is not None,
          text[-300:])
    if not found:
        return
    copied = base64.b64decode(found.group(1)).decode("utf-8", "replace")
    check("mouse: the copy holds text", copied.strip() != "", repr(copied))
    check("mouse: the copy has no interface labels",
          "HAL >" not in copied and "You >" not in copied, repr(copied))
    check("mouse: the copy has no escape sequences",
          "\x1b" not in copied, repr(copied))
    check("mouse: the copy is part of the reply",
          "paragraph" in copied or "Section" in copied, repr(copied))


def autoscroll_checks():
    """A drag that reaches the edge of the conversation scrolls the
    history on its own, and the selection grows into the text that
    comes into view."""
    write_scroll_fixture()

    # The conversation starts at row 3 (the header and the rule are
    # above it) and is sixteen rows tall. The drag ends in its top row,
    # so the history scrolls up while the pointer rests there.
    before = run_fixture_text("scrollfixture")
    visible = set(re.findall(r"## Section (\d+)", before))
    raw = run([(2.5, b"\x1b[<0;9;16M"), (0.4, b"\x1b[<32;9;3M"),
               (1.2, b""), (0.3, b"\x1b[<0;9;3m")],
              timeout=10.0, fresh=False, session="scrollfixture")
    text = raw.decode("utf-8", "replace")
    found = re.search(r"\x1b\]52;c;([A-Za-z0-9+/=]+)\x07", text)
    check("mouse: a drag at the edge copies the selection", found is not None,
          text[-300:])
    if found:
        copied = base64.b64decode(found.group(1)).decode("utf-8", "replace")
        # The history scrolled up while the pointer rested at the edge,
        # so the selection reaches sections that were never on screen.
        reached = set(re.findall(r"## Section (\d+)", copied))
        check("mouse: the edge drag reaches text that was off screen",
              bool(reached - visible),
              "reached %s, visible %s" % (sorted(reached), sorted(visible)))
        check("mouse: the edge drag copies no interface labels",
              "HAL >" not in copied and "You >" not in copied, repr(copied))
    moved = render(raw).text()
    check("mouse: the edge drag scrolls the conversation",
          first_text(moved) != first_text(before), moved[:400])

    # The other edge: the pointer rests in the last row of the
    # conversation and the history scrolls down. The wheel moves the
    # view away from the bottom first.
    raw = run([(2.5, b"\x1b[<64;40;10M" * 6), (0.4, b"\x1b[<0;9;5M"),
               (0.4, b"\x1b[<32;9;18M"), (1.2, b""), (0.3, b"\x1b[<0;9;18m")],
              timeout=11.0, fresh=False, session="scrollfixture")
    text = raw.decode("utf-8", "replace")
    found = re.search(r"\x1b\]52;c;([A-Za-z0-9+/=]+)\x07", text)
    check("mouse: a drag at the bottom edge copies the selection",
          found is not None, text[-300:])
    if found:
        copied = base64.b64decode(found.group(1)).decode("utf-8", "replace")
        visible = set(re.findall(r"## Section (\d+)", render(raw).text()))
        reached = set(re.findall(r"## Section (\d+)", copied))
        check("mouse: the bottom edge drag reaches text that was off screen",
              bool(reached - visible),
              "reached %s, visible %s" % (sorted(reached), sorted(visible)))


def collapse_checks():
    """A long prompt is abbreviated while HAL answers, and opens again
    when the answer is there."""
    prompt = "\n".join("museum thought experiment line %d" % i
                       for i in range(1, 31))
    body = (prompt + "\nplease hold the line").encode()
    paste = b"\x1b[200~" + body + b"\x1b[201~"

    # The provider holds the line for this prompt, so the state while
    # HAL works can be watched.
    during = run_until([(2.0, paste), (0.5, b"\r")],
                       timeout=3.5, fresh=True, session="collapsecheck")
    check("collapse: a long prompt is abbreviated while HAL answers",
          "Ctrl+O to expand" in during, during)
    # The middle of the prompt is not on screen while it is abbreviated.
    check("collapse: the abbreviated prompt hides its middle",
          "museum thought experiment line 22" not in during, during)
    check("collapse: the turn is still running",
          "Esc cancels" in during, during)

    after = run_until([(2.0, paste), (0.5, b"\r")],
                      timeout=7.0, fresh=True, session="collapsecheck")
    check("collapse: the prompt opens when the answer is there",
          "museum thought experiment line 22" in after, after)
    check("collapse: the answer stays in view",
          "I read your message" in after, after)
    check("collapse: the turn is over",
          "Esc cancels" not in after, after)


def paste_checks():
    """A multiline paste lands in the composer whole and is readable.

    The composer shows a five-row window on the draft. The window is an
    abbreviation, never a truncation: the buffer keeps every line, and
    Enter submits all of it.
    """
    def paste_and_wait(body, wait=1.0):
        raw = run([(1.5, bracket_paste(body)), (wait, b"")],
                  timeout=1.5 + wait + 0.5, fresh=True, session="pastecheck")
        return raw

    for n in (2, 5, 20, 100):
        body = "\n".join("line %03d of the draft" % i for i in range(1, n + 1))
        raw = paste_and_wait(body)
        text = render(raw).text()
        rows = composer_rows(text)
        want = min(n, 5)
        check("paste: %d lines fill %d composer row(s)" % (n, want),
              len(rows) == want, "%d row(s): %r" % (len(rows), rows))
        check("paste: %d lines show the end of the draft" % n,
              bool(rows) and rows[-1] == "line %03d of the draft" % n,
              repr(rows))
        # The caret is a reverse-video cell in the composer: it has to be
        # on the composer's last row, after the text.
        s = render(raw)
        cells = reverse_cells(s)
        lines = s.text().split("\n")
        bottom = next((i for i, l in enumerate(lines) if l.startswith("╰")), -1)
        check("paste: %d lines keep the cursor in the composer" % n,
              bottom > 0 and any(row == bottom - 1 and col > 1 for row, col in cells),
              str(cells[:6]))

    # The regression for the invisible composer: a draft taller than the
    # textarea's old internal limit (99 rows) used to render blank.
    body = "\n".join("line %03d of the draft" % i for i in range(1, 151))
    raw = paste_and_wait(body)
    text = render(raw).text()
    rows = composer_rows(text)
    check("paste: a 150-line paste is not invisible",
          len(rows) == 5 and all(rows), repr(rows))
    check("paste: the 150-line paste shows its last line",
          bool(rows) and rows[-1] == "line 150 of the draft", repr(rows))

    # Unicode and Markdown survive the paste.
    body = "## Heading\n\n- ✓ héllo wörld 日本語\n- **bold** and `code`"
    text = render(paste_and_wait(body)).text()
    joined = "\n".join(composer_rows(text))
    check("paste: unicode is visible", "héllo wörld 日本語" in joined, joined)
    check("paste: Markdown is visible", "**bold**" in joined, joined)

    # A single long line wraps, and the window shows its last rows.
    body = " ".join(["word"] * 120)
    text = render(paste_and_wait(body)).text()
    rows = composer_rows(text)
    check("paste: a long line wraps into the five-row window",
          len(rows) == 5 and all("word" in r for r in rows), repr(rows))

    # Trailing line breaks are part of the draft.
    body = "alpha\nbravo\n\n"
    text = render(paste_and_wait(body)).text()
    rows = composer_rows(text)
    check("paste: trailing newlines are kept",
          len(rows) == 4 and rows[0] == "alpha" and rows[1] == "bravo",
          repr(rows))

    # The whole draft is submitted, not the five visible rows: the
    # session file is the canonical record.
    body = "\n".join("line %03d of the draft" % i for i in range(1, 121))
    run([(1.5, bracket_paste(body)), (0.5, b"\r")], timeout=8.0,
        fresh=True, session="pastsubmit")
    path = os.path.join(HOME, "sessions", "pastsubmit.jsonl")
    stored = ""
    try:
        with open(path) as fh:
            for line in fh:
                msg = json.loads(line)
                if msg.get("role") == "user":
                    stored = msg.get("content", "")
                    break
    except (OSError, ValueError):
        stored = ""
    check("paste: Enter submits the complete draft", stored == body,
          "%d bytes stored, want %d" % (len(stored), len(body)))


def draft_notice_checks():
    """Ctrl+U and Ctrl+Z report what happened, and the report is
    transient."""
    body = "\n".join("line %03d of the draft" % i for i in range(1, 151))
    raw = run([(1.5, bracket_paste(body)), (0.5, b"\x15")], timeout=4.0,
              fresh=True, session="pastecheck")
    text = render(raw).text()
    rows = composer_rows(text)
    check("draft: Ctrl+U collapses the composer",
          len(rows) == 1 and "Message HAL" in rows[0], repr(rows))
    check("draft: Ctrl+U says what it did",
          "Draft cleared · Ctrl+Z to restore" in text, text)

    raw = run([(1.5, bracket_paste(body)), (0.5, b"\x15"), (0.5, b"\x1a")],
              timeout=5.0, fresh=True, session="pastecheck")
    text = render(raw).text()
    rows = composer_rows(text)
    check("draft: Ctrl+Z restores the whole draft",
          len(rows) == 5 and rows[-1] == "line 150 of the draft", repr(rows))
    check("draft: Ctrl+Z says what it did", "Draft restored" in text, text)
    check("draft: the restore replaces the clear notice",
          "Draft cleared" not in text, text)

    # A notice is replaced, and it expires on its own.
    early = render(run([(1.5, b"hi"), (0.5, b"\x15"), (1.0, b"")],
                       timeout=3.5, fresh=True, session="pastecheck")).text()
    check("draft: the notice is still there a second later",
          "Draft cleared · Ctrl+Z to restore" in early, early)
    late = render(run([(1.5, b"hi"), (0.5, b"\x15"), (6.5, b"")],
                      timeout=9.0, fresh=True, session="pastecheck")).text()
    check("draft: the notice disappears after its interval",
          "Draft cleared" not in late, late)

    # The composer window scrolls on its own: the wheel over it shows
    # earlier rows without moving the conversation.
    body = "\n".join("line %03d of the draft" % i for i in range(1, 21))
    before = render(run([(1.5, bracket_paste(body)), (0.6, b"")],
                        timeout=3.0, fresh=True, session="pastecheck")).text()
    scrolled = render(run([(1.5, bracket_paste(body)),
                           (0.5, b"\x1b[<64;20;21M"), (0.5, b"\x1b[<64;20;21M")],
                          timeout=3.5, fresh=True, session="pastecheck")).text()
    first_before = composer_rows(before)[0] if composer_rows(before) else ""
    first_scrolled = composer_rows(scrolled)[0] if composer_rows(scrolled) else ""
    check("draft: the wheel scrolls the composer window",
          first_before != first_scrolled,
          "%r -> %r" % (first_before, first_scrolled))
    check("draft: the wheel in the composer leaves the conversation alone",
          "Section" not in scrolled and "HAL9001" in scrolled,
          scrolled[:400])


def footer_hint_checks():
    """The footer row under the composer is contextual: it offers the keys
    that act on the draft, then the undo hint, then the minimal line."""
    def hint(script, cols=100, rows=30, timeout=4.0):
        raw = run(script, cols=cols, rows=rows, timeout=timeout, fresh=True,
                  session="hintcheck")
        return render(raw, cols, rows).text()

    empty = hint([(1.5, b"")])
    check("footer: an empty composer keeps the minimal line",
          "444 READ ONLY · /help" in empty, empty)
    check("footer: an empty composer does not offer Ctrl+U",
          "Ctrl+U" not in empty, empty)

    typed = hint([(1.5, b"hello")])
    check("footer: typing offers the editing keys",
          "Enter send · Ctrl+J newline · Ctrl+U clear · /help" in typed, typed)
    check("footer: the model stays in the row", "MiniMax-M3.1-Flash" in typed,
          typed)

    cleared = hint([(1.5, b"hello"), (0.6, b"\x15")])
    check("footer: Ctrl+U offers the bound restore key",
          "Ctrl+Z restore · /help" in cleared, cleared)
    check("footer: a cleared draft does not offer Ctrl+U",
          "Ctrl+U" not in cleared, cleared)

    restored = hint([(1.5, b"hello"), (0.5, b"\x15"), (0.5, b"\x1a")], timeout=4.5)
    check("footer: restoring offers the clear key again",
          "Ctrl+U clear" in restored, restored)

    submitted = hint([(1.5, b"hello\r"), (1.5, b"")], timeout=5.5)
    check("footer: submitting returns the minimal line",
          "444 READ ONLY · /help" in submitted and "Ctrl+U" not in submitted,
          submitted)

    pasted = hint([(1.5, bracket_paste("alpha\nbravo")), (0.6, b"")])
    check("footer: a paste offers the editing keys", "Ctrl+U clear" in pasted,
          pasted)

    # A narrow terminal shortens the hint instead of clipping the row.
    narrow = hint([(1.5, b"hello")], cols=40, rows=20, timeout=3.5)
    check("footer: a narrow terminal keeps the clear key visible",
          "Ctrl+U clear" in narrow, narrow)

    # The row is legible on every theme.
    original = pty_drive.CONFIG
    try:
        for name, _ in THEMES:
            pty_drive.CONFIG = write_theme_config(name, name)
            text = hint([(1.5, b"hello")], timeout=3.5)
            check("footer: the hint is visible on %s" % name,
                  "Ctrl+U clear" in text, text)
    finally:
        pty_drive.CONFIG = original


def approval_checks():
    """666: one operator-approved write, and nothing else.

    The fake provider proposes a write when the prompt contains
    ``[[save PATH]]``. The workspace is a temporary directory, so these
    checks never touch the operator's own ~/HAL.
    """
    # Outside HAL9001_HOME on purpose: the application data directory is
    # protected, and the checks must exercise a normal workspace.
    workspace_dir = os.path.join(os.path.dirname(HOME.rstrip("/")), "halworkspace")

    def reset_workspace():
        if os.path.isdir(workspace_dir):
            import shutil
            shutil.rmtree(workspace_dir)
        os.makedirs(workspace_dir, exist_ok=True)

    def saved(rel):
        path = os.path.join(workspace_dir, rel)
        try:
            with open(path, "rb") as fh:
                return fh.read()
        except OSError:
            return None

    def run_save(extra, rel="notes/letter.md", timeout=9.0):
        script = [(1.5, ("Please [[save %s]] this.\r" % rel).encode())]
        script.extend(extra)
        return run(script, timeout=timeout, fresh=True, session="writecheck")

    previous = os.environ.get("HAL9001_WORKSPACE")
    os.environ["HAL9001_WORKSPACE"] = workspace_dir
    try:
        # Default: 444, and a request only opens the review.
        reset_workspace()
        text = render(run_save([(2.5, b"")])).text()
        check("write: the review shows the destination",
              "notes/letter.md" in text, text)
        check("write: the review says it creates the file", "create" in text, text)
        check("write: the review shows the contents",
              "Dear hosting provider" in text, text)
        check("write: the review names 666",
              "666 REQUESTED" in text, text)
        check("write: nothing is written before approval",
              saved("notes/letter.md") is None, repr(saved("notes/letter.md")))
        check("write: the footer leaves 444 while pending",
              "444 READ ONLY" not in text, text)

        # Approve: exactly the reviewed bytes, then back to 444. The
        # capture happens while the completion notice is still on screen.
        reset_workspace()
        raw = run_save([(2.5, b"\x19"), (1.5, b"")], timeout=6.0)
        text = render(raw).text()
        body = saved("notes/letter.md")
        check("write: approval saves the exact contents",
              body is not None and body.decode() == SAVE_BODY, repr(body))
        check("write: approval reports the destination",
              "Saved" in text and "notes/letter.md" in text, text)
        check("write: the interface returns to 444",
              "444 READ ONLY" in text, text)
        check("write: 666 is not left behind", "666" not in text, text)

        # Reject with the chord.
        reset_workspace()
        text = render(run_save([(2.5, b"\x0e"), (0.8, b"")], timeout=4.2)).text()
        check("write: rejection writes nothing",
              saved("notes/letter.md") is None, repr(saved("notes/letter.md")))
        check("write: rejection is reported", "rejected" in text, text)
        check("write: rejection returns to 444",
              "444 READ ONLY" in text and "666" not in text, text)

        # Esc rejects too: the default is refusal.
        reset_workspace()
        text = render(run_save([(2.5, b"\x1b"), (0.8, b"")], timeout=4.2)).text()
        check("write: Esc rejects the request",
              saved("notes/letter.md") is None, repr(saved("notes/letter.md")))
        check("write: Esc returns to 444",
              "444 READ ONLY" in text and "666" not in text, text)

        # Typed text, including "yes", never decides the review.
        reset_workspace()
        text = render(run_save([(2.5, b"yes\r"), (1.0, b"")], timeout=4.4)).text()
        check("write: typed yes does not approve",
              saved("notes/letter.md") is None, repr(saved("notes/letter.md")))
        check("write: the review stays open after typed text",
              "666 REQUESTED" in text, text)

        # A destination outside the workspace is refused in conversation.
        reset_workspace()
        text = render(run_save([(1.5, b"")], rel="../escape.md", timeout=8.0)).text()
        check("write: a path outside the workspace is refused",
              "can't save that" in text, text)
        check("write: no review opens for a refused path",
              "666 REQUESTED" not in text, text)
        check("write: the refused path wrote nothing",
              not os.path.exists(os.path.join(workspace_dir, "..", "escape.md")),
              workspace_dir)

        # Overwrite: shown as one, and only replaced when approved.
        reset_workspace()
        os.makedirs(os.path.join(workspace_dir, "notes"), exist_ok=True)
        with open(os.path.join(workspace_dir, "notes", "letter.md"), "w") as fh:
            fh.write("old letter")
        text = render(run_save([(2.5, b"")])).text()
        check("write: an existing file is shown as an overwrite",
              "OVERWRITE" in text, text)
        check("write: reviewing an overwrite keeps the old file",
              (saved("notes/letter.md") or b"").decode() == "old letter",
              repr(saved("notes/letter.md")))
        text = render(run_save([(2.5, b"\x19"), (1.5, b"")], timeout=6.0)).text()
        check("write: approving the overwrite replaces the file",
              (saved("notes/letter.md") or b"").decode() == SAVE_BODY,
              repr(saved("notes/letter.md")))
        check("write: the overwrite reports a replacement",
              "Replaced" in text, text)

        # A second write needs its own approval.
        reset_workspace()
        text = render(run([(1.5, b"Please [[save one.md]] this.\r"), (2.2, b"\x19"),
                           (1.0, b"Please [[save two.md]] this.\r"), (2.2, b"")],
                          timeout=12.0, fresh=True, session="writecheck")).text()
        check("write: the first approval saved its file",
              saved("one.md") is not None, repr(saved("one.md")))
        check("write: a second write waits for its own approval",
              saved("two.md") is None, repr(saved("two.md")))
        check("write: the second review is pending", "666 REQUESTED" in text, text)

    finally:
        if previous is None:
            os.environ.pop("HAL9001_WORKSPACE", None)
        else:
            os.environ["HAL9001_WORKSPACE"] = previous
        if os.path.isdir(workspace_dir):
            import shutil
            shutil.rmtree(workspace_dir)


def skills_checks():
    """Skills: routing, the catalogue and the operator's override.

    The checks run against a temporary MPM workspace holding the two
    shipped skills, so they never depend on the operator's own store.
    """
    import shutil
    import tempfile

    root = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
    source = os.path.join(root, "skills")
    if not os.path.isdir(source):
        check("skills: the skill sources are present", False, source)
        return
    if shutil.which("mpm") is None:
        check("skills: mpm is available", False, "mpm was not found")
        return

    workspace = tempfile.mkdtemp(prefix="hal-mpm-")
    previous = os.environ.get("MPM_WORKSPACE")
    os.environ["MPM_WORKSPACE"] = workspace
    try:
        for name in ("system-prompt-writer", "humanize-text"):
            path = os.path.join(source, name + ".md")
            result = subprocess.run(
                ["mpm", "save-skill", "--file", path],
                capture_output=True, text=True, timeout=120)
            if result.returncode != 0:
                check("skills: %s is installed" % name, False, result.stderr[-300:])
                return
        check("skills: the two skills are installed", True)

        # The catalogue is reachable through the command line.
        listing = subprocess.run(
            ["mpm", "call", "mpm_skills", "--payload",
             '{"action":"list","params":{"scope":"all"}}'],
            capture_output=True, text=True, timeout=120)
        check("skills: the catalogue lists them",
              "system-prompt-writer" in listing.stdout and
              "humanize-text" in listing.stdout, listing.stdout[-300:])

        # /skills lists what is available.
        text = render(run([(1.6, b"/skills\r"), (1.0, b"")],
                          timeout=9.0, fresh=True, session="skillcheck")).text()
        check("skills: /skills lists the skills",
              "system-prompt-writer" in text and "humanize-text" in text, text)
        check("skills: /skills shows the routing mode", "routing" in text, text)

        # A human-directed request uses the humanize skill.
        text = render(run([(1.6, b"Write an email to my landlord.\r"), (2.0, b"")],
                          timeout=11.0, fresh=True, session="skillcheck")).text()
        check("skills: an email request uses humanize-text",
              "humanize-text" in text, text)

        # An ordinary question uses none.
        text = render(run([(1.6, b"What is the capital of Iceland?\r"), (2.0, b"")],
                          timeout=11.0, fresh=True, session="skillcheck")).text()
        check("skills: an ordinary question uses no skill",
              "skill:" not in text, text)

        # An agent-directed request uses the other skill.
        text = render(run([(1.6, b"Write a system prompt for a coding agent.\r"), (2.0, b"")],
                          timeout=11.0, fresh=True, session="skillcheck")).text()
        check("skills: an agent prompt uses system-prompt-writer",
              "system-prompt-writer" in text, text)

        # The operator can pin a skill. The capture is short so the
        # transient notice is still on screen.
        text = render(run([(1.6, b"/skill humanize-text\r"), (0.4, b"")],
                          timeout=3.6, fresh=True, session="skillcheck")).text()
        check("skills: /skill pins a skill", "humanize-text" in text, text)

        # The reply carries no routing instruction.
        text = render(run([(1.6, b"Write an email to my landlord.\r"), (2.0, b"")],
                          timeout=12.0, fresh=True, session="skillcheck")).text()
        check("skills: the reply carries no routing instruction",
              "Task guidance" not in text and "does not change what you are allowed" not in text,
              text)

        # The reply is credited with the skill that shaped it.
        text = render(run([(1.6, b"Write an email to my landlord.\r"), (2.0, b"")],
                          timeout=12.0, fresh=True, session="skillcheck")).text()
        check("skills: the reply is credited with the skill",
              "skill: humanize-text" in text, text)
        meta = [l for l in text.split("\n") if "skill:" in l and "in /" in l and "out" in l]
        check("skills: the credit sits on the metadata line",
              len(meta) == 1, repr(meta))

        # An ordinary question is credited with nothing.
        text = render(run([(1.6, b"What is the capital of Iceland?\r"), (2.0, b"")],
                          timeout=12.0, fresh=True, session="skillcheck")).text()
        check("skills: an ordinary answer carries no credit",
              "skill:" not in text, text)

        # The credit survives reopening the conversation: it is read from
        # the stored record, not looked up again.
        render(run([(1.6, b"Write an email to my landlord.\r"), (2.0, b"")],
                   timeout=12.0, fresh=True, session="skillreopen"))
        text = render(run([(1.8, b""), (0.6, b"")],
                          timeout=7.0, fresh=False, session="skillreopen")).text()
        check("skills: a reopened conversation keeps the credit",
              "skill: humanize-text" in text, text)
    finally:
        if previous is None:
            os.environ.pop("MPM_WORKSPACE", None)
        else:
            os.environ["MPM_WORKSPACE"] = previous
        shutil.rmtree(workspace, ignore_errors=True)


def composer_mouse_checks():
    """The wheel over the composer scrolls the draft instead of the
    conversation, and the caret stays where it was."""
    write_scroll_fixture()
    before = run([(2.0, b"draft line\r draft line\r draft line\r")],
                 timeout=6.0, fresh=False, session="scrollfixture")
    before_text = render(before).text()
    # The composer sits above the footer: row 21 of 24.
    moved = run([(2.0, b"draft line\r draft line\r draft line\r"),
                 (0.6, b"\x1b[<64;20;21M"), (0.6, b"\x1b[<64;20;21M")],
                timeout=9.0, fresh=False, session="scrollfixture")
    moved_text = render(moved).text()
    check("mouse: the wheel over the composer leaves the conversation alone",
          "Section" in moved_text, moved_text[:400])
    check("mouse: the wheel over the composer keeps the draft",
          "draft line" in moved_text, moved_text[:400])
    # The draft itself is untouched: the wheel moves the composer's
    # window, not its text.
    check("mouse: the composer window does not move the caret",
          composer_line(before_text) == composer_line(moved_text),
          "%r vs %r" % (composer_line(before_text), composer_line(moved_text)))


def exit_checks():
    for name in ("/q", "/quit", "/exit"):
        raw = run([(2.0, (name + "\r").encode())], timeout=5.0,
                  fresh=False, session="scrollfixture")
        text = raw.decode("utf-8", "replace")
        check("exit: %s leaves the alternate screen" % name, "\x1b[?1049l" in text)
        check("exit: %s restores the terminal colours" % name, "\x1b]111" in text)
        check("exit: %s restores the cursor" % name, "\x1b[?25h" in text)


def write_short_fixture():
    path = os.path.join(HOME, "sessions", "shortchat.jsonl")
    with open(path, "w") as fh:
        fh.write(json.dumps({"role": "user", "content": "a short question"}) + "\n")
        fh.write(json.dumps({"role": "assistant", "content": "a short answer"}) + "\n")


def write_long_prompt_fixture():
    body = "\n".join("museum thought experiment line %d" % i for i in range(1, 31))
    path = os.path.join(HOME, "sessions", "longprompt.jsonl")
    with open(path, "w") as fh:
        fh.write(json.dumps({"role": "user", "content": body}) + "\n")
        fh.write(json.dumps({"role": "assistant", "content": "A response."}) + "\n")


DEFAULT_CONFIG = os.environ.get("HAL9001_CONFIG", pty_drive.CONFIG)


def run_fixture(session):
    pty_drive.CONFIG = DEFAULT_CONFIG
    raw = run([(2.0, b"")], timeout=5.0, fresh=False, session=session)
    return render(raw).text()


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
    check("composer: one text row by default", end - top - 1 == 1, str(end - top - 1))
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

    shifted = capture([(2.0, b"\x1b[5;2~\x1b[5;2~")])
    check("scroll: shift+PgUp pages too (laptop Fn key)",
          first_text(shifted) != first_text(bottom), shifted[:400])

    before = capture([(2.0, b"\x1b[5~\x1b[5~")])
    after = capture([(2.0, b"\x1b[5~\x1b[5~"), (0.5, ("resize", 60, 14)), (1.5, b"")],
                    final=(60, 14))
    # The narrower terminal wraps the same paragraph differently, so the
    # reading position is compared by the text it starts with.
    check("scroll: shrinking keeps the reading position",
          first_text(before)[:30] == first_text(after)[:30],
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
    check("mouse reporting is on by default", "\x1b[?1006h" in text)
    check("mouse modes reset on exit", "\x1b[?1002l" in text)

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
    theme_checks()
    layout_checks()
    exit_checks()
    session_checks()
    token_checks()
    rename_checks()
    mouse_checks()
    flag_checks()
    env_checks()
    selection_checks()
    autoscroll_checks()
    collapse_checks()
    paste_checks()
    draft_notice_checks()
    footer_hint_checks()
    approval_checks()
    skills_checks()
    composer_mouse_checks()

    # Common terminal sizes keep the frame the right size.
    for cols, rows in [(120, 40), (160, 50)]:
        s = screen(run([(2.0, b"")], cols=cols, rows=rows, timeout=4.0), cols, rows)
        lines = s.text().split("\n")
        check(f"{cols}x{rows}: header stays on row one", "HAL9001" in lines[0], repr(lines[0]))
        check(f"{cols}x{rows}: footer stays at the bottom",
              "444 READ ONLY" in lines[-2], repr(lines[-2:]))
        check(f"{cols}x{rows}: no terminal scroll", len(lines) == rows, str(len(lines)))

    # A resize keeps the layout intact. The draft is still in the
    # composer, so the footer carries the editing keys rather than the
    # permission line; either way it is the same row at the bottom.
    s = screen(run([(2.0, b"hi"), (1.5, ("resize", 60, 15))], timeout=9.0), 60, 15)
    lines = s.text().split("\n")
    check("resize keeps the header", "HAL9001" in lines[0], repr(lines[0]))
    footer = lines[-2]
    check("resize keeps the footer",
          "444 READ ONLY" in footer or "Ctrl+U clear" in footer, repr(lines[-2:]))
    check("resize leaves no leftover rows", len(lines) == 15, str(len(lines)))

    print()
    if FAILURES:
        print("FAILED: " + ", ".join(FAILURES))
        return 1
    print("ALL PTY CHECKS PASSED")
    return 0


if __name__ == "__main__":
    sys.exit(main())
