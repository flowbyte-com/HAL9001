package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// Delta is one streaming unit. Text is user-facing content; Reasoning is
// provider thinking metadata (for example a reasoning_content field) and
// must never be displayed as the answer.
type Delta struct {
	Text         string
	Reasoning    string
	FinishReason string
	Done         bool
}

// Streamer is optionally implemented by providers with SSE support.
type Streamer interface {
	ChatStream(ctx context.Context, messages []Message, model Model, emit func(Delta) error) (Response, error)
}

// ToolStreamer is optionally implemented by providers that can offer
// structured functions and report the calls a model makes.
//
// Tool calls are proposals, never authority: the adapter only reports what
// the model asked for, and the engine decides whether it may even be
// prepared. A provider that does not implement this interface simply has
// no tool-call capability, and the conversation continues without one.
type ToolStreamer interface {
	ChatStreamTools(ctx context.Context, messages []Message, model Model, tools []Tool, emit func(Delta) error) (Response, error)
}

// OpenAICompatible talks to any OpenAI-style /chat/completions endpoint
// (custom base URL, bearer key, model id). It sends no reasoning or
// thinking parameters: adapters must only use parameters the API
// documents, and thinking controls are out of scope for this milestone.
type OpenAICompatible struct {
	Endpoint string
	APIKey   string
	Client   *http.Client
	// DefaultMaxTokens caps one response when the model sets no limit.
	// Zero uses DefaultMaxTokens.
	DefaultMaxTokens int
	// IdleTimeout aborts a stream that sends nothing for this long.
	// Zero uses DefaultIdleTimeout.
	IdleTimeout time.Duration
	// TurnTimeout bounds one whole turn. Zero uses DefaultTurnTimeout.
	TurnTimeout time.Duration
}

// Name implements Provider.
func (o *OpenAICompatible) Name() string { return "openai-compatible" }

// client returns the HTTP client. There is deliberately no overall
// Client.Timeout: a legitimate long answer must not be cut mid-sentence.
// The turn ceiling and the idle watchdog bound the request instead, and
// both close the connection when they fire.
func (o *OpenAICompatible) client() *http.Client {
	if o.Client != nil {
		return o.Client
	}
	return &http.Client{
		Transport: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			DialContext:           (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
			TLSHandshakeTimeout:   15 * time.Second,
			ResponseHeaderTimeout: 60 * time.Second,
			ExpectContinueTimeout: time.Second,
			ForceAttemptHTTP2:     true,
		},
	}
}

type openAIMessage struct {
	Role       string           `json:"role"`
	Content    string           `json:"content"`
	ToolCalls  []openAIToolCall `json:"tool_calls,omitempty"`
	ToolCallID string           `json:"tool_call_id,omitempty"`
	Name       string           `json:"name,omitempty"`
}

// openAITool is the wire form of one offered function.
type openAITool struct {
	Type     string `json:"type"`
	Function struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		Parameters  json.RawMessage `json:"parameters,omitempty"`
	} `json:"function"`
}

// openAIToolCall is one call as the provider reports it. Streaming
// providers send it in pieces: Index identifies the call, and Name and
// Arguments arrive split across chunks.
type openAIToolCall struct {
	Index    *int   `json:"index,omitempty"`
	ID       string `json:"id,omitempty"`
	Type     string `json:"type,omitempty"`
	Function struct {
		Name      string `json:"name,omitempty"`
		Arguments string `json:"arguments,omitempty"`
	} `json:"function"`
}

type openAIRequest struct {
	Model     string          `json:"model"`
	Messages  []openAIMessage `json:"messages"`
	Stream    bool            `json:"stream"`
	MaxTokens *int            `json:"max_tokens,omitempty"`
	// ReasoningEffort is sent only for models whose API documents it.
	// The adapter never guesses: see EffortCapable.
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
	// StreamOptions asks the provider to report token usage on the
	// stream. Providers that do not understand the field ignore it;
	// without it most providers never send usage at all.
	StreamOptions *streamOptions `json:"stream_options,omitempty"`
	// Tools offers functions to the model. Only the engine's single
	// proposal operation is ever offered, and a call carries no
	// authority: it is reviewed before anything happens.
	Tools []openAITool `json:"tools,omitempty"`
}

type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type openAIChoice struct {
	Delta *struct {
		Content          *string          `json:"content"`
		ReasoningContent *string          `json:"reasoning_content"`
		ToolCalls        []openAIToolCall `json:"tool_calls,omitempty"`
	} `json:"delta,omitempty"`
	Message *struct {
		Content          *string          `json:"content"`
		ReasoningContent *string          `json:"reasoning_content"`
		ToolCalls        []openAIToolCall `json:"tool_calls,omitempty"`
	} `json:"message,omitempty"`
	FinishReason *string `json:"finish_reason,omitempty"`
}

type openAIResponse struct {
	Choices []openAIChoice `json:"choices"`
	// Usage is sent by providers in the final chunk (or in a chunk with
	// no choices at all). It is the only reliable token accounting we get.
	Usage *struct {
		PromptTokens            int `json:"prompt_tokens"`
		CompletionTokens        int `json:"completion_tokens"`
		TotalTokens             int `json:"total_tokens"`
		CompletionTokensDetails *struct {
			ReasoningTokens int `json:"reasoning_tokens"`
		} `json:"completion_tokens_details"`
		PromptTokensDetails *struct {
			CachedTokens int `json:"cached_tokens"`
		} `json:"prompt_tokens_details"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    any    `json:"code"`
	} `json:"error,omitempty"`
}

func (o *OpenAICompatible) requestBody(messages []Message, model Model, stream bool) ([]byte, error) {
	return o.requestBodyTools(messages, model, stream, nil)
}

// requestBodyTools builds one request, optionally offering functions.
func (o *OpenAICompatible) requestBodyTools(messages []Message, model Model, stream bool, tools []Tool) ([]byte, error) {
	out := openAIRequest{Model: model.ID, Stream: stream}
	for _, m := range messages {
		wire := openAIMessage{Role: m.Role, Content: m.Content,
			ToolCallID: m.ToolCallID, Name: m.Name}
		for _, call := range m.ToolCalls {
			var tc openAIToolCall
			tc.ID = call.ID
			tc.Type = "function"
			tc.Function.Name = call.Name
			tc.Function.Arguments = call.ArgumentsJSON()
			wire.ToolCalls = append(wire.ToolCalls, tc)
		}
		out.Messages = append(out.Messages, wire)
	}
	maxTokens := model.MaxTokens
	if maxTokens <= 0 {
		maxTokens = o.maxTokens()
	}
	if maxTokens > 0 {
		out.MaxTokens = &maxTokens
	}
	// Capability first: a level is only ever sent to a model whose API
	// documents reasoning_effort.
	if EffortCapable(model.ID) {
		out.ReasoningEffort = NormalizeEffort(model.Effort)
	}
	if stream {
		out.StreamOptions = &streamOptions{IncludeUsage: true}
	}
	for _, tool := range tools {
		wire := openAITool{Type: "function"}
		wire.Function.Name = tool.Name
		wire.Function.Description = tool.Description
		if tool.Parameters != "" {
			wire.Function.Parameters = json.RawMessage(tool.Parameters)
		}
		out.Tools = append(out.Tools, wire)
	}
	return json.Marshal(out)
}

// toolCallsFrom converts the provider's calls into the engine's form.
func toolCallsFrom(calls []openAIToolCall) []ToolCall {
	out := make([]ToolCall, 0, len(calls))
	for _, c := range calls {
		if strings.TrimSpace(c.Function.Name) == "" {
			continue
		}
		call := ToolCall{ID: c.ID, Name: c.Function.Name}
		if raw := strings.TrimSpace(c.Function.Arguments); raw != "" {
			var args map[string]any
			if err := json.Unmarshal([]byte(raw), &args); err != nil {
				// An unparseable call is still reported: the engine
				// refuses it rather than pretending it never happened.
				call.Arguments = map[string]any{"_invalid": raw}
			} else {
				call.Arguments = args
			}
		}
		out = append(out, call)
	}
	return out
}

// toolCallSet accumulates streamed tool calls by index.
type toolCallSet struct {
	calls []openAIToolCall
}

func (s *toolCallSet) add(deltas []openAIToolCall) {
	for _, d := range deltas {
		idx := len(s.calls)
		if d.Index != nil {
			idx = *d.Index
		}
		if idx < 0 {
			continue
		}
		for len(s.calls) <= idx {
			s.calls = append(s.calls, openAIToolCall{})
		}
		call := &s.calls[idx]
		if d.ID != "" {
			call.ID = d.ID
		}
		if d.Type != "" {
			call.Type = d.Type
		}
		call.Function.Name += d.Function.Name
		call.Function.Arguments += d.Function.Arguments
	}
}

func (s *toolCallSet) list() []openAIToolCall {
	return s.calls
}

func (o *OpenAICompatible) post(ctx context.Context, body []byte, stream bool) (*http.Response, error) {
	url := strings.TrimRight(o.Endpoint, "/") + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("provider: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if o.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+o.APIKey)
	}
	if stream {
		req.Header.Set("Accept", "text/event-stream")
	}
	resp, err := o.client().Do(req)
	if err != nil {
		return nil, fmt.Errorf("provider: %w", err)
	}
	return resp, nil
}

// readErrorBody returns a bounded, credential-free error description and
// closes the body.
func readErrorBody(resp *http.Response) string {
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	var parsed openAIResponse
	if err := json.Unmarshal(raw, &parsed); err == nil && parsed.Error != nil && parsed.Error.Message != "" {
		return fmt.Sprintf("HTTP %d: %s", resp.StatusCode, parsed.Error.Message)
	}
	text := strings.TrimSpace(string(raw))
	if text == "" {
		text = resp.Status
	}
	if len(text) > 300 {
		text = text[:300] + "…"
	}
	return fmt.Sprintf("HTTP %d: %s", resp.StatusCode, text)
}

// Chat implements Provider with a single non-streaming request.
func (o *OpenAICompatible) Chat(ctx context.Context, messages []Message, model Model) (Response, error) {
	body, err := o.requestBody(messages, model, false)
	if err != nil {
		return Response{}, err
	}
	turnCtx, cancel := o.turnContext(ctx)
	defer cancel()
	resp, err := o.post(turnCtx, body, false)
	if err != nil {
		return Response{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return Response{}, fmt.Errorf("provider: authentication failed (%d): check the API key", resp.StatusCode)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Response{}, fmt.Errorf("provider: %s", readErrorBody(resp))
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return Response{}, fmt.Errorf("provider: %w", err)
	}
	var parsed openAIResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return Response{}, fmt.Errorf("provider: bad response: %w", err)
	}
	if parsed.Error != nil && parsed.Error.Message != "" {
		return Response{}, fmt.Errorf("provider: %s", parsed.Error.Message)
	}
	if len(parsed.Choices) == 0 {
		return Response{}, fmt.Errorf("provider: empty response (no choices)")
	}
	out := Response{}
	out.Stats.StartedAt = time.Now()
	out.Stats.MaxTokens = o.effectiveMaxTokens(model)
	out.Stats.IdleTimeout = o.idleTimeout()
	out.Stats.TurnTimeout = o.turnTimeout()
	if parsed.Choices[0].Message != nil {
		if parsed.Choices[0].Message.Content != nil {
			out.Content = *parsed.Choices[0].Message.Content
		}
		if parsed.Choices[0].Message.ReasoningContent != nil {
			out.Reasoning = *parsed.Choices[0].Message.ReasoningContent
		}
		out.ToolCalls = toolCallsFrom(parsed.Choices[0].Message.ToolCalls)
	}
	if parsed.Choices[0].FinishReason != nil {
		out.FinishReason = *parsed.Choices[0].FinishReason
		out.Stats.FinishReason = *parsed.Choices[0].FinishReason
		out.Stats.OutputLimitHit = *parsed.Choices[0].FinishReason == "length"
	}
	if u := parsed.Usage; u != nil {
		out.Stats.HasUsage = true
		out.Stats.Usage = Usage{
			PromptTokens:     u.PromptTokens,
			CompletionTokens: u.CompletionTokens,
			TotalTokens:      u.TotalTokens,
		}
		if u.CompletionTokensDetails != nil {
			out.Stats.Usage.ReasoningTokens = u.CompletionTokensDetails.ReasoningTokens
		}
		if u.PromptTokensDetails != nil {
			out.Stats.Usage.CachedTokens = u.PromptTokensDetails.CachedTokens
		}
	}
	out.Stats.VisibleChunks = 1
	out.Stats.VisibleRunes = len([]rune(out.Content))
	out.Stats.Status = StatusFinished
	return out, nil
}

// Models probes the provider's model catalogue with a short timeout. It
// powers the onboarding connection test: success means the endpoint and
// key work, and the count tells the operator models are reachable. The
// key travels only in the Authorization header, never into errors or logs.
func (o *OpenAICompatible) Models(ctx context.Context) ([]string, error) {
	url := strings.TrimRight(o.Endpoint, "/") + "/models"
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("provider: %w", err)
	}
	if o.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+o.APIKey)
	}
	resp, err := o.client().Do(req)
	if err != nil {
		return nil, fmt.Errorf("provider: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("provider: authentication failed (%d): check the API key", resp.StatusCode)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("provider: %s", readErrorBody(resp))
	}
	var listing struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("provider: %w", err)
	}
	if err := json.Unmarshal(raw, &listing); err != nil {
		return nil, fmt.Errorf("provider: bad model list: %w", err)
	}
	ids := make([]string, 0, len(listing.Data))
	for _, m := range listing.Data {
		if strings.TrimSpace(m.ID) != "" {
			ids = append(ids, m.ID)
		}
	}
	return ids, nil
}

// ChatStream implements Streamer using server-sent events.
//
// One turn is bounded three ways: the caller's context (cancellation), an
// idle watchdog (no data for IdleTimeout) and a total ceiling
// (TurnTimeout). Every bound closes the HTTP request, so an abandoned
// turn stops consuming tokens instead of silently running on.
func (o *OpenAICompatible) ChatStream(ctx context.Context, messages []Message, model Model, emit func(Delta) error) (Response, error) {
	return o.chatStream(ctx, messages, model, nil, emit)
}

// ChatStreamTools implements ToolStreamer: the same streamed turn, with
// function definitions offered to the model. Any call the model makes is
// reported in Response.ToolCalls; nothing is executed here.
func (o *OpenAICompatible) ChatStreamTools(ctx context.Context, messages []Message, model Model, tools []Tool, emit func(Delta) error) (Response, error) {
	return o.chatStream(ctx, messages, model, tools, emit)
}

func (o *OpenAICompatible) chatStream(ctx context.Context, messages []Message, model Model, tools []Tool, emit func(Delta) error) (Response, error) {
	body, err := o.requestBodyTools(messages, model, true, tools)
	if err != nil {
		return Response{}, err
	}
	turnCtx, cancel := o.turnContext(ctx)
	defer cancel()
	resp, err := o.post(turnCtx, body, true)
	if err != nil {
		return Response{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return Response{}, fmt.Errorf("provider: authentication failed (%d): check the API key", resp.StatusCode)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Response{}, fmt.Errorf("provider: %s", readErrorBody(resp))
	}

	stats := TurnStats{
		StartedAt:   time.Now(),
		MaxTokens:   o.effectiveMaxTokens(model),
		IdleTimeout: o.idleTimeout(),
		TurnTimeout: o.turnTimeout(),
	}
	watch := newIdleWatch(cancel, o.idleTimeout())
	defer watch.stop()
	stream := &watchedBody{body: resp.Body, watch: watch}

	var out Response
	var calls toolCallSet
	scanner := bufio.NewScanner(stream)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	finished := false
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			stats.Status = StatusCancelled
			stats.Duration = time.Since(stats.StartedAt)
			out.Stats = stats
			return out, fmt.Errorf("provider: %w", err)
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, ":") {
			// SSE comment, typically a keep-alive. It proves the
			// connection is alive but says nothing about progress.
			stats.KeepAlives++
			continue
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			stats.Status = StatusDone
			break
		}
		stats.Events++
		var chunk openAIResponse
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			// Keep-alive noise and provider-specific frames are not
			// fatal; count them so diagnostics show the stream shape.
			stats.Malformed++
			continue
		}
		if chunk.Error != nil && chunk.Error.Message != "" {
			stats.Status = StatusError
			stats.Duration = time.Since(stats.StartedAt)
			out.Stats = stats
			return out, fmt.Errorf("provider: %s", chunk.Error.Message)
		}
		// Usage often arrives in a chunk with no choices at all, so it
		// must be read before the choice guard below.
		if u := chunk.Usage; u != nil {
			stats.HasUsage = true
			stats.Usage = Usage{
				PromptTokens:     u.PromptTokens,
				CompletionTokens: u.CompletionTokens,
				TotalTokens:      u.TotalTokens,
			}
			if u.CompletionTokensDetails != nil {
				stats.Usage.ReasoningTokens = u.CompletionTokensDetails.ReasoningTokens
			}
			if u.PromptTokensDetails != nil {
				stats.Usage.CachedTokens = u.PromptTokensDetails.CachedTokens
			}
		}
		if len(chunk.Choices) == 0 || chunk.Choices[0].Delta == nil {
			continue
		}
		if tc := chunk.Choices[0].Delta.ToolCalls; len(tc) > 0 {
			calls.add(tc)
		}
		delta := Delta{}
		if c := chunk.Choices[0].Delta.Content; c != nil && *c != "" {
			delta.Text = *c
			out.Content += *c
			stats.VisibleChunks++
			stats.VisibleRunes += len([]rune(*c))
			if stats.FirstVisible == 0 {
				stats.FirstVisible = time.Since(stats.StartedAt)
			}
		}
		if r := chunk.Choices[0].Delta.ReasoningContent; r != nil && *r != "" {
			delta.Reasoning = *r
			out.Reasoning += *r
			stats.ReasoningChunks++
			stats.ReasoningRunes += len([]rune(*r))
			if stats.FirstReasoning == 0 {
				stats.FirstReasoning = time.Since(stats.StartedAt)
			}
		}
		if chunk.Choices[0].FinishReason != nil {
			delta.FinishReason = *chunk.Choices[0].FinishReason
			out.FinishReason = *chunk.Choices[0].FinishReason
			stats.FinishReason = *chunk.Choices[0].FinishReason
			stats.OutputLimitHit = *chunk.Choices[0].FinishReason == "length"
			finished = true
			// Only a usage chunk or [DONE] is expected now; do not wait
			// out the full idle window for a provider that never closes.
			watch.grace()
		}
		if emit != nil && (delta.Text != "" || delta.Reasoning != "") {
			if err := emit(delta); err != nil {
				stats.Status = StatusCancelled
				stats.Duration = time.Since(stats.StartedAt)
				out.Stats = stats
				return out, err
			}
		}
	}
	out.ToolCalls = toolCallsFrom(calls.list())
	stats.Duration = time.Since(stats.StartedAt)
	if err := scanner.Err(); err != nil {
		// A provider that keeps the socket open after finish_reason is
		// finished, not stalled: the answer is complete. The grace
		// window only exists so a late usage chunk or [DONE] is not
		// missed.
		if finished && watch.fired.Load() {
			stats.Status = StatusFinished
			out.Stats = stats
			return out, nil
		}
		stats.Status = streamStatus(ctx, turnCtx, watch)
		out.Stats = stats
		return out, streamError(ctx, turnCtx, watch, err)
	}
	if stats.Status == "" {
		if finished {
			stats.Status = StatusFinished
		} else {
			stats.Status = StatusEOF
		}
	}
	out.Stats = stats
	return out, nil
}
