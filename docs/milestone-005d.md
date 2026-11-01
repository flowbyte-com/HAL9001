# Milestone 005D: coding-agent tool-use observability

Status: **implemented and qualified offline. No live calls, no production,
prompt, fixture, or policy changes.** Benchmark-only instrumentation plus
a disposable sandbox audit, both committed in `91544372`.

- Base: `dcf3c9c0` (005C report).
- Repair commit: `91544372`.
- Go: `go1.27.3`; sandbox: bubblewrap 0.11.1 (unchanged, unweakened).
- Qualification: full `go test ./pkg/...` (76 packages ok), `go build
  ./...`, `go vet ./pkg/...`, `gofmt` clean, fixtures 5/5.
- Evidence: `.lab/bench/005d/` (validation logs, fixture report).

## 1. Instrumentation design

`pkg/bench/tool_trace.go` (new) subscribes the trial process to three
trusted runtime events — `KindAgentToolExecStart` (final validated tool
name + arguments), `KindAgentToolExecEnd` (duration, outcome length,
error flag, async flag), `KindAgentToolExecSkipped` (hook denials) — with
locked, in-order delivery. Dispatch in the agent is strictly sequential
(one tool loop, no goroutines), so order-joins are exact without ID
correlation: the n-th Start of a turn pairs with the n-th End. Each
attempt gets a trial-unique ExecID, the bench turn, the agent turn, and
the latest provider request of that turn, and is appended as JSONL with
an `fsync` per line to `raw/tool-trace.jsonl`, shared across restart
segments (the second process reseeds ExecIDs from the file). Arguments
are sanitized at record time (500-rune truncation, existing
`RedactSecrets` patterns); outcome text is never stored — only a
content hash for equality checks. Recovery merges Start/End/Skipped by
ExecID; true duplicates (same ExecID + same event kind) are counted,
completions supersede reservations. The parent reconciles every trial
against the trace file, including stubs for crashed children.

`pkg/bench/trial.go` wires the subscriptions, stamps per-turn changed
files (`git status` before/after; `sessions/` runtime churn filtered),
and attaches a `ToolTrace` summary to every result, including fail
paths. Provider requests (intent) and trusted executions (observation)
stay separate series, joinable by turn order and request ID.

Nothing touches prompts or schemas: a 4-condition mock check asserts
identical tool-schema hashes across conditions, and raw requests contain
no trace markers.

## 2. What the historical 005B evidence can and cannot reconstruct

Can: every tool name and argument (from session transcripts + now from
traces), every outcome text, full request histories with usage, file
diffs, task-state snapshots, compaction/restart boundaries. The Phase 3
shipping analysis below is built exclusively from this material — no
intention was inferred that the transcripts do not state.

Cannot: the exact guard sub-check for `./...` (the deny is logged only
as "path outside working dir"; the token-level regex trace is not
recorded — see §7, recommendation 2), provider-side tool-call IDs (the
adapter drops them; order-join substitutes), and per-request token
attribution for pre-ledger runs (the aborted 005B pilot trial).

## 3. Shipping failure classification

All 25 treatment bash calls, replayed verbatim against the real guard:

| Bucket | Count | Examples |
|---|---|---|
| 1. Legitimate, conservatively rejected | 11 | `go test/build/vet/list ./...` (7 — `./...` is inside the workspace yet matches the outside heuristic); `cd <abs-workspace>` ×2; `/dev/null` redirect ×1; absolute-path `GOCACHE=` ×2 |
| 2. Invalid/malformed invocation | 0 | — |
| 3. Legitimately forbidden | 4 | `$(which go)`, `${PIPESTATUS}` ×2 (deliberate substitution ban); `ls` of the trial dir outside the workspace (correct confinement) |
| 4. Repeated without strategy change | ~10 | the `go test ./...` core retried with varying wrappers (cache theories, `export`, inline env) but never trying `./check.sh` or `go test .` |
| 5. Insufficient evidence | 1 | which sub-check matches `./...` (inferred path heuristic, not logged) |

Control, for contrast: blocked 4× (heredoc-to-/tmp: path-outside;
combined sweep command: `rm -f` dangerous-pattern win over the
simultaneous path issues; `chmod 644`: dangerous), succeeded via
`./check.sh`, dot-form `go test`, `git diff`, unflagged `rm`, and a
`python3 os.chmod` workaround. The divergence point is the first check
command: relative script (allowed) vs `go test ./...` (denied). Task
state is exonerated: all 29 entries are faithful `tool_reported`
observations, max prompt 5.7k of 32k context, and treatment used fewer
total tokens than control.

## 4. Sandbox audit: ordinary commands vs current policy

Disposable-repo tests against the real `ExecTool` guard (`restrict=true`):

Allowed: `./check.sh`, `go test .`, `go test -run T -v .`, `go version`,
`go env`, `ls`, `find`, `cat <rel>`, `mkdir`, `cat <abs-inside>`,
`which go`, plain `export FOO=bar` (no-op: each call is a fresh shell),
unquoted heredoc to a relative path. Denied with logged reasons:
`./...` forms (`go test/build/vet/list`, path-outside), absolute
out-of-workspace paths, `$(...)`/backticks/`${...}`, `rm -f`, `chmod
NNN`, `eval`, `source`, `curl|sh`, `git push`, traversal `../foo`.

Three usability findings (read-only; policy unchanged):

1. **`./...` is overbroad.** Every recursive Go invocation is refused
   although it stays inside the workspace; the dot-form works. Agents
   that default to `./...` (as both live agents did) lose their
   primary check loop to a message that reads like a sandbox escape.
2. **No usable cache override exists.** Absolute `GOCACHE=` is refused
   (correctly, as a path rule); bare `export` does not persist; the
   relative inline form (`GOCACHE=.gocache go test`) passes the guard
   but the toolchain then fails to set up. Hermetic Go execution has no
   working spelling under the current rules.
3. **Case-normalization hole in deny patterns.** Commands are lowercased
   before matching, but several literals are uppercase-only and can
   never match: `<<\s*EOF` (heredocs unblocked — verified), `del /F`,
   `rmdir /S`. The `rm -f`/`chmod`/`eval` family still matches. This is
   a guard-coverage gap in the *permissive* direction, found while
   proving the *restrictive* behavior.

Negative tests: reads/writes/listings of outside paths denied;
`isolation.ProtectPaths` covers descendants (unit-verified); MPM-path
masking itself remains covered by the 003D suite, referenced not
duplicated. No denial was weakened and none is proposed here.

## 5. Recommended coding-tool improvements

1. Distinguish `./...` (inside workspace) from genuine escapes in the
   path rule; keep denying `..` traversal. Removes the largest
   legitimate-operation block observed.
2. Log the matched rule (pattern id or path-check name) with each
   denial. Eliminates the §3 bucket-5 gap and makes future audits
   exact instead of inferential.
3. Provide a supported hermetic-cache spelling (e.g., allowlist a
   workspace-relative `GOCACHE` or set it in the sandbox env).
4. Fix case handling in deny literals (`(?i)` or lowercase literals).
5. Consider capability-level `chmod`/mode control: the current
   string match is bypassed by `python3 os.chmod` (observed in the
   control trial) while blocking legitimate numeric modes.

All are proposals; none implemented in this milestone.

## 6. New benchmark metrics (`pkg/bench/tool_metrics.go`, all tested)

- `classifyTraceOutcome`: guard_rejected (+kind: dangerous_pattern,
  path_outside, traversal, allowlist, escaped), hook_denied,
  tool_error/success via exit-code regexes (`exit=N`, `[Command exited
  with code N]`, `EXIT: N`), guard-before-exit precedence.
- `summarizeTraceRepetitions`: per-command totals, failures, guarded
  count, longest run; *pointless* iff identical command + identical
  outcome + identical failure with no intervening change, else
  *legitimate re-verification* with reason.
- `longestNoProgressRun`, `readsWithoutProgress` (steady-hash re-reads
  without intervening writes), `isCheckCommand` (check.sh/go
  test/pytest/npm/cargo), `ReverificationAfterRestart`,
  `TimeAndTokensToFirstEdit`.
- Explicit non-goals, enforced by design: no intent inference, no
  model-reasoning claims; repetition counts bushy-tail-tolerant
  (`?`-command collapse is gone — args now come from the trusted
  start event, not the adapter's ID-only records).

## 7. Regression results and remaining limitations

- Full suite: 76 packages ok; build, vet, gofmt clean; fixtures 5/5;
  capped mock D run 10/10 ties (unchanged behavior).
- New tests: trace e2e (joined attempts, names/args/request IDs,
  cross-segment ExecID continuity, schema parity, FilesChanged
  attribution), crash recovery (unknown retained, duplicates counted,
  corrupt lines skipped, missing file ok), sanitization (secrets,
  truncation, absent-file), metrics (table above), sandbox audit
  (allowed/denied matrix, env assignment, negative paths).
- Limitations: (a) no SIGKILL-mid-trial e2e — crash coverage rests on
  fsync-per-event plus recovery/fail-path tests; (b) per-request token
  attribution before the ledger era is unrecoverable (aborted 005B
  pilot); (c) guard sub-check identity for `./...` remains
  inferential until recommendation 2 lands; (d) `time/tokens to first
  edit` needs request-token maps the runner can supply but no live
  trial has yet exercised end-to-end (mock has no usage deltas worth
  measuring).

## Acceptance

A future benchmark can now distinguish "cannot solve" from "repeatedly
attempted legitimate operations the environment prevents": every
attempt carries its validated name and arguments, every outcome is
classified (guard kind, exit code, error), repetitions are counted
with a pointlessness rule, and all of it survives crashes with secrets
redacted and isolation untouched. Measure what the agent does — the
tracing, the audit, and the preserved 005B evidence now agree on what
it did.
