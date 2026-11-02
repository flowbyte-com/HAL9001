#!/usr/bin/env python3
"""Drive the HAL9001 binary inside a real pseudo-terminal.

The script forks a pty, starts hal9001 inside it and feeds it a scripted
sequence of key presses and window resizes. The raw byte stream is
returned so it can be replayed through a terminal emulator.

Usage:

    from pty_drive import run
    raw = run([(2.0, b"/help\\r"), (1.0, b"\\x1b")])
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


def reset_session():
    """Start from an empty conversation."""
    path = os.path.join(STORE, SESSION + ".jsonl")
    try:
        os.remove(path)
    except FileNotFoundError:
        pass


def run(script, cols=80, rows=24, timeout=12.0, fresh=True):
    """Run the interface and send the scripted actions.

    script: list of (delay, payload) pairs. The payload is either bytes
    to write or a ("resize", cols, rows) tuple.
    """
    if fresh:
        reset_session()
    pid, fd = pty.fork()
    if pid == 0:
        os.environ["TERM"] = "xterm-256color"
        os.environ["HAL9001_CONFIG"] = CONFIG
        os.environ["HAL9001_HOME"] = HOME
        os.execv(BINARY, [BINARY, "--session", SESSION, "--store", STORE])
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
