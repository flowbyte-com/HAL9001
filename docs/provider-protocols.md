# Model endpoints, protocols and discovery

HAL9001 talks to model endpoints over a small number of implemented
protocols. It keeps no catalogue of providers and no catalogue of
models: what an endpoint offers is asked of the endpoint.

## Endpoint categories and wire protocols

An endpoint declares a **category** and names the **wire protocol** HAL
speaks to it.

| Category | Wire protocol | Conversation |
|---|---|---|
| `anthropic-compatible` | `anthropic-compatible` | Anthropic Messages API |
| `openai-compatible` | `openai-compatible` | OpenAI chat completions |
| `local` | `openai-compatible` by default, `anthropic-compatible` if named | either |
| `custom` | must be named | either |

The category says how the endpoint is meant to be reached; the wire
protocol says which HTTP conversation HAL actually speaks. The two are
separate so that one implementation can serve several labels: a
`custom` endpoint that names `anthropic-compatible` is the same code as
an `anthropic-compatible` endpoint.

HAL never infers a protocol from a URL and never guesses one from a
model identifier. An unknown category, an unsupported wire protocol and
a `custom` endpoint without a wire protocol are all errors, and the
error names the protocols HAL does speak.

## The two implemented conversations

### OpenAI chat completions

`POST {base}/chat/completions`, `Authorization: Bearer …`, string
message content, `tools[].function.parameters`, and a stream of
undifferentiated `data:` chunks carrying `choices[].delta`.

### Anthropic Messages

`POST {base}/v1/messages`, `x-api-key: …` and `anthropic-version:
2023-06-01`. The differences from the OpenAI conversation are not
cosmetic, and HAL speaks the documented one:

- the system prompt is a **top-level field**, not a message;
- message content is a **list of typed blocks** (`text`, `tool_use`,
  `tool_result`), not a string;
- a tool definition carries an **`input_schema`**, not a nested
  `function` object;
- `max_tokens` is **required**;
- the stream is a sequence of **named events** — `message_start`,
  `content_block_start`, `content_block_delta` (`text_delta`,
  `input_json_delta`, `thinking_delta`), `content_block_stop`,
  `message_delta`, `message_stop`, `error` — not anonymous `data:`
  chunks;
- a tool result is a **user turn** carrying `tool_result` blocks, and
  the results of one assistant turn belong in one user turn.

The base URL may be given with or without the `/v1` segment: both
address the same endpoint. The credential travels in `x-api-key`; a
gateway that wants a bearer token says so explicitly in the
configuration, and HAL never guesses it from the URL.

`max_tokens` has a conservative default for the Messages API (4096),
because the API refuses a value above the model's own output limit and
a rejected request helps nobody. An operator who knows the model's
limit can raise it.

## Model discovery

`Discoverer.ListModels` asks an endpoint which models it serves.

- The listing is **bounded**: the response body is read through a
  limit, the number of models is capped, and the number of pages that
  are followed is capped.
- The **cursor pagination** of the Messages API is followed while the
  bounds allow it; a cursor that does not advance ends the walk.
- The results can be **filtered** by a case-insensitive substring of the
  identifier or the label, and **bounded** by a count.
- An endpoint **without a listing** is reported as such
  (`ErrNoModelListing`), not as a bare status code. The operator can
  then add a model identifier by hand.
- **Authentication and HTTP errors** are reported with the endpoint's
  own explanation, bounded in length, and the credential never appears
  in them.
- The identifiers are the endpoint's **exact** strings. HAL never
  rewrites, trims, lower-cases or otherwise tidies them — it either
  uses them as they are or refuses them (see *Discovery metadata is
  untrusted* below).
- A listed model is **not claimed to work**. `ModelInfo.Access` is
  `unknown` until a request to that model has actually succeeded.
- Discovery **never changes the active model**. Adding a model and
  choosing one are separate, explicit acts.

## Context discovery

A context record always says where its number came from.

| Source | Meaning |
|---|---|
| `operator` | the operator configured the limit |
| `provider` | the endpoint advertised it |
| `local-runtime` | a local runtime reported the context it allocated |
| `cached` | it was verified earlier and stored |
| `provisional` | HAL knows nothing and says so |

`ContextMetadata` keeps two numbers apart:

- `ContextWindow` — the window in force;
- `MaxContextWindow` — the largest context the model advertises.

They are different numbers and are never merged. A local runtime may
allocate far less than the model could accept, and the advertised
maximum must never silently become the budget.

**A window is a total.** Whichever source it comes from — the operator's
own limit, the provider's advertisement, a local runtime's allocation or a
record verified earlier — the number is the whole context the model
accepts. It is never an allowance for the request alone: the room for the
answer is reserved from it first, and what is left is the room the request
may use. A window of 8192 with 4096 tokens reserved for the answer leaves
4096 tokens for the request.

The reservation is the **output allowance**: the room HAL asks the
provider for. It is the model's own cap when it has one, the configured
cap otherwise, and the protocol's default otherwise. Two rules apply to
it:

- a **configured** allowance that does not fit inside the window is a
  contradiction between two numbers the operator wrote down, and it is
  refused with a `ContextAllowanceError` that names both of them;
- a **default** that does not fit is reduced conservatively, so that a
  window always leaves room for the request.

The reservation that comes out of this is the **effective** one, and it
is the number that goes on the wire: the provider is asked for exactly
the room the budget held back for the answer. A provisional record
carries its own output cap, and it is used when the operator configured
none — but never against an operator's own number.

A window that is known never produces a zero or a negative budget: a zero
budget would be read as "no budget at all", which is the opposite of what
a known window says. A window that is not known at all leaves the
provider's own limit in force, and the budget says so.

`ResolveContext` decides the window in force:

1. the operator's own limit always wins;
2. a local runtime's allocation is the budget for a local endpoint,
   because that is what the runtime can accept;
3. the provider's advertised maximum is the budget when there is no
   local allocation;
4. a cached, previously verified record is used next;
5. otherwise the **provisional** record is used: a conservative 8k
   total context and a 1k output cap, `Verified: false`, with a note that
   says the endpoint reported nothing.

A guessed number is never labelled verified. HAL never probes a context
length by generating enormous requests, and a context-limit error never
causes an uncontrolled retry or a claim about the exact maximum.

### The context metadata belongs to one endpoint

A model identifier is scoped to its endpoint: the same identifier may
exist under two endpoints with very different windows. The metadata an
endpoint reported is therefore stored under the **endpoint and the
model**, and one endpoint's advertisement never becomes another's. A
model on endpoint B inherits nothing from endpoint A.

Three things keep the stored metadata honest:

- editing an endpoint's address, protocol or credential forgets what it
  reported: the window it advertised belonged to a configuration that is
  no longer in force;
- a lookup that finishes after the endpoint changed does not store its
  answer: it describes an address that is no longer in force, and it
  would be worse than no answer at all;
- the operator's own limit stays authoritative over anything an endpoint
  advertised.

The provenance and the verified status of every number survive all of
this: the resolution decides which number applies, and the source
travels with it into the budget that is enforced.

### Where the operator's limit comes from

The operator's limit is the number in the model record of the **active
endpoint**. A model belongs to one endpoint, and the limit belongs to the
model, so the record is read from there and not from a flat list. A
pre-version-2 configuration has no endpoint records; its flat list is
the fallback, and the migration carries the limits into the endpoint
record.

The record carries two different numbers and they are never confused: in
`manual` mode the window is the operator's own limit, in `auto` mode it
is what the provider advertised. A record with a window and **no** mode
was written by hand, so the number is honoured as the operator's limit.

The lookup of a model's context is a request to the operator's provider,
so it is made only for an endpoint whose `discovery` flag allows the
catalogue to be probed.

### The budget is enforced

The budget is not a number in a panel. It is applied to the request as
it will be sent:

- `internal/provider` owns the budget and the algorithm:
  `ContextBudget`, `EstimateTokens`, `TrimToBudget`, `TrimReport` and
  the typed `ContextBudgetError`;
- `internal/engine` applies it in `chat()`, the last point before the
  transmission, so it covers the plain turn and every round of the tool
  loop;
- `cmd/hal9001` resolves it and puts it on `provider.Model.Budget`.

The resolution happens again for every request, because an endpoint may
report a model's context window after the interface started. The
lookup itself runs in the background: the interface never waits for a
metadata lookup, and a late answer is used from the next request on.

The rules of the trim:

- the safety instructions are never dropped;
- the operator's current request is never dropped;
- older context is dropped only at exchange boundaries — a user
  message with the assistant and tool messages that answer it — so a
  tool call is never separated from its result;
- the estimate accounts for the instructions, the history, the tool
  definitions and the current request;
- a request that still does not fit is **refused** with an explanation.
  It is never truncated silently, and no retry is attempted;
- nothing is summarised and nothing is rewritten. Older context is
  dropped, not compacted.

The token count is always an estimate — four characters per token plus
a small overhead, multiplied by a bounded safety margin — and every
message that quotes one calls it an estimate.

**The estimate is not a guarantee.** The safety margin makes the number
conservative: a normal HAL request measured about 5086 real tokens
against 4115 estimated, and dense code and non-English text undercount
by more, so the margin is 1.5. HAL does **not** claim that a request
that fits HAL's estimate fits every provider's tokenizer. The margin is
a bounded guess, not a tokenizer: a request that is too large for the
model can still be too large after the estimate says it fits, and the
provider's own refusal is then the honest report.

### The output cap on the wire

The room the budget reserves for the answer is the number the provider
is asked for. The budget and the outgoing request cannot disagree: the
effective reservation travels with the request, and both wire protocols
carry it as their output cap (`max_tokens`).

- a configured allowance that does not fit inside the window is refused
  before the request is transmitted, with both numbers named;
- a default that does not fit is reduced conservatively, so that a
  window always leaves room for the request;
- a reasoning model keeps the room its thinking budget needs: the
  Messages API refuses a smaller cap, so a configured cap below that
  floor is refused with the reason instead of being silently replaced.

The operator's `context_window` is the **total context** the model
accepts, exactly like an advertised or an allocated window: the room for
the answer is reserved from it, and the request may use what is left. A
`context_window` of 8192 with a 4096-token answer leaves 4096 tokens for
the request.

A **provisional** window is a conservative total, and it is enforced like
any other: an endpoint that reports nothing bounds the conversation
instead of letting it grow without limit. It is not a claim about the
model's true capacity — it is labelled `provisional`, it is never
verified, and the note says the endpoint reported nothing. The operator's
own limit always wins over it, and the endpoint's own advertisement
replaces it as soon as one arrives.

The provisional numbers are small on purpose:

| Number | Value | Meaning |
|---|---|---|
| provisional total | 8192 | the whole context HAL assumes for a model nobody described |
| provisional output cap | 1024 | the room the answer gets inside it |
| effective input budget | 7168 | what is left for the request |

The 8192 leaves room for HAL's own instructions and tool definitions and
for an ordinary exchange, and it cannot be mistaken for a measurement of
the model. A model that advertises a window is not restricted by any of
it: the advertisement replaces the provisional total, and the operator's
own limit replaces both.

### Discovery metadata is untrusted

What an endpoint says about its models is untrusted data, and the two
fields are treated differently:

- an **identifier** is either valid or refused. It is never repaired:
  an identifier with a control character, with surrounding whitespace,
  that is empty or that is longer than 256 runes is refused, and the
  model is not listed. A rewritten identifier would be sent to the
  provider under a name the endpoint does not know;
- a **display label** is sanitised: complete ANSI CSI and OSC sequences
  are removed, the remaining control characters are dropped, line
  breaks become spaces, and the result is bounded to 120 runes;
- a repeated identifier is listed once.

The identifier that reaches a provider request is the exact string the
endpoint wrote, and the sanitised values are what reach the interface.

### Local runtime probing

A local endpoint may be asked what it actually allocated:

- the OpenAI-style listing (`GET {root}/models`);
- the runtime's own tag listing (`GET {root}/api/tags`);
- the runtime's model objects (`GET {root}/api/v0/models`);
- the runtime's model report (`POST {root}/api/show`), whose
  `model_info` names the architecture's context length.

Every probe is optional. A local server that answers none of them yields
no metadata, and HAL then says that it knows nothing about the window.

## What is not here

- No provider catalogue: an unknown provider name is an error, not a
  lookup.
- No model catalogue: the models come from the endpoint.
- No protocol inference from a URL.
- No automatic retry of a failed context probe.
- No automatic compaction: older context is dropped, never summarised.
- No trimming on a guessed window.
