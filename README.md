# HAL9001

HAL9001 is a conversational thinking partner that investigates, architects,
coordinates, manages, reviews, reports, records and suggests. It creates
through prompts that other agents implement.

HAL itself never implements.

Its operating principle is **444: read-only authority, permanently enforced.**
There are no coding profiles, execution modes, or switches that can turn HAL
into an implementation agent.

## Status

Milestone 002: the native application holds live conversations through
its own TUI — streaming answers, onboarding, model picker, persistent
model-independent sessions. Web research, Prompt Workshop, review
workflow, and MPM memory recording arrive in later milestones.

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
hal9001                                    # chat (Ctrl+S sends, Enter is a newline)
hal9001 --session work                     # resume or start a named session
hal9001 --list-sessions                    # list stored conversations
hal9001 --provider openai-compatible \
  --endpoint https://api.minimax.io/v1 \
  --model MiniMax-M3.1-Flash               # flags win over the file
```

In the TUI: `Ctrl+M` opens the model picker (switch or add a model),
`Ctrl+N` starts a new conversation, `Esc` cancels a stream,
`Ctrl+C` quits. Switching models keeps the conversation history: the
session store is model-independent.

## Layout

```text
cmd/hal9001/          entry point: flags/env/file config, TUI launch
internal/capability/  444 allowlist: what HAL may do, default-deny rest
internal/persona/     identity, conversational contract, Dave Protocol
internal/session/     model-independent JSONL conversation storage
internal/provider/    OpenAI-compatible streaming client + contracts + mock
internal/memory/      durable record tiers, admission rules, store interface
internal/engine/      conversation core: session + provider + 444 gate
internal/tui/         chat, onboarding, model picker, streaming display
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

## Limitations (Milestone 002)

- One provider shape: OpenAI-compatible `/chat/completions` (SSE
  streaming) plus a `/models` connection test. No per-model thinking
  controls yet.
- Live provider verification needs `HAL9001_LIVE_SMOKE=1` with real
  credentials; it is skipped by default and reported separately.
- No memory write-back, no web research, no Prompt Workshop, no review
  workflow, no delegation, no scheduled tasks.
- Assistant messages render as Markdown on completion; the live stream
  is plain text.
