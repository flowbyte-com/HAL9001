# HAL9001 repository guide

`~/projects/hal` is the canonical local repository. Work on `main` for
integration; feature branches are merged or fast-forwarded into it only
after verification.

## Worktrees

Create feature worktrees inside the canonical repository:

```sh
cd ~/projects/hal
git worktree add .worktrees/<name> -b feature/<name> main
```

Use a separate worktree for each concurrent agent. `.worktrees/` is
ignored by Git. Remove a worktree with `git worktree remove` only after
its commits are integrated or preserved and its working tree is clean.
Removing a working directory never removes the branch ref.

## Integration and testing

Before pushing anything to `origin/main`:

1. Confirm `origin/main` is an ancestor of the candidate and fetch again
   immediately before the push. Never force-push.
2. Run `gofmt`, `go vet ./...`, `go vet -tags demo ./...`, `go build ./...`,
   `go test -count=1 ./...`, and the race tests for the changed packages.
3. Run the isolated PTY suites in `tools/ptycheck/` sequentially. Each
   suite must use its own temporary config, home, session store and
   workspace. Do not run competing PTY suites against the same fake
   provider port.
4. Push only a verified integration. Leave the canonical tree clean.

The detailed PTY commands and coverage are in
[`tools/ptycheck/README.md`](tools/ptycheck/README.md).

## Preservation

`legacy_base/` is the preserved DeepClaw/PicoClaw reference. Do not edit
it, move it or commit it into another directory. Do not use destructive
Git operations or overwrite an operator's running work.
