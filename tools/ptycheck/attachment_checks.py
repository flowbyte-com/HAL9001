#!/usr/bin/env python3
"""Headless PTY checks for native file attachments.

The suite runs the real binary against a local OpenAI-compatible stub. It
checks the operator path end to end: /attach copies the chosen file into
the upload directory, the pending row names it above the composer, the
file's text travels with the message as untrusted data, the transcript
keeps a reference to it, /detach takes it back, and the original file is
never changed.

Usage:

    go build -o /tmp/opencode/hal9001 ./cmd/hal9001
    HAL9001_BINARY=/tmp/opencode/hal9001 python3 tools/ptycheck/attachment_checks.py
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
PORT = 8129
SESSION = "attachments"

# The marker the checks look for in the reply. It proves the file's text
# reached the model.
MARKER = "HAL9001-ATTACH-MARKER-42"


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


def capture(script, cols=80, rows=24, timeout=None):
    """Run the script and return the raw bytes and the final screen.

    The run ends shortly after the last scripted action, so a transient
    notice is still on screen when the screen is read.
    """
    write_config(CONFIG)
    if timeout is None:
        # The delays are relative to the previous action, so the run lasts
        # their sum. The margin keeps a transient notice on screen.
        timeout = sum(delay for delay, _ in script) + 1.5
    raw = pty_drive.run(script, cols=cols, rows=rows, timeout=timeout,
                        fresh=False, session=SESSION,
                        args=["--workspace", WORKSPACE])
    return raw, render(raw, cols, rows).text()


def shown(raw, needle):
    """Report whether the text was ever put on the screen.

    The raw stream is searched, not the final screen: a transient notice
    disappears again, and what the check is about is that it was shown.
    """
    return needle in raw.decode("utf-8", "replace")


def wait_for_server():
    deadline = time.time() + 5.0
    while time.time() < deadline:
        try:
            with socket.create_connection(("127.0.0.1", PORT), timeout=0.2):
                return True
        except OSError:
            time.sleep(0.05)
    return False


def upload_copies():
    """Return the copies in the upload directory, as (dir, name, path)."""
    uploads = os.path.join(WORKSPACE, "uploads")
    out = []
    if not os.path.isdir(uploads):
        return out
    for entry in sorted(os.listdir(uploads)):
        sub = os.path.join(uploads, entry)
        if not os.path.isdir(sub):
            continue
        for name in sorted(os.listdir(sub)):
            if name == "attachment.json":
                continue
            out.append((entry, name, os.path.join(sub, name)))
    return out


def session_text():
    """Return the stored conversation, if any."""
    path = os.path.join(STORE, SESSION + ".jsonl")
    try:
        with open(path, "r", errors="replace") as fh:
            return fh.read()
    except FileNotFoundError:
        return ""


def session_user_text():
    """Return the text of every stored user message."""
    out = []
    for line in session_text().splitlines():
        line = line.strip()
        if not line:
            continue
        try:
            record = json.loads(line)
        except ValueError:
            continue
        if record.get("role") == "user":
            out.append(record.get("content", ""))
    return "\n".join(out)


def main():
    global CONFIG, WORKSPACE, STORE
    if not os.path.isfile(BINARY):
        print("missing HAL9001 binary:", BINARY)
        return 2
    base = os.environ.get("HAL9001_PTY_TMPDIR", "/tmp/opencode")
    os.makedirs(base, exist_ok=True)
    root = tempfile.mkdtemp(prefix="hal-att-", dir=base)
    home = os.path.join(root, "config")
    store = os.path.join(root, "sessions")
    WORKSPACE = os.path.join(root, "HAL")
    sources = os.path.join(root, "src")
    os.makedirs(home)
    os.makedirs(store)
    os.makedirs(WORKSPACE)
    os.makedirs(sources)
    STORE = store
    CONFIG = os.path.join(home, "config.json")
    write_config(CONFIG)

    # The file the operator attaches. Its name has a space, so the
    # command has to take the path whole.
    source = os.path.join(sources, "my notes.txt")
    with open(source, "w") as fh:
        fh.write(MARKER + "\nthe rest of the file\n")
    source_stat = os.stat(source)

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

        # 1. Attaching names the file above the composer.
        _, text = capture([(2.0, ("/attach " + source + "\r").encode()),
                           (0.8, b"")])
        check("the pending row names the attached file",
              "my notes.txt" in text, text)
        check("the pending row shows the size", "B)" in text, text)
        copies = upload_copies()
        check("the file was copied into the upload directory",
              len(copies) == 1 and copies[0][1] == "my notes.txt", copies)
        if copies:
            with open(copies[0][2], "r", errors="replace") as fh:
                body = fh.read()
            check("the copy holds the original bytes", MARKER in body, body)
            check("the copy is not readable by others",
                  (os.stat(copies[0][2]).st_mode & 0o077) == 0,
                  oct(os.stat(copies[0][2]).st_mode))

        # 2. The original file is untouched.
        now = os.stat(source)
        with open(source, "r", errors="replace") as fh:
            body = fh.read()
        check("the source file is unchanged", MARKER in body, body)
        check("the source file kept its size",
              now.st_size == source_stat.st_size, now.st_size)
        check("the source file kept its timestamp",
              now.st_mtime == source_stat.st_mtime, now.st_mtime)

        # 3. The listing names the pending file and the directory.
        raw, text = capture([(2.0, ("/attach " + source + "\r").encode()),
                             (0.5, b"/attachments\r"), (0.8, b"")])
        check("the listing names the attached file", "my notes.txt" in text, text)
        check("the listing names the upload directory",
              shown(raw, "HAL/uploads"), text)

        # 4. Detaching takes the file back, and the copy stays.
        raw, text = capture([(2.0, ("/attach " + source + "\r").encode()),
                             (0.5, b"/detach my notes.txt\r"), (0.8, b"")])
        check("detaching reports the removal", shown(raw, "Detached"), text)
        check("the detached file is no longer pending",
              text.count("my notes.txt") <= 2, text)

        # 5. A missing file is refused with a clear message.
        raw, text = capture([(2.0, b"/attach /nonexistent/nope.txt\r"), (0.8, b"")])
        check("a missing file is refused", shown(raw, "no such file"), text)

        # 6. A directory is refused.
        raw, text = capture([(2.0, ("/attach " + sources + "\r").encode()), (0.8, b"")])
        check("a directory is refused", shown(raw, "directory"), text)

        # 7. The content travels with the message, as data.
        _, text = capture([(2.0, ("/attach " + source + "\r").encode()),
                           (0.5, b"[[attached]]\r"), (2.5, b"")])
        check("the file's text reached the model", MARKER in text, text)
        check("the request marked the content as data",
              "GUIDANCE: yes" in text, text)
        check("the request delimited the untrusted block",
              "BOUNDARY: yes" in text, text)

        # 7a. A file called "attachment.json" is an ordinary file: the
        #      upload directory keeps its own record under that name, so
        #      the copy must not take it.
        reserved = os.path.join(sources, "attachment.json")
        with open(reserved, "w") as fh:
            fh.write(MARKER + "\nfrom the reserved name\n")
        raw, text = capture([(2.0, ("/attach " + reserved + "\r").encode()),
                             (0.8, b"")])
        check("a file named like the sidecar attaches",
              "Attached attachment.json" in text, text)
        check("the file named like the sidecar is refused with no "
              "system error", "openat" not in text, text)
        check("the operator sees the original name",
              "attachment.json" in text, text)
        _, text = capture([(0.5, b"[[attached]]\r"), (2.5, b"")])
        check("the file named like the sidecar reached the model",
              MARKER in text, text)

        # 8. The transcript keeps a reference to the file.
        check("the transcript shows the attached file", "my notes.txt" in text, text)

        # 9. The stored record holds the reference, not the content.
        stored = session_text()
        check("the session records the attachment",
              "my notes.txt" in stored, stored[:400])
        check("the session does not store the file's text",
              MARKER not in session_user_text(), session_user_text()[:400])
        check("the session record stays small",
              len(stored) < 4096, len(stored))
        # The operator's own directory is not part of the conversation.
        check("the session record holds no source path",
              '"source"' not in stored and sources not in stored, stored[:400])

        # 10. A short terminal keeps exactly nine rows.
        _, text = capture([(2.0, ("/attach " + source + "\r").encode()),
                           (0.8, b"")], cols=80, rows=9)
        rows = text.split("\n")
        check("80x9 keeps exactly nine rows", len(rows) == 9, text)
        check("80x9 keeps every row inside the terminal",
              all(display_width(row) <= 80 for row in rows), text)

        # 11. A conversation without files is unchanged: no attachment
        # row and no paperclip. It runs in its own conversation, so the
        # earlier turns cannot be mistaken for a pending row.
        raw = pty_drive.run([(2.0, b"hello\r"), (1.5, b"")],
                            cols=80, rows=24, timeout=3.5,
                            fresh=True, session="attachments-plain",
                            args=["--workspace", WORKSPACE])
        plain = render(raw, 80, 24).text()
        check("an ordinary conversation shows no attachment row",
              "📎" not in plain, plain)
        check("an ordinary conversation still answers", "I read" in plain, plain)

        # 12. A restart keeps the reference: the conversation is read
        # back from disk and still names the file.
        _, text = capture([(2.0, b"")], timeout=3.5)
        check("a restart still shows the attached file",
              "my notes.txt" in text, text)
        check("a restart still shows the paperclip", "📎" in text, text)
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
    print("ALL ATTACHMENT PTY CHECKS PASSED")
    return 0


if __name__ == "__main__":
    sys.exit(main())
