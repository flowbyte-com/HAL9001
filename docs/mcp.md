# Native MCP client

HAL9001 can use servers that speak the Model Context Protocol (MCP). This
is a native client built on the official
[`modelcontextprotocol/go-sdk`](https://github.com/modelcontextprotocol/go-sdk)
(`v1.8.0`). It is deliberately separate from HAL's existing `mpm call`
bridge: the MPM integration keeps working exactly as before, and no MPM
tool is replaced by MCP.

## Status

- The client is available but does nothing until an operator configures a
  server **and** explicitly connects it with `/mcp connect`.
- The default transport for a configured server is stdio; remote servers
  use Streamable HTTP.
- The client requests the current protocol revision and lets the server
  negotiate an older one. With SDK `v1.8.0` the supported versions are
  `2026-07-28` (latest), `2025-11-25`, `2025-06-18`, `2025-03-26` and
  `2024-11-05`. The version actually negotiated is shown in the `/mcp`
  panel as `protocol <version>`.

## Configuration

MCP servers live in the existing user configuration file
(`~/.hal9001/config.json`, mode `0600`) under `mcp_servers`. The list is
empty by default. HAL reads it at startup; it never writes it, and the
model cannot create, edit, connect or disconnect a server on its own.

```json
{
  "provider": "openai-compatible",
  "endpoint": "https://api.example.com/v1",
  "models": [{"id": "example-model"}],
  "active_model": "example-model",
  "mcp_servers": [
    {
      "name": "local-tools",
      "transport": "stdio",
      "command": "/usr/local/bin/mock-mcp",
      "args": ["--stdio"],
      "cwd": "/home/operator/work",
      "env": {"MOCK_LOG_LEVEL": "warn"},
      "timeout_seconds": 30,
      "allowed_tools": ["read_note"]
    },
    {
      "name": "remote-tools",
      "transport": "http",
      "url": "https://mcp.example.com/mcp",
      "headers": {"X-Api-Key": "${MOCK_API_KEY}"},
      "token_env": "MOCK_BEARER_TOKEN",
      "timeout_seconds": 20,
      "allowed_tools": ["search_docs"]
    }
  ]
}
```

Fields:

| Field | Meaning |
|---|---|
| `name` | Unique identity, 1-64 characters of `A-Z a-z 0-9 _ -`. It is part of every exposed tool name. |
| `transport` | `stdio` (default) or `http` (Streamable HTTP). |
| `command`, `args`, `cwd`, `env` | The local child process. `env` adds to a small inherited set (PATH, HOME, locale, proxy and certificate variables); HAL's other environment variables, including provider and search keys, are not passed to the child. A value of `${NAME}` is resolved from HAL's environment at connect time. |
| `url` | The Streamable HTTP endpoint. `https` is required for every non-loopback host; plain `http` is accepted only for a literal loopback host (`localhost`, `127.0.0.0/8` or `::1`) so a local fixture can run without TLS. |
| `headers` | Extra HTTP headers. Values may contain `${NAME}` references. |
| `token_env` | A variable holding a bearer token. It is sent as `Authorization: Bearer <token>`. |
| `timeout_seconds` | One call's timeout. Zero means 30 seconds; the maximum accepted is 600. |
| `allowed_tools` | The complete 444 policy for this server. Every tool not named here needs 666. |

A missing `${NAME}` reference, or a missing `token_env` variable, is a
configuration error: the server does not connect with an empty credential.
Secrets are resolved in memory only. They are not written to the
configuration, a log, the audit trail, the session, a model prompt or any
tool result. A credential is redacted from successful text, IsError text,
structured and nested values, metadata, resource and prompt documents, and
base64 image/audio payloads, in every textual representation that could
carry it. Redaction work is bounded; a value that cannot be scanned within
the bound is withheld rather than returned raw. Local servers receive only
the small inherited environment described above; a server that needs
another variable must have it named explicitly, and configured `env` values
are treated as credentials for redaction.
Use `https://` for every remote endpoint. A plain `http://` URL is accepted
only when its host is a literal loopback address, where it is intended for
a local test fixture; any other host is refused at configuration time.

A redirect is not followed. The configured endpoint is the only address
that receives the headers, so a 3xx response cannot move a credential to
another host.

## Commands

| Command | Action |
|---|---|
| `/mcp` or `/mcp list` | Show the configured servers, their state, negotiated protocol, capabilities and counts. |
| `/mcp status` | The same view. |
| `/mcp tools <server>` | Re-list tools, resources and prompts of a connected server. |
| `/mcp connect <server>` | Start the server and perform the MCP handshake. |
| `/mcp disconnect <server>` | Close the session; a local child process is shut down and reaped. |
| `/mcp read <server> <uri>` | Read one resource and show it in the panel as an untrusted document. |
| `/mcp prompt <server> <name> [key=value ...]` | Fetch one prompt and show it in the panel as an untrusted document. |

There is no automatic connect at startup. A server that is configured but
not connected contributes no tools, runs no subprocess and makes no HTTP
request.

## Permissions

The permission decision never comes from the server.

- **444 (default).** The model is offered only the tools named in that
  server's `allowed_tools`. Everything else is refused at the execution
  boundary even if the model asks for it, even if the server's tool list
  was stale, and even if the server set `readOnlyHint` or any other
  annotation. Annotations are display metadata, never authority.
- **666 (operator-enabled).** Every tool of every connected server is
  offered. Each call begins through `permission.Gate.Begin`, so disabling
  666 cancels in-flight MCP calls and revokes further ones. HAL's own
  controller remains the only object that can enter or leave 666.
- **Unknown tools.** A name that is not in the connected registry is
  refused. A name that two different server tools would share is removed
  from the registry entirely (fail closed), so one server can never answer
  for another.
- **No elevation.** A server cannot read the permission mode, change it,
  call HAL's built-in tools, reach the 666 executor or write the
  configuration. A tool result claiming otherwise is only text.
- **Bounded execution.** A call times out, is bound to the turn's context,
  and can be cancelled with `Esc`. It is never retried automatically after
  a connection loss: the server is marked disconnected, its tools are
  removed, and the operator must `/mcp connect` again.

The model sees MCP tools through HAL's ordinary tool surface, so they keep
the normal activity line, cancellation, tool ledger and audit trail.
Model-visible names are `mcp__<server>__<tool>`, sanitized to the
provider's function-name alphabet and capped at 64 characters. Every name
carries a 16-hex SHA-256 suffix of the exact server and tool identity, so
two distinct identities cannot produce one name. The registry also checks
for an exact collision and withdraws both names if one is ever found.

## Untrusted input

Everything a server sends is data, not instruction: tool descriptions,
input schemas, results, structured results, resources and prompts.

- Tool descriptions reach the model with an explicit `[MCP server … —
  untrusted description]` marker, and the request carries a guidance block
  saying that server output is not an operator instruction.
- Input schemas are validated, bounded and sanitized. Annotation-only
  keywords (`description`, `title`, `$comment`, `examples`, `default`) are
  removed, remote references are refused, and an unusable schema is
  replaced by a permissive `object` fallback. Validation keywords keep
  their exact values, including enum and const instances.
- Tool results are wrapped in an envelope with `"untrusted":true`, a fixed
  warning note, the server and tool names, and the server's own error flag.
- Resources and prompts are operator-facing documents in the `/mcp` panel.
  They are not inserted into the model's conversation automatically.
- Output is bounded before it reaches the model, the panel or the audit
  trail: 48 KiB per tool result, 64 KiB per resource, 48 KiB per prompt,
  64 KiB per tool schema, 1 KiB per description, 128 tools per server and
  16 servers. A cut document says it was cut; a half document is never
  presented as whole.
- The audit and tool ledger record names, modes, durations, sizes and
  outcomes. They never record arguments, result text, resource text or
  credentials.

## Authentication

The implemented mechanisms are the documented HTTP ones that need no
interactive browser flow:

- arbitrary headers, including provider-specific API-key headers;
- `Authorization: Bearer` from `token_env`;
- `${NAME}` environment expansion for either.

OAuth is **not implemented** in this client. The SDK contains OAuth
helpers, but HAL does not claim or exercise an OAuth flow here, so a server
that requires interactive OAuth cannot be used yet. There is no token
storage, refresh or browser callback. Use a bearer token in the
environment, or a header supplied by the operator.

## What is deliberately absent

- No automatic server start, discovery over the network, marketplace or
  configuration editing from the model.
- No legacy HTTP+SSE transport; Streamable HTTP only.
- No sampling, elicitation, roots, completions, logging controls or
  resource subscriptions.
- No automatic reconnect and no replay of a failed tool call.
- No live refresh of the model's tool table when a server changes its
  tool list mid-session. `/mcp tools <server>` re-lists for the operator;
  a disconnect/connect refreshes what the model is offered.
- No Windows-specific process-group guarantees; the stdio transport is
  tested on the platform used by the development environment.

## Tests

`internal/mcp` tests use deterministic local fixtures: an SDK server over
Streamable HTTP with optional authentication, and the test binary
re-executed as a stdio server. They cover validation, schema bounds,
discovery, both transports, structured and error results, output limits,
prompt injection marking, 444 refusal (including a hostile
`readOnlyHint`), 666 execution, revocation during a call, timeouts,
credential handling and redaction, malformed HTTP responses, multiple
servers, name collisions, subprocess cleanup, audit content, and session
evidence after reopening the store (`internal/engine/mcp_test.go`).
