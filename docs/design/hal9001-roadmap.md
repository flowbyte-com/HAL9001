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

## Stage 2 — Conversation quality

- Multi-turn coherence checks: summary-driven continuity prompts,
  correction/supersede handling in session notes.
- Workshop depth: brief versioning, audience-specific tuning, review
  severity thresholds — still rule-based, still no model calls.
- Genuine-memory evaluation design (which threads deserve recall),
  following the halted-005g lesson: measure before building.

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
