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

Layout: a one-line `● HAL9001` header, a rule, the content region, a
rule, a one-line status footer (`444 READ ONLY · /help` left, active
model right) and a notice line that is always reserved so the layout
never shifts. In chat the content region is the scrollable Markdown
viewport above a borderless composer that grows to eight lines; in an
overlay it is the panel, which scrolls when it does not fit.

Views: chat, onboarding, model picker, theme picker (five presets with
live preview, persisted), help, and stored sessions. A slash dispatcher
handles `/help`, `/copy`, `/copy code`, `/theme`, `/model`, `/new` and
`/sessions` locally — commands never reach the model, and `//` escapes
to a literal slash message. `/copy` writes the last response (or its
latest fenced block) via OSC 52; the mouse is deliberately never
captured so native selection keeps working. Pasted text (bracketed or
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
Long user prompts render as a four-line preview with an expand hint;
the collapse never touches the stored text or the provider request.

## Streaming

A turn is bounded three ways: the caller's context (cancellation), an
idle watchdog (no data for the configured window) and a total ceiling.
Each bound closes the HTTP request, so an abandoned turn stops
generating instead of running on. Requests always carry an output cap
(`max_tokens`), from the model or the configured default — a reasoning
model must never be allowed to generate without one.

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
