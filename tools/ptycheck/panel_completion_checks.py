#!/usr/bin/env python3
"""PTY checks for a turn that completes while a panel is open.

The defect this suite exists for: while a turn was running, opening
/help, /diag, /model, /theme, /sessions, /usage or /effort swallowed
the messages of the running exchange. The answer never arrived, the
interface kept saying it was sending, and the exchange was lost.

The suite runs the real binary against a local provider that holds a
request until the check says "now". The operator asks a question, opens
a panel while the turn is in flight, and only then lets the provider
answer. What happened afterwards is checked against what the operator
can see and against what the provider and the session store recorded.

What is checked:

* every one of the seven panels: the completion is processed while the
  panel is open, the answer is in the conversation once the panel is
  closed, it is there exactly once, the turn is over, and the next
  question reaches the provider;
* a provider failure while a panel is open: the failure reaches the
  conversation, and the exchange can be continued once the panel is
  closed;
* a turn that is stopped while a panel is open: the panel owns the
  keyboard, so the stop is typed again after the panel is closed, and
  the exchange is continued with /resume;
* an answer that arrives in pieces while a panel is open;
* a tool call whose activity arrives while a panel is open;
* the queue panel of the steering branch, when the binary has one, and
  a message that is written while HAL is working and sent when the turn
  ends.

The answers are distinctive on purpose: no panel and no session line
can produce "PANEL-ANSWER-..." by accident, so a match is the real
answer and not something the interface printed for its own reasons.

Usage:

    go build -o /tmp/opencode/hal9001 ./cmd/hal9001
    HAL9001_BINARY=/tmp/opencode/hal9001 python3 tools/ptycheck/panel_completion_checks.py
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
from pty_session import Session, plain  # noqa: E402

FAILURES = []
SKIPPED = []
BINARY = os.environ.get("HAL9001_BINARY", "/tmp/opencode/hal9001")
# Every harness needs its own port: two providers on one port would
# answer each other's requests.
PORT_BASE = int(os.environ.get("HAL9001_PANEL_PORT", "8270"))

# The question the operator asks. The provider holds the request that
# carries it, so the turn is in flight while the panel is opened.
QUESTION = "what is the answer"
# The answer of the first turn. Nothing else in the interface can print
# it, so a match is the answer of the turn and nothing else.
ANSWER = "PANEL-ANSWER-7f3a9c"
# The answer of every later turn.
ANSWER2 = "PANEL-ANSWER-second-4c1e2b"
# The model of the configuration. It exposes reasoning effort, so the
# /effort panel opens instead of explaining that the model has none.
MODEL = "MiniMax-M3.1-Flash"

# The panels, with the line that names them. The line is what tells the
# check that the panel is really open.
PANELS = [
    ("/help", "Help · Keys and commands"),
    ("/diag", "Last turn — diagnostics"),
    ("/model", "Models (Up and down choose"),
    ("/theme", "Themes (Enter applies"),
    ("/sessions", "Sessions (Enter resumes"),
    ("/usage", "Session usage"),
    ("/effort", "Reasoning effort"),
]

# The queue panel of the steering branch. It is a control: that panel
# already handed the messages it did not use to the conversation, so the
# answer has to arrive there as well.
QUEUE_PANEL = ("/queue", "Queued messages")


def check(name, ok, detail=""):
    print(("[ok  ] " if ok else "[FAIL] ") + name + ("" if ok else "  " + str(detail)))
    if not ok:
        FAILURES.append(name)


def skip(name, why):
    print("[skip] " + name + "  " + why)
    SKIPPED.append(name)


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


def user_messages(request):
    """The user messages of a request."""
    return [m for m in request.get("messages", []) if m.get("role") == "user"]


class Harness:
    """One isolated environment: config, home, store, workspace, provider."""

    def __init__(self, root, name, port, mode="release", session="panel",
                 answer=ANSWER, answer2=ANSWER2, tool="read", model=MODEL,
                 hold_second=False):
        self.root = os.path.join(root, name)
        self.home = os.path.join(self.root, "home")
        self.store = os.path.join(self.home, "sessions")
        self.workspace = os.path.join(self.root, "HAL")
        self.config = os.path.join(self.home, "config.json")
        self.log = os.path.join(self.root, "requests.jsonl")
        self.port = port
        self.mode = mode
        self.session = session
        self.answer = answer
        self.answer2 = answer2
        self.tool = tool
        self.model = model
        self.hold_second = hold_second
        # The files that release the held requests. The provider waits
        # for them, so the check decides when the answer is given.
        self.release1 = os.path.join(self.root, "release-1")
        self.release2 = os.path.join(self.root, "release-2")
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
                "models": [{"id": self.model}],
                "active_model": self.model,
                "theme": "HAL Classic",
            }, fh)
        env = os.environ.copy()
        env["HAL9001_HOLDING_PORT"] = str(self.port)
        env["HAL9001_REQUEST_LOG"] = self.log
        env["HAL9001_HOLDING_MODE"] = self.mode
        env["HAL9001_HOLDING_ANSWER"] = self.answer
        env["HAL9001_HOLDING_ANSWER2"] = self.answer2
        env["HAL9001_HOLDING_TOOL"] = self.tool
        env["HAL9001_HOLDING_RELEASE"] = self.release1
        if self.hold_second:
            env["HAL9001_HOLDING_RELEASE2"] = self.release2
        self.provider = subprocess.Popen(
            [sys.executable, os.path.join(HERE, "holding_provider.py")],
            stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, env=env)
        if not wait_for_server(self.port):
            raise RuntimeError("the holding provider did not start")

    def requests(self):
        """The requests the provider recorded so far."""
        return read_json_lines(self.log)

    def conversation(self):
        """The stored conversation of the session."""
        return read_json_lines(os.path.join(self.store, self.session + ".jsonl"))

    def answers(self):
        """The answers the session stored."""
        return [m.get("content", "") for m in self.conversation()
                if m.get("role") == "assistant"]

    def release_first(self):
        """Let the held request go: the answer is on its way."""
        with open(self.release1, "w") as fh:
            fh.write("go\n")

    def release_second(self):
        """Let the second held request go."""
        with open(self.release2, "w") as fh:
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


def open_panel(session, command, title):
    """Open a panel and wait until it is on screen."""
    session.send(command.encode() + b"\r")
    return session.until(lambda: title in session.screen(), 10.0)


def close_panel(session, title):
    """Close the panel that is open."""
    session.send(b"\x1b")
    return session.until(lambda: title not in session.screen(), 10.0)


def scenario_panel(harness, command, title):
    """One panel: the answer has to arrive while it is open."""
    session = open_session(harness)
    try:
        # 1. The operator asks a question. The provider holds it, so the
        #    turn is in flight and nothing has arrived yet.
        check("the provider received the question", ask(session, harness),
              len(harness.requests()))
        check("no answer has arrived yet", ANSWER not in session.screen(),
              session.screen())

        # 2. The operator opens the panel while the turn is running.
        check("the panel opens while the turn runs",
              open_panel(session, command, title), session.screen())

        # 3. The provider answers now: the completion reaches the
        #    interface while the panel is open.
        harness.release_first()
        check("the completion is processed while the panel is open",
              session.until(lambda: "Complete" in session.screen(), 25.0),
              session.screen())
        check("the panel is still open", title in session.screen(),
              session.screen())

        # 4. The panel closes and the conversation is there.
        check("the panel closes", close_panel(session, title), session.screen())
        screen = session.screen()
        check("the answer reached the conversation", ANSWER in screen, screen)
        check("the answer reached the conversation exactly once",
              screen.count(ANSWER) == 1, screen)

        # 5. The turn is over: the next question reaches the provider.
        session.send(b"the second question\r")
        check("the interface is usable again",
              session.until(lambda: len(harness.requests()) == 2, 20.0),
              len(harness.requests()))
        check("the second answer arrived",
              session.until(lambda: ANSWER2 in session.screen(), 25.0),
              session.screen())
        screen = session.screen()
        check("the second answer arrived exactly once",
              screen.count(ANSWER2) == 1, screen)
        check("the first answer is still there exactly once",
              screen.count(ANSWER) == 1, screen)

        # 6. The stored conversation holds one answer for each turn and
        #    no more: the exchange was neither lost nor doubled.
        answers = harness.answers()
        check("the conversation holds the first answer exactly once",
              answers.count(ANSWER) == 1, answers)
        check("the conversation holds the second answer exactly once",
              answers.count(ANSWER2) == 1, answers)
    finally:
        session.close()


def scenario_provider_failure(harness):
    """The provider fails while a panel is open."""
    session = open_session(harness)
    try:
        check("the provider received the question", ask(session, harness),
              len(harness.requests()))
        check("the panel opens while the turn runs",
              open_panel(session, "/help", "Help · Keys and commands"),
              session.screen())

        # The provider fails the request now: the failure reaches the
        # interface while the panel is open.
        harness.release_first()
        check("the failure is processed while the panel is open",
              session.until(lambda: "error:" in session.screen(), 25.0),
              session.screen())

        check("the panel closes", close_panel(session, "Help · Keys and commands"),
              session.screen())
        screen = session.screen()
        check("the failure reached the conversation",
              "I couldn't answer that" in screen, screen)
        check("the failure names the provider error", "503" in screen, screen)

        # The exchange can be continued: the question was not lost.
        session.send(b"/resume\r")
        check("the failed exchange was continued",
              session.until(lambda: len(harness.requests()) == 2, 25.0),
              len(harness.requests()))
        check("the continued answer arrived",
              session.until(lambda: ANSWER2 in session.screen(), 25.0),
              session.screen())
        check("the continued answer arrived exactly once",
              session.screen().count(ANSWER2) == 1, session.screen())
    finally:
        session.close()


def scenario_stop_with_panel(harness):
    """A turn that is stopped while a panel is open."""
    session = open_session(harness)
    try:
        check("the provider received the question", ask(session, harness),
              len(harness.requests()))
        check("the panel opens while the turn runs",
              open_panel(session, "/effort", "Reasoning effort"),
              session.screen())

        # The panel owns the keyboard: the keys of /stop are read by the
        # panel, not by the composer, so nothing is stopped yet. The
        # Enter is left out on purpose: every panel has its own meaning
        # for it, and this check is about the keyboard, not about the
        # panel's action.
        session.send(b"/stop")
        session.wait(1.0)
        check("the panel keeps the keyboard",
              "Reasoning effort" in session.screen(), session.screen())
        check("the turn is still running",
              ANSWER not in session.screen() and len(harness.requests()) == 1,
              session.screen())

        # The panel is closed and the stop is typed where it belongs.
        check("the panel closes", close_panel(session, "Reasoning effort"),
              session.screen())
        check("the keys of the stop never reached the composer",
              "/stop" not in session.screen(), session.screen())
        session.send(b"/stop\r")
        check("the turn was stopped",
              session.until(lambda: "cancelled" in session.screen(), 20.0),
              session.screen())

        # The exchange can be continued, and the answer arrives.
        session.send(b"/resume\r")
        check("the stopped exchange was continued",
              session.until(lambda: len(harness.requests()) == 2, 25.0),
              len(harness.requests()))
        check("the continued answer arrived",
              session.until(lambda: ANSWER2 in session.screen(), 25.0),
              session.screen())
        check("the continued answer arrived exactly once",
              session.screen().count(ANSWER2) == 1, session.screen())
    finally:
        session.close()


def scenario_streaming(harness):
    """An answer that arrives in pieces while a panel is open."""
    session = open_session(harness)
    try:
        check("the provider received the question", ask(session, harness),
              len(harness.requests()))
        check("the panel opens while the turn runs",
              open_panel(session, "/help", "Help · Keys and commands"),
              session.screen())

        harness.release_first()
        check("the completion is processed while the panel is open",
              session.until(lambda: "Complete" in session.screen(), 25.0),
              session.screen())

        check("the panel closes", close_panel(session, "Help · Keys and commands"),
              session.screen())
        screen = session.screen()
        check("the streamed answer reached the conversation",
              ANSWER in screen, screen)
        check("the streamed answer reached the conversation exactly once",
              screen.count(ANSWER) == 1, screen)
    finally:
        session.close()


def scenario_tool_activity(harness):
    """A tool call whose activity arrives while a panel is open."""
    session = open_session(harness)
    try:
        check("the provider received the question", ask(session, harness),
              len(harness.requests()))
        check("the panel opens while the turn runs",
              open_panel(session, "/help", "Help · Keys and commands"),
              session.screen())

        # The provider proposes a tool call. The activity of the call
        # reaches the interface while the panel is open, and the answer
        # of the turn follows it.
        harness.release_first()
        check("the completion is processed while the panel is open",
              session.until(lambda: "Complete" in session.screen(), 25.0),
              session.screen())

        check("the panel closes", close_panel(session, "Help · Keys and commands"),
              session.screen())
        screen = session.screen()
        check("the answer reached the conversation", ANSWER in screen, screen)
        check("the answer reached the conversation exactly once",
              screen.count(ANSWER) == 1, screen)
        check("the tool activity reached the conversation",
              "read" in screen and "refused" in screen, screen)
    finally:
        session.close()


def queue_supported(session):
    """Whether this binary knows the queue panel."""
    session.send(b"/queue\r")
    session.wait(1.2)
    if "Queued messages" in session.screen():
        session.send(b"\x1b")
        session.wait(0.6)
        return True
    return False


def scenario_queue_panel(harness):
    """The queue panel of the steering branch, as a control."""
    session = open_session(harness)
    try:
        if not queue_supported(session):
            skip("the queue panel keeps the answer", "this binary has no /queue panel")
            return
        check("the provider received the question", ask(session, harness),
              len(harness.requests()))
        check("the queue panel opens while the turn runs",
              open_panel(session, "/queue", "Queued messages"), session.screen())

        harness.release_first()
        check("the completion is processed while the panel is open",
              session.until(lambda: "Complete" in session.screen(), 25.0),
              session.screen())

        check("the panel closes", close_panel(session, "Queued messages"),
              session.screen())
        screen = session.screen()
        check("the answer reached the conversation", ANSWER in screen, screen)
        check("the answer reached the conversation exactly once",
              screen.count(ANSWER) == 1, screen)
    finally:
        session.close()


def scenario_queued_message(harness):
    """A message written while HAL is working is sent when the turn ends."""
    session = open_session(harness)
    try:
        if not queue_supported(session):
            skip("a queued message is sent when the turn ends",
                 "this binary has no /queue panel")
            return
        check("the provider received the question", ask(session, harness),
              len(harness.requests()))

        # The operator writes the next message while HAL is working.
        session.send(b"the queued message\r")
        session.wait(1.0)
        check("the message was not sent yet",
              len(harness.requests()) == 1, len(harness.requests()))

        # The panel is open when the turn ends.
        check("the queue panel opens while the turn runs",
              open_panel(session, "/queue", "Queued messages"), session.screen())
        check("the queued message is listed",
              "the queued message" in session.screen(), session.screen())

        harness.release_first()
        check("the completion is processed while the panel is open",
              session.until(lambda: "Complete" in session.screen(), 25.0),
              session.screen())
        check("the queued message was sent when the turn ended",
              session.until(lambda: len(harness.requests()) == 2, 25.0),
              len(harness.requests()))
        check("the panel closes", close_panel(session, "Queued messages"),
              session.screen())
        check("the answer of the queued message arrived",
              session.until(lambda: ANSWER2 in session.screen(), 25.0),
              session.screen())
        check("the answer of the queued message arrived exactly once",
              session.screen().count(ANSWER2) == 1, session.screen())

        recorded = harness.requests()
        if len(recorded) >= 2:
            carried = [m.get("content", "") for m in user_messages(recorded[1])]
            check("the provider received the queued message",
                  "the queued message" in carried, carried)
        answers = harness.answers()
        check("the conversation holds both answers exactly once",
              answers.count(ANSWER) == 1 and answers.count(ANSWER2) == 1, answers)
    finally:
        session.close()


def main():
    if not os.path.isfile(BINARY):
        print("missing HAL9001 binary:", BINARY)
        return 2
    base = os.environ.get("HAL9001_PTY_TMPDIR", "/tmp/opencode")
    os.makedirs(base, exist_ok=True)
    root = tempfile.mkdtemp(prefix="hal-panel-", dir=base)
    port = PORT_BASE
    try:
        for index, (command, title) in enumerate(PANELS):
            print("== %s ==" % command)
            harness = Harness(root, "panel-%d" % index, port,
                              session="panel%d" % index)
            port += 1
            harness.start()
            try:
                scenario_panel(harness, command, title)
            finally:
                harness.close()

        print("== the provider fails while a panel is open ==")
        failure = Harness(root, "failure", port, mode="release-fail",
                          session="panelfail")
        port += 1
        failure.start()
        try:
            scenario_provider_failure(failure)
        finally:
            failure.close()

        print("== a turn is stopped while a panel is open ==")
        stop = Harness(root, "stop", port, session="panelstop")
        port += 1
        stop.start()
        try:
            scenario_stop_with_panel(stop)
        finally:
            stop.close()

        print("== an answer arrives in pieces while a panel is open ==")
        stream = Harness(root, "stream", port, mode="release-stream",
                         session="panelstream")
        port += 1
        stream.start()
        try:
            scenario_streaming(stream)
        finally:
            stream.close()

        print("== a tool call arrives while a panel is open ==")
        tool = Harness(root, "tool", port, mode="release-tool",
                       session="paneltool")
        port += 1
        tool.start()
        try:
            scenario_tool_activity(tool)
        finally:
            tool.close()

        print("== the queue panel keeps the answer ==")
        queue = Harness(root, "queue", port, session="panelqueue")
        port += 1
        queue.start()
        try:
            scenario_queue_panel(queue)
        finally:
            queue.close()

        print("== a queued message is sent when the turn ends ==")
        queued = Harness(root, "queued", port, session="panelqueued")
        port += 1
        queued.start()
        try:
            scenario_queued_message(queued)
        finally:
            queued.close()
    finally:
        shutil.rmtree(root, ignore_errors=True)

    if SKIPPED:
        print("%d checks skipped" % len(SKIPPED))
    if FAILURES:
        print("%d PANEL COMPLETION PTY CHECKS FAILED" % len(FAILURES))
        return 1
    print("ALL PANEL COMPLETION PTY CHECKS PASSED")
    return 0


if __name__ == "__main__":
    sys.exit(main())
