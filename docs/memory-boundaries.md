# Memory boundaries

## Layers after 004A

DeepClaw now has four distinct layers. They differ in ownership, lifetime and
the route by which they can enter a prompt.

| Layer | Owner | Lifetime | Prompt entry |
|---|---|---|---|
| Recent conversation history | PicoClaw session store | One session; compacted or summarized | Replayed as real `user`/`assistant`/`tool` messages |
| Evidence-backed task state | `ActiveMemoryStore` sidecar under `PICOCLAW_HOME`, protected by isolation | One project + session; survives compaction and restart; cleared by objective completion or explicit new objective | Separate lower-trust `task.state` prompt part only |
| Resident MPM retrieval cache | `ActiveWorkingSet.Records` in the same sidecar | One session while active memory is enabled | Lower-trust `mpm.retrieval` prompt part |
| Long-term MPM knowledge | MPM store | Project lifetime | Retrieved records, never written by task state |

### Recent conversation history

Owned by the session store. The current turn's user message is authored only by
the user; retrieved context and task state are attached to the request view and
are never persisted as user text. History is the first source of truth for a
fact. Task-state rendering suppresses any entry whose evidence quote is still
present in history, the summary, or the current user message.

### Evidence-backed task state

Owned by the runtime, not the model. It records only deterministic evidence:
explicit user markers, tool-reported commands and outcomes, and session /
compaction lifecycle events. Each entry preserves:

- origin (`user`, `tool`, `runtime`, `model`);
- project and session identity;
- an evidence reference (turn, tool call, command/path, exit status, output
  hash/length, bounded quote);
- verification (`user_confirmed`, `tool_reported`, `runtime_verified`,
  `proposed`, `unresolved`);
- supersession (`Superseded`, `SupersededBy`, `Supersedes`).

A tool result proves what the tool reported, not why it happened. Assistant
claims are only captured when explicitly labelled `Proposed diagnosis:` or
`Proposed decision:` and always remain `proposed`; they are never promoted to
verified facts. Task state is never written to MPM automatically.

The store is created only when `DEEPCLAW_TASK_STATE=1`. It lives under
`PICOCLAW_HOME/.deepclaw/active-memory`, outside the agent workspace, and is
registered with `isolation.ProtectPaths`, so agent file tools and the sandboxed
shell cannot rewrite it.

Two budgets are distinct:

- **Storage budget**: what the sidecar remembers. It is bounded to 64 entries,
  240 characters per entry content and 320 characters per evidence excerpt.
  Superseded entries are retained for audit but are not rendered.
- **Prompt-residency budget**: how much of that stored state occupies a request.
  `DEEPCLAW_TASK_STATE_BUDGET` caps the rendered block at 500 estimated tokens
  by default (100-2000, heading included), but the actual block is usually much
  smaller.

`DEEPCLAW_TASK_STATE_RESIDENCY` selects the residency policy:

- `boundary` (default): render an entry only while its evidence is unavailable
  in the visible window. Visibility comparison is whitespace-normalized, so a
  stored one-line excerpt matches multi-line history or a summary. Routine
  filesystem successes and compaction bookkeeping are not rendered; failures
  are. Completed objectives and superseded entries are omitted.
- `always`: the Milestone 004A renderer, kept for comparison and compatibility.
  It uses exact quote matching and renders every non-superseded entry whose
  quote is not exactly present.

Boundary mode is conservative: it can recognize an identical finding in a
summary, but not a paraphrase. A paraphrased summary therefore leaves the
stored entry resident rather than risking suppression of a fact the summary did
not actually preserve. The state remains a separate `PromptPart` with source
`task.state`, inside the existing lower-trust retrieved-context envelope.

The rendered block is a **per-request snapshot**, not a turn-long snapshot. A
verified tool result is committed to the sidecar before the result joins the
next request view. Before dispatch, the runtime re-renders only the
`task.state` block against the current history, summary and committed state.
The MPM block and the user-authored question are preserved, no retrieval is
repeated, and the persisted session history is not rewritten. Once a request is
dispatched its message slice is frozen, so a later tool result cannot rewrite
what the provider already received. `always` retains 004A's visibility rules;
it no longer carries a turn-start snapshot forward.

### Resident MPM retrieval cache

Owned by the existing active-memory lifecycle. It carries retrieved MPM records
across turns so retrieval is not repeated. It remains a cache of MPM content:
it does not capture task progress, and a retrieval-cache merge must preserve
task state rather than replace the sidecar. It enters the prompt as
`mpm.retrieval`.

### Long-term MPM knowledge

Owned by MPM and scoped by `project:<id>`. Only explicit, durable knowledge
belongs there. Task state does not call MPM, does not schedule reflection and
does not add a summarization step.

## Lifecycle audit

### What survives

- Within one turn: PicoClaw assembles session history, ingests the new user
  message, and rebuilds prompts after compaction. Retrieved MPM memory and task
  state are request-only parts.
- Across turns in one session: JSONL session history and summary survive. Task
  state survives in its sidecar and is re-rendered when relevant.
- Across compaction: older messages may be summarized or dropped; tool outputs
  may be pruned. The session summary survives, and task state retains tool
  evidence that history no longer shows.
- Across process restart: JSONL history, summary, and task state survive if the
  same explicit session key and project are reused.
- Across a new session in the same project: only long-term MPM knowledge is
  available. Task state is session-scoped and is not inherited.
- Across project switch: session scope, task-state store ownership, workspace
  and MPM project tags isolate state. A sidecar carrying another project's
  identity is discarded on load.

### What is lost or duplicated

- Retrieved MPM facts are re-retrieved each turn unless active memory carries
  them.
- Tool findings are embedded in session history and may be compacted or pruned;
  task state keeps a bounded evidence excerpt and source reference instead.
- Session summaries are conversation summaries, not structured task state.
- Facts still visible in history are intentionally not repeated by task state.
- Superseded entries remain on disk for audit but are not rendered.

### Boundary rules

- Durable storage: session JSONL and MPM records.
- Task state: a bounded, session-scoped, evidence-backed working set, distinct
  from the MPM retrieval cache.
- Prompt-visible context: the subset of task state and retrieved records
  actually sent to the model.
- MPM remains authoritative for long-term knowledge.
- Task state is temporary and session-scoped, not a second long-term database.
- Project scope is enforced in code by workspace, session scope and
  `project:<id>` tags.
- Retrieved records and task state remain data, not instructions.
- MPM failures remain explicit failures in DeepClaw mode.
