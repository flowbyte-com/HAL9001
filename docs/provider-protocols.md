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
  rewrites, trims, lower-cases or otherwise tidies them.
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
| `provisional` | HAL guessed a conservative budget |

`ContextMetadata` keeps two numbers apart:

- `ContextWindow` — the budget in force;
- `MaxContextWindow` — the largest context the model advertises.

They are different numbers and are never merged. A local runtime may
allocate far less than the model could accept, and the advertised
maximum must never silently become the budget.

`ResolveContext` decides the budget:

1. the operator's own limit always wins;
2. a local runtime's allocation is the budget for a local endpoint,
   because that is what the runtime can accept;
3. the provider's advertised maximum is the budget when there is no
   local allocation;
4. a cached, previously verified record is used next;
5. otherwise the **provisional** budget is used: a conservative 8k
   context and a 4k output cap, `Verified: false`, with a note that
   says the endpoint reported nothing.

A guessed number is never labelled verified. HAL never probes a context
length by generating enormous requests, and a context-limit error never
causes an uncontrolled retry or a claim about the exact maximum.

### Local runtime probing

A local endpoint may be asked what it actually allocated:

- the OpenAI-style listing (`GET {root}/models`);
- the runtime's own tag listing (`GET {root}/api/tags`);
- the runtime's model objects (`GET {root}/api/v0/models`);
- the runtime's model report (`POST {root}/api/show`), whose
  `model_info` names the architecture's context length.

Every probe is optional. A local server that answers none of them yields
no metadata, and HAL then uses the provisional budget and says so.

## What is not here

- No provider catalogue: an unknown provider name is an error, not a
  lookup.
- No model catalogue: the models come from the endpoint.
- No protocol inference from a URL.
- No automatic retry of a failed context probe.
