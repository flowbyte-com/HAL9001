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
| `PgUp` / `PgDn` | Scroll the conversation |

`Enter` and `Ctrl+M` are the same byte on a terminal, so the model
picker moved to `Ctrl+K`; `/model` works everywhere. Most terminals
report `Shift+Enter` as a plain `Enter`, which would send the message
instead of breaking the line — use `Ctrl+J` there. HAL detects whether
the terminal reports modifier combinations and says so in `/help`.

Slash commands run locally and never reach the model: `/help`,
`/copy`, `/copy code`, `/theme`, `/model`, `/new`, `/sessions`. A
leading `//` sends a literal slash-prefixed message. Pasting never
submits: multiline pastes keep their line breaks, indentation and code
fences in the composer, where you can edit them before sending.
Switching models keeps the conversation history and the unsent draft:
the session store is model-independent.

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
