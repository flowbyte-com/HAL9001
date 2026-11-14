# Installing and updating HAL9001

HAL9001 installs entirely in the operator's home directory. It needs no
root access, no system package, no MPM runtime and no published release
artifacts.

The managed installation is separate from HAL's data:

| Path | Purpose |
| --- | --- |
| `~/.local/bin/hal` | stable launcher, symlink through `current` |
| `~/.local/bin/hal-update` | stable updater, symlink through `current` |
| `~/.hal/current` | the one active pointer |
| `~/.hal/releases/<commit-sha>/` | immutable release (`bin/hal`, `bin/hal-update`, `manifest.json`) |
| `~/.hal/state/` | updater lock, previous-release record, preserved old commands, Go build cache |
| `~/.hal/staging/` | candidates under assembly; removed after every run |
| `~/.hal/data/` | HAL configuration, credentials, sessions, audit and usage (never touched) |
| `~/.hal/workspace/` | HAL's writable workspace (never touched) |

## Requirements

- Linux or another platform supported by the current HAL9001 module.
- Git, used to resolve and fetch an exact commit.
- Go 1.27.3 or newer, used to build both binaries.
- `~/.local/bin` on `PATH`.

Updates build from the official repository,
`https://github.com/flowbyte-com/HAL9001.git`, on the stable branch `main`.
There are no prebuilt release archives yet.

## First installation

From a clean HAL9001 checkout:

```sh
cd ~/projects/hal
go run ./cmd/hal-update --bootstrap
```

The bootstrap command:

1. requires a clean Git checkout and resolves its exact `HEAD` commit;
2. builds `hal` and `hal-update` in a staging directory under `~/.hal`;
3. writes and validates a manifest containing both binary hashes and the
   full commit;
4. installs the immutable release under `~/.hal/releases/<sha>`;
5. creates `~/.local/bin/hal` and `~/.local/bin/hal-update` as symlinks
   through `~/.hal/current`.

It does not start HAL, change `~/.hal/data`, change `~/.hal/workspace`, or write shell
configuration. A local installation already in progress is not needed.

If `~/.local/bin/hal` already exists and is not managed by HAL9001, the
command refuses rather than overwriting it. Move it aside yourself, or run:

```sh
go run ./cmd/hal-update --bootstrap --replace-existing
```

That preserves the old command under
`~/.hal/state/backups/legacy-<timestamp>-*/` after the new release has
built and validated, then installs the managed link. It never deletes the
old file.

## Launch and version

```sh
hal
hal --version
```

`hal --version` prints the product, version and full build commit. The
version is derived from the source revision and the commit timestamp; it is
not a hand-maintained constant.

Development use remains unchanged:

```sh
cd ~/projects/hal
go run ./cmd/hal9001
```

## User data layout and migration

HAL keeps all user-owned runtime data under one root:

| Path | Contents |
| --- | --- |
| `~/.hal/data/` | configuration (`config.json`, mode `0600`), credentials, sessions, audit (`audit.jsonl`), usage |
| `~/.hal/workspace/` | the workspace HAL may propose files in |

Previous releases used `~/.hal9001` for data and `~/HAL` for the
workspace. On startup HAL copies both trees into the new layout,
byte-for-byte with permissions intact, and records every file with its
SHA-256 digest in `~/.hal/state/home-migration.json`. The copy is
idempotent: rerunning it skips files that already match and changes
nothing else. If the same relative path holds different bytes on both
sides, migration refuses to start and names every conflicting path —
delete, move or reconcile the listed files, or point HAL at explicit
locations with `--store`, `--workspace` or `HAL9001_CONFIG`, then start
again.

The legacy directories are left in place after the copy. Remove them
only deliberately, once the new layout is verified, with
`HAL_CLEAN_LEGACY_HOME=1` set for that run: HAL re-verifies every
migrated file, then moves `~/.hal9001` and `~/HAL` into a timestamped
backup under `~/.hal/state/backups/home-migration-<timestamp>/`.

### Rollback across the migration

A previous release does not understand the new paths: it reads
`~/.hal9001` and `~/HAL` only. Rolling back therefore depends on where
the legacy directories are:

- Before cleanup, the legacy directories are untouched, so running the
  previous release just works. Anything written by the new release after
  migrating is not visible to the old one.
- After cleanup, restore the legacy layout from the backup first
  (copy `home-migration-<timestamp>/.hal9001` back to `~/.hal9001` and
  `home-migration-<timestamp>/HAL` back to `~/HAL`), then run the
  previous release. The backup is byte-identical to what was migrated
  and is removed only by an explicit, verified cleanup of its own.
  The supported way to restore it is `HAL_ROLLBACK_HOME_MIGRATION=1 hal`,
  which puts the legacy directories back and exits.

A fresh installation creates only `~/.hal/data` and `~/.hal/workspace`;
it never creates the legacy directories.

## Day-to-day maintenance

```sh
hal-update --status     # offline: release, previous candidate, health, ownership
hal-update --check      # resolves main; reports current, behind or diverged
hal-update              # build and activate the latest main commit
hal-update --rollback   # atomically restore the previous release
```

`--status` and `--check` make no installation changes.

An update is safe while HAL is running. The running process keeps its
loaded executable and its session; the new release becomes active on the
next `hal` launch. Updates never send signals to HAL, change permissions,
touch sessions, or restart anything.

### What an update does

1. Resolves `main` to a full commit SHA with `git ls-remote`.
2. Takes the single updater lock at `~/.hal/state/update.lock`.
3. Fetches that exact SHA into an isolated checkout under
   `~/.hal/staging/`; it never builds from a moving branch and never
   touches the operator's checkout.
4. Builds both binaries with `go build -trimpath -buildvcs=false` and
   stamps the commit, version and commit time.
5. Writes `manifest.json` with SHA-256 hashes, then validates the
   directory, both files, and both `--version` reports.
6. Renames the complete candidate to `~/.hal/releases/<sha>`. Releases are
   immutable and are never overwritten.
7. Atomically renames a new symlink over `~/.hal/current`, recording the
   previous target in `~/.hal/state/previous-release`.
8. Repairs the two stable command symlinks if necessary.

If any step before the pointer swap fails, the current release remains
active and the staging directory is removed. If a later step fails, the
previous pointer is restored automatically.

### Rollback

`--rollback` validates the recorded previous release and atomically points
`current` back at it. It does not fetch, build or use the network. A second
`--rollback` returns to the release you rolled back from, so the two
recorded releases can be toggled safely. Earlier release directories are
retained and are not garbage-collected.

## Recovery after an interrupted update

Run `hal-update --status` first. The status is read-only.

- **Status is healthy, previous release is valid:** run
  `hal-update --rollback`.
- **`current` is missing or broken:** use a release directly:
  `~/.hal/releases/<sha>/bin/hal-update --rollback`, or from a checkout:
  `go run ./cmd/hal-update --rollback`.
- **A release directory is incomplete:** it was never activated. Remove
  only that directory under `~/.hal/releases/` and retry; the active
  release is untouched.
- **An update was interrupted after installing a release but before
  activation:** the next `hal-update` validates and reuses that immutable
  release, then activates it.

The updater never deletes the release that `current` names.

## Uninstall

Check that the commands are the managed links before removing them:

```sh
readlink ~/.local/bin/hal
readlink ~/.local/bin/hal-update
rm -f ~/.local/bin/hal ~/.local/bin/hal-update
rm -rf ~/.hal
```

This deliberately leaves `~/.hal/data/` (configuration, credentials,
sessions, audit, usage) and `~/.hal/workspace/` (workspace files)
untouched. If a command was preserved under
`~/.hal/state/backups/`, restore it from a backup you kept before deleting
`~/.hal`.

## Advanced overrides

These exist for isolated testing and unusual home layouts:

| Variable | Default |
| --- | --- |
| `HAL9001_INSTALL_ROOT` | `~/.hal` |
| `HAL9001_LOCAL_BIN` | `~/.local/bin` |
| `HAL9001_UPDATE_REMOTE` | `https://github.com/flowbyte-com/HAL9001.git` |
| `HAL9001_UPDATE_BRANCH` | `main` |

Only point the remote at a repository you trust: an update executes the
source it fetches with your user's permissions.
