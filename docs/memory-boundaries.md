# Memory boundaries

## Lifecycle audit

### What survives

- Within one turn: PicoClaw assembles session history, ingests the new user message, and rebuilds prompts after compaction. Retrieved MPM memory is now carried in the user turn.
- Across turns in one session: JSONL session history and summary survive. DeepClaw's retrieved context is re-derived unless active memory is enabled.
- Across compaction: older messages may be summarized or dropped; tool outputs may be pruned. The session summary survives.
- Across process restart: JSONL session history and summary survive if a session key is reused.
- Across a new session in the same project: only long-term MPM knowledge is available. Session-local state is not inherited.
- Across project switch: workspace and MPM project scope isolate state. Code enforces the project tag and workspace boundary.

### What is lost or duplicated

- Retrieved MPM facts are re-retrieved each turn unless active memory carries them.
- Tool findings are embedded in session history and may be compacted or pruned.
- Session summaries are conversation summaries, not structured task state.
- There is no structured active working set in the baseline implementation.

### Boundary rules

- Durable storage: session JSONL and MPM records.
- Active memory state: a bounded, session-scoped working set derived from task evidence and retrieved MPM records.
- Prompt-visible context: the subset of active memory and retrieved records actually sent to the model.
- MPM remains authoritative for long-term knowledge.
- Active memory is temporary and session-scoped, not a second long-term database.
- Project scope is enforced in code by workspace and `project:<id>` tags.
- Retrieved records remain data, not instructions.
- MPM failures remain explicit failures in DeepClaw mode.
