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

The composer is a single bordered input, three text rows by default,
growing with the input up to eight rows. A blank row separates it from
the conversation. Pasting never submits. Pasting never submits: multiline pastes keep
their line breaks, indentation and code fences in the composer, where
you can edit them before sending. Switching models keeps the
conversation history and the unsent draft: the session store is
model-independent.

**Long prompts.** A prompt that fills more than a few lines is collapsed
to a four-line preview after you send it, so HAL's answer stays in view.
`Ctrl+O` toggles the most recent prompt; `/expand` and `/collapse` apply
to all of them. Collapsing is display only — the full text is what gets
stored and what the model receives.

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
the line you were reading. The mouse wheel is deliberately not captured —
that would break native selection and right-click paste — so use the
keyboard to scroll; the terminal's own Alt-screen behaviour applies to
the wheel.

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

**Sessions.** HAL remembers which conversation was active. `/clear` and
`/new` start a genuinely new one, select it immediately, and that choice
survives quitting and relaunching — including while it is still empty.
Picking an older conversation in `/sessions` makes it active again for
the next launch. Nothing is ever deleted: every earlier conversation
stays on disk.

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
launches. The previous conversation is **not** deleted: it stays
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
  never to the model. Native terminal selection keeps working because
  HAL never captures the mouse.
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
