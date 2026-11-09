#!/usr/bin/env python3
"""PTY checks for the lifecycle of an exchange: /stop and /resume.

The suite runs the real binary against a provider that holds a request
and records every request it receives. The operator stops the running
turn with /stop, continues the interrupted exchange with /resume, and
the checks look at what actually happened: the requests that left the
machine, the conversation that was stored, the side effect that was (or
was not) repeated, and the status the interface shows.

Everything happens inside one HAL process. The safe point that /resume
continues from lives in the running process, so a scenario spread over
two processes would resume nothing at all.

What is checked:

* /stop ends the running turn: the request is abandoned, the interface
  says so, and it is usable again at once;
* the conversation is not corrupt: the operator's message is stored
  exactly once, and the stopped turn stored no answer;
* /resume continues the interrupted exchange: the provider receives a
  second request that carries the operator's message exactly once, and
  the answer arrives;
* an operation that already ran inside the exchange is not run again:
  the side effect happens exactly once, the model is told the work is
  done, and the ledger of the exchange keeps one record of it;
* a finished exchange is never replayed: /resume says there is nothing
  to continue, and the provider is asked nothing;
* a failed exchange is continued, not abandoned: the operator's message
  stays unanswered, and /resume answers it;
* /stop with nothing running says so;
* the diagnostics name the state of the exchange.

Usage:

    go build -o /tmp/opencode/hal9001 ./cmd/hal9001
    HAL9001_BINARY=/tmp/opencode/hal9001 python3 tools/ptycheck/stop_resume_checks.py
"""
import fcntl
import json
import os
import pty
import re
import select
import shutil
import signal
import socket
import struct
import subprocess
import sys
import tempfile
import termios
import time

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, HERE)
from termsim import render  # noqa: E402

FAILURES = []
BINARY = os.environ.get("HAL9001_BINARY", "/tmp/opencode/hal9001")
PORT = int(os.environ.get("HAL9001_HOLDING_PORT", "8266"))
GUARD_PORT = int(os.environ.get("HAL9001_GUARD_PORT", "8267"))
FAIL_PORT = int(os.environ.get("HAL9001_FAILURE_PORT", "8268"))

# The question the operator asks. The provider holds the request that
# carries it, so the turn can be stopped while it is in flight.
QUESTION = "hold the line"
# The answer the provider gives once it is asked again.
ANSWER = "the continued answer"

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


class Session:
    """One HAL process, driven one step at a time.

    The lifecycle of an exchange cannot be checked across processes: the
    safe point that /resume continues from lives in the running
    process. This session therefore keeps one process alive and lets the
    checks send keys and read the screen between the steps.
    """

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


class Harness:
    """One isolated environment: config, home, store, workspace, provider."""

    def __init__(self, root, name, port, mode="plain", command=None,
                 session="stopresume"):
        self.root = os.path.join(root, name)
        self.home = os.path.join(self.root, "home")
        self.store = os.path.join(self.home, "sessions")
        self.workspace = os.path.join(self.root, "HAL")
        self.config = os.path.join(self.home, "config.json")
        self.log = os.path.join(self.root, "requests.jsonl")
        self.port = port
        self.mode = mode
        self.command = command
        # The session is named so that the word "lifecycle" on the
        # screen can only come from the diagnostics line and not from
        # the session name.
        self.session = session
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
                "models": [{"id": "test-model"}],
                "active_model": "test-model",
                "theme": "HAL Classic",
            }, fh)
        env = os.environ.copy()
        env["HAL9001_HOLDING_PORT"] = str(self.port)
        env["HAL9001_REQUEST_LOG"] = self.log
        env["HAL9001_HOLDING_MODE"] = self.mode
        if self.command:
            env["HAL9001_HOLDING_COMMAND"] = self.command
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

    def close(self):
        """Stop the provider and take the environment down."""
        if self.provider is not None:
            self.provider.terminate()
            try:
                self.provider.wait(timeout=5)
            except subprocess.TimeoutExpired:
                self.provider.kill()
            self.provider = None


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


def roles_of(messages):
    """The roles of the stored messages."""
    return [m.get("role") for m in messages]


def scenario_lifecycle(harness):
    """The lifecycle of one exchange: stop it, then continue it."""
    session = Session(BINARY, harness.config, harness.home, harness.store,
                      harness.session, harness.workspace)
    session.start()
    try:
        session.wait_for("Enter send", 20.0)

        # 1. The operator asks a question and stops the turn while the
        #    provider is still working on it.
        session.send(QUESTION.encode() + b"\r")
        session.until(lambda: len(harness.requests()) == 1, 15.0)
        mark = session.mark()
        session.send(b"/stop\r")
        session.wait_for("cancelled", 10.0, mark)
        delta = plain(session.since(mark))
        check("the turn was stopped", "stopping" in delta.lower() or "cancelled" in delta.lower(), delta)
        check("the stopped exchange can be continued", "/resume" in delta, delta)
        check("the provider received the request", len(harness.requests()) == 1,
              len(harness.requests()))

        # 2. The conversation is not corrupt: the operator's message is
        #    stored exactly once, and the stopped turn stored no answer.
        roles = roles_of(harness.conversation())
        check("the stopped turn stored no answer", "assistant" not in roles, roles)
        check("the operator's message is stored exactly once",
              roles.count("user") == 1, roles)

        # 3. The interface is usable again: the composer takes the
        #    operator's next word.
        session.send(b"next question")
        session.wait(0.6)
        check("the composer is usable again", "next question" in session.screen(),
              session.screen())
        session.send(b"\x15")  # clear the draft, the message is not sent
        session.wait(0.5)

        # 4. /resume continues the interrupted exchange. The provider
        #    answers the second request, and the operator's message is
        #    carried exactly once.
        mark = session.mark()
        session.send(b"/resume\r")
        session.wait_for(ANSWER, 15.0, mark)
        recorded = harness.requests()
        check("the exchange was continued", len(recorded) == 2, len(recorded))
        if len(recorded) == 2:
            carried = user_messages(recorded[1])
            check("the continued request carries the operator's message once",
                  len(carried) == 1, len(carried))
            check("the continued request carries the operator's message",
                  bool(carried) and QUESTION in carried[0].get("content", ""), carried)
        check("the continued answer is on screen", ANSWER in session.screen(),
              session.screen())

        # 5. The conversation is whole: the operator's message and the
        #    answer, each exactly once.
        roles = roles_of(harness.conversation())
        check("the conversation holds the question and the answer",
              roles.count("user") == 1 and roles.count("assistant") == 1, roles)

        # 6. A finished exchange is never replayed.
        mark = session.mark()
        session.send(b"/resume\r")
        session.wait_for("nothing to continue", 8.0, mark)
        delta = plain(session.since(mark))
        check("a finished exchange is not continued", "nothing to continue" in delta, delta)
        check("the provider was asked nothing more", len(harness.requests()) == 2,
              len(harness.requests()))

        # 7. /stop with nothing running says so.
        mark = session.mark()
        session.send(b"/stop\r")
        session.wait_for("nothing is running", 8.0, mark)
        delta = plain(session.since(mark))
        check("stopping nothing says so", "nothing is running" in delta, delta)

        # 8. The diagnostics name the state of the exchange. The word
        #    "lifecycle" can only come from the diagnostics line: the
        #    session is not named after it.
        session.send(b"/diag\r")
        session.wait(1.5)
        screen = session.screen()
        check("the diagnostics name the lifecycle", "lifecycle" in screen, screen)
        check("the diagnostics name the completed state", "completed" in screen, screen)
    finally:
        session.close()


def scenario_guard(harness, side_effect):
    """The side effect of an interrupted exchange is not repeated.

    The provider proposes the same command twice: once before the
    operator stops the turn, and once in the continuation. The command
    must run exactly once.
    """
    session = Session(BINARY, harness.config, harness.home, harness.store,
                      harness.session, harness.workspace)
    session.start()
    try:
        session.wait_for("Enter send", 20.0)

        # The privileged tools need 666. The chord is the operator's
        # own, and the conversation cannot reach it.
        session.send(b"\x10")
        session.wait(0.6)
        session.send(b"\x19")
        session.wait(0.8)
        check("666 is enabled for the guarded scenario",
              "666 UNRESTRICTED" in session.screen(), session.screen())

        # The operator asks; the provider proposes the command; it runs;
        # the next request is held.
        session.send(QUESTION.encode() + b"\r")
        session.until(lambda: len(lines_of(side_effect)) == 1, 20.0)
        check("the command ran once", len(lines_of(side_effect)) == 1,
              lines_of(side_effect))

        # The operator stops the turn while the provider is still
        # working on it.
        mark = session.mark()
        session.send(b"/stop\r")
        session.wait_for("cancelled", 10.0, mark)
        roles = roles_of(harness.conversation())
        check("the stopped exchange stored no answer", "assistant" not in roles, roles)

        # The continuation proposes the same command again. It must not
        # run a second time.
        mark = session.mark()
        session.send(b"/resume\r")
        session.until(lambda: len(harness.requests()) >= 4, 25.0)
        session.wait_for(ANSWER, 15.0, mark)
        check("the command was not run again", len(lines_of(side_effect)) == 1,
              lines_of(side_effect))
        screen = session.screen()
        check("the continuation says the work is done", "already done" in screen, screen)
        check("the continued answer is on screen", ANSWER in screen, screen)

        # The ledger of the exchange keeps one record of what really ran.
        ledger = []
        for message in harness.conversation():
            if message.get("role") == "assistant":
                ledger = (message.get("tool_ledger") or {}).get("executions") or []
        check("the ledger holds the operation exactly once", len(ledger) == 1, ledger)
    finally:
        session.close()


def scenario_failure(harness):
    """A failed exchange is continued, not abandoned.

    The provider fails the first request. The operator's message stays
    unanswered, and /resume answers it.
    """
    session = Session(BINARY, harness.config, harness.home, harness.store,
                      harness.session, harness.workspace)
    session.start()
    try:
        session.wait_for("Enter send", 20.0)

        session.send(QUESTION.encode() + b"\r")
        session.until(lambda: len(harness.requests()) == 1, 15.0)
        session.wait_for("error", 15.0)
        roles = roles_of(harness.conversation())
        check("the failed exchange stored the question and no answer",
              roles.count("user") == 1 and "assistant" not in roles, roles)

        # The exchange can be continued: the provider is asked again and
        # the answer arrives.
        mark = session.mark()
        session.send(b"/resume\r")
        session.wait_for(ANSWER, 20.0, mark)
        check("the failed exchange was continued", len(harness.requests()) == 2,
              len(harness.requests()))
        check("the answer to the failed exchange is on screen",
              ANSWER in session.screen(), session.screen())
        roles = roles_of(harness.conversation())
        check("the continued exchange holds the question and the answer",
              roles.count("user") == 1 and roles.count("assistant") == 1, roles)
    finally:
        session.close()


def lines_of(path):
    """The lines of a file, or none at all when it does not exist."""
    if not os.path.exists(path):
        return []
    with open(path, "r", errors="replace") as fh:
        return [line for line in fh.read().splitlines() if line.strip()]


def main():
    if not os.path.isfile(BINARY):
        print("missing HAL9001 binary:", BINARY)
        return 2
    base = os.environ.get("HAL9001_PTY_TMPDIR", "/tmp/opencode")
    os.makedirs(base, exist_ok=True)
    root = tempfile.mkdtemp(prefix="hal-lifecycle-", dir=base)
    try:
        print("== the lifecycle of one exchange ==")
        plain_harness = Harness(root, "lifecycle", PORT)
        plain_harness.start()
        try:
            scenario_lifecycle(plain_harness)
        finally:
            plain_harness.close()

        print("== a side effect is not repeated ==")
        side_effect = os.path.join(root, "side-effect.txt")
        guard_harness = Harness(root, "guard", GUARD_PORT, mode="guard",
                                command="echo guard >> %s" % side_effect)
        guard_harness.start()
        try:
            scenario_guard(guard_harness, side_effect)
        finally:
            guard_harness.close()

        print("== a failed exchange is continued ==")
        fail_harness = Harness(root, "failure", FAIL_PORT, mode="fail",
                                session="failedresume")
        fail_harness.start()
        try:
            scenario_failure(fail_harness)
        finally:
            fail_harness.close()
    finally:
        shutil.rmtree(root, ignore_errors=True)

    if FAILURES:
        print("%d LIFECYCLE PTY CHECKS FAILED" % len(FAILURES))
        return 1
    print("ALL LIFECYCLE PTY CHECKS PASSED")
    return 0


if __name__ == "__main__":
    sys.exit(main())
