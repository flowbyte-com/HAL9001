# Migration Inventory

Components worth migrating from `legacy_base/` into the native HAL9001
module, with current location, destination, dependencies, test coverage,
and recommended priority. Bulk copying is forbidden: each item is
extracted narrowly, adapted to HAL's new contracts, and tested.

Priority order is dependency order: identity and 444 first (already
seeded), then review, then Prompt Workshop, then continuity, then MPM.

| # | Component | Legacy location | Destination | Depends on | Legacy tests | Priority |
|---|-----------|-----------------|-------------|------------|--------------|----------|
| 1 | HAL identity + conversational contract | `pkg/agent/hal9001.go` (`Hal9001Identity`), `docs/design/hal9001-vision.md` | `internal/persona` (seeded) | — | `pkg/agent/hal9001_test.go` | Done (seed) |
| 2 | 444 allowlist + execution gate | `pkg/agent/hal9001.go` (`Hal9001ToolAllowed`), `pkg/agent/pipeline_execute.go` (gate) | `internal/capability` + `internal/engine` (seeded) | 1 | `pkg/agent/hal9001_stage3*.go` | Done (seed) |
| 3 | Dave Protocol + safety-signal detection | `pkg/agent/hal9001.go` (`IsSafetyRefusal`, `DaveRefusal`) | `internal/persona` (seeded) | 1 | `pkg/agent/hal9001_test.go` | Done (seed) |
| 4 | Prompt Workshop (brief Build/Validate) | `pkg/promptwork/promptwork.go` (`Brief`, `Build`, `Validate`) | new `internal/promptwork` | 1 | `pkg/promptwork/promptwork_test.go` | Next |
| 5 | Report assessment (verified/supported/claimed/contradictions/missing) | `pkg/promptwork/promptwork.go` (`AssessReport`), tool adapters in `pkg/tools/prompt_workshop.go` | `internal/promptwork` + engine review path | 4 | `pkg/promptwork/promptwork_test.go`, `pkg/agent/hal9001_stage3*.go` | After 4 |
| 6 | Review-report tool wiring | `pkg/tools/prompt_workshop.go` (`ReviewReportTool`), `pkg/agent/instance.go` registration | engine capability executors | 2, 5 | `pkg/tools/prompt_workshop_test.go` | After 5 |
| 7 | Thought-continuity tiers + admission rules | `docs/architecture/hal9001-memory-contract.md` | `internal/memory` (seeded) | — | (contract doc; new store tests) | Done (seed) |
| 8 | Session continuity + context assembly | `pkg/agent/context.go` (`BuildMessagesFromPrompt`), `pkg/session/*` | `internal/session` (seeded) + system-prompt prepending in engine | 7 | `pkg/agent/*_test.go` (broad) | Done (store + assembly) |
| 9 | MPM project isolation + provenance | `pkg/mpm/*`, `docs/architecture/hal9001-memory-contract.md` | `internal/memory` Store impl | 7 | `pkg/agent/agent_coding_mpm_test.go` (needs MPM binary) | Later |
| 10 | Provider abstractions + fallback routing | `pkg/providers/*`, `pkg/routing/*` | `internal/provider` OpenAI-compatible streaming client (native, not ported) | — | `internal/provider/openai_test.go` (httptest SSE, errors, cancel, reasoning split) | Done (native) |
| 11 | Terminal branding (compact HAL heading, labels) | `cmd/picoclaw/internal/cliui/brand.go`, `banner.go` | `internal/tui` theme (seeded) | 1 | `cmd/picoclaw/internal/cliui/brand_test.go` | Done (seed) |
| 12 | Greeting personalization | `pkg/commands/cmd_start.go` (`startGreeting`) | engine/TUI onboarding text | 1 | `pkg/commands/cmd_start_test.go` | Later |

## What stays behind

- The general-purpose tool registry and all execution tools (`exec`,
  file writers/editors, `spawn`/`subagent`, `cron`): HAL has no
  implementation tools by design; nothing here migrates.
- Coding profiles and turn-profile machinery: superseded by the
  unconditional allowlist.
- Channel integrations (Telegram, Discord, Matrix, …), gateway, cron,
  auth, plugins, MCP client: out of scope for the native foundation.
- Provider ecosystem beyond the contract: reimplemented per-adapter in
  the provider milestone, not ported.

## Special care items

- **Provisional ideas vs decisions**: the new architecture must preserve
  HAL's ability to develop ideas without confusing speculation, retrieved
  information, and verified facts. The `internal/memory` tiers encode
  this; the future assembler must keep tier labels attached to recalled
  records all the way into the prompt.
- **MPM trust boundaries**: recall stays automatic, writes stay gated.
  The legacy `mpm.NewRetrieverFromEnv` pattern (read-only by default) is
  the model for future integration.
- **Dave line exactness**: `internal/persona.Refuse("")` pins the exact
  string; any future refactoring must keep the byte-identical test.
