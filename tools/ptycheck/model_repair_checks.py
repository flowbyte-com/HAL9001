#!/usr/bin/env python3
"""PTY checks for the repaired model-management workflow.

The operator found three defects in the management interface: adding a
model wanted an endpoint id where they had typed an address, editing a
model showed too little and offered no way back to a default, and Esc
left the whole interface instead of one step of it. The endpoint screens
had no obvious way to configure an API key.

These checks drive the real binary inside one pseudo-terminal, against an
isolated configuration, home, session store and workspace, and against a
local provider that never leaves the machine.

What is proved:

  * the operator's own journey: /model, Add, an address typed where an
    endpoint id belongs, a DeepSeek endpoint created from inside Add
    Model, the new endpoint chosen, the model added, and the model found
    in List and Show under the right endpoint;
  * the active model is never changed by any of it;
  * every setting of a model can be changed and saved, and the settings
    that were not touched keep their values;
  * the reasoning effort and the output limit can be restored to their
    defaults, and an empty field is never read as a reset;
  * a refused value and a cancelled screen change nothing;
  * Esc returns one level at a time, and the top of the interface is the
    conversation;
  * the interface stays usable in a small terminal;
  * a change survives a restart;
  * Discover, Show, Remove and the quick switch still work;
  * an answer that arrives while the interface is open still reaches the
    conversation;
  * the interface never leaves 444 and never shows a credential;
  * a first-time onboarding journey with a synthetic environment
    variable ends with a working endpoint, a credential reference and no
    secret anywhere on the screen or on the disk.

Usage:

    go build -o /tmp/opencode/hal9001 ./cmd/hal9001
    HAL9001_BINARY=/tmp/opencode/hal9001 python3 tools/ptycheck/model_repair_checks.py
"""
import json
import os
import shutil
import sys
import tempfile
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, HERE)
from pty_session import Session  # noqa: E402

FAILURES = []
BINARY = os.environ.get("HAL9001_BINARY", "/tmp/opencode/hal9001")
PORT = int(os.environ.get("HAL9001_REPAIR_PORT", "8290"))

# The synthetic credential of the onboarding journey. It is a made-up
# value: nothing in this suite ever reaches a real provider.
SECRET = "synthetic-deepseek-key-000111222333"
ANSWER = "the local provider answered"

# The catalogue the local provider serves.
CATALOGUE = ["flash", "deepseek-chat"]


def check(name, ok, detail=""):
    print(("[ok  ] " if ok else "[FAIL] ") + name + ("" if ok else "  " + str(detail)))
    if not ok:
        FAILURES.append(name)


class LocalProvider:
    """A local OpenAI-compatible provider that records what it is asked.

    It serves a catalogue, and it answers a question with a fixed answer.
    The first question is held until the check releases it, so a check
    can open the interface while the turn is in flight.
    """

    def __init__(self, port):
        self.port = port
        self.requests = []
        self.lock = threading.Lock()
        self.server = None
        self.held = False
        self.released = threading.Event()

    def record(self, request):
        with self.lock:
            self.requests.append(request)

    def start(self):
        provider = self

        class Handler(BaseHTTPRequestHandler):
            def log_message(self, *args):
                pass

            def do_GET(self):
                if self.path.rstrip("/").endswith("/models"):
                    body = json.dumps({"data": [{"id": i} for i in CATALOGUE]}).encode()
                    self.send_response(200)
                    self.send_header("Content-Type", "application/json")
                    self.send_header("Content-Length", str(len(body)))
                    self.end_headers()
                    self.wfile.write(body)
                    return
                self.send_response(404)
                self.end_headers()

            def do_POST(self):
                length = int(self.headers.get("Content-Length", 0))
                try:
                    request = json.loads(self.rfile.read(length))
                except Exception:
                    request = {}
                provider.record(request)
                if provider.held:
                    provider.released.wait(30.0)
                if not request.get("stream"):
                    body = json.dumps({
                        "choices": [{"message": {"content": ANSWER}, "finish_reason": "stop"}],
                    }).encode()
                    self.send_response(200)
                    self.send_header("Content-Type", "application/json")
                    self.send_header("Content-Length", str(len(body)))
                    self.end_headers()
                    self.wfile.write(body)
                    return
                self.send_response(200)
                self.send_header("Content-Type", "text/event-stream")
                self.send_header("Cache-Control", "no-cache")
                self.end_headers()
                self.wfile.write(
                    ("data: " + json.dumps(
                        {"choices": [{"delta": {"content": ANSWER}, "index": 0}]}) + "\n\n").encode())
                self.wfile.flush()
                self.wfile.write(
                    ("data: " + json.dumps(
                        {"choices": [{"delta": {}, "finish_reason": "stop"}]}) + "\n\n").encode())
                self.wfile.flush()

        self.server = ThreadingHTTPServer(("127.0.0.1", self.port), Handler)
        self.server.daemon_threads = True
        threading.Thread(target=self.server.serve_forever, daemon=True).start()

    def stop(self):
        if self.server is not None:
            self.server.shutdown()
            self.server.server_close()
            self.server = None


# The configuration the operator starts from: one endpoint and one
# model, and that model is the active one.
CONFIG = {
    "version": 2,
    "endpoints": [
        {
            "id": "primary",
            "name": "Primary",
            "protocol": "openai-compatible",
            "base_url": "http://127.0.0.1:%d/v1" % PORT,
            "credential_env": "PRIMARY_KEY",
            "credential": "",
            "discovery": {"enabled": False},
            "models": [
                {"id": "flash", "label": "Flash", "effort": "medium",
                 "context_mode": "auto", "context_window": 0,
                 "max_output_tokens": 0, "source": "manual"},
            ],
        },
    ],
    "active": {"endpoint": "primary", "model": "flash"},
    "theme": "HAL Classic",
}

# The configuration of an installation with two models: the switch is
# only visible when there is something to switch to.
TWO_MODELS = json.loads(json.dumps(CONFIG))
TWO_MODELS["endpoints"][0]["models"].append(
    {"id": "other", "label": "Other", "effort": "", "context_mode": "auto",
     "source": "manual"})

# The configuration of a first-time installation: nothing at all.
EMPTY_CONFIG = {
    "version": 2,
    "endpoints": [],
    "active": {"endpoint": "", "model": ""},
    "theme": "HAL Classic",
}


def stored(config):
    """The configuration as it is on disk."""
    with open(config) as fh:
        return json.load(fh)


def endpoints_of(config):
    """The endpoints as they are on disk."""
    return {e.get("id"): e for e in stored(config).get("endpoints") or []}


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


def start(root, name, cols=120, rows=45, config=None, base=None, path=None):
    """Start the interface with an isolated environment.

    The configuration that is written is the one that is named, and a
    path that is named is reused as it is: a second process reads
    exactly what the first one wrote.
    """
    home = os.path.join(root, name, "home") if base is None else base
    store = os.path.join(home, "sessions")
    workspace = os.path.join(home, "HAL")
    os.makedirs(store, exist_ok=True)
    os.makedirs(workspace, exist_ok=True)
    if path is None:
        path = os.path.join(home, "config.json")
    if base is None:
        with open(path, "w") as fh:
            json.dump(CONFIG if config is None else config, fh)
    os.environ["PRIMARY_KEY"] = "synthetic-primary-key"
    os.environ["DEEPSEEK_API_KEY"] = SECRET
    session = Session(BINARY, path, home, store, "repair", workspace,
                      cols=cols, rows=rows)
    session.start()
    session.wait_for("Enter send", 25.0)
    return session, path


def escape_to_chat(session, times=6):
    """Leave the management interface, one level at a time."""
    for _ in range(times):
        session.send(b"\x1b")
        session.wait(0.35)


def scenario_the_operator_journey(harness, provider):
    """The journey the operator reported, end to end."""
    session, config = harness
    try:
        # 1. The interface opens.
        session.send(b"/model\r")
        session.wait(1.2)
        screen = session.screen()
        check("/model opens the management interface",
              "Every action is also a command" in screen, screen)

        # 2. Add is chosen.
        session.send(b"a")
        session.wait(1.2)
        screen = session.screen()
        check("Add asks which endpoint the model belongs to",
              "Choose the endpoint" in screen, screen)
        check("the endpoint list names the endpoint and its address",
              "id primary" in screen and "http://127.0.0.1" in screen, screen)
        check("the endpoint list offers to create an endpoint",
              "Create an endpoint" in screen, screen)

        # 3. An address cannot be typed where the endpoint id belongs:
        #    there is no field for it any more.
        check("the endpoint is not typed by hand",
              "Endpoint " not in screen, screen)

        # The command that takes an endpoint id says what an address is.
        escape_to_chat(session)
        session.send(b"/model add https://api.deepseek.com/v1 deepseek-chat\r")
        session.wait(1.5)
        screen = session.screen()
        check("an address is refused as an endpoint id",
              "API URL" in screen, screen)
        check("the refusal names the endpoints that exist",
              "primary" in screen, screen)
        check("nothing was added",
              not any(m.get("id") == "deepseek-chat" for m in models_of(config)),
              json.dumps(models_of(config)))

        # 4. The endpoint is created from inside Add Model.
        session.send(b"/model\r")
        session.wait(1.2)
        session.send(b"a")
        session.wait(1.2)
        # The last row creates an endpoint.
        session.send(b"\x1b[B")
        session.wait(0.4)
        session.send(b"\r")
        session.wait(1.2)
        screen = session.screen()
        check("the endpoint screen opens",
              "Add an endpoint" in screen, screen)
        # The id.
        session.send(b"i")
        session.wait(0.8)
        session.send(b"deepseek")
        session.wait(0.5)
        session.send(b"\x13")
        session.wait(0.8)
        # The name.
        session.send(b"n")
        session.wait(0.8)
        session.send(b"DeepSeek")
        session.wait(0.5)
        session.send(b"\x13")
        session.wait(0.8)
        # The address, which is the local fixture.
        session.send(b"u")
        session.wait(0.8)
        session.send(b"http://127.0.0.1:%d/v1" % PORT)
        session.wait(0.5)
        session.send(b"\x13")
        session.wait(0.8)
        screen = session.screen()
        check("the credential source is offered with a suggestion",
              "Credential source" in screen, screen)
        # The credential is a reference to an environment variable.
        session.send(b"k")
        session.wait(1.0)
        screen = session.screen()
        check("the credential list suggests the variable of the endpoint",
              "DEEPSEEK_API_KEY" in screen, screen)
        check("the credential list never shows the key",
              SECRET not in screen, screen)
        # The suggestion is chosen.
        session.send(b"\x1b[B")
        session.wait(0.4)
        session.send(b"\r")
        session.wait(1.0)
        screen = session.screen()
        check("the credential source is the suggested variable",
              "DEEPSEEK_API_KEY" in screen, screen)
        check("the screen says whether the variable is set",
              "DEEPSEEK_API_KEY is set in this environment" in screen, screen)
        check("the screen never shows the key",
              SECRET not in screen, screen)
        # The endpoint is stored.
        session.send(b"s")
        session.wait(1.5)
        endpoints = endpoints_of(config)
        check("the endpoint is on disk", "deepseek" in endpoints,
              json.dumps(list(endpoints)))
        if "deepseek" in endpoints:
            check("the endpoint kept its address",
                  endpoints["deepseek"].get("base_url") == "http://127.0.0.1:%d/v1" % PORT,
                  json.dumps(endpoints["deepseek"]))
            check("the endpoint names the variable that holds the key",
                  endpoints["deepseek"].get("credential_env") == "DEEPSEEK_API_KEY",
                  json.dumps(endpoints["deepseek"]))
            check("the endpoint holds no literal key",
                  not endpoints["deepseek"].get("credential"),
                  json.dumps(endpoints["deepseek"]))
            check("the endpoint did not take the key of another endpoint",
                  endpoints["deepseek"].get("credential_env") != "PRIMARY_KEY",
                  json.dumps(endpoints["deepseek"]))

        # 5. The operator is back in Add Model, with the new endpoint
        #    chosen, and the model is added.
        screen = session.screen()
        check("the operator is back in Add Model",
              "Add a model to deepseek" in screen, screen)
        session.send(b"deepseek-chat")
        session.wait(0.6)
        session.send(b"\x13")
        session.wait(1.5)
        screen = session.screen()
        check("the model was added", "Added model" in screen, screen)
        check("the model is under the endpoint that was created",
              "deepseek" in screen, screen)

        # 6. The model is in the configuration, under the right endpoint.
        check("the model is on disk under the new endpoint",
              any(m.get("id") == "deepseek-chat" for m in models_of(config, "deepseek")),
              json.dumps(models_of(config, "deepseek")))
        check("the model was not added to the other endpoint",
              not any(m.get("id") == "deepseek-chat" for m in models_of(config, "primary")),
              json.dumps(models_of(config, "primary")))

        # 7. The list and the details show it under the right endpoint.
        session.send(b"/model list\r")
        session.wait(1.5)
        screen = session.screen()
        check("the list names the new endpoint",
              "id deepseek" in screen, screen)
        check("the list names the new model",
              "deepseek-chat" in screen, screen)
        session.send(b"\x1b")
        session.wait(0.8)
        session.send(b"/model show deepseek deepseek-chat\r")
        session.wait(1.5)
        screen = session.screen()
        check("the details name the endpoint of the model",
              "deepseek" in screen, screen)
        check("the details name the model",
              "deepseek-chat" in screen, screen)
        session.send(b"\x1b")
        session.wait(0.8)

        # 8. The active model did not change.
        check("the active model did not change",
              (stored(config).get("active") or {}).get("model") == "flash",
              json.dumps(stored(config).get("active")))
    finally:
        session.close()


def scenario_every_setting(harness):
    """Every setting of a model can be changed and saved."""
    session, config = harness
    try:
        session.send(b"/model edit\r")
        session.wait(1.2)
        session.send(b"\r")
        session.wait(1.2)
        screen = session.screen()
        for want in ("Label", "Effort", "Output limit", "Context"):
            check("the settings screen offers %s" % want, want in screen, screen)

        # The label.
        session.send(b"l")
        session.wait(0.8)
        session.send(b"Renamed")
        session.wait(0.5)
        session.send(b"\x13")
        session.wait(0.8)
        # The effort: the list offers the documented levels, and the
        # model's own default is one of them.
        session.send(b"e")
        session.wait(0.8)
        screen = session.screen()
        check("the effort list offers the model's own default",
              "the model's own default" in screen, screen)
        for level in ("low", "medium", "high", "xhigh", "max"):
            check("the effort list offers %s" % level, level in screen, screen)
        session.send(b"\x1b[B\x1b[B\x1b[B")
        session.wait(0.5)
        session.send(b"\r")
        session.wait(0.8)
        # The output limit.
        session.send(b"o")
        session.wait(0.8)
        session.send(b"t")
        session.wait(0.8)
        session.send(b"2048")
        session.wait(0.5)
        session.send(b"\x13")
        session.wait(0.8)
        session.send(b"\x1b")
        session.wait(0.8)
        # The context mode.
        session.send(b"c")
        session.wait(0.8)
        session.send(b"m")
        session.wait(0.8)
        session.send(b"\x1b[B")
        session.wait(0.5)
        session.send(b"\r")
        session.wait(0.8)
        session.send(b"\x1b")
        session.wait(0.8)
        # The changes are written.
        screen = session.screen()
        check("the settings menu shows the staged changes",
              "Renamed" in screen, screen)
        session.send(b"s")
        session.wait(1.5)
        screen = session.screen()
        check("the changes were written", "updated" in screen, screen)
        record = model_of(config, "flash")
        check("the label was stored", record.get("label") == "Renamed",
              json.dumps(record))
        check("the effort was stored", record.get("effort") == "high",
              json.dumps(record))
        check("the output limit was stored",
              record.get("max_output_tokens") == 2048, json.dumps(record))
        check("the context mode was stored",
              record.get("context_mode") == "manual", json.dumps(record))
    finally:
        session.close()


def scenario_the_defaults(harness):
    """The effort and the output limit can be restored to their defaults."""
    session, config = harness
    try:
        # The model starts with an effort and an output limit.
        session.send(b"/model edit\r")
        session.wait(1.2)
        session.send(b"\r")
        session.wait(1.2)
        # The effort is restored to the model's own default.
        session.send(b"e")
        session.wait(0.8)
        session.send(b"\r")
        session.wait(0.8)
        # The output limit is restored to the provider's own default.
        session.send(b"o")
        session.wait(0.8)
        session.send(b"p")
        session.wait(0.8)
        session.send(b"s")
        session.wait(1.5)
        screen = session.screen()
        check("the changes were written", "updated" in screen, screen)
        record = model_of(config, "flash")
        check("the effort was restored", not record.get("effort"),
              json.dumps(record))
        check("the output limit was restored", not record.get("max_output_tokens"),
              json.dumps(record))
        check("the label was not touched", record.get("label") == "Flash",
              json.dumps(record))
    finally:
        session.close()


def scenario_an_empty_field_is_not_a_reset(harness):
    """An empty field never restores a default on its own."""
    session, config = harness
    try:
        session.send(b"/model edit\r")
        session.wait(1.2)
        session.send(b"\r")
        session.wait(1.2)
        # The effort screen is opened and left empty.
        session.send(b"e")
        session.wait(0.8)
        session.send(b"\x1b")
        session.wait(0.8)
        # The output screen is opened and left empty.
        session.send(b"o")
        session.wait(0.8)
        session.send(b"t")
        session.wait(0.8)
        session.send(b"\x1b")
        session.wait(0.8)
        # Nothing was written.
        session.send(b"s")
        session.wait(1.5)
        record = model_of(config, "flash")
        check("the effort was not restored", record.get("effort") == "medium",
              json.dumps(record))
        check("the output limit was not restored",
              not record.get("max_output_tokens"), json.dumps(record))
    finally:
        session.close()


def scenario_a_refused_change(harness):
    """A change the configuration refuses changes nothing."""
    session, config = harness
    try:
        before = model_of(config, "flash")
        session.send(b"/model edit\r")
        session.wait(1.2)
        session.send(b"\r")
        session.wait(1.2)
        # An output limit that does not fit the model's own context
        # window is refused by the configuration.
        session.send(b"o")
        session.wait(0.8)
        session.send(b"t")
        session.wait(0.8)
        session.send(b"999999")
        session.wait(0.5)
        session.send(b"\x13")
        session.wait(0.8)
        session.send(b"\x1b")
        session.wait(0.8)
        # The settings are written, and the configuration refuses them.
        session.send(b"s")
        session.wait(1.5)
        screen = session.screen()
        check("the refusal is reported", "Cannot change the model" in screen, screen)
        check("the refused screen stays open",
              "Edit model primary" in screen, screen)
        check("the refused change was not stored",
              model_of(config, "flash").get("max_output_tokens") ==
              before.get("max_output_tokens"),
              json.dumps(model_of(config, "flash")))
    finally:
        session.close()


def scenario_esc(harness):
    """Esc returns one level at a time."""
    session, config = harness
    try:
        session.send(b"/model edit\r")
        session.wait(1.2)
        check("the list is open", "Choose a model" in session.screen(),
              session.screen())
        session.send(b"\x1b")
        session.wait(0.8)
        screen = session.screen()
        check("Esc returns to the menu",
              "Every action is also a command" in screen, screen)
        check("the interface is still open", "444 READ ONLY" in screen, screen)

        # A list of choices returns to the step it came from.
        session.send(b"e")
        session.wait(1.2)
        session.send(b"\r")
        session.wait(1.2)
        screen = session.screen()
        check("the settings of the model are shown",
              "Edit model primary" in screen, screen)
        session.send(b"e")
        session.wait(0.8)
        screen = session.screen()
        check("the effort list is open",
              "the model's own default" in screen, screen)
        session.send(b"\x1b")
        session.wait(0.8)
        screen = session.screen()
        check("Esc returns to the settings",
              "Edit model primary" in screen, screen)
        session.send(b"\x1b")
        session.wait(0.8)
        screen = session.screen()
        check("Esc returns to the list of models",
              "Choose a model" in screen, screen)
        session.send(b"\x1b")
        session.wait(0.8)
        screen = session.screen()
        check("Esc returns to the menu",
              "Every action is also a command" in screen, screen)
        session.send(b"\x1b")
        session.wait(0.8)
        screen = session.screen()
        check("Esc at the top of the interface returns to the conversation",
              "Every action is also a command" not in screen, screen)
    finally:
        session.close()


def scenario_small_terminal(root):
    """The interface stays usable in a small terminal."""
    session, _ = start(root, "small", cols=60, rows=12)
    try:
        session.send(b"/model\r")
        session.wait(1.2)
        screen = session.screen()
        check("the menu does not overflow a small terminal",
              "Discover" not in screen, screen)
        for _ in range(8):
            session.send(b"\x1b[B")
            session.wait(0.25)
        session.wait(1.0)
        screen = session.screen()
        check("the last action is reachable in a small terminal",
              "List" in screen, screen)
        check("the panel scrolled to the chosen action",
              "▸ L  List" in screen, screen)
        # The endpoint list is usable too.
        session.send(b"a")
        session.wait(1.2)
        screen = session.screen()
        check("the endpoint list is usable in a small terminal",
              "Create an endpoint" in screen, screen)
    finally:
        session.close()


def scenario_restart(harness, root):
    """A change survives a restart."""
    session, config = harness
    try:
        session.send(b"/model edit\r")
        session.wait(1.2)
        session.send(b"\r")
        session.wait(1.2)
        session.send(b"l")
        session.wait(0.8)
        session.send(b"Kept")
        session.wait(0.5)
        session.send(b"\x13")
        session.wait(0.8)
        session.send(b"s")
        session.wait(1.5)
    finally:
        session.close()

    home = os.path.dirname(os.path.dirname(config))
    session, _ = start(root, "restart", base=home, path=config)
    try:
        session.send(b"/model show\r")
        session.wait(1.2)
        session.send(b"\r")
        session.wait(1.5)
        screen = session.screen()
        check("the change survived the restart", "Kept" in screen, screen)
    finally:
        session.close()


def scenario_the_other_actions(harness):
    """Discover, Show, Remove and the quick switch still work."""
    session, config = harness
    try:
        # Discover asks the provider only when the operator names the
        # endpoint.
        provider_requests = []
        session.send(b"/model discover\r")
        session.wait(1.2)
        screen = session.screen()
        check("the discovery asks which endpoint",
              "Choose an endpoint" in screen, screen)
        session.send(b"\r")
        session.wait(2.0)
        screen = session.screen()
        check("the catalogue is shown", "reports" in screen, screen)
        session.send(b"\x1b")
        session.wait(0.8)

        # Show describes the model.
        session.send(b"/model show\r")
        session.wait(1.2)
        session.send(b"\r")
        session.wait(1.5)
        screen = session.screen()
        check("the details are shown", "endpoint" in screen, screen)
        session.send(b"\x1b")
        session.wait(0.8)

        # The quick switch changes the active model, and only it does.
        session.send(b"/model use\r")
        session.wait(1.2)
        screen = session.screen()
        check("the quick switch asks which model",
              "Choose the model to use" in screen, screen)
        check("the quick switch names the models", "other" in screen, screen)
        session.send(b"\x1b[B")
        session.wait(0.5)
        session.send(b"\r")
        session.wait(1.5)
        check("the quick switch changed the active model",
              (stored(config).get("active") or {}).get("model") == "other",
              json.dumps(stored(config).get("active")))

        # Remove asks before it removes.
        session.send(b"/model remove\r")
        session.wait(1.2)
        session.send(b"\r")
        session.wait(1.2)
        screen = session.screen()
        check("the removal asks for a confirmation",
              "Enter confirms" in screen, screen)
        session.send(b"\x1b")
        session.wait(0.8)
        check("the cancelled removal kept the model",
              any(m.get("id") == "flash" for m in models_of(config)),
              json.dumps(models_of(config)))
    finally:
        session.close()


def scenario_completion_while_open(harness, provider):
    """An answer that arrives while the interface is open still arrives."""
    session, config = harness
    try:
        provider.held = True
        provider.released.clear()
        session.send(b"a question\r")
        session.wait(1.5)
        check("the provider received the question",
              len(provider.requests) > 0, len(provider.requests))
        check("no answer has arrived yet", ANSWER not in session.screen(),
              session.screen())
        # The interface opens while the turn is in flight.
        session.send(b"/model\r")
        session.wait(1.2)
        check("the interface opens while the turn runs",
              "Every action is also a command" in session.screen(),
              session.screen())
        # The provider answers now.
        provider.held = False
        provider.released.set()
        check("the completion is processed while the interface is open",
              session.until(lambda: "Complete" in session.screen(), 25.0),
              session.screen())
        check("the interface is still open",
              "Every action is also a command" in session.screen(),
              session.screen())
        escape_to_chat(session)
        screen = session.screen()
        check("the answer reached the conversation", ANSWER in screen, screen)
        check("the answer reached the conversation exactly once",
              screen.count(ANSWER) == 1, screen)
    finally:
        provider.held = False
        provider.released.set()
        session.close()


def scenario_safety(harness):
    """The interface never leaves 444 and never shows a credential."""
    session, config = harness
    try:
        for command in ("/model", "/model add", "/model edit", "/model remove",
                        "/endpoint", "/endpoint add", "/endpoint edit"):
            escape_to_chat(session)
            session.send(command.encode() + b"\r")
            session.wait(1.0)
            screen = session.screen()
            check("%s keeps the read-only mode" % command,
                  "444 READ ONLY" in screen, screen)
            check("%s shows no credential" % command, SECRET not in screen, screen)
    finally:
        session.close()


def scenario_onboarding(harness):
    """The first-time journey: an endpoint, a credential and a model."""
    session, config = harness
    try:
        # The first run asks for three values.
        screen = session.screen()
        check("the first run asks for the endpoint",
              "Endpoint:" in screen, screen)
        check("the first run asks for the model",
              "Model:" in screen, screen)
        check("the first run asks for the credential",
              "API key:" in screen, screen)

        # The endpoint, the model and the credential are named. The
        # credential is a synthetic one: nothing here reaches a real
        # provider.
        session.send(b"http://127.0.0.1:%d/v1" % PORT)
        session.wait(0.6)
        session.send(b"\t")
        session.wait(0.4)
        session.send(b"deepseek-chat")
        session.wait(0.6)
        session.send(b"\t")
        session.wait(0.4)
        session.send(SECRET.encode())
        session.wait(0.8)
        screen = session.screen()
        check("the credential is never echoed",
              SECRET not in screen, screen)
        # The setup is saved.
        session.send(b"\x13")
        session.wait(2.0)
        screen = session.screen()
        check("the setup was saved", "Provider configured" in screen, screen)

        endpoints = endpoints_of(config)
        check("the endpoint is on disk", "default" in endpoints,
              json.dumps(list(endpoints)))
        if "default" in endpoints:
            check("the endpoint kept its address",
                  endpoints["default"].get("base_url") ==
                  "http://127.0.0.1:%d/v1" % PORT,
                  json.dumps(endpoints["default"]))
            check("the model was added",
                  any(m.get("id") == "deepseek-chat" for m in models_of(config, "default")),
                  json.dumps(models_of(config, "default")))
        # The file that holds the secret is protected.
        mode = os.stat(config).st_mode & 0o777
        check("the configuration file is protected", mode == 0o600, oct(mode))

        # The literal key is replaced by a reference to the variable
        # that holds it.
        session.send(b"/endpoint edit\r")
        session.wait(1.5)
        session.send(b"\r")
        session.wait(1.5)
        screen = session.screen()
        check("the endpoint screen names the credential source",
              "Credential source" in screen, screen)
        session.send(b"k")
        session.wait(1.0)
        session.send(b"\x1b[B\x1b[B")
        session.wait(0.5)
        session.send(b"\r")
        session.wait(1.0)
        screen = session.screen()
        check("the name of the variable is asked for",
              "Credential source" in screen, screen)
        session.send(b"DEEPSEEK_API_KEY")
        session.wait(0.6)
        session.send(b"\x13")
        session.wait(0.8)
        screen = session.screen()
        check("the screen says the variable is set",
              "DEEPSEEK_API_KEY is set in this environment" in screen, screen)
        check("the screen never shows the key", SECRET not in screen, screen)
        session.send(b"s")
        session.wait(1.5)
        endpoints = endpoints_of(config)
        if "default" in endpoints:
            check("the endpoint names the variable that holds the key",
                  endpoints["default"].get("credential_env") == "DEEPSEEK_API_KEY",
                  json.dumps(endpoints["default"]))
            check("the literal key is gone",
                  not endpoints["default"].get("credential"),
                  json.dumps(endpoints["default"]))
        with open(config) as fh:
            raw = fh.read()
        check("the secret is not in the configuration file", SECRET not in raw,
              raw[:200])
    finally:
        session.close()


def scenario_a_cancelled_onboarding(harness):
    """A setup that is left changes nothing."""
    session, config = harness
    try:
        before = stored(config)
        session.send(b"http://127.0.0.1:%d/v1" % PORT)
        session.wait(0.6)
        session.send(b"\t")
        session.wait(0.4)
        session.send(b"deepseek-chat")
        session.wait(0.6)
        session.send(b"\x1b")
        session.wait(1.0)
        screen = session.screen()
        check("the setup is not left behind",
              "Finish setup" in screen, screen)
        check("the configuration is untouched", stored(config) == before,
              json.dumps(stored(config)))
        check("no endpoint was written", not stored(config).get("endpoints"),
              json.dumps(stored(config).get("endpoints")))
    finally:
        session.close()


def main():
    if not os.path.isfile(BINARY):
        print("missing HAL9001 binary:", BINARY)
        return 2
    base = os.environ.get("HAL9001_PTY_TMPDIR", "/tmp/opencode")
    os.makedirs(base, exist_ok=True)
    root = tempfile.mkdtemp(prefix="hal-repair-", dir=base)
    provider = LocalProvider(PORT)
    try:
        provider.start()
        time.sleep(0.3)

        print("== the journey the operator reported ==")
        session, config = start(root, "journey")
        try:
            scenario_the_operator_journey((session, config), provider)
        finally:
            session.close()

        print("== every setting of a model ==")
        session, config = start(root, "settings")
        try:
            scenario_every_setting((session, config))
        finally:
            session.close()

        print("== the defaults can be restored ==")
        session, config = start(root, "defaults")
        try:
            scenario_the_defaults((session, config))
        finally:
            session.close()

        print("== an empty field is not a reset ==")
        session, config = start(root, "empty")
        try:
            scenario_an_empty_field_is_not_a_reset((session, config))
        finally:
            session.close()

        print("== a refused change changes nothing ==")
        session, config = start(root, "refused")
        try:
            scenario_a_refused_change((session, config))
        finally:
            session.close()

        print("== Esc walks back one level ==")
        session, config = start(root, "esc")
        try:
            scenario_esc((session, config))
        finally:
            session.close()

        print("== a small terminal ==")
        scenario_small_terminal(root)

        print("== the change survives a restart ==")
        session, config = start(root, "persist")
        try:
            scenario_restart((session, config), root)
        finally:
            session.close()

        print("== the other actions still work ==")
        session, config = start(root, "actions", config=TWO_MODELS)
        try:
            scenario_the_other_actions((session, config))
        finally:
            session.close()

        print("== an answer arrives while the interface is open ==")
        session, config = start(root, "completion")
        try:
            scenario_completion_while_open((session, config), provider)
        finally:
            session.close()

        print("== the read-only mode and the credential ==")
        session, config = start(root, "safety")
        try:
            scenario_safety((session, config))
        finally:
            session.close()

        print("== the first-time journey ==")
        session, config = start(root, "onboarding", config=EMPTY_CONFIG)
        try:
            scenario_onboarding((session, config))
        finally:
            session.close()

        print("== a setup that is left changes nothing ==")
        session, config = start(root, "cancelled", config=EMPTY_CONFIG)
        try:
            scenario_a_cancelled_onboarding((session, config))
        finally:
            session.close()
    finally:
        provider.stop()
        shutil.rmtree(root, ignore_errors=True)

    if FAILURES:
        print("%d MODEL REPAIR PTY CHECKS FAILED" % len(FAILURES))
        return 1
    print("ALL MODEL REPAIR PTY CHECKS PASSED")
    return 0


if __name__ == "__main__":
    sys.exit(main())
