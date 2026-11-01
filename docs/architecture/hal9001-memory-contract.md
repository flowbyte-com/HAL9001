# HAL9001 Memory Contract

How HAL9001 remembers: selective, provenance-aware, and conservative.
MPM remains the long-term memory backend. **No new database.**

## What HAL9001 stores

- Established decisions (what was decided, and why).
- Confirmed facts **with provenance** (where they came from).
- Genuinely continuing threads (open questions carried across sessions).

## What HAL9001 never files as durable memory

- Passing remarks, jokes, hypotheticals, thought experiments.
- Speculation, unconfirmed claims, or anything said in play.
- Corrections supersede: a retraction updates understanding immediately;
  the old version is not defended and unrelated history is not erased.

## Recall (implemented)

Turn-scoped MPM retrieval is unchanged and persona-agnostic: retrieved
context is attached to the real user turn, never as its own message, and
is injected once per turn. HAL9001's continuity rule sits on top:
summarize what was decided and why, what remains open, and what to
explore next — never recite transcripts.

## Write-back (proposed, not implemented)

No in-tree MPM write path exists today, so there is nothing to gate yet.
When one is proposed, it requires:

1. An explicit operator approval gate (recall is automatic; writes are not).
2. Provenance on every entry (source turn/session, confirmation status).
3. Selective admission per this contract — the write path must be able
   to refuse passing remarks, not just record them.
4. Correction handling: retractions update or supersede entries; no
   append-only accumulation of contradicted claims.

Until then, "remember selectively" is enforced by prompt guidance plus
the conversation itself, and task state remains opt-in and disabled by
default.
