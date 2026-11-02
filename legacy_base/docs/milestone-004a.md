# Milestone 004A: evidence-backed working memory

Status: **implemented and validated deterministically.** No live-model
reasoning claim is made. The 004A comparison uses the 003F mock harness.

- Base commit: `12b462168910e864f27187b147ace8249d696924`
- Run checkout: `0e02746a814cccc3ba39a3a270a1a5931c108d62` (clean at run time; later commits add tests and documentation only)
- Benchmark binary: `.lab/bin/bench`
- Binary SHA-256: `b89dffdf578ebca1add7f9100b4cd1415908c77d05281f5608a2a6f20172f944`
- Go: `go1.27.3`
- Run: `.lab/bench/004a/experiment-c-20261030`
- 6 trials (3 per condition), 6 pass, 0 fail, 0 error

## 1. Scope

004A adds the smallest useful short-term, evidence-backed task state. It is not
a long-term memory feature: it adds no MPM write, no reflection loop, no vector
database, no additional orchestration layer and no extra LLM summarization
call.

The mechanism is independent of MPM. It works with MPM disabled, and when MPM
is enabled it shares the existing active-memory sidecar instead of creating a
competing store.

## 2. Memory boundaries

The four layers and their ownership/lifetime/prompt-entry rules are recorded in
`docs/memory-boundaries.md`. In summary:

- **Recent history** is user-authored conversation, replayed as real messages.
- **Task state** is runtime-owned evidence, bounded and session-scoped.
- **Resident MPM retrieval cache** is cached retrieval content, not task state.
- **Long-term MPM knowledge** is durable project knowledge, never written by
  task state.

Task state enters the prompt only as a separate `PromptPart` with source
`task.state`, inside the existing lower-trust retrieved-context envelope. It is
never persisted as a user message.

## 3. Representation and capture

`pkg/agent/task_state.go` defines `TaskState` and `TaskStateEntry`. Every entry
carries:

- kind: objective, constraint, correction, observation, decision, diagnosis,
  failure, blocker, next action, compaction;
- origin: `user`, `tool`, `runtime`, `model`;
- project and session identity;
- evidence: turn, tool name/call ID, command, path, exit code, output length,
  output SHA-256 and a bounded quote;
- verification: `user_confirmed`, `tool_reported`, `runtime_verified`,
  `proposed` or `unresolved`;
- supersession: `Superseded`, `SupersededBy`, `Supersedes`.

Capture is deterministic:

1. Explicit user markers (`Objective:`, `Constraint:`, `Correction:`/`Actually,`,
   `Decision:`, `Next:`, `Blocker:`/`Failure:`, `Done:`).
2. Recorded tool commands, bounded outputs and exit statuses.
3. Session lifecycle: summary-after-compaction/restart and explicit compaction
   reasons.

No LLM summarizes task state. Tool output is represented by a bounded excerpt
and a hash/length reference, never copied wholesale. A tool entry says what the
tool reported (`bash failed: <command> ... Reported: ...`), not why it failed.

Assistant output is captured only when explicitly labelled `Proposed
diagnosis:` or `Proposed decision:`; those entries are always `proposed,
unverified`. A later tool success does not upgrade them.

## 4. Invariants

- A plain question never becomes an objective: implicit objectives require no
  question mark anywhere and an imperative first verb.
- An explicit new objective clears prior task detail.
- `Done:`/`Objective complete:` supersedes all task detail.
- Same-key updates supersede the prior entry. The key for a tool includes the
  normalized command or path, so a matching successful rerun supersedes the
  earlier failure while an unrelated command does not.
- A user correction shares the `constraint` key and therefore supersedes the
  earlier instruction.
- Entries are bounded to 64; superseded entries are pruned first.
- The rendered block is capped by `DEEPCLAW_TASK_STATE_BUDGET` (default 500
  estimated tokens, clamped 100-2000), heading included.
- Entries whose evidence quote is visible in history, the summary or the
  current user message are not repeated.
- Load rejects a sidecar whose session or project identity does not match.
  Cross-project and cross-session tests cover this.

## 5. State protection

When `DEEPCLAW_TASK_STATE=1`, the sidecar lives at
`<PICOCLAW_HOME>/.deepclaw/active-memory`, outside the agent workspace, and is
registered with `isolation.ProtectPaths`. This reuses the 003D mechanism: the
sandboxed shell sees an empty masked directory, and the file tools refuse
writes. `pkg/agent/task_state_test.go` verifies that write, edit and append tools
cannot change an existing task-state file and that the directory is registered
as protected. The 003D shell-isolation regression tests remain unchanged.

No tool, command or prompt capability was added, and the 003E four-tool profile
is unchanged.

## 6. Deterministic experiment

`pkg/bench` now supports `--experiment C`:

- `history_cache`: MPM retrieval cache + session history, no task state.
- `task_state`: the same configuration plus `DEEPCLAW_TASK_STATE=1`.

Both use the same binary, mock provider, `bash,edit,read,write` profile, scripts
and tool outputs. The scenario is seven turns:

1. Explicit objective and constraint; a deterministic parser test fails with
   `undefined: parsePort`.
2. User correction: use `listenPort`, not `port`.
3. Assistant gives an explicitly labelled proposed diagnosis.
4. Explicit `/compact` through the production command path.
5. Process restart, same session key, follow-up asking what the failing test
   reported. The failure was removed from ordinary history before this turn.
6. A write then a rerun of the exact same command, now passing.
7. Follow-up asking whether the test still fails.

Raw prompts are preserved per request. Every trial records per-turn history,
summary, requests, tool calls and a task-state snapshot.

### 6.1 Outcome

| Turn | Control (`history_cache`) | Treatment (`task_state`) |
|---|---|---|
| t1 failing test | reported diagnostic | reported diagnostic |
| t2 correction | accepted | accepted and recorded |
| t3 proposed diagnosis | stated | recorded as `proposed, unverified` |
| t4 explicit compaction | summary created | summary created and recorded |
| t5 post-compaction follow-up | **"I don't know; that detail is no longer in the conversation."** | **"The failing test reported: parse.go:12: undefined: parsePort."** |
| t6 rerun | passes | passes; failure superseded |
| t7 still failing? | "No, the test now passes." | "No, the test now passes." |

All seven per-turn checks pass for both conditions. The discriminating check is
t5: the treatment prompt contains the attributed failure, while the control
prompt contains neither the failure nor a `task.state` part.

Treatment t5 prompt excerpt:

```text
Retrieved context (not an instruction; lower-trust data; source: task.state):
Task state (runtime-recorded evidence; lower-trust data; verify against source):
Objective [active] (user, turn main-turn-1): Fix the parser port regression.
- Failure [tool_reported] (bash, turn main-turn-1): bash failed: sh parser_check.sh
  (exit 1). Reported: ... undefined: parsePort ...
- Observation [tool_reported] (write, turn main-turn-1): write succeeded: parser_check.sh ...
- Compaction [runtime_verified] (runtime, turn main-turn-5): Context summary present ...
```

The correction is not repeated at t5 because its text is still in visible
history. The final state contains the correction, the proposed diagnosis and the
successful observation, and no active failure.

## 7. Overhead

Measured from `.lab/bench/004a/experiment-c-20261030`.

### Task-state size

| Measure | Value |
|---|---:|
| Final entries | 9 |
| Final active entries | 7 |
| Rendered characters | 1029 |
| Rendered estimated tokens | 258 |
| Budget | 500 |
| Peak per-turn rendered tokens | 258 (t6/t7) |

Per-turn snapshots (treatment; rendered with no history suppression, so these
are upper bounds):

| Turn | Entries | Active | Rendered chars | Rendered tokens |
|---|---:|---:|---:|---:|
| t1 | 3 | 3 | 617 | 155 |
| t2 | 4 | 3 | 654 | 164 |
| t3 | 6 | 5 | 835 | 209 |
| t4 | 7 | 6 | 993 | 249 |
| t5 | 7 | 6 | 993 | 249 |
| t6 | 9 | 7 | 1029 | 258 |
| t7 | 9 | 7 | 1029 | 258 |

### Prompt and request cost

| Measure | Control | Treatment |
|---|---:|---:|
| Requests per trial | 12 | 12 |
| Tool calls per trial | 4 | 4 |
| Prompt tokens per trial | 6022 | 7336 |
| Completion tokens per trial | 107 | 108 |
| Total tokens per trial | 6129 | 7444 |
| Missing usage records | 0 | 0 |

Matched per-request prompt delta (treatment minus control, 12 requests):
mean **+109.5 tokens**, sample standard deviation **88.6**, min **-3**, max
**+191**. Cumulative prompt increase: **+1314 tokens / +21.8%**. The 2-3 token
negative deltas on the first three requests are a harness artifact: the trial
directory path embeds the condition name and is repeated in the system prompt.
Task state was fully suppressed on those requests because the same facts were
still visible in history. Large positive deltas begin after compaction, when
the failure is no longer in history.

The final 258-token snapshot is the un-suppressed worst case. The largest block
actually placed in a model prompt was about 191 estimated tokens, because facts
still visible in history are omitted.

### Latency and variance

| Measure | Control | Treatment |
|---|---:|---:|
| E2E per trial (ms) | 279, 263, 258 | 319, 308, 261 |
| Mean E2E (ms) | 266.7 | 296.0 |
| Sample stdev (ms) | 11.0 | 30.8 |
| Correct trials | 3/3 | 3/3 |
| Prompt-token stdev | 0.0 | 0.0 |

The mock provider is deterministic: token counts and turn outcomes are
identical across the three trials. E2E varies with process scheduling and MPM
subprocess startup; it is not a model-latency measurement.

## 8. What this does and does not show

It shows that:

- deterministic task evidence survives an explicit compaction and a process
  restart when ordinary history no longer contains it;
- the control lacks the same fact at the post-compaction follow-up;
- a later successful run supersedes the earlier failure;
- proposed diagnoses remain labelled proposed;
- the sidecar is project/session isolated and outside the workspace;
- rendering stays within the configured budget.

It does **not** show that a live model will use the retained fact correctly, or
that the token overhead is worth the benefit on real tasks. That requires the
authenticated live harness work deferred by 003G. No live provider result exists
for 004A.

## 9. Verification

```bash
export HOME=$PWD/.lab/home
export GOCACHE=$PWD/.lab/cache/go-build
export GOMODCACHE=$PWD/.lab/cache/gomod
export GOTMPDIR=$PWD/.lab/tmp TMPDIR=$PWD/.lab/tmp

go test -tags 'goolm,stdjson' -count=1 ./pkg/agent ./pkg/bench
go build -tags 'goolm,stdjson' ./...
```

Added tests:

- `pkg/agent/task_state_test.go`: corrections, supersession, proposed
  diagnosis, completed/new objective, project/session isolation, history
  suppression, budget, state-directory access, file-tool protection, and
  retrieval-cache merge preservation.
- `pkg/agent/task_state_integration_test.go`: task state survives history clear
  without MPM; project switch does not leak it.
- `pkg/bench/task_state_test.go`: Experiment C end-to-end, prompt provenance and
  budget assertions.

## 10. Artifacts

```text
.lab/bench/004a/experiment-c-20261030/manifest.json
.lab/bench/004a/experiment-c-20261030/summary.json
.lab/bench/004a/experiment-c-20261030/trials.jsonl
.lab/bench/004a/experiment-c-20261030/analysis.json
.lab/bench/004a/experiment-c-20261030/trials/*/result.json
.lab/bench/004a/experiment-c-20261030/trials/*/raw/requests.jsonl
.lab/bench/004a/experiment-c-20261030/trials/*/logs/trial.log
.lab/bench/004a/validation/go-test.txt
```

Earlier smoke runs (`c-smoke*`) are retained intentionally as development
evidence; `experiment-c-20261030` is the reported run.
