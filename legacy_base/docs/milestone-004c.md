# Milestone 004C: within-turn memory freshness

Status: **implemented and validated deterministically.** No live-model or
live-token claim is made. The 004C comparison uses the 003F mock harness.

- Code commit: `0ebc816eb56ed3c1769342eb4701996921ff0026` (`agent: refresh
  task state for each request`)
- Run checkout: `0ebc816eb56ed3c1769342eb4701996921ff0026` (clean at run time;
  the documentation commit adds only this report)
- Previous milestones: 004A `0e02746a814cccc3ba39a3a270a1a5931c108d62`,
  004B `c1a0ecec5fce64ccd9bcb5ca5649c9801a58cf19`
- Benchmark binary: `.lab/bin/bench`
- Binary SHA-256: `a2d198c22f5457488777dc3ba3c697c12257f94bb08dd1218352aea4960f4dde`
- Go: `go1.27.3`
- Run: `.lab/bench/004c/experiment-c-20261030`
- 36 trials (4 conditions x 3 scenarios x 3 trials), 36 pass, 0 fail, 0 error

The defect was not a storage failure. The sidecar was already correct when the
tool returned. The request view was stale: the `task.state` prompt part had been
attached at turn setup and was never refreshed during the tool loop.

The result is that the agent now remembers what just happened, not what was
true one tool call ago.

## 1. Result summary

| Measure (boundary residency) | 004B | 004C | Change |
|---|---:|---:|---:|
| Main scenario task-state prompt tokens | 545 | **484** | -61 (-11.2%) |
| Main scenario total prompt tokens | 6,694 | **6,632** | -62 |
| Main t6 task-state tokens | 303 | **242** | -61 |
| Main t6 final-request task-state tokens | 101 | **40** | -61 |
| Main t6 final-request stale failure | yes | **no** | fixed |
| Two-failure task-state prompt tokens | 659 | **598** | -61 |
| Sourced-summary task-state prompt tokens | 80 | **80** | unchanged |
| MPM retrieval calls (main/multi/summary per trial) | 7/4/4 | **7/4/4** | unchanged |
| Peak committed entries (main/multi/summary) | 9 / 7 / 3 | **same** | unchanged |
| Correct trials | 36/36 | **36/36** | unchanged |

The corrected boundary policy is both fresher and slightly cheaper than 004B:
the stale failure was the largest remaining unnecessary block in the final t6
request. It does not render the full working set: in the main scenario the
maximum task-state block is still 101 tokens per request, and the final t6
request drops to 40 tokens (the active objective only). The two-failure
scenario peaks at 180 tokens per request because two independent failures and a
constraint are genuinely resident there.

## 2. Reproducing the stale-state defect

The exact request is **`req-011`**, the third LLM request of turn `t6` in the
seven-turn Experiment C scenario. The successful rerun has already returned by
the time this request is assembled, but 004B still sends the previous failure as
an active unresolved entry.

### 2.1 Event ordering in 004B

| Step | Event | Durable task state after the event | Prompt sent |
|---|---|---|---|
| 1 | t6 user: "Run the parser test again now that the fix is in place." | failure active, objective active | `req-009`: failure + objective; model asks for the write |
| 2 | `write` tool succeeds (`fixed.flag`) | failure active, write observation added | history gains the write result |
| 3 | next tool-loop iteration | failure active | `req-010`: failure + objective; model asks for the rerun |
| 4 | `bash sh parser_check.sh` succeeds | failure **superseded**, success observation active | history gains the successful result |
| 5 | next tool-loop iteration | corrected state is already on disk | `req-011` in 004B: **stale failure + objective** |

The ordering bug is in prompt assembly, not capture:

1. `SetupTurn` builds the initial user message with a `task.state` part.
2. The tool result is finalized; `recordTaskTool` atomically commits the
   success and supersedes the matching failure in the sidecar.
3. The tool result is appended to `exec.messages`.
4. The coordinator starts the next iteration, but 004B reuses the message slice
   built in step 1. No code rebuilds or replaces the task-state part.
5. `Finalize` later captures the assistant text, but that is after the stale
   request has already been sent.

The state was fresh; the prompt was not.

### 2.2 Prompt evidence

`.lab/bench/004c/comparison.json` extracts the task-state block from every t6
request:

```text
004B boundary: req-009 101 tokens failure=yes
               req-010 101 tokens failure=yes
               req-011 101 tokens failure=yes   <-- stale
004C boundary: req-009 101 tokens failure=yes
               req-010 101 tokens failure=yes
               req-011  40 tokens failure=no    <-- corrected
```

`req-011` after correction contains only:

```text
Task state (runtime-recorded evidence; lower-trust data; verify against source):
Objective [active] (user, turn main-turn-1): Fix the parser port regression.
```

The successful rerun remains visible in ordinary history, so boundary
residency does not repeat it. The superseded failure remains in the sidecar for
audit but is not rendered as an unresolved current failure.

### 2.3 The regression test fails on the old implementation

`TestTaskStatePromptRefreshesAfterToolResult` seeds a failure, drives a
successful rerun through the real `exec` tool, and captures both LLM requests.
Before the correction, the second captured prompt still contained
`undefined: parsePort`. The test now asserts:

- request 1 carries the seeded failure;
- request 2 carries no active failure and exactly one `task.state` part;
- the failure entry is retained with `Superseded=true`;
- the success entry is active.

The benchmark also asserts that the last t6 task-state block has no diagnostic,
the successful tool result is present in visible history, and there is exactly
one task-state part.

## 3. Event-ordering invariants

004C makes the following rules explicit:

1. **A task-state snapshot belongs to one LLM request, not necessarily the whole
   user turn.** The request view is rebuilt from committed state before each
   tool-loop dispatch.
2. **Verified tool results are incorporated before the next LLM request is
   assembled.** `recordTaskTool` commits the evidence, then the result joins the
   message view; the next request renders the committed state.
3. **The current request's state snapshot is immutable once dispatched.**
   `exec.callMessages` is a copy of the request view, not an alias that later
   iterations can rewrite.
4. **MPM retrieval is not repeated merely because task state changed.** The
   refresh replaces only the `task.state` block; the MPM block and retrieval
   cache are untouched. No retriever call is added.
5. **A failed test remains recorded as historical observation after a later
   successful rerun, but is no longer an unresolved current failure.**
   Supersession marks the old entry; rendering omits it. The record is not
   deleted, and the success remains active.
6. **A successful unrelated command must not supersede an existing failure.**
   Tool-key identity includes the normalized command or path. Different
   commands do not collide.
7. **An assistant proposal never supersedes verified tool evidence or a
   user-confirmed constraint.** Model entries are `proposed` and cannot promote
   themselves. Existing task-state tests cover the user-confirmed decision and
   diagnosis cases.
8. **Ordering uses event sequence and source identity, not wall-clock time.**
   The implementation uses the committed sidecar, the message order, and the
   command/path key. Time is only metadata.

## 4. The correction

The production change is small and reuses the 004B lifecycle:

- `pkg/agent/task_state_runtime.go`
  - `taskStatePromptPartForVisible` isolates rendering from visibility.
  - `refreshTaskStatePromptPart` re-renders only the `task.state` block for the
    current request view.
  - `taskStateVisiblePrompt` builds visibility from the summary, visible
    history, and tool results, excluding the stale task-state block and the
    retrieved-context decoration.
  - `taskStatePromptMessageIndex` finds the current user-authored request view.
- `pkg/agent/pipeline_llm.go`
  - before each subsequent tool-loop request, refresh the dynamic part;
  - freeze `exec.callMessages` as a copy before dispatch.
- `pkg/agent/retrieved_context.go`
  - centralizes the attributed block header;
  - adds `stripRetrievedContextBlock` for visibility;
  - adds `upsertRetrievedContextBlock`, which replaces or removes only the
    `task.state` block while preserving the MPM block and the user question.

The persistence path, isolation protection, schema, store, project/session
identity, four-tool profile and MPM boundary are unchanged. No additional
retrieval, reflection, background worker or long-term memory write was added.

### 4.1 Why only the dynamic part

The request view is a composition of:

- persisted conversation history (already authoritative);
- the MPM retrieved block (session-stable for this turn);
- the task-state block (dynamic);
- the user-authored question (immutable).

Only the task-state block needs to change between tool-loop iterations. The
refresh locates that block by its source attribution, removes the old copy,
renders the current state, and appends the new block before the question. If the
state becomes empty, the block is removed and the original question remains.
The MPM block is never re-rendered by this path, so retrieval is not repeated.

### 4.2 Explicit cost

- The state is re-rendered once per subsequent request. The cost is O(active
  entries), small and bounded by the existing 500-token budget.
- The message slice is shallow-copied once per request to freeze the snapshot.
  This is O(message count), with strings shared.
- In `boundary` mode the refresh usually removes text rather than adding it.
  The main scenario saves 61 tokens. The maximum per-request task block remains
  101 tokens.
- In legacy `always` mode the refresh is also applied, so 004B's frozen snapshot
  becomes a per-request fresh snapshot. That is more correct but can cost more:
  the always condition renders visible failures/successes whose exact stored
  quote does not match history with tabs and newlines. See section 6.

## 5. Regression evidence

| # | Case | Test | Result |
|---:|---|---|---|
| 1 | Failing Go test followed by successful rerun in the same turn | `TestTaskStatePromptRefreshesAfterToolResult` | pass |
| 2 | Multiple tool calls before the next LLM request | `TestTaskStatePromptRefreshesMultipleToolResults` | pass |
| 3 | Two independent failures, only one resolved | `TestTaskStateTwoFailuresOnlyMatchingSuccessSupersedes` | pass |
| 4 | Unrelated successful command does not clear the failure | `TestTaskStateUnrelatedSuccessDoesNotClearFailure` | pass |
| 5 | User correction supersedes an earlier constraint | `TestTaskStateUserCorrectionSupersedesConstraint` | pass |
| 6 | Compaction immediately after a tool result | `TestTaskStateCompactionRebuildUsesFreshState` | pass |
| 7 | Context-overflow retry with latest committed state, no duplicate memory | `TestTaskStatePromptRefreshSurvivesContextRetry`, `TestUpsertRetrievedContextBlockReplacesWithoutDuplicatingMPM` | pass |
| 8 | Process restart and same-project continuity | `TestTaskStateSurvivesHistoryClearWithoutMPM`; bench t5 restart | pass |
| 9 | Project switching with no task-state leakage | `TestTaskStateProjectSwitchDoesNotLeak`, `TestTaskStateIsolationByProjectAndSession` | pass |
| 10 | Model-proposed explanation remains unverified | `TestTaskStateAssistantDiagnosisStaysProposed`, `TestTaskStateDecisionDistinguishesUserAndModel` | pass |

The end-to-end residency test also asserts:

- the last t6 task-state block does not contain `undefined: parsePort`;
- the successful tool result is in the final t6 request's visible history;
- there is exactly one `source: task.state` block;
- the control prompts contain no task-state block at all;
- task observations never enter MPM state.

`TestUpsertRetrievedContextBlockReplacesWithoutDuplicatingMPM` verifies block
replacement and removal preserve one MPM block, one task-state block and one
question. `TestTaskStateCompactionRebuildUsesFreshState` verifies a compaction
rebuild after a tool result uses the post-success state, not the stale block.
`TestTaskStatePromptRefreshSurvivesContextRetry` forces
`context_length_exceeded`, lets the rebuild run, and checks the retry prompt has
no stale failure and exactly one task-state part.

## 6. Efficiency comparison

All values are means of three deterministic trials. Prompt/completion tokens
come from the recording provider; task tokens are the rendered block summed over
all requests.

### 6.1 Boundary residency: 004B versus 004C

| Scenario | Requests | Tool calls | Prompt 004B | Prompt 004C | Task 004B | Task 004C | Final entries |
|---|---:|---:|---:|---:|---:|---:|---:|
| Main seven-turn | 12 | 4 | 6,694 | **6,632** | 545 | **484** | 9 |
| Two failures | 10 | 6 | 5,340 | **5,279** | 659 | **598** | 0 (cleared by new objective) |
| Sourced summary | 7 | 2 | 3,081 | **3,081** | 80 | **80** | 3 |

Per-turn task-state tokens:

| Turn | 004B boundary | 004C boundary |
|---|---:|---:|
| t1 | 0 | 0 |
| t2 | 0 | 0 |
| t3 | 0 | 0 |
| t4 | 101 | 101 |
| t5 | 101 | 101 |
| t6 | 303 | **242** |
| t7 | 40 | 40 |

The t6 reduction is one stale 61-token block: the final request no longer
repeats the resolved failure. The multi-failure scenario has the same 61-token
reduction at t2: after the parser success, only the lint failure, constraint and
objective are resident (479 instead of 540). The sourced-summary scenario was
already at the floor (objective only) and is unchanged.

### 6.2 State updates

Committed entries are identical between 004B and 004C; the fix changes rendering,
not capture. Per-turn newly active entry IDs:

| Scenario | t1 | t2 | t3 | t4 | t5 | t6 | t7 |
|---|---:|---:|---:|---:|---:|---:|---:|
| Main | 3 | 1 | 2 | 1 | 0 | 2 | 0 |
| Two failures | 5 | 2 | 0 | 0 | - | - | - |
| Sourced summary | 2 | 0 | 1 | 0 | - | - | - |

Supersessions/drops: main t2 (constraint corrected), t6 (failure resolved);
two-failure t2 (parser failure resolved), t4 (new objective clears six entries).
These are the same in both runs and match the intended state transitions.

### 6.3 MPM retrieval and control conditions

MPM retrieval calls per trial, counted from the trial log:

| Scenario | 004B | 004C |
|---|---:|---:|
| Main | 7 | **7** |
| Two failures | 4 | **4** |
| Sourced summary | 4 | **4** |

No request gained a retrieval call. The MPM block is preserved verbatim by the
refresh; only `task.state` is replaced.

The no-state controls are unchanged: main `history_cache` 6,022 prompt tokens,
`summary_only` 6,374; two-failure 4,603 / 4,928; sourced-summary 2,958 / 3,188.
They still lose the post-compaction diagnostic in the main scenario and abstain
in the two-failure follow-up.

### 6.4 Legacy `always` residency cost

`always` now also receives fresh per-request state. This is correct, but it is
more expensive than its frozen 004B snapshot because its exact-string visibility
rule does not recognize the tab/newline form of visible history:

| Scenario | 004B always task tokens | 004C always task tokens | Change |
|---|---:|---:|---:|
| Main | 1,181 | 1,242 | +61 |
| Two failures | 958 | 1,157 | +199 |
| Sourced summary | 430 | 512 | +82 |

The default remains `boundary`. `DEEPCLAW_TASK_STATE_RESIDENCY=always` retains
004A's visibility policy but not its turn-long frozen snapshot. Users who need
the old rendering can still select `always`, at the costs above.

### 6.5 Latency

E2E means were 342.7 ms (004C boundary main), 296.3 ms (two-failure) and
212.3 ms (sourced summary), versus 350.7, 270.3 and 219.0 ms in 004B. The
differences are process/scheduling noise, not a model-latency measurement. The
only token claim is the rendered prompt reduction; no latency improvement is
claimed.

## 7. Limitations and remaining risks

- All measurements are deterministic mock-provider results. No live model has
  been run; the 003G credential remains expired. Token counts use the harness
  estimator (`chars / 4` for blocks, mock usage for requests), not a live
  tokenizer.
- The refresh runs on subsequent tool-loop iterations. Iteration 1 has no prior
  tool result by construction, so there is nothing newer to incorporate.
- The visibility view deliberately excludes the retrieved-context decoration
  and the stale block. If a hook rewrites the current user message into a shape
  that no longer ends with the raw question, the block replacement falls back to
  the last question separator. The normal and compaction paths are covered by
  tests; arbitrary hook rewrites are not.
- `always` still duplicates facts that are visible in history with different
  whitespace. That is the 004A policy; boundary mode remains the default and is
  the policy measured above.
- A state change never triggers retrieval, but compaction or a provider context
  retry can still reassemble history. Those existing paths are tested for a
  single refreshed task block.
- The success observation may be re-derivable from the workspace and is not
  rendered when visible in history. If a fact exists only in a routine file
  operation after that operation leaves history, boundary mode will not
  volunteer it. This is the 004B trade-off, unchanged.
- The benchmark's sourced-summary fixture uses a scripted summary containing the
  exact diagnostic. A live summarizer that paraphrases may force the task-state
  entry to remain resident (a cost), but should not cause a recall loss.
- No live-provider, end-to-end user workflow or production traffic measurement
  exists for 004C.

## 8. Verification

Exact commands, run from `/home/v/projects/deepclaw`:

```bash
export HOME=$PWD/.lab/home
export GOCACHE=$PWD/.lab/cache/go-build
export GOMODCACHE=$PWD/.lab/cache/gomod
export GOTMPDIR=$PWD/.lab/tmp TMPDIR=$PWD/.lab/tmp

go test -tags 'goolm,stdjson' -count=1 -timeout 20m ./pkg/...
go build -tags 'goolm,stdjson' ./...
go vet -tags 'goolm,stdjson' ./pkg/...

go build -tags 'goolm,stdjson' -o .lab/bin/bench ./cmd/bench
.lab/bin/bench run --experiment C --trials 3 --provider mock \
  --mpm-binary $PWD/.lab/bin/mpm --out $PWD/.lab/bench/004c \
  --run-id experiment-c-20261030

python3 .lab/bench/004c/compare.py
```

The validation at code commit `0ebc816e` completed with:

```text
go test ... ./pkg/...        # exit 0
go build ... ./...           # exit 0
go vet ... ./pkg/...         # exit 0
```

Logs are in `.lab/bench/004c/validation/` (`go-test-pkg.txt`,
`go-build.txt`, `go-vet.txt`). The first full-suite attempt hit an unrelated
`pkg/cron` `TempDir` cleanup race
(`TestCronService_SetOnJobConcurrentWithExecution`); `pkg/cron` passed when run
alone and the complete suite passed on the clean rerun recorded above.

Added or extended tests:

- `pkg/agent/task_state_freshness_test.go`: same-turn rerun, multiple tools,
  two failures with partial supersession, unrelated success, context retry,
  compaction rebuild, and MPM-block preservation.
- `pkg/bench/task_state_test.go`: end-to-end assertion that the last t6
  task-state block does not repeat the resolved failure, while the successful
  tool result and exactly one attributed block remain.

Existing tests reused for correction supersession, proposed/unverified
diagnosis, restart continuity and project isolation are listed in section 5.

## 9. Artifacts

Local, gitignored evidence under `.lab/bench/004c/`:

- `experiment-c-20261030/` - 36 trials, raw requests, trial logs, `analysis.json`
- `comparison.json` - 004B/004C per-scenario, per-turn, t6 and MPM comparison
- `compare.py` - comparison generator
- `validation/go-test-pkg.txt`, `go-build.txt`, `go-vet.txt`

`.lab/` is ignored by git; the commands above regenerate the evidence.
