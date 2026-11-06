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

## Child process lifecycle

Every `run()` owns exactly the child that `pty.fork()` returned. On every
path — the app exiting, a script timeout, a failed action or a raised
exception — cleanup closes the pty descriptor, sends `SIGTERM` to that
child (and to its own process group when it is the group leader), waits a
bounded grace period, escalates to `SIGKILL` only if needed, and then
reaps the child with `waitpid()`.
Cleanup never scans for processes by name, so a concurrent harness and
the operator's interactive HAL session are never signalled.

The lifecycle regression suite uses a temporary fake HAL and temporary
home/store directories. It records the direct children and open
descriptors of the test runner before and after each scenario: normal
exit, assertion failure, exception, timeout, interrupted test, forced
termination, an unrelated session and two concurrent harnesses.

```sh
python3 tools/ptycheck/lifecycle_check.py -v
```

The help and suggestion checks own every path they use and can run
beside another suite without sharing fixtures:

```sh
go build -o /tmp/opencode/hal9001 ./cmd/hal9001
HAL9001_BINARY=/tmp/opencode/hal9001 \
python3 tools/ptycheck/help_checks.py
```

The permission checks start their own fake provider on port 8117 and own
their config, `HOME`, workspace, session store and audit file. They prove
the operator-only enable path and that 666 does not survive a conversation
change or a restart:

```sh
HAL9001_BINARY=/tmp/opencode/hal9001 \
python3 tools/ptycheck/permission_checks.py
```

The presentation checks own their config, home, workspace and a static
session with a verified tool ledger. They need no provider. They assert
display-cell alignment at 60x20, 80x24, 120x30 and 170x38, all five
themes, the red eye, tool and metadata order, slash dismissal and the
444/666 footer:

```sh
HAL9001_BINARY=/tmp/opencode/hal9001 \
python3 tools/ptycheck/polish_checks.py
```

The web-research checks own a fake OpenAI-compatible provider on port 8118,
a fake Firecrawl v2 server on 8123, a fake Exa server on 8124, a fake
DuckDuckGo Lite server on 8125, and their config, Home, workspace, session
store and audit file. They never read the operator's real keys:

```sh
HAL9001_BINARY=/tmp/opencode/hal9001 \
python3 tools/ptycheck/firecrawl_checks.py
```

The link checks own a static conversation fixture and a temporary
home/store. They need no provider and no graphical desktop:

```sh
HAL9001_BINARY=/tmp/opencode/hal9001 \
python3 tools/ptycheck/link_checks.py
```

The composer checks send a paste and render the first frame after it,
without sending an arrow key or any follow-up input. They cover bracketed
paste, a raw LF alternate path and a bracketed CRLF path:

```sh
HAL9001_BINARY=/tmp/opencode/hal9001 \
python3 tools/ptycheck/composer_checks.py
```

The tool-icon checks use a fixture session with historical tool evidence.
They check the default emoji rows, the classic opt-out, switching back,
the failure priority, the `✏️` variation selector, the icon gutter and all
five themes, including 80x9:

```sh
HAL9001_BINARY=/tmp/opencode/hal9001 \
python3 tools/ptycheck/tool_icon_checks.py
```

## What it covers

- The interface starts in the alternate screen and leaves it on exit,
  restoring the cursor and disabling bracketed paste.
- The header stays on the first row and the footer at the bottom at
  80x24, 120x40 and 160x50, and after a resize.
- `Enter` sends, `Ctrl+J` and `Alt+Enter` break the line, `Ctrl+S`
  still sends, `Ctrl+K` opens the model picker, `/help` opens on Enter
  and `Esc` closes it.
- `/help` is one vertical, grouped reference with fixed label and
  description columns, keeps its title and exit instruction fixed while
  the body scrolls, reaches the 666 permission reference and the
  `Ctrl+P`/`Ctrl+Y`/`Ctrl+N` controls at 60x20, and does not overflow on
  any theme. Slash suggestions are capped, aligned, filter as text is
  typed, shorten a long syntax inside its label column, and report how
  many results are above or below the visible window; scrolling reaches
  `/skills` and `/skill`.
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
- The presentation suite checks a label-free transcript: user and
  assistant text share one display column, the assistant eye sits in its
  own two-cell gutter, wrapped Markdown/code/tables stay inside the
  usable width at 60x20, 80x24, 120x30, 170x38 and 200x45, tool activity and
  right-aligned statistics sit beneath the answer, and all five themes
  render the eye in colour. It also covers slash Esc dismissal and the
  444/666 footer.
- The web-research suite runs the unified `web_search` path and the
  Firecrawl scrape path. It checks Firecrawl success, empty results,
  Firecrawl -> Exa fallback, Exa -> DuckDuckGo fallback, malformed-primary
  fallback, all-provider failure, missing keys, provider attribution,
  scrape metadata and truncation, invalid scrape URLs, prompt injection,
  compact activity lines, `/diag`, `/help`, the 444 footer, the audit trail
  and session resume. Every check uses local fake servers; no live credits
  are spent.
- The link suite verifies OSC 8 destinations, label-only Markdown links,
  visible bare URLs, numbered lists, tables, unsafe-scheme rejection,
  fenced-code exclusion and plain reconstructed screen text.
- The composer PTY suite covers two, three and five-line pastes, a
  six-line paste whose caret window is the last five rows, a long wrapped
  Markdown link followed by a newline, and the footer remaining visible
  immediately after the paste. It also renders the exact two-line field
  report on 8, 9 and 10-row terminals, before and after Up, plus a
  five-line and a ten-line paste at 80x9. The first frame is asserted
  before any key is sent; the alternate raw-LF and bracketed-CRLF paths
  are checked at the same short size.
- The tool-icon suite covers the emoji default, `/tool-icons classic` and
  `/tool-icons emoji`, the no-argument status, historical evidence,
  failure and destructive priority, expected failures (📍) versus
  unexpected failures (🚨), the unknown fallback, `✏️`'s variation
  selector, the shared icon gutter, 80x9 and all five themes. The
  permission suite also executes the expected read/bash cases and resumes
  their verified evidence.
- The permission suite runs the lifecycle end to end: 444 is the default;
  a model-invented `read` is refused; conversation text and a plain `y` do
  not enable 666; `Ctrl+P` then `Ctrl+Y` enables a red `666 UNRESTRICTED`
  footer; `read` and `bash` run outside the workspace and return their
  results; `Ctrl+P` then `Ctrl+N` returns to 444 and a later read is
  refused; `/clear` and a restart start in 444. Its audit checks prove the
  tool records carry mode and exit status but never file contents, shell
  command text, or a missing command hash.
- A `[[save PATH]]` prompt makes the stub propose a write: the review shows
  the destination, create-versus-overwrite, size, digest and contents, and
  `666 REQUESTED`; nothing is written before approval. `Ctrl+Y` saves exactly
  the reviewed bytes and returns to 444; `Ctrl+N` and `Esc` reject; typed
  text never decides it; paths outside the workspace are refused; overwrites
  are shown as such; and a second write needs its own approval.
  `[[saveas PATH]]` uses the fixed `HAL444_WRITE_OK` body for the 444
  workspace-path check: `~/HAL/...` writes directly to the requested file,
  never to `~/HAL/HAL/...`; rejection, outside paths and symlink escapes
  remain refused.
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
| `lifecycle_check.py` | process-lifecycle regression checks for the driver |
| `termsim.py` | replays the byte stream into a screen buffer |
| `checks.py` | the shared assertions |
| `help_checks.py` | isolated help and suggestion checks with their own temporary config, home, store and sessions |
| `permission_checks.py` | isolated 444/666 enable, execution, disable, reset and audit checks with their own provider |
| `polish_checks.py` | isolated transcript alignment, eye, tool/metadata and slash-dismissal checks with a static ledger fixture |
| `fake_provider.py` | a local OpenAI-compatible streaming stub (`[[save]]`, `[[read]]`, `[[bash]]`) |
