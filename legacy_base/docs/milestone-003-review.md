# Milestone 003 review: active memory or retrieval cache?

Reviewed at commit `b048a4035fc6370fea625af516da813c2025a95c`.

This review is based on the committed code, the committed benchmark notes, the raw
benchmark artifacts under `.lab/bench/`, and one temporary in-package unit test used
only to check project isolation. No production code was changed, no live-provider
benchmark was rerun, and the temporary test was removed after it produced its result.

## Executive summary

The implementation is **not yet a working memory**. It is a session-scoped resident
cache of MPM retrieval results plus a single mutable field containing the latest user
message. It does not capture task state, decisions, constraints, tool observations,
test failures, next actions, provenance, confidence, or invalidation state.

The mechanism does carry a previously retrieved MPM record into a later turn. That is
real and is visible in the prompt. It is not, however, a persistent working
understanding of the task.

Benchmark 003 cannot establish that active memory improves continuity, recall, or token
efficiency. The benchmark sessions were reused between runs, so every condition began
with 59-87 messages of prior conversation. The benchmark fact appeared 12-19 times in
each condition's reused session file, and the turn-1 requests already began with
59-85 prior messages. The "no-memory" condition's own history already contained a
4,858-byte read of its session JSONL, including the fact. All conditions used 18 tools, and
the opt-in four-tool coding profile was not enabled. There was one trial per condition,
no compaction, no restart, no new-session test, no project switch, and no coding task.

Measured active-memory overhead is small but negative: the rendered working set is
about 58 estimated tokens plus a 40-character wrapper, and active memory used 72 more
prompt tokens than retrieval-only on turn 1 and 150 more on turn 2. The turn-2 delta is
confounded by four additional messages from the preceding retrieval-only run.
There was no correctness gain to offset the cost, because every condition already had
the fact.

One genuine isolation defect was found and reproduced: with the same workspace and
session key but a different MPM project, the previous project's resident records are
merged into the new project's prompt. The existing project-isolation test uses two
different workspaces, so it does not exercise this case.

The recommended next step is **B: reduce duplication and prompt overhead, and establish
a clean measurement**, not another layer of memory. The project-isolation defect should
be fixed first because it can contaminate any memory experiment.

---

## 1. What was actually implemented?

### 1.1 Files, types, and interfaces

No new Go interface was introduced. The change adds one concrete store type, one
working-set type, one record type, and a small number of integration points.

| File | Change | Important lines |
|---|---|---|
| `pkg/agent/active_memory.go` | New sidecar store and merge/render logic | `ActiveMemoryRecord` 16-21; `ActiveWorkingSet` 24-30; `ActiveMemoryStore` 33-36; constructor 38-42; path 44-52; load 54-72; save 74-85; delete 87-91; token estimate 93-98; render 101-126; merge 129-175 |
| `pkg/agent/instance.go` | Adds `ActiveMemory *ActiveMemoryStore` to `AgentInstance` | field 48; env-gated construction 205-208; assignment 373 |
| `pkg/agent/pipeline_setup.go` | Loads, merges, saves, and renders the working set once per turn | retrieval 38-48; active branch 49-62; retrieval-only fallback 63-64 |
| `pkg/agent/prompt_turn.go` | Places the rendered memory immediately before the current question | 53-58 |
| `pkg/agent/agent_active_memory_test.go` | Cross-turn, restart, and project-isolation tests | 14-46, 48-84, 86-134 |
| `docs/memory-boundaries.md` | Lifecycle notes | whole file |
| `docs/benchmark-003.md` | Benchmark summary | whole file |

The feature is opt-in through `DEEPCLAW_ACTIVE_MEMORY=1` (`instance.go:205-208`).
It is only constructed when that variable is exactly `"1"`; setting it without a
working MPM retriever has no effect because the integration point is inside the
`MPMRetriever != nil` branch (`pipeline_setup.go:38-49`).

### 1.2 Sidecar storage format and location

The working set is stored as JSON at:

```text
<agent-workspace>/.deepclaw/active-memory/<sanitized-session-key>.json
```

The directory is created with mode `0700` and the file is written with mode `0600`
(`active_memory.go:38-42`, `74-85`). The session key is sanitized by replacing `:`,
`/`, and `\` with `_` (`active_memory.go:44-52`). An empty key or `.` becomes
`default`.

The schema is:

```json
{
  "session_key": "sk_v1_...",
  "project_id": "deepclaw-bench",
  "objective": "Repeat the benchmark secret code.",
  "records": [
    {
      "id": "b8c4448ec681b7c9",
      "content": "What is the benchmark secret code? The benchmark secret code is blue-heron-42.",
      "tags": ["deepclaw-bench", "project:deepclaw-bench"],
      "score": 0.4383715076612066
    }
  ],
  "updated_at": "2026-10-30T11:01:10.471256823Z"
}
```

That example is the actual sidecar left by the active-memory benchmark run at
`.lab/bench/deepclaw/home/.picoclaw/workspace/.deepclaw/active-memory/`.

Notably, the record schema drops `mpm.Record.Pointer` and `mpm.Record.CreatedAt`
(compare `pkg/mpm/retriever.go:28-35` with `active_memory.go:16-21`). It keeps the ID,
content, tags, and retrieval score. It does not keep trust status, scope beyond tags,
a source pointer, or a verification state.

### 1.3 How it is populated and updated

The complete lifecycle is one code path:

1. `SetupTurn` assembles session history and summary
   (`pipeline_setup.go:22-35`).
2. If an MPM retriever exists and the user message is non-empty, it calls
   `Retrieve(ctx, ts.userMessage)` (`pipeline_setup.go:38-43`). Retrieval errors are
   fatal in DeepClaw mode; this preserves the existing explicit failure behavior.
3. If active memory is enabled, it loads the previous sidecar for the session
   (`pipeline_setup.go:50`), reads the configured MPM project ID
   (`pipeline_setup.go:51-54`), and calls
   `MergeActiveWorkingSet(prev, retrieval, ts.userMessage, projectID, 500)`
   (`pipeline_setup.go:55`).
4. It sets `ws.SessionKey = ts.sessionKey`, saves the sidecar
   (`pipeline_setup.go:56-59`), renders it with a hardcoded budget of 500
   (`pipeline_setup.go:60`), and stores the text in
   `ts.mpmMemoryUserContext` (`pipeline_setup.go:61`).
5. `promptBuildRequestForTurn` prepends that text to the current user message:
   `memory + "\n\nQuestion: " + currentMessage` (`prompt_turn.go:53-58`).
6. `BuildMessagesFromPrompt` emits one system message, the session history, and the
   current user message (`context.go:1031-1047`).

There is no other update site. Tool results, assistant messages, user corrections,
and session summaries are never read by active memory. A tool discovery made during a
turn is visible to the model through session history, but it is not added to the
working set, even on the next turn, unless it independently becomes an MPM record and
is retrieved by a later query.

### 1.4 Deduplication, eviction, and budget enforcement

`MergeActiveWorkingSet` (`active_memory.go:129-175`) does the following:

- It always creates a new working set whose `Objective` is the current user message.
- It adds fresh retrieval records first, then previous resident records
  (`active_memory.go:163-172`).
- It deduplicates by MPM record ID (`active_memory.go:147-151`). Fresh records win
  over previous records with the same ID, so a same-ID refresh is possible.
- It bounds insertion by rendering a candidate set and comparing estimated tokens to
  the budget (`active_memory.go:153-159`).
- It drops records that do not fit, silently. There is no eviction log or metric.

This is not a real eviction policy. There is no TTL, no LRU, no recency ordering, no
score decay, and no explicit invalidation. Once a record is resident, it remains until
the process stops writing the sidecar or the file is deleted. A record that is no
longer returned by retrieval is never aged out. When the budget is tight, a newly
retrieved large record can be dropped while a smaller previous record still fits;
nothing in the policy prefers freshness or importance.

The budget is also not a hard cap:

- The header and `Objective` are written before any budget check
  (`active_memory.go:108-113`). A long user message becomes a long objective and can
  exceed 500 tokens by itself.
- The first record is always admitted because the check requires
  `len(records) > 0` before rejecting a candidate (`active_memory.go:157`).
- The token count is `(len(bytes)+3)/4` (`active_memory.go:93-98`), not the model's
  tokenizer and not the shared `tokenizer` package used for tool definitions.
- The budget is hardcoded at 500 in both the merge and render calls
  (`pipeline_setup.go:55,60`). There is no config or environment variable for it.

### 1.5 Session and project isolation

Session isolation is mostly sound:

- The sidecar path is derived from the session key (`active_memory.go:44-52`).
- `Load` rejects a file whose stored `SessionKey` does not match the requested key
  (`active_memory.go:68-70`).

Project isolation is incomplete:

- MPM retrieval filters records by the exact `project:<id>` tag
  (`pkg/mpm/retriever.go:226-231`). That part is enforced in code.
- The sidecar path is workspace-scoped, not project-scoped
  (`active_memory.go:38-42`).
- `Load` checks `SessionKey` but not `ProjectID` (`active_memory.go:54-72`).
- `MergeActiveWorkingSet` accepts a new `project` argument but still merges every
  previous record without checking `prev.ProjectID` or the record's project tag
  (`active_memory.go:139-172`).

Therefore, if the same workspace and session key are used with a different
`DEEPCLAW_MPM_PROJECT`, the previous project's records are carried into the new
project's prompt. This is a real leak, not a theoretical one.

A temporary test reproduced it. It seeded project A and project B in separate MPM
workspaces, used the same agent workspace and session key, cleared the conversation
history between projects, and then asked project B a question. The project B prompt
contained both records:

```text
- (8907d1f855082be4) Project B secret code is beta-2.
- (2e659204481be02f) Project A secret code is alpha-1.
```

The committed project-isolation test does not catch this because it uses different
workspaces (`wsA` and `wsB`) and therefore different sidecar paths
(`agent_active_memory_test.go:92-115`). The test checks the correct behavior for
workspace switching, not for project switching inside one workspace.

There is also a lower-probability filename-collision risk. The sanitizer maps `:`, `/`,
and `\` all to `_` (`active_memory.go:45-47`), so distinct session keys such as
`a:b` and `a/b` map to the same file. `Load` will reject the mismatched `SessionKey`,
but a subsequent `Save` overwrites the other session's sidecar, causing data loss
rather than a cross-session disclosure. Normal PicoClaw session keys are hashed
(`sk_v1_...`), so this is unlikely in the current CLI path, but it is not enforced by
the store itself.

### 1.6 Compaction and restart

Active memory is not integrated with the context manager or compaction pipeline.

- `legacyContextManager.Assemble` reads history and summary directly from the session
  store (`context_legacy.go:23-45`).
- `legacyContextManager.compactSession` summarizes or prunes session messages
  (`context_legacy.go:88-124`) and `pruneSessionToolResults` clears older tool outputs
  (`context_legacy.go:170-202`).
- `Pipeline.CompactBeforeLLM` decides when to compact and rebuilds the prompt
  (`pipeline_compact.go:24-68`).
- None of those paths reads or writes `ActiveMemory`.

As a result:

- The sidecar survives compaction because it is a separate file, but the working set
  is not derived from the session state that compaction is trying to preserve. It
  still contains only the latest objective and previously resident MPM records.
- Compaction does not add constraints, findings, decisions, failures, or next actions
  to the working set. It can summarize them into the session summary, but the working
  set does not consume that summary.
- The working set can survive a process restart if the same workspace and session key
  are reused. `Load` reads the sidecar in the next process. This is covered by
  `TestActiveMemoryRestartsFromSidecar` (`agent_active_memory_test.go:48-84`).
- A corrupt or unreadable sidecar is silently treated as absent because the load error
  is discarded with `prev, _ := ...` (`pipeline_setup.go:50`). A save error, by
  contrast, aborts the turn (`pipeline_setup.go:57-59`). The failure policy is
  inconsistent.

There is a second compaction interaction worth flagging. `reassembleActiveTurn`
rebuilds the prompt with `currentMessage == ""` (`pipeline_compact.go:119`). In that
case `promptBuildRequestForTurn` sets `req.CurrentMessage = ts.mpmMemoryUserContext`
(`prompt_turn.go:53-56`), so the rendered working set becomes the current user message
while the real user message is preserved in the protected history. This can both
duplicate the memory block and present memory as if it were the user's request. The
benchmark did not trigger compaction (`has_summary=false` in the logs), so this path
was not exercised.

### 1.7 Stale or contradicted information

There is a narrow refresh path: if fresh retrieval returns a record with the same ID,
it is added before the previous copy and the duplicate ID is skipped
(`active_memory.go:147-166`). That refreshes the content.

There is no general invalidation path:

- A record that is no longer relevant but is not returned by the new query remains
  resident (`active_memory.go:168-171`).
- A record that is contradicted by a new record with a different ID remains alongside
  the contradiction. There is no relationship, supersession, or confidence field.
- There is no per-record timestamp, so the prompt cannot say which record is newer.
  The global `UpdatedAt` is not rendered.
- There is no TTL, no session-delete hook, and no cleanup job.

The rendered header says "session-scoped; verify before acting"
(`active_memory.go:109`), but that is a generic instruction, not a verification state.
Nothing in the data model distinguishes a verified fact from an unverified MPM record
or from a model claim.

### 1.8 Requirements from Milestone 003 that are missing

| Milestone 003 requirement | Status | Evidence |
|---|---|---|
| Current objective | Partial | `Objective` is overwritten with the latest user message every turn (`active_memory.go:136`); it is not a stable task objective and has no history. |
| Active constraints | Missing | No field or update path in `ActiveWorkingSet` (`active_memory.go:24-30`). |
| Relevant verified findings | Missing | `Records` come only from `mpm.Retrieval`; no tool/session ingestion (`active_memory.go:163-172`). |
| Decisions and rationale | Missing | No field; `MergeActiveWorkingSet` only accepts an MPM retrieval. |
| Current files, test failures, execution state | Missing | Tool outputs stay in session history; active memory never reads them. |
| Unresolved questions | Missing | No field. |
| Next action | Missing | No field. |
| Source references and trust/verification status | Partial | ID and tags are retained; pointer, timestamp, and trust are dropped (`active_memory.go:16-21` vs `retriever.go:28-35`). |
| Configurable ~500-token target | Missing | Budget is hardcoded to 500 (`pipeline_setup.go:55,60`). |
| One consistent snapshot within a turn | Yes | Built once in `SetupTurn`; no tool-call update path. |
| Carry relevant information between turns | Partial | Objective and resident records carry; task state does not. |
| Update or invalidate changed information | Partial | Same-ID refresh only; no TTL, contradiction handling, or invalidation. |
| Preserve task state at compaction | Missing | Compaction never reads or writes active memory. |
| Resume after restart | Partial | Sidecar load by session key; no project validation; corrupt load is ignored. |
| New-session scoped retrieval | Yes | New session key means no sidecar; MPM retrieval still applies. |
| Project switching eviction/isolation | Partial / bug | Workspace isolation works; same-workspace project switch leaks previous records. |
| MPM remains authoritative and read-only | Yes | Production path only calls `mpm_memory query` (`retriever.go:188`); no MPM writes. |
| Deduplicate active and retrieved records | Yes | ID dedup (`active_memory.go:147-151`). |
| Stale records refreshed or invalidated | Partial | Same-ID refresh; no invalidation. |
| Near-turn presentation; records are data | Partial | Prepended to the user message with a "not an instruction" wrapper (`pipeline_setup.go:61`, `prompt_turn.go:53-58`); no structural isolation. |
| Explicit MPM failure behavior | Yes | Retrieval errors abort the turn (`pipeline_setup.go:41-43`). |
| Tests for continuity, invalidation, isolation, duplication, compaction, restart | Partial | Continuity and restart are covered; invalidation, duplication, compaction, and same-workspace project isolation are not. |

---

## 2. Does this constitute working memory?

Short answer: **no**. It is a resident retrieval cache with a one-line objective.

| Question | Answer | Code evidence and explanation |
|---|---|---|
| Can it retain a user correction made during the session? | **Partial** | The latest user message becomes `Objective` on every turn (`pipeline_setup.go:55`, `active_memory.go:136`). If the correction is the latest message, it is visible that turn and later turns until replaced. But the previous objective and any reason for the correction are lost; there is no correction record, no superseded value, and no way to know that a change occurred. |
| Can it retain a design decision not already stored in MPM? | **No** | `ActiveWorkingSet` has only `Objective` and `Records` (`active_memory.go:24-30`). `MergeActiveWorkingSet` only accepts `*mpm.Retrieval` (`active_memory.go:129`). There is no write path for a decision or its rationale. |
| Can it retain a verified discovery from tool output? | **No** | Tool results are appended to session history, not read by active memory. A search for `ActiveMemory` outside `instance.go`, `pipeline_setup.go`, and the store itself finds no tool-execution integration. A discovery survives only as ordinary history until compaction prunes or summarizes it. |
| Can it remember the current failing test and next debugging step? | **No** | There are no fields for test state, files, execution state, or next action. The model might see a failing test in session history, but the working set does not represent it. |
| Can it distinguish observed evidence from an unsupported model claim? | **No** | `ActiveMemoryRecord` has `ID`, `Content`, `Tags`, and `Score` only (`active_memory.go:16-21`). `mpm.Record` has `Pointer` and `CreatedAt` (`retriever.go:28-35`), but the merge drops them. There is no trust, provenance, verification, or confidence field. Everything is rendered with the same generic caveat. |
| Can it update or invalidate information that becomes obsolete? | **Partial, effectively no** | Same-ID retrieval replaces a previous record because fresh records are inserted first (`active_memory.go:147-166`). A record not returned by the new query remains forever (`active_memory.go:168-171`). There is no TTL, contradiction detection, supersession, or invalidation flag. |

The distinction matters. A retrieval cache answers "what did search return recently?"
A working memory answers "what is true about the task now, what did we decide, what
failed, and what should happen next?" The current type answers the first question only.
Calling it `ActiveWorkingSet` is aspirational; the behavior is closer to a
session-scoped retrieval cache with a mutable objective string.

---

## 3. Benchmark 003 explained

### 3.1 Harness

The benchmark script is `.lab/bench/run_active.sh`. It runs four conditions:

- A: baseline PicoClaw without memory (`picoclaw-baseline`, no MPM).
- B: baseline PicoClaw with an eager `MEMORY.md` containing the fact.
- C: DeepClaw retrieval-only MPM.
- D: DeepClaw active memory + MPM.

All conditions use the same logged model `poolside/laguna-xs-2.1:free`, the same
`max_tokens=256`, and the same two-turn script:

1. `What is the benchmark secret code?`
2. `Repeat the benchmark secret code.`

Each condition uses a session name `activebench-<condition>`. The session key is
derived from that name, so repeated benchmark runs in the same home resume the same
session instead of starting fresh. The script also does not set
`DEEPCLAW_CODING_PROFILE=1`, so every run used 18 tools, not the four-tool coding
profile from Milestone 002.

The raw artifacts are:

- `.lab/bench/results_active.json`
- `.lab/bench/raw_active/<condition>_turn<N>.log`
- `.lab/bench/raw_active/<condition>_turn<N>.log.ms`

### 3.2 Measured results

All token values below are the provider's reported usage. They are measurements, not
the local four-bytes-per-token estimate. "Cache read" is the provider's reported
`cache_read_tokens`; it is not subtracted from `prompt_tokens`.

| Condition | Turn | Messages | Prompt tok | Completion tok | Total tok | Cache read | Retrieval (records / tokens / ms) | E2E ms | Answer |
|---|---:|---:|---:|---:|---:|---:|---|---:|---|
| no-memory | 1 | 77 | 9,367 | 21 | 9,388 | 0 | n/a | 2,911 | blue-heron-42 |
| no-memory | 2 | 79 | 9,404 | 15 | 9,419 | 0 | n/a | 1,313 | blue-heron-42 |
| eager | 1 | 59 | 6,083 | 15 | 6,098 | 0 | n/a | 2,781 | blue-heron-42 |
| eager | 2 | 61 | 6,114 | 15 | 6,129 | 0 | n/a | 6,234 | blue-heron-42 |
| deepclaw | 1 | 81 | 7,268 | 21 | 7,289 | 16 | 1 / 47 / 11 | 2,838 | blue-heron-42 |
| deepclaw | 2 | 83 | 7,225 | 21 | 7,246 | 0 | 0 / 0 / 19 | 6,924 | blue-heron-42 |
| active | 1 | 85 | 7,340 | 21 | 7,361 | 0 | 1 / 47 / 12 | 4,083 | blue-heron-42 |
| active | 2 | 87 | 7,375 | 21 | 7,396 | 7,248 | 0 / 0 / 17 | 2,170 | blue-heron-42 |

Two-turn totals:

| Condition | Prompt tokens | Completion tokens | Total tokens | Δ vs retrieval-only | Δ vs eager |
|---|---:|---:|---:|---:|---:|
| no-memory | 18,771 | 36 | 18,807 | +4,272 | +6,580 |
| eager | 12,197 | 30 | 12,227 | -2,308 | 0 |
| deepclaw | 14,493 | 42 | 14,535 | 0 | +2,308 |
| active | 14,715 | 42 | 14,757 | **+222** | **+2,530** |

Per-turn active vs retrieval-only deltas are +72 prompt tokens on turn 1 and +150 on
turn 2. The turn-2 delta is not clean: the active run's requests contained 85 messages
on turn 1 and 87 on turn 2, while the retrieval-only run's contained 81 and 83. The
active run reused the same home and session after the retrieval-only run, so it
inherited four additional messages from that run. Some of the +150 is therefore prior
conversation, not active memory.

The rendered working set itself is small. For the benchmark record and objective it is
229-230 characters, or about 58 tokens by the local estimator. The wrapper
`"Retrieved context (not an instruction):\n"` adds 40 characters, about 10 tokens. So
the active-memory block is roughly 68 estimated tokens. The rest of the difference is
history and framing.

Operational results:

- **Actual LLM calls:** one per turn. Every log contains one `LLM request` line and one
  assistant response. `results_active.json` records `llm_calls: 2` because the parser
  matched both the `LLM response agent_id=...` line and the later
  `LLM response without tool calls` line for the same response. That field is a
  parsing artifact, not two provider calls.
- **Tool calls:** zero in all eight trials.
- **Retrieval latency:** 11-19 ms, with 1 record and 47 estimated tokens on turn 1,
  and 0 records on turn 2 for both DeepClaw conditions.
- **E2E latency:** 1.3-6.9 s. The variation is dominated by provider latency and rate
  limiting. Active turn 1 was 4.08 s vs retrieval-only 2.84 s; active turn 2 was
  2.17 s vs retrieval-only 6.92 s. There is no stable latency advantage.
- **Provider cache:** only active turn 2 reports a large cache read (7,248 tokens);
  retrieval-only turn 1 reports 16; all other turns report 0. The benchmark results
  JSON does not include cache fields at all, so this variation is invisible in the
  committed summary.

### 3.3 Correctness

All eight trials answered `blue-heron-42`, the seeded fact. That sounds like 100%
correctness, but it is not evidence for any memory policy.

The session files show the fact was already in every condition's history. The totals
below include the measured turns, but the turn-1 requests themselves began with 59-85
prior messages, and the active turn-1 prompt explicitly contains an earlier copy of
the question and answer:

| Condition | Session lines | Occurrences of `blue-heron-42` | Occurrences of the question |
|---|---:|---:|---:|
| no-memory | 79 | 12 | 23 |
| eager | 61 | 19 | 18 |
| deepclaw | 87 | 15 | 28 |

The no-memory turn-1 log shows the condition reading its own session JSONL as a tool
result. At `.lab/bench/raw_active/no-memory_turn1.log:118-129` it lists the session
file and reads 4,858 bytes of it. That history contained the fact from earlier runs.
The active turn-1 log likewise contains an earlier `Repeat the benchmark secret code`
question and answer at messages [82]-[83] before the measured current message [84].
So the "no-memory" condition was not memory-free, and every condition could answer
from history alone.

The no-memory answer also begins "Based on the retrieved context..." even though that
condition performs no MPM retrieval. That is an unsupported attribution, although the
fact itself is correct.

There were no coding tasks, no code reading, no search, no edit, no test execution,
and no multi-tool task. The benchmark exercises a single-fact recall, twice.

### 3.4 Why active memory did not outperform the baselines

**Established causes:**

1. **The fact was already in session history in every condition.** The active record
   added no new information. The model could answer from the 59-87 messages in the
   request, most of which were prior conversation.
2. **The session histories differed by condition and were not reset.** Eager had the
   fewest messages (59/61), no-memory had 77/79, retrieval-only had 81/83, and active
   had 85/87. The token ranking mostly follows history length, not memory policy.
   No-memory's 9,367/9,404 prompt tokens include a 4.8 KB self-read of its session
   JSONL. Eager's 6,083/6,114 are consistent with its shorter history; this run does
   not isolate the effect of eager memory from the effect of history length.
3. **The active block is tiny compared with the rest of the prompt.** About 68
   estimated tokens against a 7,375-token prompt is under 1%. It cannot produce a
   large token or correctness effect.
4. **Turn 2 retrieval returned zero records.** Active memory rendered the resident
   record where retrieval-only rendered nothing. That demonstrates carry-over at the
   prompt level, but it also means active memory added a block rather than replacing
   one on that turn.
5. **All conditions used 18 tools.** The four-tool coding profile was not enabled.
   Tool definitions were ~16,957 bytes in every prompt, far larger than the memory
   block.

**Hypotheses that this benchmark does not establish:**

- Active memory might help when the session is compacted and the fact is no longer in
  history. This run had no compaction (`has_summary=false` in both active logs).
- Active memory might help when retrieval misses a paraphrase but a prior record
  remains relevant. Turn 2 is consistent with that, but the history also contained the
  fact, so the model's answer cannot be attributed to the working set.
- Active memory might reduce retrieval calls. It does not, except when the query is
  byte-identical: `Retriever.Retrieve` caches only `lastQuery == query`
  (`retriever.go:144-158`). A paraphrase still invokes MPM.
- Active memory might become more expensive over long sessions as stale records
  accumulate. This run is too short and too small to show that.

### 3.5 Trial-level notes

- The benchmark ran one trial per condition. There is no repetition, no variance
  estimate, and no confidence interval.
- The eager condition's `MEMORY.md` was a single line containing the fact. It appears
  in the system prompt, which is 1,580 characters vs 1,440 for no-memory and 1,176 for
  DeepClaw. The extra ~140 characters are the memory file.
- The no-memory condition used a baseline binary from `.lab/picoclaw-baseline`, while
  the DeepClaw conditions used `.lab/bin/picoclaw-deepclaw`. The system prompt lengths
  differ (1,440 vs 1,176), so the conditions are not perfectly matched even outside
  memory policy.
- The active and retrieval-only conditions share the same home and session key. The
  active run therefore inherited the retrieval-only run's messages. This is the main
  reason the active turn-1 prompt has four more messages than the retrieval-only
  turn-1 prompt.

### 3.6 Missing metrics and missing scenarios

The committed benchmark does not measure:

- per-component prompt tokens (system, tools, history, summary, active memory,
  retrieved memory, current request);
- cache creation/read tokens in the JSON results;
- compaction behavior or post-compaction recall;
- restart and same-session resumption;
- new-session recall from long-term memory;
- project switching or isolation violations;
- abstention when evidence is absent;
- prompt injection from retrieved content;
- relevant facts among substantial irrelevant knowledge;
- stale or contradicted facts requiring invalidation;
- tool calls, coding tasks, or multi-step continuity;
- working-set token size as a separate metric;
- repeated trials or variance.

It also does not test the four-tool coding profile, so the tool-schema overhead that
dominates the prompt is the 18-tool baseline, not the profile introduced in Milestone
002.

---

## 4. Context overhead accounting

### 4.1 Components of the actual prompt

The active turn-2 log reports `prompt_tokens=7,375`, `messages_count=87`,
`system_prompt_len=1,176`, and `tools_count=18`. The following table decomposes that
prompt. Character counts and byte counts are measured; token counts marked "est" use
the local `(bytes+3)/4` estimator and are not provider tokenizer counts.

| Component | Measured evidence | Estimate | Share of 7,375 |
|---|---|---:|---:|
| Stable + dynamic system prompt | `static_chars=953`, `dynamic_chars=216`, total 1,176 chars | ~294 tok | ~4% |
| Tool definitions | 18 tools, 16,957 bytes | ~4,240 tok | ~57% |
| Session history | 85-87 messages; ~9,633 chars in the DeepClaw session JSONL | ~2,409 tok | ~33% |
| Compaction summary | `has_summary=false` | 0 | 0% |
| Active working set | 229-230 chars + 40-char wrapper | ~68 tok | <1% |
| Retrieved MPM content | 47 tokens on turn 1; 0 on turn 2 | 0-47 tok | <1% |
| Current user request | `Repeat the benchmark secret code.` | ~9 tok | <1% |
| Residual | provider framing, role markers, tokenizer differences | ~355 tok | ~5% |

The estimate sums to about 7,020 tokens; the provider reports 7,375. The residual is
expected: the local estimator is approximate, tool JSON tokenizes differently from
plain text, and message framing is not counted. The important conclusion is robust:
tool definitions and session history dominate. The memory block is a rounding error.

This is why schema bytes must not be equated with provider tokens. The 16,957-byte
schema yields an estimate of ~4,240 tokens at four bytes per token; a real provider
tokenizer may produce more because JSON keys, punctuation, and CamelCase identifiers
tokenize poorly. The measured prompt is consistent with the schema being the largest
single component, but the exact token attribution is an estimate.

The 16,957-byte and 5,644-byte schema figures come from a temporary `ToProviderDefs`
probe in the same tree, not from the committed benchmark logs. The logs report
`tools_count=18` but not schema bytes. The probe was removed after measurement.

If the four-tool coding profile were enabled, the measured schema size would fall from
16,957 to 5,644 bytes, a 66.7% reduction, or roughly 2,829 estimated tokens per call.
That is more than forty times the active-memory block. It was not enabled in Benchmark
003.

### 4.2 Duplicated content

There are five distinct duplications:

1. **Objective vs current question.** Active memory renders
   `Objective: <current user message>` (`active_memory.go:110-112`), and
   `prompt_turn.go:57` appends `Question: <same current user message>`. Every active
   turn contains the user's request twice.
2. **Resident record vs session history.** The benchmark fact appears in the active
   record and 15 times in the DeepClaw session history. The model sees both.
3. **Retrieved record vs history.** Retrieval-only turn 1 injected the same fact that
   was already in history (15 occurrences). Retrieval did not add new information.
4. **Eager memory vs history.** Eager `MEMORY.md` contains the fact, and the history
   contains it 19 times.
5. **Compaction rebuild.** During a rebuild, `currentMessage` is empty and the active
   block becomes the current user message (`pipeline_compact.go:119`,
   `prompt_turn.go:53-56`), while the real user message is in the protected history.
   This is a potential duplicate and a role misattribution.

### 4.3 Does active memory replace context or add a layer?

It does both, depending on the turn:

- **Turn 1 with retrieval results:** active mode replaces `retrieval.Text` with
  `ws.Render` (`pipeline_setup.go:60-64`). It does not add a second retrieval block;
  it changes the representation of the same record.
- **Turn 2 with zero retrieval results:** retrieval-only injects nothing; active mode
  injects the resident working set. Here it adds a block.
- **Relative to session history:** active memory never removes or rewrites history.
  It adds a resident copy alongside whatever history already contains. It is another
  layer, not a replacement.
- **Relative to tool definitions:** it does nothing. The 18-tool schema remains.

So the net effect is an additional layer over history, plus a representation change
for retrieval results. It does not reduce the prompt; in this benchmark it increased
it.

---

## 5. Memory boundaries

The following table separates **durable storage**, **active state**, and
**prompt-visible context**. "Active state" means the sidecar contents; "prompt-visible"
means what the model actually receives.

| Boundary | Durable storage | Active state | Prompt-visible | What survives / what is lost |
|---|---|---|---|---|
| **Tool calls within one turn** | Session JSONL and in-memory turn history | Snapshot built once in `SetupTurn`; never updated by tool calls | System + history + current user message + active block | Tool outputs survive in history until compaction. Active memory never learns them. A discovery made mid-turn is not resident until a later MPM retrieval, and only if it is an MPM record. |
| **Consecutive turns in one session** | JSONL history + summary | Sidecar: latest objective + resident records | History + summary + active block + current question | Objective and MPM records carry. Constraints, decisions, findings, failures, questions, and next actions do not. |
| **Compaction** | Summary replaces or prunes older history; tool outputs may be cleared | Sidecar unchanged; not read by the compactor | Summary + protected tail + active block | The summary may preserve some task facts. Active memory preserves only its objective and records, not the compacted task state. A post-compaction rebuild can inject active memory as the current user message. |
| **Process restart** | JSONL + summary + sidecar files | Loaded by session key from the sidecar | Resumed history + summary + active block | Works if the same workspace and session key are reused. A corrupt sidecar is silently ignored; a failed save aborts the turn. |
| **New session in the same project** | New JSONL; MPM long-term records | New sidecar path; no previous sidecar | Fresh history + fresh MPM retrieval | No speculative working notes are inherited. Long-term MPM knowledge is available through retrieval. If the caller reuses the old session key, it is not a new session and the old sidecar is reused. |
| **Switching projects** | Workspace-scoped session store and sidecar; MPM project tag | Sidecar is workspace-scoped, not project-scoped | History + active block + project-filtered MPM retrieval | MPM retrieval is project-filtered in code (`retriever.go:226-231`). The sidecar's previous records are not filtered; same-workspace project switching leaks them. This was reproduced. |

Durable storage, active state, and prompt-visible context are not interchangeable:

- The session JSONL is durable but may be summarized or pruned.
- The sidecar is durable across turns and restarts but is not authoritative and may be
  stale.
- The prompt contains only the subset that is rendered; the sidecar may contain
  records that are omitted by the budget, and history may contain facts that active
  memory never captures.

### D8 and trusted lessons

The D8 trust boundary remains intact in the production path:

- The retriever only calls `mpm call mpm_memory` with `action: "query"`
  (`retriever.go:166-188`). It never calls `save`, `promote`, or a lesson-writing
  action.
- Active memory writes only to its local sidecar (`active_memory.go:74-85`). It never
  writes to MPM.
- The tests seed MPM fixtures with `save`, but that is test setup, not the agent's
  runtime behavior (`agent_mpm_e2e_test.go:36-57`).
- No unverified observation is promoted into a trusted MPM lesson. There is no
  autonomous lesson-writing path.

The caveat is that active memory does persist retrieved MPM content and the latest user
message to a local file. That is not a trusted-lesson write, but it is a new durable
copy of potentially sensitive content, with no TTL or cleanup.

---

## 6. Risks and limitations

### 6.1 Genuine problems

1. **Same-workspace project leak (reproduced).** `MergeActiveWorkingSet` merges
   previous records without checking `prev.ProjectID` or record tags
   (`active_memory.go:139-172`), and `Load` checks only `SessionKey`
   (`active_memory.go:68-70`). A project switch inside one workspace carries the old
   project's records into the new prompt. The committed test uses different
   workspaces, so it passes without covering this case
   (`agent_active_memory_test.go:92-115`).

2. **The token budget is not a hard cap.** The objective is rendered without a budget
   check (`active_memory.go:108-113`); the first record is always admitted
   (`active_memory.go:157`); the estimator is bytes/4, not the model tokenizer
   (`active_memory.go:93-98`); and the budget is hardcoded at 500
   (`pipeline_setup.go:55,60`). A long user message can exceed the budget before any
   record is considered.

3. **No lifecycle cleanup.** `Delete` exists (`active_memory.go:87-91`) but is never
   called. Session pruning deletes the JSONL, not the sidecar. There is no TTL, no
   orphan sweep, and no size cap on the directory. Objectives and retrieved records
   persist indefinitely under `.deepclaw/active-memory/`.

4. **Inconsistent failure handling.** A load error is discarded
   (`pipeline_setup.go:50`), so a corrupt sidecar silently resets the working set. A
   save error aborts the turn (`pipeline_setup.go:57-59`). The same component treats
   read and write failures with opposite severity.

5. **No provenance, trust, or verification state.** The record drops the MPM pointer
   and creation time (`active_memory.go:16-21` vs `retriever.go:28-35`). Everything is
   presented with the same generic "verify before acting" header. The working set
   cannot distinguish a verified observation, a user requirement, an MPM record, and a
   model claim.

6. **No invalidation or staleness policy.** A record not returned by the new query
   stays forever (`active_memory.go:168-171`). Contradictions with different IDs
   coexist. There is no TTL, recency ordering, supersession, or confidence.

7. **Compaction is not integrated, and the rebuild path can misplace memory.**
   Compaction never reads or writes active memory. During a compaction rebuild,
   `currentMessage` is empty, so the active block becomes the current user message
   (`pipeline_compact.go:119`, `prompt_turn.go:53-56`). The real user message remains
   in protected history, so the prompt can duplicate the memory and present it as a
   user request.

8. **Prompt injection is mitigated only by wording.** Retrieved records are inserted
   verbatim into a user message with a "not an instruction" prefix
   (`pipeline_setup.go:61`). Active memory makes a retrieved record resident across
   turns. If MPM content contains instructions, the model may follow them. The wrapper
   is not a structural boundary and there is no sanitization or instruction hierarchy
   enforcement.

9. **Benchmark 003 is not a valid comparison.** Reused sessions, contaminated
   histories, different message counts, one trial per condition, 18 tools, no coding
   profile, no compaction, no restart, no project switch, no abstention, no injection,
   and no coding task. The correctness and token results cannot be attributed to the
   memory policy.

10. **Non-atomic writes and process-local locking.** `Save` uses `os.WriteFile`
    directly (`active_memory.go:84`); the mutex is per store instance
    (`active_memory.go:33-36`). Two processes sharing a workspace and session can
    clobber each other or leave a truncated file. A crash during write can lose the
    sidecar.

11. **Filename collisions can cause data loss.** The sanitizer maps `:`, `/`, and `\`
    to `_` (`active_memory.go:45-47`). Distinct keys that sanitize to the same name
    share a file; `Load` rejects the mismatch, but `Save` overwrites the other
    session's state. Normal hashed session keys make this unlikely, but the store does
    not enforce uniqueness.

12. **Formatting is not clean.** `gofmt -l` reports `pkg/agent/instance.go` and
    `pkg/agent/pipeline_setup.go` at this commit. The code compiles and tests pass,
    but the added struct fields and calls are not gofmt-aligned.

### 6.2 Theoretical or lower-priority risks

- **Path traversal:** The sanitizer replaces `/` and `\`, and `filepath.Join` keeps
  the result inside the directory. Exact `.` and `..` are handled, and `..foo` remains
  a local name. No traversal was found. The collision issue above is the real
  filename risk.
- **Permissions on existing files:** New directories are `0700` and new files `0600`,
  but `os.WriteFile` does not tighten permissions on a pre-existing file, and the
  `MkdirAll` error is ignored (`active_memory.go:40`). On a shared host, an existing
  looser file could remain readable.
- **Estimator mismatch and O(n²) rendering:** `Render` rebuilds the whole string for
  each record to estimate the budget (`active_memory.go:114-121`). The set is small,
  so this is not a practical performance problem, but the estimate is not the
  provider's token count.
- **Cache-read variance:** The provider reported 7,248 cache-read tokens on active
  turn 2 and 0 on most other turns. This may reflect prefix caching behavior, but the
  benchmark does not record it in the JSON and there are not enough samples to
  interpret it.
- **Model attribution:** The benchmark note says "Nous Research"; the logs only show
  the model identifier. This review does not rely on the provider attribution.

---

## 7. Recommended next experiment

### Priority: B - reduce duplication and prompt overhead, then re-measure

**Why B, not A, C, or D:**

- The active-memory block is about 68 estimated tokens, under 1% of a 7,375-token
  prompt. Tool definitions (~4,240 estimated tokens) and session history (~2,409
  estimated tokens) dominate. Adding structured task state before reducing this
  overhead would add complexity to a prompt where the new state would be hard to see.
- The current benchmark cannot distinguish memory policies because every condition
  already had the fact in history. A structured task-state experiment (A) would inherit
  the same measurement problem.
- Selection and eviction (C) cannot be evaluated with one record and a contaminated
  history. There is nothing meaningful to select or evict in the current data.
- Large-volume testing (D) is premature: retrieval took 11-19 ms and returned 0-1
  records. The bottleneck is not knowledge volume; it is prompt composition and
  measurement validity.

B is the smallest change that can produce an interpretable result. It also directly
tests the observed problem: active memory added tokens without removing the duplicate
copy already in history.

### Prerequisite: fix the same-workspace project leak

Before running another memory experiment, fix the isolation defect:

- Reject or filter previous records whose `ProjectID` does not match the current
  project, or whose tags do not contain the current `project:<id>` tag.
- Add a test that uses the **same workspace and session key** with two different
  projects, clears history, and asserts that the second prompt does not contain the
  first project's record.

This is a correctness fix, not a feature. It should be a separate small commit.

### Minimal measurable experiment

1. **Fresh state per run.** Use a new temporary workspace, a new home, and a new
   session key for every trial. Assert that the session JSONL starts empty and that no
   sidecar exists before the first turn. Do not reuse `-s` names across trials.
2. **Hold the tool profile constant.** Enable `DEEPCLAW_CODING_PROFILE=1` in every
   condition, or run the 18-tool vs 4-tool comparison as a separate two-level factor.
   Record `tools_count` and the exact schema bytes. Do not compare an 18-tool baseline
   against a 4-tool treatment.
3. **Use a small, fixed fact set.** Seed exactly one relevant fact and several
   irrelevant facts. Use three turns:
   - turn 1: ask for the fact;
   - turn 2: ask a paraphrase that retrieval is likely to miss;
   - turn 3: ask a follow-up after an explicit compaction or after a restart.
4. **Run three conditions, three repetitions each:** eager `MEMORY.md`,
   retrieval-only MPM, and active memory + MPM. Keep model, temperature, `max_tokens`,
   and tool profile identical.
5. **Measure per component, per turn:** provider `prompt_tokens`, `completion_tokens`,
   `cache_read_tokens`, `cache_creation_tokens`; message count; system prompt chars;
   tool schema bytes and estimated tokens; history chars; active block chars and
   estimated tokens; retrieval record count, tokens, and latency; correctness; and
   whether the answer is supported by visible evidence.
6. **Report every trial, not only averages.** Include failures. Mark missing provider
   fields as missing, not zero.
7. **Success criterion:** active memory adds no more than ~50 prompt tokens beyond
   retrieval-only on a turn where retrieval returns nothing, and it improves recall on
   the paraphrase/compaction/restart turn relative to retrieval-only. If it does not,
   the cache-only design should be abandoned before adding structured task state.
8. **Only then consider A.** If B shows a measurable continuity gain, add a structured
   task-state schema with explicit evidence pointers, trust/verification status, and an
   invalidation policy. If B shows no gain, the hypothesis that a bounded working set
   helps is not supported by this mechanism.

### What not to do next

- Do not add more fields to the current cache and call it working memory without a
  clean measurement.
- Do not run a larger benchmark on contaminated sessions; it will reproduce the same
  saturation.
- Do not treat the eager `MEMORY.md` result as evidence that eager memory is better;
  its history was shorter and the fact was already present.
- Do not equate schema-byte reductions with provider-token reductions without
  measuring the provider usage.

---

## 8. Conclusion

Milestone 003 delivered a working, opt-in, session-scoped resident retrieval cache.
It preserves an MPM record across turns and restarts, deduplicates by ID, and keeps
MPM read-only. Those are real, if modest, achievements.

It did not deliver a working memory. The data model has no task state beyond the
latest user message and retrieved records. It cannot retain a decision, a verified
tool finding, a failing test, a next action, a correction history, a trust status, or
an invalidation decision. It is a cache with an objective string.

Benchmark 003 does not support the claim that active memory improves continuity,
recall, or token efficiency. The sessions were contaminated, the fact was already in
history, all conditions used 18 tools, and the only measured effect was a small
increase in prompt tokens. The one genuine isolation defect found in this review is
more important than the benchmark result: the same workspace can leak a previous
project's resident records into a new project.

The next step should be B: remove duplication, reduce prompt overhead, fix the
project-isolation defect, and establish a clean measurement. Only after that should
the project consider A, a structured evidence-backed task state. The current system
should be understood as a retrieval cache, not as working memory, until it can
demonstrate otherwise.

---

## Appendix: reproduction and evidence

### Committed tests at this commit

```text
go test -tags 'goolm,stdjson' -run 'ActiveMemory|DeepClawMPM|CodingProfile' \
  -count=1 ./pkg/agent/
ok github.com/sipeed/picoclaw/pkg/agent 0.644s
```

The three active-memory tests cover:

- cross-turn carry (`agent_active_memory_test.go:14-46`);
- restart from the sidecar (`agent_active_memory_test.go:48-84`);
- project isolation with **different workspaces**
  (`agent_active_memory_test.go:86-134`).

They do not cover same-workspace project switching, invalidation, duplication,
compaction, or prompt-injection content.

### Formatting check

```text
gofmt -l pkg/agent/active_memory.go pkg/agent/instance.go \
  pkg/agent/pipeline_setup.go pkg/agent/prompt_turn.go
pkg/agent/instance.go
pkg/agent/pipeline_setup.go
```

### Raw benchmark artifacts

- `.lab/bench/run_active.sh` - harness
- `.lab/bench/results_active.json` - per-turn parsed results
- `.lab/bench/raw_active/<condition>_turn<N>.log` - full debug logs
- `.lab/bench/raw_active/<condition>_turn<N>.log.ms` - end-to-end wall time
- `.lab/bench/deepclaw/home/.picoclaw/workspace/.deepclaw/active-memory/` -
  the sidecar left by the active run
- `.lab/bench/{no-memory,eager,deepclaw}/home/.picoclaw/workspace/sessions/` -
  the contaminated session histories

### Temporary isolation check

A temporary test, not committed, used the same workspace and session key with two
different MPM projects, cleared the conversation between projects, and inspected the
second prompt. The second prompt contained the first project's record:

```text
- (8907d1f855082be4) Project B secret code is beta-2.
- (2e659204481be02f) Project A secret code is alpha-1.
```

That result is the basis for the project-leak finding in sections 1.5 and 6.1.
