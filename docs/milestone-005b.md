# Milestone 005B: live coding-agent evaluation

Status: **renewed 10-trial pilot complete (10/10 harness-valid, 0 errors).**
Control full success 2/5, treatment 1/5; 1 control-won pair, 0 treatment-won,
4 ties. No significance claimed (n=5, descriptive only). The 50-trial
expansion is not justified by this signal — see §7.

- Production memory: frozen at 004C
  (`0ebc816eb56ed3c1769342eb4701996921ff0026`); no production behavior changed.
- HEAD: `becbbfc99b9dc131118d46fea59a02c819e70acd` (clean; verified
  `git status --porcelain` empty at qualification time).
- Code under test is identical to the 005A qualification (`aafe7bdb`);
  `becbbfc9` adds only `docs/milestone-005a.md`.
- Benchmark binary: `.lab/bin/bench`
- Binary SHA-256: `8983b4fb325b5d8c153108a2fa1d4e18636ee200376566eb3a5c675a3de8bb2a`
  (rebuild at `becbbfc9`; differs from the 005A report's `4a46f7de...` only
  because Go embeds the VCS revision, which moved from `aafe7bdb` to the
  docs commit `becbbfc9` with identical code).
- Go: `go1.27.3`; sandbox: bubblewrap 0.11.1, isolation enabled.
- Offline qualification: `.lab/bench/005b/qualification/`
- Auth probe (mainland base): `.lab/bench/005b/005b-auth-probe/` (1 trial,
  0 pass, 0 fail, 1 error; HTTP 401).
- Auth probe (international base): `.lab/bench/005b/005b-auth-probe-io/`
  (1 trial, 1 pass, 0 fail, 0 error; HTTP 200, model responses received).
- Pilot run: `.lab/bench/005b/005b-pilot-20261101/` (1 trial attempted,
  0 pass, 0 fail, 1 error; runner stopped early with reason recorded).
- No live comparison exists. There is nothing to score as a paired result.

## 1. Stage 1: qualification gate

### 1.1 Offline qualification — pass

Rebuilt from the clean HEAD and re-ran the full offline gate:

- `fixtures --verify`: **5/5 valid**, deterministic commits, every pristine
  repo builds and fails at least one hidden test:

| Task | Starting commit | Hidden on pristine repo | Public failures | Pristine partial |
|---|---|---:|---:|---:|
| `multi_file_cart` | `63014eb0236567caf3a0fca66ce58224b9a0094e` | 4 fail | 2 | 0.36 |
| `misleading_failure` | `1a4b0d0ac074e9f0eace21643c48fcad71c51024` | 1 fail | 1 | 0.57 |
| `requirement_change` | `2775a29c59562d7a6e348b0afe699b214646fb5b` | 3 fail | 1 | 0.12 |
| `compaction_ledger` | `e1fa55fbb41c3523c473b4e7c029835a26bfd51a` | 3 fail | 2 | 0.40 |
| `restart_parser` | `a8166691dbfe1030e40acd1f38f6596015ff635d` | 4 fail | 2 | 0.22 |

- Mock end-to-end (`005b-qual-mock-d-1t`, 5 tasks x 2 conditions x 1 rep):
  **10/10 trials pass, 5/5 pairs tie**, 0 constraint violations, 0 MPM
  retrieval, 0 resident records, identical four-tool profile
  (`bash,edit,read,write`) in every trial, restart tasks in exactly 2
  processes with history continuity 12 → 12.
- Full verification at this HEAD: `go test ./pkg/...` exit 0 (76 packages ok,
  no failures), `go build ./...` exit 0, `go vet ./pkg/...` exit 0.
  Logs: `qualification/go-test-pkg.txt`, `go-build.txt`, `go-vet.txt`.
- Sandbox: `isolation enabled` in every trial preflight; bubblewrap 0.11.1
  present on the host.

### 1.2 Condition parity (planned live config) — verified in code and mock

Both conditions use the same binary, fixture commits, turn scripts, context
windows, token/iteration caps, temperature 0, compaction policy and execution
limits. The only intended difference is the task-state layer.

| | Control | Treatment |
|---|---|---|
| `DEEPCLAW_TASK_STATE` | `0` | `1` |
| Residency | none | `boundary` (004C) |
| Session history / native compaction | enabled | enabled |
| MPM retrieval | disabled (`DEEPCLAW_MPM_ENABLED=0`) | disabled |
| Resident MPM cache | disabled (`DEEPCLAW_ACTIVE_MEMORY=0`) | disabled |
| Coding profile | `bash,edit,read,write` | same four tools |
| Task-state budget | 500 | 500 |
| Temperature | 0 | 0 |

Per-task budgets (identical in both conditions, from `task.json`):

| Task | Context | Max tokens | Max iterations | Summarize % | Turns | Interruption |
|---|---:|---:|---:|---:|---:|---|
| `multi_file_cart` | 32768 | 1024 | 16 | 75 | 1 | none |
| `misleading_failure` | 32768 | 1024 | 16 | 75 | 1 | none |
| `requirement_change` | 32768 | 1024 | 16 | 75 | 2 | none |
| `compaction_ledger` | 8192 | 1024 | 20 | 60 | 3 | `/compact` before t3 |
| `restart_parser` | 32768 | 1024 | 20 | 75 | 2 | process restart before t2 |

Preflight enforces the parity: `coding_task_state_matches_condition`,
`coding_mpm_disabled`, `coding_resident_cache_disabled`,
`coding_fixture_clean`, `coding_hidden_tests_absent`,
`coding_known_starting_commit`, plus
`provider_config_outside_agent_paths`. The mock run above passed all of
them in both conditions.

### 1.3 Credential qualification — cleared on the international endpoint

- Operator supplied `MINIMAX_API_KEY` (present; value never printed, copied,
  or committed) and named model `MiniMax-M3.1-Flash` with roughly 100M tokens
  of account inventory.
- The harness created a project-scoped provider config at
  `.lab/bench/005b/provider/config.json` (mode `0600`, gitignored):
  `model_name=minimax-m3-flash`, `model=minimax/MiniMax-M3.1-Flash`,
  key present. No secret appears in the trial spec, environment, prompts,
  raw traces, results, or logs (scan of each probe run directory for the
  full key: not present). The config path is outside every agent workspace,
  PicoClaw home, and trial dir.
- Global OpenCode credentials were not read. The expired 003F/003G
  credential was not retried or inspected beyond its documented expiry.
- Region determination: the key is opaque, so region cannot be decoded from
  key material. The mainland base returned `invalid api key (2049)` while
  the operator attests the key works elsewhere with `MiniMax-M3.1-Flash`;
  that combination is consistent with a region-mismatched (international)
  key rather than a dead key. The plan/subscription type (M Plan) is
  operator-attested, not independently verifiable from the key or the repo.
- Transport verification (code, `pkg/providers/openai_compat/provider.go`):
  the bench uses non-streaming `Chat`, which sends `POST
  {apiBase}/chat/completions` — with the international base this is exactly
  `https://api.minimax.io/v1/chat/completions`. The Authorization header is
  set once via `Header.Set("Authorization", "Bearer "+apiKey)` (single
  Bearer prefix; the stored key itself carries no prefix).
- First bounded probe (mainland base,
  `https://api.minimaxi.com/v1`, run `005b-auth-probe`: 1 control trial,
  `multi_file_cart`):

```bash
.lab/bin/bench run \
  --experiment D \
  --conditions control \
  --scenarios multi_file_cart \
  --trials 1 \
  --provider live \
  --provider-config "$PWD/.lab/bench/005b/provider/config.json" \
  --model minimax-m3-flash \
  --run-id 005b-auth-probe \
  --out "$PWD/.lab/bench/005b"
```

Result: `0 pass, 0 fail, 1 error`. The single LLM call failed after
retries with `Status: 401`, body `invalid api key (2049)`. Preflight was
fully green (18/18 checks); the request record exists with
`usage.reported=false` (no tokens fabricated). The error is preserved on
the turn and the trial, not discarded.

- Directed correction: only `api_base` changed to
  `https://api.minimax.io/v1`; model, key, and every other field untouched.
- Second bounded probe (international base, run `005b-auth-probe-io`,
  otherwise identical command with `--run-id 005b-auth-probe-io`):
  `1 pass, 0 fail, 0 error`. HTTP 200; **a model response was received**.
  10/10 requests carry provider-reported usage (model
  `MiniMax-M3.1-Flash`); 17 tool calls executed; build succeeded; the trial
  is harness-valid with an independently scored partial coding outcome
  (4/11 points — a model-behavior observation from one trial, not a pilot
  result). Provider-reported total for the probe: 44,836 tokens.

`valid_credential_found=true` for the international endpoint. The credential
gate is cleared. The live pilot still was not started: it waits on
confirmation of the usage ceiling.

### 1.4 Budget qualification — ceiling recorded, spend negligible

- Operator inventory: ~100M tokens remaining (operator statement, not
  independently verified).
- Pilot ceiling for the planned 10-trial pilot: worst-case bound ~8M total
  tokens given the per-task caps above; abort on any auth failure, rate
  limit, or cumulative provider-reported total above 8M. The expanded
  50-trial study requires a separate explicit approval after a pilot audit.
- Actual spend in 005B: one rejected request, zero provider-reported usage
  objects, zero successful completions. No budget was consumed.

## 2. Stage 2: small live pilot — attempted 1/10, auto-aborted

With the credential gate cleared and the 8M/1M ceilings authorized, the
runner's own guard was verified first (bench-only code, commit `508509a0`:
request-boundary refusal with a context+max-tokens allowance,
metering-failure latch on unreported usage, cross-trial and cross-segment
accounting, run stop on invalid trials or unmetered live usage; three new
guard tests pass and a capped mock run reproduces 10/10 ties). The pilot
launched with the exact authorized shape (seed 20261101, caps 8M/1M),
on binary `.lab/bin/bench` SHA-256
`7d60aaf4b8cc1ab76742c5e511ee1bf5703c526c744ec816f1614f2404142851`
built clean at commit `508509a0`:

```bash
export HOME=$PWD/.lab/home
export GOCACHE=$PWD/.lab/cache/go-build
export GOMODCACHE=$PWD/.lab/cache/gomod
export GOTMPDIR=$PWD/.lab/tmp TMPDIR=$PWD/.lab/tmp
.lab/bin/bench run \
  --experiment D \
  --trials 1 \
  --provider live \
  --provider-config "$PWD/.lab/bench/005b/provider/config.json" \
  --model minimax-m3-flash \
  --run-id 005b-pilot-20261101 \
  --seed 20261101 \
  --token-budget-total 8000000 \
  --token-budget-per-trial 1000000 \
  --out "$PWD/.lab/bench/005b"
python3 scripts/bench-005a-analyze.py ".lab/bench/005b/005b-pilot-20261101"
```

Result: `1 trial, 0 pass, 0 fail, 1 error` — the run stopped itself
before a second trial. Token-budget record: metered reported total 0 of
8M, per-trial max 0 of 1M, `stopped_early=true` with the trial error as
reason. No ceiling was breached in metered terms.

### What happened in trial 1 (control `compaction_ledger`)

The child process log (preserved verbatim) shows a trajectory the mock
never produces at this scale:

- ~40 `LLM request` log lines (vs 13 mock requests for this task), 45
  `bash` calls, 27 reads, 10 failed tool executions.
- The live model never converged: twice it hit `max_tool_iterations` (20)
  without a final response, looping over checks and reads.
- Each 1024-token completion grew the prompt until the 8192-token context
  window hit its 60% summarize threshold: **25 threshold compactions and
  21 session-summarize events** fired during turns 1–2.
- When turn 3 ran the trial's designed explicit `/compact`, history had
  nothing left to compact: `error turn t3 compact produced no summary`.
  The trial errors on that path by design (a compaction task without real
  compaction is not the experiment), the child wrote no `result.json` and
  no raw usage trace, and the parent recorded `trial process produced no
  readable result` and stopped the run.

This is an **invalid benchmark state**, not a coding outcome: the fixture's
8192-token window was sized for mock-scale traffic, and the live model's
verbosity plus the production threshold-compaction path interacted to
destroy the very history the explicit-compaction step requires. It would
affect both conditions equally in principle (same window, same model), but
the trial cannot execute its designed sequence, so its result would not be
a fair paired observation. It was preserved as an error, not rerun, and
no fixture difficulty, parameter, or scoring criterion was changed
afterward.

### Metering consequence

The ~40 provider calls in the failed trial were **never recorded** (no raw
JSONL, no usage objects): their account-level spend is unknown to the
harness. Per the missing-usage rule this alone requires a stop — it is
counted as unmetered, never as zero. The operator should check
account-level usage for the run window; the harness cannot reconstruct it.
All subsequent spend was prevented by the stop.

## 3. Stage 3: independent scoring — no live comparison scores exist

There are no live comparison repositories to score. The live observations to
date are the two auth probes: one **authentication failure** (mainland base)
and one **harness-valid single control trial** (international base,
partial 4/11 with a `wrong_fix` constraint flag), each classified as such
(distinct from model failure, tool error, rate limit, or harness failure).
The failed attempt is preserved at
`.lab/bench/005b/005b-auth-probe/trials/005b-auth-probe-control-multi-file-cart-t01/`
and the successful probe at the corresponding `005b-auth-probe-io` path;
neither was rerun as though it never happened.

The scoring path itself remains validated by the mock run: condition-independent
`ScoreCodingTask`, hidden tests materialized only post-run, weighted
criteria with full success kept separate from partial points, constraint
checks, and merged whole-trial metrics for restarted trials.

## 4. Stage 4: pilot audit

- The agent performed live coding operations in two trials (probe + pilot
  trial 1) but completed no paired comparison: the probe was a single
  control trial (partial 4/11, harness-valid), and pilot trial 1 errored
  before scoring.
- Hidden-test mechanics are proven by mock (pristine repos fail, fixed repos
  pass) but no live hidden-test outcome exists beyond the probe's partial.
- Fairness machinery held: identical tools/schema, clean preflight in the
  errored trial's setup phase (18/18), randomized pairing with recorded
  seed 20261101, MPM structurally disabled, no leakage of hidden tests or
  credentials into workspaces or traces (full-key scans of both probe runs
  and the pilot trial dir: absent).
- The stop rule worked as specified: invalid trial → run halted, reason and
  budget state recorded in manifest and summary, no rerun, no silent
  substitution.
- No task-state capture gap could be assessed live: the pilot trial died at
  the compaction step before any post-compaction comparison. The known 004C
  gaps (005A §3) remain the watch list.
- No weakness was fixed in response to an outcome. Bench-only guard code
  (`508509a0`) was added before the pilot to satisfy the pre-start
  enforcement requirement, re-qualified by mock, and applied identically to
  both conditions; production code, memory behavior, and scoring criteria
  are untouched.

Paired differences in outcome or effort cannot be reported: zero complete
pairs exist (one errored trial), and no significance claim is made.

## 5. Stage 5: expanded experiment — not authorized, pilot incomplete

Do not proceed to the 50-trial study. The pilot itself did not complete.
Requirements for any re-attempt (all needing explicit operator approval —
no automatic reruns, no silent design changes):

1. Acknowledge the compaction-window interaction in §2: the 8192-token
   `compaction_ledger` window plus a verbose live model reliably destroys
   the history the designed explicit compaction needs. Any fix (larger
   window, smaller completion cap, compaction-aware sequencing) is a
   fixture/parameter change requiring re-qualification and a design
   amendment — explicitly not done here.
2. A separately approved budget accounting for the unmetered-spend risk:
   failed trials can leave provider calls unrecorded.
3. Same binary/source tree or a re-qualified one with fresh provenance.

## 6. Traceability, limitations, acceptance

Artifacts (gitignored, redacted by the harness):

- `.lab/bench/005b/qualification/fixtures/verification.json`
- `.lab/bench/005b/qualification/005b-qual-mock-d-1t/{manifest,summary,trials.jsonl,trials/...}`
- `.lab/bench/005b/qualification/stage1-qualification.json`
- `.lab/bench/005b/qualification/go-test-pkg.txt`, `go-build.txt`, `go-vet.txt`
- `.lab/bench/005b/005b-auth-probe/{manifest,summary,trials/...}` with the
  preserved 401 on the mainland base.
- `.lab/bench/005b/005b-auth-probe-io/{manifest,summary,trials/...}` with
  the successful international-base probe (HTTP 200, 10/10 usage-reported
  requests, 44,836 provider-reported total tokens).
- `.lab/bench/005b/provider/config.json` (mode 0600, secret material, never
  committed; `api_base` now `https://api.minimax.io/v1`).
- `.lab/bench/005b/005b-pilot-20261101/{manifest,summary,trials/...}`
  with the auto-aborted 1/10 run, the preserved child `process.log`
  (threshold-compaction storm, max-iteration exhaustion), and the
  `token_budget` stop record.
- `.lab/bench/005b/qualification/mock-d-capped/{manifest,summary,...}`
  with the capped mock re-qualification (10/10 ties under the pilot caps).

Limitations:

1. No paired live comparison exists: one probe control (partial 4/11)
   and one errored pilot trial. This report supports no claim about
   task-state benefit, harm, or token costs.
2. The failure mode is model-verbosity × fixture-window interaction, not
   task-state signal. Both conditions share the window and the model, but
   with zero complete pairs nothing can be attributed either way.
3. Metered pilot spend is 0 reported tokens, but the errored trial made
   ~40 unrecorded provider calls: account-level spend is strictly greater
   than harness-metered spend. Future budgets must price in unmetered
   failure modes, and failed-trial raw-trace recovery (write raw before
   failing) is an identified harness gap.
4. The mainland 401 is most plausibly a region mismatch, but the key's
   region and M-plan subscription type are operator-attested, not
   cryptographically verified.
5. The ~100M inventory figure is an operator statement, not a metered
   observation.
6. Mock ties validate orchestration only, as in 005A.

Acceptance criterion: the study design **is capable** of detecting benefit,
harm, or no difference in actual coding outcomes — randomized paired
control/treatment, real compaction and process restart, hidden acceptance
tests, independent repository scoring, effort metrics, request-boundary
budget enforcement, and fail-closed stops all validated — but the pilot
itself is incomplete (1/10 trials, 0 complete pairs), so it detects
nothing yet. The abort performed as specified: invalid state in, run
stopped, evidence preserved, no rerun. Bench-only guard code (`508509a0`)
was added to meet the pre-start enforcement requirement and re-qualified
by mock; no production memory, MPM, OpenCode, scheduler, release, or
PicoClaw-baseline code was modified. The only config change was the
directed `api_base` correction in the gitignored project-scoped provider
file. No push or deploy was made. No key was rotated; the model was not
changed; the expanded study was not started.

## 7. Renewed pilot with increased budget — 10/10 complete

Operator re-authorized with ~100M tokens remaining (expiring midnight) and
caps of 20M aggregate / 2M per trial — 20% of stated inventory. Pre-flight
for the fresh runs: clean tree at `38ec91b6`, binary rebuilt
(`fdca03ce...`), fixtures 5/5 re-verified, fast guard/ledger unit tests
green, prior ledgers reconciled (outstanding reserved/unknown = 0; the
aborted pilot trial predates the ledger and stays historically unmetered).

Staged execution: pair 1 (`multi_file_cart`) ran first
(`005b-pilot-20261102a`, seed 20261102, caps 20M/2M) and was inspected —
2/2 harness-valid, fully metered, no unknown — before pairs 2–5 ran under
identical settings (`005b-pilot-20261102b`). Pair order was randomized
within each pair (recorded in manifests). Nothing was modified during the
pilot: same 8192-token ledger window, fixtures, frozen 004C task state,
scoring rules, model (`minimax-m3-flash`), and four-tool profile.

| Measure | Run A (pair 1) | Run B (pairs 2–5) | Pilot total |
|---|---|---|---|
| Trials valid / attempted | 2/2 | 8/8 | **10/10, 0 fail, 0 error** |
| Provider-reported tokens | 103,568 | 830,853 | **934,421 (4.67% of 20M)** |
| Max single-trial tokens | 57,301 | 207,408 (10% of 2M) | cap respected |
| Reserved / unknown | 0 / 0 | 0 / 0 | **0 / 0** |
| Missing-usage requests | 0 | 0 | 0 |

### 7.1 Paired functional correctness (treatment − control)

| Task | Control | Treatment | Pair result |
|---|---|---|---|
| `multi_file_cart` | partial 0.364 (4/11) | partial 0.364 (4/11) | **tie** |
| `misleading_failure` | **full 1.000 (7/7)** | partial 0.571 (4/7) | **control wins** |
| `requirement_change` | partial 0.125 (1/8) | partial 0.125 (1/8) | **tie** |
| `compaction_ledger` | partial 0.700, task-limited t1 | partial 0.700, task-limited t1 | **tie** |
| `restart_parser` | **full 1.000 (9/9)** | **full 1.000 (9/9)** | **tie** |

Full success: control 2/5, treatment 1/5. No constraint violations except
the pre-existing wrong-fix pattern both cart agents left in place
(`return subtotalCents / 10` untouched). Scoring is from hidden tests on
the final repositories, never from model claims.

### 7.2 Effort and overhead (treatment − control)

| Task | Δ requests | Δ tool calls | Δ total tokens | Task-state prompt tok. |
|---|---:|---:|---:|---:|
| cart | +3 (8→11) | +2 | +11,034 | 3,294 |
| shipping | 0 (16→16) | +4 | −21,389 | 4,973 |
| billing | −10 (23→13) | −17 | −110,359 | 3,735 |
| ledger | −1 (28→27) | +3 | +5,542 | 7,297 |
| parser | +2 (17→19) | +12 | −21,515 | 5,009 |

Treatment totals: fewer requests (69 vs 75) and fewer tokens overall, but
more tool calls (+4) and 22.8–57.3 KB of sidecar storage per trial, plus
3.3–7.3k task-state prompt tokens where control pays zero. Direction of
effort is mixed; direction of outcome favors control (1–0 with 4 ties).

### 7.3 What the agents actually did (model mistakes)

- **Cart, both:** investigated (6–7 reads, 8–9 checks) but **never wrote
  a file** — pristine repos, failure to act. Identical partials.
- **Shipping control:** applied the README contract (boundary fix via
  `edit`) → full success, evidence reuse true. **Treatment:** no fix
  (pristine boundary bug retained), 14 failed bash calls looping the
  same failing checks — the repeated-investigation shape, without
  converging.
- **Billing, both:** no edits; the t2 banker's-rounding correction was
  not implemented (partial 0.125 = build only). Consistent with the
  known 004C gap: natural corrections are not captured as task-state
  entries (005A §3).
- **Ledger, both:** fixed `balance.go` early, then hit the compaction
  churn guard in t1 (8/7 auto-compactions ≥ 6) with max-iteration
  exhaustion — the 005B-R-predicted shape. Both stopped as task limits
  (harness-valid), scored partial 0.7, t2/t3 unreached, explicit
  compaction untested (vacuous pass). `RecoveredAfterCompaction` false
  in both — correctly, since full success needs the validation fix.
- **Parser, both:** converged cleanly across a true process restart
  (history 23→32 control, 36→50 treatment; 2 processes each),
  `RecoveredAfterRestart` true, evidence reuse true, full success.
  Treatment wrote structured `memory/MEMORY.md` notes citing only
  public test names (no hidden-test leakage; `memory/` is excluded
  from scoring by construction). Control's memory dir stayed empty —
  a behavioral difference with no attributable cause and no scoring
  effect.
- Unsupported diagnoses: `diagnosis_statements` = 0 in all 10 trials —
  the heuristic found no wrong-diagnosis claims in traces.

### 7.4 Measurement caveats found live

- Command/path-level repetition is unmeasurable with this provider
  adapter: response tool-call records carry only an `id` (no
  name/arguments), so `repeated_*_commands/paths` collapse to an
  unknown-command bucket. Same-tool recall counts remain valid; the
  fine-grained "repeated investigation" metric does not.
- Mock token counts vary ±1 token run-to-run (workspace path length
  leaks into prompt estimates); tests assert same-run relationships,
  never absolute totals.
- Time-to-pass is defined only for the 3 full successes (46–129 s);
  failed trials record duration without a pass time.

### 7.5 Expansion verdict and limits

The pilot does **not** justify the 50-trial expansion for the primary
question: with 5 pairs the direction is control-favoring (1–0–4) with
treatment overhead and no outcome gain — a descriptive signal, not a
significant one, but with no positive trend to chase. The ledger task
cannot measure its compaction property under live verbosity in either
condition (2/2 task-limited in t1), bounding what a larger run could
learn without a protocol amendment (not made here). Stopping rules
fired zero times across 10 trials (no auth/quota/isolation/ledger
failures, no missing usage, no invalid states); the artificial
compaction limit engaged exactly as designed without voiding the
experiment.

Artifacts: `.lab/bench/005b/005b-pilot-20261102a/` and
`.../005b-pilot-20261102b/` (manifests with seeds/orders, summaries
with coding pairs and token budgets, per-trial results, raw traces,
usage ledgers, redacted logs). Previous aborted run preserved
unchanged. No push or deploy was made. The 50-trial expansion remains
**not authorized**.
