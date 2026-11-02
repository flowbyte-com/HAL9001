# Pseudo-terminal checks

Unit tests cannot prove how the interface behaves in a real terminal.
This harness runs the built binary inside a forked pseudo-terminal,
sends scripted key presses and window resizes, and replays the captured
byte stream through a small terminal emulator to reconstruct the screen
the user would see.

## Running

```sh
# 1. A local provider that answers with a streamed reply.
python3 tools/ptycheck/fake_provider.py &

# 2. A configuration pointing at it.
cat > /tmp/hal9001-test.json <<'EOF'
{"provider":"openai-compatible","endpoint":"http://127.0.0.1:8099/v1",
 "api_key":"test-key","models":[{"id":"MiniMax-M3.1-Flash"}],
 "active_model":"MiniMax-M3.1-Flash","theme":"Midnight"}
EOF

# 3. The binary and the checks.
go build -o /tmp/opencode/hal9001 ./cmd/hal9001
mkdir -p /tmp/opencode/halhome/sessions
HAL9001_BINARY=/tmp/opencode/hal9001 \
HAL9001_CONFIG=/tmp/hal9001-test.json \
HAL9001_HOME=/tmp/opencode/halhome \
python3 tools/ptycheck/checks.py
```

Every check prints `ok` or `FAIL`; the exit status is non-zero when
something fails.

## What it covers

- The interface starts in the alternate screen and leaves it on exit,
  restoring the cursor and disabling bracketed paste.
- The header stays on the first row and the footer at the bottom at
  80x24, 120x40 and 160x50, and after a resize.
- `Enter` sends, `Ctrl+J` and `Alt+Enter` break the line, `Ctrl+S`
  still sends, `Ctrl+K` opens the model picker, `/help` opens on Enter
  and `Esc` closes it.
- A bracketed paste stays in the composer and never submits.
- The mouse is never captured, so native selection keeps working.

## Files

| File | Purpose |
|---|---|
| `pty_drive.py` | forks the pty, drives the binary, returns the raw bytes |
| `termsim.py` | replays the byte stream into a screen buffer |
| `checks.py` | the assertions |
| `fake_provider.py` | a local OpenAI-compatible streaming stub |
