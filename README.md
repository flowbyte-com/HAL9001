# HAL9001

HAL9001 is a conversational thinking partner that investigates, architects,
coordinates, manages, reviews, reports, records and suggests. By default it
creates through prompts that other agents implement.

Its default authority is **444: read-only.** HAL may converse, reason,
retrieve MPM knowledge, research public web pages through Firecrawl and
prepare documents, but it cannot run commands, edit arbitrary files or act
on its own. There are no coding profiles and no model-controlled execution
switches.

There is one exception, and only the human operator can open it: **666**, a
session-scoped window in which HAL may use real `read`, `write`, `edit` and
`bash` tools with the operating-system user's permissions. It cannot be
enabled by conversation text, a model answer, a skill or an MPM record, and
it ends when the operator disables it, changes conversation, clears the
conversation or quits HAL. See [Session 666](#session-666-operator-enabled).

The original narrow transaction is separate and unchanged: **666
REQUESTED**, a single operator-approved file write inside a dedicated
workspace, available in 444. It is not the session mode. See
[One approved write](#one-approved-write-666).

## Status

Milestone 006: HAL can research the live web through Firecrawl's v2 API
in 444. Two typed tools, `firecrawl_search` and `firecrawl_scrape`, return
ranked sources and main-content Markdown with bounded cost and context,
source attribution, explicit truncation and an untrusted-content boundary.
Credentials come only from `FIRECRAWL_API_KEY`; research cannot enable
666 or write to MPM automatically. See
[Live web research](#live-web-research-firecrawl-444).

Milestone 005: HAL gains an operator-only, session-scoped 666 window with
real `read`, `write`, `edit` and `bash` tools, alongside the preserved 444
workflows. 444 remains the default: normal conversation, typed MPM
knowledge tools, skills and the one-off guarded write. The mode can only be
changed from the trusted permission screen; it is never stored in config,
sessions, checkpoints or MPM.

Milestone 004A: the native application discovers the skills MPM manages
and routes each request to the one that fits, beginning with two
specialised writing skills: `system-prompt-writer` and `humanize-text`.
Routing is local and free, the guidance is loaded into the request that
needs it and nowhere else, and a skill can never change or widen HAL's
permission mode.

Milestone 003.1: the native application holds live conversations through
a fullscreen terminal interface — alternate screen, persistent header and
footer, `Enter` to send, safe multiline input, five themes, clipboard
commands, and persistent model-independent sessions. Prompt Workshop,
review workflow, and further MPM memory workflows arrive in later
milestones.

The former DeepClaw/PicoClaw implementation is preserved for reference in
[`legacy_base/`](legacy_base/) (tag `pre-migration-base`). The new module
does not import it.

## Quick start

```sh
go build ./...
./hal9001 --help
```

On first launch HAL9001 asks for three values: provider endpoint, model
name, and API key. They are stored in `~/.hal9001/config.json` (mode
`0600`); the environment (`HAL9001_ENDPOINT`, `HAL9001_MODEL`,
`HAL9001_API_KEY`) overrides the file and is never written back.
A connection test runs against the provider's model list before saving;
you may save anyway if it fails.

```sh
hal9001                                    # chat (Enter sends, Ctrl+J adds a line)
hal9001 --session work                     # resume or start a named session
hal9001 --list-sessions                    # list stored conversations
hal9001 --no-mouse                         # leave the pointer to the terminal
hal9001 --workspace ~/HAL                  # where an approved write may land
hal9001 --provider openai-compatible \
  --endpoint https://api.minimax.io/v1 \
  --model MiniMax-M3.1-Flash               # flags win over the file
```

## The interface

HAL9001 takes over the terminal with the alternate screen buffer: the
shell output is hidden while HAL runs and the previous screen comes back
when it exits. A fixed header (`● HAL9001` and the session title) sits at
the top, the conversation scrolls in the middle, and the composer, one
row of keyboard hints, a thin divider and the status footer stay at the
bottom.

```text
● HAL9001 · the quarterly review
  Summarize the plan.

◉ The plan has three parts.
  ⚙ read notes/plan.md · completed · 2.0 KiB · 666
                        2s · 4.2k in / 12.8k out

  What changed?
──────────────────────────────────────────────
 the draft
──────────────────────────────────────────────
Enter send · Ctrl+J newline · /help
──────────────────────────────────────────────
444 READ ONLY    Tokens: 4.2k in / 2.8k out    MiniMax-M3.1-Flash · medium
```

The conversation has no speaker labels. User text and assistant text share
one content column, with a narrow gutter to its left. The first row of
each assistant response carries a restrained red `◉` in that gutter; no
label sits beside it. Wrapped lines, Markdown headings, lists, quotes,
code fences and tables keep the same content column. One blank row
separates turns, so consecutive messages stay readable without chrome.
The transcript uses the full usable terminal width with two-cell side
margins; the composer, header and footer keep the full width.

Verified tool activity sits beneath the answer that produced it in one
compact line per call, for example `⚙ bash · completed · exit 0 · 666`.
The per-response statistics line is the last row of the assistant block,
right-aligned and muted. Neither the eye, the tool lines nor the
statistics are part of the reply text, a selection or `/copy`.

The footer is split in three: the permission on the left, the session's
token usage in the middle and the model with its reasoning effort on the
right. Narrow terminals shed detail in a fixed order — the effort, then
the usage, then the model — and the permission is never given up: `444
READ ONLY` stays on screen while you type, and becomes a red `666
UNRESTRICTED` for the whole time the operator-enabled window is active.

| Key | Action |
|---|---|
| `Enter` | Send the message |
| `Ctrl+J` | Newline — works on every terminal |
| `Alt+Enter` | Newline — xterm-style terminals |
| `Shift+Enter` | Newline — only where the terminal reports modifiers |
| `Ctrl+S` | Send (alternative) |
| `Ctrl+K` | Model picker |
| `Ctrl+P` | Open the 444/666 permission control |
| `Ctrl+N` | New conversation |
| `Ctrl+C` | Quit |
| `Esc` | Cancel a stream / close a panel |
| `Ctrl+O` | Expand or collapse the last long prompt |
| `PgUp` / `PgDn` | Scroll the conversation by a page |
| `Home` / `End` | Jump to the start / end of the conversation |
| `Ctrl+End` | Jump to the latest message (the same as the control) |
| `Ctrl+U` | Clear the entire draft (Ctrl+Z restores it) |
| `Ctrl+Z` | Restore the last cleared draft |
| `Ctrl+Y` | Enable 666 on the permission screen; approve the one-off write on the approval screen |
| `Ctrl+N` | Disable 666 on the permission screen; reject the one-off write on the approval screen |
| `/skills` | List the skills MPM offers |
| `/permission` | Switch between 444 and session-scoped 666 |
| `/skill <name>` | Pin a skill for the writing task (`/skill auto` restores routing) |
| mouse wheel | Scroll history (eight rows a notch), or the draft |
| left drag | Select conversation text and copy it |

When you scroll back through the conversation, a small `↓ Jump to
latest message · Ctrl+End` control appears over the bottom of the
conversation. Click it, or press `Ctrl+End`, to return to the latest
message and to the bottom-follow that shows the next answer as it
arrives. The control is drawn over the conversation rather than in a row
of its own, so the frame never changes size and the reading position
never moves when it appears.

`Enter` and `Ctrl+M` are the same byte on a terminal, so the model
picker moved to `Ctrl+K`; `/model` works everywhere. Most terminals
report `Shift+Enter` as a plain `Enter`, which would send the message
instead of breaking the line — use `Ctrl+J` there. HAL detects whether
the terminal reports modifier combinations and says so in `/help`.

Slash commands run locally and never reach the model: `/help`,
`/copy`, `/copy code`, `/theme`, `/model`, `/new`, `/clear`, `/sessions`,
`/effort`, `/expand`, `/collapse`, `/diag`, `/usage`, `/permission`,
`/q`, `/quit`, `/exit`.
Typing `/` opens a suggestion
panel above the composer: keep typing to filter, `Up`/`Down` to move,
`Tab` to complete, `Enter` to run (a complete command runs as typed, a
partial one runs the highlighted suggestion), `Esc` to dismiss it and
keep the draft. A leading `//` sends a literal slash-prefixed message.

The composer is framed by two full-width rules with no side borders and
no placeholder. It is one row for ordinary input, grows only as the text
really wraps (up to five rows) and shrinks back when you delete. One
reserved row below it offers the keys that act on the draft right now, and
it is the same row whatever the draft holds: only its text changes, so the
composer and the footer never move. A short paste stays one row, and a
clipboard's CRLF line endings are one break, not two. Pasting never
submits: multiline pastes keep their line breaks, indentation and code
fences in the composer, where you can edit them before sending. The
composer shows a five-row window on the draft — never a cut-down copy: a
paste of any size is kept whole,
`Enter` sends all of it, and the window follows the caret. `Ctrl+U`
clears the whole draft (even hundreds of pasted lines) without submitting
anything and returns the composer to one row, and says so
(`Draft cleared · Ctrl+Z to restore`); `Ctrl+Z` puts it back, but only
into an empty composer and never across sessions, and says `Draft
restored`. Clearing never touches the conversation — that is what
`/clear` is for. Switching models keeps the conversation history and the
unsent draft: the session store is model-independent. When the draft is
taller than five rows the wheel scrolls it and the caret stays where it
was; typing brings the caret back into view.

Status notices under the footer — a draft cleared, a selection copied, a
conversation renamed — replace each other and disappear after a few
seconds. The row they use is always part of the frame, so a notice
appearing or expiring never moves the conversation or the composer.

**Long prompts.** A prompt that fills more than a few lines is
abbreviated to a four-line preview while HAL writes its answer, so the
answer has room to be read. When the answer is there, the prompt opens
again — the abbreviation was a way to make room, not a way to shorten the
message. A prompt you collapse yourself with `Ctrl+O` stays collapsed;
`/expand` and `/collapse` apply to all of them. A cancelled or failed
turn opens the prompt too, and reopening a conversation shows the stored
prompts in full: the abbreviation is never stored. Collapsing is display
only — the full text is what gets stored and what the model receives.

**Reasoning effort.** MiniMax-M3.1-Flash applies `max` effort when the
parameter is omitted, which is why HAL sends `reasoning_effort` explicitly
and starts at `medium`. `/effort` opens a selector; `/effort low`,
`/effort medium`, `/effort high`, `/effort xhigh` and `/effort max` set a
level directly. The footer shows the active level next to the model
(`MiniMax-M3.1-Flash · medium`) and the choice is stored per model, so
switching models restores each one's level. The parameter is never sent
to a provider that does not document it: an unsupported model says so and
offers no selector. Changing effort never touches the conversation.

**Scrolling.** `PgUp`/`PgDn` move a full page and `Home`/`End` jump to
the start and end. Page keys are recognised by key code, not by their
exact escape sequence, so they work even when the terminal reports
Fn+PageUp as `shift+pgup`. New output only follows the bottom when you
were already there; scrolling up during a reply keeps your place.
Resizing the terminal and collapsing or expanding a long prompt preserve
the line you were reading.

**Mouse.** HAL asks the terminal for mouse events and takes over the
pointer itself, the way Pi does. This is the default; `--no-mouse` (or
`HAL9001_MOUSE=0`) turns it off and leaves the terminal's own selection
in charge. `--mouse` is still accepted as an explicit way to ask for it.

The setting is resolved in one order: an explicit flag, then the
environment, then the default (on). `HAL9001_MOUSE` accepts `1`/`true`/
`yes`/`on` and `0`/`false`/`no`/`off`; a value that is neither is
refused with a message that names the variable, so a typo cannot quietly
change how the terminal behaves. A flag decides on its own, so a
malformed value that no longer decides anything is left alone. Asking
for both `--mouse` and `--no-mouse` is refused instead of silently
choosing one.

- The wheel scrolls the conversation, or a long draft under the pointer,
  or an open panel.
- Left-dragging selects text in the conversation. The selection is
  highlighted while it is dragged, and releasing the pointer copies it to
  the clipboard through OSC 52 — the same mechanism `/copy` uses.
- A drag that reaches the top or the bottom of the conversation scrolls
  the history on its own, one row at a time, and the selection grows into
  the text that comes into view. The scroll stops as soon as the pointer
  leaves the edge, the button is released, the selection is cancelled
  with `Esc`, or the conversation changes. The wheel takes over from it
  while you drag, and the composer is never touched.
- The copied text is the reply's own text: no colours, no borders, no eye
  gutter, no speaker labels, no tool activity and no metadata, and no
  artificial line breaks. A wrapped paragraph comes back as the one line
  it was written as, and a word the renderer had to break comes back
  whole. The interface's own lines — the `Ctrl+O to expand` hint, the
  answer's metadata and tool records — are never copied.
- An ordinary click copies nothing.
- The selection survives scrolling, so a selection can be made across a
  long reply; it is dropped when the conversation changes or the terminal
  is resized.

With reporting on, the terminal sends mouse events instead of selecting
text, so the terminal's own selection needs Shift+drag in most emulators
and right-click behaviour varies. With `--no-mouse` the native
selection, right-click paste and middle-click paste of the PRIMARY
selection on Linux all keep working instead; either way they are
separate from the regular clipboard that `/copy` and a selection write
through OSC 52.

**Bottom-anchored history.** A new or short conversation sits just above
the composer, with the unused space above it, the way a chat application
does. As it grows, older messages move up into scrollable history. The
spacing is presentation only: it never changes the conversation, the
scroll offsets or `Home`/`End`.

**Themes.** `/theme` switches between HAL Classic, Midnight, Amber,
Paper and Monochrome. Each palette paints the whole interface: the
application background and default text, the header and footer, message
labels, rules, the composer rules, panels and Markdown. Paper is a real
light theme. Moving through the picker previews the palette live,
including history already on screen; `Esc` restores the previous theme
and `Enter` saves the choice for the next start. The terminal's own
colours are restored when HAL exits. With `NO_COLOR` the interface is
plain text and the picker says themes cannot be previewed.

**Exiting.** `/q`, `/quit` and `/exit` all close HAL through the same
path as `Ctrl+C`: an active request is cancelled, nothing incomplete is
stored, and the alternate screen, cursor and terminal colours are
restored.

**Sessions.** Every conversation has an immutable ID and a human-readable
title. New sessions start with the local date and time
(`2026-11-02 16:48`); `/rename Museum of Unfinished Ideas` sets a proper
name, and bare `/rename` opens a small editor. Titles never become files,
so any text is safe; clearing the title restores the date/time default.
`/sessions` lists titles with creation and activity dates, newest first,
marking the current one. Selecting a conversation there (or with
`/new`, `/clear`, `Ctrl+N`) makes it the active one, and that choice
survives quitting and relaunching — including while it is still empty.
Nothing is ever deleted: earlier conversations, including ones from
before titles existed, stay on disk and keep working.

HAL remembers which conversation was active, so starting HAL again reopens
it. Permission is not part of that memory: a restarted conversation always
begins in 444.

**Session 666 (operator-enabled).** `Ctrl+P` or `/permission` opens the
permission screen. This is the only place the session mode can change, and
only a deliberate operator chord in that screen can change it: `Ctrl+Y`
enables 666, `Ctrl+N` disables it, and `Esc` leaves the mode alone.
Conversation text, a model answer, a tool result, a skill, an MPM record or
a pasted instruction cannot enable it.

- 666 is scoped to the current conversation. It returns to 444 when the
  operator disables it, starts, switches or resumes another conversation,
  runs `/clear` or `/new`, or quits HAL. It is never written to the config,
  the session store, a checkpoint or MPM, so it cannot survive a restart.
- In 666 the model is offered four real tools: `read`, `write`, `edit` and
  `bash`. They run with the permissions of the operating-system user
  running HAL and are not confined to `~/HAL`. `bash` has a wall-clock
  timeout, bounded stdout and stderr, and its process group is terminated
  when the command ends or is cancelled.
- The mode is rechecked at every operation boundary, not only when the
  tools were offered. Disabling 666 cancels active privileged work; a tool
  result that was denied or cancelled is reported honestly to the model.
- 444 keeps its own guarded write proposal and adds MPM's typed knowledge
  tools. In 666 the guarded `write_file` proposal is replaced by the real
  `write`; MPM tools remain available because they change MPM records, not
  arbitrary operating-system files.
- A red `666 UNRESTRICTED` footer is visible for the whole window. The
  one-off guarded write keeps its distinct `666 REQUESTED` and `666
  WRITING` labels; it is a single transaction, not the session mode.
- The audit trail records each tool's name, action, target, mode, outcome,
  exit code, duration, bytes and truncation. It never records file
  contents or shell command text; a command is identified by a hash.
- Completed tools are also recorded with their assistant turn in a
  versioned, bounded session ledger. On resume the verified records appear
  as historical evidence, including their original turn, 444/666 mode and
  outcome. The current conversation still starts in 444; a historical
  record is never a tool, never a permission and never replayed. Older
  sessions without a ledger load normally, and an unverified earlier claim
  is described as uncertain rather than fabricated.

**One approved write (666).** This is the 444 transaction, separate from
the session mode. HAL's authority stays 444. When a model asks to save a
file, it can only *propose*: HAL prepares the operation — one
workspace-relative path and the exact bytes — and shows it for review. Nothing
touches the filesystem until the operator approves that one operation.

- The workspace is `~/HAL` by default (`--workspace` or `HAL9001_WORKSPACE`
  changes it). Proposed writes can never reach `~/.hal9001/`, a system
  directory or anything outside the workspace.
- Every write is resolved through a handle on the workspace directory, so a
  directory replaced by a symbolic link between the review and the write
  cannot redirect it. Symbolic links are refused outright, and the reviewed
  file must still be the same file when the write happens.
- The review shows the destination, whether it creates or **overwrites** a
  file, the size, the SHA-256 digest, the conversation turn and the full
  contents. The header and the decisions stay on screen while the document
  scrolls: `Ctrl+Y Approve · Ctrl+N Reject · ↑/↓ Scroll · Esc Reject` is
  the last row, at the beginning of a long document and at its end.
- `↑`/`↓`/`PgUp`/`PgDn`/`Home`/`End` and the wheel scroll the document, and
  nothing else.
- `Ctrl+Y` approves, `Ctrl+N` or `Esc` rejects. Only those chords in the
  review can decide it: typed text, a paste, a click or a model message
  never can.
- Approval writes once, reports the destination, and returns to 444. A second
  write needs a second approval. A create never replaces a file that appeared
  in the meantime, and a replace fails if the destination changed after the
  review.
- The status line reads `444 READ ONLY`, `666 REQUESTED` while a request is
  waiting, and `666 WRITING` for the one operation in progress. These are
  the transaction's labels, not the session mode; a red `666 UNRESTRICTED`
  is the session mode and is shown only while it is enabled.
- Decisions and tool activity are recorded in `~/.hal9001/audit.jsonl` —
  time, session, tool or operation, path, digest, decision, outcome, exit
  code, duration and sizes, never the document or command text — and
  records older than six days are pruned.
- Saving a file never gives HAL the ability to execute it: this transaction
  has no shell, no interpreter, no external program and no general
  filesystem editing. Execution is available only through the separate,
  operator-enabled session 666 window.

**Skills (MPM).** HAL discovers the skills MPM manages and uses the one
that fits the request, without being told which to use.

- The skills live in **MPM's store**, not in this repository. HAL reads
  them through MPM's own command line (`mpm call mpm_skills`, the `list`
  and `read` actions only) and never writes to it. A skill can be
  updated in MPM and HAL picks it up on the next request, with no
  rebuild.
- Two skills ship with this milestone: `system-prompt-writer` (clear,
  structured instructions for LLMs, coding agents and automated
  workflows) and `humanize-text` (natural human correspondence that
  keeps the meaning and the sender's voice). Their authoring source is
  in [`skills/`](skills/).
- Routing is decided locally, without an extra model call: the request
  is matched against the activation guidance MPM publishes for each
  skill. Ordinary conversation, investigation and philosophical questions
  select nothing and HAL behaves exactly as before. A follow-up such as
  "make it shorter" continues the writing task already under way.
- Only the selected skill is loaded, and only into the request that
  needs it. It never enters the persona and never enters the session
  history.
- A skill is text from a store, so it cannot change the permission mode:
  the request says so explicitly, the mode is owned by the operator's
  controller, and a skill can never enable 666 or widen the current
  boundary.
- `/skills` lists what is available, `/skill <name>` pins one for the
  writing task ahead, `/skill auto` restores automatic routing, and
  `/diag` names the skill of the latest request and where it came from.
- Each reply carries a **skill credit** on its metadata line, naming the
  skills whose instructions actually reached the model. The credit is
  stored with the answer, with the version and the content hash that
  were supplied, so it stays true after the skill is updated in MPM. It
  is interface metadata: it is never part of the answer, the clipboard
  or a selection, and the model's own text can never produce one.

**MPM knowledge (both modes).** MPM remains the sole owner of its records.
HAL reaches them through MPM's own command line (`mpm call <tool>
--payload …`) and passes MPM's parameters and identifiers through
unchanged. There is no HAL schema, no second store and no direct SQLite
access.

- HAL offers typed tools for memories (`query`, `show`, `save`), lessons
  (`search`, `list`, `propose`), theories (`propose`, `resolve`, `show`,
  `list`, `query`), decisions (`record`, `show`, `list`, `query`),
  evidence (`add`, `list`, `source_groups`), work items (`create`, `list`,
  `show`, `update`, `complete`, `cancel`, `history`, `note`), handoffs
  (`write`, `read`, `list`) and skills (`list`, `read`, `save`).
- Retrieval and mutation are separate actions in the schema. A save is
  reported as successful only when MPM confirms it; a lesson proposal is
  reported as a pending proposal, not a trusted lesson, because only the
  operator can promote it.
- Destructive and operator-only actions (`delete`, `shred`, `supersede`,
  `invalidate`, `promote_to_global`) are not exposed to HAL.
- Large MPM answers are shortened explicitly, with the original byte count
  and digest, rather than presented as if the returned preview were
  complete.
- The knowledge tools are available in 444 and 666: they change MPM
  records, not arbitrary operating-system files. Their calls and outcomes
  are audited like every other tool, without copying the payload.

**Live web research (Firecrawl, 444).** HAL can search the live web and
read one public page at a time through Firecrawl's v2 REST API. This is a
small native Go client; it does not start Node, an MCP bridge or a browser.

- Set `FIRECRAWL_API_KEY` in the environment. The key is sent only in the
  `Authorization: Bearer` header. It is never written to the config, a
  session, the audit trail, `/diag`, a tool result or a model message.
  Missing credentials disable the tools cleanly; `/diag` says why.
- `firecrawl_search` accepts a focused query and returns ranked titles,
  source URLs and concise excerpts. It never scrapes the results
  automatically. The default is five results, with a hard maximum of ten.
- `firecrawl_scrape` accepts one absolute HTTPS URL on a public host,
  refuses credentials, local names and private/special-use addresses, and
  returns main-content Markdown with the source URL, final URL, title,
  description, language, publication date when stated, retrieval time and
  truncation metadata.
- The default budget is three searches and two full-page scrapes per
  research turn, with at most 12 KiB of search text, 24 KiB per page and
  64 KiB of returned research text. Content over the limit is explicitly
  marked truncated. Requests time out after 20 seconds; rate limits and
  transient server errors get at most two bounded retries.
- Retrieved pages are **untrusted external data**. Text inside a page
  cannot enable 666, authorise shell or filesystem operations, change
  settings, create MPM records or override the system instruction. The
  tool result carries `"untrusted":true`, and the request guidance says so.
  If the operator asks to preserve a discovery, HAL uses the existing
  authorised MPM tools; research never writes to MPM automatically.
- The compact activity line reports the result count or returned size,
  failures, rate limits and truncation, and the tool ledger keeps the
  same content-free record across resume.
- `FIRECRAWL_API_URL` overrides the base URL for a compatible proxy or a
  local test server (plain HTTP is allowed only on loopback). The basic
  limits can be tuned with `HAL9001_FIRECRAWL_SEARCH_LIMIT`,
  `HAL9001_FIRECRAWL_MAX_SEARCHES`, `HAL9001_FIRECRAWL_MAX_SCRAPES`,
  `HAL9001_FIRECRAWL_TIMEOUT_SECONDS` and
  `HAL9001_FIRECRAWL_MAX_TURN_BYTES`.

**Token usage.** The footer shows the session's cumulative provider-reported
tokens (`Tokens: 12.4k in / 3.2k out`) once a turn has reported usage;
narrow terminals hide it before the model name. `/usage`
breaks it down: input, output, reasoning, cached input, the last turn and
how many turns were completed versus ended without a usage report. Counts
come only from the provider — HAL never estimates or prices tokens — and
are stored per session across restarts. `/diag` remains the place for
streaming diagnostics.

**The footer row.** The line under the composer is contextual. With an
empty composer it is the permission line (`444 READ ONLY · /help`). As
soon as the draft has text it offers the keys that act on it — `Enter
send · Ctrl+J newline · Ctrl+U clear · /help` — and after `Ctrl+U` it
offers the way back (`Ctrl+Z restore · /help`) until the draft is
restored, submitted or the session changes. It is the same row as
always: nothing is added, the conversation does not move, and a narrow
terminal shortens the hint instead of clipping the row.

**Clearing.** `/clear` empties the visible conversation and starts a fresh
context for the next request, and selects that new session for later
launches — even before anything is said there. The previous conversation is **not** deleted: it stays
recoverable through `/sessions`. Model, effort and
theme are kept. `/new` does the same and is the shortcut (`Ctrl+N`) for
it; `/clear` is the explicit name for "start over". `/clear` works during
a running turn — it cancels the turn first, so nothing from it can appear
in the new conversation.

**Slow turns.** While a model is working, the notice under the footer
shows the phase and the elapsed time (`waiting for the model… 2s`,
`thinking… 47s`, `streaming… 51s`). A reasoning model can spend a long
time thinking before it writes anything: the indicator says so, without
ever showing the reasoning text. `/diag` reports the last turn's
duration, event counts, finish reason and token usage — counts only,
never prompt, reply or reasoning text.

If a turn ends without any visible answer — for example the output budget
was spent on reasoning — HAL explains it with the provider's finish
reason and token count, shows it through `/diag`, and stores no assistant
message. Nothing is retried automatically and the output cap is never
raised without the operator asking.

See [`docs/terminal-support.md`](docs/terminal-support.md) for the
tested and unsupported terminal behaviour. The pseudo-terminal checks
that produced it live in [`tools/ptycheck/`](tools/ptycheck/).

## Layout

```text
cmd/hal9001/          entry point: flags/env/file config, TUI launch
internal/capability/  444 allowlist: what HAL may do, default-deny rest
internal/permission/  session-scoped 444/666 controller and read-only Gate
internal/agenttools/  the 666 read/write/edit/bash executor
internal/knowledge/   typed MPM knowledge tools (both modes)
internal/persona/     identity, conversational contract, Dave Protocol
internal/session/     model-independent JSONL conversation storage
internal/provider/    OpenAI-compatible streaming client + contracts + mock
internal/memory/      durable record tiers, admission rules, store interface
internal/engine/      conversation core: session + provider + tool loop + boundary
internal/tui/         fullscreen chat, themes, slash commands, pickers
internal/clipboard/   OSC 52 clipboard writer (write-only, no model access)
internal/config/      three-value onboarding, user config file (0600)
docs/                 architecture, migration inventory, dependencies
legacy_base/          preserved DeepClaw/PicoClaw reference (separate module)
```

## Rules

- 444 is the default. The capability allowlist is explicit; unknown
  operations are refused by default-deny. 666 exists only inside a live
  operator-enabled conversation and never widens any other session.
- Only the trusted interface can change the permission mode. The engine
  receives a read-only gate and rechecks it at every privileged operation.
- Presentation stays in the TUI; the conversation core has no terminal code.
- Conversation storage is model-independent: switching providers never
  discards the discussion.
- Credentials live in a 0600 user config file or the environment, never
  in logs, sessions, or Git. HAL9001 never reads PicoClaw, OpenCode or
  MPM credentials.
- No PicoClaw or lobster branding anywhere in the new application.
- Copying is a UI feature, not a tool: OSC 52 writes to the terminal,
  never to the model. The clipboard is never reachable from the model,
  and the terminal's mouse modes are restored when HAL exits.
- The frame always fits the terminal, so HAL never scrolls the terminal
  and never leaves the cursor hidden or the terminal in raw mode.

## Safeguards

Every request carries an output cap, and every turn is bounded in time,
so a provider cannot generate or stall without limit:

| Setting | Default | Flag | Environment |
|---|---|---|---|
| Output cap | 15000 tokens | `--max-tokens` | `HAL9001_MAX_TOKENS` |
| Idle timeout | 45 s | `--stream-idle-timeout` | `HAL9001_STREAM_IDLE_TIMEOUT` |
| Turn ceiling | 4 min | `--turn-timeout` | `HAL9001_TURN_TIMEOUT` |

The values also persist in the user configuration file. Zero means "use
the built-in default"; an explicit value always wins, so an operator who
needs a larger budget or a longer thinking phase raises it here. The
idle window resets on every chunk the provider sends, while the turn
ceiling is absolute: a reasoning phase that keeps streaming is still
bounded by it. When either fires, the HTTP request is closed, so the
provider stops generating and stops billing. `Esc` does the same
immediately.

## Limitations (Milestone 003.1)

- One provider shape: OpenAI-compatible `/chat/completions` (SSE
  streaming) plus a `/models` connection test. No per-model thinking
  controls yet.
- Live provider verification needs `HAL9001_LIVE_SMOKE=1` with real
  credentials; it is skipped by default and reported separately.
- No memory write-back outside MPM, no Prompt Workshop, no review
  workflow, no delegation and no scheduled tasks. Web research is limited
  to Firecrawl search and single-page scrape; crawl, batch, browser
  interaction, agent jobs and monitoring are deliberately absent.
- Assistant messages render as Markdown on completion; the live stream
  is plain text.
- Modifier-aware shortcuts (`Shift+Enter`) need a terminal that
  implements the Kitty keyboard protocol; GNOME Terminal does not.
- Token usage is shown only when the provider reports it in the stream;
  absent usage is reported as unknown, never estimated.
