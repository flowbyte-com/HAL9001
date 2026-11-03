# HAL9001

HAL9001 is a conversational thinking partner that investigates, architects,
coordinates, manages, reviews, reports, records and suggests. It creates
through prompts that other agents implement.

HAL itself never implements.

Its operating principle is **444: read-only authority, permanently enforced.**
There are no coding profiles, execution modes, or switches that can turn HAL
into an implementation agent.

## Status

Milestone 003.1: the native application holds live conversations through
a fullscreen terminal interface — alternate screen, persistent header and
footer, `Enter` to send, safe multiline input, five themes, clipboard
commands, and persistent model-independent sessions. Web research, Prompt
Workshop, review workflow, and MPM memory recording arrive in later
milestones.

The former DeepClaw/PicoClaw implementation is preserved for reference in
[`legacy_base/`](legacy_base/) (tag `pre-migration-base`). The new module
does not import it.

## Quick start

```sh
go build ./...
./hal9001 --help
```

On first launch HAL9001 asks for three values: provider endpoint, model
name, and API key. They are stored in `~/.hal9001/config.json` (mode
`0600`); the environment (`HAL9001_ENDPOINT`, `HAL9001_MODEL`,
`HAL9001_API_KEY`) overrides the file and is never written back.
A connection test runs against the provider's model list before saving;
you may save anyway if it fails.

```sh
hal9001                                    # chat (Enter sends, Ctrl+J adds a line)
hal9001 --session work                     # resume or start a named session
hal9001 --list-sessions                    # list stored conversations
hal9001 --no-mouse                         # leave the pointer to the terminal
hal9001 --provider openai-compatible \
  --endpoint https://api.minimax.io/v1 \
  --model MiniMax-M3.1-Flash               # flags win over the file
```

## The interface

HAL9001 takes over the terminal with the alternate screen buffer: the
shell output is hidden while HAL runs and the previous screen comes back
when it exits. A fixed header (`● HAL9001`) sits at the top, the
conversation scrolls in the middle, and the composer and the status
footer stay at the bottom.

| Key | Action |
|---|---|
| `Enter` | Send the message |
| `Ctrl+J` | Newline — works on every terminal |
| `Alt+Enter` | Newline — xterm-style terminals |
| `Shift+Enter` | Newline — only where the terminal reports modifiers |
| `Ctrl+S` | Send (alternative) |
| `Ctrl+K` | Model picker |
| `Ctrl+N` | New conversation |
| `Ctrl+C` | Quit |
| `Esc` | Cancel a stream / close a panel |
| `Ctrl+O` | Expand or collapse the last long prompt |
| `PgUp` / `PgDn` | Scroll the conversation by a page |
| `Home` / `End` | Jump to the start / end of the conversation |
| `Ctrl+U` | Clear the entire draft (Ctrl+Z restores it) |
| `Ctrl+Z` | Restore the last cleared draft |
| mouse wheel | Scroll history, or the draft |
| left drag | Select conversation text and copy it |

`Enter` and `Ctrl+M` are the same byte on a terminal, so the model
picker moved to `Ctrl+K`; `/model` works everywhere. Most terminals
report `Shift+Enter` as a plain `Enter`, which would send the message
instead of breaking the line — use `Ctrl+J` there. HAL detects whether
the terminal reports modifier combinations and says so in `/help`.

Slash commands run locally and never reach the model: `/help`,
`/copy`, `/copy code`, `/theme`, `/model`, `/new`, `/clear`, `/sessions`,
`/effort`, `/expand`, `/collapse`, `/diag`, `/usage`, `/q`, `/quit`,
`/exit`.
Typing `/` opens a suggestion
panel above the composer: keep typing to filter, `Up`/`Down` to move,
`Tab` to complete, `Enter` to run (a complete command runs as typed, a
partial one runs the highlighted suggestion), `Esc` to dismiss it and
keep the draft. A leading `//` sends a literal slash-prefixed message.

The composer is a single bordered one-line input that grows as you write,
up to five rows, and shrinks back when you delete. A blank row separates
it from the conversation. Pasting never submits: multiline pastes keep
their line breaks, indentation and code fences in the composer, where
you can edit them before sending. The composer shows a five-row window
on the draft — never a cut-down copy: a paste of any size is kept whole,
`Enter` sends all of it, and the window follows the caret. `Ctrl+U`
clears the whole draft (even hundreds of pasted lines) without submitting
anything and returns the composer to one row, and says so
(`Draft cleared · Ctrl+Z to restore`); `Ctrl+Z` puts it back, but only
into an empty composer and never across sessions, and says `Draft
restored`. Clearing never touches the conversation — that is what
`/clear` is for. Switching models keeps the conversation history and the
unsent draft: the session store is model-independent. When the draft is
taller than five rows the wheel scrolls it and the caret stays where it
was; typing brings the caret back into view.

Status notices under the footer — a draft cleared, a selection copied, a
conversation renamed — replace each other and disappear after a few
seconds. The row they use is always part of the frame, so a notice
appearing or expiring never moves the conversation or the composer.

**Long prompts.** A prompt that fills more than a few lines is
abbreviated to a four-line preview while HAL writes its answer, so the
answer has room to be read. When the answer is there, the prompt opens
again — the abbreviation was a way to make room, not a way to shorten the
message. A prompt you collapse yourself with `Ctrl+O` stays collapsed;
`/expand` and `/collapse` apply to all of them. A cancelled or failed
turn opens the prompt too, and reopening a conversation shows the stored
prompts in full: the abbreviation is never stored. Collapsing is display
only — the full text is what gets stored and what the model receives.

**Reasoning effort.** MiniMax-M3.1-Flash applies `max` effort when the
parameter is omitted, which is why HAL sends `reasoning_effort` explicitly
and starts at `medium`. `/effort` opens a selector; `/effort low`,
`/effort medium`, `/effort high`, `/effort xhigh` and `/effort max` set a
level directly. The footer shows the active level next to the model
(`MiniMax-M3.1-Flash · medium`) and the choice is stored per model, so
switching models restores each one's level. The parameter is never sent
to a provider that does not document it: an unsupported model says so and
offers no selector. Changing effort never touches the conversation.

**Scrolling.** `PgUp`/`PgDn` move a full page and `Home`/`End` jump to
the start and end. Page keys are recognised by key code, not by their
exact escape sequence, so they work even when the terminal reports
Fn+PageUp as `shift+pgup`. New output only follows the bottom when you
were already there; scrolling up during a reply keeps your place.
Resizing the terminal and collapsing or expanding a long prompt preserve
the line you were reading.

**Mouse.** HAL asks the terminal for mouse events and takes over the
pointer itself, the way Pi does. This is the default; `--no-mouse` (or
`HAL9001_MOUSE=0`) turns it off and leaves the terminal's own selection
in charge. `--mouse` is still accepted as an explicit way to ask for it.

The setting is resolved in one order: an explicit flag, then the
environment, then the default (on). `HAL9001_MOUSE` accepts `1`/`true`/
`yes`/`on` and `0`/`false`/`no`/`off`; a value that is neither is
refused with a message that names the variable, so a typo cannot quietly
change how the terminal behaves. A flag decides on its own, so a
malformed value that no longer decides anything is left alone. Asking
for both `--mouse` and `--no-mouse` is refused instead of silently
choosing one.

- The wheel scrolls the conversation, or a long draft under the pointer,
  or an open panel.
- Left-dragging selects text in the conversation. The selection is
  highlighted while it is dragged, and releasing the pointer copies it to
  the clipboard through OSC 52 — the same mechanism `/copy` uses.
- A drag that reaches the top or the bottom of the conversation scrolls
  the history on its own, one row at a time, and the selection grows into
  the text that comes into view. The scroll stops as soon as the pointer
  leaves the edge, the button is released, the selection is cancelled
  with `Esc`, or the conversation changes. The wheel takes over from it
  while you drag, and the composer is never touched.
- The copied text is the reply's own text: no colours, no borders, no
  `You > ` / `HAL > ` labels, and no artificial line breaks. A wrapped
  paragraph comes back as the one line it was written as, and a word the
  renderer had to break comes back whole. The interface's own lines —
  the `Ctrl+O to expand` hint, the answer's metadata — are never copied.
- An ordinary click copies nothing.
- The selection survives scrolling, so a selection can be made across a
  long reply; it is dropped when the conversation changes or the terminal
  is resized.

With reporting on, the terminal sends mouse events instead of selecting
text, so the terminal's own selection needs Shift+drag in most emulators
and right-click behaviour varies. With `--no-mouse` the native
selection, right-click paste and middle-click paste of the PRIMARY
selection on Linux all keep working instead; either way they are
separate from the regular clipboard that `/copy` and a selection write
through OSC 52.

**Bottom-anchored history.** A new or short conversation sits just above
the composer, with the unused space above it, the way a chat application
does. As it grows, older messages move up into scrollable history. The
spacing is presentation only: it never changes the conversation, the
scroll offsets or `Home`/`End`.

**Themes.** `/theme` switches between HAL Classic, Midnight, Amber,
Paper and Monochrome. Each palette paints the whole interface: the
application background and default text, the header and footer, message
labels, rules, the composer border, panels and Markdown. Paper is a real
light theme. Moving through the picker previews the palette live,
including history already on screen; `Esc` restores the previous theme
and `Enter` saves the choice for the next start. The terminal's own
colours are restored when HAL exits. With `NO_COLOR` the interface is
plain text and the picker says themes cannot be previewed.

**Exiting.** `/q`, `/quit` and `/exit` all close HAL through the same
path as `Ctrl+C`: an active request is cancelled, nothing incomplete is
stored, and the alternate screen, cursor and terminal colours are
restored.

**Sessions.** Every conversation has an immutable ID and a human-readable
title. New sessions start with the local date and time
(`2026-11-02 16:48`); `/rename Museum of Unfinished Ideas` sets a proper
name, and bare `/rename` opens a small editor. Titles never become files,
so any text is safe; clearing the title restores the date/time default.
`/sessions` lists titles with creation and activity dates, newest first,
marking the current one. Selecting a conversation there (or with
`/new`, `/clear`, `Ctrl+N`) makes it the active one, and that choice
survives quitting and relaunching — including while it is still empty.
Nothing is ever deleted: earlier conversations, including ones from
before titles existed, stay on disk and keep working.

HAL remembers which conversation was active,

**Token usage.** The footer shows the session's cumulative provider-reported
tokens (`Tokens: 12.4k in / 3.2k out`) once a turn has reported usage;
narrow terminals hide it before the model name or the 444 marker. `/usage`
breaks it down: input, output, reasoning, cached input, the last turn and
how many turns were completed versus ended without a usage report. Counts
come only from the provider — HAL never estimates or prices tokens — and
are stored per session across restarts. `/diag` remains the place for
streaming diagnostics.

**Clearing.** `/clear` empties the visible conversation and starts a fresh
context for the next request, and selects that new session for later
launches — even before anything is said there. The previous conversation is **not** deleted: it stays
recoverable through `/sessions`. Model, effort and
theme are kept. `/new` does the same and is the shortcut (`Ctrl+N`) for
it; `/clear` is the explicit name for "start over". `/clear` works during
a running turn — it cancels the turn first, so nothing from it can appear
in the new conversation.

**Slow turns.** While a model is working, the notice under the footer
shows the phase and the elapsed time (`waiting for the model… 2s`,
`thinking… 47s`, `streaming… 51s`). A reasoning model can spend a long
time thinking before it writes anything: the indicator says so, without
ever showing the reasoning text. `/diag` reports the last turn's
duration, event counts, finish reason and token usage — counts only,
never prompt, reply or reasoning text.

If a turn ends without any visible answer — for example the output budget
was spent on reasoning — HAL explains it with the provider's finish
reason and token count, shows it through `/diag`, and stores no assistant
message. Nothing is retried automatically and the output cap is never
raised without the operator asking.

See [`docs/terminal-support.md`](docs/terminal-support.md) for the
tested and unsupported terminal behaviour. The pseudo-terminal checks
that produced it live in [`tools/ptycheck/`](tools/ptycheck/).

## Layout

```text
cmd/hal9001/          entry point: flags/env/file config, TUI launch
internal/capability/  444 allowlist: what HAL may do, default-deny rest
internal/persona/     identity, conversational contract, Dave Protocol
internal/session/     model-independent JSONL conversation storage
internal/provider/    OpenAI-compatible streaming client + contracts + mock
internal/memory/      durable record tiers, admission rules, store interface
internal/engine/      conversation core: session + provider + 444 gate
internal/tui/         fullscreen chat, themes, slash commands, pickers
internal/clipboard/   OSC 52 clipboard writer (write-only, no model access)
internal/config/      three-value onboarding, user config file (0600)
docs/                 architecture, migration inventory, dependencies
legacy_base/          preserved DeepClaw/PicoClaw reference (separate module)
```

## Rules

- Read-only authority. The capability allowlist is explicit; unknown
  operations are refused by default-deny.
- Presentation stays in the TUI; the conversation core has no terminal code.
- Conversation storage is model-independent: switching providers never
  discards the discussion.
- Credentials live in a 0600 user config file or the environment, never
  in logs, sessions, or Git. HAL9001 never reads PicoClaw, OpenCode or
  MPM credentials.
- No PicoClaw or lobster branding anywhere in the new application.
- Copying is a UI feature, not a tool: OSC 52 writes to the terminal,
  never to the model. The clipboard is never reachable from the model,
  and the terminal's mouse modes are restored when HAL exits.
- The frame always fits the terminal, so HAL never scrolls the terminal
  and never leaves the cursor hidden or the terminal in raw mode.

## Safeguards

Every request carries an output cap, and every turn is bounded in time,
so a provider cannot generate or stall without limit:

| Setting | Default | Flag | Environment |
|---|---|---|---|
| Output cap | 15000 tokens | `--max-tokens` | `HAL9001_MAX_TOKENS` |
| Idle timeout | 45 s | `--stream-idle-timeout` | `HAL9001_STREAM_IDLE_TIMEOUT` |
| Turn ceiling | 4 min | `--turn-timeout` | `HAL9001_TURN_TIMEOUT` |

The values also persist in the user configuration file. Zero means "use
the built-in default"; an explicit value always wins, so an operator who
needs a larger budget or a longer thinking phase raises it here. The
idle window resets on every chunk the provider sends, while the turn
ceiling is absolute: a reasoning phase that keeps streaming is still
bounded by it. When either fires, the HTTP request is closed, so the
provider stops generating and stops billing. `Esc` does the same
immediately.

## Limitations (Milestone 003.1)

- One provider shape: OpenAI-compatible `/chat/completions` (SSE
  streaming) plus a `/models` connection test. No per-model thinking
  controls yet.
- Live provider verification needs `HAL9001_LIVE_SMOKE=1` with real
  credentials; it is skipped by default and reported separately.
- No memory write-back, no web research, no Prompt Workshop, no review
  workflow, no delegation, no scheduled tasks.
- Assistant messages render as Markdown on completion; the live stream
  is plain text.
- Modifier-aware shortcuts (`Shift+Enter`) need a terminal that
  implements the Kitty keyboard protocol; GNOME Terminal does not.
- Token usage is shown only when the provider reports it in the stream;
  absent usage is reported as unknown, never estimated.
