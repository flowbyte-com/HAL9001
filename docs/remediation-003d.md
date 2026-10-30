# Remediation 003D: MPM capability isolation

- **Baseline:** `a4758830` (Remediation 003C)
- **Date:** 2026-10-30
- **Scope:** capabilities reachable from DeepClaw coding-tool execution
  (`exec`/`bash`, interpreters, child processes) plus written file tools, MPM
  initialization failure policy, and MPM retrieval failure policy
- **Constraints observed:** disposable MPM workspaces under
  `/home/v/projects/deepclaw/.lab/` only; no live `~/.mpm`, scheduler, OpenCode or
  release-candidate changes; no PicoClaw baseline changes; no autonomous memory
  capture, trusted-lesson writes or D8 workarounds; no live-provider runs, remote
  pushes or deployments.

## Executive summary

At baseline `a4758830`, the DeepClaw coding profile exposed the ordinary `exec`
tool as `bash`. That shell ran as the agent's host user with the agent's full
environment, including `DEEPCLAW_MPM_EXECUTABLE`, `MPM_WORKSPACE` and
`DEEPCLAW_MPM_HOME`. The model could therefore call the MPM CLI with writable
actions, or open the MPM SQLite database through an interpreter. Read-only
behaviour was a property of the Go retriever, not of the execution environment.

This remediation makes the boundary structural on Linux:

- DeepClaw mode forces the existing bubblewrap subprocess backend on.
- The MPM workspace, MPM home and MPM executable are registered as protected
  host paths.
- The Linux backend appends masks after every other mount, so a protected
  directory becomes an empty `tmpfs` and a protected file becomes a read-only
  bind inside the shell sandbox.
- `DEEPCLAW_MPM_*` and `MPM_*` are removed from the environment of isolated
  children.
- The shell runs in a private PID namespace so `/proc/<host-pid>/root` is not a
  mount-namespace escape.
- The in-process `write_file`/`edit_file`/`append_file` tools refuse writes below
  protected paths with the same registry.
- If the host cannot enforce the boundary, `exec` is removed entirely (fail
  closed) rather than run unprotected. `DEEPCLAW_MPM_UNSANDBOXED=1` is an
  explicit, loudly logged, unsafe trusted-development exception that makes no
  read-only claim.
- MPM initialization failure in DeepClaw mode no longer falls back to eager
  workspace `MEMORY.md`; it records an explicit degraded state and runs without
  retrieved memory. Runtime retrieval failures degrade the turn instead of
  aborting it.

The trusted retriever is unchanged and remains read-only: it executes only the
`query` action, unsandboxed, with the configured MPM environment.

## Threat model and trust boundaries

### Actors and privileges

| Component | Runs as | Filesystem / environment | Trust |
| --- | --- | --- | --- |
| DeepClaw agent (Go process) | Host user | Full host access of that user | Trusted |
| MPM retrieval subprocess | Host user | Inherits agent env; explicitly overrides `MPM_WORKSPACE`, `HOME`, XDG dirs | Trusted, read-only |
| `bash`/`exec` subprocess | Host user, inside bubblewrap (DeepClaw mode) | Read-only system tree, writable instance root and cwd, MPM paths masked, MPM env removed | Untrusted (model-controlled) |
| Children of the shell | Inside the same bubblewrap namespaces | Inherit mounts, PID namespace and scrubbed env | Untrusted |
| `write_file`/`edit_file`/`append_file` | In-process Go tools | Host user filesystem | Untrusted input, in-process enforcement |
| MCP/plugin subprocesses | Operator-configured | Not changed by this remediation | Operator responsibility |

The protected configuration defends the **untrusted model** from reaching MPM
through the capabilities it is given. It does not defend against a **malicious
local user** who already controls the host: that user can read and write
`~/.mpm`, replace `bwrap`, change configuration, or run MPM directly. No
OS-level isolation is claimed against the host user itself, and the agent's Go
process remains trusted.

### Reachability paths at baseline

1. Literal CLI: `bash` → `mpm call mpm_memory --payload '{"action":"save",...}'`.
2. Interpreter: `python3` → `subprocess.run([mpm, "call", ...])`.
3. Direct storage: `python3`/`sqlite3` → open `mpm-workspace/src/db/mpm.db` and
   `INSERT`/`UPDATE`/DDL.
4. Environment: inherited `DEEPCLAW_MPM_*` / `MPM_*` name the exact executable,
   workspace and home even without guessing paths.
5. In-process file tools: `write_file`/`edit_file`/`append_file` with an absolute
   path outside the workspace in unrestricted mode.
6. `/proc/<host-pid>/root/...`: on hosts with permissive ptrace settings, a
   shared PID namespace could expose the host mount namespace through another
   process's root link.

## Reproduction evidence

All reproduction used disposable fixtures under
`/home/v/projects/deepclaw/.lab/tmp`. The live `~/.mpm` was never touched.

### Before remediation (`a4758830`)

A scratch checkout at `/tmp/opencode/dc-003d-before` (git archive of
`a4758830`) was used for baseline tests so the committed tree stayed clean. The
baseline shell test seeded a disposable workspace, then drove the ordinary
`ExecTool`:

```text
=== RUN   TestBaselineShellWritesDisposableMPM
    agent_003d_before_test.go:82: baseline interpreter created table in
      /home/v/projects/deepclaw/.lab/tmp/003d-baseline-.../mpm-workspace/src/db/mpm.db
      (rows=1)
--- PASS: TestBaselineShellWritesDisposableMPM
```

The same test first succeeded with a literal
`mpm call mpm_memory --payload '{"action":"save",...}'`, then ran a Python script
through the shell that created a `baseline_pwn` table and inserted a row
directly into `mpm.db`. Both were **demonstrated writes**, not mere attempts.

Baseline failure-policy behaviour was also reproduced:

```text
=== RUN   TestBaselineMPMInitFailureInjectsEagerMemory
--- PASS: TestBaselineMPMInitFailureInjectsEagerMemory
=== RUN   TestBaselineRetrievalFailureAbortsTurn
--- PASS: TestBaselineRetrievalFailureAbortsTurn
```

The first confirms that a missing MPM executable silently injected workspace
`MEMORY.md`; the second confirms that a runtime retrieval error aborted the
turn.

### After remediation

The committed capability suite includes the same escape with isolation disabled,
so the test itself proves the attack is real:

```text
--- PASS: TestWithoutIsolationDemonstratesMPMWrite
    direct CLI save is visible to a query, and the interpreter modifies the
    disposable MPM workspace/database
```

With DeepClaw protection active:

```text
--- PASS: TestProtectedShellCannotModifyMPMStore
    direct CLI invocation is denied or has no effect; the interpreter sees an
    empty protected directory (listing: []), no sqlite-ok; host workspace hash,
    sentinel and query results are unchanged
--- PASS: TestProtectedShellScrubsMPMCapabilityEnv
    `env` inside the sandbox contains no DEEPCLAW_MPM_* or MPM_* entries
--- PASS: TestProtectedShellKeepsCodingWork
    printf/cat/git still work and note.txt is written in the project
--- PASS: TestProtectedShellAfterRestart
    a fresh tool instance after registry re-establishment still cannot change
    the protected workspace or create an MPM record
```

## Architecture evaluation

### Option A — OS-isolated coding subprocesses (chosen)

Reuse the existing Linux bubblewrap backend, add protected-path masking, force
it on in DeepClaw mode, and fail closed when it cannot be verified.

Advantages:

- Real kernel-level mount and PID separation, not string matching.
- No new daemon, identity, package or upstream protocol.
- Preserves the full shell inside a writable project area.
- The same registry can deny in-process file-tool writes, closing the
  non-subprocess path.

Costs:

- Linux-only enforcement. macOS/Windows cannot enforce protected paths, so the
  protected configuration removes the shell there.
- Requires `bwrap` and usable user namespaces or setuid bubblewrap.
- Protected paths are path-based; aliases created outside the policy (for
  example a pre-existing hard link) are outside the check.

### Option B — separate MPM service identity (rejected)

A read-only query socket or separate uid would give a cleaner capability
boundary in principle, but requires a permanent daemon, packaging and lifecycle
work, plus a new protocol between the agent and MPM. That is a larger change
than the remediation needs, and the task explicitly asks to avoid unnecessary
daemons. The existing read-only retriever already provides the trusted query
path; the missing piece was constraining model-controlled execution.

### Option C — reuse an existing PicoClaw sandbox (partially adopted)

The existing bubblewrap backend already enforced a read-only system tree and a
writable instance root, but it did **not** exclude MPM paths or environment, and
isolation was opt-in. The remediation extends and reuses that backend rather
than inventing a second sandbox. The existing backend alone was therefore
insufficient.

### Rejected shortcuts

- **Project-directory location:** MPM under the project does not isolate it; the
  shell gets a writable cwd.
- **Database-only read-only bind:** the CLI, WAL/journal files, home config and
  caches remain writable, and alternate actions remain available.
- **Command deny patterns:** fragile, bypassable through interpreters, scripts,
  aliases and encodings. No new deny patterns were added.

## Implementation

### Protected-path registry (`pkg/isolation/runtime.go`)

- `ProtectPaths(paths...)` requires absolute paths, cleans them, deduplicates and
  also registers resolved symlink targets.
- `ProtectedPaths`, `ResetProtectedPaths` and `IsProtectedPath` expose the
  registry. `IsProtectedPath` resolves relative paths and symlinks and matches
  both exact paths and descendants.
- `SupportsProtectedPaths` verifies that the current platform can enforce the
  boundary (Linux + `bwrap`); other platforms return an explicit error.
- `Preflight` fails when protected paths are registered but the backend cannot
  enforce them.

### Linux sandbox mounts (`pkg/isolation/platform_linux.go`)

- `appendLinuxProtectedMountRules` appends rules after the executable, working
  directory and absolute-argument mounts:
  - directories → `MountRule{Target: path, Mode: "mask"}` rendered as
    `--tmpfs <path>`;
  - regular files → read-only bind;
  - symlink destinations are skipped (bwrap cannot mount over them); the
    resolved target registered by `ProtectPaths` is masked instead.
- `--unshare-pid` was added before `--proc /proc` so `/proc/<host-pid>/root` is
  not available as an escape on permissive-ptrace hosts. Verified manually:
  inside the sandbox `ls /proc | grep -c '^[0-9]'` drops from hundreds of host
  processes to a handful, and `/proc/<host-pid>/root` is inaccessible.

### Environment scrub (`pkg/isolation/runtime.go`)

`ApplyUserEnv` deletes every `DEEPCLAW_MPM_*` and `MPM_*` entry from isolated
child environments. This is capability removal, not secrecy: even if a path were
guessed, the named workspace and executable are masked.

### In-process file tools (`pkg/tools/fs/file_safety.go`)

`checkSensitiveWritePath` now consults `isolation.IsProtectedPath` for both the
absolute and resolved candidate. `write_file`, `edit_file` and `append_file`
therefore return `write denied: path is protected by DeepClaw MPM isolation`
without touching the target. Reads are intentionally not blocked by this check;
the shell sandbox masks the same paths entirely, and the trusted retriever needs
read access.

### DeepClaw wiring (`pkg/agent/mpm_isolation.go`, `pkg/agent/instance.go`)

- `deepClawProtectedPaths` derives the workspace, home and executable from the
  retriever configuration. Relative executables are resolved with `LookPath`,
  falling back to `filepath.Abs`.
- `configureDeepClawMPMIsolation` registers the paths, enables
  `cfg.Isolation.Enabled`, applies the config, and verifies
  `SupportsProtectedPaths` + `Preflight`.
- On success the agent records `MPMUnsandboxed=false`.
- On failure `removeExecForFailedIsolation` unregisters `exec` (and therefore the
  coding `bash` alias, which is created later from the surviving registry
  entries). No shell runs without a verified boundary.
- `DEEPCLAW_MPM_UNSANDBOXED=1` skips forced isolation, logs a warning, keeps the
  shell, and records `MPMUnsandboxed=true`. It is documented as unsafe and is
  not described as protected.

### Initialization and retrieval failure policy

**Initialization** (`DEEPCLAW_MPM_ENABLED=1`):

- Retriever creation failure sets `AgentInstance.MPMDegradedReason`, logs at
  error level, sets `contextBuilder.deepClawMode=true`, and therefore does not
  inject eager workspace `MEMORY.md`.
- The configured MPM paths from the environment are still registered as
  protected; if that cannot be enforced, `exec` is removed.
- If the retriever is created, normal DeepClaw mode applies.

**Runtime retrieval:**

- `SetupTurn` logs `MPM retrieval failed; continuing without retrieved context`
  and proceeds with no retrieved block. It does not abort the turn and does not
  fall back to eager memory.
- A later successful retrieval works normally in the same process.

Outside DeepClaw mode, lazy/off behavior is unchanged: `DEEPCLAW_MPM_ENABLED`
not equal to `1` still allows eager workspace `MEMORY.md`, and isolation remains
opt-in.

## Tests added

Isolation unit tests (`pkg/isolation/protected_paths_test.go`,
`pkg/isolation/protected_paths_linux_test.go`):

- `TestProtectPaths_RejectsRelativePath`
- `TestProtectPaths_DeduplicatesAndResolves`
- `TestApplyUserEnv_ScrubsMPMCapability`
- `TestAppendLinuxProtectedMountRules_MasksDirectoriesAndBindsFiles`
- `TestAppendLinuxProtectedMountRules_SkipsSymlinkDestination`
- `TestBuildLinuxBwrapArgs_UsesTmpfsAndPrivatePIDNamespace`

Capability tests (`pkg/tools/mpm_isolation_test.go`):

- `TestWithoutIsolationDemonstratesMPMWrite` — direct CLI and interpreter writes
  succeed when protection is off; this is the positive control.
- `TestProtectedShellCannotModifyMPMStore` — direct CLI, interpreter, subprocess
  and SQLite attempts cannot change the protected workspace or create records.
- `TestProtectedShellScrubsMPMCapabilityEnv` — no MPM env in the child.
- `TestProtectedShellKeepsCodingWork` — file writes, reads and Git still work.
- `TestProtectedShellAfterRestart` — boundary re-established with a fresh tool
  instance and the same protected paths.

File-tool test (`pkg/tools/fs/deepclaw_protection_test.go`):

- `TestWriteToolsRefuseProtectedDeepClawPaths` — write/edit/append are denied for
  a protected database, new files under a protected directory, and the
  protected executable; an ordinary workspace file remains writable.

Agent policy tests (`pkg/agent/agent_mpm_init_policy_test.go`):

- `TestMPMInitFailureDisablesEagerMemoryAndRecordsDegradedState`
- `TestMPMInvalidWorkspaceDisablesEagerMemory`
- `TestMPMDisabledStillAllowsEagerWorkspaceMemory`
- `TestMPMMalformedResponseDegradesTurnWithoutAborting`
- `TestMPMTransientRetrievalErrorDegradesThenRecovers`
- `TestDeepClawMPMForcesIsolationAndProtectsStore`
- `TestDeepClawIsolationUnavailableFailsClosed`
- `TestDeepClawUnsandboxedModeIsExplicit`

Updated e2e expectation (`pkg/agent/agent_mpm_e2e_test.go`):

- `TestProcessMessage_DeepClawMPMRetrievalFailureDegradesEndToEnd` replaces the
  old abort-on-retrieval-failure expectation with degraded continuation and no
  memory block.

`seedMPMFixture` now resets global isolation state in `t.Cleanup`, because the
registry and configuration are process-wide.

## Supported and unsupported host configurations

**Supported (protected):**

- Linux with `bubblewrap` (`bwrap`) installed and usable unprivileged user
  namespaces, or setuid bubblewrap. Verified here with bubblewrap 0.11.1.
- A filesystem layout where the MPM workspace, MPM home and MPM executable are
  addressable absolute paths (the normal `MPM_WORKSPACE`,
  `DEEPCLAW_MPM_HOME`, `DEEPCLAW_MPM_EXECUTABLE` setup).

**Not supported for protected mode:**

- macOS and Windows: the backend cannot enforce protected host paths. DeepClaw
  MPM mode therefore removes `exec`; retrieval remains available.
- Linux without `bwrap` or usable namespaces: same fail-closed removal.
- Container hosts where bubblewrap cannot create user/mount namespaces.

**Unsafe but explicit:**

- `DEEPCLAW_MPM_UNSANDBOXED=1` on any platform keeps the shell and does not claim
  read-only MPM.

The default configuration outside DeepClaw is unchanged: isolation stays opt-in
and no MPM paths are registered.

## Before/after assessment of read-only MPM end to end

For the protected configuration (Linux + bubblewrap, DeepClaw mode, no unsafe
flag):

- **Trusted retrieval path:** enforced. The Go retriever only invokes the MPM CLI
  with `action:"query"` and read-only projection fields.
- **Model-controlled shell/interpreters/children:** enforced. Protected
  directories are empty `tmpfs` mounts, protected files are read-only, the
  private PID namespace closes the `/proc` root route, and MPM environment is
  absent. Direct CLI, Python subprocess, `sqlite3` and arbitrary child process
  attempts were demonstrated ineffective.
- **In-process file tools:** enforced by the same protected-path registry.
- **Fail-closed behaviour:** if the boundary cannot be verified, the shell is
  removed rather than left writable.
- **Initialization failure:** no silent eager `MEMORY.md` fallback.

Read-only MPM is therefore **enforced end to end for model-controlled coding
capabilities on supported Linux hosts**. It is not enforced in
`DEEPCLAW_MPM_UNSANDBOXED=1` mode or on unsupported platforms (where the shell is
removed instead), and it is not a defense against a malicious host user or a
compromised agent process.

## Residual risks

1. **Trusted Go process.** The agent and retriever run as the host user with full
   ambient authority. A bug that lets model output drive Go file APIs outside the
   registered tools would bypass the boundary.
2. **MCP/plugin subprocesses.** Operator-configured MCP servers and plugins are
   not routed through `isolation.Start` and are outside this boundary. They are
   trusted operator extensions, not model-controlled shell children.
3. **Path-based protection.** `IsProtectedPath` and mount masks reason about
   paths. A pre-existing hard link to an MPM file at an unprotected path would
   still reference the same inode. The sandbox prevents creating new links to
   masked paths, but it cannot retroactively detect pre-planted aliases.
4. **Missing protected directory at startup.** `appendLinuxProtectedMountRules`
   skips paths that do not exist. If a configured MPM directory is absent at
   policy application time and is later created by a process outside the
   sandbox, a restart is needed before the mask covers it. In practice a missing
   workspace also prevents a working retriever.
5. **Alternate MPM installations/copies.** Only the configured workspace, home
   and executable are protected. A second MPM binary or database elsewhere on a
   writable path is not discovered automatically.
6. **Network.** Network namespaces remain optional; a remote writable MPM service
   would be a separate capability not covered here. No such service is used by
   this experiment.
7. **Configuration and restart persistence outside the sandbox.** The shell
   cannot modify the protected MPM store or executable, but it can modify
   ordinary project files. If the operator later executes project scripts or
   configuration outside the sandbox, those changes run with host authority.
8. **Kernel and bubblewrap trust.** The boundary relies on bubblewrap and Linux
   namespaces. A kernel or bubblewrap vulnerability, or a setuid-bwrap
   misconfiguration, is out of scope.
9. **Read access by the host user.** Protection is against model-controlled
   capabilities, not confidentiality from the local user; the host user can read
   MPM directly.
10. **Logging and telemetry.** Degraded/unsafe states are logged and recorded on
    `AgentInstance`, but there is no separate operator-facing dashboard.

## Verification

```text
go test -tags 'goolm,stdjson' -count=1 \
  ./pkg/isolation/ ./pkg/tools/... ./pkg/agent/ ./pkg/mpm/ ./pkg/session/...
go build -tags 'goolm,stdjson' ./...
go vet -tags 'goolm,stdjson' ./...
gofmt -l pkg
```

All packages above pass. Build and vet are clean, and `gofmt -l` returns no
files for the changed packages.

The 003B project-scoping suite and the 003C prompt-provenance suite remain in the
same `pkg/agent` run and pass unchanged.
