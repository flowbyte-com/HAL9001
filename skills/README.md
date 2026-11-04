# HAL9001 skills

The authoring source of the MPM-managed skills HAL9001 discovers at
runtime. The skills themselves live in **MPM's store**, not here: this
directory is where they are written, and MPM is where they are
authoritative.

## Installing

```sh
mpm save-skill --file skills/system-prompt-writer.md
mpm save-skill --file skills/humanize-text.md
```

`mpm save-skill` reads the `name` and `version` from the frontmatter.
Both are required by MPM. Saving a new version makes it the latest one;
HAL reads a skill **by name**, so it picks the new version up on the
next request without being rebuilt.

```sh
mpm list-skills           # what the store holds
mpm read-skill humanize-text
```

## The frontmatter

```yaml
---
name: humanize-text          # required, no ":" and no whitespace
version: 1.0.1               # required, semver
description: ...             # for humans reading the store
domain: writing              # optional
when_to_use: >-              # the activation guidance
  ... Triggers: "email", "letter", ...
constraints:                  # optional, the hard rules
  - ...
---
```

### The trigger phrases

`when_to_use` is the only activation field MPM returns when a skill is
read, so it carries the activation vocabulary. HAL reads the **quoted
phrases after the word `Triggers:`** as the explicit trigger list and
matches them as phrases. The rest of the prose contributes single words
with a lower weight, so a skill written without explicit triggers still
has a chance of being found — but one incidental word is never enough.

```yaml
when_to_use: >-
  Rewriting text that a person will read. Triggers: "email", "letter",
  "reply", "warmer".
```

A trigger only activates the skill inside a paragraph that also asks for
something to be written, and a later paragraph only counts when it is
short. A pasted document therefore cannot activate a skill by
mentioning the right words.

## The body

The body is the instruction HAL loads into the request that needs it.
Two things matter:

- **Keep it focused.** The whole body is loaded on every request the
  skill applies to, so it costs context each time. The two shipped
  skills are around 3 KB and 4 KB.
- **Never write permissions into it.** A skill shapes how HAL writes; it
  cannot widen what HAL may do. HAL composes a precedence line into the
  request that says so, and the application enforces it.

## Verifying a change

```sh
mpm save-skill --file skills/humanize-text.md
go test ./internal/skills/ ./internal/engine/
HAL9001_BINARY=/tmp/opencode/hal9001 python3 tools/ptycheck/checks.py
```

The live comparison harness shows what a skill changes in the answer:

```sh
HAL9001_DEMO=1 go test -tags demo -v -run TestLiveDemo ./internal/engine/
```
