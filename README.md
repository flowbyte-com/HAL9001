# HAL9001

HAL9001 is a conversational thinking partner that investigates, architects,
coordinates, manages, reviews, reports, records and suggests. It creates
through prompts that other agents implement.

HAL itself never implements.

Its operating principle is **444: read-only authority, permanently enforced.**
There are no coding profiles, execution modes, or switches that can turn HAL
into an implementation agent.

## Status

Clean foundation milestone: an independently buildable Go module with a
working native terminal interface, conversation storage, provider
contracts, memory record types, and the 444 capability boundary. Model
provider adapters, web research, Prompt Workshop, review workflow, and MPM
memory recording arrive in later milestones.

The former DeepClaw/PicoClaw implementation is preserved for reference in
[`legacy_base/`](legacy_base/) (tag `pre-migration-base`). The new module
does not import it.

## Quick start

```sh
go build ./...
./hal9001 --help
```

Onboarding is three values: an API key, a provider endpoint, and a model
identifier (flags or `HAL9001_API_KEY`, `HAL9001_ENDPOINT`,
`HAL9001_MODEL`, `HAL9001_PROVIDER`). Until provider adapters land, the
interface stores the conversation and says so honestly instead of
fabricating replies.

## Layout

```text
cmd/hal9001/          entry point: flags, session store, TUI launch
internal/capability/  444 allowlist: what HAL may do, default-deny rest
internal/persona/     identity, conversational contract, Dave Protocol
internal/session/     model-independent JSONL conversation storage
internal/provider/    provider/model contracts, config validation, mock
internal/memory/      durable record tiers, admission rules, store interface
internal/engine/      conversation core: session + provider + 444 gate
internal/tui/         Bubble Tea interface: scroll, composer, 444 indicator
docs/                 architecture, migration inventory, dependencies
legacy_base/          preserved DeepClaw/PicoClaw reference (separate module)
```

## Rules

- Read-only authority. The capability allowlist is explicit; unknown
  operations are refused by default-deny.
- Presentation stays in the TUI; the conversation core has no terminal code.
- Conversation storage is model-independent: switching providers never
  discards the discussion.
- Credentials live in the environment, never in files or session data.
- No PicoClaw or lobster branding anywhere in the new application.
