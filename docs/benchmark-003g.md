# DeepClaw Benchmark 003G: Live Benchmark Qualification

Status: **authentication blocked; live pilot not started.** The harness and
credential-isolation regression tests pass. No live-model correctness, token or
cache result was produced, and none is inferred.

- Harness code: `c536a5ebf62dd12bb2c3076d90f07abe2f3a7ce5`
- Run checkout: `c536a5ebf62dd12bb2c3076d90f07abe2f3a7ce5` (clean at run time)
- Baseline: `79ae926c91f3c14ea5a6f107545ffd53b99c5016`
- Binary: `.lab/bin/bench`
- Binary SHA-256: `8159654e9350c9877bdf8d19f5c830128e49c3d4dc000c9b3eb9b80d6b30d96f`
- Go: `go1.27.3`

## 1. Scope

003G was intended to complete the first trustworthy live-model memory
comparison on top of the 003F harness. It does not add memory features or
change production retrieval, prompts or tool profiles.

The live portion stopped at Stage 1 because no valid project-scoped credential
was available. Stages 2 and the credential-isolation regression were completed
so that a future authenticated run starts from a verified harness.

## 2. Stage 1: credential and provider qualification

### 2.1 Authentication mechanism

The configured `free` model uses a direct API key, not an OAuth refresh flow:

```json
{
  "model_name": "free",
  "model": "openai/poolside/laguna-xs-2.1:free",
  "api_base": "https://inference-api.nousresearch.com/v1",
  "api_keys": ["<redacted>"]
}
```

`CreateProvider` resolves the model entry and the OpenAI-compatible provider
sends that value as `Authorization: Bearer <key>`. The config has no
`auth_method: oauth`, no refresh token and no project-local `auth.json`. An
auth store would be read from `<PICOCLAW_HOME>/auth.json`; none exists in the
project-local state.

Only project-scoped state was inspected:

- `/home/v/projects/deepclaw/.lab/**/config.json`
- `/home/v/projects/deepclaw/.lab/**/auth.json`
- project environment-variable **names** (values were not printed)

Global OpenCode configuration and unrelated user credentials were not read.
No token was printed, copied into a report, or committed.

### 2.2 Credential status

Four project-local configs contain the same expired credential family:

| Config | Model | Credential expiry |
|---|---|---|
| `.lab/bench/no-memory/home/.picoclaw/config.json` | `openai/poolside/laguna-xs-2.1:free` | `2026-10-30T11:31:39Z` |
| `.lab/bench/eager/home/.picoclaw/config.json` | same | `2026-10-30T11:31:39Z` |
| `.lab/bench/deepclaw/home/.picoclaw/config.json` | same | `2026-10-30T11:31:39Z` |
| `.lab/home/.picoclaw/config.json` | same | `2026-10-30T10:46:41Z` |

Decoded metadata (not the token) shows `token_use=access` and
`scope=inference:invoke`; no refresh token is present.

A single bounded authentication probe at `2026-10-30T14:31Z` confirmed the
state: one request, HTTP 401, zero provider-reported usage records.

```text
Status: 401
Your API key is invalid, blocked or out of funds.
```

`valid_credential_found=false`. The live pilot was not started.

### 2.3 Exact operator action

1. Obtain a fresh credential for
   `openai/poolside/laguna-xs-2.1:free` at
   `https://inference-api.nousresearch.com/v1` from the provider's official
   login or refresh flow.
2. Place it only in a project-local provider config outside the agent-visible
   workspace, PicoClaw home and trial tree, for example
   `/home/v/projects/deepclaw/.lab/bench/003g/provider/config.json` with mode
   `0600`. Do not commit it.
3. The credential must have an `exp` later than the planned run window. If the
   provider issues a refresh token, configure a project-local auth store inside
   the trusted benchmark environment; the current config uses a direct
   `api_keys` access token and has no refresh mechanism.
4. Re-run the bounded pilot with the explicit `--out .lab/bench/003g` path, for
   example:

```bash
.lab/bin/bench run \
  --experiment A \
  --conditions no_memory,eager,mpm_retrieval,mpm_active \
  --scenarios recall --trials 3 \
  --provider live \
  --provider-config "$PWD/.lab/bench/003g/provider/config.json" \
  --model free \
  --out "$PWD/.lab/bench/003g" \
  --run-id "live-A-recall-$(date -u +%Y%m%dT%H%M%SZ)"
```

The harness records the exact model identity and settings in every trial. Do
not switch provider or model silently after a failure.

## 3. Credential exposure isolation

### 3.1 Harness guard

A provider config is now rejected before provider creation if its absolute path
is inside any agent-visible location:

- the agent workspace (file tools);
- `PICOCLAW_HOME` (mounted read/write for isolated shell execution);
- the trial tree.

The trial records the `provider_config_outside_agent_paths` preflight check.
A rejected configuration sends zero provider requests.

The credential value itself is never placed in the trial spec, environment,
prompt, raw request JSONL, result JSON or logs. The trusted child process reads
the config directly from the operator-supplied path.

### 3.2 Deterministic synthetic-sentinel regression

`pkg/bench/credential_test.go` adds two layers of evidence using a synthetic
value (`SENTINEL_CREDENTIAL_003G_DO_NOT_LEAK`), never a real token.

**Live transport test.** A local OpenAI-compatible HTTP endpoint requires the
synthetic value as its bearer credential. The trial uses the real live-provider
transport and the four-tool profile. The fake model asks the agent to read the
provider config with:

- the `read` file tool; and
- the `bash` tool (`cat <config>`).

Results for both paths:

- the trusted process presented the credential to the endpoint;
- the agent tool call was denied (`is_error=true`);
- the sentinel did not reach the agent prompt, tool output, raw request file,
  result, process log, trial log, or trial environment;
- scanning the entire run directory found no sentinel;
- the trial made two actual requests and recorded two provider usage objects;
- the trial abstained as intended.

This is a transport and isolation test, not a model result.

**Bad-location test.** A provider config placed inside the agent workspace is
rejected at preflight with `provider_config_outside_agent_paths` and zero
requests.

### 3.3 Residual limitation

The isolation guarantee covers accidental exposure through tools, shell,
prompts and artifacts. It cannot prevent a provider from echoing a credential
inside a model response if the trusted process itself sends it to the model;
the harness never sends the config to the model. Redaction handles JWT, `sk-`
and bearer-shaped values in raw/log artifacts, but arbitrary opaque secrets are
protected by isolation rather than by pattern matching.

## 4. Stage 2: harness verification

Command and preserved output:

```bash
export HOME=$PWD/.lab/home
export GOCACHE=$PWD/.lab/cache/go-build
export GOMODCACHE=$PWD/.lab/cache/gomod
export GOTMPDIR=$PWD/.lab/tmp
export TMPDIR=$PWD/.lab/tmp

go test -tags 'goolm,stdjson' -count=1 -v -timeout 15m ./pkg/bench
```

Output: `.lab/bench/003g/validation/go-test.txt`.

All tests passed:

- `TestHarnessValidation` and its ten Phase 6 checks;
- `TestCredentialIsolation` (`file_tool`, `shell`);
- `TestProviderConfigInsideWorkspaceRejected`;
- redaction and session-key tests.

### 4.1 Clean live preflight evidence

The single 401 authentication probe still produced a complete preflight record
for a live trial:

```text
session_key_explicit                 pass
tool_profile_four                    pass  (bash,edit,read,write)
isolation_enabled                    pass
no_pre_existing_session              pass
no_pre_existing_active_memory        pass
provider_model_recorded              pass
provider_config_outside_agent_paths  pass
no_answer_in_workspace               pass
one_effective_session                pass
effective_session_not_canonical      pass
```

Turn 1 started with `history_before=0`, `summary_chars_before=0`; the session
contained exactly one explicit `sk_v1_...` file. The provider request failed
with 401 before any answer was produced.

### 4.2 Four-condition mock control

A separate mock control run at the same code revision confirms the four memory
conditions expose identical tools and clean state:

```bash
.lab/bin/bench run \
  --experiment A \
  --conditions no_memory,eager,mpm_retrieval,mpm_active \
  --scenarios recall --trials 1 \
  --provider mock \
  --out "$PWD/.lab/bench/003g" \
  --run-id mock-control-003g
```

Result: 4/4 valid trials, 0 failures, 0 errors.

| Condition | Tools | Schema SHA-256 (prefix) | Preflight failures | Effective session files |
|---|---|---|---:|---:|
| no_memory | bash,edit,read,write | `516e5c3d032c` | 0 | 1 |
| eager | bash,edit,read,write | `516e5c3d032c` | 0 | 1 |
| mpm_retrieval | bash,edit,read,write | `516e5c3d032c` | 0 | 1 |
| mpm_active | bash,edit,read,write | `516e5c3d032c` | 0 | 1 |

No condition had a pre-existing session, active-memory sidecar or
answer-bearing workspace history. MPM sessions use the expected project-scoped
derived storage key; the explicit session key is recorded in every result.

### 4.3 Effective live model configuration

From the authentication probe:

| Field | Value |
|---|---|
| Provider kind | `live` |
| Model alias | `free` |
| Exact model | `poolside/laguna-xs-2.1:free` |
| API base | `https://inference-api.nousresearch.com/v1` |
| Temperature | `0` |
| Max tokens | `256` |
| Context window | `32768` |
| Max tool iterations | `3` |
| Tool profile | `bash,edit,read,write` |
| Isolation | enabled |

No successful completion means no provider usage, cache, latency or
correctness measurement exists for this model.

## 5. Stage 3: live pilot

Not started.

- No valid credential was available.
- No randomized live blocks were executed.
- No live responses were generated.
- No provider token or cache measurements exist.
- The only live request at this revision was the single 401 authentication
  probe; it is preserved as a failure observation, not a result.

The pilot was not replaced with a different provider or model.

## 6. Stage 4: measurement status

| Measurement | Status |
|---|---|
| Prompt tokens | unavailable (no successful request) |
| Completion tokens | unavailable |
| Total tokens | unavailable |
| Cache-read tokens | unavailable |
| Cache-creation tokens | unavailable |
| Message count / roles | recorded for the 401 request only |
| Tool calls | none (request failed before a response) |
| Retrieval count / latency | none (no model turn completed) |
| End-to-end latency | 643 ms for the failed probe, not a model measurement |
| Correctness | no score; trial status `error` |
| Hallucinations | not observable |
| Per-run effective prompt sizes | the two mock usage values are synthetic and are not live measurements |

The 401 request has `usage.reported=false`; zero is not substituted for
unavailable token values.

Three trials are not presented as proof, and no variance is reported because
no live trial completed. No memory-policy difference is attributed where task
trajectories or provider conditions differ.

## 7. Working-memory hypothesis

The deterministic harness evidence shows that:

- the four-tool profile is identical across memory conditions;
- no-memory requests contain no answer-bearing context;
- eager memory is confined to its fixture;
- MPM retrieval is project-scoped;
- active memory survives a controlled history clear and loop restart while
  retrieval-only does not answer the follow-up.

These support the **mechanism** needed to test the working-memory hypothesis,
not the hypothesis about live model behaviour.

The live question remains open:

- Does a live model use the retrieved fact?
- Does it resist the injected instruction?
- Does it prefer the current over the stale record?
- Does active memory improve follow-up recall beyond retrieval-only?
- What are the actual prompt, completion, cache and latency costs?

No live result supports or contradicts the hypothesis because no authenticated
completion occurred.

## 8. Preserved artifacts

```text
.lab/bench/003g/validation/go-test.txt
.lab/bench/003g/qualification/credential-qualification.json
.lab/bench/003g/auth-probe-20261030/{manifest.json,summary.json,trials/...}
.lab/bench/003g/mock-control-003g/{manifest.json,summary.json,trials/...}
.lab/bench/003g/auth-probe-20261030.out
.lab/bench/003g/mock-control-003g.out
```

The qualification artifact contains paths, model metadata, expiry, claim type
and a boolean credential-presence result. It contains no token, account email
or other credential material.

## 9. Acceptance assessment

| Criterion | Result |
|---|---|
| Successful authentication | **Fail** — expired credential, HTTP 401 |
| Credential isolation | Pass — synthetic live transport and bad-location tests |
| Independent clean trials | Pass for deterministic mock control and live preflight |
| Identical model/tool conditions | Pass for the controlled mock configuration |
| Evidence-backed correctness scoring | Available in harness; no live score |
| Traceable provider usage | None for live; missing usage is null, not zero |
| No invented results | Pass — live portion stopped |
| No production/memory optimisation | Pass |

The correct next action is to supply a valid project-scoped credential and
re-run the bounded pilot. The harness is ready; the live evidence is not.
