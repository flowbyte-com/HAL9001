# Milestone 005B: live coding-agent evaluation

Status: **credential gate cleared on the international endpoint; live pilot
not started.** A first bounded probe against MiniMax's mainland endpoint
(`https://api.minimaxi.com/v1`) failed with HTTP 401 (`invalid api key
(2049)`). After switching only the API base to the international endpoint
(`https://api.minimax.io/v1`), a second bounded probe authenticated
successfully and received real model responses. Per instructions the study
stops here until the existing usage ceiling is confirmed: no pilot, no
scoring of a comparison, no model or endpoint switching beyond the directed
base-URL correction.

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
- No live pilot run exists. There is nothing to score as a comparison.

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

## 2. Stage 2: small live pilot — not run (awaiting ceiling confirmation)

The pilot (one matched pair per fixture, randomized order, identical
budgets, real subprocess restarts) was correctly **not started**: the
credential gate cleared only with the second probe, and the instruction
requires stopping before the pilot until the existing usage ceiling is
confirmed. No fixture difficulty or scoring criterion was changed.

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
  preserved 401 on the mainland base.
- `.lab/bench/005b/005b-auth-probe-io/{manifest,summary,trials/...}` with
  the successful international-base probe (HTTP 200, 10/10 usage-reported
  requests, 44,836 provider-reported total tokens).
- `.lab/bench/005b/provider/config.json` (mode 0600, secret material, never
  committed; `api_base` now `https://api.minimax.io/v1`).

Limitations:

1. One successful probe exists on the international endpoint, but no paired
   comparison; this report supports no claim about task-state benefit,
   MiniMax behavior beyond one control trial, or token costs.
2. The mainland 401 is most plausibly a region mismatch, but the key's
   region and M-plan subscription type are operator-attested, not
   cryptographically verified. If the international probe had also failed,
   the next step would have been comparing endpoint, credential type, and
   key source against the working MiniMax configuration — not concluding
   the key itself is invalid.
3. The ~100M inventory figure is an operator statement, not a metered
   observation.
4. Mock ties validate orchestration only, as in 005A.

Acceptance criterion: the study design **is capable** of detecting benefit,
harm, or no difference in actual coding outcomes — randomized paired
control/treatment, real compaction and process restart, hidden acceptance
tests, independent repository scoring, and effort metrics all validated
offline, and the live transport now demonstrably delivers model responses
with provider-reported usage — but the live comparison itself remains
unexecuted pending usage-ceiling confirmation. No production memory, MPM,
OpenCode, scheduler, release, or PicoClaw-baseline code was modified; the
only config change was the directed `api_base` correction in the
gitignored project-scoped provider file. No push or deploy was made. No key
was rotated; the model was not changed; the full benchmark was not started.
