# Remediation 003E: minimal coding tool contract

- **Baseline:** `66c4a017` (Remediation 003D)
- **Date:** 2026-10-30
- **Scope:** the opt-in four-tool coding profile, its provider-facing
  descriptions and schemas, registry/profile enforcement across the agent
  lifecycle, and integration with the 003B–003D memory and isolation policies
- **Constraints observed:** all fixtures and caches under
  `/home/v/projects/deepclaw`; no live MPM, scheduler, OpenCode or
  release-candidate changes; no PicoClaw baseline changes; no live-provider
  benchmark, push or deployment.

## Executive summary

At baseline the coding profile was a registry replacement. It exposed four
names (`read`, `write`, `edit`, `bash`) but delegated every description, schema
and execution path to unrelated general-purpose tools:

- `bash` still required `{"action":"run","command":...}` and advertised
  background sessions, PTY control, polling, stdin writes and process
  management.
- `write` and `bash` descriptions told the model to use `append_file`,
  `edit_file`, `list_dir`, `search_files`, `read_file` and `write_file`, none of
  which existed in the profile.
- The replacement registry discarded the agent allowlist, so late MCP/plugin
  tools could become visible.
- The profile still contained 5,644 bytes of schema, most of it from the full
  `exec` surface.

This remediation replaces the alias layer with a coherent contract:

- `bash` accepts `{"command":"..."}` with optional `cwd` and `timeout`, forwards
  only those fields to the existing `ExecTool`, and therefore keeps every 003D
  isolation, guard, timeout and output-limit path. It exposes no session, PTY,
  polling or stdin operations.
- `read`, `write` and `edit` have concise descriptions that name only tools in
  the profile. The write tool's overwrite guidance names `edit`.
- The registry keeps all original tools and its allowlist, then applies an
  explicit visibility profile. Only four tools are callable and
  provider-visible; late registrations remain installed but hidden.
- Stricter per-agent allowlists still apply. Allowlist entries may use either
  runtime names (`read_file`, `exec`) or profile names (`read`, `bash`).
- The four-tool schema is now 2,637 bytes (966 estimated tokens) versus 16,938
  bytes (6,353 estimated tokens) for the default 18-tool configuration and 5,644
  bytes for the previous alias profile.

No provider usage data was collected; token figures are the project's existing
deterministic estimate, not billed-provider measurements.

## Previous defects and baseline evidence

A clean checkout at `.lab/003e-baseline-src` (`git archive 66c4a017`) was used
for reproduction. Temporary baseline tests lived only there; committed tests
cover the corrected behaviour.

| Defect | Baseline reproduction | Result |
| --- | --- | --- |
| `bash` rejects a natural call | `{"command":"printf baseline"}` → `action is required` | reproduced |
| `bash` exposes unnecessary surface | schema required `action` and had `sessionId`, `background`, `pty`, `keys`, `data` | reproduced |
| Descriptions name absent tools | `write` description says “use append_file or edit_file” | reproduced |
| Allowlist is not enforced after the swap | allowlist `[read_file, write_file]` accepted late `web_search`; visible names became `[read web_search write]` | reproduced |
| Late registration becomes visible | `Register(extra_tool)` appeared in `Get` and `ToProviderDefs` | reproduced |
| Schema overhead remains high | old profile: `bash 3468`, `edit 708`, `read 455`, `write 1008`, total `5644` bytes | reproduced |

The baseline `bash` description also directed the model to `list_dir`,
`search_files`, `read_file`, `edit_file`, `append_file` and `write_file`, and
advertised `action=list/poll/wait/read/write/kill/send-keys`.

## Contract changes

### `bash`

```json
{
  "command": "go test ./...",
  "cwd": "optional/workspace/subdir",
  "timeout": 120
}
```

- `command` is required and non-empty.
- `cwd` is optional. It is validated by the existing `ExecTool` workspace/path
  guard.
- `timeout` is optional, in seconds; `0` disables the timeout. Integer values
  are normalized to the execution tool's expected representation.
- The adapter forwards `action:"run"` plus `command`, `cwd` and `timeout` to the
  existing `exec` tool. It does not call `os/exec`, does not construct a shell,
  and cannot select a session action.
- Background, PTY, polling, stdin, key injection and process management are
  absent from the schema and cannot be reached through the adapter.
- The description explicitly says the simpler interface does not make arbitrary
  commands safe.

### `read`

The adapter delegates to the configured reader (byte mode or line mode) and
uses a description matching its actual parameters:

- byte mode: `offset` and `length`;
- line mode: `start_line` and `max_lines`.

The read tool reports truncation using `read` rather than the hidden
`read_file` name. File-size limits and output limits are inherited unchanged.

### `write`

- `path` and `content` are required; `overwrite` remains optional and defaults
  to `false`.
- Replacing an existing file still requires `overwrite=true`.
- Guidance and errors name `edit`; `append_file` and `edit_file` are never
  mentioned.
- The protected-path denial from 003D still applies before any write.
- If `edit` is unavailable (for example denied by the agent allowlist), the
  guidance does not mention it.

### `edit`

- `path`, `old_text` and `new_text` are required.
- The description states that `old_text` must match exactly and uniquely.
- Protected-path denial and workspace restrictions are unchanged.

## Profile enforcement across the lifecycle

`ToolRegistry` now has an explicit visibility profile:

- `SetVisibleTools(names)` sets the provider-visible/callable set; `nil` clears
  it and an empty non-nil slice hides everything.
- `List`, `Count`, `Get`, `GetDefinitions`, `ToProviderDefs`, `GetSummaries`
  and clones respect the profile.
- `RegisterProfileTool` installs an adapter derived from an underlying tool that
  already passed the allowlist. It does not widen the allowlist.
- Late `Register`/`RegisterHidden` calls store tools but cannot overwrite a
  profile-visible name; `RegisterUnique` remains duplicate-safe.
- `Clone` preserves the profile, so a turn/subagent clone cannot silently widen
  it.

`applyCodingProfile` runs after shared tools and MCP/plugin-independent setup:

1. It reads the already-allowlisted originals from the agent's existing
   registry.
2. It wraps `read_file`, `write_file`, `edit_file` and `exec` (only those
   present) as `read`, `write`, `edit`, `bash`.
3. It sets the visibility profile to exactly the adapters it created.

Consequences:

- `[read_file, write_file]` yields `[read, write]`.
- `[read, bash]` and `[read_file, exec]` both yield `[read, bash]`.
- `[]` yields no tools.
- If 003D removed `exec` because isolation could not be verified, `bash` is not
  created and the visible set contains only `read`/`write`/`edit`.
- MCP tools registered later, plugin tools registered later, and discovery
  tools remain installed but are neither callable through `Get` nor present in
  provider definitions or summaries.
- MCP server prompt contributors are skipped while a visibility profile is
  active. Plugin resource/skill prompts are skipped because they would refer to
  a resource tool the model cannot see.

Tool discovery and prompt contributors are also filtered through
`PromptBuildRequest.ToolsVisibilityRestricted`: when the profile is active,
prompt text may not mention `read_file`, MCP tools, or discovery tools outside
the four names. The skill catalog points at `read` when it is available.

Additional tools become available only by disabling `DEEPCLAW_CODING_PROFILE`.
The profile deliberately has no silent extension mechanism; the default
provider-visible set is always the four tools.

## Security analysis

The tool-contract change did not weaken 003D:

- `bash` execution still enters `ExecTool.executeRun`, which calls
  `isolation.Start`; the adapter has no direct process-launch path.
- Protected MPM workspace/home masks, read-only executable bindings, environment
  scrubbing and the private PID namespace are unchanged.
- In-process `write`/`edit` still pass through the protected-path checks.
- When protected-path enforcement is unavailable, 003D removes `exec`; the
  profile then has no `bash`. It does not recreate the shell.
- `DEEPCLAW_MPM_UNSANDBOXED=1` remains explicit, logged, and marked on
  `AgentInstance.MPMUnsandboxed`.
- The per-agent allowlist, MCP server allowlist and workspace restrictions still
  apply independently of the profile.

The profile is a tool-interface policy, not a sandbox. The description says so,
and the execution path continues to rely on the OS isolation and existing
guards.

## Tests

Committed tests:

- `pkg/agent/agent_coding_profile_test.go`
  - `TestAgentCodingProfileExposesOnlyCoreTools`
  - `TestCodingBashAcceptsCommandOnly`
  - `TestCodingBashSchemaHasNoSessionSurface`
  - `TestCodingProfileDescriptionsAndSchemasReferenceOnlyVisibleTools`
  - `TestCodingProfilePreservesAgentAllowlist`
  - `TestCodingProfileAllowlistAcceptsProfileNames`
  - `TestCodingWriteGuidanceUsesVisibleEdit`
  - `TestCodingProfileHidesLateRegistrations`
  - `TestCodingProfileToolOperations`
  - `TestCodingReadTruncationNamesVisibleTool`
- `pkg/agent/agent_coding_mpm_test.go`
  - `TestCodingProfileBashUsesMPMIsolation`
  - `TestCodingProfileFailClosedWithoutIsolationBackend`
  - `TestCodingProfileUnsafeModeRemainsExplicit`
  - `TestCodingProfileProtectedWithoutUnsafeFlag`
- `pkg/agent/agent_coding_profile_schema_test.go`
  - `TestCodingProfileSchemaMeasurement`
- `pkg/tools/registry_visibility_test.go`
  - `TestVisibilityProfileFiltersVisibleAndCallableTools`
  - `TestVisibilityProfileBlocksLateOverwriteButStoresHiddenTools`
- `pkg/tools/mpm_isolation_test.go`
  - `TestCodingBashRoutesThroughProtectedExecution`

`TestCodingProfileToolOperations` is a deterministic end-to-end exercise of
read, write, edit, bash execution, cwd handling, edit-miss errors, overwrite
denial, command failure and timeout. No live provider is involved.

## Measurements

Measured with `json.Marshal(registry.ToProviderDefs())` in
`TestCodingProfileSchemaMeasurement`. Full schemas are recorded in
[`remediation-003e-tool-schemas.json`](remediation-003e-tool-schemas.json).

### Default configuration (18 tools)

| Tool | Bytes |
| --- | ---: |
| append_file | 483 |
| edit_file | 713 |
| exec | 3468 |
| find_skills | 596 |
| install_skill | 749 |
| list_dir | 223 |
| load_image | 442 |
| message | 639 |
| reaction | 495 |
| read_file | 460 |
| search_files | 2116 |
| send_file | 431 |
| spawn | 674 |
| subagent | 490 |
| todo | 2963 |
| web_fetch | 398 |
| web_search | 585 |
| write_file | 1013 |
| **Total** | **16938** |

Estimated tokens (project tokenizer): **6353**.

### Previous alias profile (4 tools, baseline)

| Tool | Bytes |
| --- | ---: |
| bash | 3468 |
| edit | 708 |
| read | 455 |
| write | 1008 |
| **Total** | **5644** |

### Corrected coding profile (4 tools)

| Tool | Bytes |
| --- | ---: |
| bash | 745 |
| edit | 676 |
| read | 463 |
| write | 753 |
| **Total** | **2637** |

Estimated tokens (project tokenizer): **966**.

Compared with the default configuration, the corrected profile serializes 84.4%
fewer bytes and estimates 84.8% fewer tokens. Compared with the previous alias
profile, it serializes 53.3% fewer bytes. These are schema-size figures, not
provider billing or usage measurements.

## Remaining limitations

1. The profile is enabled process-wide through `DEEPCLAW_CODING_PROFILE=1`, not
   per agent or per turn. It applies consistently to every agent created by the
   process while preserving each agent's own allowlist.
2. There is no profile extension configuration. An operator who needs MCP,
   plugin or other tools in the same process must disable the coding profile.
3. Hidden originals remain installed in memory. They are not visible, callable
   through `Get`, or present in provider definitions, but they are still
   registered.
4. Deferred MCP discovery cannot surface tools while the profile is active. This
   is intentional but means discovery-oriented deployments need the profile off.
5. Allowlists name runtime tools (`read_file`, `write_file`, `edit_file`,
   `exec`) or profile tools (`read`, `write`, `edit`, `bash`). Mixing the two is
   supported; arbitrary aliases are not.
6. Registry visibility is a policy layer, not an OS boundary. The 003D
   isolation remains the executable boundary and retains the residual risks
   documented there (trusted Go process, MCP/plugin subprocesses, path aliases,
   host-user authority).
7. Schema bytes and token estimates are deterministic project measurements;
   provider-specific tokenization and caching were not measured because no
   live-provider benchmark was permitted.

## Verification

```text
go test -tags 'goolm,stdjson' -count=1 \
  ./pkg/isolation/ ./pkg/tools/... ./pkg/agent/ ./pkg/mpm/ ./pkg/session/...
go build -tags 'goolm,stdjson' ./...
go vet -tags 'goolm,stdjson' ./pkg/isolation/ ./pkg/tools/... ./pkg/agent/ ./pkg/mpm/ ./pkg/session/...
gofmt -l pkg/agent pkg/isolation pkg/tools pkg/mpm pkg/session
```

The 003B project-isolation and 003C prompt-provenance suites were also run with
`DEEPCLAW_CODING_PROFILE=1` and pass unchanged. `DEEPCLAW_CODING_PROFILE=1`
together with `DEEPCLAW_MPM_ENABLED=1` is covered by
`TestCodingProfileBashUsesMPMIsolation` and the fail-closed/unsafe tests.
