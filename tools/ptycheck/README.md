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

The heartbeat checks start their own fake provider on port 8127 and own
their config, home, workspace and session store. They verify the stationary
red marker, independently moving frames, `/animation off`/`on`, the
five-second silence suffix, completion, 80x9 and all five themes:

```sh
HAL9001_BINARY=/tmp/opencode/hal9001 \
python3 tools/ptycheck/heartbeat_checks.py
```

The attachment checks start their own fake provider on port 8129 and own
their config, home, workspace, session store and source files. They verify
that `/attach` copies the chosen file into `~/HAL/uploads/`, that the
pending row names it above the composer, that the file's text reaches the
model as untrusted data inside a delimited block, that the session keeps a
reference and not the content or the operator's own path, that `/detach`
takes the file back without deleting the copy, that a missing file and a
directory are refused, and that 80x9 stays exactly nine rows:

```sh
HAL9001_BINARY=/tmp/opencode/hal9001 \
python3 tools/ptycheck/attachment_checks.py
```

The model management checks start the built binary against a
pre-version-2 configuration file in a temporary home, session store and
workspace, with no provider behind it. They prove that the startup
migration keeps the endpoint, the credential, the models, the theme and
the active session, that `/model list` shows the endpoints and flags the
models that need a review, that `/model add` stores a model under its
endpoint without changing the active model, that `/model use` persists
the selection, that removing the active model needs an explicit
replacement, that `/endpoint list` and `/endpoint add` work, and that
`/endpoint remove` refuses to remove the active endpoint without a
replacement, refuses a replacement inside the removed endpoint, removes
it with a replacement from another endpoint, and clears the selection
when the last endpoint goes away:

```sh
go build -o /tmp/opencode/hal9001 ./cmd/hal9001
HAL9001_BINARY=/tmp/opencode/hal9001 \
python3 tools/ptycheck/model_management_checks.py
```

The discovery checks start their own hostile provider on port 8263 and own
their config, home, workspace and session store. The provider answers with
a model catalogue that carries every trick a broken or hostile endpoint can
use: an identifier that is an ANSI colour sequence, an identifier with
surrounding whitespace, the same identifier twice, an identifier with an
embedded newline, an identifier with a bell, an identifier that is far too
long, and a display name that tries to rewrite the screen and to forge a
row. The suite asserts on the raw byte stream, not on the rendered screen:
an escape sequence that reaches the terminal is an escape sequence, whether
or not the emulator happened to draw something sensible:

```sh
HAL9001_BINARY=/tmp/opencode/hal9001 \
python3 tools/ptycheck/discovery_checks.py
```

The budget checks start their own recording provider on port 8265 and own
their config, home, workspace and session store. The provider writes every
request it receives to a log, so the request that actually leaves the
machine can be inspected. A conversation of forty-one messages is stored
before the run: without an operator limit the conversation is still bounded
by the provisional total, the wire carries the provisional output cap and
the interface says that the budget is provisional; with a configured window
and answer cap the transmitted request fits the room that is left of the
window, the wire carries the configured cap and the newest request is kept;
with a hundred-token window nothing is transmitted at all — the request
cannot be reduced, so it is refused with an explanation instead of being
truncated; and with an answer cap that fills the whole window the two
numbers contradict each other, so the contradiction is reported instead of
being guessed at:

```sh
HAL9001_BINARY=/tmp/opencode/hal9001 \
python3 tools/ptycheck/budget_checks.py
```

The steering checks start their own provider on port 8131 and own their
config, home, workspace and session store. The provider can hold a turn
open, so the operator has time to write while HAL is working, and it records
every request it receives, so the request that actually left the machine can
be inspected. The suite asserts that a message written while HAL is working
waits in the queue and says so, that the request which was already sent is
never rewritten — neither the queued message nor the steering instruction is
in it — that the waiting messages are sent when the turn ends in the order
they were written, that a steering instruction leads the next turn, that
`/queue` shows the states, and that `/queue clear` removes what is waiting
without sending it:

```sh
HAL9001_BINARY=/tmp/opencode/hal9001 \
python3 tools/ptycheck/steering_checks.py
```

## What it covers

- The interface starts in the alternate screen and leaves it on exit,
  restoring the cursor and disabling bracketed paste.
- The header and assistant blocks carry the stationary two-cell `🔴`
  marker; the activity row advances through the 24 clock frames while a
  provider response is quiet, shows `no new data for Ns` after five
  seconds, and stops at completion or cancellation. `/animation off`
  freezes it to `🕛`, `/animation on` resumes it, and 80x9 stays exactly
  nine rows.
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
  assistant text share one display column, the two-cell assistant eye sits
  in its own three-cell gutter, wrapped Markdown/code/tables stay inside
  the usable width at 60x20, 80x24, 120x30, 170x38 and 200x45, tool activity and
  right-aligned statistics sit beneath the answer, and all five themes
  render the eye in colour. It also covers slash Esc dismissal and the
  444/666 footer.
- The web-research suite runs the unified `web_search` path and the
  Firecrawl scrape path. It checks Firecrawl success, empty results,
  Firecrawl -> Exa fallback, Exa -> DuckDuckGo fallback, malformed-primary
  fallback, all-provider failure, missing keys, provider attribution,
  scrape metadata and truncation, invalid scrape URLs, prompt injection,
  compact activity lines, `/diag`, `/help`, the 444 footer, the audit trail
  and session resume. Its model declares a context window of its own: the
  suite scrapes a page that is larger than any 8k window holds, and the
  point of the check is that the page is truncated and its size reported,
  not that the request fits a window nobody declared. Every check uses
  local fake servers; no live credits are spent.
- The attachment suite covers the operator path end to end: `/attach`
  copies the chosen file into `~/HAL/uploads/` in a directory of its own,
  the original file is unchanged, the pending row names the file and its
  size above the composer, `/attachments` lists it, `/detach` takes it
  back while the copy stays, a missing file and a directory are refused,
  the file's text reaches the model marked as untrusted data inside a
  block delimited by a per-request token, the session record holds the
  reference and not the content or the operator's own path, and 80x9
  stays exactly nine rows.
- The link suite verifies OSC 8 destinations, label-only Markdown links,
  visible bare URLs, numbered lists, tables, unsafe-scheme rejection,
  fenced-code exclusion and plain reconstructed screen text.
- The discovery suite runs the real binary against a provider that answers
  with a hostile model catalogue: identifiers that are escape sequences,
  identifiers with surrounding whitespace, with an embedded newline or a
  bell, an identifier that is far too long, and a duplicate. The raw byte
  stream carries no injected colour sequence and no injected hyperlink, no
  row is forged, the refused identifiers are not listed, a legitimate
  identifier survives exactly, the sanitised name is shown on one line, and
  the conversation still works afterwards.
- The budget suite runs the real binary against a provider that records
  every request: without an operator limit the stored conversation is
  still bounded by the provisional total, the wire carries the
  provisional output cap and the interface says that the budget is
  provisional; with a configured window and answer cap the transmitted
  request fits the room that is left of the window, the wire carries the
  configured cap and the newest request is kept, the interface says that
  older context was dropped to fit; with a hundred-token window nothing
  is transmitted and the refusal explains itself; and with an answer cap
  that fills the whole window the contradiction is reported with both
  numbers.
- The steering suite runs the real binary against a provider that holds a
  turn open and records every request: a message written while HAL is
  working waits in the queue and is shown as waiting, the request that was
  already sent is never rewritten, the waiting messages are sent when the
  turn ends in the order they were written, a steering instruction leads
  the next turn, `/queue` shows the states, and `/queue clear` removes
  what is waiting without sending it.
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
| `fake_provider.py` | a local OpenAI-compatible streaming stub (`[[save]]`, `[[read]]`, `[[bash]]`, `[[attached]]`) |
| `attachment_checks.py` | isolated attachment checks: safe copies, the pending row, untrusted content and session references |
| `hostile_provider.py` | a local provider whose model catalogue carries hostile identifiers and labels |
| `discovery_checks.py` | isolated discovery-hygiene checks: no injected escape sequence, no forged row, exact identifiers survive |
| `recording_provider.py` | a local provider that records every request it receives |
| `budget_checks.py` | isolated context-budget checks: the transmitted request honours the operator's limit, or nothing is sent |
| `steering_provider.py` | a local provider that holds a turn open and records every request it receives |
| `steering_checks.py` | isolated steering and queue checks: the request already sent is not rewritten, the waiting messages keep their order |
