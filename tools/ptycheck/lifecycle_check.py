#!/usr/bin/env python3
"""Regression checks for the PTY harness's child process lifecycle.

Run with:

    python3 tools/ptycheck/lifecycle_check.py -v

The checks use a temporary fake HAL binary and temporary home/store
directories. They never touch the operator's sessions or configuration.

Every case records the direct children of this test runner (pid, state)
and its open file descriptors before and after the harness call. That is
ownership evidence for the child this harness spawned, not a global
zombie count.
"""
import json
import os
import shutil
import signal
import stat
import subprocess
import sys
import tempfile
import time
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, HERE)
import pty_drive  # noqa: E402


def direct_children(pid=None):
    """The direct children of pid, as (pid, state) pairs.

    state is the first field of /proc/<pid>/stat after the command name:
    Z is a zombie, S/R/D a live child. Only this runner's children are
    inspected.
    """
    pid = os.getpid() if pid is None else pid
    found = []
    try:
        entries = os.listdir("/proc")
    except OSError:
        return found
    for entry in entries:
        if not entry.isdigit():
            continue
        try:
            with open("/proc/%s/stat" % entry, "rb") as fh:
                line = fh.read().decode("utf-8", "replace")
        except OSError:
            continue
        try:
            # The comm field can contain spaces and parentheses.
            rest = line.rsplit(")", 1)[1].split()
            state, ppid = rest[0], int(rest[1])
        except (IndexError, ValueError):
            continue
        if ppid == pid:
            found.append((int(entry), state))
    return sorted(found)


def open_fds():
    """The numeric descriptors this runner currently holds open."""
    try:
        return sorted(int(name) for name in os.listdir("/proc/self/fd")
                      if name.isdigit())
    except OSError:
        return []


def owned_state():
    return {"children": direct_children(), "fds": open_fds()}


FAKE_HAL = r'''#!/usr/bin/env python3
import os
import signal
import time

mode = os.environ.get("FAKE_HAL_MODE", "exit")
marker = os.environ.get("FAKE_HAL_MARKER")
if mode == "ignore-term":
    signal.signal(signal.SIGTERM, signal.SIG_IGN)
if mode == "term":
    def on_term(_signum, _frame):
        if marker:
            with open(marker, "w") as fh:
                fh.write("SIGTERM")
        os._exit(0)
    signal.signal(signal.SIGTERM, on_term)
if mode in ("hang", "ignore-term", "term"):
    while True:
        time.sleep(0.05)
time.sleep(0.01)
'''

WORKER = r'''#!/usr/bin/env python3
import json
import os
import sys
import time

sys.path.insert(0, os.environ["PTYCHECK_DIR"])
import pty_drive


def children(pid=None):
    pid = os.getpid() if pid is None else pid
    found = []
    try:
        entries = os.listdir("/proc")
    except OSError:
        return found
    for entry in entries:
        if not entry.isdigit():
            continue
        try:
            with open("/proc/%s/stat" % entry, "rb") as fh:
                line = fh.read().decode("utf-8", "replace")
        except OSError:
            continue
        try:
            rest = line.rsplit(")", 1)[1].split()
            if int(rest[1]) == pid:
                found.append((int(entry), rest[0]))
        except (IndexError, ValueError):
            continue
    return sorted(found)


pty_drive.BINARY = sys.argv[1]
pty_drive.HOME = sys.argv[2]
pty_drive.STORE = os.path.join(sys.argv[2], "sessions")
os.makedirs(pty_drive.STORE, exist_ok=True)
pty_drive.CONFIG = os.path.join(sys.argv[2], "config.json")
pty_drive.SESSION = "concurrent"
before = children()
start = time.monotonic()
pty_drive.run([(0.0, b"")], timeout=float(sys.argv[3]), fresh=False,
              session="concurrent")
elapsed = time.monotonic() - start
print(json.dumps({"before": before, "after": children(),
                  "elapsed": elapsed}))
'''


class _BrokenScript:
    """A script whose first access fails like a test assertion."""

    def __getitem__(self, index):
        raise AssertionError("simulated assertion failure")


class LifecycleTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.tmp = tempfile.TemporaryDirectory(prefix="hal-pty-life-")
        cls.binary = os.path.join(cls.tmp.name, "fake-hal9001")
        with open(cls.binary, "w") as fh:
            fh.write(FAKE_HAL)
        os.chmod(cls.binary, os.stat(cls.binary).st_mode | stat.S_IXUSR)
        cls.worker = os.path.join(cls.tmp.name, "worker.py")
        with open(cls.worker, "w") as fh:
            fh.write(WORKER)
        cls.saved = {
            "BINARY": pty_drive.BINARY,
            "CONFIG": pty_drive.CONFIG,
            "HOME": pty_drive.HOME,
            "STORE": pty_drive.STORE,
            "SESSION": pty_drive.SESSION,
            "TERMINATE_GRACE": pty_drive.TERMINATE_GRACE,
        }
        pty_drive.BINARY = cls.binary
        pty_drive.CONFIG = os.path.join(cls.tmp.name, "config.json")
        pty_drive.HOME = os.path.join(cls.tmp.name, "home")
        pty_drive.STORE = os.path.join(pty_drive.HOME, "sessions")
        os.makedirs(pty_drive.STORE, exist_ok=True)
        pty_drive.SESSION = "lifecycle"

    @classmethod
    def tearDownClass(cls):
        for key, value in cls.saved.items():
            setattr(pty_drive, key, value)
        cls.tmp.cleanup()

    def setUp(self):
        self.baseline = direct_children()
        self.addCleanup(self._clean_leftovers)

    def _clean_leftovers(self):
        """Reap anything a failed assertion left; the failure is kept."""
        known = {pid for pid, _state in self.baseline}
        for pid, _state in direct_children():
            if pid in known:
                continue
            try:
                os.kill(pid, signal.SIGKILL)
            except ProcessLookupError:
                pass
            try:
                os.waitpid(pid, 0)
            except ChildProcessError:
                pass

    def _set_mode(self, mode):
        previous = os.environ.get("FAKE_HAL_MODE")
        os.environ["FAKE_HAL_MODE"] = mode

        def restore():
            if previous is None:
                os.environ.pop("FAKE_HAL_MODE", None)
            else:
                os.environ["FAKE_HAL_MODE"] = previous

        self.addCleanup(restore)

    def _assert_clean(self, label, before):
        after = owned_state()
        print("  %s: before=%r after=%r" % (label, before, after))
        self.assertEqual(after["children"], before["children"],
                         "%s: owned child processes changed" % label)
        self.assertEqual(after["fds"], before["fds"],
                         "%s: the pty descriptor was not closed" % label)
        return after

    def test_normal_completion(self):
        """A child that exits on its own is waited for."""
        self._set_mode("exit")
        before = owned_state()
        pty_drive.run([(0.1, b"")], timeout=2.0, fresh=False,
                      session="life")
        self._assert_clean("normal completion", before)

    def test_assertion_failure(self):
        """An assertion raised after the fork still cleans up."""
        self._set_mode("hang")
        before = owned_state()
        with self.assertRaises(AssertionError):
            pty_drive.run(_BrokenScript(), timeout=2.0, fresh=False,
                          session="life")
        self._assert_clean("assertion failure", before)

    def test_exception(self):
        """An action that fails during the loop still cleans up."""
        self._set_mode("hang")
        before = owned_state()
        with self.assertRaises(TypeError):
            pty_drive.run([(0.0, object())], timeout=2.0, fresh=False,
                          session="life")
        self._assert_clean("exception", before)

    def test_timeout(self):
        """A child still running at the script timeout is terminated."""
        marker = os.path.join(self.tmp.name, "term-marker-%d" % os.getpid())
        try:
            os.remove(marker)
        except FileNotFoundError:
            pass
        previous = os.environ.get("FAKE_HAL_MARKER")
        os.environ["FAKE_HAL_MARKER"] = marker

        def restore():
            if previous is None:
                os.environ.pop("FAKE_HAL_MARKER", None)
            else:
                os.environ["FAKE_HAL_MARKER"] = previous

        self.addCleanup(restore)
        self._set_mode("term")
        before = owned_state()
        start = time.monotonic()
        pty_drive.run([(0.0, b"")], timeout=0.3, fresh=False,
                      session="life")
        elapsed = time.monotonic() - start
        self.assertGreaterEqual(elapsed, 0.3)
        self._assert_clean("timeout", before)
        with open(marker) as fh:
            self.assertEqual(fh.read(), "SIGTERM",
                             "the child left on timeout was not sent SIGTERM")

    def test_forced_termination(self):
        """SIGKILL is used only after the SIGTERM grace period, then reaped."""
        self._set_mode("ignore-term")
        previous = pty_drive.TERMINATE_GRACE
        pty_drive.TERMINATE_GRACE = 0.2
        self.addCleanup(setattr, pty_drive, "TERMINATE_GRACE", previous)
        before = owned_state()
        start = time.monotonic()
        pty_drive.run([(0.0, b"")], timeout=0.25, fresh=False,
                      session="life")
        elapsed = time.monotonic() - start
        self.assertGreaterEqual(elapsed, 0.25 + 0.1,
                                "SIGKILL arrived before the grace period")
        self._assert_clean("forced termination", before)

    def test_interrupted(self):
        """KeyboardInterrupt during the loop still terminates and reaps."""
        self._set_mode("hang")
        before = owned_state()

        def interrupt(_signum, _frame):
            os.kill(os.getpid(), signal.SIGINT)

        previous = signal.signal(signal.SIGALRM, interrupt)
        signal.setitimer(signal.ITIMER_REAL, 0.3)
        try:
            with self.assertRaises(KeyboardInterrupt):
                pty_drive.run([(0.0, b"")], timeout=5.0, fresh=False,
                              session="life")
        finally:
            signal.setitimer(signal.ITIMER_REAL, 0)
            signal.signal(signal.SIGALRM, previous)
        self._assert_clean("interrupted", before)

    def test_unrelated_session_survives(self):
        """A same-binary session outside the harness is never signalled."""
        self._set_mode("hang")
        env = dict(os.environ)
        env["FAKE_HAL_MODE"] = "hang"
        decoy = subprocess.Popen([self.binary], env=env,
                                 stdout=subprocess.DEVNULL,
                                 stderr=subprocess.DEVNULL,
                                 start_new_session=True)
        self.addCleanup(self._stop, decoy)
        time.sleep(0.05)
        before = owned_state()
        self.assertIn(decoy.pid, [pid for pid, _state in before["children"]])
        pty_drive.run([(0.0, b"")], timeout=0.2, fresh=False,
                      session="life")
        self._assert_clean("unrelated session", before)
        self.assertIsNone(decoy.poll(),
                          "cleanup killed an unrelated HAL session")

    @staticmethod
    def _stop(proc):
        if proc.poll() is not None:
            return
        proc.terminate()
        try:
            proc.wait(timeout=2)
        except subprocess.TimeoutExpired:
            proc.kill()
            proc.wait()

    def test_two_concurrent_harnesses(self):
        """Two harness processes each own and reap only their own child."""
        parent_before = owned_state()
        env = dict(os.environ)
        env["FAKE_HAL_MODE"] = "hang"
        env["PTYCHECK_DIR"] = HERE
        workers = []
        homes = []
        try:
            for _ in range(2):
                home = tempfile.mkdtemp(prefix="hal-pty-worker-",
                                        dir=self.tmp.name)
                homes.append(home)
                proc = subprocess.Popen(
                    [sys.executable, self.worker, self.binary, home, "0.4"],
                    env=env, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                    text=True)
                workers.append(proc)
            results = []
            for proc in workers:
                stdout, stderr = proc.communicate(timeout=20)
                self.assertEqual(proc.returncode, 0,
                                 "worker failed:\n%s\n%s" % (stdout, stderr))
                results.append(json.loads(stdout.strip().splitlines()[-1]))
        finally:
            for proc in workers:
                if proc.poll() is None:
                    proc.kill()
                    proc.wait()
            for home in homes:
                shutil.rmtree(home, ignore_errors=True)

        for index, result in enumerate(results):
            print("  concurrent worker %d: before=%r after=%r" %
                  (index, result["before"], result["after"]))
            self.assertEqual(result["before"], [])
            self.assertEqual(result["after"], [],
                             "worker %d leaked a child" % index)
            self.assertGreaterEqual(result["elapsed"], 0.4)
        self._assert_clean("concurrent harnesses", parent_before)


if __name__ == "__main__":
    unittest.main(verbosity=2)
