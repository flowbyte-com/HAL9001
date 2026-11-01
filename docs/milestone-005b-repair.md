# Milestone 005B-R: live-benchmark reliability repair

Status: **repair implemented and qualified offline. No live trial was run.**
This milestone answers the abort of the 005B pilot, not a memory question:
production task state stays frozen at 004C and no production file was
touched.

- Base: `e957f676` (005B pilot report).
- Repair code: `7945754` (`bench: make 005b usage evidence crash-resilient
  and bound runaways`).
- Previous HEAD before repair: `e957f676`, clean.
- Go: `go1.27.3`; sandbox: bubblewrap 0.11.1.
- Offline qualification: full `go test ./pkg/...` (76 packages ok),
  `go build ./...`, `go vet ./pkg/...` all exit 0;
  `fixtures --verify` 5/5; capped mock D run 10/10 ties.
- Evidence: `.lab/bench/005b-repair/` (gitignored).
- The aborted live trial is preserved untouched at
  `.lab/bench/005b/005b-pilot-20261101/`.

Acceptance criterion: **a failed coding trial may lose its result, but must
not silently lose the evidence of its provider requests or undermine the
spending limit.** Every dispatched request is now recoverable or explicitly
marked unresolved, and ceilings are enforced against known plus reserved
spend.

## 1. Phase 1 — failure reconstruction

### 1.1 Timeline (control `compaction_ledger`, run `005b-pilot-20261101`)

| Seq | Observation (from preserved `trial.log` / `process.log`) |
|---|---|
| 1 | t1: agent fixes `balance.go` correctly with 1 edit, then loops: re-reads, re-runs `sh check.sh` (validation still fails as designed for t1), tries absolute-path commands blocked by the safety guard |
| 2 | t1 hits `max_tool_iterations` (20) without a final response → 20 requests |
| 3 | t2 repeats the pattern (reads, checks, guard-blocked commands) → 20 more requests |
| 4 | Every request past the first few exceeds the 4300-token threshold → 25 threshold compactions, 21 session-summarize events (summaries ~850–1560 chars, 3–9 messages each, 5–8 kept) |
| 5 | Prompt oscillates 4300–5600 tokens and never drops below threshold; two pre-checks need a second pass |
| 6 | t3's designed explicit `/compact` finds nothing older to compress → `turn t3 compact produced no summary` → trial error |
| 7 | Child writes no `result.json` and no raw trace; parent records `trial process produced no readable result` and stops the run (1/10, metered total 0) |

Totals: 40 `LLM request` lines, 73 tool executions (45 bash, 27 read, 1 edit), 10 failed tool calls, 2 iteration exhaustions, 25/21 compactions/summaries, 0 recorded usage objects.

### 1.2 Root causes, separated

**A. Coding-agent behavior (model + task + equal-condition params).**
The live model fixed the balance bug early but never produced a final
response, looping over checks and exploratory commands (some outside the
sandbox, correctly blocked) until the per-turn iteration budget fired
twice. `max_tokens=1024` lets each completion add ~1k tokens, so prompts
grow fast. None of this is task-state signal; both conditions share the
model, tools, and budgets.

**B. Compaction implementation (production behavior, not a 005B-R change).**
Threshold = 60% × (8192 − 1024) = **4300 tokens**. Each tool-loop
iteration appends a ~1k completion plus tool outputs while each summarize
pass removes only 3–9 messages, so the prompt re-crosses the threshold on
the next request: steady-state churn that costs a summarizer call per
request without ever getting below 4300. Up to 3 passes run per request;
`pass=2` events show even double summarization failing to clear it. The
policy has no cooldown, no progress check, and no attempt cap. This is
distinct from verbosity alone: a shorter completion would still churn
because removal per pass is smaller than growth per iteration at this
window size.

**C. Fixture assumptions.**
The 8192-token window, 60% threshold, and the explicit-`/compact` event
were validated against mock-scale traffic (13 requests, ~24 completion
tokens total, zero threshold compactions ever). Live traffic is ~3× the
requests and ~40× the completion tokens per request. The parameters
embed a mock-scale assumption the fixture never stated.

**D. Benchmark instrumentation defects (fixed here).**
1. `WriteRaw` and `result.json` were written only on clean completion.
   All 13 early `return fail(...)` paths after recorder creation, plus any
   panic/kill, discarded every in-memory request, tool call, and total.
2. The 40 live responses almost certainly carried provider-reported usage
   (the international probe showed 10/10 reported), but usage lived only
   in memory, so it vanished with the child.
3. The 005B budget guard metered only in-memory totals, so the failed
   trial contributed 0 to pilot accounting while real spend happened.
4. No per-trial request/tool/compaction caps and no no-progress detection:
   the only bounds were per-turn iterations and token allowances, which
   the trajectory respected while still going nowhere.

History at the explicit compact: session file held 10 records (user
message plus summaries and tail); summary ~1k chars; history small but
not compressible further — the property the trial wanted (earlier
evidence out of visible history) had effectively been achieved by the
automatic compactions, yet the trial failed on the technicality that no
*new* summary could be produced.

## 2. Phase 2 — crash-resilient usage accounting

New file `pkg/bench/usage_ledger.go`, wired into `RecordingProvider`:

- One trial-level append-only file,
  `<trialDir>/raw/usage-ledger.jsonl` (shared by all segments of a
  restart trial), mode `0600`, one `fsync` per event. Volume is tens of
  events per trial; cost is negligible next to an LLM round trip.
- `reservation` event (request ID, turn, segment, model, allowance)
  written in `begin()` **before** dispatch — a crash before the response
  leaves the reservation on the books.
- `response` event (request ID, status, reported flag, prompt/completion/
  total/cache tokens, allowance, latency) written in `finish()` before
  returning — a crash after persistence but before finalization is fully
  recovered.
- The ledger never contains prompt/response text, credentials, or
  headers — only IDs, counts, status, latency (asserted by test).
- `RecoverUsageLedger` rebuilds spend: responses supersede reservations
  by request ID (segment-prefixed IDs make cross-segment dedup natural),
  duplicate lines collapse with a counter, corrupt lines are skipped, a
  missing file is not an error.
- Three values stay separate: **known** (reported totals), **reserved**
  (allowances of requests with no recorded outcome), **unknown** (their
  count). Missing usage is never zero: `TrialSpendForBudget` charges
  known + reserved.
- `NewRecordingProvider` seeds its meter from the ledger with `max()`
  against the parent-provided base, so a resumed segment cannot reset
  the budget even if either side is stale.
- `fail()` in `trial.go` now persists partial requests, tool calls,
  totals, budget state, reconciled recovery, raw trace, and result on
  **every** post-recorder failure path — the exact gap that lost the
  aborted trial. The normal path attaches reconciled recovery too.
- The parent (`run.go`) reconciles every completed trial against its
  ledger — including error stubs with no `result.json` — and charges the
  conservative spend to the pilot total. `TokenBudgetSummary` now also
  reports reserved allowance and unknown-outcome counts.

## 3. Phase 3 — budget enforcement across processes

- Request boundary (`requestAllowed`, unchanged semantics): refuse when
  metered base + allowance (context window + max tokens) would exceed
  the per-trial or pilot-total cap. Base = parent-provided prior totals
  + ledger recovery via `max()`.
- Parent pre-check refuses to start a trial once the pilot total is
  reached; `trialAbortReason` stops the run on any harness-invalid
  trial, on per-trial conservative-spend breach, and on unmetered live
  usage — while a harness-valid poor coding outcome never stops it.
- Deterministic tests cover all eight required cases (all offline):
  1. termination before a response → reservation retained, unknown=1;
  2. termination after a response → known, no reservation held;
  3. termination after persistence but before finalization → recovery
     works with no result file at all;
  4. two segments sharing one trial budget → second segment refused;
  5. multiple trials sharing the aggregate → run stops after the cap;
  6. response with missing usage → metering latch, reservation retained;
  7. duplicate event recovery → counted once, duplicates reported;
  8. exhaustion during a tool loop → 4th request refused, prior three
     retained with full ledger history.

## 4. Phase 4 — compaction-fixture repair

The 8192-token window is **kept** (raising it until the benchmark passes
would hide the defect). What changed is the acceptance rule and the
evidence around it:

- A no-op explicit `/compact` is accepted only when history loss was
  already demonstrated: non-empty summary (fresh sessions start empty,
  so a summary proves some compaction ran) **and** remaining history
  within twice the normal protected tail (8 → bound 16). Anything
  larger, or an empty summary, still fails exactly as before.
- Rationale is predetermined and documented here, before any live
  comparison — not fitted to outcomes.
- A deterministic verbose-agent simulation (`VerboseMock`, test-only,
  never for live runs) replays the aborted shape: ~1000-token
  completions, 20 tool actions per turn, repeated failing checks. The
  live shape (no converging response) task-limits in t2 with partial
  7/10 scored and a complete ledger; the converging variant (final
  response after N tools) exercises automatic-then-explicit compaction
  end-to-end and reaches full success through the already-lost path.
- Exercise coverage map: repeated tool calls ✓ (verbose filler),
  automatic threshold compaction ✓ (21 summaries in the live-shape
  test), explicit-after-automatic ✓ (converging test, already-lost
  flag asserted), context-overflow retry ✓ (existing 004C regression
  test), process restart after compaction ✓ (existing restart_parser
  mock test asserts two processes and history continuity), multiple
  summaries ✓ (per-turn `AutoCompactions` now recorded and asserted).

## 5. Phase 5 — runaway-trajectory safeguards

`pkg/bench/runaway.go` (new). All limits are symmetric, predetermined,
and far above mock maxima (mock peak: 13 requests, 9 tool calls,
1 compaction, 1 consecutive identical command, 0 exhaustions):

| Guard | Trip point | Live-shape behavior |
|---|---|---|
| Automatic compactions per turn | ≥ 6 | trips (the churn signature) |
| Automatic compactions per trial | > 12 | backstop |
| Consecutive identical bash per turn | ≥ 6 | — |
| Iteration exhaustion per trial | ≥ 2 turns | trips (treatment path) |
| Requests per trial | > 100 | last-resort backstop |
| Tool calls per trial | > 200 | last-resort backstop |

A trip ends remaining turns as a **task execution limit**: status stays
pass, the repository is scored as-is, `TaskLimitReached/Reason` is
recorded, and a score note is attached. This distinguishes task-level
non-convergence from harness infrastructure error — the aborted trial's
void becomes a metered partial. Postflight interruption checks are
vacuous-passed only when the limit stopped the trial *before* the
interruption turn (property untested, not violated); a reached
interruption turn is still verified normally.

## 6. Phase 6 — experimental fairness

- Production task state frozen at 004C: `pkg/agent` untouched.
- Five fixtures, hidden tests, scoring rules, model settings, coding
  tools, provider config, execution restrictions, and budgets identical
  in control and treatment.
- Protocol changes (ledger, acceptance rule, task limits) apply to both
  conditions; the verbose simulation is test-only and never enabled for
  live runs.
- The aborted live trial is preserved untouched at its original path; no
  rerun, replacement, or erasure.
- `git diff e957f676..HEAD --name-only` touches only `pkg/bench`,
  `cmd/bench`, `docs/milestone-005b-repair.md`, and the new test files.

## 7. Phase 7 — offline qualification

- `go test ./pkg/...`: **76 packages ok, 0 failures** (includes 19 new
  repair tests plus all pre-existing bench/agent suites, credential
  isolation, session-key, and redaction tests).
- `go build ./...` exit 0; `go vet ./pkg/...` exit 0.
- `fixtures --verify`: **5/5 valid**, unchanged commits and hidden-test
  failures.
- Capped mock D run (1M/8M caps): **10/10 pass, 5/5 pairs tie**,
  budget `reported=60105 reserved=0 unknown=0`, no task limits — the
  repair changes nothing on converging trajectories.
- Before/after on the live shape (verbose simulation): before, the
  trajectory class produced a harness error with zero surviving
  evidence; after, control task-limits in t2 (compaction churn) and
  treatment in t2 (iteration exhaustion), both status pass, partial
  7/10 scored, ledger complete with 0 unknown.
- Before/after on fail paths: a budget-blocked trial previously left no
  `result.json`; now it persists status, error, requests, recovery, raw
  trace, and result.

Reproduce:

```bash
export HOME=$PWD/.lab/home
export GOCACHE=$PWD/.lab/cache/go-build GOMODCACHE=$PWD/.lab/cache/gomod
export GOTMPDIR=$PWD/.lab/tmp TMPDIR=$PWD/.lab/tmp
go test -tags 'goolm,stdjson' -count=1 -timeout 25m ./pkg/...
go build -tags 'goolm,stdjson' ./...
go vet -tags 'goolm,stdjson' ./pkg/...
.lab/bin/bench fixtures --verify --out "$PWD/.lab/bench/005b-repair/fixtures"
.lab/bin/bench run --experiment D --trials 1 --provider mock \
  --token-budget-total 8000000 --token-budget-per-trial 1000000 \
  --run-id mock-d-capped --out "$PWD/.lab/bench/005b-repair"
```

## 8. Recovered-usage semantics and remaining risks

Charged spend per trial = `max(in-memory reported, ledger known)` +
ledger reserved. Unknown outcomes keep their full allowance reserved;
they are reported separately and never zeroed. A failed trial retains
its measured and reserved consumption in `UsageRecovery` on the result
and in the run's `TokenBudgetSummary`.

Remaining risks:

1. The repair is validated offline only — no live MiniMax call was made
   in this milestone (forbidden). Live behavior of the guards
   (especially the already-lost acceptance on real summaries) awaits an
   explicitly authorized pilot.
2. Account-level spend can still precede harness awareness by one
   in-flight request; the allowance bounds that window per request.
3. Threshold-compaction churn is production behavior (frozen); verbose
   models may task-limit often. That is now a valid, scored outcome
   rather than evidence loss, but it caps achievable full-success rates
   on the 8192-token task until parameters are revisited in a future
   design amendment.
4. Mock token counts vary ±1 with temp-dir path lengths (system prompt
   embeds the workspace path); tests assert same-run relationships,
   never absolute totals.

## 9. Resume-readiness verdict

The harness is **safe to attempt a new live pilot** — failed trials can
no longer silently lose request evidence or undermine the ceiling — but
a new run requires fresh explicit authorization (new run-id, reaffirmed
budget; the previously authorized spending ceiling must not be reused
automatically), and this report's predetermined rules (window, bounds,
guard thresholds) must be accepted as the protocol going in.
