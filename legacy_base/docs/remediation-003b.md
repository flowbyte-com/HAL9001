# Remediation 003B: project and session isolation

- **Baseline:** independent audit `0806be5a` (audit of milestones 001–003)
- **Date:** 2026-10-30
- **Scope:** DeepClaw active memory, JSONL conversation history, compaction
  summaries and seahorse history when the MPM project changes inside one agent
  workspace
- **Constraints observed:** no live MPM, scheduler, OpenCode or
  release-candidate changes; no PicoClaw baseline changes; no trusted-memory
  writes, task-state capture, new databases, reflection or unrelated tool
  changes; MPM remains read-only; no live-provider runs; no push.

## Executive summary

The audit's suspected cross-project disclosure was real and reproducible through
four independent paths:

1. active-memory sidecars were keyed only by session key and trusted their own
   `SessionKey` field;
2. the merge step re-adopted every previous resident record and could relabel
   the previous working set with the current project;
3. JSONL history and summaries were stored under the incoming session key, so a
   project switch in the same workspace read the previous project's
   conversation;
4. compaction state and seahorse rows used the same unscoped key.

The fix introduces one explicit effective session identity that includes the
MPM project. All persisted conversation state now lives under that key. Active
memory is bound to the project by path, by working-set `ProjectID` and by a
`project:<id>` tag on every resident record. Legacy unscoped state is
quarantined rather than relabelled. Project B's final LLM prompt no longer
contains Project A data in any tested path.

## Root cause

`NewAgentInstance` created a session store keyed by the incoming session key
alone (`sk_v1_<hash>`). MPM project identity was only used for retrieval
filtering. Therefore, in the same workspace:

- `agent.Sessions` read and wrote the same JSONL history and summary for any
  project that used the same session key;
- `ActiveMemoryStore.path()` used only the session key, and `Load()` accepted a
  sidecar whenever `ws.SessionKey == sessionKey`;
- `MergeActiveWorkingSet` copied the previous objective and every previous
  record, and set `ws.ProjectID = prev.ProjectID` when the current project was
  empty, with no per-record ownership check;
- the legacy context manager read `GetHistory`/`GetSummary` through the same
  unscoped store, and the seahorse context manager keyed its own SQLite
  history/summary by the same unscoped key.

MPM retrieval itself already filtered records by `project:<id>`. The disclosure
came from DeepClaw's own resident and persisted state, not from the MPM
retriever.

## Reproduction evidence

`pkg/agent/agent_project_isolation_test.go` drives real `processMessage` turns
with two disposable MPM workspaces (Project A fact `alpha-1`, Project B fact
`beta-2`), the same agent workspace and the same explicit session key
`sk_v1_003b_project_isolation` (an explicit key avoids the benchmark trap where
`-s` was ignored). A recording mock provider captures the final request
messages, and the assertions inspect the complete rendered prompt rather than
just sidecar or merge output.

Before the fix (same tests copied into a clean checkout of `0806be5a`):

```
--- FAIL: TestProjectSwitchClearedHistoryDoesNotLeakActiveMemory
    project B prompt leaked project A's alpha-1
--- FAIL: TestProjectSwitchRetainedHistoryDoesNotLeakConversation
    project B prompt leaked project A's alpha-1
--- FAIL: TestExistingActiveMemorySidecarWithWrongProjectIsRejected
    forged project A sidecar leaked alpha-1
--- FAIL: TestCompactionSummaryDoesNotCrossProjects
    project A summary leaked into project B
--- FAIL: TestSeahorseHistoryDoesNotCrossProjects
    seahorse history leaked project A into project B
--- FAIL: TestLegacyUnscopedSessionIsNotAdopted
    legacy unscoped history was adopted
--- FAIL: TestMergeActiveWorkingSetRejectsForeignProjectRecords
    project A merge kept foreign records (project-a, project-b and unscoped)
--- PASS: TestSameProjectSessionSurvivesRestart   (positive control)
```

After the fix all of them pass, and the positive control still passes.

## Session/project identity design

Effective persisted session key:

```
projectDigest = hex(sha256("deepclaw-project-scope-v1\0" + projectID))[:16]
scopeHash     = hex(sha256("deepclaw-session-scope-v1\0" + projectID + "\0" + baseSessionKey))
storageKey    = "sk_v1_dc1_" + projectDigest + "_" + scopeHash
```

Properties:

- The key includes the project, so history, summary, active-memory sidecar and
  seahorse rows separate automatically on a project switch in one workspace.
- The digest is embedded so "already scoped" is verifiable. A key that carries
  another project's digest is re-scoped into the current project namespace
  instead of being trusted; a caller cannot escape its project by imitating the
  prefix.
- Same-project calls are idempotent, so seahorse bootstrap, pruning and
  metadata lookups that already hold a scoped key do not double-scope.
- Routing, steering, outbound replies, event metadata and active-turn maps keep
  using the original base key. Only persisted conversation state is scoped.

Two transparent wrappers enforce the identity:

- `projectScopedSessionStore` translates every `session.SessionStore` call
  (history, summary, save, truncate, list), forwards metadata-aware calls, and
  forwards `DeleteSession`/`LastActivity`/`Close` so pruning and shutdown keep
  working.
- `projectScopedContextManager` translates `Assemble`, `Compact`, `Ingest` and
  `Clear`, and forwards `Close` to the underlying manager. This covers seahorse,
  which keys its own history/summary store independently of `agent.Sessions`.

`ListSessions` reports only keys belonging to the current project. Another
project's sessions and legacy unscoped sessions are not listed, bootstrapped,
adopted or pruned through this store.

### Active memory

- Sidecar path is derived from the project-scoped key.
- `Load` requires `ws.ProjectID` to equal the store's project and keeps only
  records whose tags include `project:<current-project>`.
- `Save` rejects a working set whose `ProjectID` does not match and filters
  foreign records before writing.
- `MergeActiveWorkingSet` uses a previous working set only when its `ProjectID`
  matches the current project; every record taken from either fresh retrieval
  or previous state must carry the current project tag. A matching session key
  alone is never sufficient.
- A load error is logged and treated as empty state; it no longer silently
  falls back to unvalidated data.

MPM retrieval keeps its own `project:<id>` tag filter, so the resident cache and
the retrieval path are independent safeguards.

## Files changed

| File | Change |
| --- | --- |
| `pkg/agent/session_scope.go` | new project-scoped session key, session-store wrapper, context-manager wrapper, project resolution for the loop |
| `pkg/agent/instance.go` | wraps the agent session store and builds a project-aware active-memory store when MPM is enabled |
| `pkg/agent/agent_init.go` | wraps the resolved context manager for DeepClaw |
| `pkg/agent/active_memory.go` | project-scoped sidecar path, validated load/save, project/tag-filtered merge |
| `pkg/agent/pipeline_setup.go` | warning and fresh state on active-memory load failure |
| `pkg/agent/agent_project_isolation_test.go` | regression tests and unit tests for the identity model |
| `docs/remediation-003b.md` | this report |

## Legacy-state handling

- Unscoped session keys (`sk_v1_<hash>`, legacy aliases, `agent:<id>:...`) are
  not read, listed or adopted. They remain on disk as quarantined data.
- Working sets that lack `ProjectID`, or whose records lack the matching
  `project:<id>` tag, are discarded on load and never merged.
- A sidecar is not trusted because `session_key` matches; `ProjectID` and every
  record tag must match the current project.
- Seahorse rows written under legacy unscoped keys are unreachable because the
  wrapper never issues those keys to the engine.
- No record is silently reassigned to the current project. Same-project state
  continues to load normally, including after restart.

## Validation

Commands (all from `/home/v/projects/deepclaw`, with the repository's `.lab`
Go caches):

```
go test -tags 'goolm,stdjson' -run \
  'TestProjectScoped|TestMergeActiveWorkingSet|TestProjectSwitch|TestExistingActiveMemory|TestSameProjectSession|TestCompactionSummary|TestLegacyUnscoped|TestSeahorseHistoryDoesNotCrossProjects' \
  -count=1 -v ./pkg/agent/

go test -tags 'goolm,stdjson' -run 'ActiveMemory|DeepClawMPM|CodingProfile' \
  -count=1 ./pkg/agent/

go test -tags 'goolm,stdjson' -count=1 ./pkg/agent/ ./pkg/mpm/ ./pkg/session/...
```

Results:

- 10 isolation tests pass, including both positive controls
  (`TestSameProjectSessionSurvivesRestart`,
  `TestCompactionSummaryDoesNotCrossProjects` same-project resume, and
  `TestSeahorseHistoryDoesNotCrossProjects` same-project resume).
- Existing `ActiveMemory|DeepClawMPM|CodingProfile` tests pass unchanged.
- Full `pkg/agent` suite passes (86s), as do `pkg/mpm`, `pkg/session` and
  `pkg/session/pruner`.
- `go build -tags 'goolm,stdjson' ./...` succeeds.

Coverage map:

| Requirement | Test |
| --- | --- |
| history cleared, sidecar remains | `TestProjectSwitchClearedHistoryDoesNotLeakActiveMemory` |
| history retained | `TestProjectSwitchRetainedHistoryDoesNotLeakConversation` |
| existing/forged sidecar | `TestExistingActiveMemorySidecarWithWrongProjectIsRejected` |
| restart and resume, same project | `TestSameProjectSessionSurvivesRestart` |
| JSONL compaction summary | `TestCompactionSummaryDoesNotCrossProjects` |
| seahorse history/summary store | `TestSeahorseHistoryDoesNotCrossProjects` |
| legacy unscoped state | `TestLegacyUnscopedSessionIsNotAdopted` |
| merge rejects foreign records | `TestMergeActiveWorkingSetRejectsForeignProjectRecords` |
| context-manager key translation | `TestProjectScopedContextManagerTranslatesEveryKey` |
| store isolation, idempotence, prefix forgery | `TestProjectScopedSessionStoreIsolatesAndIsIdempotent` |

## Remaining isolation limitations

- Isolation depends on a non-empty MPM `ProjectID`. With no project configured
  there is no project identity to bind to; the wrappers are not installed and
  the previous single-project behavior remains. DeepClaw production
  configuration sets the project.
- MPM configuration is process-global in the current design, so one agent loop
  supports one project. `deepClawProjectIDForLoop` takes the first agent with a
  retriever; a loop mixing agents from different projects is unsupported and
  should be split into separate processes.
- There is no automatic migration of legacy sessions or sidecars. Quarantined
  data must be inspected or removed manually if desired.
- Per-session auxiliary state keyed by the base key (todo tool state, steering
  queues, active-turn maps, outbound session metadata, aliases from old
  metadata) is not migrated or rewritten. It does not feed the conversation
  history, summaries or retrieved/resident memory, so it cannot re-introduce
  Project A content into Project B's prompt; todo state could still be displayed
  by the todo tool for the same base key.
- Workspace files outside session storage (workspace memory files, skills,
  prompt files) are shared by design when two projects share one agent
  workspace. This remediation does not partition them.
- Seahorse pruning runs against the shared SQLite database. In a shared
  workspace it may consider another project's rows as deletion candidates, but
  the wrapper only lists and bootstraps the current project's keys; pruning
  never exposes those rows to a prompt. Pruning is opt-in and disabled by
  default.
- The tests use a mock provider; no live-provider benchmark was run. Seahorse
  summary text is not generated in the tests; the seahorse test isolates the
  stored history and proves same-project resume, while key translation for
  `Compact`/`Assemble` covers the summary path.
- SHA-256 collision resistance is assumed for both the project digest and the
  session scope hash.
- Inbound session-key construction (including the `-s` handling investigated in
  the audit) is unchanged; this fix scopes whatever key routing produces.
