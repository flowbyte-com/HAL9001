#!/usr/bin/env python3
"""Headless PTY checks for the enforced context budget.

The suite runs the real binary against a provider that records every
request it receives. A conversation of forty-one messages is stored
before the run, and the request that leaves the machine is compared
with the budget that is enforced.

The operator's window is a **total**: the room for the answer comes out
of it, and the request may use what is left. The suite therefore
configures both numbers and compares the transmitted request with the
room that is really left, not with the whole window.

What is checked:

* with no operator limit the conversation is still bounded: the
  provisional total applies, the request that leaves the machine fits
  it, and the interface says that the budget is provisional rather than
  a fact about the model;
* with a configured window and a configured answer cap the request that
  is transmitted is smaller, it fits the room that is left of the
  window, it keeps the newest request, and the interface says that older
  context was dropped;
* with a hundred-token window nothing is transmitted at all: the request
  cannot be reduced, so it is refused with an explanation instead of
  being truncated;
* with an answer cap that fills the whole window the two numbers
  contradict each other, and the contradiction is reported instead of
  being guessed at.

Usage:

    go build -o /tmp/opencode/hal9001 ./cmd/hal9001
    HAL9001_BINARY=/tmp/opencode/hal9001 python3 tools/ptycheck/budget_checks.py
"""
import json
import os
import re
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

# The provisional budget: the provider package uses a 32768 total window
# when nothing is known, and the OpenAI protocol's 15000 answer cap
# comes out of it, so 17768 tokens are left for the request. The suite
# has to compare the transmitted request with the number that is
# enforced, so the numbers are written down here.
PROVISIONAL_TOTAL = 32768
PROVISIONAL_ANSWER = 15000
PROVISIONAL_INPUT = PROVISIONAL_TOTAL - PROVISIONAL_ANSWER


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

    def write_config(window=0, answer=0):
        record = {"id": "test-model"}
        if window:
            record["context_window"] = window
        if answer:
            record["max_output_tokens"] = answer
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

    def capture(session, cols=120, rows=40, script=None):
        if script is None:
            script = [(2.0, b"hello there\r"), (3.0, b"")]
        timeout = sum(delay for delay, _ in script) + 4.0
        raw = pty_drive.run(script, cols=cols, rows=rows, timeout=timeout,
                            fresh=False, session=session,
                            args=["--workspace", workspace])
        return raw, render(raw, cols, rows).text()

    try:
        if not wait_for_server():
            print("the recording provider did not start")
            return 2

        # 1. Without an operator limit the conversation is still bounded.
        #    The provisional total applies, and the interface says that it
        #    is provisional rather than a fact about the model.
        write_config()
        seed("budget-base")
        raw, screen = capture("budget-base")
        text = raw.decode("utf-8", "replace")
        recorded = requests()
        check("the provider received a request", len(recorded) == 1, len(recorded))
        if not recorded:
            return 1
        baseline = recorded[0]
        baseline_messages = len(baseline.get("messages", []))
        baseline_size = estimate(baseline)
        check("the whole conversation was not transmitted",
              baseline_messages < MESSAGES + 1, baseline_messages)
        check("the request fits the provisional budget",
              baseline_size <= PROVISIONAL_INPUT,
              "%d > %d" % (baseline_size, PROVISIONAL_INPUT))
        _, diag = capture("budget-base",
                          script=[(1.8, b""), (0.1, b"/diag\r"), (1.2, b"")])
        check("the interface says the budget is provisional",
              "provisional" in diag, diag)

        # 2. A configured window bounds the request that is transmitted.
        #    The window is a total: the room for the answer comes out of
        #    it, so the request may use what is left.
        window = max(4000, baseline_size)
        answer = window // 2
        write_config(window, answer)
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
        check("the transmitted request fits the room that is left of the window",
              estimate(bounded) <= window - answer,
              "%d > %d" % (estimate(bounded), window - answer))
        check("the newest request was transmitted",
              any("hello there" in (m.get("content") or "")
                  for m in user_messages(bounded)),
              [m.get("content", "")[:40] for m in user_messages(bounded)])
        check("the interface says that older context was dropped",
              "dropped to fit" in text, screen)

        # 3. A hundred-token window cannot be met: nothing is sent.
        write_config(100)
        seed("budget-tiny")
        raw, screen = capture("budget-tiny")
        text = raw.decode("utf-8", "replace")
        recorded = requests()
        check("nothing was transmitted", len(recorded) == 2, len(recorded))
        check("the refusal explains itself",
              "context budget" in text and "estimated" in text, screen)

        # 4. An answer cap that fills the whole window contradicts the
        #    window, and the contradiction is reported. The screen is
        #    wide here: the notice is one line, cut to the width of the
        #    terminal, and the message names both numbers.
        write_config(8192, 8192)
        seed("budget-contradiction")
        raw, screen = capture("budget-contradiction", cols=200, rows=45)
        text = raw.decode("utf-8", "replace")
        recorded = requests()
        check("a contradictory window and answer cap transmit nothing",
              len(recorded) == 2, len(recorded))
        check("the contradiction is reported with both numbers",
              "the answer is allowed 8192 tokens" in text and
              "context window is 8192 tokens" in text and
              "does not fit inside the window" in text,
              screen)
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
