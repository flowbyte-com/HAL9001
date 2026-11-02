# Streaming Investigation: Long Prompts and Silent Turns

Two problems were reported from a real MiniMax-M3.1-Flash session:

- **A.** A long user prompt filled the conversation viewport.
- **B.** A lengthy prompt showed `streaming…` for about five minutes with no
  visible answer, and the operator saw a significant MiniMax usage increase.

This document separates what the code and deterministic tests **prove**
from what remains a **hypothesis**. No live API quota was spent: every
stream below is a synthetic MiniMax-style SSE stream served by
`httptest`.

## A. Long prompts dominated the viewport

**Root cause.** Every user message was rendered in full: the conversation
model stored one string per turn (`"You > " + text`) and the viewport
displayed it verbatim, soft-wrapped. Nothing collapsed it, so a pasted
page of prose occupied the whole conversation area and pushed HAL's
answer out of view.

**Fix.** User messages are now conversation entries with a cached
rendered form. A message that renders to more than six rows is shown as
four preview rows plus a hint (`… 16 more line(s) · Ctrl+O to expand`).
`Ctrl+O` toggles the most recent prompt; `/expand` and `/collapse` apply
to every long prompt. The collapse is **rendering only**: the entry keeps
the full text, `Lines()` exposes it, the session store writes it
unchanged, and the engine sends it to the provider unchanged
(`TestLongPromptReachesProviderUnchanged`). Streaming still follows the
reply when the reader was already at the bottom.

## B. Silent streaming and the usage spike

### Root causes established from the code

1. **Reasoning was invisible by design.** `ChatStream` parsed
   `reasoning_content` into `Delta.Reasoning` and `Response.Reasoning`,
   but the engine's callback forwarded only `d.Text`:

   ```go
   streamer.ChatStream(..., func(d provider.Delta) error {
       if d.Text != "" { emit(d.Text) }
       return nil
   })
   ```

   A model that spends its first minutes emitting reasoning produces no
   `Text`, so the interface had **no signal at all** that work was in
   progress. The label stayed `streaming…`, unchanged, for as long as
   the reasoning phase lasted. This is the direct cause of "silent
   streaming" from the operator's point of view.

2. **No output cap was ever sent.** `requestBody` set `max_tokens` only
   when `Model.MaxTokens > 0`, and nothing in the application set it:
   `ModelRecord` has only `ID` and `Label`, and `modelFor()` returned
   `provider.Model{ID: id}`. Every request therefore omitted the cap and
   the **provider default** governed generation. This was the only lever
   the application had over response length, and it was unused.

3. **No token accounting.** The SSE `usage` object was not parsed, so the
   application could not show or record consumption.

4. **`finish_reason` was recorded but ignored.** A `length` finish (the
   provider truncated the reply at the output cap) looked exactly like a
   normal `stop`: the reply simply ended.

5. **One blunt timeout.** The only bound was
   `http.Client{Timeout: 120 * time.Second}`, which applies to the whole
   body read. It cut **any** turn at two minutes, including a legitimate
   long answer, and reported a generic deadline error. Every cut turn
   had already been billed for the tokens it generated before the cut.

6. **No idle detection.** A stream that sent nothing (or only
   keep-alives) was indistinguishable from one that was working until the
   total timeout fired.

### Measured, not assumed

`http.Client.Timeout` really does interrupt a streaming body — a
timeout applies to the whole body read, not just the headers. The old
code relied on exactly that, which is why a single turn could not exceed
120 s. The new turn ceiling is tested the same way
(`TestChatStreamTurnTimeoutWhileTrickling` and
`TestTurnTimeoutDuringReasoningTrickle`): a stream that keeps sending
content or reasoning but never finishes is cut by the turn limit while
the idle watchdog never fires.

**Consequence:** in this build a single turn could not have displayed
`streaming…` for five minutes. The hard ceiling was 120 s. The observed
duration therefore cannot be explained by one uninterrupted request in
this code.

### Hypotheses (not proven)

- **Repeated attempts.** A plausible sequence is: the prompt is sent, the
  reasoning phase produces no visible output, the 120 s client timeout
  cuts the turn, the operator sends it again, and again. Each attempt
  pays for the reasoning generated before the cut. Two or three attempts
  would look like roughly five minutes of "streaming…" and would multiply
  usage. We could not inspect the operator's session file or billing
  dashboard, so this remains a hypothesis.
- **Reasoning counted against the cap.** MiniMax-M3.1-Flash may count
  reasoning tokens toward `max_tokens`. With no cap sent, the provider
  default allowed a long reasoning phase to bill a large number of
  tokens. Consistent with the observed effect, not verified against the
  provider.
- **Reasoning preceding content.** The provider may emit
  `reasoning_content` for a long time before any `content`. Our synthetic
  streams model that shape; we did not observe the live stream.

**This is not evidence of a provider defect.** The code discarded the
progress signal and sent no output limit. Those are defects on our side.

## What changed

### Diagnostics (content-free)

`provider.TurnStats` records, per turn: status, elapsed time, time to
first visible output, time to first reasoning event, SSE event count,
keep-alive count, malformed count, visible chunks and runes, reasoning
chunks and runes (**counts only**), `finish_reason`, output-limit flag,
token usage (prompt/completion/total/reasoning), and the limits that
applied. It contains no prompt, reply or reasoning text and no
credentials.

`/diag` shows the last turn's statistics. A failed turn reports them too,
so a stall can be explained after the fact.

### Safeguards (configurable)

| Setting | Default | Flag | Environment |
|---|---|---|---|
| Output cap | 8192 tokens | `--max-tokens` | `HAL9001_MAX_TOKENS` |
| Idle timeout | 45 s | `--stream-idle-timeout` | `HAL9001_STREAM_IDLE_TIMEOUT` |
| Turn ceiling | 2 min | `--turn-timeout` | `HAL9001_TURN_TIMEOUT` |

They are also persisted in the user configuration file as
`max_tokens`, `stream_idle_timeout_seconds` and
`turn_timeout_seconds`. Zero means "use the built-in default"; a
malformed environment value is ignored rather than silently disabling
the safeguard.

These are the operator's chosen defaults: a bounded reply, a stall
detected in under a minute, and a hard two-minute ceiling. An explicit
flag, environment value or configuration entry overrides any of them.

The idle window resets on every chunk the provider sends, so a slow but
active stream is never cut. The turn ceiling is absolute: a reasoning
phase that keeps emitting events is still stopped at two minutes, which
is the behaviour `TestTurnTimeoutDuringReasoningTrickle` pins down. After
`finish_reason`, the idle window shrinks to three seconds so a provider
that keeps the socket open does not hold the turn.

The old implementation also had a 120 s bound, but it was a blunt
`http.Client.Timeout` over the whole body read with no progress signal,
no idle distinction and a generic error. The new ceiling is the same
number with different machinery: it is paired with a 45 s idle
watchdog, a visible elapsed-time indicator and an explanation when it
fires. The number matching is coincidence, not a restoration.

### Visible progress

While a turn runs, the notice shows the phase and elapsed time:
`waiting for the model… 2s`, `thinking… 47s`, `streaming… 51s`, each with
`Esc cancels`. The thinking phase is driven by the presence of reasoning
events; **the reasoning text is never displayed, stored or logged**.

### Cancellation and turn isolation

Cancelling closes the HTTP request through the request context. Each turn
now carries a generation id; messages from a cancelled turn are ignored,
so a late error can no longer clear or cancel the next turn.

### Completion handling

- `finish_reason: length` produces an explicit notice and a `/diag` flag.
- A stream that ends without `[DONE]` is reported as `eof`, not a silent
  success.
- A reply that is entirely reasoning is reported as
  `(the model produced reasoning but no answer)` instead of
  `(no response)`.

## Tests

`internal/provider/stream_test.go` covers, against synthetic SSE:
reasoning-then-content with usage, `finish_reason: length`, EOF without
`[DONE]`, malformed chunks, idle timeout, context cancellation closing
the request, the finish grace, and the output cap on every request.
`internal/engine/engine_test.go` covers reasoning presence without text,
stats delivery, the reasoning-only reply, and full prompt preservation.
`internal/tui/tui_test.go` covers collapsing, expansion, the thinking
indicator, elapsed time, stale-turn isolation, `/diag`, the output-limit
notice, the idle-stall notice, and auto-follow.

## Evidence limits

- No live MiniMax call was made for this milestone; the provider's exact
  reasoning/usage behaviour is inferred from the API shape we parse.
- The five-minute figure cannot come from a single request in this build;
  the repeated-attempt explanation is plausible but unproven.
- Token totals appear only when the provider sends them; absent usage is
  shown as "not reported by the provider", never invented.
