# Milestone 005A: realistic coding-agent evaluation

Status: **evaluation infrastructure implemented and qualified offline.** No live
provider trial was run. The previously used credential is expired, so this
milestone stops after deterministic qualification as required.

- Production memory: frozen at 004C
  (`0ebc816eb56ed3c1769342eb4701996921ff0026`); no production behavior changed.
- Evaluation code commit: `f549e95c60be87b61c2b07f8faf037f3ea525cd7`
  (`bench: add realistic 005a coding evaluation`).
- Previous HEAD: `1e36a5546d5d6cc48b5c3eacafc92d1307e84d7f`.
- Run checkout: `f549e95c60be87b61c2b07f8faf037f3ea525cd7` (clean at run time;
  the documentation commit adds only this report).
- Benchmark binary: `.lab/bin/bench`
- Binary SHA-256: `c61539c97c03f551d05693b2763adaf803a6f2d53d4338be31af5f93f5cc9976`
- Go: `go1.27.3`
- Fixture qualification: `.lab/bench/005a/fixtures/verification.json`
- Deterministic run: `.lab/bench/005a/005a-mock-3t`
- 30 trials (5 tasks x 2 conditions x 3 repetitions), 30 valid, 15 complete pairs

The evaluation asks one question: **does the 004C task state help an agent
complete real repository work with less wasted effort?** It does not ask whether
a diagnostic appears in a prompt. Every success value in this milestone is
computed from the repository that remains after the agent stops.

## 1. Experimental conditions

Both conditions use the same binary, the corrected four-tool coding profile
(`bash`, `edit`, `read`, `write`), the same fixture commit, the same turn
scripts, the same context window, the same max tokens, temperature 0 and the
same maximum tool iterations.

| | Control | Treatment |
|---|---|---|
| `DEEPCLAW_TASK_STATE` | `0` | `1` |
| Residency | none | `boundary` (004C) |
| Session history | enabled | enabled |
| Native compaction | enabled | enabled |
| MPM retrieval | disabled | disabled |
| Resident MPM cache | disabled | disabled |
| Coding profile | four tools | four tools |
| Workspace | fresh per trial | fresh per trial |

The control is not weakened. It keeps ordinary history and the production
compaction path. The only intended difference is the short-term task-state
layer. The treatment necessarily creates a protected sidecar outside the
workspace; isolation protects it in both conditions, and this unavoidable
mechanical difference is recorded rather than attributed to intelligence.

Each trial runs in its own subprocess with fresh `HOME`, XDG directories,
workspace, session store and PicoClaw home. MPM is explicitly disabled with
`DEEPCLAW_MPM_ENABLED=0`, and the resident cache with
`DEEPCLAW_ACTIVE_MEMORY=0`. Preflight fails a trial if MPM is enabled, a
resident record exists, the wrong task-state setting is active, the fixture is
not at its recorded commit, or a hidden test is present in the workspace.

## 2. Coding fixtures

Five self-contained Go repositories live under
`pkg/bench/testdata/005a/tasks/`. They are embedded in the bench binary, so the
trial subprocess does not need a repository path and hidden tests are never
materialised during agent execution. Each fixture has a real `go.mod` (stored
as `go.mod.txt` only to avoid the Go tool's nested-module embedding rule), a
public test suite, a `check.sh`, a README with the desired contract, and a
hidden acceptance suite under `hidden/`.

| Task | Scenario | Turns | Interruption | Points | Starting commit |
|---|---|---:|---|---:|---|
| `multi_file_cart` | Multi-file pricing repair | 1 | none | 11 | `63014eb0236567caf3a0fca66ce58224b9a0094e` |
| `misleading_failure` | Misleading failing test; contradictory evidence | 1 | none | 7 | `1a4b0d0ac074e9f0eace21643c48fcad71c51024` |
| `requirement_change` | User correction changes the requirement | 2 | none | 8 | `2775a29c59562d7a6e348b0afe699b214646fb5b` |
| `compaction_ledger` | Partially completed fix, then compaction | 3 | `/compact` before t3 | 10 | `e1fa55fbb41c3523c473b4e7c029835a26bfd51a` |
| `restart_parser` | Unfinished parser work, then process restart | 2 | restart before t2 | 9 | `a8166691dbfe1030e40acd1f38f6596015ff635d` |

### 2.1 What each task requires

- **`multi_file_cart`** needs reads of `discount.go` and `money.go`, edits or
  full writes to both, and repeated execution of `sh check.sh`. The bug spans
  promotion handling and money formatting.
- **`misleading_failure`** is a boundary bug in a shipping rate. The repository
  contains `legacy_golden.txt` and `diagnose.sh`, both claiming that 1000 g
  costs 750 cents. The finance-approved README says 500 cents. The agent must
  run the checks, read the contradictory evidence, and choose the current
  contract. This is the contradictory-tool-evidence scenario.
- **`requirement_change`** first asks for nearest-cent rounding, then corrects
  the tie rule to banker's rounding (half to even). The public tests cover only
  non-tie amounts and stay valid under both rules. Turn 1 can therefore produce
  a legitimate partial result; turn 2 must apply the correction.
- **`compaction_ledger`** has two independent defects. Turn 1 fixes only the
  balance defect and leaves validation failing. Turn 2 confirms the remaining
  failure. A real `/compact` runs before turn 3, so turn 1's tool evidence
  leaves ordinary history. Turn 3 must finish the validation fix. The task has
  no compaction at all in the other four fixtures.
- **`restart_parser`** fixes only `ParseInt` in turn 1. The process is then
  stopped and restarted with the same session state, and turn 2 must finish
  `ParseList` without losing the earlier finding.

The deterministic mock uses all four tools: `read`, `write`, `edit` (the
shipping boundary fix) and `bash` (every `check.sh` run). It is a scripted
trajectory, not a model.

### 2.2 Reproducible starting point and reset

Materialising a fixture writes the files, runs `git init` and commits with a
fixed author/committer identity and timestamp. Two independent materialisations
produce the same commit and content SHA. The content SHA is checked again after
materialisation and at preflight.

```bash
# Reset one fixture to its pristine state (prints commit and SHA-256).
.lab/bin/bench fixtures --reset --task multi_file_cart --out /tmp/opencode/cart-reset

# Re-materialise every fixture twice and prove the hidden tests fail before work.
.lab/bin/bench fixtures --verify --out "$PWD/.lab/bench/005a/fixtures"
```

Fixture qualification results:

| Task | Hidden tests on pristine repo | Public tests on pristine repo | Pristine partial |
|---|---|---|---|
| `multi_file_cart` | 2 pass / 4 fail | 2 fail | 0.36 |
| `misleading_failure` | 3 pass / 1 fail | 1 fail | 0.57 |
| `requirement_change` | 0 pass / 3 fail | 1 fail | 0.12 |
| `compaction_ledger` | 3 pass / 3 fail | 2 fail | 0.40 |
| `restart_parser` | 1 pass / 4 fail | 2 fail | 0.22 |

Every fixture builds, has a deterministic commit, and fails at least one hidden
acceptance test before any repair. Hidden tests are therefore not free credit.

## 3. What 004C captures automatically, and what it misses

These tasks deliberately use natural instructions. The only thing the prompts
do that 004C recognises is start with an ordinary imperative verb such as
"Fix"; that is the existing implicit-objective rule, not a benchmark-specific
marker.

### Captured

- The first imperative line becomes an active objective when the message has no
  question mark.
- Tool commands and outcomes are recorded as observations with bounded quotes,
  exit status, output length and hash: reads, writes, edits and check runs.
- The compaction event itself is recorded as runtime evidence.
- A later successful run of the same command can supersede an earlier failure,
  while an unrelated command does not.
- Boundary residency suppresses an entry whose evidence is still visible in
  ordinary history, the summary or the current message.

### Missed

- **Natural corrections.** `requirement_change` says "Finance has changed the
  tie rule..." rather than `Correction:` or `Actually,`. 004C does not create a
  correction entry. The final snapshot contains the original objective and tool
  observations but no correction; the agent must act on the current user
  message and history. This is a real limitation, not hidden by the fixture.
- **Contradictory evidence.** `misleading_failure` stores the obsolete 750-cent
  diagnostic and the README as two observations. Task state records what tools
  reported; it does not decide which source is authoritative. The agent must
  resolve the contradiction.
- **Priorities and scope.** "Leave the validation failure for the next step" is
  not a `Next:` marker, so it is not stored as a next action. The agent must
  preserve that instruction from history.
- **Code semantics.** Task state stores bounded tool output, not a model of the
  source tree. It cannot tell the agent that a boundary comparison is wrong,
  that two files share a contract, or which hidden edge cases matter.
- **Paraphrased summaries.** Boundary residency recognizes identical evidence,
  not a paraphrase. A live summarizer that paraphrases can force an entry to
  remain resident (a token cost); one that preserves the evidence verbatim lets
  boundary mode suppress it. The mock uses an extractive summary so the control
  is not disadvantaged; live runs use the production summarizer.

The deterministic snapshots make the gap visible. For `multi_file_cart`,
`misleading_failure` and `requirement_change`, the treatment renders **0**
task-state prompt tokens: every stored observation is still visible in
ordinary history, so boundary residency correctly stays out of the way. Only
`compaction_ledger` (1,208 tokens per trial; 3,624 over the three trials) and
`restart_parser` (768 per trial; 2,304 over three) pay prompt residency,
because earlier evidence has actually left the visible window. That is the
mechanism working as designed, not a coding-intelligence result.

## 4. Independent scoring

Scoring is a pure function of the resulting repository, the embedded hidden
tests and the recorded trace. It receives no condition, no prompt text and no
agent claim.

`ScoreCodingTask` (`pkg/bench/coding_scoring.go`):

1. Copies the agent workspace to a separate scoring directory, skipping
   runtime state (`.git`, `sessions`, `.deepclaw`, `memory`) and recording
   symlinks or oversized files as constraint violations.
2. Records the final content SHA.
3. Adds the hidden test files **only after the agent process has stopped**.
4. Runs `go build ./...` with `GOPROXY=off`, `GOFLAGS=-mod=readonly`,
   `GOTOOLCHAIN=local`, `CGO_ENABLED=0` and isolated `GOCACHE`/`GOPATH`.
5. Runs `go test -json -count=1 ./...`, parses each top-level test, and
   separates public from hidden test names.
6. Awards weighted criterion points and computes:
   - `FullSuccess`: every mandatory criterion passes **and** there are no
     constraint violations.
   - `Partial`: `points_earned / points_total`, reported independently.
   - `EvidenceReuse`: the pre-declared evidence-reuse tests pass.
   - `WrongFixApplied`: a known wrong-fix pattern is present (a heuristic, not
     a general diagnosis judge).
   - `RecoveredAfterCompaction` / `RecoveredAfterRestart`: the interrupted task
     reaches full success.

Criterion kinds are `build`, `public_tests` and a named hidden `test`.
Constraint checks are independent of desired behavior:

- protected-file hashes (public tests, `go.mod`, README, and the legacy notes);
- forbidden files (`go.work`, `go.work.sum`);
- nested `go.mod` files;
- symlinks and oversized files;
- known wrong-fix regular expressions (for example `750` in the shipping
  implementation or `+ 0.5` in the billing implementation);
- hidden test files present in the workspace.

A trial is `pass` when the harness is valid, even if the coding task fails.
Coding success lives in `score.full_success` and `coding.full_success`, never
in the harness status. That keeps harness health, task success and partial work
separate.

The same scorer is used for pristine fixtures, control trials and treatment
trials. A dedicated test runs it twice against the same repository with
different condition metadata and requires an identical verdict.

## 5. Compaction, restart and leakage control

- **Compaction** is a real `/compact` through the production command path before
  turn 3 of `compaction_ledger`. The trial fails if no summary was produced.
  The test asserts that ordinary history was compacted and that the summary is
  non-empty.
- **Restart** closes the agent loop and constructs a new one with the same
  session store and workspace before turn 2 of `restart_parser`. The trial
  fails if the restart flag is not observed.
- **Hidden tests** are embedded in the bench binary and copied to a post-run
  scoring directory. Preflight and postflight assert that no hidden filename is
  present in the workspace.
- **Cross-trial leakage** is prevented by a fresh trial tree per trial. The
  task-state sidecar lives under that trial's PicoClaw home, outside the
  workspace, and is session/project scoped. Control has no sidecar at all.
- **MPM leakage** is prevented structurally: there is no MPM workspace, no MPM
  binary invocation, and postflight requires zero `MPM retrieval` log events
  and zero resident records.
- **Mock summaries** are extractive: user requirements and tool evidence are
  retained instead of being replaced by a generic "details were compacted"
  summary. This gives the control a fair post-compaction history. Live runs use
  the production summarizer in both conditions.

## 6. Measurements

Every trial records, from sources independent of agent claims:

| Measurement | Source |
|---|---|
| Hidden tests passed/failed, per test | `go test -json` on the post-run repository |
| Public tests passed/failed | same run |
| Build success | `go build ./...` |
| Constraint violations | hashes, file checks, pattern scan |
| Full success and partial points | weighted criteria |
| Provider prompt/completion/total/cache tokens | provider-reported usage only |
| Missing usage objects | explicit `usage_missing_requests` |
| LLM requests | `RecordingProvider.Chat` calls |
| Coding-tool calls and failures | runtime tool-end events |
| Repeated reads | repeated `read` path arguments in provider calls |
| Repeated failed investigations | repeated identical bash commands when bash failures occurred |
| Evidence reuse | pre-declared hidden tests |
| Wrong/unsupported fix | known wrong-fix patterns; diagnosis statements counted separately |
| Recovery after compaction/restart | full success on the interrupted task |
| Time to full success | trial duration for a successful repository |
| Task-state prompt residency | rendered `task.state` block per request |
| Task-state storage | sidecar bytes on disk |
| MPM retrieval | log event count; required to be zero |

Provider measurements and local estimates are separate fields. The mock's
`estimateUsage` values are synthetic and are never used as live token evidence.
Raw per-request traces are written as JSONL and redacted for JWTs, `sk-` keys,
bearer tokens and key-shaped fields.

## 7. Evaluation integrity

- One subprocess per trial, fresh filesystem and session state.
- Condition and treatment order randomized within each pair. Pairs themselves
  are shuffled. The seed is recorded in `manifest.json`; the same seed
  reproduces the same order.
- Identical task scripts, budgets and parameters in both conditions.
- Five paired repetitions per task are planned for live runs (50 trials total:
  5 tasks x 5 pairs x 2 conditions).
- Criteria are pre-registered below, before any live outcome is observed.
- Deterministic mock qualification precedes any live attempt.
- A non-passing or failing task result is preserved as an observation; it is
  never rewritten as a harness error.

### 7.1 Pre-registration

Fixed before live data collection:

- **Primary endpoint:** number of paired tasks reaching independently verified
  full success, treatment versus control, per task and pooled over tasks.
- **Secondary endpoints:** partial points; hidden tests passed; constraint
  violations; provider prompt/completion/cache tokens; LLM requests; tool
  calls; repeated reads; repeated failed investigations; evidence reuse;
  wrong-fix flag; recovery after compaction/restart; time to full success;
  task-state prompt tokens and storage bytes.
- **Sample:** 5 paired repetitions per task. Control and treatment use the same
  task turn script and parameters. No optional stopping; either all 50 planned
  trials are attempted or the run is reported as incomplete.
- **Analysis:** report per-task and pooled paired outcomes, the paired
  treatment-control deltas, and the raw distributions. With five pairs per task
  this is a descriptive study, not a significance test.
- **Order:** seed `20261101` unless the operator records a different seed before
  the run. The manifest records the actual seed and order.
- **Stopping:** if authentication or provider access fails, record the blocker
  and stop. Do not substitute a different provider or claim a result.

## 8. Deterministic qualification

The offline run used the deterministic mock provider, three repetitions per
task per condition:

```text
run 005a-mock-3t: 30 trials, 30 pass, 0 fail, 0 error
15 complete control/treatment pairs
```

| Measure | Control | Treatment |
|---|---:|---:|
| Full success | 15/15 | 15/15 |
| Pairs won | 0 | 0 |
| Ties | — | 15 |
| Mean partial | 1.000 | 1.000 |
| Constraint violations | 0 | 0 |
| Mean LLM requests | 9.0 | 9.0 |
| Mean tool calls | 7.0 | 7.0 |
| Mean prompt tokens (mock estimate) | 5,637 | 6,138 |
| Mean completion tokens (mock estimate) | 24.2 | 24.2 |
| Mean task-state prompt tokens | 0 | 395 |
| Mean task-state storage bytes | 0 | 8,877 |
| Mean repeated reads | 0.6 | 0.6 |
| Mean repeated failed investigations | 1.2 | 1.2 |
| Mean diagnosis statements | 0 | 0 |
| MPM retrieval calls | 0 | 0 |

Per-task task-state prompt tokens:

| Task | Control | Treatment |
|---|---:|---:|
| `multi_file_cart` | 0 | 0 |
| `misleading_failure` | 0 | 0 |
| `requirement_change` | 0 | 0 |
| `compaction_ledger` | 0 | 1,208 |
| `restart_parser` | 0 | 768 |

Interpretation is deliberately narrow:

- The harness, scoring, isolation, reset, pairing and measurements are
  validated.
- The mock produces identical code in both conditions, so **this is a tie by
  construction and says nothing about coding intelligence**.
- The treatment pays a measurable prompt and storage cost exactly on the tasks
  where evidence leaves the visible window. On the short tasks it adds storage
  but no prompt residency.
- The duration difference (`-87 ms` mean) is process noise, not a speed claim.
- The run can reveal help, harm or no difference once a live model is used:
  full success, partial points, constraints, token cost and wasted-effort
  metrics are all independent of the mechanism.

Artifacts: `.lab/bench/005a/005a-mock-3t/{manifest,summary,trials,raw}` and
`.lab/bench/005a/fixtures/verification.json`.

## 9. Live study command

After a valid credential and a separately agreed usage budget are available:

```bash
cd /home/v/projects/deepclaw
export HOME=$PWD/.lab/home
export GOCACHE=$PWD/.lab/cache/go-build
export GOMODCACHE=$PWD/.lab/cache/gomod
export GOTMPDIR=$PWD/.lab/tmp TMPDIR=$PWD/.lab/tmp

# Build the same harness used for offline qualification.
go build -tags 'goolm,stdjson' -o .lab/bin/bench ./cmd/bench

export PROVIDER_CONFIG=/secure/path/to/provider-config.json   # outside agent paths
export MODEL=provider/model-id
export RUN_ID=005a-live-20261101

.lab/bin/bench run \
  --experiment D \
  --trials 5 \
  --provider live \
  --provider-config "$PROVIDER_CONFIG" \
  --model "$MODEL" \
  --run-id "$RUN_ID" \
  --seed 20261101 \
  --out "$PWD/.lab/bench/005a/live"

python3 scripts/bench-005a-analyze.py \
  ".lab/bench/005a/live/$RUN_ID"
```

The provider config must be outside the agent workspace, PicoClaw home and
trial directory; preflight rejects a config the agent could read. The harness
does not inspect the credential beyond what the provider client does, and it
does not retry an expired credential as a benchmark shortcut. If the first
request fails authentication, the run is incomplete and no coding claim is
made.

## 10. Weaknesses and possible biases

1. **The mock is not a model.** All offline outcomes are ties by construction.
   They validate orchestration and measurement only.
2. **Small, single-package tasks.** Five tiny Go repositories do not represent
   large, multi-package codebases, long build times or ambiguous ownership.
3. **Author-written tests.** Public and hidden tests share an author and a
   style. A real bug-fix benchmark should include externally sourced issues.
4. **Hidden-test overfitting.** The tasks reward the specific behavior encoded
   in the hidden tests. A correct but differently structured implementation
   can still pass; an incomplete interpretation cannot. Partial points reduce
   but do not remove this.
5. **Task-state may be unnecessary on short tasks.** Three of five tasks fit in
   ordinary history. The design intentionally includes them so harm and
   overhead are observable; it also means a pooled result can dilute the
   interruption-specific effect. Report per task as well as pooled.
6. **Natural-language capture gap.** The correction in `requirement_change` is
   not stored as a 004C correction entry. That is a genuine limitation of the
   mechanism, and the evaluation exposes it rather than hiding it behind
   markers.
7. **Contradictory evidence is stored, not adjudicated.** The model must decide
   which source to trust. Task state can make both claims more salient; that
   could help or hurt.
8. **Mock summary policy.** The deterministic summary is extractive and
   deliberately fair to the control. A live summarizer may paraphrase; the
   production summary is the same model in both conditions, but its behavior
   can change the control's access to earlier evidence.
9. **Limited power.** Five paired repetitions per task are enough to expose
   large effects and direction, not to establish small effects. Do not report
   a significance claim from this design.
10. **Provider and host variance.** Temperature 0 reduces but does not remove
    model variance; one host, one Go version and one provider configuration.
11. **Mechanical condition difference.** Treatment registers a protected
    sidecar and writes it; control does not. This is inherent to the mechanism
    and is recorded, not attributed to intelligence.
12. **Diagnosis metrics are heuristics.** `DiagnosisStatements` counts
    trace text; `WrongFixApplied` matches known wrong code patterns. Neither is
    a general judge of reasoning quality.
13. **Repeated-call metrics are syntactic.** Identical commands or read paths
    can be legitimate; the metric flags potential waste, not proven waste.
14. **Time to pass is only defined for successes.** Failed trials still record
    duration, but "time to a passing implementation" is absent by definition.
15. **Cache behavior is unknown offline.** Mock usage has no real cache
    semantics. Live cache tokens are recorded but may be omitted or reported as
    zero by the adapter.
16. **No MPM in the primary experiment.** This isolates short-term task state
    but means the result does not describe DeepClaw with long-term retrieval
    enabled.

## 11. Acceptance assessment

The evaluation is capable of revealing help, harm or no measurable difference:

- **Help:** treatment reaches more full successes or more partial points, or
  reaches them with fewer requests, repeated reads, repeated failed
  investigations or tokens.
- **Harm:** treatment loses full successes, adds constraint violations, or
  spends materially more tokens without outcome gain.
- **No difference:** paired outcomes and effort metrics are the same, as in the
  mock qualification, while overhead remains measurable.

The control has a fair opportunity through ordinary history and the production
compaction path. Success is never inferred from a diagnostic appearing in a
prompt: it is computed from the final repository by hidden tests and independent
constraints. The remaining blocker is external: no valid live credential was
available, so this milestone stops at offline qualification.

## 12. Reproduction and artifacts

```bash
export HOME=$PWD/.lab/home
export GOCACHE=$PWD/.lab/cache/go-build
export GOMODCACHE=$PWD/.lab/cache/gomod
export GOTMPDIR=$PWD/.lab/tmp TMPDIR=$PWD/.lab/tmp

# Fixtures: deterministic commits, pristine hidden failures.
.lab/bin/bench fixtures --verify --out "$PWD/.lab/bench/005a/fixtures"

# Deterministic end-to-end qualification.
.lab/bin/bench run --experiment D --trials 3 --provider mock \
  --run-id 005a-mock-3t --out "$PWD/.lab/bench/005a"

python3 scripts/bench-005a-analyze.py .lab/bench/005a/005a-mock-3t

# Full verification.
go test -tags 'goolm,stdjson' -count=1 -timeout 25m ./pkg/...
go build -tags 'goolm,stdjson' ./...
go vet -tags 'goolm,stdjson' ./pkg/...
```

Repository artifacts:

- `pkg/bench/testdata/005a/tasks/` - five fixtures, public tests, hidden tests,
  task metadata.
- `pkg/bench/coding_fixtures.go` - embedded fixture loading, materialisation,
  deterministic commits, reset and qualification.
- `pkg/bench/coding_scoring.go` - condition-independent repository scoring,
  constraints and metrics.
- `pkg/bench/coding_eval_test.go` - fixture, full/partial/constraint,
  condition-independence, pairing and end-to-end tests.
- `pkg/bench/run.go`, `pkg/bench/trial.go`, `pkg/bench/mock_provider.go` -
  experiment D, randomized pairing, isolation, interruption and deterministic
  mock trajectories.
- `cmd/bench/main.go` - `bench run --experiment D` and `bench fixtures`.
- `scripts/bench-005a-analyze.py` - paired run summary.

Local, gitignored evidence under `.lab/bench/005a/`:

- `fixtures/verification.json`
- `005a-mock-3t/manifest.json`, `summary.json`, `trials/`, `raw/`
- `go-test-pkg.txt`, `go-build.txt`, `go-vet.txt`

No production memory code, MPM service, scheduler, OpenCode configuration,
release work or PicoClaw baseline was modified.
