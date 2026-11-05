#!/usr/bin/env python3
"""Headless PTY checks for OSC 8 hyperlinks.

The suite owns its config, home, store and session fixture. It needs no
provider and no graphical desktop: the rendered bytes are checked directly
and the screen is reconstructed with the same terminal emulator the other
PTY suites use.

Usage:

    go build -o /tmp/opencode/hal9001 ./cmd/hal9001
    HAL9001_BINARY=/tmp/opencode/hal9001 python3 tools/ptycheck/link_checks.py
"""
import json
import os
import shutil
import sys
import tempfile

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, HERE)
import pty_drive  # noqa: E402
from termsim import render  # noqa: E402

FAILURES = []
BINARY = os.environ.get("HAL9001_BINARY", "/tmp/opencode/hal9001")
SESSION = "links"

FIXTURE = """\
[Article title](https://example.com/article) and https://example.org/bare.

1. [First item](https://example.com/1)
2. [Second item](https://example.com/2)

| Site | Link |
|---|---|
| Docs | [Documentation](https://example.com/docs) |

[unsafe](javascript:alert(1))

```text
[code link](https://example.com/code)
https://example.com/code
```
"""


def check(name, ok, detail=""):
    print(("[ok  ] " if ok else "[FAIL] ") + name + ("" if ok else "  " + str(detail)))
    if not ok:
        FAILURES.append(name)


def write_fixture(home, store, config):
    with open(config, "w") as fh:
        json.dump({
            "provider": "openai-compatible",
            "endpoint": "http://127.0.0.1:9/v1",
            "api_key": "test-key",
            "models": [{"id": "MiniMax-M3.1-Flash"}],
            "active_model": "MiniMax-M3.1-Flash",
            "theme": "HAL Classic",
        }, fh)
    with open(os.path.join(store, SESSION + ".jsonl"), "w") as fh:
        for item in [
            {"role": "user", "content": "show links",
             "ts": "2026-11-05T10:00:00Z", "turn_id": "turn-1"},
            {"role": "assistant", "content": FIXTURE,
             "ts": "2026-11-05T10:00:01Z", "turn_id": "turn-1"},
        ]:
            fh.write(json.dumps(item) + "\n")


def main():
    if not os.path.isfile(BINARY):
        print("missing HAL9001 binary: %s" % BINARY)
        return 2
    base = os.environ.get("HAL9001_PTY_TMPDIR", "/tmp/opencode")
    os.makedirs(base, exist_ok=True)
    home = tempfile.mkdtemp(prefix="hal9001-links-", dir=base)
    try:
        config_dir = os.path.join(home, "config")
        store = os.path.join(config_dir, "sessions")
        workspace = os.path.join(home, "HAL")
        os.makedirs(store, exist_ok=True)
        os.makedirs(workspace, exist_ok=True)
        config = os.path.join(config_dir, "config.json")
        write_fixture(home, store, config)

        pty_drive.BINARY = BINARY
        pty_drive.CONFIG = config
        pty_drive.HOME = config_dir
        pty_drive.STORE = store
        os.environ["HAL9001_MPM_BIN"] = os.path.join(home, "missing-mpm")
        raw = pty_drive.run([(2.5, b"")], cols=120, rows=40,
                            fresh=False, session=SESSION)
        screen = render(raw, 120, 40).text()

        for href in (
            "https://example.com/article",
            "https://example.org/bare",
            "https://example.com/1",
            "https://example.com/docs",
        ):
            check("OSC 8 destination present: " + href,
                  (";" + href + "\x07").encode() in raw, screen)

        for label in ("Article title", "First item", "Documentation"):
            check("label visible: " + label, label in screen, screen)
        for href in ("https://example.com/article", "https://example.com/docs"):
            check("labelled destination not repeated: " + href,
                  href not in screen, screen)
        check("bare URL remains visible",
              "https://example.org/bare" in screen, screen)

        check("unsafe scheme is not clickable",
              b";javascript" not in raw and "unsafe" in screen, screen)
        check("fenced code URL is not clickable",
              b";https://example.com/code\x07" not in raw, screen)
        check("fenced code remains readable",
              "https://example.com/code" in screen, screen)
        check("no raw control characters in the screen",
              "\x1b" not in screen and "\x07" not in screen, screen)
    finally:
        shutil.rmtree(home, ignore_errors=True)

    print()
    if FAILURES:
        print("FAILED: " + ", ".join(FAILURES))
        return 1
    print("ALL LINK PTY CHECKS PASSED")
    return 0


if __name__ == "__main__":
    sys.exit(main())
