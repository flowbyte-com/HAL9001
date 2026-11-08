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

### Model fields

| Field | Meaning |
|---|---|
| `id` | The exact identifier the provider returns, or one the operator supplied by hand. |
| `label` | Optional display name. |
| `effort` | Optional reasoning level (`low`, `medium`, `high`, `xhigh`, `max`). |
| `context_mode` | `auto` uses the discovered context window; `manual` uses `context_window`. |
| `context_window` | Explicit context-window token limit. |
| `max_output_tokens` | Explicit output-token limit. |
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

## Migration

The migration from the pre-version-2 file:

* derives one endpoint from the legacy provider, endpoint and credential;
* keeps the credential exactly as it was — an environment reference stays an
  environment reference, a literal secret stays a literal secret;
* moves every legacy model under that endpoint, because there was only one
  endpoint to move them to;
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

## Operator commands

```
/model                                     open the model picker
/model list                                list endpoints and their models
/model add <endpoint> <id> [label]         add a model to an endpoint
/model edit <endpoint> <id> <key=value>…   edit a model's settings
/model remove <endpoint> <id> [--use E/M]  remove a model
/model use <endpoint> <id>                 select the active model
/model show <endpoint> <id>                inspect a model's settings
/model context <endpoint> <id> auto|manual [tokens]
/model discover <endpoint>                 probe the provider's catalogue

/endpoint list                             list endpoints
/endpoint add <id> <name> <protocol> <url> [credential-env]
/endpoint edit <id> <key=value>…           edit an endpoint
/endpoint remove <id> [--use E/M]          remove an endpoint
```

Adding a model never changes the active model. Removing the active model, or
an endpoint that holds the active model, is refused unless the operator names
an explicit replacement with `--use <endpoint>/<model>`.
