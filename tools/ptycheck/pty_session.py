#!/usr/bin/env python3
"""Drive the HAL9001 binary inside one pseudo-terminal, step by step.

The script forks a pty, starts hal9001 inside it and lets the checks
send keys and read the screen between the steps. The process stays
alive for the whole session, which matters for every feature whose
state lives in the running process: the safe point that /resume
continues from, and the exchange whose completion has to reach the
conversation while a panel is open.

The session owns exactly the child that pty.fork() returned: closing it
signals that child alone, escalates from SIGTERM to SIGKILL after a
bounded grace period, and reaps it. Nothing is ever found by name, so
a concurrent harness and the operator's own HAL are never touched.

Usage:

    from pty_session import Session
    session = Session(binary, config, home, store, "demo", workspace)
    session.start()
    session.send(b"/help\\r")
    session.wait(1.0)
    print(session.screen())
    session.close()
"""
import fcntl
import os
import pty
import re
import select
import signal
import struct
import termios
import time

from termsim import render

# The escape sequences a terminal understands. The checks read plain
# text, so the styling is removed before anything is compared.
ANSI = re.compile(
    r"\x1b\[[0-9;?]*[ -/]*[@-~]"      # CSI
    r"|\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)"  # OSC
    r"|\x1b[@-Z\\-_]"                  # other escapes
)


def plain(data):
    """The text of a captured byte stream, without the styling."""
    return ANSI.sub("", data.decode("utf-8", "replace"))


class Session:
    """One HAL process, driven one step at a time."""

    def __init__(self, binary, config, home, store, session, workspace,
                 cols=120, rows=45):
        self.binary = binary
        self.config = config
        self.home = home
        self.store = store
        self.session = session
        self.workspace = workspace
        self.cols = cols
        self.rows = rows
        self.out = bytearray()
        self.pid = -1
        self.fd = -1

    def start(self):
        """Fork the pty and run the binary inside it."""
        pid, fd = pty.fork()
        if pid == 0:
            try:
                os.environ["TERM"] = "xterm-256color"
                os.environ["HOME"] = self.home
                os.environ["HAL9001_CONFIG"] = self.config
                os.environ["HAL9001_HOME"] = self.home
                os.environ["HAL9001_WORKSPACE"] = self.workspace
                # This suite never exercises MPM. A missing executable
                # keeps the knowledge registry away from the operator's
                # real store.
                os.environ["HAL9001_MPM_BIN"] = os.path.join(self.home, "no-mpm")
                os.execv(self.binary, [
                    self.binary,
                    "--session", self.session,
                    "--store", self.store,
                    "--workspace", self.workspace,
                ])
            except BaseException:
                # exec failed: never run the parent's cleanup in the child.
                os._exit(127)
        self.pid = pid
        self.fd = fd
        fcntl.ioctl(fd, termios.TIOCSWINSZ,
                    struct.pack("HHHH", self.rows, self.cols, 0, 0))

    def send(self, data):
        """Type something at the interface."""
        if self.fd >= 0:
            os.write(self.fd, data)

    def wait(self, seconds):
        """Read the output for a while.

        The interface redraws itself while it works, so the pty has to
        be drained even when nothing is expected: a full pty buffer
        would block the process under test.
        """
        deadline = time.monotonic() + seconds
        while True:
            remaining = deadline - time.monotonic()
            if remaining <= 0:
                return
            try:
                ready, _, _ = select.select([self.fd], [], [], min(remaining, 0.05))
            except (OSError, ValueError):
                return
            if not ready:
                continue
            try:
                data = os.read(self.fd, 65536)
            except OSError:
                return
            if not data:
                return
            self.out.extend(data)

    def mark(self):
        """A position in the output, for reading what came after it."""
        return len(self.out)

    def since(self, mark):
        """The bytes that arrived after the mark."""
        return bytes(self.out[mark:])

    def screen(self):
        """The screen the operator would see."""
        return render(bytes(self.out), self.cols, self.rows).text()

    def until(self, predicate, timeout=15.0):
        """Read until the predicate holds, or the time runs out."""
        deadline = time.monotonic() + timeout
        while True:
            if predicate():
                return True
            if time.monotonic() >= deadline:
                return False
            self.wait(0.2)

    def wait_for(self, needle, timeout=15.0, mark=None):
        """Read until the needle appears in the output, or the time runs out."""
        deadline = time.monotonic() + timeout
        while True:
            hay = plain(self.since(mark) if mark is not None else bytes(self.out))
            if needle in hay:
                return True
            if time.monotonic() >= deadline:
                return False
            self.wait(0.2)

    def close(self):
        """End the session and collect the process.

        The child owns its own process group, so the group is signalled
        as well. Nothing is ever found by name: only this child and
        its group are touched.
        """
        if self.fd < 0:
            return
        try:
            os.kill(self.pid, signal.SIGTERM)
        except ProcessLookupError:
            pass
        deadline = time.monotonic() + 2.0
        while time.monotonic() < deadline:
            try:
                waited, _ = os.waitpid(self.pid, os.WNOHANG)
            except ChildProcessError:
                break
            if waited == self.pid:
                break
            self.wait(0.05)
        else:
            try:
                os.kill(self.pid, signal.SIGKILL)
            except ProcessLookupError:
                pass
            try:
                os.waitpid(self.pid, 0)
            except ChildProcessError:
                pass
        try:
            os.close(self.fd)
        except OSError:
            pass
        self.fd = -1
