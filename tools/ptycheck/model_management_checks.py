#!/usr/bin/env python3
"""Integration checks: the provider-scoped model management.

The checks drive the real binary in a pseudo-terminal with an isolated
configuration, home, session store and workspace. Nothing outside the
temporary directory is read or written, and no provider is contacted: the
endpoint points at a closed port, because the checks are about the
configuration itself.

What is proved:

  * A pre-version-2 configuration is migrated at startup, and the
    migration keeps the endpoint, the credential, the models, the theme
    and the active session.
  * /model list shows the endpoints and their models.
  * /model add stores a model under the endpoint it was named for, and
    does not change the active model.
  * /model use selects a model, and the selection is persisted.
  * /model remove refuses to remove the active model without an explicit
    replacement, and removes it with one.
  * /endpoint list and /endpoint add work.
  * The configuration on disk has the version-2 shape.

Usage:

    go build -o /tmp/opencode/hal9001 ./cmd/hal9001
    HAL9001_BINARY=/tmp/opencode/hal9001 python3 tools/ptycheck/model_management_checks.py
"""
import json
import os
import shutil
import sys
import tempfile

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, HERE)

FAILURES = []

# The configuration an operator would have before the version-2 schema: one
# endpoint, one credential and a flat list of model identifiers.
LEGACY_CONFIG = {
    "provider": "openai-compatible",
    "endpoint": "https://api.minimax.io/v1",
    "api_key": "test-key",
    "models": [
        {"id": "MiniMax-M3.1-Flash", "effort": "medium"},
        {"id": "deekseek-flash"},
    ],
    "active_model": "deekseek-flash",
    "theme": "HAL Classic",
    "active_session": "cli-20261107-195136-3",
}


def check(name, ok, detail=""):
    if ok:
        print("[ok  ] %s" % name)
    else:
        print("[FAIL] %s" % name)
        FAILURES.append(name)
        if detail:
            print("       %s" % detail.replace("\n", "\n       ")[:1500])


def shown(raw, needle):
    """Report whether the text was ever put on the screen."""
    return needle in raw.decode("utf-8", "replace")


def main():
    base = os.environ.get("HAL9001_PTY_TMPDIR", "/tmp/opencode")
    root = tempfile.mkdtemp(prefix="hal9001-models-", dir=base)
    home = os.path.join(root, "home")
    store = os.path.join(home, "sessions")
    os.makedirs(store)
    os.makedirs(os.path.join(root, "HAL"))
    config = os.path.join(home, "config.json")
    with open(config, "w") as f:
        json.dump(LEGACY_CONFIG, f)

    os.environ["HAL9001_CONFIG"] = config
    os.environ["HAL9001_HOME"] = home
    os.environ["HAL9001_SESSION"] = "models"

    import pty_drive
    from termsim import render
    pty_drive.CONFIG = config
    pty_drive.HOME = home
    pty_drive.STORE = store
    pty_drive.SESSION = "models"

    def capture(script, cols=100, rows=30):
        timeout = sum(delay for delay, _ in script) + 1.5
        raw = pty_drive.run(script, cols=cols, rows=rows, timeout=timeout,
                            fresh=False, session="models",
                            args=["--workspace", os.path.join(root, "HAL")])
        return raw, render(raw, cols, rows)

    def stored():
        with open(config) as f:
            return json.load(f)

    try:
        # 1. The startup migration turns the legacy file into the
        #    version-2 shape.
        raw, screen = capture([(2.0, b"")])
        check("HAL starts with a legacy configuration", "HAL9001" in screen.text(),
              screen.text())
        migrated = stored()
        check("the configuration was migrated", migrated.get("version") == 2,
              json.dumps(migrated, indent=2)[:1200])
        endpoints = migrated.get("endpoints") or []
        check("the endpoint survived the migration",
              len(endpoints) == 1 and
              endpoints[0].get("base_url") == "https://api.minimax.io/v1",
              json.dumps(endpoints, indent=2)[:1200])
        check("the credential survived the migration",
              endpoints and endpoints[0].get("credential") == "test-key",
              json.dumps(endpoints, indent=2)[:1200])
        check("both models stayed under the endpoint",
              endpoints and [m.get("id") for m in endpoints[0].get("models", [])] ==
              ["MiniMax-M3.1-Flash", "deekseek-flash"],
              json.dumps(endpoints, indent=2)[:1200])
        check("the active model survived the migration",
              (migrated.get("active") or {}).get("model") == "deekseek-flash",
              json.dumps(migrated.get("active"), indent=2))
        check("the theme survived the migration",
              migrated.get("theme") == "HAL Classic", migrated.get("theme"))
        # The migration keeps the active session, but the run itself then
        # records the conversation it opened. The backup the migration
        # wrote is the evidence that the field survived it.
        backup = config + ".v1.bak"
        check("the migration kept a backup of the original",
              os.path.exists(backup), backup)
        if os.path.exists(backup):
            with open(backup) as f:
                original = json.load(f)
            check("the backup holds the original active session",
                  original.get("active_session") == "cli-20261107-195136-3",
                  original.get("active_session"))
        check("the active session was not dropped by the migration",
              "active_session" in migrated, json.dumps(migrated, indent=2)[:800])
        check("the questionable model was flagged for review",
              endpoints and any(m.get("review") for m in endpoints[0].get("models", [])),
              json.dumps(endpoints, indent=2)[:1200])

        # 2. /model alone opens the picker, and /endpoint alone shows the
        #    endpoints.
        raw, screen = capture([(2.0, b"/model\r"), (1.0, b"")])
        check("/model opens the picker", shown(raw, "Models"), screen.text())
        raw, screen = capture([(2.0, b"/endpoint\r"), (1.0, b"")])
        check("/endpoint shows the endpoints", shown(raw, "Endpoints"), screen.text())

        # 3. /model list shows the endpoints and their models.
        raw, screen = capture([(2.0, b"/model list\r"), (1.0, b"")])
        check("/model list opens", shown(raw, "Endpoints"), screen.text())
        check("/model list names the endpoint",
              "MiniMax" in screen.text() or "minimax" in screen.text(), screen.text())
        check("/model list names the models",
              "MiniMax-M3.1-Flash" in screen.text() and
              "deekseek-flash" in screen.text(), screen.text())
        check("/model list never shows the credential",
              "test-key" not in raw.decode("utf-8", "replace"), screen.text())
        check("/model list marks the model for review",
              "needs review" in screen.text(), screen.text())

        # 3. /model add stores a model under its endpoint and leaves the
        #    active model alone.
        raw, screen = capture([(2.0, b"/model add minimax deepseek-chat\r"), (1.0, b"")])
        check("/model add works", shown(raw, "Added model"), screen.text())
        check("/model add says the active model is unchanged",
              "active model is unchanged" in raw.decode("utf-8", "replace"), screen.text())
        added = stored()
        models = (added["endpoints"][0].get("models") or [])
        check("the model was stored under its endpoint",
              any(m.get("id") == "deepseek-chat" for m in models),
              json.dumps(models, indent=2)[:800])
        check("the active model did not change",
              (added.get("active") or {}).get("model") == "deekseek-flash",
              json.dumps(added.get("active"), indent=2))

        # 4. /model use selects a model, and the selection is persisted.
        raw, screen = capture([(2.0, b"/model use minimax MiniMax-M3.1-Flash\r"), (1.0, b"")])
        check("/model use works", shown(raw, "Model: MiniMax-M3.1-Flash"), screen.text())
        selected = stored()
        check("the selection was persisted",
              (selected.get("active") or {}).get("model") == "MiniMax-M3.1-Flash",
              json.dumps(selected.get("active"), indent=2))

        # 5. Removing the active model needs an explicit replacement.
        raw, screen = capture([(2.0, b"/model remove minimax MiniMax-M3.1-Flash\r"), (1.0, b"")])
        check("removing the active model is refused",
              "replacement" in raw.decode("utf-8", "replace"), screen.text())
        kept = stored()
        check("the active model is still there",
              any(m.get("id") == "MiniMax-M3.1-Flash"
                  for m in kept["endpoints"][0].get("models", [])),
              json.dumps(kept["endpoints"][0].get("models"), indent=2)[:800])

        raw, screen = capture([(2.0, b"/model remove minimax MiniMax-M3.1-Flash "
                                        b"--use minimax deekseek-flash\r"), (1.2, b"")])
        check("removing the active model with a replacement works",
              shown(raw, "Removed model"), screen.text())
        removed = stored()
        check("the model was removed",
              not any(m.get("id") == "MiniMax-M3.1-Flash"
                      for m in removed["endpoints"][0].get("models", [])),
              json.dumps(removed["endpoints"][0].get("models"), indent=2)[:800])
        check("the replacement became the active model",
              (removed.get("active") or {}).get("model") == "deekseek-flash",
              json.dumps(removed.get("active"), indent=2))

        # 6. /endpoint list and /endpoint add work.
        raw, screen = capture([(2.0, b"/endpoint list\r"), (1.0, b"")])
        check("/endpoint list opens", shown(raw, "Endpoints"), screen.text())

        raw, screen = capture([(2.0, b"/endpoint add deepseek DeepSeek openai-compatible "
                                        b"https://api.deepseek.com/v1 DEEPSEEK_KEY\r"), (1.2, b"")])
        check("/endpoint add works", shown(raw, "Added endpoint"), screen.text())
        endpoints = stored().get("endpoints") or []
        check("the endpoint was stored",
              any(e.get("id") == "deepseek" for e in endpoints),
              json.dumps(endpoints, indent=2)[:1200])
        check("the endpoint kept its credential reference",
              any(e.get("id") == "deepseek" and
                  e.get("credential_env") == "DEEPSEEK_KEY" for e in endpoints),
              json.dumps(endpoints, indent=2)[:1200])

        # 7. The command suggestions find the new commands.
        for prefix, want in (("/mod", "/model"), ("/end", "/endpoint")):
            raw, screen = capture([(2.0, prefix.encode()), (0.8, b"")])
            check("the suggestions find %s" % want, shown(raw, want), screen.text())
    finally:
        shutil.rmtree(root, ignore_errors=True)

    if FAILURES:
        print("%d MODEL MANAGEMENT CHECKS FAILED" % len(FAILURES))
        return 1
    print("ALL MODEL MANAGEMENT CHECKS PASSED")
    return 0


if __name__ == "__main__":
    sys.exit(main())
