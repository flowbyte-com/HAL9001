# HAL9001 Memory Contract

How HAL9001 remembers: selective, provenance-aware, and conservative.
MPM remains the long-term memory backend. **No new database.**

## Thought-continuity categories

Durable memory distinguishes four tiers. Promotion is conservative: when
in doubt, material stays in the conversation, not in memory.

- **Conversation.** The live session itself. Default home for everything:
  remarks, jokes, hypotheticals, speculation, unconfirmed claims. Needs
  no provenance because it claims no durability.
- **Sparks.** Interesting but undeveloped: an observation, a half-formed
  idea, a question worth revisiting. Recorded only with its open status
  intact — never reworded as a conclusion. A spark that never develops
  stays a spark; sparks expire rather than promote by age.
- **Developing ideas.** Threads under active development across turns or
  sessions: what the idea currently is, what remains open, what was
  already considered and set aside. Updated in place as the idea moves;
  superseded versions are replaced, not accumulated.
- **Decisions.** Established outcomes only: what was decided, why, and on
  what basis. A developing idea promotes to a decision **only on explicit
  user confirmation or when it passes the durability test**: the user has
  acted on it, referenced it as settled, or confirmed it when asked.
  Unsure → ask; never infer a decision from enthusiasm.

Retractions and corrections supersede immediately at every tier: the old
version is replaced, unrelated history is untouched, and a retracted
decision drops back to developing idea or vanishes — it is never kept
"for the record" against the user's stated intent.

## Sensitive material

Credentials, secrets, access tokens, and others' private data are never
durable-memory candidates at any tier, no matter how often repeated.
Reference secret names or placeholders instead of values.

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
