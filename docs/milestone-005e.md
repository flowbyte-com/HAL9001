# Milestone 005E: Pi-like shell inside an OS-enforced boundary

Status: **implemented and qualified offline. No live trials.**
Lexical deny patterns and path-token heuristics are bypassed **only**
when every child process runs under the verified Linux bubblewrap
backend. Everything else — mounts, PID namespace, env scrub, protected
MPM paths, in-process file-tool checks, timeouts, output limits, cwd
handling, fail-closed removal, unsafe-mode distinction, non-DeepClaw
defaults — is unchanged.

- Base: `dcf3c9c0` (005C report).
- Code: `1ac21b7` (`bench+agent: pi-like shell inside verified
  bubblewrap boundary`).
- Previous HEAD: `dcf3c9c0`, clean at work start; no live MPM, OpenCode,
  scheduler, release or baseline changes.
- Go: `go1.27.3`; bubblewrap 0.11.1; Linux.
- Qualification: full `go test ./pkg/...` (76 packages ok),
  `go build ./...`, `go vet ./pkg/...`, `gofmt` clean, fixtures 5/5,
  mock D run 10/10 ties (harness behavior unchanged).
- 004C memory, four-tool schemas, fixtures, scoring: untouched.

Design principle: **Pi-like unrestricted command syntax inside an
OS-enforced capability boundary. No pretend security from regexes.**

## 1. Architecture

Before: every shell command passed `guardCommand` — ~40 deny regexes
plus path-token scanning — *before* the bubblewrap backend confined
it. The pilot showed the cost: `go test ./...`, absolute in-workspace
paths, `/dev/null` redirects, and `GOCACHE=` overrides were refused by
string matching although the sandbox could confine them safely, while
`python3 os.chmod` bypassed the `chmod` pattern entirely.

After: `ExecTool` checks `isolation.BubblewrapEnforced()` — isolation
enabled **and** preflight clean for the current configuration **and**
Linux **and** `bwrap` present — and only then skips the built-in
deny-pattern list and the path-token scan. Operator-configured custom
allowlists still apply; every command still flows through
`isolation.Start` (preflight-cached, fail-closed: if bubblewrap
disappears mid-run the launch errors instead of running free).

The gate is environmental, not identity-based: it holds exactly when
OS confinement is active, in DeepClaw agent runs, bench trial
children, and any future verified-bubblewrap coding context — and
nowhere else. Default PicoClaw (isolation off), macOS/Windows (no
bubblewrap backend), unverifiable hosts, and the explicit
`DEEPCLAW_MPM_UNSANDBOXED=1` mode all keep the lexical guard.

## 2. Pre-removal boundary proof

Per the milestone's ordering rule, removal followed proof, not the
reverse. Before the change, the 003D suite passed unmodified
(`TestProtectedShellCannotModifyMPMStore`,
`TestProtectedShellScrubsMPMCapabilityEnv`,
`TestProtectedShellKeepsCodingWork`,
`TestProtectedShellAfterRestart`,
`TestWriteToolsRefuseProtectedDeepClawPaths`,
`TestDeepClawIsolationUnavailableFailsClosed`, plus the
positive-control `TestWithoutIsolationDemonstratesMPMWrite`): masked
MPM mounts, scrubbed capability env, working coding tools, fail-closed
removal. The new bypass tests then re-proved the same boundary *with
the guard bypassed* (section 4), so no protection depends on a
string match.

## 3. What changed

- `pkg/isolation/runtime.go`: new `BubblewrapEnforced()` predicate
  (27 lines). No changes to mounts, scrubbing, registry, or preflight.
- `pkg/tools/shell.go`: `guardCommand` skips deny patterns and the
  workspace path-token scan when `BubblewrapEnforced()` holds, with a
  debug log line; custom allowlists, timeouts, output limits, cwd
  resolution, and `isolation.Start` are untouched (~29 lines).
- Tests only, otherwise: `bubblewrap_enforced_test.go` (gate states),
  `shell_bypass_test.go` (behavior under bypass), bench audit pinning
  (process-global isolation state, no semantic change).

## 4. Verification

Ordinary coding now runs under verified isolation (all executed, none
guard-denied): `./check.sh`, `go test ./...`, `go vet ./...`,
quoted heredocs, `$(which go)`, backticks-free `export`,
workspace-absolute paths, `chmod 644`, `rm -f` (workspace),
`python3`, `go env`, `which go`.

Still blocked, now demonstrably by the OS rather than strings: MPM
store listing reads empty; sentinel reads fail; interpreter and
rewritten-path variants fail; outside-workspace writes fail;
`DEEPCLAW_MPM_*`/`MPM_*` absent from child env. Deny messages in
these cases are filesystem errors, never guard strings.

Fail-closed paths verified by existing tests (backend unavailable →
no shell; DeepClaw setup failure → exec removed) and unchanged.

Cache/tmp audit: default `GOCACHE` (sandboxed HOME) runs the suite;
workspace-absolute `GOCACHE=$PWD/.gocache` works; workspace tmp
round-trips; relative bare `GOCACHE=.gocache` is refused by Go itself
(upstream limitation, not ours); absolute out-of-workspace cache dirs
fail at the OS boundary.

## 5. Findings carried over from 005D (now explained)

- `./...` was refused by the path-token scan (`...` treated as
  outside), not by any deny pattern; `.` always worked. Bypass
  removes the distinction's cost.
- The `export`/`GOCACHE` saga needed no pattern: absolute paths
  triggered the path rule; bare `export` was always allowed (though
  useless — each call is a fresh shell).
- The control's combined heredoc was denied for the trailing `rm -f`
  (deny list wins over path rules), which is why splitting the
  command and using the `write` tool worked.
- Case-normalization hole (documented, **not fixed** per this
  milestone's no-regex-work rule): commands are lowercased before
  matching, so uppercase-only literals can never fire (`<<\s*EOF`,
  `DEL /F`, `RMDIR /S`). It survives only in the unverified fallback
  path. Recommended fix: case-insensitive literals — but the
  strategic direction is fewer regexes, not better ones.

## 6. Limitations and residual risks

1. The bypass trusts process-global isolation state; a bug that
   enables isolation without verification would widen execution
   silently. Mitigations: the gate requires preflight *success*, not
   just the enabled flag; `isolation.Start` re-verifies per launch
   and fails closed; the unsafe-mode path never verifies.
2. Reads outside the workspace that the mounts permit (e.g.,
   world-readable system files) are now allowed by design; MPM and
   credential paths remain masked/scrubbed regardless of content.
3. Hard links or pre-planted aliases to protected paths remain outside
   path-based protection (003D residual risk, unchanged).
4. Network namespaces remain optional; no remote writable MPM service
   is used by the evaluation.
5. Mock bench behavior is byte-identical (10/10 ties, same requests);
   no fixture, schema, or scoring change was needed or made.

## 7. Verification record

- New: `TestBubblewrapEnforced{DefaultsOff,RequiresEnabled,
  RequiresCleanPreflight,WhenVerified}` — pass.
- New: `TestBypassMode{AllowsOrdinaryCoding,KeepsOSEnforcement,
  GoCacheAndTmpAudit}` — pass (25s, real toolchain + bubblewrap).
- Unchanged and green: full `./pkg/...` suite (76 packages),
  `go build`, `go vet`, `gofmt`, fixtures 5/5, mock D 10/10 ties,
  003B–003E suites.
- Artifacts: `.lab/bench/005e/` (fixtures, mock run).

Bench harness required no changes: trial children already verify
isolation at preflight, so they automatically run Pi-like; the
005D audit tests pin *unverified* isolation and keep asserting the
legacy guard there.
