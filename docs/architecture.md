# HAL9001 Architecture

HAL9001 is a conversational thinking partner with permanent 444
read-only authority. This document describes the new native foundation
(root Go module `github.com/flowbyte-com/HAL9001`). The legacy
DeepClaw/PicoClaw design is preserved untouched in `legacy_base/`.

## Identity

One application, one identity: HAL9001. There are no personas to switch,
no coding profiles, and no execution modes. `internal/persona` holds the
conversational contract (chat-first, intellectually honest, memory
selective, prompt craft) and the Dave Protocol: provider safety refusals
render exactly `I'm sorry Dave, I can't do that.`, detected only from
explicit provider signals — never broad refusal-text matching.

## The 444 boundary as design

The legacy system inherited a general-purpose tool registry and
restricted it afterward. The new architecture inverts this: capabilities
are explicit from the start (`internal/capability`), and anything not
allowed is refused by default-deny. The engine (`internal/engine`)
enforces the boundary on every provider-requested operation; the TUI
has no authority of its own.

Allowed today: conversation, read-only investigation (`web_search`,
`web_fetch`, `read_file`, `find_skills`), prompt creation
(`draft_prompt`), and review (`review_prompt`, `review_report`).
Memory admission and future authorized delegation are separate,
narrowly scoped operations — not generic write permissions.

## Layers

```text
cmd/hal9001      flags/env/file config, session store, TUI launch
internal/tui     chat, onboarding, model picker, streaming display (no logic)
internal/engine  RespondStream(): store user msg → streamed provider → 444 gate → store reply
internal/session JSONL conversation storage (model-independent)
internal/provider OpenAI-compatible streaming client, contracts, mock
internal/memory  record tiers, admission validation, Store interface
internal/config  three-value onboarding, user config file (0600)
internal/persona identity, conversational contract, Dave Protocol
internal/capability 444 allowlist
```

Conversation flows downward only: the TUI calls the engine, the engine
calls the provider and the session store. Nothing calls upward. The
engine never fabricates replies: without a provider it stores the user
message and reports `ErrNoProvider`, which the TUI shows as a status
notice — never as an assistant message. Cancellation aborts the HTTP
request and stores nothing from the assistant side, so an interrupted
stream can never become a complete answer. Reasoning metadata returned
by the API is captured separately and never displayed or stored as the
answer.

The engine prepends the persona system instruction to every request. It
is never stored in the session, keeping history provider-independent
and the instruction foundation byte-stable. Assistant messages are
persisted only after successful generation.

## Terminal interface

Full-screen chat on the terminal's alternate screen buffer: the shell
output is hidden while HAL runs and the previous screen is restored on
exit. The frame is assembled as exactly one row per terminal row, so the
terminal never scrolls.

Layout: a one-line `● HAL9001` header, a rule, the scrollable Markdown
conversation, exactly one blank row, an optional command suggestion
panel, a bordered composer with one text row by default (growing to
five), a one-line status footer (`444 READ ONLY · /help` left, model and
effort right) and a notice line. A `chatFrame` resolves that geometry
once per size change; on short terminals it gives up the suggestion
panel, the separator, the notice, the rules and finally the border —
never the header, the composer text or the footer. Overlays replace the
conversation region with their panel.

Scrolling is keyboard-driven (`PgUp`/`PgDn`, `Home`/`End`) with an
wheel: `View.MouseMode` is on unless `--no-mouse` or
`HAL9001_MOUSE=0` turns it off, so the pointer works out of the box and
the terminal keeps its own selection only when asked.
Wheel events are routed by pointer position to the viewport (which owns
no keys of its own), the composer (a long draft scrolls in place) or the
open panel, and manual scrolling follows the same bottom-follow rules as
the keyboard. Page keys are matched by key code rather than by their
string form, so a terminal that reports Fn+PageUp as `shift+pgup` still
pages. The reading position is anchored to an entry and a
row inside it, so resizing or re-wrapping Markdown restores the same
content rather than a stale line number.

With mouse reporting on, HAL owns the pointer. A left press starts a
selection, the drag extends it, and the release copies it through OSC 52
— the same write-only path `/copy` uses, so the model never sees it. The
selection lives in conversation coordinates, not screen coordinates, so
scrolling the history does not move it; it is dropped when the
conversation changes or the terminal is resized.

A drag that reaches the top or the bottom of the conversation scrolls
the history by itself, one row per tick, so a selection can reach text
that was never on screen. The scroll is driven by the event loop: a tick
moves the conversation and asks for the next tick, so nothing runs in
the background and the chain ends with the selection. The pointer's
position at the edge decides the direction, the selection follows the
content under the pointer, and the wheel takes over from the auto-scroll
while a drag is running.

Copying needs a map from screen cells back to text, and the screen shows
wrapped, styled, padded lines. `internal/tui/cells.go` scans a rendered
row into grapheme clusters and records the cell boundaries and the
origin of every byte, so a cell can be traced back to a character and a
selection can be drawn in reverse video at exactly the right place.
`internal/tui/selection.go` maps the conversation onto its *logical*
lines — the unwrapped text — by rendering each reply a second time
without wrapping and matching the rendered rows against those lines. A
selection therefore copies the text as it was written: a wrapped
paragraph stays one line, a word the renderer had to break comes back
whole, and the interface labels are never part of it.

The composer keeps its own window. The bubbles textarea pins its view to
the cursor on every update, which makes the wheel fight the caret, so
HAL renders the whole draft through the textarea and shows the slice it
wants. The height of the draft and the row of the cursor come from a
copy of the textarea's own soft wrap (`internal/tui/composer.go`), so the
rows HAL counts are the rows the textarea draws. The wheel moves the
window and never reaches the textarea; typing pulls the window back to
the caret.

That window is only an abbreviation. The textarea is configured without
its own maximum height, so it renders every row of the draft from the
first one and never scrolls internally; HAL's five-row window is the only
scroll. The textarea's default maximum (99 rows) made it scroll its own
viewport on a long paste, and the window HAL sliced out of the scrolled
view could land past the end — a blank composer over a buffer that held
the whole draft.

Status notices live in one row under the footer that is always part of
the frame, so a notice appearing or expiring never shifts anything. A new
notice replaces the old one, and every notice is put on a one-shot timer;
the timer carries the notice's generation, so the expiry of a notice that
has already been replaced is ignored. A notice set while a turn is in
flight is the turn's live status, refreshed every second and cleared when
the turn ends, so it does not expire on its own.

Short conversations are bottom-aligned in the presentation layer: the
viewport pads its rendered rows above the content, leaving the viewport
offsets untouched. Collapsed prompts take at most a third of the
viewport, computed from `chatFrame.viewport` and recomputed on resize.

Themes are one semantic palette (background, text, accent, muted) applied
to every surface: the view sets the terminal background and foreground
(restored on exit), Lip Gloss styles the chrome, and glamour receives a
StyleConfig derived from the same palette so Markdown follows the theme
too. The composer's textarea receives the palette as well — its own
defaults paint the cursor line with a black band, which would swallow the
text on a light theme. Selecting a theme re-renders cached entries; the
picker previews by swapping the active theme and restoring it on `Esc`.

Every command lives in one table with optional aliases; dispatch,
autocomplete and the help panel read it. `/q`, `/quit` and `/exit` are
aliases of one quit command that shares `Ctrl+C`'s shutdown path.

Typing `/` opens a suggestion panel built from the same command table the
dispatcher uses, so the two can never drift apart.

Views: chat, onboarding, model picker, theme picker (five presets with
live preview, persisted), help, and stored sessions. A slash dispatcher
handles `/help`, `/copy`, `/usage`, `/rename`, `/theme`, `/model`,
`/effort`, `/new`, `/clear`, `/sessions`, `/diag`, `/expand`, `/collapse`
and the `/quit` family locally — commands never reach the model, and `//` escapes
to a literal slash message. `/copy` writes the last response (or its
latest fenced block) via OSC 52; the mouse is never captured unless the operator enables
it, so native selection and right-click paste keep working by default. Pasted text (bracketed or
keystroke) is inserted with newlines preserved and never submitted.
Scrolling is sticky: streams follow only when already at the bottom.

Keyboard: `Enter` sends, `Ctrl+J` (and `Alt+Enter`, and `Shift+Enter`
where the terminal reports modifiers) inserts a newline. `Ctrl+M` is
the same byte as `Enter` on a terminal, so the model picker uses
`Ctrl+K`. The key ambiguity is resolved from measured terminal
behaviour, not assumption: the byte sequences were decoded with the
input parser the application itself uses. See
`docs/terminal-support.md` for the tested/unsupported matrix.

Conversation entries keep the original text and a cached rendered form.
Long user prompts render as a four-line preview with an expand hint
while HAL writes the answer, and open again when the answer is there;
the collapse never touches the stored text or the provider request. The
entry keeps three presentation states — shown, collapsed by hand, and
collapsed for the running turn — so the automatic expansion that follows
an answer never overrides a choice the operator made, and the temporary
state is never stored with the conversation.

## Streaming

A turn is bounded three ways: the caller's context (cancellation), an
idle watchdog (no data for the configured window) and a total ceiling.
Each bound closes the HTTP request, so an abandoned turn stops
generating instead of running on. Requests always carry an output cap
(`max_tokens`), from the model or the configured default — a reasoning
model must never be allowed to generate without one.

Reasoning effort is per-model and capability-gated: the OpenAI-compatible
adapter sends `reasoning_effort` only for model ids in its documented
table (the MiniMax M-series), and only for one of the five documented
levels. The application resolves a level from the per-model record, or
`medium` for a capable model with none recorded. `/effort` changes it and
rebuilds the engine, so the next request carries the new value.

`Ctrl+U` clears the composer into a one-slot undo buffer and `Ctrl+Z`
restores it into an empty composer; the buffer is dropped on submit and
on every session change so drafts never leak across conversations.

A turn that completes without visible answer text is not stored as an
assistant message: `ErrNoVisibleAnswer` is returned, the interface shows a
diagnostic note, and `/diag` reports the finish reason and token usage.
Nothing retries and nothing raises the cap implicitly.

`/clear` and `/new` share one path: cancel any live turn, ask the
application for a new session store, reset the visible conversation. The
previous session file is never deleted and stays listed by `/sessions`.

The active conversation is recorded in the user configuration
(`active_session`) whenever a session starts or is selected, and resolved
at startup as: explicit `--session`, then the recorded session (honoured
even when its file does not exist yet), then the most recently modified
stored conversation for installations that predate the metadata, then the
default empty session. A corrupt or unsafe name is ignored rather than
crashing or silently reopening the earliest conversation.

Token accounting lives in `internal/usage` as a per-session record in
`usage.json`: input, output, reasoning and cached-input totals, completed
and incomplete turn counts and the last turn. Only provider-reported
numbers are stored — reasoning tokens are kept separate because they are
already inside the completion total, and a turn without reported usage
increments the incomplete count instead of inventing tokens.

`provider.TurnStats` records content-free telemetry per turn: status,
elapsed time, time to first visible output and to first reasoning event,
SSE event and keep-alive counts, visible and reasoning chunk/byte counts,
`finish_reason`, output-limit flag and token usage. `/diag` renders it.
Reasoning text is parsed only to detect that reasoning happened; it is
never displayed, stored or logged.

The engine reports reasoning **presence** and final stats as events
(`RespondStreamEvents`), so the interface can show `thinking…` without
ever seeing the reasoning. `RespondStream` remains as the text-only
wrapper.

See `docs/streaming-investigation.md` for the evidence behind these
decisions and the hypotheses that remain open.

## Conversation continuity

`internal/session` persists one JSONL file per session. History is
model-independent: switching providers keeps the discussion. Corrupt
lines fail loudly instead of being absorbed into context.

## Memory

`internal/memory` defines the durable tiers — spark, developing,
decision, review — with provenance on every record and a sensitive-data
exclusion that rejects credentials at any tier. Admission itself
requires an explicit operator gate above this package; no implementation
here writes anything durable except the conversation store, and MPM
remains the intended long-term backend for a later milestone.

## What is deliberately absent

Model provider adapters, web research execution, Prompt Workshop,
report assessment, MPM recording, and multi-model switching UI. See
`docs/migration-inventory.md` for what migrates next, and in what order.
