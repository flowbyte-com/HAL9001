# DeepClaw experiment notes

## Upstream provenance

- Community fork: https://github.com/afjcjsbx/picoclaw
- Original: https://github.com/sipeed/picoclaw
- Starting commit: ab8f28d6be86b197b16fad8ef5f1dd1583b73e19
- PicoClaw license: MIT. MPM license: AGPL-3.0. MPM is used only through the
  `mpm call` executable boundary in this experiment.

## Git remotes

- community: https://github.com/afjcjsbx/picoclaw
- upstream: https://github.com/sipeed/picoclaw
- Pristine baseline worktree: .lab/picoclaw-baseline

## Toolchain

- Go: 1.27.3
- Baseline tests: go test -tags stdjson -count=1 ./pkg/agent/ -> ok (85s)
- Full app build requires libolm headers for the goolm tag; not available in
  this environment, so baseline/full-app builds are blocked on that dependency.

## Experimental MPM

- Source: pinned commit 3df723156d7649517508212453de72d347759f43 under
  .lab/mpm-source
- Binary: .lab/bin/mpm (built with -tags fts5)
- Workspace: .lab/mpm-workspace
- Canonical DB: .lab/mpm-workspace/src/db/mpm.db

## DeepClaw mode

Enable with environment variables documented in pkg/mpm NewRetrieverFromEnv.

## First integration

- Package: pkg/mpm read-only adapter using mpm call mpm_memory.
- Retrieval is project-scoped by requiring a project:<id> tag.
- Results are truncated to an explicit token budget (default 350).
- Retrieved memory is injected once per turn as a non-stable prompt overlay.
- Workspace MEMORY.md eager injection and its write instruction are disabled in
  DeepClaw mode.
- MPM errors are returned as turn errors rather than silently falling back.

## Beam Free qualification

- Used Beam Free to inspect the repository, implement pkg/mpm, modify agent
  pipeline/context code, and run go test for pkg/mpm and pkg/agent.
- Evidence: both packages pass; adapter test covers project isolation and
  budget truncation.

## libolm investigation

- `go build ./...` without build tags fails in
  `maunium.net/go/mautrix/crypto/libolm` because
  `registerlibolm.go` is compiled under `!goolm` and includes `olm/olm.h`.
- The repository Makefile uses `-tags goolm,stdjson` by default, selecting
  `registergoolm.go` and avoiding the libolm header.
- No system packages were installed. Use `make build` / `make test` or pass
  `-tags 'goolm,stdjson'`.

## Benchmark methodology (prepared)

Compare three modes with the same provider/model and inputs:
1. No memory: MPM disabled and no MEMORY.md.
2. Eager MEMORY.md: MPM disabled with the fixture written to MEMORY.md.
3. DeepClaw: MPM enabled with project-scoped fixture.

Record:
- Task accuracy and memory recall against expected fixture values.
- Provider-reported prompt/completion/cache-read/cache-creation tokens.
- End-to-end latency, retrieval latency, LLM calls, tool calls.
- Injected memory size in bytes/tokens.
- Retrieval overhead and truncation behavior.

Distinguish measured provider usage from local character/token estimates. No
live-provider benchmark has been run in this checkpoint; the harness is limited
to deterministic mock-provider tests so far.
