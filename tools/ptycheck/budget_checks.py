#!/usr/bin/env python3
"""Headless PTY checks for the enforced context budget.

The suite runs the real binary against a provider that records every
request it receives. A conversation of forty-one messages is stored
before the run, and the request that leaves the machine is compared
with the limit the operator configured.

What is checked:

* with no limit the whole conversation is transmitted;
* with a configured limit the request that is transmitted is smaller,
  it fits the limit, and the interface says that older context was
  dropped;
* with a hundred-token limit nothing is transmitted at all: the
  request cannot be reduced, so it is refused with an explanation
  instead of being truncated.

Usage:

    go build -o /tmp/opencode/hal9001 ./cmd/hal9001
    HAL9001_BINARY=/tmp/opencode/hal9001 python3 tools/ptycheck/budget_checks.py
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
PORT = int(os.environ.get("HAL9001_RECORDER_PORT", "8265"))

# The stored conversation: forty-one messages, each big enough that the
# request they make is far larger than any sensible budget.
MESSAGES = 41
MESSAGE_SIZE = 4000


def check(name, ok, detail=""):
    print(("[ok  ] " if ok else "[FAIL] ") + name + ("" if ok else "  " + str(detail)))
    if not ok:
        FAILURES.append(name)


def wait_for_server():
    deadline = time.time() + 5.0
    while time.time() < deadline:
        try:
            with socket.create_connection(("127.0.0.1", PORT), timeout=0.2):
                return True
        except OSError:
            time.sleep(0.05)
    return False


def estimate(request):
    """The size of a request in the same rough tokens HAL estimates.

    It is the same rule the engine uses: four characters per token plus
    a small overhead per message and per tool. It is an estimate, and
    the check treats it as one.
    """
    total = 0
    for message in request.get("messages", []):
        total += len(message.get("content") or "") // 4 + 4
    for tool in request.get("tools", []):
        function = tool.get("function", {})
        total += len(function.get("name", "")) // 4
        total += len(function.get("description", "") or "") // 4
        parameters = function.get("parameters", "")
        if not isinstance(parameters, str):
            parameters = json.dumps(parameters)
        total += len(parameters) // 4 + 8
    return total


def user_messages(request):
    """The user messages of a request."""
    return [m for m in request.get("messages", []) if m.get("role") == "user"]


def main():
    if not os.path.isfile(BINARY):
        print("missing HAL9001 binary:", BINARY)
        return 2
    base = os.environ.get("HAL9001_PTY_TMPDIR", "/tmp/opencode")
    os.makedirs(base, exist_ok=True)
    root = tempfile.mkdtemp(prefix="hal-budget-", dir=base)
    home = os.path.join(root, "home")
    store = os.path.join(home, "sessions")
    workspace = os.path.join(root, "HAL")
    os.makedirs(store)
    os.makedirs(workspace)
    config = os.path.join(home, "config.json")
    log = os.path.join(root, "requests.jsonl")

    pty_drive.BINARY = BINARY
    pty_drive.CONFIG = config
    pty_drive.HOME = home
    pty_drive.STORE = store

    env = os.environ.copy()
    env["HAL9001_RECORDER_PORT"] = str(PORT)
    env["HAL9001_REQUEST_LOG"] = log
    provider = subprocess.Popen(
        [sys.executable, os.path.join(HERE, "recording_provider.py")],
        stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, env=env)

    def write_config(limit=0):
        record = {"id": "test-model"}
        if limit:
            record["context_window"] = limit
        with open(config, "w") as fh:
            json.dump({
                "provider": "openai-compatible",
                "endpoint": "http://127.0.0.1:%d/v1" % PORT,
                "api_key": "test-key",
                "models": [record],
                "active_model": "test-model",
                "theme": "HAL Classic",
            }, fh)

    def seed(session):
        """Store a conversation of forty-one messages."""
        path = os.path.join(store, session + ".jsonl")
        with open(path, "w") as fh:
            for i in range(MESSAGES):
                role = "user" if i % 2 == 0 else "assistant"
                body = ("message %02d: " % i) + "x" * MESSAGE_SIZE
                fh.write(json.dumps({"role": role, "content": body}) + "\n")

    def requests():
        """The requests the provider recorded so far."""
        if not os.path.exists(log):
            return []
        out = []
        with open(log, "r", errors="replace") as fh:
            for line in fh:
                line = line.strip()
                if not line:
                    continue
                try:
                    out.append(json.loads(line))
                except ValueError:
                    pass
        return out

    def capture(session, cols=120, rows=40):
        timeout = 12.0
        raw = pty_drive.run([(2.0, b"hello there\r"), (3.0, b"")], cols=cols,
                            rows=rows, timeout=timeout, fresh=False,
                            session=session,
                            args=["--workspace", workspace])
        return raw, render(raw, cols, rows).text()

    try:
        if not wait_for_server():
            print("the recording provider did not start")
            return 2

        # 1. Without a limit the whole conversation is transmitted.
        write_config()
        seed("budget-base")
        raw, screen = capture("budget-base")
        recorded = requests()
        check("the provider received a request", len(recorded) == 1, len(recorded))
        if not recorded:
            return 1
        baseline = recorded[0]
        baseline_messages = len(baseline.get("messages", []))
        check("the whole conversation was transmitted",
              baseline_messages >= MESSAGES + 1, baseline_messages)
        baseline_size = estimate(baseline)

        # 2. A configured limit bounds the request that is transmitted.
        limit = max(1000, baseline_size // 2)
        write_config(limit)
        seed("budget-limited")
        raw, screen = capture("budget-limited")
        text = raw.decode("utf-8", "replace")
        recorded = requests()
        check("the provider received a second request", len(recorded) == 2,
              len(recorded))
        if len(recorded) < 2:
            return 1
        bounded = recorded[1]
        bounded_messages = len(bounded.get("messages", []))
        check("the transmitted request is smaller",
              bounded_messages < baseline_messages,
              "%d of %d messages" % (bounded_messages, baseline_messages))
        check("the transmitted request fits the limit",
              estimate(bounded) <= limit,
              "%d > %d" % (estimate(bounded), limit))
        check("the newest request was transmitted",
              any("hello there" in (m.get("content") or "")
                  for m in user_messages(bounded)),
              [m.get("content", "")[:40] for m in user_messages(bounded)])
        check("the interface says that older context was dropped",
              "dropped to fit" in text, screen)

        # 3. A hundred-token limit cannot be met: nothing is sent.
        write_config(100)
        seed("budget-tiny")
        raw, screen = capture("budget-tiny")
        text = raw.decode("utf-8", "replace")
        recorded = requests()
        check("nothing was transmitted", len(recorded) == 2, len(recorded))
        check("the refusal explains itself",
              "context budget" in text and "estimated" in text, screen)
    finally:
        provider.terminate()
        try:
            provider.wait(timeout=5)
        except subprocess.TimeoutExpired:
            provider.kill()
        shutil.rmtree(root, ignore_errors=True)

    if FAILURES:
        print("%d BUDGET PTY CHECKS FAILED" % len(FAILURES))
        return 1
    print("ALL BUDGET PTY CHECKS PASSED")
    return 0


if __name__ == "__main__":
    sys.exit(main())
