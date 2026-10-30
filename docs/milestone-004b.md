# Milestone 004B: working-memory residency and efficiency

Status: **implemented and validated deterministically.** No live-model or
live-token claim is made. The 004B comparison uses the 003F mock harness.

- Code commit: `a4c2b374a1194cb864bbd44b9878dfa28527d0c9` (`agent: add
  boundary-aware task-state residency`)
- Run checkout: `a4c2b374a1194cb864bbd44b9878dfa28527d0c9` (clean at run time;
  the documentation commit adds only this report)
- Previous milestone: `0e02746a814cccc3ba39a3a270a1a5931c108d62` / 004A
- Benchmark binary: `.lab/bin/bench`
- Binary SHA-256: `8b09a4d5cc588d53681b187d7a2fb59aa510f29f7652c0091dd3a71c06f1a5b8`
- Go: `go1.27.3`
- Run: `.lab/bench/004b/experiment-c-20261030`
- 36 trials (4 conditions x 3 scenarios x 3 trials), 36 pass, 0 fail, 0 error

The principle is:

> Working memory should remain available continuously, but it need not be
> repeated continuously.

004B keeps the 004A evidence model and storage format unchanged. It changes
only the deterministic decision about which already-stored entries occupy the
prompt. Storage is still complete; residency is now boundary-aware.

## 1. Result summary

| Scenario | Native summary only (D) | No state (A) | Always state (B, 004A) | Boundary state (C) |
|---|---:|---:|---:|---:|
| Main seven-turn regression: task-state prompt tokens | 0 | 0 | 1,181 | **545 (-53.9%)** |
| Main: total prompt tokens | 6,374 | 6,022 | 7,354 | **6,694** |
| Multi-failure: task-state prompt tokens | 0 | 0 | 958 | **659 (-31.2%)** |
| Sourced-summary: task-state prompt tokens | 0 | 0 | 430 | **80 (-81.4%)** |
| Post-compaction diagnostic retained at t5 | no | no | yes | **yes** |
| Two-failure partial supersession | no | no | yes | **yes** |
| New objective clears unrelated failures | n/a | n/a | yes | **yes** |

The boundary policy removes 636 of the 1,181 task-state prompt tokens in the
004A regression (53.9%), while preserving the exact post-compaction diagnostic,
the proposed/unverified distinction, partial supersession of two failures, and
the new-objective reset. It does not change what is stored: the final
un-suppressed render is 1,029 characters / 258 estimated tokens in both B and C.

The controls confirm that the retained fact is real. At t5 the no-state
conditions answer *"I don't know; that detail is no longer in the conversation."*
B and C answer *"The failing test reported: parse.go:12: undefined: parsePort."*

## 2. What changed

`pkg/agent/task_state.go` now has two residency policies:

- `always`: the exact 004A renderer. It renders every non-superseded entry
  whose evidence quote is not exactly present in visible text.
- `boundary` (default): preserves the same stored records but renders an entry
  only while it is needed for continuity.

`DEEPCLAW_TASK_STATE_RESIDENCY=always` restores 004A rendering exactly.
The default is `boundary`; an unset or invalid value also selects `boundary`.

Boundary rendering is deterministic and uses only stored records plus the
visible prompt text. There is no LLM call, no reflection loop, no new store and
no schema change.

### 2.1 Rules

In order:

1. Filter by project and session identity (unchanged).
2. Skip entries marked `Superseded`.
3. Skip a completed objective.
4. Skip compaction bookkeeping entries. Their only content is that a summary
   exists, which the system prompt already states.
5. Skip routine filesystem successes (`read`, `write`, `edit`, append). They
   are workflow mechanics, not findings. Command observations (`bash`, `exec`)
   and every failure remain eligible.
6. Skip an entry whose evidence quote is already visible in recent history,
   the compaction summary, or the current user message. Boundary mode compares
   whitespace-normalized text, so a stored one-line excerpt matches history or
   a summary that still contains newlines or tabs.
7. Sort by kind priority, then age, then apply the existing 500-token rendered
   budget.

The rules never use "the current message did not mention it" as a reason to
suppress an entry. Visibility is evidence-based: the fact must still be present
in the visible window, or be present verbatim in a summary. A hidden failure
with no matching visible text remains resident, which is what preserves the
004A post-compaction behavior.

Summary equivalence is deliberately conservative. Only exact normalized
containment is accepted. A paraphrased summary that preserves the meaning but
not the words leaves the state entry resident. That over-renders in the worst
case; it does not silently drop a fact the summary failed to preserve.

### 2.2 Storage budget versus prompt-residency budget

These are different budgets and 004B only changes the second:

| Budget | Bound | 004B value |
|---|---|---|
| Storage | 64 entries; 240 chars per entry; 320-char evidence excerpt | 9 entries, 7 active; unchanged from 004A |
| Un-suppressed render | 500 estimated tokens (`DEEPCLAW_TASK_STATE_BUDGET`) | 1,029 chars / 258 estimated tokens; unchanged from 004A |
| Actual request residency (B) | same 500 cap | peak 173 tokens in one request; 1,181 tokens summed over 12 requests |
| Actual request residency (C) | same 500 cap | peak 101 tokens in one request; 545 tokens summed over 12 requests |

The 258-token final snapshot is the same in B and C because it renders state
with no visible-text suppression. It is an upper bound on the stored model,
not a measurement of prompt residency.

## 3. Phase 1: audit of the measured 004A overhead

Method: `.lab/bench/004b/audit.py` reads the raw request records from the 004A
run, extracts the `source: task.state` part from each request, and compares each
rendered line's evidence quote against the visible history and the compaction
summary after whitespace normalization. Per-request prompt tokens come from the
recording provider, not from log lines. The full machine-readable table is
`.lab/bench/004b/audit-main.json`.

The 004A run had 12 requests. Request 6 is the summarization request, not a
user turn; it contains no task state.

| Req | Turn | Prompt A | Prompt B | Delta | State chars | State tokens | Rendered lines | Stored/active after turn | Duplicated in history | Duplicated in summary | Needed for this question |
|---:|---|---:|---:|---:|---:|---:|---:|---|---|---|---|---|
| 1 | t1 | 453 | 451 | -2 | 0 | 0 | 0 | 3/3 | no | no | no |
| 2 | t1 | 461 | 458 | -3 | 0 | 0 | 0 | 3/3 | no | no | no |
| 3 | t1 | 494 | 492 | -2 | 0 | 0 | 0 | 3/3 | no | no | no |
| 4 | t2 | 514 | 613 | +99 | 326 | 82 | 1 | 4/3 | **yes: failure** | no | no |
| 5 | t3 | 529 | 628 | +99 | 326 | 82 | 1 | 6/5 | **yes: failure** | no | no |
| 6 | summarizer | 88 | 88 | 0 | 0 | 0 | 0 | 6/5 | no | no | n/a |
| 7 | t4 | 531 | 721 | +190 | 692 | 173 | 4 | 7/6 | no | no | partly |
| 8 | t5 | 559 | 749 | +190 | 692 | 173 | 4 | 7/6 | no | no | **yes** |
| 9 | t6 | 586 | 777 | +191 | 692 | 173 | 4 | 9/7 | no | no | partly |
| 10 | t6 | 592 | 783 | +191 | 692 | 173 | 4 | 9/7 | no | no | no |
| 11 | t6 | 603 | 794 | +191 | 692 | 173 | 4 | 9/7 | no | no | no |
| 12 | t7 | 612 | 782 | +170 | 607 | 152 | 4 | 9/7 | **yes: success** | no | no |

`Prompt A` is `history_cache`; `Prompt B` is the 004A `task_state` condition.
`State tokens` is `ceil(chars / 4)` for the extracted block. `Stored/active`
counts state after that turn, excluding the separate objective field.

### 3.1 Where the 1,314 tokens went

The task-state block itself accounts for **1,181 tokens**:

```text
82  (t2: failure repeated)
82  (t3: failure repeated)
173 (t4: failure + write observation + compaction + objective)
173 (t5: same)
173 (t6 request 1: same)
173 (t6 request 2: same)
173 (t6 request 3: same)
152 (t7: write observation + success observation + compaction + objective)
= 1,181
```

The remaining **133 tokens** are wrapper, path and tokenization artifacts. The
first three deltas are negative because the trial directory path embedded in
the system prompt has a different length per condition. Subsequent requests
show +17 or +18 tokens beyond the `chars / 4` block estimate for the same
reason. Reconstructing the 1,314 as `1,181 + 133` matters: the state block is
not the whole delta, but it is the only part 004B changes.

### 3.2 Duplicate, stale and necessary content

- **t2 and t3 (164 tokens):** the stored failure quote is whitespace-normalized
  (`... parse.go:12: undefined: parsePort ...`), while the history tool result
  still contains newlines and tabs. 004A's exact `strings.Contains` check
  therefore fails to see it and repeats the failure. This is a pure duplicate:
  the record is correct, but the prompt already contains the fact.
- **t4 and t5 (346 tokens):** the failure is genuinely hidden after compaction,
  so this content is necessary. Each 173-token block also spends about 72
  tokens on a routine successful write and on a compaction entry that merely
  restates the summary's presence. Suppressed at t4/t5: the correction, the
  proposed diagnosis and the next action, because all three are still verbatim
  in the retained history.
- **t6 (519 tokens across three requests):** the failure is needed for the
  first request; the routine write and compaction entries are not. Requests 2
  and 3 still render the failure because supersession is recorded after those
  requests are assembled; the response is nevertheless correct, and the state
  after the turn has no active failure. The success observation is not yet in
  the prompt.
- **t7 (152 tokens):** the failure is correctly superseded and absent. The
  block still repeats the successful rerun (`ok ... PASS`) because history
  contains tabs and the stored quote does not, and it repeats the routine write
  and compaction entries. The answer ("No, the test now passes") is available
  from visible history, so none of the 152 tokens is needed. Boundary mode
  keeps only the 40-token objective.
- **Compaction summaries:** in the 84-character generic 004A summary, no fact
  is duplicated. The compaction entry is bookkeeping about the summary, not a
  copy of it. The sourced-summary fixture in section 6 is the case where a
  summary does contain the diagnostic verbatim, and 004A repeats it there.

### 3.3 Reduction decomposition

The 636-token difference between B and C in the main scenario is exactly:

| Source | Tokens |
|---|---:|
| Whitespace-blind duplicate failure at t2/t3 | 164 |
| Routine write + compaction bookkeeping at t4, t5 and three t6 requests (5 x 72) | 360 |
| Routine write + duplicate success + compaction at t7 (152 minus the 40-token objective) | 112 |
| **Total** | **636** |

No necessary hidden failure was removed. The 101-token t4-t6 block consists of
the hidden failure plus the active objective; C keeps both.

## 4. Phase 2: residency policies compared

All four conditions use the same binary, fixtures, mock provider, and
`bash,edit,read,write` tool profile. Only residency and memory policy differ.

| Condition | Structured state | Residency | Memory policy | Purpose |
|---|---|---|---|---|
| A `history_cache` | no | n/a | `mpm_active` | 004A control: history + MPM cache + native summary |
| B `always_state` | yes | `always` | `mpm_active` | exact 004A behavior |
| C `boundary_state` | yes | `boundary` | `mpm_active` | new policy |
| D `summary_only` | no | n/a | `none` | native compaction summary as the only carried evidence |

C and B store identical records; the only difference is which records are
rendered. D is not a weakened summary: in the sourced-summary scenario all
conditions receive the same 202-character summary, including the source command
and the exact diagnostic. D uses `MemoryPolicy: "none"` so MPM retrieval cannot
substitute for the summary; that changes the harness system preamble by about
396 characters, so D's token totals are not directly comparable with A/B/C, but
its answer correctness is.

The controls also run with the same retrieval, summary and turn budgets.
Experiment C uses `MaxToolIterations=8` for all four conditions so the t6 tool
loop is not truncated differently.

## 5. Phase 3: correctness outcomes

### 5.1 Original seven-turn regression

| Turn | A no state | B always | C boundary | D summary only |
|---|---|---|---|---|
| t1 failing test | diagnostic | diagnostic | diagnostic | diagnostic |
| t2 correction | accepted | accepted + recorded | accepted + recorded | accepted |
| t3 proposed diagnosis | stated | `proposed, unverified` | `proposed, unverified` | stated |
| t4 explicit compaction | summary | summary + state | summary + state | summary |
| t5 post-compaction follow-up | **loses fact** | **diagnostic** | **diagnostic** | **loses fact** |
| t6 rerun | passes | passes; failure superseded | passes; failure superseded | passes |
| t7 still failing? | "No..." | "No..." | "No..." | "No..." |

The A and D turns pass their control checks because the harness expects the
no-state conditions to abstain when the fact is gone. They fail the continuity
requirement itself. B and C pass the treatment checks at every turn.

Per-turn task-state prompt tokens, mean of three deterministic trials:

| Turn | B always | C boundary |
|---|---:|---:|
| t1 | 0 | 0 |
| t2 | 82 | **0** |
| t3 | 82 | **0** |
| t4 | 173 | 101 |
| t5 | 173 | 101 |
| t6 (3 requests) | 519 | 303 |
| t7 | 152 | 40 |
| **Total** | **1,181** | **545** |

The t6 and t7 values are sums over the requests in that turn; a turn with three
tool iterations contributes its block three times.

### 5.2 Additional regression cases

**Two simultaneous failures and partial supersession**
(`task_state_multi_failure`). Turn 1 stores the parser failure
(`undefined: parsePort`) and a distinct lint failure (`lint.go:4: unused
import`). Turn 2 clears history and reruns only the parser command, which now
passes. At turn 3:

- A and D answer *"I don't know which failures remain; that detail is no longer
  in the conversation."*
- B and C answer *"The lint check still fails: lint.go:4: unused import."*
- The parser failure is superseded; the lint failure remains stored and
  resident. C's t3 block is 119 tokens versus B's 223.
- At turn 4 a new objective (`Add a metrics endpoint.`) clears the unrelated
  failure. Final active kinds are empty and the objective is the new one.

**User correction changes an earlier constraint.** The t2 correction shares the
constraint key and supersedes the earlier port instruction. C does not render
it while it is still visible in history, but the stored correction survives and
is present in the final state.

**Proposed diagnosis remains unverified.** The t3 diagnosis is stored with
`proposed` verification. The later successful rerun supersedes the failure but
does not promote the diagnosis.

**New objective invalidates unrelated state.** Covered by the multi-failure
turn 4 above; the final state has no active failure.

**A summary already contains a properly sourced finding**
(`task_state_summary_source`). The summary is:

```text
Compaction summary.
Source: bash sh parser_check.sh, exit 1.
Reported:
STDERR:
--- FAIL: TestParsePort
parse.go:12: undefined: parsePort
FAIL	github.com/example/parser	0.01s
[Command exited with code 1]
```

D answers from it correctly. B repeats the finding (174 tokens at t3/t4); C
renders only the 40-token objective because exact normalized containment shows
the summary already carries the diagnostic. C still stores the failure; this is
render suppression, not deletion.

**Project switch with existing state.** Unchanged 004A behavior is covered by
`TestTaskStateProjectSwitchDoesNotLeak` and
`TestTaskStateIsolationByProjectAndSession`. The sidecar carries project and
session identity, and a mismatched sidecar is discarded on load.

**Archived/superseded entries are not injected.** The main scenario's t7 block
has no failure. The multi-failure t2 parser failure is not rendered after the
matching success. `TestTaskStateBoundaryPartialSupersession` covers the same
state transition in isolation.

**Whitespace-normalized visibility.** `TestTaskStateBoundarySuppressesWhitespaceDuplicate`
fails in `always` mode and passes in `boundary` mode for a multi-line history
excerpt. `TestTaskStateBoundaryKeepsHiddenFailure` ensures the normalization
does not suppress a fact that is actually gone.

## 6. Phase 4: structured state versus native compaction

The result depends on what the summary preserves.

- **Generic summary (main scenario).** The 84-character summary says only that
  earlier conversation was compacted. It does not contain the diagnostic. A and
  D lose the fact at t5; B and C recover it from structured state. This is the
  value that 004A demonstrated: exact tool-reported diagnostics and provenance
  that a lossy summary omitted.
- **Sourced summary (summary-source scenario).** The 202-character summary
  preserves the source command and diagnostic verbatim. D answers correctly
  without structured state. B still spends 174 tokens repeating the finding
  because 004A compares with exact string containment against a summary
  containing newlines and tabs. C spends 40 tokens on the objective and trusts
  the summary. Structured state adds no answer value here, but it preserves the
  record and its verification/supersession metadata for later turns.
- **What structured state adds beyond a good summary:** typed verification
  (`tool_reported`, `user_confirmed`, `proposed`), precise supersession when a
  matching rerun succeeds, separation of a user correction from a model
  proposal, and a record that survives even when the summary does not mention
  it. C's summary-equivalence rule keeps those benefits without paying to repeat
  a finding the summary already carries.
- **What it does not add:** when the summary is complete and correct, the
  answer can be produced from the summary alone. Structured state is not a
  replacement for a good summary; it is a fallback for the facts the summary
  loses.

The baseline summary is not weakened. All conditions receive the same summary
text in each scenario. D simply has no structured state and no MPM retrieval.

## 7. Phase 5: measurement

Three deterministic trials per condition and scenario. Prompt/completion tokens
come from the recording provider. `Task tokens` is the task-state part summed
over every request in the trial. Latency is wall-clock process time and is not
a model-latency measurement.

### 7.1 Main seven-turn scenario

| Condition | Requests | Tool calls | Prompt | Task tokens | Completion | Total | E2E mean +/- sd (ms) | Correct |
|---|---:|---:|---:|---:|---:|---:|---:|---:|
| A `history_cache` | 12 | 4 | 6,022 | 0 | 107 | 6,129 | 304.7 +/- 17.7 | 3/3 |
| B `always_state` | 12 | 4 | 7,354 | 1,181 | 108 | 7,462 | 322.7 +/- 3.2 | 3/3 |
| C `boundary_state` | 12 | 4 | 6,694 | 545 | 108 | 6,802 | 350.7 +/- 23.2 | 3/3 |
| D `summary_only` | 12 | 4 | 6,374 | 0 | 107 | 6,481 | 123.3 +/- 5.5 | 3/3 |

B versus C: -660 prompt tokens (-9.0%), -636 task-state tokens (-53.9%), same
requests, tool calls, completion tokens and correctness.

### 7.2 Two-failure scenario

| Condition | Requests | Tool calls | Prompt | Task tokens | Completion | Total | E2E mean +/- sd (ms) | Correct |
|---|---:|---:|---:|---:|---:|---:|---:|---:|
| A `history_cache` | 10 | 6 | 4,603 | 0 | 51 | 4,654 | 280.0 +/- 5.0 | 3/3 |
| B `always_state` | 10 | 6 | 5,622 | 958 | 44 | 5,666 | 296.3 +/- 13.1 | 3/3 |
| C `boundary_state` | 10 | 6 | 5,340 | 659 | 44 | 5,384 | 270.3 +/- 15.1 | 3/3 |
| D `summary_only` | 10 | 6 | 4,928 | 0 | 51 | 4,979 | 124.3 +/- 8.1 | 3/3 |

C keeps the same partial-supersession answer as B while removing 299 task-state
tokens (-31.2%).

### 7.3 Sourced-summary scenario

| Condition | Requests | Tool calls | Prompt | Task tokens | Completion | Total | E2E mean +/- sd (ms) | Correct |
|---|---:|---:|---:|---:|---:|---:|---:|---:|
| A `history_cache` | 7 | 2 | 2,958 | 0 | 90 | 3,048 | 224.3 +/- 19.1 | 3/3 |
| B `always_state` | 7 | 2 | 3,441 | 430 | 90 | 3,531 | 257.0 +/- 8.5 | 3/3 |
| C `boundary_state` | 7 | 2 | 3,081 | 80 | 90 | 3,171 | 219.0 +/- 21.0 | 3/3 |
| D `summary_only` | 7 | 2 | 3,188 | 0 | 90 | 3,278 | 92.0 +/- 2.6 | 3/3 |

C removes 350 task-state tokens (-81.4%) relative to B, because the sourced
summary already carries the diagnostic and only the objective remains resident.

### 7.4 Variance and latency

- Token counts, request counts, tool-call counts and turn outcomes are identical
  across all three trials of each condition: prompt-token standard deviation is
  exactly 0.
- E2E standard deviations are 3-23 ms. They reflect process and MPM subprocess
  scheduling, not model speed. C is not consistently faster than B (main:
  350.7 vs 322.7 ms; multi-failure: 270.3 vs 296.3 ms; summary: 219.0 vs
  257.0 ms). No latency improvement is claimed.
- D is faster in every scenario mainly because it runs no MPM retrieval
  subprocess, not because of the summary.
- Every one of the 36 trials passes its condition-specific checks. The
  difference is what the treatment can recover after compaction, not whether
  the deterministic script completes.

## 8. What this shows and does not show

It shows, deterministically, that:

- the 004A overhead is dominated by repeated facts (t2/t3), routine filesystem
  successes and compaction bookkeeping (t4-t7), not by the hidden diagnostic
  itself;
- whitespace-normalized visibility removes the duplicate failures without
  removing the hidden failure;
- boundary residency preserves the post-compaction diagnostic, partial
  supersession, proposed verification, the new-objective reset, project/session
  isolation, and the MPM read-only boundary;
- a properly sourced native summary can answer the follow-up by itself, and
  boundary residency avoids repeating that finding while always-resident state
  does not;
- no schema, store, agent loop, database, or extra LLM call was added.

It does **not** show that:

- a live model will use the retained fact as reliably as the mock script, or
  that these token estimates match a real provider tokenizer. Token estimates
  are `chars / 4` for the block and mock-provider usage for the request. The
  003G live harness remains unavailable because its credential expired; no live
  measurement exists for 004B.
- boundary residency is always cheaper in wall-clock time. Token reduction is
  established; latency reduction is not.
- the summary-equivalence rule handles paraphrase. Only exact normalized
  containment is recognized. A paraphrased summary leaves the entry resident,
  which is a token cost but not a recall loss.
- routine-success suppression is risk-free. A write success is re-derivable
  from the workspace and from failures/commands, but if a user asks a question
  whose only answer is "a write succeeded" after the tool result has left
  history, boundary mode will not volunteer the stored observation. This is a
  deliberate trade-off: routine mechanics are dropped, failures and command
  observations are kept.
- the active objective is always suppressed when unnecessary. It remains
  resident after compaction (40 tokens at t7) because its quote is no longer in
  the visible window. This is conservative and preserves continuity.
- the final t6 request is perfectly current. Supersession is recorded after
  that request is assembled, so both B and C still show the failure there; the
  next turn is clean and the answer is correct. This is capture timing, not
  residency.

The production default is now `boundary`. `DEEPCLAW_TASK_STATE_RESIDENCY=always`
restores exact 004A behavior for comparison or rollback. The task-state store,
isolation protection, four-tool profile, D8 boundary and MPM read-only rule are
unchanged.

## 9. Artifacts

Local, gitignored evidence under `.lab/bench/004b/`:

- `experiment-c-20261030/` - 36 trial records, raw prompts, `analysis.json`,
  `summary.json` and `manifest.json`
- `audit-main.json` - per-request 004A/004B block audit
- `audit.py` - audit generator
- `validation/go-test.txt` - full `go test` output at the code commit
- `validation/go-build.txt` - build output (empty on success)

`.lab/` is ignored by git; the report records the exact commands so the
artifacts can be regenerated.

## 10. Verification

Exact commands, run from `/home/v/projects/deepclaw`:

```bash
export HOME=$PWD/.lab/home
export GOCACHE=$PWD/.lab/cache/go-build
export GOMODCACHE=$PWD/.lab/cache/gomod
export GOTMPDIR=$PWD/.lab/tmp TMPDIR=$PWD/.lab/tmp

go test -tags 'goolm,stdjson' -count=1 -v -timeout 20m \
  ./pkg/agent ./pkg/bench
go build -tags 'goolm,stdjson' ./...

go build -tags 'goolm,stdjson' -o .lab/bin/bench ./cmd/bench
.lab/bin/bench run --experiment C --trials 3 --provider mock \
  --mpm-binary $PWD/.lab/bin/mpm --out $PWD/.lab/bench/004b \
  --run-id experiment-c-20261030

python3 .lab/bench/004b/audit.py
```

The final validation run at code commit `a4c2b374` exited 0 for both commands:

```text
ok  github.com/sipeed/picoclaw/pkg/agent
ok  github.com/sipeed/picoclaw/pkg/bench
go build -tags 'goolm,stdjson' ./...   # exit 0
```

Added or extended tests:

- `pkg/agent/task_state_test.go`: residency mode parsing; whitespace duplicate
  suppression; routine/bookkeeping suppression; hidden failure retention;
  summary equivalence; completed objective; partial supersession.
- `pkg/bench/task_state_test.go`: four-condition residency comparison;
  two-failure partial supersession; sourced-summary equivalence; prompt-level
  assertion that control prompts contain no `task.state` part; assertion that
  task observations never enter MPM state.
