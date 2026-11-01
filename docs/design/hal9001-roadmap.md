# HAL9001 Staged Roadmap

Incremental milestones. Each stage is independently shippable and leaves
default behavior unchanged.

## Stage 1 — Persona foundation (done)

- Config selection (`agents.defaults.persona`), identity block, chat-first
  tool-use suppression, curated visible tools, Dave Protocol enforcement.
- Prompt Workshop core (`pkg/promptwork`) plus `draft_prompt` /
  `review_prompt` tool adapters registered under HAL only.
- Behavioral tests (Dave exactness, greeting with no tool calls,
  workshop Build/Review, overhead comparison); full `pkg/agent` suite green.
- Docs: vision, architecture (with Stage A), memory contract, this roadmap.

## Stage 2 — Conversational identity and capability boundaries (done)

- Capability audit: provider defs, registry execution (`Get`-gated),
  SubTurn clone inheritance, and late registration all enforce the
  visibility profile; turn profiles intersect rather than widen; custom
  prompts and retrieved memory are content-only and labeled lower-trust.
- One real gap closed: a `SystemPromptMode: off` turn profile injected
  the always-use-tools fallback into HAL turns (`prompt_turn.go` now
  gates `ToolUseFallback` on the persona).
- Deterministic system-level tests with scripted models: plain chat,
  absurd-idea-as-conversation, multi-turn development, retrieved
  continuity assembly, coding discussion without implementation,
  investigate→develop→brief→review acceptance with zero implementor calls.
- Workshop validation: section order, contradiction fidelity, self-hosting
  cleanliness, iterative refinement reducing findings to zero.
- Context costs measured on a real assembled request: ~1,666-token
  foundation (~17% of the 10k soft budget), ~5k representative request.
- Memory contract extended with thought-continuity tiers
  (Conversation / Sparks / Developing ideas / Decisions) and promotion
  criteria. No automatic writes; none implemented.

## Stage 3 — The 444 contract (done)

- Read-only authority enforced in application logic at three layers
  (provider defs, registry execution, 444 execution gate on
  `HalRestricted`); full audit of tools, MCP, plugins, subturns, custom
  prompts, and coding-profile precedence. One gap class closed by
  construction: blocklisted tools are refused by name even if visible.
- Coding capabilities preserved for non-HAL profiles: operator-enabled
  coding profile clears the restriction and executes normally.
- Review capability: `promptwork.AssessReport` + `review_report` tool —
  verified findings vs unverified claims vs deviations, risks, actions,
  and a follow-up brief; self-report-only disclaimer by design.
- Memory admission design proposed in the
  [memory contract](../architecture/hal9001-memory-contract.md) for
  review: four record types, doubt rule, sensitive tier, correction
  supersession, operator approval boundary. No live writes; none
  implemented.
- Roadmap conflict resolved (below): no HAL coding-profile escalation,
  ever. Delegation is by assignment, not by implementation.

## Stage 4 — Authorized delegation and specialist coordination (design)

Supersedes the earlier "explicit action mode" plan, which is withdrawn:
HAL never becomes the implementor, even when asked to fix something
directly. Instead:

- HAL investigates, develops the idea, and prepares the implementation
  assignment as a portable brief (`draft_prompt`).
- The operator authorizes; a specialist agent (or person) executes —
  through the existing coding profiles, which remain non-HAL.
- HAL reviews the returned work (`review_report`) and either accepts or
  prepares a follow-up assignment. The loop is review-driven, never
  execution-driven.
- `spawn`/`subagent`/`delegate` stay outside HAL's tool profile until a
  delegation design with its own authorization boundary is approved.
  Coordination happens through briefs and reviews, not orchestration
  primitives.

## Non-goals (all stages)

Rewrite of the agent runtime; changes to the pristine PicoClaw baseline,
live MPM, OpenCode settings, scheduler, or unrelated projects; remote
push or deployment without authorization; model-based intent routing;
large prompt frameworks.
