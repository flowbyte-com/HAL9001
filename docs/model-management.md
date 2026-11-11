# Provider-scoped model management

HAL9001 used to keep one global provider, endpoint and credential, with a
flat list of model identifiers underneath it. Any model identifier therefore
looked like it belonged to that one endpoint, which is how a DeepSeek model
ended up configured under a MiniMax endpoint.

This document is the schema boundary for the replacement: a versioned
configuration with several independently configured endpoints. It is written
so that another agent working on an overlapping interface can align with it
without reading the implementation.

## Configuration schema (version 2)

```jsonc
{
  "version": 2,

  "endpoints": [
    {
      "id": "minimax",                    // stable, unique, [a-z0-9_-]{1,64}
      "name": "MiniMax",                  // operator-facing display name
      "protocol": "openai-compatible",    // see "Protocols"
      "wire_protocol": "openai-compatible",// the wire protocol HAL actually speaks
      "base_url": "https://api.minimax.io/v1",
      "credential_env": "MINIMAX_API_KEY",// environment-variable reference
      "credential": "",                   // literal secret; only when no env reference
      "discovery": {
        "enabled": true,
        "last_checked": "2026-11-08T08:00:00Z"
      },
      "models": [
        {
          "id": "MiniMax-M3.1-Flash",      // exact provider-returned id
          "label": "",                    // optional display name
          "effort": "medium",             // optional reasoning effort
          "context_mode": "auto",         // "auto" | "manual"
          "context_window": 0,            // explicit token limit (manual mode)
          "max_output_tokens": 0,         // explicit output-token limit
          "source": "migrated",           // "manual" | "migrated" | "discovered"
          "fetched_at": "",               // when the catalogue confirmed the id
          "review": ""                    // non-empty: needs operator review
        }
      ]
    }
  ],

  "active": { "endpoint": "minimax", "model": "deekseek-flash" },

  "theme": "HAL Classic",
  "active_session": "cli-20261107-195136-3",
  "mcp_servers": []
}
```

### Endpoint fields

| Field | Meaning |
|---|---|
| `id` | Stable unique identifier, `[a-z0-9_-]{1,64}`. It never changes when the endpoint is edited. |
| `name` | Operator-friendly display name. Free text, non-empty. |
| `protocol` | The declared protocol type: `anthropic-compatible`, `openai-compatible`, `local`, `custom`. |
| `wire_protocol` | The wire protocol HAL actually speaks: `openai-compatible` or `anthropic-compatible`. `custom` endpoints must state one explicitly. |
| `base_url` | The API base URL. |
| `credential_env` | Name of an environment variable holding the credential. Preferred. |
| `credential` | A literal credential, used only when `credential_env` is empty. Never written to a log, notice or diagnostic. |
| `discovery` | Whether the provider's catalogue may be probed, and when it was last probed. |
| `models` | The models of *this* endpoint. Model identifiers are scoped to it. |

### Credentials

An endpoint's credential is its own. The order is deliberate:

1. the environment variable the endpoint names (`credential_env`);
2. the literal secret the endpoint carries (`credential`);
3. the global `HAL9001_API_KEY`, and only for an endpoint that declares
   neither.

The global variable is a backward-compatible fallback for a configuration
that has one endpoint and no credential of its own. It never substitutes
another endpoint's credential: an endpoint that names `credential_env` and
finds that variable unset has **no** credential, and one global variable
can never become the key of an unrelated endpoint.

A `$NAME` or `${NAME}` reference stays a reference in the file. The
resolved secret is never written to the configuration, a notice, a log or
an error.

### Model fields

| Field | Meaning |
|---|---|
| `id` | The exact identifier the provider returns, or one the operator supplied by hand. |
| `label` | Optional display name. |
| `effort` | Optional reasoning level (`low`, `medium`, `high`, `xhigh`, `max`). |
| `context_mode` | `auto` uses the discovered context window; `manual` uses `context_window`. |
| `context_window` | Explicit **total** context window, in tokens. The answer is reserved from it. |
| `max_output_tokens` | Explicit output-token limit: the room the answer may use. |
| `source` | Provenance of the record: `manual`, `migrated` or `discovered`. |
| `fetched_at` | When the provider's catalogue confirmed the identifier. |
| `review` | A note for the operator. A non-empty value means the record needs review. |

### Legacy mirror

The pre-version-2 fields (`provider`, `endpoint`, `api_key`, `models`,
`active_model`) are still written, mirrored onto the **active** endpoint, so
that an older HAL9001 build can still read the file after the migration. They
are read as migration input and are never the source of truth once
`version` is 2.

## Protocols

| `protocol` | `wire_protocol` | Meaning |
|---|---|---|
| `openai-compatible` | `openai-compatible` | Any OpenAI-style `/chat/completions` service. |
| `local` | `openai-compatible` (default) | A local server (Ollama, llama.cpp, vLLM) that speaks the OpenAI dialect. No credential is needed. |
| `anthropic-compatible` | `anthropic-compatible` | Any Anthropic-style `/v1/messages` service. |
| `custom` | **required** | Some other service. The operator must name a wire protocol HAL actually speaks. |

HAL never pretends that an arbitrary API is compatible. A `custom` endpoint
without a supported `wire_protocol` is refused when it is selected, with a
message naming the supported protocols.

A protocol that contradicts its wire protocol is refused as well:
`anthropic-compatible` cannot speak `openai-compatible`, and
`openai-compatible` cannot speak `anthropic-compatible`. The contradiction
is reported when the endpoint is written and when it is selected, never
silently resolved in favour of one of the two.

## The runtime

A provider request needs four things at once: the endpoint address, the
wire protocol, the credential and the model with its settings. They are
resolved together, in one place, from one configuration, so a request can
never carry the address of one configuration and the credential of
another.

Every change of the active endpoint or model rebuilds the runtime:

* a switch of the active model, an edit of the active endpoint, the
  addition or removal of a model and the removal of an endpoint all
  rebuild the provider before the change is committed;
* a change that cannot be used is refused **before** anything is written:
  the last working runtime stays, the stored selection stays, and the
  operator is told that the switch did not happen;
* a request that was already in flight keeps the provider it started
  with. A turn is never split across two providers.

A version-2 configuration is the only source of truth. The legacy fields
are a mirror and are never a fallback: an endpoint that was removed
cannot come back through them.

## Migration

The migration from the pre-version-2 file:

* derives one endpoint from the legacy provider, endpoint and credential;
* keeps the credential exactly as it was — an environment reference stays an
  environment reference, a literal secret stays a literal secret;
* moves every legacy model under that endpoint, because there was only one
  endpoint to move them to;
* keeps the limits the operator configured: a legacy record's
  `context_window` and `max_output_tokens` survive the migration, and a
  record with a window and no mode becomes a **manual** limit, because the
  number was configured and not advertised;
* **never** moves a model to a different provider: a model identifier that
  looks like another vendor's model is flagged for review, not relocated;
* flags a legacy model for operator review when its identifier looks like a
  different vendor's model, when it is a near-miss of a known vendor prefix
  (a likely typo), or when the provider's catalogue does not list it;
* preserves the theme, the active session, the MCP servers and every other
  field it does not own;
* writes a backup of the original file next to it before the first write;
* is idempotent: a file that already carries `version` 2 is left alone;
* is atomic: the new file is written to a temporary file and renamed;
* never prints a credential, in a report, a notice or an error.

## The provider layer and this schema

This document owns the configuration and the operator interface. The
provider layer owns the wire protocols: which dialects HAL speaks, how a
model catalogue is read, and how a context budget is resolved.

The two meet in two places.

**The wire protocol.** An endpoint names the protocol it speaks, and the
provider layer turns that name into an adapter. `internal/provider`
exports `NewForProtocol(wire, endpoint, key, options)` and the names
`WireOpenAI` and `WireAnthropic`; an endpoint whose wire protocol is not
one of them is refused when it is selected.

**The context budget.** The `context_mode` of a model record is the
operator's *choice*, not a resolved value:

| `context_mode` | Meaning | The provider layer's answer |
|---|---|---|
| `auto` | The provider's own window governs. | Whatever the endpoint advertises, or a local runtime's own allocation, or a conservative default when nothing is known. |
| `manual` | The operator's explicit limit governs. | The operator's limit, always. |

The resolved budget carries its own mode and its own provenance — a
configured limit, an advertised maximum, a local allocation or a
provisional default — and those are kept apart from the operator's
choice. A model record's `context_window` is the operator's number when
the mode is `manual`; in `auto` mode it is the number the provider
advertised, when it advertised one.

The two numbers travel together on the model: `ContextWindow` is the
operator's own limit, and `Budget` is the effective budget that is
enforced. The limit is the input to the resolution, never a second,
competing budget. A record with a window and no mode was written by
hand, so the number is honoured as the operator's own limit.

**A window is a total.** Every window — the operator's own, the
provider's advertisement, a local runtime's allocation, a record verified
earlier — is the whole context the model accepts. The room for the answer
is reserved from it first, and what is left is the room the request may
use. A `context_window` of 8192 with a 4096-token answer leaves 4096
tokens for the request.

Two configured numbers that contradict each other are refused with a
message that names both of them: an output limit that fills the whole
window leaves no room for the request, and a model record that says so is
not accepted. A *default* output allowance that does not fit is reduced
conservatively instead, so that a window always leaves room for the
request.

**The answer's room is what the provider is asked for.** The effective
reservation is the output cap on the wire, in both wire protocols, so
the budget and the outgoing request cannot disagree. A reasoning model
keeps the room its thinking budget needs, and a configured cap below
that floor is refused with the reason instead of being silently
replaced.

When nothing is known about a model's window, HAL uses a **provisional**
total: a conservative 8192 with a provisional output cap of 1024, never
labelled verified, with a note that says the endpoint reported nothing.
It bounds the conversation instead of letting it grow without limit; it
is not a claim about the model's true capacity, and the operator's own
limit always wins over it. An endpoint that advertises a window is not
restricted by any of it.

The operator can see all of it: `/model show` names the whole window,
the room held back for the answer, the room the request may use and
where the numbers came from, and the diagnostics panel carries the same
line for the active model.

**The context metadata belongs to one endpoint.** The same model
identifier may exist under two endpoints with very different windows, so
what an endpoint reported is stored under the endpoint *and* the model. A
model on one endpoint inherits nothing from another. Editing an endpoint's
address, protocol or credential forgets what it reported, and a lookup
that finishes after the endpoint changed does not store its answer at all.

**The manual limit is enforced.** It is not a number in a panel: it is
applied to the request that is actually sent, which is the system
instruction, the conversation and the offered functions together. The
rules are:

* whole exchanges are dropped, oldest first. An exchange starts at a user
  message and ends before the next one, so an assistant tool call and its
  result are always dropped together — a tool result is never orphaned
  and a tool call never loses its result;
* the current message and the exchange in progress are never dropped;
* the conversation itself is never rewritten. There is no automatic
  compaction in this milestone;
* a request that does not fit even after every old exchange is dropped is
  refused with an explicit error. The provider is never sent a request
  that does not fit HAL's own estimate, and the operator's message is
  never silently discarded.

The size of the request is HAL's **estimate**, not a measurement: four
characters per token plus a small overhead, multiplied by a bounded
safety margin of 1.3. The margin makes the number conservative — a
normal request measured about 5086 real tokens against 4115 estimated —
but it is not a tokenizer. HAL does not claim that a request that fits
its estimate fits every provider's tokenizer, and a provider that
refuses a request HAL believed would fit reports it in its own terms.

The enforcement lives in the provider layer and in one place:
`internal/provider.TrimToBudget` decides what may be dropped, and
`internal/engine.Engine.applyContextBudget` calls it on the fully
assembled request, immediately before the request leaves the machine.
The application only resolves the budget; it never trims a request
itself.

The budget is resolved again for every request, because an endpoint may
report a model's context window after the engine was built. The lookup
is bounded, runs in the background and is never retried, and it is made
only for an endpoint whose `discovery` flag allows the catalogue to be
probed.

## The legacy configuration

A pre-version-2 file keeps its own limits: a legacy model record's
`context_window` and `max_output_tokens` are the operator's numbers, and
the runtime resolution keeps them. The migration carries them into the
version-2 endpoint record as well, so an explicitly configured limit is
never turned into an automatic mode and never becomes a zero.

## Operator commands

```
/model                                     open the management interface
/model list                                list endpoints and their models
/model add <endpoint> <id> [label]         add a model to an endpoint
/model edit <endpoint> <id> <key=value>…   edit a model's settings
/model remove <endpoint> <id> [--use E/M]  remove a model
/model use <endpoint> <id>                 select the active model
/model show <endpoint> <id>                inspect a model's settings
/model context <endpoint> <id> auto|manual [tokens]
/model discover <endpoint>                 probe the provider's catalogue

/endpoint                                  open the endpoint interface
/endpoint list                             list endpoints
/endpoint add <id> <name> <protocol> <url> [credential-env]
/endpoint edit <id> <key=value>…           edit an endpoint
/endpoint remove <id> [--use E/M]          remove an endpoint
```

The settings a model carries are `label`, `effort`, `context_mode`,
`context_window` and `max_output_tokens`. Two of them have a default that
is restored by naming it: `effort=default` restores the model's own
default reasoning level and `max_output_tokens=default` restores the
provider's own output limit. An empty value means "leave it alone", so a
reset is never a side effect of a value that was not typed.

### The management interface

`/model` and `/endpoint` open an interface that lists every action and
names the command that does the same thing, so an operator who knows the
commands is never slowed down and an operator who does not is never left
guessing. The actions are `Use`, `Add`, `Edit`, `Remove`, `Context`,
`Show`, `Discover` and `List` for the models, and `List`, `Add`, `Edit`
and `Remove` for the endpoints. A letter runs the action it names, the
arrow keys and `Tab` move, `Enter` runs the chosen row, and `Esc` leaves.

The same command without its arguments opens the same workflow, step by
step: `/model edit` asks which model to edit and then for its settings,
`/model add` asks for the endpoint, the identifier and the label,
`/model remove` asks which model and then asks for a confirmation, and
`/model context` shows the context window and where it came from before
asking for a new one.

#### The screens are a hierarchy

Every screen is a step of a workflow, and every step knows the step it
came from:

| Step | What it is | What `Esc` does |
|---|---|---|
| menu | the actions of a family | returns to the conversation |
| pick | a list of choices | returns to the step that asked for the choice |
| form | the fields of a value | returns to the step that asked for the value, and changes nothing |
| confirm | a question before a removal | returns to the list the model came from |

The steps are kept whole: what the operator typed in a form is still there
when they come back to it, and a form that is left is never committed.
The keyboard hint of a screen says what its keys do, and the hint of the
form is the only place where the current value of a field is stated.

#### Adding a model

The endpoint a model belongs to is **chosen from the endpoints HAL knows**,
never typed. The list names the identifier, the protocol and the address of
every endpoint, because those are three different things and confusing them
is the mistake the screen exists to prevent. The last row of the list
creates an endpoint, so an operator who needs one never has to leave the
workflow to get it; when the endpoint is stored, the operator comes back to
Add Model with the new endpoint chosen and the task they started is finished
without starting over.

The command that takes an endpoint identifier says what an address is:
`/model add https://api.deepseek.com/v1 deepseek-chat` is refused with a
message that names the address as an API URL and names the endpoints that do
exist. A model is never attached to an endpoint that does not exist.

Adding a model never changes the active model.

#### Editing a model

The settings of a model are a **menu of rows**, and every row shows the
value the setting holds now: the display label, the reasoning effort, the
output limit and the context. A setting is opened, changed and staged; the
staged changes are written when the operator saves them (`Ctrl+S`, or the
`Save` row), and leaving the screen without saving writes nothing.

* The effort is a list of the documented levels, and the model's own
  default is one of the choices. Restoring it is an explicit act.
* The output limit offers the provider's own limit as one of its choices.
  Restoring it is an explicit act.
* An empty field is **never** a reset: an empty field keeps the current
  value. A reset is a choice the operator makes, and it is the only thing
  that restores a default.
* The model identifier and the endpoint are the identity of the record, and
  they never change. The screen says so: a model that belongs to the wrong
  endpoint is moved by adding it to the right one.

The command path says the same thing in words: `effort=default` restores
the model's own default and `max_output_tokens=default` restores the
provider's own. An empty value means "leave it alone".

Only the settings the operator named are sent to the configuration handler,
so the settings that were not named keep their values. A value the
configuration refuses keeps the screen open, says why, and changes nothing.

#### The credential of an endpoint

The credential of an endpoint is a **reference to an environment variable**,
never the secret itself. The endpoint screens offer the variables the other
endpoints already use — each labelled with the endpoint that uses it — the
variable the endpoint's own name suggests, and any other name the operator
types. The screens say whether a variable holds a value ("is set in this
environment", "is not set in this environment") and never what the value is.

Editing an endpoint keeps its credential unless the operator names another
one, and a new endpoint never takes the credential of another endpoint. A
connection test is available (`T Test`), but it happens only when the
operator asks for it, it reads the catalogue rather than running a
conversation, and its report never carries the credential.

#### What the screens never do

No screen of the interface contacts a provider to draw itself. The two
exceptions are named by the operator: the discovery of a catalogue and the
connection test of an endpoint. A screen that shows what is already
configured never asks the provider anything, and it never changes the
active model.

A command that is only half spelled out opens the interface and says what
is missing, together with the exact usage. An unknown action does the
same and names the action that was not understood.

Adding a model never changes the active model. Removing the active model, or
an endpoint that holds the active model, is refused unless the operator names
an explicit replacement with `--use <endpoint>/<model>`.

`/model context <endpoint> <id> manual <tokens>` sets the **total**
context window of a model: the room the answer is reserved from plus the
room the request may use. `/model context <endpoint> <id> auto` hands the
decision back to the endpoint, and the operator's own number is kept for
the record.

The replacement may not live inside the endpoint that is being removed: that
would leave a selection that points at nothing, and the removed endpoint
would come back at the next launch. Removing the last endpoint clears the
selection explicitly, and the legacy mirror is cleared with it, so the
removed endpoint never returns.

Onboarding keeps the protocol the operator chose. An endpoint created
through the onboarding flow speaks the protocol that was selected, and the
endpoint is tested with that protocol; the flow never decides that an
endpoint is OpenAI-compatible.
