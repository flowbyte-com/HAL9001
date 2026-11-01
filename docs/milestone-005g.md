# Milestone 005G: genuine working-memory evaluation design

Status: **design + deterministic qualification complete. No live trials.**
This milestone answers whether any coding continuation in our reach
genuinely needs task state — and builds the one that does. Production
memory stays frozen at 004C; no fixture, scoring, or production behavior
changed for existing tasks.

- Base: `dcf3c9c0` (005C report).
- Code: `939d33e7` (eligibility gate + `tiered_migration` fixture).
- Previous HEAD: `dcf3c9c0`, clean at start.
- Go: `go1.27.3`; bubblewrap 0.11.1.
- Qualification: full `go test ./pkg/...` (76 packages ok),
  `go build`, `go vet`, `gofmt` clean; fixtures 6/6 valid; mock D
  12/12 pass (6 pairs tie).
- No live calls, no pushes, no deploys, no live MPM/OpenCode/scheduler
  or baseline changes.

Acceptance answer up front: **of the existing five tasks, zero present
a genuine memory opportunity** — verified per task below. One new
fixture, `tiered_migration`, is constructed, qualified, and
gate-demonstrated to present one. Whether the mechanism, the model, or
the outcome responds is a live question, explicitly not claimed here.

## 1. Phase 1 — what the 005F traces actually show

All evidence below is observed (session files, raw requests, diffs,
snapshots), not inferred intent.

**`compaction_ledger` (control, 005F, full success).** t1 fixed
`balance.go` and re-ran checks; one automatic threshold compaction fired
across the trial; the t3 explicit `/compact` succeeded and t3 finished
the validation fix (`RecoveredAfterCompaction` true). The summary
preserved the diagnosis, the fix, the deferred failure, and even the
likely next fix — and t3 implemented exactly that hint. The workspace
kept the full diff (`M balance.go`). The pre-t3 evidence never left the
trial: visible history shrank, but the summary and the workspace
retained everything t3 needed. t3's need was also independent of t1's
findings (separate defect). No qualifying loss.

**`restart_parser` (control, 005F, full success).** Zero compactions
anywhere; 54 history records intact across a true 2-process restart
(history 23→32); t2 re-read `token.go`/`parse.go` from the workspace
rather than consulting the `MEMORY.md` notes t1 had written (verified:
no memory read in t2; notes cite only public test names). Restart
mechanics validated; nothing was at risk of loss.

**Shipping, cart, billing (005F control).** No compaction and no
restart touched the decision turns; histories (≤50 records) fit
comfortably. The billing correction was visible in the current user
message throughout and still unimplemented — consistent with 005C, a
planning gap, not a memory gap.

**005B treatment snapshots (both conditions ran the same fixtures).**
Task state faithfully recorded tool observations (e.g., 29 entries for
shipping: 15 observations + 14 failures, all `tool_reported`; the
rendered block quotes real commands with real errors). It was rendered
(3–7k prompt tokens per trial) into histories that already contained
the same facts. No decision demonstrably depended on it — both
conditions looped identically on ledger and converged identically on
parser.

**Net: five tasks examined, zero genuine information-loss
opportunities.** In every case the evidence either survived in ordinary
history, survived in a compaction summary, lived in the workspace, was
never needed downstream, or some combination thereof.

## 2. Phase 2 — the eligibility gate

`pkg/bench/eligibility.go`. A continuation qualifies only if **all**
hold:

1. **Boundary** — a compaction or process restart actually occurred.
2. **Dependence** — a prior finding F is required by a subsequent
   coded decision (a hidden test or scored criterion needs F).
3. **Capturable** — F could legitimately enter task state from existing
   events (tool observations, marked user state, runtime events; never
   model proposals or hidden content).
4. **Loss** — F's evidence markers are absent from post-boundary
   visible history **and** from the compaction summary.
5. **Recorded recoverability** — whether F is re-derivable from
   workspace/checks and at what measured cost. Recoverability does
   **not** disqualify: the control may legitimately re-derive F, and
   that cost bounds the treatment's measurable advantage.

The gate never sees the eventual outcome. Markers must be specific
recorded strings (exact values, commands, or findings), never generic
terms — a marker present merely because it is a common word proves
nothing. `SessionEvidence` rebuilds post-boundary history and summary
text from trial artifacts for the check.

## 3. Phase 3 — one fixture that qualifies

Since no existing task can lose what it never needs (ledger/parser) or
ever loses anything (the rest), a memory test needs: (a) a verbose
diagnostic whose specifics scroll out of working memory, (b) a later
decision that genuinely requires those specifics, and (c) a control
path that can re-derive them at measurable cost.

`tiered_migration` (new; existing fixtures untouched):

- Repo: tiered rate service; `Migrate` maps legacy codes with a
  single-boundary bug (G4→800 instead of 650); `RoundEdge` truncates
  instead of banker's-rounding.
- t1 (verbose dry run): `go test -run TestDryRunMapping -v` prints the
  full 21-row legacy→current table, exposing the G4 mis-mapping; fix
  `Migrate`; leave rounding for later.
- Interruption: real `/compact` before t3 (3-turn shape, as validated).
- t2 (post-compaction): fix `RoundEdge` to banker's rounding **and**
  pin the original bug with a prescribed `TestG4Regression` asserting
  the current rate **and recording the exact pre-fix wrong value
  (800)**. That value exists nowhere post-fix except the dry-run
  output: the code is fixed, re-running shows 650, and only git
  archaeology or memory recovers what the bug produced.
- Scoring (12 pts): build, public, hidden mapping/banker's suites,
  plus `agent_test:TestG4Regression` (must run green) and
  `file_contains: regression_test.go / 800` (must pin the value).
  New criterion kinds, validated; old fixtures unaffected.
- Evidence markers (pre-registered in `task.json`): two late dry-run
  rows (`tier G7…`, `tier G6…`) that mock summarization truncates away.

Why this dependence is genuine: without the pinned 800 the regression
test cannot satisfy `file_contains`; without the edge mapping the
reviewer cannot confirm the fix scope. The control's honest path is
re-running the dry run or git archaeology (counted); the treatment's
is recalling task-state tool observations of that output.

## 4. Phase 4 — live comparison design (pre-registered, not run)

- A: control (`DEEPCLAW_TASK_STATE=0`); B: treatment (004C `boundary`).
- Identical: MiniMax-M3.1-Flash, temp 0, 8K context (task-defined
  memory-test sizing, consistent with the 005B-R ledger window),
  max_tokens 1024, max tool iterations 20, four coding tools,
  bubblewrap policy, task scripts, hidden tests, scoring, budgets,
  request-boundary ceilings (8M total / 2M per trial, same scale as
  the authorized 005B pilot).
- Randomized order within pairs, fresh isolated trials, 5 paired
  repetitions proposed (50-trial expansion explicitly excluded).
- Primary endpoint: full-success pairs; secondary: partial points,
  re-investigation counts (dry-run re-runs, repeated reads/checks),
  provider tokens, task-state residency, compaction/restart behavior,
  and the mechanism checks of Phase 5.
- Benefit = treatment wins full successes or reaches them with fewer
  re-investigations at equal tokens; harm = the reverse or added
  violations/cost; null = ties. With 5 pairs this is descriptive, as
  in 005A — no significance claims.

## 5. Phase 5 — measuring use, not just presence

For each paired trial, from trusted traces + session joins:

- Was the dry run re-executed after the boundary (control behavior)?
  Count dry-run/tool invocations post-boundary per side.
- Did t3's fix reference mapping values that appear only in the
  pre-boundary output (treatment behavior)?
- Task-state audit: were dry-run observations captured (entries),
  rendered at t3 requests (prompt tokens), and is the pinned value
  consistent with them rather than hallucinated?
- Effort deltas: requests, tool calls, repeated reads/checks,
  provider tokens, time.
- Distinguish: **memory not captured** (no dry-run entries at t3)
  vs **captured but not rendered** (entries present, zero t3 prompt
  tokens) vs **rendered but ignored** (used re-run anyway / wrong
  value). Only the last two implicate the model rather than the
  mechanism; only the first implicates the mechanism.

## 6. Phase 6 — deterministic qualification

- Gate unit tests: all six branches (eligible, no-boundary,
  no-dependence, not-capturable, markers-undefined,
  retained-in-history, retained-in-summary). Pass.
- `TestDeliberatelyIneligibleTrialIsReported`: ordinary mock trials
  (history intact) are ineligible via `evidence-retained`. Pass.
- `TestEligibleTrialAfterGenuineLoss`: verbose mock on the new
  fixture at 8K compacts away the dry-run rows (auto-compactions
  observed, markers absent from both history and summary) →
  eligible, with recoverability recorded. Pass.
- `SessionEvidence` unit test (synthetic session + meta). Pass.
- Fixtures 6/6 valid (new: deterministic commit, builds, 4 hidden
  failures pristine, partial 0.083). Mock D: 12/12 pass, 6/6 ties.
- New `file_contains`/`agent_test` criterion kinds, mock
  trajectories (normal + verbose), and the verbose mock itself are
  covered by the same run.
- Full `pkg/...` suite green (76 packages), build, vet, gofmt clean.
- Mock token behavior is unchanged for old tasks; one documented
  mock property the gate relies on: mock compaction summaries are
  content-free headers, so mock trials exercise the *loss* branch by
  construction. Live summarizers vary — which is exactly why the
  gate checks content rather than assuming loss.

## 7. Phase 7 — live gate report (no live execution)

- **Eligible tasks: 1 candidate** (`tiered_migration`), demonstrated
  under deterministic mock compaction. Live confirmation (verbatim
  marker check on live summaries) is pending and required before any
  outcome claim: if a live summarizer retains the dry-run rows, the
  task is ineligible in that run and must be reported so, not scored
  as memory evidence.
- **Existing fixtures: 0 eligible** (Phase 1 table; no loss, or no
  dependence, or both).
- **Remaining condition differences:** only the 004C task-state layer
  (boundary residency), as in 005A; MPM and resident cache stay
  disabled; the 8K window is task-defined and identical both sides.
- **Proposed live sample:** 5 paired repetitions of the one task
  (10 trials), matching the 005A sample size.
- **Expected usage:** mock mean ~18k reported tokens/trial; the 005B
  pilot ran ~5–10× mock tokens live, so ~100–250k/trial expected,
  ~1–2.5M for 10 trials; proposed ceiling 8M total / 2M per trial
  (same scale as the previously authorized pilot, with the same
  abort rules: auth/quota/isolation/ledger failures, missing usage,
  invalid states stop the run).
- **Risks:** live summarizer retains markers (→ ineligible that run);
  verbose live traffic trips runaway guards (→ valid partials, no
  comparison); regression-test naming non-compliance voids scoring
  (prescribed name is explicit); model variance dominates n=5.
- **Authorization required before any live trial.** No pilot, no
  expansion, no credential use in this milestone.

## 8. The three-way distinction, as of now

1. **The mechanism can preserve evidence.** Demonstrated: 005B
   treatment snapshots recorded dry-run-equivalent tool output;
   ledgers persist it crash-safely (005B-R).
2. **The model can use it.** Unknown — 005C showed models ignoring
   even visible instructions; no live retention case exists yet.
3. **Outcomes improve.** Unknown — pending the authorized comparison.

No production memory change is justified: the mechanism works, the
open question is entirely about model use of retained evidence in a
genuinely lossy situation, which only the proposed live run can
answer.

Artifacts: `docs/milestone-005g.md` (this file);
`pkg/bench/eligibility*.go`, `pkg/bench/testdata/005a/tasks/tiered_migration/`,
`pkg/bench/mock_provider.go` (trajectories), `pkg/bench/coding_*.go`
(`file_contains`/`agent_test`, markers); qualification evidence under
`.lab/bench/005g/` (fixtures, `mock-tier`, `mock-all-6t`, full test
log). 005F evidence preserved unchanged. Nothing pushed or deployed.
