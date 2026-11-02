# Milestone 005F: live coding-competence baseline

Status: **complete. 5/5 harness-valid live trials, full redacted evidence
preserved.** This is a capability baseline under the repaired harness and
Pi-like shell — not a memory comparison (task state and MPM disabled) and
not a controlled comparison against 005B (window, shell policy, and
trajectories all changed there).

- Code: `92ab54f2` (bench-only: 32K run override, no-change-vs-unknown
  file tracking fix).
- Previous HEAD: `d93320a1` (005D report).
- No production, fixture, instruction, or scoring changes.
- Provider: MiniMax `MiniMax-M3.1-Flash`, international endpoint
  `https://api.minimax.io/v1`, existing project-scoped credential.
- 004C task state: `DEEPCLAW_TASK_STATE=0` both... (single control
  condition); MPM retrieval and resident cache: disabled.
- Context window override: **32768 on all five tasks** (fixture files
  untouched; recorded as `context_window_override` in the manifest).
  Everything else fixture-defined: max_tokens 1024, max tool iterations
  16/16/16/20/20, summarize 75/75/75/60/75, temperature 0, four-tool
  profile, native compaction and restart enabled.
- Run: `005f-baseline-20261104`, seed 20261104, 5 control trials.
- Budget authorized: 10M total / 2M per trial. Consumed: **596,526
  reported, 0 reserved, 0 unknown, 0 missing usage (5.96%)**.
  Max single trial 207,408 (10% of its cap). No aborts, no reruns.

## 1. Offline qualification (before any live request)

- Fixtures 5/5 valid (deterministic commits, pristine hidden-test
  failures unchanged).
- `005B-R` guard/ledger/crash tests, 005D trace/metric/sandbox tests,
  and the full `go test ./pkg/...` suite green on re-run (76 packages;
  one unrelated `TempDir` cleanup race in `pkg/agent` failed once in a
  full-suite run and passes in isolation and in per-package runs —
  pre-existing flake, also seen in the 004C cycle, unrelated to this
  milestone's files).
- `go build`, `go vet`, `gofmt` clean.
- Mock D run at the overridden 32K: 10/10 pass, 5/5 ties, restart
  history continuity and explicit-compaction assertions intact, no
  task limits — the window change does not invalidate history/restart
  mechanics.
- 005E bypass verified live-fire in tests: `go test ./...`,
  heredocs, `$(...)`, absolute in-workspace paths, `chmod`/`rm`
  execute; MPM store reads masked, writes fail at the OS boundary,
  capability env scrubbed; `GOCACHE=$PWD/.gocache` works, relative
  bare `GOCACHE=.gocache` is rejected by Go itself.

## 2. Live results

| Task | Outcome | Partial | Effort | Key observation |
|---|---|---|---|---|
| `compaction_ledger` | **full 10/10** | 1.000 | 26 req / 37 tools / 160k tok | t1 fix, t2 confirm, t3 real `/compact` + validation fix; `RecoveredAfterCompaction` true |
| `misleading_failure` | **full 7/7** | 1.000 | 11 req / 20 tools / 50k tok | README contract applied via `edit`; contradictory legacy notes correctly ignored |
| `multi_file_cart` | fail | 0.364 (4/11) | 7 req / 14 tools / 32k tok | zero edits — investigated, stated the contract, stopped before acting |
| `requirement_change` | criteria-complete, **constraint-violated** | 1.000 but `full_success=false` | 21 req / 24 tools / 189k tok | banker's rounding correctly implemented (`math.RoundToEven`); agent also rewrote protected `README.md` |
| `restart_parser` | **full 9/9** | 1.000 | 21 req / 32 tools / 165k tok | both files fixed in t1; clean post-restart verification in t2 (2 processes); `RecoveredAfterRestart` true |

All trials harness-valid, fully metered (usage ledgers reconcile with
zero unknown/reserved), zero guard-denials in traces (005E bypass
active), zero MPM calls, task-state tokens 0 everywhere.

## 3. Analysis

**Previously blocked operations now execute.** The 005B pilot's
treatment died fighting `go test ./...`, `/dev/null` redirects, and
`GOCACHE` workarounds; the baseline agent ran `./check.sh`,
`go test ./...`, `go vet ./...`, heredocs to `/tmp`, `chmod`, and
`$(...)` without a single guard denial. The sandbox frictional
failures are gone as a failure class in this run.

**Documented instructions are followed — except the cart stop.**
`./check.sh`-until-green was obeyed in 4/5 tasks (ledger, shipping,
billing, parser all ended with passing checks). The cart agent ran the
check, read the failures, inspected its own session files, wrote
"Now let me fix the code", and ended the turn with zero edits.

**Required edits happen in 4/5 tasks** (balance, rate boundary,
rounding, both parser functions). The exception is cart (no edits at
all) — a pure intent-action gap, now with a working shell and passing
checks available, so the cause lies in the model, not the
environment.

**No runaway loops.** No task limits fired, no max-iteration
exhaustions, no compaction churn (one threshold compaction total, in
ledger t3 alongside the explicit one). Longest repeated-command runs
are ordinary re-verification (`./check.sh` after each fix).

**Remaining failures are model reasoning, not environment.**
Cart: failure to act despite understanding. Billing: correct
implementation paired with an unnecessary protected-file edit
(functionally perfect, procedurally violating — the scorer rightly
withholds full success). No failure in this run is attributable to
tooling, sandbox, budget, or instrumentation.

**vs 005B pilot (uncontrolled comparison — window 8K→32K for ledger,
lexical guard → bypass, different trajectories):** ledger went from
t1 task-limit + void to full success with a real explicit compaction;
shipping control repeated full success; billing went from pristine
no-op to functionally-correct-but-constrained; parser repeated full
success; cart is unchanged (pristine no-op in both). Direction is
consistent with "environment no longer blocks", but no causal claim
is licensed: too much changed at once, by design of this milestone.

**No memory inference is licensed.** Task state was off; the 005C
null result stands unmodified.

## 4. Limitations and expansion

- n=5, one model, one host, one day: a capability snapshot, not a
  capability ranking.
- Billing's README edit shows agents will touch protected-adjacent
  files when it suits the narrative; constraints caught it, but a
  security-sensitive deployment should note the intent.
- The billing t2 correction was followed this time and ignored last
  time under near-identical prompts — model variance dominates
  single-sample correction studies.
- The TempDir cleanup flake (above) is unrelated but recorded.
- No 50-trial expansion was authorized or started. A memory-contrast
  follow-up (005C §7B) remains the recommended next step; this
  baseline is the control side it would compare against.

Artifacts: `.lab/bench/005f/005f-baseline-20261104/` (manifest with
override + seed + order, summaries with coding pairs and token
budgets, per-trial results, raw traces, usage ledgers, tool traces,
redacted logs), `fixtures/verification.json`, full-suite logs. No
push, no deploy, no live MPM/OpenCode/scheduler/baseline changes.
