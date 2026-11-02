# HAL9001 Architecture

HAL9001 is a conversational thinking partner with permanent 444
read-only authority. This document describes the new native foundation
(root Go module `github.com/sipeed/picoclaw/hal9001`). The legacy
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
cmd/hal9001      flags/env config, session store, TUI launch
internal/tui     Bubble Tea interface (no business logic)
internal/engine  Respond(): store user msg → provider chat → 444 gate → store reply
internal/session JSONL conversation store (model-independent)
internal/provider Provider/Model contracts, config validation, mock
internal/memory  record tiers, admission validation, Store interface
internal/config  three-value onboarding (key, endpoint, model)
internal/persona identity, Dave Protocol, safety-signal detection
internal/capability 444 allowlist
```

Conversation flows downward only: the TUI calls the engine, the engine
calls the provider and the session store. Nothing calls upward. The
engine never fabricates replies: without a provider it stores the user
message and reports `ErrNoProvider`, which the TUI shows as a status
notice — never as an assistant message.

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
