# Milestone 005B: live coding-agent evaluation

Status: **blocked before the live pilot. No live coding comparison was run.**
A single bounded authentication probe with the operator-supplied MiniMax
credential failed with HTTP 401 (`invalid api key (2049)`). Per the
pre-registered stopping rule the study stops here: no pilot, no scoring of
live code, no substituted provider, no retried expired credential.

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
- Auth probe: `.lab/bench/005b/005b-auth-probe/` (1 trial, 0 pass, 0 fail,
  1 error).
- No live pilot run exists. There is nothing to score.

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

### 1.3 Credential qualification — fail (blocked)

- Operator supplied `MINIMAX_API_KEY` (present, 125 chars; value never
  printed, copied, or committed) and named model `MiniMax-M3.1-Flash` with
  roughly 100M tokens of account inventory.
- The harness created a project-scoped provider config at
  `.lab/bench/005b/provider/config.json` (mode `0600`, gitignored):
  `model_name=minimax-m3-flash`, `model=minimax/MiniMax-M3.1-Flash`,
  `api_base=https://api.minimaxi.com/v1`, key present. No secret appears in
  the trial spec, environment, prompts, raw traces, results, or logs (scan
  of the probe run directory for the full key: not present). The config
  path is outside every agent workspace, PicoClaw home, and trial dir.
- Global OpenCode credentials were not read. The expired 003F/003G
  credential was not retried or inspected beyond its documented expiry.
- Bounded probe (1 control trial, `multi_file_cart`, live provider):

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
fully green (18/18 checks); postflight harness checks passed; the request
record exists with `usage.reported=false` (no prompt/completion/total
tokens fabricated). The error is preserved on the turn and the trial, not
discarded.

`valid_credential_found=false`. The live pilot was not started.

### 1.4 Budget qualification — ceiling recorded, spend negligible

- Operator inventory: ~100M tokens remaining (operator statement, not
  independently verified).
- Pilot ceiling for the planned 10-trial pilot: worst-case bound ~8M total
  tokens given the per-task caps above; abort on any auth failure, rate
  limit, or cumulative provider-reported total above 8M. The expanded
  50-trial study requires a separate explicit approval after a pilot audit.
- Actual spend in 005B: one rejected request, zero provider-reported usage
  objects, zero successful completions. No budget was consumed.

## 2. Stage 2: small live pilot — not run

The pilot (one matched pair per fixture, randomized order, identical
budgets, real subprocess restarts) was correctly **not started** after the
401. No fixture difficulty or scoring criterion was changed. The planned
pilot command, for the record, is:

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
  --run-id 005b-pilot-<timestamp> \
  --seed 20261101 \
  --out "$PWD/.lab/bench/005b"
python3 scripts/bench-005a-analyze.py ".lab/bench/005b/005b-pilot-<timestamp>"
```

Do not run it until a credential that passes the bounded probe is supplied.

## 3. Stage 3: independent scoring — no live scores exist

There are no live repositories to score. The only live observation is the
probe failure above, classified as an **authentication failure** (distinct
from model failure, tool error, rate limit, or harness failure). The failed
attempt is preserved at
`.lab/bench/005b/005b-auth-probe/trials/005b-auth-probe-control-multi-file-cart-t01/`
and was not rerun as though it never happened.

The scoring path itself remains validated by the mock run: condition-independent
`ScoreCodingTask`, hidden tests materialized only post-run, weighted
criteria with full success kept separate from partial points, constraint
checks, and merged whole-trial metrics for restarted trials.

## 4. Stage 4: pilot audit — harness audit only

- The agent performed no live coding operations (the request failed before
  any response or tool call).
- Hidden-test mechanics are proven by the mock run (pristine repos fail,
  fixed repos pass) but no live hidden-test outcome exists.
- Fairness machinery is proven: identical tools/schema, clean preflight in
  both conditions in mock, randomized pairing with recorded seed, MPM
  structurally disabled, no leakage of hidden tests or credentials into the
  workspace or traces.
- No task-state capture gap could manifest in live traffic because no live
  traffic completed. The known 004C gaps documented in 005A §3 (natural
  corrections, contradictory evidence adjudication, priorities/scope,
  paraphrased summaries) remain the watch list for any future pilot audit.
- No weakness was fixed in response to an outcome; no code was changed for
  this milestone.

Paired differences in outcome or effort cannot be reported: with zero live
trials there is nothing to compare, and no significance claim is made.

## 5. Stage 5: expanded experiment — not authorized

Do not proceed to the 50-trial study. Requirements for any future attempt:

1. A credential that passes the single-trial bounded probe above.
2. A separately approved budget for the pilot and, after audit, for the
   expansion (including methodologically compatible pilot pairs).
3. Same binary/source tree or a re-qualified one with fresh provenance.

## 6. Traceability, limitations, acceptance

Artifacts (gitignored, redacted by the harness):

- `.lab/bench/005b/qualification/fixtures/verification.json`
- `.lab/bench/005b/qualification/005b-qual-mock-d-1t/{manifest,summary,trials.jsonl,trials/...}`
- `.lab/bench/005b/qualification/stage1-qualification.json`
- `.lab/bench/005b/qualification/go-test-pkg.txt`, `go-build.txt`, `go-vet.txt`
- `.lab/bench/005b/005b-auth-probe/{manifest,summary,trials/...}` with the
  preserved 401.
- `.lab/bench/005b/provider/config.json` (mode 0600, secret material, never
  committed).

Limitations:

1. No live coding evidence was produced; this report supports no claim
   about MiniMax behavior, task-state benefit, or token costs.
2. The 401 may reflect a wrong endpoint, key type, or account state rather
   than a dead key. No alternative endpoint, provider, or model was tried;
   doing so without operator direction would violate the no-silent-switch
   rule.
3. The ~100M inventory figure is an operator statement, not a metered
   observation.
4. Mock ties validate orchestration only, as in 005A.

Acceptance criterion: the study design **is capable** of detecting benefit,
harm, or no difference in actual coding outcomes — randomized paired
control/treatment, real compaction and process restart, hidden acceptance
tests, independent repository scoring, and effort metrics all validated
offline — but the live comparison itself remains unexecuted for lack of a
working credential. No production memory, MPM, OpenCode, scheduler,
release, or PicoClaw-baseline code was modified; no push or deploy was made.
