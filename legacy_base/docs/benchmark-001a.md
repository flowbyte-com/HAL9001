# Benchmark 001A: Retrieval success but answer failure

## Summary

Benchmark 001 retrieved the correct MPM record and injected it into the final
LLM request, but the live model still failed to answer the recall task.
Eager `MEMORY.md` and direct user-provided context succeeded with the same fact.

This is a prompt-usage failure, not a retrieval failure.

## Evidence

Retrieval success in Benchmark 001:

- Debug log: `MPM retrieval duration_ms=... records=1 tokens=47`
- The retrieved fact was `The benchmark secret code is blue-heron-42.`

Prompt-injection success:

- The final LLM system prompt contained:
  `## Retrieved MPM memories`
  `- (..., score=...) What is the benchmark secret code? The benchmark secret code is blue-heron-42. [tags: ...]`

Controlled outcomes:

- Direct user-provided context: correct answer (`blue-heron-42`)
- Eager `MEMORY.md`: correct answer (`blue-heron-42`)
- DeepClaw retrieved-memory overlay: incorrect answer (model reported no information)

## Failure mechanism

The retrieved fact is present in the request, but the model does not use it.
The likely cause is the placement and framing of retrieved memory: it appears as a
system-level `## Retrieved MPM memories` block with provenance metadata, rather
than the model's familiar `# Memory` / `## Long-term Memory` context. The model
tends to treat that block as background metadata and falls back to saying it has
no information.

Retrieval and prompt injection are therefore distinct stages:
- Retrieval succeeded.
- Injection succeeded.
- Model usage failed.

## No-memory token total

The unexpectedly high no-memory token total is explained by tool exploration.
The no-memory agent issued workspace searches before answering, so its final LLM
request contained 51-57 messages, including tool results. Eager-memory requests
contained 31-37 messages because the memory answered directly. Provider-reported
prompt tokens reflect this cumulative context, not just the system prompt.

## Minimal fixes

No runtime fix is justified by this evidence alone. The retrieval adapter and
prompt overlay work as designed. A future experiment should test stronger model
compliance or a more explicit user-facing memory framing before changing the
architecture.

## Tests

Existing regression tests remain passing:
- `go test ./pkg/mpm/`
- `go test -tags 'goolm,stdjson' -run 'DeepClawMPM' -count=1 ./pkg/agent/`
