# Dependency Comparison

Module counts from `go list -m all` (November 2026).

| Tree | Modules |
|------|---------|
| `legacy_base/` (DeepClaw/PicoClaw) | 246 |
| HAL9001 native foundation (root) | 47 |

The legacy tree carries a full agent runtime: ~15 channel integrations,
a provider ecosystem, MCP/plugin systems, a web frontend backend,
cron, gateway, auth, TTS/STT, hardware tools, and their transitive
closures (notably the Matrix/olm CGO chain, which does not build in
this sandbox).

The native foundation's 47 modules are the Bubble Tea v2 TUI stack
(`charm.land/bubbletea/v2`, `charm.land/bubbles/v2`,
`charm.land/lipgloss/v2`, `charm.land/glamour/v2` plus Charm transitive
deps) and the Go
standard library. No implementation-capable tool is registered because
no tool packages exist at all: capability enforcement is a name
allowlist over operations the engine does not implement yet, rather
than a restriction layer over a general registry.

Full lists: `go list -m all` in the respective directories.
