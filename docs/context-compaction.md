# Manual context compaction

A conversation that outgrows its model's context window is trimmed: the
provider drops the oldest exchanges until the request fits. The model
then has no idea what was said before. Compaction replaces the dropped
part with a summary, so the work continues with the decisions and the
facts still in view.

This document is the contract for that feature. It is written so that
another agent working on the interface can align with it without reading
the implementation.

## What compaction is

`/compact` is an operator action. The operator asks for it, and nothing
else does:

* there is **no automatic compaction** — a conversation that grows is the
  operator's business, and HAL never decides on its own to summarise what
  was said;
* there is **no provider call at startup** — the compaction exists only
  because the operator ran the command;
* a compaction that is cancelled, that fails or that does not converge
  **changes nothing**: the record is written at the very end, and the
  usable context is exactly what it was.

The command works on the conversation it is given. The two most recent
exchanges are left exactly as they were; everything older is replaced by
a summary.

## What compaction is not

* It is **not a second context-budget mechanism.** The budget is still
  resolved by `internal/provider` and applied by
  `internal/engine.applyContextBudget`, exactly as before. The compaction
  is bounded by that same budget; it never decides on its own how much
  room a request has.
* It is **not a second conversation store.** The transcript is never
  rewritten and never appended to. The record of a compaction lives
  beside the transcript, in a file of its own.
* It is **not a transcript.** The summary is what the model says the
  conversation said. It is labelled as such wherever it travels, and it
  is never presented as a verified record.

## The record

`session.Compaction` is the record of one compaction. It lives in
`<session>.compaction.json`, beside `<session>.jsonl`:

| Field | Meaning |
|---|---|
| `version` | the schema version; a record with any other version is ignored |
| `summary` | the compacted representation itself |
| `covered_messages` | how many leading messages of the transcript the summary replaces |
| `covered_exchanges` | how many exchanges that is |
| `first_turn_id`, `last_turn_id` | the provenance: the range of exchanges the summary covers |
| `before_tokens`, `after_tokens` | the estimated size of the context before and after |
| `limit` | the context budget the request had to fit into |
| `created` | when the compaction was made |

The transcript is append-only, so a count of leading messages names an
exact, stable part of it: the messages after the cut are the ones that
were kept.

The write is **atomic**: the record goes into a temporary file in the
same directory, the file is flushed, and it is renamed over the record.
A rename is atomic, so a restart never finds a half-written compaction —
it finds either the old record or the new one. A record that cannot be
read at all is treated as absent: the conversation then runs on its full
transcript, which is the safe direction, and the next compaction
replaces the file.

## How the request is assembled

`engine.requestMessages` takes the compaction and does two things with
it:

1. the **conversation** that travels is the part of the transcript after
   the cut. The exchanges the summary covers are not sent again;
2. the **summary** is composed into the system instruction, in its own
   clearly labelled section, before the evidence block.

The evidence block is built from the **whole** history either way. The
verified record of what really ran — the session ledger — is never
compacted away, and it is never replaced by a summary: it is a different
kind of statement and it travels differently.

The summary block says what it is:

> It is a summary, not a transcript and not a verified record. Treat every
> statement in it as reported rather than verified. The only verified
> record of what really ran is the session ledger, which is listed
> separately.

## How the summary is written

The summary is written by the model, in **bounded stages**. The material
is cut into pieces that fit the context budget, every piece is
summarised on its own, and the summaries are merged until one is left.
A conversation that does not fit in one request is therefore compacted in
several, and **every one of them fits the same budget the ordinary turns
obey**.

The stages are:

1. **Map** — the older exchanges are cut into groups that each fit one
   request, and each group is summarised on its own.
2. **Reduce** — the partial summaries are cut into groups the same way,
   each group is merged, and the merges are repeated until one summary
   is left. The merge is bounded: eight stages at most, and a round that
   does not make the summaries smaller ends the compaction with an
   error.

Two rules hold throughout:

* an **exchange is never split** — a question and its answer are
  summarised together or not at all;
* a **request never carries tools** — a compaction is a reading of the
  conversation, never an action on the machine. The 444/666 boundary is
  therefore in force by construction: a compaction can neither read a
  file nor write one.

The instruction asks for a fixed structure — objective, constraints,
decisions, verified facts, unresolved work, next actions — and it is
explicit about what a summary may not do: it may not add facts, and it
may not report a step that was started as one that finished. The
material is delimited with a fresh boundary on every request, so nothing
inside the conversation can forge the end of the block.

The output cap on the wire is the room the budget reserved, so a summary
can never be larger than the budget allows.

## The operator's view

The status line says what happened:

```
compacted — 6 earlier exchanges summarised, 2 kept · est. 36679 → 13492 tokens
```

The conversation shows a note with the same numbers, the budget, and
what the compaction means:

> Context compacted: the model now receives a summary of 6 earlier
> exchanges instead of the exchanges themselves. 2 recent exchange(s) were
> left as they are. The estimated context went from about 36679 to about
> 13492 tokens. The request itself is fitted to the 7168-token budget, so
> the older exchanges are now summarised rather than dropped. The
> conversation above is unchanged, and every message is still on disk.

The numbers are **estimates**. HAL does not know the provider's
tokenizer, and it never claims to.

The conversation the operator sees is not changed by a compaction: the
interface keeps showing every message, and the transcript on disk is
byte for byte the same.

## The safety properties

| Property | How it is kept |
|---|---|
| The transcript is never rewritten | the record is a separate file; the transcript is only read |
| A cancelled compaction stores nothing | the record is written at the very end, after the summary exists |
| An interrupted compaction leaves no trace | the atomic write leaves either the old record or the new one, never a partial one |
| The session file cannot be corrupted by an interruption | the compaction never writes to it |
| The active model never changes | the engine does not own the selection, and the compaction never calls the switch |
| The 444/666 authority stays in force | the compaction request carries no tools, and the permission mode is untouched |
| A consequential tool is never replayed | the compaction leaves the safe point of an interrupted exchange exactly where it was |
| Queued messages and steering keep their order | a compaction is not a turn: it sends no operator message and takes nothing from the queue |
| No provider call without the operator | the compaction runs only from the command |

## The commands

```
/compact
```

There is one. It takes no arguments: the operator either wants the
conversation compacted or does not. A command with arguments is refused
with the usage line.

The command is refused while HAL is working: two operations at once
would compete for the same provider and the same conversation. The
operator stops the turn and asks again.

## Measurements

A conversation of eight exchanges (about 96,000 characters) against a
model with no window of its own, so the provisional window of 8,192
tokens applies with 1,024 of them reserved for the answer — a budget of
7,168 tokens for the request:

| | |
|---|---|
| The estimated context before | about 36,679 tokens |
| The estimated context after | about 13,492 tokens |
| The compaction requests | 7, the largest about 3,340 tokens |
| The window the stub enforces | 8,192 tokens |

The compaction requests are far below the window because they carry no
tool definitions and no persona: only the instruction and the material.
The full request — persona, tools, evidence, summary and the recent
exchanges — is fitted to the budget by the ordinary trim, as it always
was.

The reduction is what the trim alone cannot give. Without a compaction
the older exchanges are **dropped**; with one they are **summarised**.

## Limitations

* **The output limit and the effort cannot be cleared.** A value of zero
  is "not named" in the configuration, so a field that can only be set
  cannot be unset. This is the configuration's semantics, not the
  compaction's.
* **One exchange that is too large to summarise is refused.** The
  exchange is never split, so a single exchange that does not fit the
  budget cannot be compacted. The command says so and changes nothing.
* **A second compaction re-summarises the first.** The summary stays a
  bounded size, at the cost of some fidelity in the older part.
* **The summary is the model's account of the conversation.** It is
  labelled as such, and the verified record of what ran travels
  separately and is never replaced by it.
* **The compaction's own requests are not conversation turns.** The
  tokens it cost are shown in the note that follows it, but they do not
  move the session's turn counters: they were spent on the
  conversation, not on an exchange of it.
