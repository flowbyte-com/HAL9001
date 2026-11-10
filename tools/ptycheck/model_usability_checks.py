#!/usr/bin/env python3
"""PTY checks for the interactive management of endpoints and models.

The commands of the /model and /endpoint families work with all their
arguments. These checks are about the other half: the operator asks for
an action without spelling it out, and the interface asks for what the
action needs.

The suite drives the real binary inside one pseudo-terminal, against an
isolated configuration, home, session store and workspace. Nothing
outside the temporary directory is read or written.

What is proved:

  * /model opens the management interface, and every action it offers is
    also a command;
  * /model edit without arguments asks which model to edit and then for
    its settings, and saving stores only the setting that changed;
  * a value the configuration refuses keeps the form open and changes
    nothing;
  * a cancelled form changes nothing;
  * the change is on disk, it is there after a restart, and the
    management screens show it;
  * /model remove asks before it removes, and the active model is
    never removed without a replacement;
  * /model add asks for the fields of the model;
  * /model context shows the context window and where it came from;
  * /model discover asks the provider only when the operator names the
    endpoint;
  * /endpoint and its actions follow the same conventions;
  * an unknown action opens the interface and says what is wrong;
  * the interface stays usable in a small terminal;
  * none of the screens changes the active model.

Usage:

    go build -o /tmp/opencode/hal9001 ./cmd/hal9001
    HAL9001_BINARY=/tmp/opencode/hal9001 python3 tools/ptycheck/model_usability_checks.py
"""
import json
import os
import shutil
import subprocess
import sys
import tempfile
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, HERE)
from pty_session import Session, plain  # noqa: E402

FAILURES = []
BINARY = os.environ.get("HAL9001_BINARY", "/tmp/opencode/hal9001")
PORT = int(os.environ.get("HAL9001_MODEL_PORT", "8280"))


class CatalogueProvider:
    """A local provider that serves a catalogue and nothing else.

    The management screens never talk to a provider on their own, so the
    only thing this one has to answer is the catalogue the discovery
    asks for.
    """

    def __init__(self, port):
        self.port = port
        self.server = None

    def models(self):
        return ["flash", "catalogue-model"]

    def start(self):
        provider = self

        class Handler(BaseHTTPRequestHandler):
            def log_message(self, *args):
                pass

            def do_GET(self):
                if self.path.rstrip("/").endswith("/models"):
                    body = json.dumps({"data": [{"id": id} for id in provider.models()]}).encode()
                    self.send_response(200)
                    self.send_header("Content-Type", "application/json")
                    self.send_header("Content-Length", str(len(body)))
                    self.end_headers()
                    self.wfile.write(body)
                    return
                self.send_response(404)
                self.end_headers()

        self.server = ThreadingHTTPServer(("127.0.0.1", self.port), Handler)
        self.server.daemon_threads = True
        import threading
        thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        thread.start()

    def stop(self):
        if self.server is not None:
            self.server.shutdown()
            self.server.server_close()
            self.server = None

# The configuration the operator starts from: two endpoints, three
# models, and a model that was confirmed by the provider's catalogue.
CONFIG = {
    "version": 2,
    "endpoints": [
        {
            "id": "primary",
            "name": "Primary",
            "protocol": "openai-compatible",
            "base_url": "http://127.0.0.1:%d/v1" % PORT,
            "credential_env": "",
            "credential": "test-key",
            "discovery": {"enabled": True},
            "models": [
                {"id": "flash", "label": "Flash", "effort": "medium",
                 "context_mode": "auto", "source": "manual"},
                {"id": "deepseek", "label": "", "effort": "",
                 "context_mode": "manual", "context_window": 64000,
                 "max_output_tokens": 4096, "source": "discovered",
                 "fetched_at": "2026-11-09T10:00:00Z"},
            ],
        },
        {
            "id": "secondary",
            "name": "Secondary",
            "protocol": "openai-compatible",
            "base_url": "http://127.0.0.1:%d/v1" % PORT,
            "credential_env": "SECONDARY_KEY",
            "discovery": {"enabled": False},
            "models": [
                {"id": "other", "label": "", "effort": "",
                 "context_mode": "auto", "source": "manual"},
            ],
        },
    ],
    "active": {"endpoint": "primary", "model": "flash"},
    "theme": "HAL Classic",
}


def check(name, ok, detail=""):
    print(("[ok  ] " if ok else "[FAIL] ") + name + ("" if ok else "  " + str(detail)))
    if not ok:
        FAILURES.append(name)


def stored(config):
    """The configuration as it is on disk."""
    with open(config) as fh:
        return json.load(fh)


def models_of(config, endpoint="primary"):
    """The models of one endpoint, as they are on disk."""
    for record in stored(config).get("endpoints") or []:
        if record.get("id") == endpoint:
            return record.get("models") or []
    return []


def model_of(config, model_id, endpoint="primary"):
    """One model of one endpoint, as it is on disk."""
    for record in models_of(config, endpoint):
        if record.get("id") == model_id:
            return record
    return {}


def start(root, name, cols=120, rows=45, config=None):
    """Start the interface with an isolated environment.

    A configuration that is named is reused, so a second process reads
    exactly what the first one wrote.
    """
    home = os.path.join(root, name, "home")
    store = os.path.join(home, "sessions")
    workspace = os.path.join(root, name, "HAL")
    os.makedirs(store, exist_ok=True)
    os.makedirs(workspace, exist_ok=True)
    if config is None:
        config = os.path.join(home, "config.json")
        with open(config, "w") as fh:
            json.dump(CONFIG, fh)
    else:
        home = os.path.dirname(config)
        store = os.path.join(home, "sessions")
    os.environ["SECONDARY_KEY"] = "test-key"
    session = Session(BINARY, config, home, store, "models", workspace,
                      cols=cols, rows=rows)
    session.start()
    session.wait_for("Enter send", 25.0)
    return session, config


def scenario_interface(harness):
    """The management interface opens, and it names every action."""
    session, config = harness
    try:
        session.send(b"/model\r")
        session.wait(1.2)
        screen = session.screen()
        check("/model opens the management interface", "Every action is also a command" in screen,
              screen)
        for action in ("Use", "Add", "Edit", "Remove", "Context", "Show",
                       "Discover", "List"):
            check("the interface offers %s" % action, action in screen, screen)
        for command in ("/model use", "/model add", "/model edit",
                        "/model remove", "/model context", "/model show",
                        "/model discover", "/model list"):
            check("the interface names %s" % command, command in screen, screen)
        check("the interface names the settings",
              "label, effort" in screen, screen)
        session.send(b"\x1b")
        session.wait(0.8)
        check("Esc leaves the interface", "Every action is also a command" not in session.screen(),
              session.screen())

        # The endpoint family uses the same conventions.
        session.send(b"/endpoint\r")
        session.wait(1.2)
        screen = session.screen()
        check("/endpoint opens the management interface",
              "Every action is also a command" in screen, screen)
        for command in ("/endpoint list", "/endpoint add", "/endpoint edit",
                        "/endpoint remove"):
            check("the endpoint interface names %s" % command, command in screen, screen)
        session.send(b"\x1b")
        session.wait(0.8)

        # An unknown action says what is wrong and lists the actions.
        session.send(b"/model frobnicate\r")
        session.wait(1.2)
        screen = session.screen()
        check("an unknown action is named", "frobnicate" in screen, screen)
        check("an unknown action still lists the actions",
              "Every action is also a command" in screen, screen)

        # A half-spelled command says what is missing.
        session.send(b"\x1b")
        session.wait(0.6)
        session.send(b"/model edit primary\r")
        session.wait(1.2)
        screen = session.screen()
        check("a half-spelled command says what is missing",
              "model id" in screen, screen)
        check("a half-spelled command names the command",
              "/model edit" in screen, screen)
    finally:
        session.close()


def scenario_edit(harness):
    """The edit screen asks for the model and then for its settings."""
    session, config = harness
    try:
        session.send(b"/model edit\r")
        session.wait(1.2)
        screen = session.screen()
        check("the edit screen asks which model", "Choose a model" in screen, screen)
        check("the edit screen names the models", "deepseek" in screen, screen)

        # The second model is chosen and its settings are shown.
        session.send(b"\x1b[B")
        session.wait(0.4)
        session.send(b"\r")
        session.wait(1.2)
        screen = session.screen()
        check("the form names the model", "Edit model primary · deepseek" in screen, screen)
        check("the form shows the current output limit", "4096" in screen, screen)
        check("the form says that an empty field keeps the value",
              "empty field keeps the current value" in screen, screen)

        # A label is typed and the form is saved.
        session.send(b"DeepSeek EU")
        session.wait(0.6)
        session.send(b"\x13")
        session.wait(1.5)
        screen = session.screen()
        check("the edit is reported", "updated" in screen, screen)
        record = model_of(config, "deepseek")
        check("the label was stored", record.get("label") == "DeepSeek EU",
              json.dumps(record))
        check("the settings that were not touched stayed",
              not record.get("effort") and record.get("max_output_tokens") == 4096 and
              record.get("context_window") == 64000, json.dumps(record))
        check("the active model did not change",
              (stored(config).get("active") or {}).get("model") == "flash",
              json.dumps(stored(config).get("active")))
    finally:
        session.close()


def scenario_refused_and_cancelled(harness):
    """A refused value and a cancelled form change nothing."""
    session, config = harness
    try:
        before = model_of(config, "deepseek")

        # A value the configuration refuses.
        session.send(b"/model edit\r")
        session.wait(1.2)
        session.send(b"\x1b[B")
        session.wait(0.4)
        session.send(b"\r")
        session.wait(1.2)
        session.send(b"\t")          # the effort field
        session.wait(0.4)
        session.send(b"turbo")
        session.wait(0.6)
        session.send(b"\x13")
        session.wait(1.5)
        screen = session.screen()
        check("a refused value is reported", "effort level" in screen, screen)
        check("the form stays open after a refusal",
              "Ctrl+S saves" in screen, screen)
        check("the refused change was not stored",
              model_of(config, "deepseek").get("effort") == before.get("effort"),
              json.dumps(model_of(config, "deepseek")))

        # A cancelled form.
        session.send(b"\x1b")
        session.wait(1.0)
        session.send(b"\x1b")
        session.wait(1.0)
        session.send(b"/model edit\r")
        session.wait(1.2)
        session.send(b"\x1b[B")
        session.wait(0.4)
        session.send(b"\r")
        session.wait(1.2)
        session.send(b"never saved")
        session.wait(0.6)
        session.send(b"\x1b")
        session.wait(1.2)
        check("a cancelled form stores nothing",
              model_of(config, "deepseek").get("label") == before.get("label"),
              json.dumps(model_of(config, "deepseek")))
        check("the active model did not change",
              (stored(config).get("active") or {}).get("model") == "flash",
              json.dumps(stored(config).get("active")))

        # A limit of zero is the provider's own default, and the
        # configuration keeps a zero unnamed: the screen says so and
        # stores nothing.
        session.send(b"/model edit\r")
        session.wait(1.2)
        session.send(b"\x1b[B")
        session.wait(0.4)
        session.send(b"\r")
        session.wait(1.2)
        session.send(b"\t\t")
        session.wait(0.4)
        session.send(b"0")
        session.wait(0.6)
        session.send(b"\x13")
        session.wait(1.5)
        screen = session.screen()
        check("a limit of zero is refused", "provider's own default" in screen, screen)
        check("the refused limit kept the form open",
              "Ctrl+S saves" in screen, screen)
        check("the refused limit was not stored",
              model_of(config, "deepseek").get("max_output_tokens") == 4096,
              json.dumps(model_of(config, "deepseek")))
    finally:
        session.close()


def scenario_reopen_and_restart(harness, root):
    """The change is visible again, and it survives a restart."""
    session, config = harness
    try:
        session.send(b"/model edit\r")
        session.wait(1.2)
        session.send(b"\x1b[B")
        session.wait(0.4)
        session.send(b"\r")
        session.wait(1.2)
        session.send(b"Kept")
        session.wait(0.6)
        session.send(b"\x13")
        session.wait(1.5)

        # The screen of the model shows the new label.
        session.send(b"/model show\r")
        session.wait(1.2)
        session.send(b"\x1b[B")
        session.wait(0.4)
        session.send(b"\r")
        session.wait(1.2)
        screen = session.screen()
        check("the details show the new label", "Kept" in screen, screen)
        session.send(b"\x1b")
        session.wait(0.8)
    finally:
        session.close()

    # A second process reads the same configuration.
    session, _ = start(root, "restart", cols=120, rows=45, config=config)
    try:
        session.send(b"/model show\r")
        session.wait(1.2)
        session.send(b"\x1b[B")
        session.wait(0.4)
        session.send(b"\r")
        session.wait(1.5)
        screen = session.screen()
        check("the change survived the restart", "Kept" in screen, screen)
    finally:
        session.close()


def scenario_remove(harness):
    """The removal asks before it removes."""
    session, config = harness
    try:
        session.send(b"/model remove\r")
        session.wait(1.2)
        check("the removal asks which model", "Choose a model" in session.screen(),
              session.screen())
        session.send(b"\x1b[B")
        session.wait(0.4)
        session.send(b"\r")
        session.wait(1.2)
        screen = session.screen()
        check("the removal asks for a confirmation", "Enter confirms" in screen, screen)
        check("the confirmation says what happens",
              "active model" in screen, screen)

        # Esc keeps the model.
        session.send(b"\x1b")
        session.wait(1.2)
        check("a cancelled removal keeps the model",
              any(m.get("id") == "deepseek" for m in models_of(config)),
              json.dumps(models_of(config)))

        # The active model is never removed without a replacement.
        session.send(b"/model remove\r")
        session.wait(1.2)
        session.send(b"\r")
        session.wait(1.2)
        session.send(b"\r")
        session.wait(1.2)
        screen = session.screen()
        check("the removal asks for the replacement",
              "use instead" in screen, screen)
        session.send(b"\r")
        session.wait(1.5)
        check("the active model was removed with a replacement",
              not any(m.get("id") == "flash" for m in models_of(config)),
              json.dumps(models_of(config)))
        check("the selection moved to the replacement",
              (stored(config).get("active") or {}).get("model") == "deepseek" or
              (stored(config).get("active") or {}).get("model") == "other",
              json.dumps(stored(config).get("active")))
    finally:
        session.close()


def scenario_add(harness):
    """The add screen asks for the fields of the model."""
    session, config = harness
    try:
        session.send(b"/model add\r")
        session.wait(1.2)
        screen = session.screen()
        check("the add screen asks for the endpoint", "Endpoint" in screen, screen)
        check("the add screen asks for the model id", "Model id" in screen, screen)
        check("the add screen asks for the label", "Label" in screen, screen)

        session.send(b"\t")
        session.wait(0.4)
        session.send(b"new-model")
        session.wait(0.6)
        session.send(b"\x13")
        session.wait(1.5)
        screen = session.screen()
        check("the model was added", "Added model" in screen, screen)
        check("the model is on disk",
              any(m.get("id") == "new-model" for m in models_of(config)),
              json.dumps(models_of(config)))
        check("the active model did not change",
              (stored(config).get("active") or {}).get("model") == "flash",
              json.dumps(stored(config).get("active")))
    finally:
        session.close()


def scenario_context(harness):
    """The context screen shows the window and where it came from."""
    session, config = harness
    try:
        session.send(b"/model context\r")
        session.wait(1.2)
        session.send(b"\x1b[B")
        session.wait(0.4)
        session.send(b"\r")
        session.wait(1.2)
        screen = session.screen()
        check("the context screen names the model",
              "Context of primary · deepseek" in screen, screen)
        check("the context screen shows the mode", "manual" in screen, screen)
        check("the context screen shows the window", "64000" in screen, screen)
        check("the context screen shows where the numbers came from",
              "source" in screen and "discovered" in screen, screen)

        # The mode is changed to automatic.
        session.send(b"auto")
        session.wait(0.6)
        session.send(b"\x13")
        session.wait(1.5)
        record = model_of(config, "deepseek")
        check("the mode was stored", record.get("context_mode") == "auto",
              json.dumps(record))
        check("the window was cleared with the mode",
              record.get("context_window") in (0, None), json.dumps(record))

        # A window belongs to the manual mode: the two are set together.
        session.send(b"/model context\r")
        session.wait(1.2)
        session.send(b"\x1b[B")
        session.wait(0.4)
        session.send(b"\r")
        session.wait(1.2)
        session.send(b"\t")
        session.wait(0.4)
        session.send(b"120000")
        session.wait(0.6)
        session.send(b"\x13")
        session.wait(1.5)
        record = model_of(config, "deepseek")
        check("a typed window makes the mode manual",
              record.get("context_mode") == "manual", json.dumps(record))
        check("the typed window was stored",
              record.get("context_window") == 120000, json.dumps(record))
    finally:
        session.close()


def scenario_endpoints(harness):
    """The endpoint screens ask for what they need."""
    session, config = harness
    try:
        session.send(b"/endpoint add\r")
        session.wait(1.2)
        screen = session.screen()
        check("the endpoint form asks for an id", "Id" in screen, screen)
        check("the endpoint form asks for a url", "Base url" in screen, screen)
        check("the endpoint form never asks for the key itself",
              "API key" not in screen and "credential" not in screen.lower().replace(
                  "credential env", ""), screen)

        session.send(b"third")
        session.wait(0.4)
        session.send(b"\t")
        session.wait(0.4)
        session.send(b"Third")
        session.wait(0.4)
        session.send(b"\t")
        session.wait(0.4)
        session.send(b"\t")
        session.wait(0.4)
        session.send(b"https://api.third.example.com/v1")
        session.wait(0.6)
        session.send(b"\x13")
        session.wait(1.5)
        screen = session.screen()
        check("the endpoint was added", "Added endpoint" in screen, screen)
        endpoints = {e.get("id"): e for e in stored(config).get("endpoints") or []}
        check("the endpoint is on disk", "third" in endpoints, json.dumps(list(endpoints)))
        if "third" in endpoints:
            check("the endpoint kept its url",
                  endpoints["third"].get("base_url") == "https://api.third.example.com/v1",
                  json.dumps(endpoints["third"]))
            check("the endpoint has no literal credential",
                  not endpoints["third"].get("credential"), json.dumps(endpoints["third"]))

        # An edit changes only what it names.
        session.send(b"/endpoint edit\r")
        session.wait(1.2)
        session.send(b"\r")
        session.wait(1.2)
        screen = session.screen()
        check("the endpoint form names the endpoint", "Edit endpoint primary" in screen, screen)
        session.send(b"Renamed")
        session.wait(0.6)
        session.send(b"\x13")
        session.wait(1.5)
        endpoints = {e.get("id"): e for e in stored(config).get("endpoints") or []}
        check("the endpoint was renamed", endpoints["primary"].get("name") == "Renamed",
              json.dumps(endpoints["primary"]))
        check("the endpoint kept its url",
              endpoints["primary"].get("base_url") == "http://127.0.0.1:%d/v1" % PORT,
              json.dumps(endpoints["primary"]))
        check("the endpoint kept its discovery flag",
              endpoints["primary"].get("discovery", {}).get("enabled") is True,
              json.dumps(endpoints["primary"]))
    finally:
        session.close()


def scenario_discovery(harness):
    """The provider is asked only when the operator names the endpoint."""
    session, config = harness
    try:
        session.send(b"/model\r")
        session.wait(1.2)
        check("the interface does not ask the provider",
              "not configured" not in session.screen(), session.screen())
        session.send(b"\x1b")
        session.wait(0.8)

        session.send(b"/model discover\r")
        session.wait(1.2)
        screen = session.screen()
        check("the discovery asks which endpoint", "Choose an endpoint" in screen, screen)
        check("the discovery says that it asks the provider",
              "asks the provider" in screen, screen)
        session.send(b"\r")
        session.wait(3.0)
        screen = session.screen()
        check("the catalogue is shown", "reports" in screen, screen)
    finally:
        session.close()


def scenario_small_terminal(root):
    """The interface stays usable in a small terminal."""
    session, _ = start(root, "small", cols=60, rows=10)
    try:
        session.send(b"/model\r")
        session.wait(1.2)
        screen = session.screen()
        check("the menu does not overflow a small terminal",
              "Discover" not in screen, screen)
        # The last action is reachable by scrolling.
        for _ in range(8):
            session.send(b"\x1b[B")
            session.wait(0.05)
        session.wait(0.8)
        screen = session.screen()
        check("the last action is reachable in a small terminal",
              "Discover" in screen, screen)
    finally:
        session.close()


def main():
    if not os.path.isfile(BINARY):
        print("missing HAL9001 binary:", BINARY)
        return 2
    base = os.environ.get("HAL9001_PTY_TMPDIR", "/tmp/opencode")
    os.makedirs(base, exist_ok=True)
    root = tempfile.mkdtemp(prefix="hal-model-", dir=base)
    provider = CatalogueProvider(PORT)
    try:
        # The provider serves a catalogue, so the discovery has something
        # to report. It is only ever asked when the operator asks for it.
        provider.start()
        time.sleep(0.3)

        print("== the management interface ==")
        session, _ = start(root, "interface")
        try:
            scenario_interface((session, None))
        finally:
            session.close()

        print("== the edit screen ==")
        session, config = start(root, "edit")
        try:
            scenario_edit((session, config))
        finally:
            session.close()

        print("== a refused value and a cancelled form ==")
        session, config = start(root, "refused")
        try:
            scenario_refused_and_cancelled((session, config))
        finally:
            session.close()

        print("== the change is there again after a restart ==")
        session, config = start(root, "reopen")
        try:
            scenario_reopen_and_restart((session, config), root)
        finally:
            session.close()

        print("== the removal asks first ==")
        session, config = start(root, "remove")
        try:
            scenario_remove((session, config))
        finally:
            session.close()

        print("== the add screen ==")
        session, config = start(root, "add")
        try:
            scenario_add((session, config))
        finally:
            session.close()

        print("== the context screen ==")
        session, config = start(root, "context")
        try:
            scenario_context((session, config))
        finally:
            session.close()

        print("== the endpoint screens ==")
        session, config = start(root, "endpoints")
        try:
            scenario_endpoints((session, config))
        finally:
            session.close()

        print("== the discovery ==")
        session, config = start(root, "discovery")
        try:
            scenario_discovery((session, config))
        finally:
            session.close()

        print("== a small terminal ==")
        scenario_small_terminal(root)
    finally:
        provider.stop()
        shutil.rmtree(root, ignore_errors=True)

    if FAILURES:
        print("%d MODEL USABILITY PTY CHECKS FAILED" % len(FAILURES))
        return 1
    print("ALL MODEL USABILITY PTY CHECKS PASSED")
    return 0


if __name__ == "__main__":
    sys.exit(main())
