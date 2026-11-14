#!/usr/bin/env python3
"""PTY regression test for inconsistent model ownership in removal.

Observed failure: the model picker showed "MiniMax-M3.1-Flash" under
endpoint "deepseek-flash" with a "(flags)" designation, but selecting
it for removal failed with:

    Cannot remove the model: endpoint "deepseek-flash" has no model
    "MiniMax-M3.1-Flash"

Root cause under test: listModels() injects the --model flag value
(HAL9001_MODEL) and the runtime model override as display rows
attributed to the *active* endpoint, labelled "<id> (flags)". Those
rows name no stored (endpoint, model) pair, so removal — which
correctly resolves against storage — fails. The persisted data was
never wrong; the picker offered a phantom entry.

Corrected behaviour asserted here:
  * the removal picker offers exactly the stored endpoint/model pairs,
    never a "(flags)" row;
  * removing the selected pair deletes exactly that pair;
  * the active model is unchanged.

Usage (isolated fixtures only, synthetic config, no live data):

    go build -o /tmp/opencode/hal-flags ./cmd/hal9001
    HAL9001_BINARY=/tmp/opencode/hal-flags python3 tools/ptycheck/model_flags_checks.py
"""
import json
import os
import shutil
import sys
import tempfile

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, HERE)
from pty_session import Session  # noqa: E402

FAILURES = []
BINARY = os.environ.get("HAL9001_BINARY", "/tmp/opencode/hal-flags")


def check(name, ok, detail=""):
    print(("[ok  ] " if ok else "[FAIL] ") + name + ("" if ok else "  " + str(detail)))
    if not ok:
        FAILURES.append(name)


def open_session(root, config):
    home = os.path.join(root, "home")
    store = os.path.join(home, "sessions")
    workspace = os.path.join(home, "HAL")
    os.makedirs(store, exist_ok=True)
    os.makedirs(workspace, exist_ok=True)
    path = os.path.join(home, "config.json")
    with open(path, "w") as fh:
        json.dump(config, fh)
    session = Session(BINARY, path, home, store, "flags", workspace)
    session.start()
    session.wait_for("Enter send", 25.0)
    return session, path


def stored(path):
    with open(path) as fh:
        return json.load(fh)


def models_of(path):
    out = []
    for e in stored(path).get("endpoints", []):
        for m in e.get("models", []):
            out.append((e["id"], m["id"]))
    return out


def main():
    root = tempfile.mkdtemp(prefix="hal-flags-")
    # Simulate `hal --model MiniMax-M3.1-Flash`: the flag value travels
    # through HAL9001_MODEL into the runtime override.
    os.environ["HAL9001_MODEL"] = "MiniMax-M3.1-Flash"
    os.environ["FLAGS_TEST_KEY"] = "synthetic-flags-key-000"
    try:
        config = {
            "version": 2,
            "endpoints": [
                {"id": "deepseek-flash", "name": "DeepSeek",
                 "protocol": "openai-compatible",
                 "base_url": "http://127.0.0.1:9/v1",
                 "credential_env": "FLAGS_TEST_KEY",
                 "models": [{"id": "deepseek-flash", "context_mode": "auto"}]},
                {"id": "minimax", "name": "MiniMax",
                 "protocol": "openai-compatible",
                 "base_url": "http://127.0.0.1:9/v1",
                 "credential_env": "FLAGS_TEST_KEY",
                 "models": [{"id": "MiniMax-M3.1-Flash", "context_mode": "auto"}]},
            ],
            "active": {"endpoint": "deepseek-flash", "model": "deepseek-flash"},
        }
        session, path = open_session(root, config)
        try:
            # The removal picker must offer stored pairs only.
            session.send(b"/model remove\r")
            session.wait(1.5)
            screen = session.screen()
            check("the remove picker opens", "Choose a model to remove" in screen, screen[-800:])
            check("the stored deepseek pair is offered",
                  "deepseek-flash \u00b7 deepseek-flash" in screen, screen[-800:])
            check("the stored minimax pair is offered",
                  "minimax \u00b7 MiniMax-M3.1-Flash" in screen, screen[-800:])
            check("no phantom (flags) entry is offered for removal",
                  "(flags)" not in screen, screen[-800:])

            # Removing the selected pair deletes exactly that pair.
            session.send(b"\x1b[B")  # minimax row (second row in both layouts)
            session.wait(0.5)
            session.send(b"\r")
            session.wait(1.2)
            screen = session.screen()
            check("the removal asks for the selected pair",
                  "Remove model minimax \u00b7 MiniMax-M3.1-Flash?" in screen, screen[-800:])
            session.send(b"\r")
            session.wait(1.5)
            screen = session.screen()
            check("the removal succeeds",
                  "Removed model MiniMax-M3.1-Flash from endpoint minimax" in screen,
                  screen[-800:])
            check("the unrelated endpoint keeps its model",
                  ("deepseek-flash", "deepseek-flash") in models_of(path),
                  json.dumps(models_of(path)))
            check("the removed pair is gone",
                  ("minimax", "MiniMax-M3.1-Flash") not in models_of(path),
                  json.dumps(models_of(path)))
            cfg = stored(path)
            check("the active model is unchanged",
                  (cfg.get("active") or {}) == {"endpoint": "deepseek-flash", "model": "deepseek-flash"},
                  json.dumps(cfg.get("active")))
        finally:
            session.close()
    finally:
        os.environ.pop("HAL9001_MODEL", None)
        os.environ.pop("FLAGS_TEST_KEY", None)
        shutil.rmtree(root, ignore_errors=True)

    print()
    if FAILURES:
        print("%d FAILURES" % len(FAILURES))
        sys.exit(1)
    print("ALL MODEL FLAG PTY CHECKS PASSED")


if __name__ == "__main__":
    main()
