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
- A short single-line paste stays one row; a CRLF paste is one line break,
  not two; a pasted line that wraps takes exactly its wrapped rows.
- A paste of 2, 5, 20, 100 or 150 lines is visible in the five-row
  composer window, with the caret on its last row; Unicode, Markdown, a
  long wrapped line and trailing newlines survive; and `Enter` stores the
  complete draft, not the visible rows.
- `Ctrl+U` collapses the composer to one empty row and says `Draft cleared
  · Ctrl+Z to restore`; `Ctrl+Z` restores the whole draft at its height
  and says `Draft restored`. A notice replaces the previous one and
  disappears after its interval.
- Typing `/` opens the command panel, typing filters it, `Tab`
  completes, `Esc` dismisses it and keeps the draft.
- The composer is two full-width rules with one text row by default: no
  side borders, no corners and no placeholder, and the caret is visible
  in the empty composer. Exactly one hint row separates its bottom rule
  from the divider above the footer.
- `Home`/`End`/`PgUp`/`PgDn` navigate a long stored conversation, and
  shrinking the terminal keeps the reading position. Page keys work with
  modifier bits too (`shift+pgup`, as a laptop Fn key reports them).
- All five themes set their own terminal background (OSC 11) and a
  distinct foreground palette.
- A short conversation sits just above the composer; a reopened long
  prompt is shown in full, because the abbreviation that belonged to a
  finished turn is not stored.
- A long prompt is abbreviated while HAL answers and opens again when
  the answer is there, and the answer stays in view.
- `/q`, `/quit` and `/exit` leave the alternate screen and restore the
  terminal colours and cursor.
- The active conversation survives `/clear` and a relaunch, an empty new
  session stays selected, and `/sessions` recovers the previous one.
- A `[[save PATH]]` prompt makes the stub propose a write: the review shows
  the destination, create-versus-overwrite, size, digest and contents, and
  `666 REQUESTED`; nothing is written before approval. `Ctrl+Y` saves exactly
  the reviewed bytes and returns to 444; `Ctrl+N` and `Esc` reject; typed
  text never decides it; paths outside the workspace are refused; overwrites
  are shown as such; and a second write needs its own approval.
- The review keeps its three zones at 80x24, 120x30 and 170x38: the header
  names the operation, the document scrolls, and the decisions
  (`Ctrl+Y Approve · Ctrl+N Reject · ↑/↓ Scroll · Esc Reject`) are on screen
  at the beginning of a long document, in the middle of it and at its end.
  The wheel scrolls the document and leaves the decisions alone, a resize
  keeps all three zones, and a long document is approved byte for byte or
  rejected without a byte written.
- The skill checks run against a temporary MPM workspace holding the two
  shipped skills, so they never depend on the operator's own store. The stub
  names the skill it finds in the system instruction, so the checks can see
  which guidance actually reached the model: an email request reaches
  `humanize-text`, an agent prompt reaches `system-prompt-writer`, an
  ordinary question reaches neither, `/skills` lists the catalogue, and the
  reply carries no routing instruction.
- The footer row is contextual: the permission line on an empty composer,
  `Enter send · Ctrl+J newline · Ctrl+U clear · /help` while there is a
  draft, and `Ctrl+Z restore · /help` after `Ctrl+U` — on every theme and
  shortened, not clipped, on a narrow terminal.
- The footer and `/usage` report provider token usage; each finished
  answer carries a metadata line and the composer starts at one row.
- The wheel scrolls symmetrically; with `--no-mouse` no mouse reporting
  (`?1002h`) is requested and native selection is untouched.
- The mouse modes are reset when HAL exits, so the terminal is left as
  it was found — after a clean exit and after `SIGINT`.
- A left drag over the conversation copies the selected text through
  OSC 52: the payload is the reply's own text, without labels, colours
  or escape sequences. The text is marked while the drag is running.
- A drag that reaches the top or the bottom of the conversation scrolls
  the history on its own, and the copy reaches the text that was never
  on screen.
- `--mouse` with `--no-mouse` is refused, and a malformed
  `HAL9001_MOUSE` is refused with a message that names the variable —
  unless a flag has already decided.
- The wheel over the composer scrolls the draft and leaves the caret
  where it was; the conversation does not move.

## A note on resizes

A terminal that *shrinks* during a run is replayed faithfully. A terminal
that *grows* is not: the renderer keeps the scrolling region of the old
height, so the emulator scrolls the frame when the taller frame is
written. The application is not at fault — the chat view behaves the same
way — so the checks resize downwards, and a growing terminal is verified
by hand in a real terminal.

## Files

| File | Purpose |
|---|---|
| `pty_drive.py` | forks the pty, drives the binary, returns the raw bytes |
| `termsim.py` | replays the byte stream into a screen buffer |
| `checks.py` | the assertions |
| `fake_provider.py` | a local OpenAI-compatible streaming stub |
