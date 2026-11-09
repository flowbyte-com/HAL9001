#!/usr/bin/env python3
"""Headless PTY checks for steering and the message queue.

The suite runs the real binary against a local OpenAI-compatible stub that
can hold a turn open and records every request it receives. That is what
makes the rules visible:

  - a message written while HAL is working waits in the queue, and it is
    shown as waiting;
  - the request that was already sent is never rewritten: neither the
    queued message nor the steering instruction is in it;
  - the waiting messages are sent when the turn ends, in the order they
    were written;
  - a steering instruction leads the next turn, because the running turn
    had no request left to carry it;
  - /queue shows the states, and /queue clear removes what is waiting
    without sending it.

The delays in a script are relative to the previous action, so the
operator's keystrokes land inside the window the stub holds the turn
open for.

Usage:

    go build -o /tmp/opencode/hal9001 ./cmd/hal9001
    HAL9001_BINARY=/tmp/opencode/hal9001 python3 tools/ptycheck/steering_checks.py
"""
import json
import os
import shutil
import socket
import subprocess
import sys
import tempfile
import time

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, HERE)
import pty_drive  # noqa: E402
from termsim import render  # noqa: E402

FAILURES = []
BINARY = os.environ.get("HAL9001_BINARY", "/tmp/opencode/hal9001")
PORT = 8131
SESSION = "steering"
COLS = 100
ROWS = 40

# The marker that asks the stub to hold the turn open. The stub strips it
# from the answer it streams back. The stub holds for four seconds, so
# every keystroke of a scenario has to land inside that window.
HOLD = "hold the line [[hold]]"
HOLD_WINDOW = 4.0

CONFIG = ""
WORKSPACE = ""
LOG = ""


def check(name, ok, detail=""):
    print(("[ok  ] " if ok else "[FAIL] ") + name + ("" if ok else "  " + str(detail)))
    if not ok:
        FAILURES.append(name)


def write_config(path):
    with open(path, "w") as fh:
        json.dump({
            "provider": "openai-compatible",
            "endpoint": "http://127.0.0.1:%d/v1" % PORT,
            "api_key": "test-key",
            "models": [{"id": "MiniMax-M3.1-Flash"}],
            "active_model": "MiniMax-M3.1-Flash",
            "theme": "HAL Classic",
        }, fh)


def wait_for_server(port):
    deadline = time.time() + 5.0
    while time.time() < deadline:
        try:
            with socket.create_connection(("127.0.0.1", port), timeout=0.2):
                return True
        except OSError:
            time.sleep(0.05)
    return False


def requests():
    """Return the requests the stub has recorded so far."""
    if not LOG or not os.path.isfile(LOG):
        return []
    out = []
    with open(LOG) as fh:
        for line in fh:
            line = line.strip()
            if not line:
                continue
            try:
                out.append(json.loads(line))
            except ValueError:
                continue
    return out


def drive(script, timeout):
    """Run the binary with a script and return the reconstructed screen.

    The requests the stub saw during this run are returned as well. The
    queue file is removed first: a message left over from an earlier
    scenario would wait for the operator and change what the scenario
    proves.
    """
    before = len(requests())
    try:
        os.remove(os.path.join(pty_drive.STORE, SESSION + ".queue.json"))
    except OSError:
        pass
    raw = pty_drive.run(script, cols=COLS, rows=ROWS, timeout=timeout,
                        fresh=True, session=SESSION,
                        args=["--workspace", WORKSPACE])
    return raw, render(raw, COLS, ROWS).text(), requests()[before:]


def main():
    global CONFIG, WORKSPACE, LOG
    if not os.path.isfile(BINARY):
        print("missing HAL9001 binary:", BINARY)
        return 2
    base = os.environ.get("HAL9001_PTY_TMPDIR", "/tmp/opencode")
    os.makedirs(base, exist_ok=True)
    root = tempfile.mkdtemp(prefix="hal9001-steering-", dir=base)
    home = os.path.join(root, "config")
    store = os.path.join(root, "sessions")
    WORKSPACE = os.path.join(root, "HAL")
    os.makedirs(home)
    os.makedirs(store)
    os.makedirs(WORKSPACE)
    CONFIG = os.path.join(home, "config.json")
    LOG = os.path.join(root, "requests.jsonl")
    write_config(CONFIG)

    pty_drive.BINARY = BINARY
    pty_drive.CONFIG = CONFIG
    pty_drive.HOME = home
    pty_drive.STORE = store
    os.environ["HAL9001_MPM_BIN"] = os.path.join(root, "missing-mpm")

    env = os.environ.copy()
    env["HAL9001_FAKE_PORT"] = str(PORT)
    env["HAL9001_STEER_LOG"] = LOG
    provider = subprocess.Popen(
        [sys.executable, os.path.join(HERE, "steering_provider.py")],
        stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, env=env)
    try:
        if not wait_for_server(PORT):
            print("the steering provider did not start")
            return 2

        # 1. A message written while HAL is working waits, and the
        #    request that was already sent is not rewritten.
        raw, screen, sent = drive([
            (0.8, HOLD.encode() + b"\r"),
            (0.9, b"first queued message\r"),
            (0.3, b"second queued message\r"),
            (0.3, b"/steer focus on the error instead\r"),
        ], timeout=HOLD_WINDOW - 0.4)

        check("the queued message is visible", "first queued message" in screen, screen)
        check("the waiting message says it is waiting", "queued" in screen, screen)
        check("the steering instruction says it is steering", "steering" in screen, screen)
        check("the activity line counts the waiting messages", "queued" in screen, screen)

        check("one request had gone out while HAL was working", len(sent) == 1,
              "the stub saw %d requests" % len(sent))
        if sent:
            first = json.dumps(sent[0])
            check("the request that was already sent is not rewritten",
                  "first queued message" not in first
                  and "second queued message" not in first
                  and "focus on the error" not in first, first[:400])

        # 2. The waiting messages are sent when the turn ends, in the
        #    order they were written, and the steer leads them.
        raw, screen, sent = drive([
            (0.8, HOLD.encode() + b"\r"),
            (0.9, b"first queued message\r"),
            (0.3, b"second queued message\r"),
            (0.3, b"/steer focus on the error instead\r"),
            (HOLD_WINDOW + 1.0, b""),
        ], timeout=HOLD_WINDOW + 4.0)

        lasts = [r.get("last_user", "") for r in sent]
        check("the stub saw four requests", len(sent) == 4, lasts)
        if len(lasts) == 4:
            check("the first request carried nothing of the queue",
                  "queued message" not in lasts[0]
                  and "focus on the error" not in lasts[0], lasts[0])
            check("the steering instruction leads the next turn",
                  "focus on the error" in lasts[1], lasts[1])
            check("the first waiting message is sent next",
                  "first queued message" in lasts[2], lasts[2])
            check("the second waiting message is sent last",
                  "second queued message" in lasts[3], lasts[3])
        check("the steering instruction reached the model",
              "GOT focus on the error" in screen, screen)
        check("the waiting messages reached the model",
              "GOT first queued message" in screen
              and "GOT second queued message" in screen, screen)
        check("the finished messages say they are done", "done" in screen, screen)

        # 3. /queue shows what is waiting.
        raw, screen, sent = drive([
            (0.8, HOLD.encode() + b"\r"),
            (0.9, b"a queued message\r"),
            (0.3, b"/queue\r"),
        ], timeout=HOLD_WINDOW - 0.4)

        check("the queue panel opens", "Queued messages" in screen, screen)
        check("the queue panel counts what is waiting", "1 waiting" in screen, screen)
        check("the queue panel names the message", "a queued message" in screen, screen)

        # 4. /queue clear removes what is waiting without sending it.
        raw, screen, sent = drive([
            (0.8, HOLD.encode() + b"\r"),
            (0.9, b"a queued message\r"),
            (0.3, b"/queue clear\r"),
            (HOLD_WINDOW + 1.0, b""),
        ], timeout=HOLD_WINDOW + 4.0)

        # The confirmation is drawn while the turn is running, and the
        # live status of the turn takes the notice row back a moment
        # later, so the confirmation is looked for in the byte stream.
        check("the operator is told the queue was cleared",
              b"cleared 1 waiting" in raw, screen)
        check("the cleared message was never sent",
              len(sent) == 1
              and all("a queued message" not in r.get("last_user", "") for r in sent),
              [r.get("last_user", "") for r in sent])
    finally:
        provider.terminate()
        try:
            provider.wait(timeout=5)
        except subprocess.TimeoutExpired:
            provider.kill()
        shutil.rmtree(root, ignore_errors=True)

    print()
    if FAILURES:
        print("FAILED: " + ", ".join(FAILURES))
        return 1
    print("ALL STEERING PTY CHECKS PASSED")
    return 0


if __name__ == "__main__":
    sys.exit(main())
