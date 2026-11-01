# Milestone 005H: task-state token economics and memory-value audit

Status: **offline investigation complete. No code, fixture, or live
changes.** All claims are computed from the frozen 004C implementation
and the preserved redacted 005G evidence (results, raw traces, session
files), plus one read-only re-measurement pass. Nothing was pushed,
deployed, or re-run live.

Central finding: **DeepClaw is paying to remember what it did, not
preserving working memory it needs.** The mechanism works as specified
— bounded capture, budgeted rendering, crash-safe persistence — but in
the only live paired trial, all 32 captured entries were either
redundant with ordinary context, truncated past usefulness at capture
time, or consulted and then not acted upon. The ~500-token per-request
budget holds exactly as designed; the "2,500 tokens per request" premise
does not survive measurement (it is a per-turn sum, §1).

## 1. Token accounting: where the 500-token budget goes

**The budget is enforced per request, not per turn.** `RenderWithMode`
assembles objective + priority-ordered entries while
`estimateTokens(candidate) <= budget` (budget = 500 via
`DEEPCLAW_TASK_STATE_BUDGET`, min 100 / max 2000), then hard-trims to
`<= budget`. Measured on treatment t3 (5 requests):

| Request | Task-state chars | Task-state tokens (local est.) | Full prompt (provider) | Share |
|---|---|---|---|---|
| req-020…024 (each) | 2001 | 501 | 5142–7257 | ~7–10% |

The 501 vs 500 is rounding (`(len+3)/4`) plus the retrieved-context
wrapper line, which sits *outside* the budgeted block: the budget
governs the task-state part, the envelope adds ~70 chars on top. No
discrepancy, no overrun mechanism — but the accounting unit matters:

- per **request**: ~500 tokens (budget-enforced),
- per **turn** (t3): 2,505 tokens (5 requests),
- per **trial**: 7,972 prompt tokens + 44,230-byte sidecar,
- vs control trial total 269,354 tokens (treatment: 164,993 — fewer,
  because it stopped early, not because it was efficient).

Distinguishing estimates from usage: provider-reported
`prompt_tokens` *include* the rendered block (real cost under the
provider's tokenizer); `task_state_*_estimate` fields are local
`chars/4`. They agree in magnitude here (501 local vs ~500 of the
5–7k provider prompt tokens per request).

## 2. What the 32 entries actually are (treatment, end of t3)

| Class | Count | Content |
|---|---|---|
| Action/invocation records | 7 | 5× `ls`/directory listings, 1 edit confirmation, 1 write confirmation |
| Tool-reported evidence + concrete values | ~13 | 8 file reads (full contents, ≤160-char quotes), dry-run outputs (**truncated at ~160 chars** — header + G1 rows only), check failures (105.5 vs 106), gofmt/vet OK, current round.go text |
| Constraints / decisions | 0 | none captured (no user constraint, model proposal, or next-action markers fired) |
| Errors / diagnostics | 4 | missing MEMORY.md read, 2 blocked (sandbox-denied) commands, 1 failed `/tmp` write |
| Superseded | 0 | dedup-by-key never fired — near-identical commands varied enough (`head -60` vs `head -40`, `| tail`) to get distinct keys |
| Duplicates | ~9 | 6 file re-reads (round.go, migrate_test.go, tiers.go, legacy.go read in t1 *and* t3), 3× repeated check/dry-run outputs |

Already in ordinary context at render time: nearly all of it — t3's
visible history (10 post-compaction records) plus the 705-char summary
carried the task list, the G4→800 headline, and the deferred tie
result.

**The mapping question.** The 21-row dry-run table (~2,000 chars of
output) was *eligible* for capture (tool output is explicitly
recorded), and 3 dry-run observations *were* stored — but
`taskExcerpt(..., 160)` kept only the first ~160 characters (header +
G1 rows). The G4/G6/G7 rows never entered the sidecar at all; only
`OutputLen` + `OutputSHA` (hash of the full output) attest that a
longer output existed. Separately, the rendered block is
failures-first (priority 2 beats observations at 3), so the first
~2,000 of ~10,000 accumulated chars went to: 3 stale failures
(including two sandbox-denied exploratory commands from t1/t2),
`ls` outputs, and the dry-run head. **The specific values a t3
decision could have used were dropped at capture by the 160-char
quote cap, and what survived rendering was mostly history the model
already had.**

## 3. Usefulness per request (observed, not inferred)

Relevant to t3's actual decisions (fix `RoundEdge` to banker's;
pin G4/800 in a regression test): the rounding rule (in the t3
prompt), the test expectations (in the repo), and `800+G4` (in the
705-char summary). None of it came from task state.

The one observed interaction cuts *for* consultation and *against*
benefit: the final t3 response explicitly cites the rendered block
("Let me check the dry run output — from the task state: 'tier G1…'"),
concludes the mapping is "just describing banker's rounding", then
deliberates through three consecutive 1024-token-capped responses
(semantic-retry continuations per `LLMRetryOnFinishReasons`) and the
turn ends with **zero edits** — the correct 6-line fix written out
in prose, never applied. That is *rendered and consulted, but
neither sufficient* (truncation had removed the specifics) *nor acted
upon* (intent-action gap, consistent with 005B cart/billing and 005F
cart/billing). No evidence of distraction either: the failure mode
matches no-task-state trials, and nothing in the trace attributes a
wrong decision to a task-state entry.

## 4. Common pre-boundary trajectory: feasible without new fixtures

The 005G confound was pre-boundary divergence (control re-ran the
verbose dry run in t2; treatment flailed with absolute paths, so the
same compaction kept different tails). A cleaner design needs no new
fixture and no production change:

1. Run a **shared pre-boundary trajectory** with task state OFF for
   both arms (identical prompts → near-identical context; residual
   model variance is measured, not assumed away).
2. At the boundary, fork: control continues from the compacted
   session as today; treatment gets the **same** compacted session
   **plus** a sidecar transplanted from the shared trace —
   `RecordTool` replay of the actually-observed tool events (all
   fields present in `tool-trace.jsonl`), i.e. byte-identical
   evidence, zero fabrication.
3. Score the same t3 decisions with the same hidden tests.

This preserves every 005G gate property (real loss, dependence on
dropped specifics, capturability from observed events), removes the
confound, and keeps MPM/history/compaction identical. Cost: a small
bench-only replay harness; risk: transplant timing (entries must
carry original turn IDs and timestamps — both recorded). Not
implemented here per milestone scope; recommended as the protocol
for any follow-up live comparison.

## 5. Engineering trade-off

**A. Retain 004C unchanged (opt-in or default).** For: mechanism is
correct as specified (capture→budget→render→persist all verified;
overhead bounded and measured). Against: this audit finds zero cases
where retained content was both needed and used; 7,972 tokens +
44 KB per trial bought nothing here, and the failures-first ordering
spends the budget on stale trivia. Security: unchanged (local
sidecar, existing boundaries). Cost: as measured.

**B. Tiny evidence-selected working set.** For: 32 entries → ~7
rendered; most are re-reads, listings, and superseded-by-retry
failures a small set would drop; quote caps could be selective
(full content for check/test outputs, hashes only for listings).
Against: requires production ranking changes for unproven gain;
selectivity risks dropping the one needed row (the same truncation
problem, relocated); adds tuning surface (per-kind budgets, dedup
semantics) with no demonstrated payoff. Security: same sidecar.
Cost: engineering time + re-validation, token savings speculative.

**C. Disable automatic rendering; rely on history, compaction, MPM.**
For: every observed success in 005B/005F/005G (including control's
full 12/12 with mutation testing) used zero task-state tokens; no
observed failure traces to missing task state; removes 44 KB writes,
~8k tokens/trial, and the consultation-without-action failure mode
entirely. Against: closes off the one scenario the gate was built
for (genuine loss + dependence), which has never yet been observed
live. Security: strictly reduces surface (no sidecar). Cost: zero.

**Recommendation: C as the production default, A retained as an
explicit opt-in for memory-continuity workloads — but only alongside
an eligible-task demonstration, which does not yet exist.** B is not
worth its complexity without evidence that selectivity (rather than
redundancy) is the binding constraint; nothing in this audit shows
that. No code change is made by this milestone either way.

## 6. Reproduction and limits

Accounting queries: per-request `task_state_chars`/`*_tokens_estimate`
in `result.json:requests`; rendered text in `TurnResult.
task_state_prompt_text`; entry inventory in `TurnResult.task_state`
(`active_kinds/tools/verifications/quotes`); gate inputs from
session JSONL + summarizer responses (`turn_id: unknown` in
`raw/requests.jsonl`). Evidence: `.lab/bench/005g/005g-live-202*
(read-only use). Limits: single live pair; MiniMax-only; the
production summarizer's abstractive behavior decides what survives
compaction, so retention findings are hostage to it; no live calls
were made for this milestone.
