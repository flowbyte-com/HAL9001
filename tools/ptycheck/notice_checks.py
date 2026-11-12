#!/usr/bin/env python3
"""PTY checks for the transient notice of the interface.

The defect this suite exists for: a notice that was set while a turn was
running was gone before the operator could read it. The live status of a
turn and the transient notice shared one field, and the heartbeat of the
turn overwrote the notice every 140 milliseconds. The operator was told
that a message was queued and never saw it.

What is checked:

  * a message written while HAL is working is announced, and the
    announcement stays on the screen while the turn is running;
  * the announcement does not stay for ever: when it expires, the live
    status of the turn takes the row back, and the turn is still
    running;
  * a newer announcement replaces the older one, and the timer of the
    older one never clears the newer one;
  * the warning of a reply that was cut at max_tokens survives the turn
    that follows it;
  * a turn without a visible answer reports only the numbers of its own
    turn, never the numbers of the turn before it;
  * the turn itself is unchanged: the queued message is sent when the
    turn ends, and its answer arrives.

Usage:

    go build -o /tmp/opencode/hal9001 ./cmd/hal9001
    HAL9001_BINARY=/tmp/opencode/hal9001 python3 tools/ptycheck/notice_checks.py
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
from pty_session import Session  # noqa: E402

FAILURES = []
BINARY = os.environ.get("HAL9001_BINARY", "/tmp/opencode/hal9001")
# Every suite needs its own port: two providers on one port would answer
# each other's requests.
PORT = int(os.environ.get("HAL9001_NOTICE_PORT", "8286"))

# The question the operator asks. The provider holds the request that
# carries it, so the turn is in flight while the notice is set.
QUESTION = "what is the answer"
# The answer of the first turn. Nothing else in the interface can print
# it, so a match is the answer of the turn and nothing else.
ANSWER = "NOTICE-ANSWER-3b8c1d"
# The answer of the turn that carries the queued message.
ANSWER2 = "NOTICE-ANSWER-second-6e2f4a"
# The announcement of a message that was queued while HAL was working.
ANNOUNCEMENT = "queued — it is sent when this turn ends"
# The announcement of a queue that was emptied.
CLEARED = "cleared 1 waiting message"
# The model of the configuration.
MODEL = "MiniMax-M3.1-Flash"


def check(name, ok, detail=""):
    print(("[ok  ] " if ok else "[FAIL] ") + name + ("" if ok else "  " + str(detail)))
    if not ok:
        FAILURES.append(name)


def wait_for_server(port, timeout=5.0):
    """Wait until the provider is listening."""
    deadline = time.time() + timeout
    while time.time() < deadline:
        try:
            with socket.create_connection(("127.0.0.1", port), timeout=0.2):
                return True
        except OSError:
            time.sleep(0.05)
    return False


def read_json_lines(path):
    """The JSON documents of a file, one per line."""
    if not os.path.exists(path):
        return []
    out = []
    with open(path, "r", errors="replace") as fh:
        for line in fh:
            line = line.strip()
            if not line:
                continue
            try:
                out.append(json.loads(line))
            except ValueError:
                pass
    return out


def status_line(screen):
    """The live status of the turn, if the screen shows one."""
    for row in screen.split("\n"):
        if ("Waiting for model" in row or "Streaming" in row
                or "Thinking" in row or "Tool" in row):
            return row
    return None


class Harness:
    """One isolated environment: config, home, store, workspace, provider."""

    def __init__(self, root, name, port, session="notice", mode="release"):
        self.root = os.path.join(root, name)
        self.home = os.path.join(self.root, "home")
        self.store = os.path.join(self.home, "sessions")
        self.workspace = os.path.join(self.root, "HAL")
        self.config = os.path.join(self.home, "config.json")
        self.log = os.path.join(self.root, "requests.jsonl")
        self.port = port
        self.session = session
        self.mode = mode
        # The file that releases the held request: the check decides
        # when the answer is given.
        self.release = os.path.join(self.root, "release-1")
        self.provider = None

    def start(self):
        """Write the configuration and start the provider."""
        os.makedirs(self.store)
        os.makedirs(self.workspace)
        with open(self.config, "w") as fh:
            json.dump({
                "provider": "openai-compatible",
                "endpoint": "http://127.0.0.1:%d/v1" % self.port,
                "api_key": "test-key",
                "models": [{"id": MODEL}],
                "active_model": MODEL,
                "theme": "HAL Classic",
            }, fh)
        env = os.environ.copy()
        env["HAL9001_HOLDING_PORT"] = str(self.port)
        env["HAL9001_REQUEST_LOG"] = self.log
        env["HAL9001_HOLDING_MODE"] = self.mode
        env["HAL9001_HOLDING_ANSWER"] = ANSWER
        env["HAL9001_HOLDING_ANSWER2"] = ANSWER2
        env["HAL9001_HOLDING_RELEASE"] = self.release
        self.provider = subprocess.Popen(
            [sys.executable, os.path.join(HERE, "holding_provider.py")],
            stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, env=env)
        if not wait_for_server(self.port):
            raise RuntimeError("the holding provider did not start")

    def requests(self):
        """The requests the provider recorded so far."""
        return read_json_lines(self.log)

    def release_first(self):
        """Let the held request go: the answer is on its way."""
        with open(self.release, "w") as fh:
            fh.write("go\n")

    def close(self):
        """Stop the provider and take the environment down."""
        if self.provider is not None:
            self.provider.terminate()
            try:
                self.provider.wait(timeout=5)
            except subprocess.TimeoutExpired:
                self.provider.kill()
            self.provider = None


def open_session(harness):
    """Start the interface and wait until it is ready."""
    session = Session(BINARY, harness.config, harness.home, harness.store,
                      harness.session, harness.workspace)
    session.start()
    session.wait_for("Enter send", 25.0)
    return session


def ask(session, harness, text=QUESTION):
    """Ask a question and wait until the provider holds its request."""
    session.send(text.encode() + b"\r")
    return session.until(lambda: len(harness.requests()) == 1, 20.0)


def scenario_notice_during_a_turn(harness):
    """A notice set while a turn is running stays on the screen."""
    session = open_session(harness)
    try:
        # 1. The operator asks a question. The provider holds it, so the
        #    turn is in flight and nothing has arrived yet.
        check("the provider received the question", ask(session, harness),
              len(harness.requests()))
        check("the live status of the turn is on the screen",
              status_line(session.screen()) is not None, session.screen())

        # 2. The operator writes the next message while HAL is working.
        #    The interface tells them that it is waiting.
        session.send(b"a queued message\r")
        check("the message is announced",
              session.until(lambda: ANNOUNCEMENT in session.screen(), 10.0),
              session.screen())
        check("the message was not sent yet",
              len(harness.requests()) == 1, len(harness.requests()))

        # 3. The heartbeat of the turn used to overwrite the announcement
        #    within a tenth of a second. It must not: the announcement
        #    has to be readable while the turn runs.
        for index in range(3):
            session.wait(0.4)
            check("the announcement is still on the screen (%d)" % (index + 1),
                  ANNOUNCEMENT in session.screen(), session.screen())

        # 4. The announcement is transient. When it expires, the live
        #    status of the turn takes the row back, and the turn is
        #    still running.
        session.wait(5.5)
        screen = session.screen()
        check("the announcement is gone after its lifetime",
              ANNOUNCEMENT not in screen, screen)
        check("the live status of the turn is back",
              status_line(screen) is not None, screen)
        check("the turn is still running",
              "Esc cancels" in (status_line(screen) or ""), screen)

        # 5. The turn itself is unchanged: the queued message is sent
        #    when the turn ends, and its answer arrives.
        harness.release_first()
        check("the turn ends",
              session.until(lambda: "Complete" in session.screen(), 25.0),
              session.screen())
        check("the queued message was sent when the turn ended",
              session.until(lambda: len(harness.requests()) == 2, 25.0),
              len(harness.requests()))
        check("the answer of the queued message arrived",
              session.until(lambda: ANSWER2 in session.screen(), 25.0),
              session.screen())
        recorded = harness.requests()
        if len(recorded) >= 2:
            carried = [m.get("content", "") for m in recorded[1].get("messages", [])
                       if m.get("role") == "user"]
            check("the provider received the queued message",
                  any("a queued message" in text for text in carried), carried)
    finally:
        session.close()


def scenario_a_newer_notice_replaces_the_older_one(harness):
    """The notice that was set last is the one on the screen."""
    session = open_session(harness)
    try:
        check("the provider received the question", ask(session, harness),
              len(harness.requests()))
        session.send(b"a queued message\r")
        check("the message is announced",
              session.until(lambda: ANNOUNCEMENT in session.screen(), 10.0),
              session.screen())

        # A newer announcement replaces the older one.
        session.send(b"/queue clear\r")
        check("the newer announcement is on the screen",
              session.until(lambda: CLEARED in session.screen(), 10.0),
              session.screen())
        check("the older announcement is gone",
              ANNOUNCEMENT not in session.screen(), session.screen())

        # The timer of the older announcement must not clear the newer
        # one, and the newer one expires on its own as well.
        session.wait(6.0)
        screen = session.screen()
        check("the newer announcement expired on its own",
              CLEARED not in screen, screen)
        check("the live status of the turn is back",
              status_line(screen) is not None, screen)
    finally:
        session.close()


def scenario_the_warning_survives_the_next_turn(harness):
    """A reply that was cut at max_tokens stays visible.

    A queued message starts the next turn as soon as the reply arrives,
    and the notice under the footer is replaced by the status of that
    turn. The warning is a note in the conversation, so the operator can
    still see that the reply was cut.
    """
    session = open_session(harness)
    try:
        check("the provider received the question", ask(session, harness),
              len(harness.requests()))
        # The operator writes the next message while HAL is working.
        session.send(b"a queued message\r")
        session.until(lambda: ANNOUNCEMENT in session.screen(), 10.0)

        # The answer is cut at the output limit.
        harness.release_first()
        session.until(lambda: "max_tokens" in session.screen(), 25.0)
        check("the reply was cut at the output limit",
              "max_tokens" in session.screen(), session.screen())

        # The queued turn starts on its own.
        check("the queued message was sent when the turn ended",
              session.until(lambda: len(harness.requests()) == 2, 25.0),
              len(harness.requests()))
        session.wait(1.0)
        screen = session.screen()
        flat = " ".join(screen.split())
        check("the warning is still in the conversation",
              "cut at max_tokens" in flat, screen[-1200:])
        # The warning is recorded once, and not again by the turn that
        # followed it.
        check("the warning is recorded once",
              flat.count("cut at max_tokens") == 1, screen[-1200:])
    finally:
        session.close()


def scenario_the_empty_answer_reports_only_its_own_turn(harness):
    """A turn without a visible answer reports only what it knows.

    The turn before it reached its output limit. The numbers of that
    turn are not evidence about this one: the interface must not tell
    the operator that this turn exhausted its output budget.
    """
    session = open_session(harness)
    try:
        check("the provider received the question", ask(session, harness),
              len(harness.requests()))
        # The first answer is cut at the output limit.
        harness.release_first()
        session.until(lambda: "max_tokens" in session.screen(), 25.0)
        check("the first reply was cut at the output limit",
              "max_tokens" in session.screen(), session.screen())

        # The second turn ends without a visible answer.
        session.send(b"and now?\r")
        session.until(lambda: "no visible answer" in session.screen(), 25.0)
        screen = session.screen()
        flat = " ".join(screen.split())
        check("the second turn is told that it produced no visible answer",
              "no visible answer" in flat, screen[-1200:])
        check("the second turn does not claim that it exhausted its output budget",
              "output budget was exhausted" not in flat, screen[-1200:])
        check("the second turn does not borrow the finish reason of the first",
              "finish_reason=length" not in flat, screen[-1200:])
        # The first turn keeps its own warning, written once.
        check("the truncation warning of the first turn is still there",
              flat.count("cut at max_tokens") == 1, screen[-1600:])
    finally:
        session.close()


def main():
    if not os.path.isfile(BINARY):
        print("missing HAL9001 binary:", BINARY)
        return 2
    base = os.environ.get("HAL9001_PTY_TMPDIR", "/tmp/opencode")
    os.makedirs(base, exist_ok=True)
    root = tempfile.mkdtemp(prefix="hal-notice-", dir=base)

    print("== a notice set while a turn is running ==")
    first = Harness(root, "notice", PORT, session="notice")
    first.start()
    try:
        scenario_notice_during_a_turn(first)
    finally:
        first.close()

    print("== a newer notice replaces the older one ==")
    second = Harness(root, "notice-newer", PORT + 1, session="notice2")
    second.start()
    try:
        scenario_a_newer_notice_replaces_the_older_one(second)
    finally:
        second.close()

    print("== a reply that was cut at max_tokens keeps its warning ==")
    cut = Harness(root, "notice-cut", PORT + 2, session="notice3",
                  mode="release-length")
    cut.start()
    try:
        scenario_the_warning_survives_the_next_turn(cut)
    finally:
        cut.close()

    print("== a turn without a visible answer reports only its own turn ==")
    empty = Harness(root, "notice-empty", PORT + 3, session="notice4",
                    mode="release-length-empty")
    empty.start()
    try:
        scenario_the_empty_answer_reports_only_its_own_turn(empty)
    finally:
        empty.close()
        shutil.rmtree(root, ignore_errors=True)

    print()
    if FAILURES:
        print("FAILED: " + ", ".join(FAILURES))
        return 1
    print("ALL NOTICE PTY CHECKS PASSED")
    return 0


if __name__ == "__main__":
    sys.exit(main())
