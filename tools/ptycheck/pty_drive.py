#!/usr/bin/env python3
"""Drive the HAL9001 binary inside a real pseudo-terminal.

The script forks a pty, starts hal9001 inside it and feeds it a scripted
sequence of key presses, window resizes and signals. The raw byte stream
is returned so it can be replayed through a terminal emulator.

Usage:

    from pty_drive import run
    raw = run([(2.0, b"/help\\r"), (1.0, b"\\x1b")])
    raw = run([(2.5, ("signal", "SIGINT"))])
    raw = run([(2.0, b"")], args=["--no-mouse"])
"""
import contextlib
import fcntl
import os
import pty
import select
import signal
import struct
import termios
import time

BINARY = os.environ.get("HAL9001_BINARY", "/tmp/opencode/hal9001")
CONFIG = os.environ.get("HAL9001_CONFIG", "/tmp/opencode/hal-test-config.json")
HOME = os.environ.get("HAL9001_HOME", "/tmp/opencode/halhome")
SESSION = os.environ.get("HAL9001_SESSION", "demo")
STORE = os.path.join(HOME, "sessions")

# How long a child gets to leave after SIGTERM before it is killed. HAL
# exits on SIGTERM, so this is normally paid only by a child that is
# wedged or deliberately ignoring the signal.
TERMINATE_GRACE = 2.0
POLL_INTERVAL = 0.02


def reset_session(session=None):
    """Start from an empty conversation."""
    if not session:
        return
    path = os.path.join(STORE, session + ".jsonl")
    try:
        os.remove(path)
    except FileNotFoundError:
        pass


def signal_number(sig):
    """Turn a signal name ("SIGINT", "int") or a number into a number."""
    if isinstance(sig, int):
        return sig
    name = str(sig).upper()
    if not name.startswith("SIG"):
        name = "SIG" + name
    return getattr(signal, name)


@contextlib.contextmanager
def _sigint_blocked():
    """Keep a second Ctrl+C from interrupting cleanup.

    A pending SIGINT is delivered after the mask is restored, by which
    time the child is gone and the pty descriptor is closed.
    """
    try:
        old = signal.pthread_sigmask(signal.SIG_BLOCK, {signal.SIGINT})
    except (AttributeError, ValueError, OSError):
        yield
        return
    try:
        yield
    finally:
        signal.pthread_sigmask(signal.SIG_SETMASK, old)


class _ChildProcess:
    """The HAL process this runner owns, and nobody else's.

    Ownership is the pid returned by pty.fork(). Cleanup only signals
    that pid, plus its process group when pty.fork made it the session
    leader (pgid == pid). It never scans for processes by name, so a
    concurrent harness instance and the operator's interactive HAL are
    never touched.
    """

    def __init__(self, pid, fd):
        self.pid = pid
        self.fd = fd
        self.reaped = False
        try:
            pgid = os.getpgid(pid)
        except OSError:
            pgid = -1
        # Only use the group when it is the child's own session group. A
        # widened signal could otherwise reach the test runner's group.
        self.pgid = pgid if pgid == pid else None

    def _signal(self, sig):
        if self.reaped:
            return
        try:
            if self.pgid is not None:
                os.killpg(self.pgid, sig)
            else:
                os.kill(self.pid, sig)
        except ProcessLookupError:
            # It exited between the check and the signal; waitpid below
            # still has the exit status to collect. If only the recorded
            # group vanished, the exact child is still ours to signal.
            try:
                os.kill(self.pid, sig)
            except ProcessLookupError:
                pass

    def _reap(self, block=False):
        """Collect the child; return True once its status is collected.

        Without block, a running child returns False immediately.
        """
        if self.reaped:
            return True
        flags = 0 if block else os.WNOHANG
        try:
            waited, _ = os.waitpid(self.pid, flags)
        except ChildProcessError:
            # Already collected, for example by an embedding runner.
            self.reaped = True
            return True
        if waited == self.pid:
            self.reaped = True
            return True
        return False

    def _terminate(self):
        if self._reap():
            return
        self._signal(signal.SIGTERM)
        deadline = time.monotonic() + TERMINATE_GRACE
        while time.monotonic() < deadline:
            if self._reap():
                return
            time.sleep(POLL_INTERVAL)
        # The grace period is over; SIGKILL cannot be ignored, and the
        # blocking wait makes sure the corpse is collected.
        self._signal(signal.SIGKILL)
        self._reap(block=True)

    def close(self):
        try:
            with _sigint_blocked():
                self._terminate()
        finally:
            if self.fd >= 0:
                try:
                    os.close(self.fd)
                except OSError:
                    pass
                self.fd = -1


def run(script, cols=80, rows=24, timeout=12.0, fresh=True, session=None,
        args=None):
    """Run the interface and send the scripted actions.

    script: list of (delay, payload) pairs. The payload is either bytes
    to write, a ("resize", cols, rows) tuple or a ("signal", signum)
    tuple. session selects a stored conversation (default demo). args
    holds extra command-line arguments for the binary, for example
    ["--no-mouse"].

    The child is owned by this call: however the call ends — the app
    exits, the script times out, an action fails or the runner is
    interrupted — close() reaps it, escalates from SIGTERM to SIGKILL
    after a bounded grace period, and closes the pty descriptor.
    """
    session = SESSION if session is None else session
    if fresh:
        reset_session(session)
    pid, fd = pty.fork()
    if pid == 0:
        try:
            os.environ["TERM"] = "xterm-256color"
            os.environ["HAL9001_CONFIG"] = CONFIG
            os.environ["HAL9001_HOME"] = HOME
            argv = [BINARY, "--store", STORE]
            if session:
                # An explicit --session wins over the stored active
                # session; omit it to exercise the persisted choice.
                argv = [BINARY, "--session", session, "--store", STORE]
            argv.extend(args or [])
            os.execv(BINARY, argv)
        except BaseException:
            # exec failed: never run the parent's cleanup in the child.
            os._exit(127)

    child = None
    try:
        child = _ChildProcess(pid, fd)
        fcntl.ioctl(fd, termios.TIOCSWINSZ,
                    struct.pack("HHHH", rows, cols, 0, 0))
        out = bytearray()
        start = time.time()
        idx = 0
        next_at = start + (script[0][0] if script else 0)
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
                elif isinstance(action, tuple) and action and action[0] == "signal":
                    os.kill(pid, signal_number(action[1]))
                else:
                    os.write(fd, action)
                idx += 1
                next_at = time.time() + (script[idx][0] if idx < len(script) else 0)
        return bytes(out)
    finally:
        if child is None:
            child = _ChildProcess(pid, fd)
        child.close()
