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


def run(script, cols=80, rows=24, timeout=12.0, fresh=True, session=None,
        args=None):
    """Run the interface and send the scripted actions.

    script: list of (delay, payload) pairs. The payload is either bytes
    to write, a ("resize", cols, rows) tuple or a ("signal", signum)
    tuple. session selects a stored conversation (default demo). args
    holds extra command-line arguments for the binary, for example
    ["--no-mouse"].
    """
    session = SESSION if session is None else session
    if fresh:
        reset_session(session)
    pid, fd = pty.fork()
    if pid == 0:
        os.environ["TERM"] = "xterm-256color"
        os.environ["HAL9001_CONFIG"] = CONFIG
        os.environ["HAL9001_HOME"] = HOME
        argv = [BINARY, "--store", STORE]
        if session:
            # An explicit --session wins over the stored active session;
            # omit it to exercise the persisted choice.
            argv = [BINARY, "--session", session, "--store", STORE]
        argv.extend(args or [])
        os.execv(BINARY, argv)
    fcntl.ioctl(fd, termios.TIOCSWINSZ, struct.pack("HHHH", rows, cols, 0, 0))
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
    try:
        os.close(fd)
    except OSError:
        pass
    try:
        os.waitpid(pid, os.WNOHANG)
    except ChildProcessError:
        pass
    return bytes(out)
