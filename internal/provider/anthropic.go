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

// AnthropicCompatible talks to any Anthropic-style /v1/messages endpoint.
//
// It exists so that HAL's protocol taxonomy can name a wire protocol it
// really speaks. HAL never pretends that an arbitrary API is compatible:
// an endpoint whose wire protocol is not implemented is refused when it is
// selected, with a message that names the supported ones.
//
// The base URL may be given with or without the version prefix: HAL adds
// "/v1" when it is missing, so both "https://api.example.com" and
// "https://api.example.com/v1" reach the same endpoint.
type AnthropicCompatible struct {
	// Endpoint is the API base URL.
	Endpoint string
	// APIKey is the credential. It travels only in the x-api-key header.
	APIKey string
	// Client is the HTTP client. Nil uses a client with the same
	// safeguards as the OpenAI-compatible adapter.
	Client *http.Client
	// DefaultMaxTokens caps one response when the model sets no limit.
	DefaultMaxTokens int
	// IdleTimeout aborts a stream that sends nothing for this long.
	IdleTimeout time.Duration
	// TurnTimeout bounds one whole turn.
	TurnTimeout time.Duration
	// AnthropicVersion is the API version header. Empty uses the version
	// the Messages API is currently documented with.
	AnthropicVersion string
}

// DefaultAnthropicVersion is the API version HAL sends when the endpoint
// names none.
const DefaultAnthropicVersion = "2023-06-01"

// Name implements Provider.
func (a *AnthropicCompatible) Name() string { return "anthropic-compatible" }

func (a *AnthropicCompatible) client() *http.Client {
	if a.Client != nil {
		return a.Client
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

func (a *AnthropicCompatible) maxTokens() int {
	if a.DefaultMaxTokens > 0 {
		return a.DefaultMaxTokens
	}
	return DefaultMaxTokens
}

func (a *AnthropicCompatible) effectiveMaxTokens(model Model) int {
	if model.MaxTokens > 0 {
		return model.MaxTokens
	}
	return a.maxTokens()
}

func (a *AnthropicCompatible) idleTimeout() time.Duration {
	if a.IdleTimeout > 0 {
		return a.IdleTimeout
	}
	return DefaultIdleTimeout
}

func (a *AnthropicCompatible) turnTimeout() time.Duration {
	if a.TurnTimeout > 0 {
		return a.TurnTimeout
	}
	return DefaultTurnTimeout
}

func (a *AnthropicCompatible) turnContext(ctx context.Context) (context.Context, context.CancelFunc) {
	total := a.turnTimeout()
	if total <= 0 {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, total)
}

func (a *AnthropicCompatible) version() string {
	if v := strings.TrimSpace(a.AnthropicVersion); v != "" {
		return v
	}
	return DefaultAnthropicVersion
}

// base returns the API root with the version prefix, so that both
// "https://host" and "https://host/v1" reach "/v1/messages".
func (a *AnthropicCompatible) base() string {
	trimmed := strings.TrimRight(strings.TrimSpace(a.Endpoint), "/")
	if strings.HasSuffix(trimmed, "/v1") {
		return trimmed
	}
	return trimmed + "/v1"
}

// anthropicBlock is one content block of a message.
type anthropicBlock struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
	// Tool use blocks carry the call's identity and its arguments.
	ID    string          `json:"id,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`
	// Tool result blocks carry the outcome of a call.
	ToolUseID string `json:"tool_use_id,omitempty"`
	Content   string `json:"content,omitempty"`
	IsError   bool   `json:"is_error,omitempty"`
}

// anthropicMessage is one message of a conversation.
type anthropicMessage struct {
	Role    string           `json:"role"`
	Content []anthropicBlock `json:"content"`
}

// anthropicTool is one offered function.
type anthropicTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema,omitempty"`
}

// anthropicThinking is the extended-thinking request block.
type anthropicThinking struct {
	Type         string `json:"type"`
	BudgetTokens int    `json:"budget_tokens,omitempty"`
}

// anthropicRequest is one request body.
type anthropicRequest struct {
	Model     string             `json:"model"`
	Messages  []anthropicMessage `json:"messages"`
	System    string             `json:"system,omitempty"`
	MaxTokens int                `json:"max_tokens"`
	Stream    bool               `json:"stream,omitempty"`
	Tools     []anthropicTool    `json:"tools,omitempty"`
	Thinking  *anthropicThinking `json:"thinking,omitempty"`
}

// anthropicUsage is the token accounting Anthropic reports.
type anthropicUsage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
}

// anthropicResponse is one non-streamed answer.
type anthropicResponse struct {
	Content    []anthropicBlock `json:"content"`
	StopReason string           `json:"stop_reason"`
	Usage      *anthropicUsage  `json:"usage"`
	Error      *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

// anthropicMessageStart is the opening frame of a streamed turn.
type anthropicMessageStart struct {
	Usage *anthropicUsage `json:"usage"`
}

// anthropicEvent is one streamed event.
type anthropicEvent struct {
	Type         string                 `json:"type"`
	Message      *anthropicMessageStart `json:"message"`
	ContentBlock *anthropicBlock        `json:"content_block"`
	Index        int                    `json:"index"`
	Delta        *anthropicDelta        `json:"delta"`
	Usage        *anthropicUsage        `json:"usage"`
	StopReason   string                 `json:"stop_reason"`
	Error        *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

// anthropicDelta is the payload of a content-block delta.
type anthropicDelta struct {
	Type        string `json:"type"`
	Text        string `json:"text"`
	Thinking    string `json:"thinking"`
	PartialJSON string `json:"partial_json"`
	StopReason  string `json:"stop_reason"`
}

// anthropicMessages turns the engine's messages into the Anthropic shape.
//
// The system prompt is a top-level field, a tool result is a user message
// carrying a tool_result block, and consecutive messages with the same
// role are merged, because the API expects alternating roles.
func anthropicMessages(messages []Message) (string, []anthropicMessage) {
	var systemParts []string
	var out []anthropicMessage
	appendMessage := func(role string, blocks []anthropicBlock) {
		if len(blocks) == 0 {
			return
		}
		if n := len(out); n > 0 && out[n-1].Role == role {
			out[n-1].Content = append(out[n-1].Content, blocks...)
			return
		}
		out = append(out, anthropicMessage{Role: role, Content: blocks})
	}
	for _, m := range messages {
		switch m.Role {
		case "system":
			if text := strings.TrimSpace(m.Content); text != "" {
				systemParts = append(systemParts, m.Content)
			}
		case "assistant":
			var blocks []anthropicBlock
			if text := m.Content; text != "" {
				blocks = append(blocks, anthropicBlock{Type: "text", Text: text})
			}
			for _, call := range m.ToolCalls {
				input := json.RawMessage(call.ArgumentsJSON())
				if len(input) == 0 {
					input = json.RawMessage("{}")
				}
				blocks = append(blocks, anthropicBlock{
					Type:  "tool_use",
					ID:    call.ID,
					Name:  call.Name,
					Input: input,
				})
			}
			appendMessage("assistant", blocks)
		case "tool":
			appendMessage("user", []anthropicBlock{{
				Type:      "tool_result",
				ToolUseID: m.ToolCallID,
				Content:   m.Content,
			}})
		default:
			appendMessage("user", []anthropicBlock{{Type: "text", Text: m.Content}})
		}
	}
	return strings.Join(systemParts, "\n\n"), out
}

// anthropicTools turns the engine's tools into the Anthropic shape.
func anthropicTools(tools []Tool) []anthropicTool {
	out := make([]anthropicTool, 0, len(tools))
	for _, t := range tools {
		schema := json.RawMessage(`{"type":"object"}`)
		if trimmed := strings.TrimSpace(t.Parameters); trimmed != "" {
			if json.Valid([]byte(trimmed)) {
				schema = json.RawMessage(trimmed)
			}
		}
		out = append(out, anthropicTool{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: schema,
		})
	}
	return out
}

// anthropicThinkingCapable reports whether a model identifier belongs to
// a family that accepts the extended-thinking block.
//
// The table is the adapter's own: HAL only sends a parameter an API
// documents, and an unknown model simply gets no thinking control.
func anthropicThinkingCapable(modelID string) bool {
	id := strings.ToLower(strings.TrimSpace(modelID))
	if !strings.Contains(id, "claude") {
		return false
	}
	if strings.Contains(id, "3-7") || strings.Contains(id, "3.7") {
		return true
	}
	fields := strings.FieldsFunc(id, func(r rune) bool { return r == '-' || r == '.' })
	for i, f := range fields {
		if f == "4" && i > 0 {
			return true
		}
	}
	return false
}

// anthropicEffortBudgets maps a reasoning level onto a thinking budget.
var anthropicEffortBudgets = map[string]int{
	"low":    1024,
	"medium": 4096,
	"high":   8192,
	"xhigh":  16384,
	"max":    32768,
}

// anthropicThinkingFor returns the thinking block for a model, or nil
// when the model does not take one or no level is configured.
func anthropicThinkingFor(model Model) *anthropicThinking {
	if !anthropicThinkingCapable(model.ID) {
		return nil
	}
	level := NormalizeEffort(model.Effort)
	if level == "" {
		return nil
	}
	budget, ok := anthropicEffortBudgets[level]
	if !ok {
		return nil
	}
	return &anthropicThinking{Type: "enabled", BudgetTokens: budget}
}

// requestBody builds one request, optionally offering functions.
func (a *AnthropicCompatible) requestBody(messages []Message, model Model, stream bool, tools []Tool) ([]byte, error) {
	system, conversation := anthropicMessages(messages)
	out := anthropicRequest{
		Model:     model.ID,
		Messages:  conversation,
		System:    system,
		MaxTokens: a.effectiveMaxTokens(model),
		Stream:    stream,
	}
	if thinking := anthropicThinkingFor(model); thinking != nil {
		// The API requires max_tokens to exceed the thinking budget.
		if out.MaxTokens <= thinking.BudgetTokens {
			out.MaxTokens = thinking.BudgetTokens + 1024
		}
		out.Thinking = thinking
	}
	if len(tools) > 0 {
		out.Tools = anthropicTools(tools)
	}
	return json.Marshal(out)
}

func (a *AnthropicCompatible) post(ctx context.Context, body []byte, stream bool) (*http.Response, error) {
	url := a.base() + "/messages"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("provider: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("anthropic-version", a.version())
	if a.APIKey != "" {
		req.Header.Set("x-api-key", a.APIKey)
	}
	if stream {
		req.Header.Set("Accept", "text/event-stream")
	}
	resp, err := a.client().Do(req)
	if err != nil {
		return nil, fmt.Errorf("provider: %w", err)
	}
	return resp, nil
}

// Chat implements Provider with a single non-streaming request.
func (a *AnthropicCompatible) Chat(ctx context.Context, messages []Message, model Model) (Response, error) {
	body, err := a.requestBody(messages, model, false, nil)
	if err != nil {
		return Response{}, err
	}
	turnCtx, cancel := a.turnContext(ctx)
	defer cancel()
	resp, err := a.post(turnCtx, body, false)
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
	var parsed anthropicResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return Response{}, fmt.Errorf("provider: bad response: %w", err)
	}
	if parsed.Error != nil && parsed.Error.Message != "" {
		return Response{}, fmt.Errorf("provider: %s", parsed.Error.Message)
	}
	out := Response{}
	out.Stats.StartedAt = time.Now()
	out.Stats.MaxTokens = a.effectiveMaxTokens(model)
	out.Stats.IdleTimeout = a.idleTimeout()
	out.Stats.TurnTimeout = a.turnTimeout()
	for _, block := range parsed.Content {
		switch block.Type {
		case "text":
			out.Content += block.Text
			out.Stats.VisibleChunks++
			out.Stats.VisibleRunes += len([]rune(block.Text))
		case "thinking":
			out.Reasoning += block.Text
			out.Stats.ReasoningChunks++
			out.Stats.ReasoningRunes += len([]rune(block.Text))
		case "tool_use":
			call := ToolCall{ID: block.ID, Name: block.Name}
			if len(block.Input) > 0 {
				var args map[string]any
				if err := json.Unmarshal(block.Input, &args); err != nil {
					call.Arguments = map[string]any{"_invalid": string(block.Input)}
				} else {
					call.Arguments = args
				}
			}
			out.ToolCalls = append(out.ToolCalls, call)
		}
	}
	out.FinishReason = parsed.StopReason
	out.Stats.FinishReason = parsed.StopReason
	out.Stats.OutputLimitHit = parsed.StopReason == "max_tokens"
	if parsed.Usage != nil {
		out.Stats.HasUsage = true
		out.Stats.Usage = anthropicUsageStats(*parsed.Usage)
	}
	out.Stats.Duration = time.Since(out.Stats.StartedAt)
	out.Stats.Status = StatusFinished
	return out, nil
}

// anthropicUsageStats converts the reported accounting.
func anthropicUsageStats(u anthropicUsage) Usage {
	return Usage{
		PromptTokens:     u.InputTokens,
		CompletionTokens: u.OutputTokens,
		TotalTokens:      u.InputTokens + u.OutputTokens,
		CachedTokens:     u.CacheReadInputTokens,
	}
}

// Models probes the provider's catalogue for the discovery flow.
func (a *AnthropicCompatible) Models(ctx context.Context) ([]string, error) {
	url := a.base() + "/models"
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("provider: %w", err)
	}
	if a.APIKey != "" {
		req.Header.Set("x-api-key", a.APIKey)
		req.Header.Set("anthropic-version", a.version())
	}
	resp, err := a.client().Do(req)
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
func (a *AnthropicCompatible) ChatStream(ctx context.Context, messages []Message, model Model, emit func(Delta) error) (Response, error) {
	return a.chatStream(ctx, messages, model, nil, emit)
}

// ChatStreamTools implements ToolStreamer.
func (a *AnthropicCompatible) ChatStreamTools(ctx context.Context, messages []Message, model Model, tools []Tool, emit func(Delta) error) (Response, error) {
	return a.chatStream(ctx, messages, model, tools, emit)
}

// anthropicToolCall accumulates one streamed tool call.
type anthropicToolCall struct {
	ID   string
	Name string
	JSON string
}

func (a *AnthropicCompatible) chatStream(ctx context.Context, messages []Message, model Model, tools []Tool, emit func(Delta) error) (Response, error) {
	body, err := a.requestBody(messages, model, true, tools)
	if err != nil {
		return Response{}, err
	}
	turnCtx, cancel := a.turnContext(ctx)
	defer cancel()
	resp, err := a.post(turnCtx, body, true)
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
		MaxTokens:   a.effectiveMaxTokens(model),
		IdleTimeout: a.idleTimeout(),
		TurnTimeout: a.turnTimeout(),
	}
	watch := newIdleWatch(cancel, a.idleTimeout())
	defer watch.stop()
	stream := &watchedBody{body: resp.Body, watch: watch}

	var out Response
	calls := map[int]*anthropicToolCall{}
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
			stats.KeepAlives++
			continue
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" || data == "[DONE]" {
			continue
		}
		stats.Events++
		var event anthropicEvent
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			stats.Malformed++
			continue
		}
		if event.Error != nil && event.Error.Message != "" {
			stats.Status = StatusError
			stats.Duration = time.Since(stats.StartedAt)
			out.Stats = stats
			return out, fmt.Errorf("provider: %s", event.Error.Message)
		}
		switch event.Type {
		case "message_start":
			if event.Message != nil && event.Message.Usage != nil {
				stats.HasUsage = true
				stats.Usage = anthropicUsageStats(*event.Message.Usage)
			}
		case "content_block_start":
			if event.ContentBlock != nil && event.ContentBlock.Type == "tool_use" {
				calls[event.Index] = &anthropicToolCall{ID: event.ContentBlock.ID, Name: event.ContentBlock.Name}
			}
		case "content_block_delta":
			if event.Delta == nil {
				continue
			}
			switch event.Delta.Type {
			case "text_delta":
				if event.Delta.Text == "" {
					continue
				}
				out.Content += event.Delta.Text
				stats.VisibleChunks++
				stats.VisibleRunes += len([]rune(event.Delta.Text))
				if stats.FirstVisible == 0 {
					stats.FirstVisible = time.Since(stats.StartedAt)
				}
				if emit != nil {
					if err := emit(Delta{Text: event.Delta.Text}); err != nil {
						stats.Status = StatusCancelled
						stats.Duration = time.Since(stats.StartedAt)
						out.Stats = stats
						return out, err
					}
				}
			case "thinking_delta":
				if event.Delta.Thinking == "" {
					continue
				}
				out.Reasoning += event.Delta.Thinking
				stats.ReasoningChunks++
				stats.ReasoningRunes += len([]rune(event.Delta.Thinking))
				if stats.FirstReasoning == 0 {
					stats.FirstReasoning = time.Since(stats.StartedAt)
				}
				if emit != nil {
					if err := emit(Delta{Reasoning: event.Delta.Thinking}); err != nil {
						stats.Status = StatusCancelled
						stats.Duration = time.Since(stats.StartedAt)
						out.Stats = stats
						return out, err
					}
				}
			case "input_json_delta":
				if call, ok := calls[event.Index]; ok {
					call.JSON += event.Delta.PartialJSON
				}
			}
		case "message_delta":
			if event.Usage != nil {
				stats.HasUsage = true
				usage := anthropicUsageStats(*event.Usage)
				// The closing frame reports only the output tokens.
				// The prompt tokens come from the opening frame, so
				// they are carried over rather than lost.
				if usage.PromptTokens == 0 {
					usage.PromptTokens = stats.Usage.PromptTokens
				}
				if usage.CachedTokens == 0 {
					usage.CachedTokens = stats.Usage.CachedTokens
				}
				usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens
				stats.Usage = usage
			}
			reason := event.StopReason
			if event.Delta != nil && event.Delta.StopReason != "" {
				reason = event.Delta.StopReason
			}
			if reason != "" {
				out.FinishReason = reason
				stats.FinishReason = reason
				stats.OutputLimitHit = reason == "max_tokens"
				finished = true
				watch.grace()
			}
		case "message_stop":
			finished = true
			watch.grace()
		}
	}
	for _, call := range calls {
		if call == nil || strings.TrimSpace(call.Name) == "" {
			continue
		}
		toolCall := ToolCall{ID: call.ID, Name: call.Name}
		if raw := strings.TrimSpace(call.JSON); raw != "" {
			var args map[string]any
			if err := json.Unmarshal([]byte(raw), &args); err != nil {
				toolCall.Arguments = map[string]any{"_invalid": raw}
			} else {
				toolCall.Arguments = args
			}
		}
		out.ToolCalls = append(out.ToolCalls, toolCall)
	}
	stats.Duration = time.Since(stats.StartedAt)
	if err := scanner.Err(); err != nil {
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
