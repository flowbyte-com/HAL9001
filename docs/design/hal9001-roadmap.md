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

## Stage 3 — Memory write-back (gated)

- Propose, then implement, the approval-gated MPM write path per the
  [memory contract](../architecture/hal9001-memory-contract.md):
  provenance, selective admission, correction supersede.
- Nothing here proceeds without operator approval of the design.

## Stage 4 — Realize (explicit action mode)

- User-invoked escalation only: when the user asks for action, HAL may
  enable broader tool access for that turn (coding profile is the
  existing mechanism and takes precedence).
- No automatic coding workflows, no multi-agent orchestration — those
  remain explicitly out of scope until Stage 4 is separately authorized.

## Non-goals (all stages)

Rewrite of the agent runtime; changes to the pristine PicoClaw baseline,
live MPM, OpenCode settings, scheduler, or unrelated projects; remote
push or deployment without authorization; model-based intent routing;
large prompt frameworks.
