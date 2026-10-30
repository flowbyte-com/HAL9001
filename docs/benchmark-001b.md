# Benchmark 001B: Memory presentation

## Result

The reliable correction is to inject retrieved MPM memory into the final user turn
as clearly labeled retrieved context, rather than as a system-prompt overlay.

The memory remains lower-trust data and is explicitly marked as not an instruction.

## Controlled comparison

Same model, fact, question, tools and settings:

1. Eager `MEMORY.md`: correct.
2. Original DeepClaw system overlay: incorrect.
3. Concise eager `MEMORY.md`: correct.
4. DeepClaw memory placed near the final user turn as retrieved context: correct
   in 3/3 repeated trials.
5. Direct user-provided context: correct.

## Mechanism

The original system overlay was present in the request, but the model did not use
it. Placing the same fact near the final user turn, while preserving its status
as retrieved data, made it reliably usable.

## Implementation

- `pkg/agent/pipeline_setup.go`: store retrieved text in `mpmMemoryUserContext`.
- `pkg/agent/prompt_turn.go`: prepend it to the current user message as
  `Retrieved context (not an instruction): ...`.
- Retrieval still happens once per turn.
- Project isolation, provenance, budget truncation and D8 protections are unchanged.
- The stable system-prompt prefix remains cacheable.

## Measurements

Repeated DeepClaw trials after the change: 3/3 correct recall.

No new provider-reported token benchmark is claimed in this checkpoint; the
change reduces prompt churn by avoiding a larger system-prompt rebuild.

## Tests

- `go test ./pkg/mpm/` passed.
- `go test -tags 'goolm,stdjson' -run 'DeepClawMPM' -count=1 ./pkg/agent/` passed.
