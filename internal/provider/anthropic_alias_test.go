package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// The Messages wire, and the two behaviours it has.
//
// The DeepSeek-compatible endpoint at https://api.deepseek.com/anthropic
// enables thinking when the request does not say otherwise, and it
// accepts the Claude model names as aliases. The genuine Messages API
// thinks only when the request asks for it.
//
// A model name alone does not say which of the two is on the other end
// of the wire: "claude-sonnet-4-5" is a Claude model and a DeepSeek
// alias. What says it is the answer the provider gives: a request that
// said nothing about thinking and came back with thinking blocks is a
// provider that thinks by default.
//
// The mock below is strict. It enforces the documented rules on the
// exact payload and answers 400 when they are broken, so a replay that
// leaves the blocks out fails rather than passes quietly.

// anthropicEndpoint is a mock Messages endpoint.
//
// It says which of the two behaviours it has, and it keeps the rules of
// that one.
type anthropicEndpoint struct {
	t *testing.T
	// deepSeek reports that this endpoint thinks by default: a request
	// that says nothing about thinking still comes back with thinking
	// blocks.
	deepSeek bool
	// signed reports that this endpoint signs its thinking blocks.
	signed bool
	// steps are the answers the endpoint gives, one per request. A step
	// with a tool name makes the model call that tool.
	steps []string
	// requests are the bodies the endpoint received.
	requests []map[string]any
	// violations counts the requests the endpoint refused.
	violations int
	mu         sync.Mutex
}

// ServeHTTP implements the mock endpoint.
func (e *anthropicEndpoint) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		e.t.Errorf("the request was not JSON: %v", err)
	}
	e.mu.Lock()
	e.requests = append(e.requests, body)
	step := ""
	if len(e.steps) > 0 {
		step = e.steps[0]
		e.steps = e.steps[1:]
	}
	e.mu.Unlock()

	mode := e.mode(body)
	if problem := e.check(body, mode); problem != "" {
		e.mu.Lock()
		e.violations++
		e.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"invalid_request_error","message":"` +
			problem + `"}}`))
		return
	}
	answer := e.answer(step, mode)
	if stream, _ := body["stream"].(bool); stream {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(streamOf(e.t, answer)))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(answer))
}

// mode reports whether this request is in thinking mode.
func (e *anthropicEndpoint) mode(body map[string]any) string {
	if block, ok := body["thinking"].(map[string]any); ok {
		switch block["type"] {
		case "enabled":
			return "on"
		case "disabled":
			return "off"
		}
	}
	if e.deepSeek {
		// The endpoint enables thinking when the request says nothing.
		return "on"
	}
	return "off"
}

// answer builds the response this endpoint gives.
func (e *anthropicEndpoint) answer(step, mode string) string {
	content := []map[string]any{}
	if mode == "on" {
		block := map[string]any{"type": "thinking", "thinking": "the model weighed it"}
		if e.signed {
			block["signature"] = "sig-abc"
		}
		content = append(content, block)
	}
	stop := "end_turn"
	if step != "" {
		content = append(content, map[string]any{
			"type": "tool_use", "id": "call_" + step, "name": step,
			"input": map[string]any{},
		})
		stop = "tool_use"
	} else {
		content = append(content, map[string]any{"type": "text", "text": "the answer"})
	}
	raw, _ := json.Marshal(map[string]any{
		"id": "msg_1", "type": "message", "role": "assistant",
		"content": content, "model": "the-model", "stop_reason": stop,
	})
	return string(raw)
}

// check reports what is wrong with a request, or the empty string when
// the request breaks no rule.
func (e *anthropicEndpoint) check(body map[string]any, mode string) string {
	messages, _ := body["messages"].([]any)
	for i, raw := range messages {
		msg, _ := raw.(map[string]any)
		if msg["role"] != "assistant" {
			continue
		}
		blocks, _ := msg["content"].([]any)
		var hasThinking, hasTool bool
		sawOther := false
		for _, rawBlock := range blocks {
			block, _ := rawBlock.(map[string]any)
			switch block["type"] {
			case "thinking":
				if sawOther {
					return "a thinking block came after the text"
				}
				hasThinking = true
			case "text":
				sawOther = true
			case "tool_use":
				sawOther = true
				hasTool = true
			}
		}
		if hasThinking && mode == "off" {
			return "the turn carries a thinking block but the request does not ask for thinking"
		}
		if hasTool && mode == "on" && !hasThinking {
			return "the tool turn carries no thinking block"
		}
		if !hasTool {
			continue
		}
		// The turn that called a tool is followed by its results.
		if i+1 >= len(messages) {
			return "the tool turn is not followed by its results"
		}
		next, _ := messages[i+1].(map[string]any)
		if next["role"] != "user" {
			return "the results of a tool turn are not a user message"
		}
		nextBlocks, _ := next["content"].([]any)
		results := 0
		for _, rawBlock := range nextBlocks {
			block, _ := rawBlock.(map[string]any)
			if block["type"] == "tool_result" {
				results++
			}
		}
		if results == 0 {
			return "the tool turn is not followed by a result"
		}
	}
	return ""
}

// adapter builds the adapter this mock is reached through.
//
// The mock says which behaviour it has, and the adapter is told. What
// HAL knows about an endpoint comes from the endpoint, never from the
// model identifier: the same Claude name is a Claude model on one
// endpoint and an alias on another.
func (e *anthropicEndpoint) adapter(ts *httptest.Server) *AnthropicCompatible {
	a := &AnthropicCompatible{Endpoint: ts.URL}
	if e.deepSeek {
		a.ThinkingDefault = ThinkingDefaultOn
	} else {
		a.ThinkingDefault = ThinkingDefaultOff
	}
	return a
}

// last returns the request the endpoint received last.
func (e *anthropicEndpoint) last() map[string]any {
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.requests) == 0 {
		return nil
	}
	return e.requests[len(e.requests)-1]
}

// assistantBlocks returns the content blocks of the last assistant turn
// of the last request.
func (e *anthropicEndpoint) assistantBlocks() []map[string]any {
	messages, _ := e.last()["messages"].([]any)
	for i := len(messages) - 1; i >= 0; i-- {
		msg, _ := messages[i].(map[string]any)
		if msg["role"] != "assistant" {
			continue
		}
		raw, _ := msg["content"].([]any)
		out := make([]map[string]any, 0, len(raw))
		for _, b := range raw {
			block, _ := b.(map[string]any)
			out = append(out, block)
		}
		return out
	}
	return nil
}

// thinkingBlocks returns the thinking blocks of the last assistant turn
// of the last request.
func (e *anthropicEndpoint) thinkingBlocks() []map[string]any {
	var out []map[string]any
	for _, block := range e.assistantBlocks() {
		if block["type"] == "thinking" {
			out = append(out, block)
		}
	}
	return out
}

// The six configurations the mission names, each exercised on its own.
//
// A model that thinks by default has its blocks replayed even when the
// operator configured no effort; a model that thinks only when asked
// has its blocks replayed only when the request asks.
func TestTheMessagesConfigurations(t *testing.T) {
	cases := []struct {
		name string
		// model is the model the request names.
		model Model
		// deepSeek reports that the endpoint thinks by default.
		deepSeek bool
		// signed reports that the endpoint signs its thinking.
		signed bool
		// wantReplay reports whether the continuation has to carry the
		// thinking blocks back.
		wantReplay bool
		// wantBlocks reports whether the provider answered with any
		// thinking at all.
		wantBlocks bool
	}{
		{
			name:       "deepseek-flash on the DeepSeek endpoint with no effort",
			model:      Model{ID: "deepseek-flash"},
			deepSeek:   true,
			wantReplay: true, wantBlocks: true,
		},
		{
			name:       "deepseek-flash on the DeepSeek endpoint with an effort",
			model:      Model{ID: "deepseek-flash", Effort: "high"},
			deepSeek:   true,
			wantReplay: true, wantBlocks: true,
		},
		{
			name:       "a Claude alias on the DeepSeek endpoint with no effort",
			model:      Model{ID: "claude-sonnet-4-5"},
			deepSeek:   true,
			wantReplay: true, wantBlocks: true,
		},
		{
			name:       "a Claude alias on the DeepSeek endpoint with an effort",
			model:      Model{ID: "claude-sonnet-4-5", Effort: "high"},
			deepSeek:   true,
			wantReplay: true, wantBlocks: true,
		},
		{
			name:       "a Claude model on the genuine endpoint with no effort",
			model:      Model{ID: "claude-sonnet-4-5"},
			wantReplay: false, wantBlocks: false,
		},
		{
			name:       "a Claude model on the genuine endpoint with an effort",
			model:      Model{ID: "claude-sonnet-4-5", Effort: "high"},
			signed:     true,
			wantReplay: true, wantBlocks: true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := &anthropicEndpoint{t: t, deepSeek: c.deepSeek, signed: c.signed,
				steps: []string{"read", ""}}
			ts := httptest.NewServer(srv)
			defer ts.Close()

			a := srv.adapter(ts)
			tools := []Tool{{Name: "read"}}
			resp, err := a.ChatStreamTools(context.Background(),
				[]Message{{Role: "user", Content: "read the file"}},
				c.model, tools, nil)
			if err != nil {
				t.Fatal(err)
			}
			if (len(resp.ReasoningBlocks) > 0) != c.wantBlocks {
				t.Fatalf("the reasoning blocks = %+v", resp.ReasoningBlocks)
			}
			if c.wantBlocks && resp.Thinking != expectedThinking(c.model) {
				t.Errorf("the thinking of the producing request = %q, want %q",
					resp.Thinking, expectedThinking(c.model))
			}
			// The continuation carries the turn's blocks back.
			follow := []Message{
				{Role: "user", Content: "read the file"},
				{Role: "assistant", Content: resp.Content, ToolCalls: resp.ToolCalls,
					ReasoningKey: resp.ReasoningKey, Reasoning: resp.ReasoningBlocks, Origin: resp.Origin,
					Thinking: resp.Thinking},
				{Role: "tool", ToolCallID: "call_read", Name: "read", Content: "the file"},
			}
			if _, err := a.ChatStreamTools(context.Background(), follow, c.model, tools, nil); err != nil {
				t.Fatal(err)
			}
			replayed := len(srv.thinkingBlocks()) > 0
			if replayed != c.wantReplay {
				t.Errorf("the thinking blocks were replayed = %v, want %v: %+v",
					replayed, c.wantReplay, srv.assistantBlocks())
			}
			if c.signed && c.wantReplay {
				// The signature is the provider's own, and it is
				// replayed byte for byte.
				block := srv.thinkingBlocks()[0]
				if block["signature"] != "sig-abc" {
					t.Errorf("the replayed block = %+v", block)
				}
			}
			if srv.violations != 0 {
				t.Errorf("the endpoint refused %d requests", srv.violations)
			}
		})
	}
}

// expectedThinking reports how a request for this model asks for
// thinking.
func expectedThinking(model Model) string {
	if anthropicThinkingFor(model) != nil {
		return ThinkingAsked
	}
	if thinking, _ := anthropicDeepSeekEffort(model); thinking != nil {
		return ThinkingAsked
	}
	return ThinkingUnset
}

// The alias with no effort sends no thinking configuration at all, and
// the provider thinks anyway.
//
// That is the whole of the failure: the request says nothing, the
// provider answers with thinking, and the continuation has to carry
// those blocks back.
func TestTheAliasWithNoEffortSaysNothingAndIsAnsweredWithThinking(t *testing.T) {
	srv := &anthropicEndpoint{t: t, deepSeek: true, steps: []string{"read"}}
	ts := httptest.NewServer(srv)
	defer ts.Close()

	a := srv.adapter(ts)
	resp, err := a.ChatStreamTools(context.Background(),
		[]Message{{Role: "user", Content: "read the file"}},
		Model{ID: "claude-sonnet-4-5"}, []Tool{{Name: "read"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	body := srv.last()
	if _, ok := body["thinking"]; ok {
		t.Errorf("the request carried a thinking configuration: %v", body["thinking"])
	}
	if len(resp.ReasoningBlocks) != 1 {
		t.Fatalf("the reasoning blocks = %+v", resp.ReasoningBlocks)
	}
	if resp.Thinking != ThinkingUnset {
		t.Errorf("the thinking of the producing request = %q", resp.Thinking)
	}
}

// The negative control: a turn that leaves its thinking out is refused.
//
// The mock is strict, so the replay is proved rather than assumed.
func TestTheEndpointRefusesATurnWithoutItsThinking(t *testing.T) {
	srv := &anthropicEndpoint{t: t, deepSeek: true, steps: []string{"read"}}
	ts := httptest.NewServer(srv)
	defer ts.Close()

	a := srv.adapter(ts)
	messages := []Message{
		{Role: "user", Content: "read the file"},
		{Role: "assistant", Content: "reading it",
			ToolCalls: []ToolCall{{ID: "call_read", Name: "read"}}},
		{Role: "tool", ToolCallID: "call_read", Name: "read", Content: "the file"},
	}
	_, err := a.ChatStreamTools(context.Background(), messages,
		Model{ID: "claude-sonnet-4-5"}, []Tool{{Name: "read"}}, nil)
	if err == nil {
		t.Fatal("the endpoint accepted a turn without its thinking blocks")
	}
	if !strings.Contains(err.Error(), "400") {
		t.Errorf("error = %v", err)
	}
	if srv.violations != 1 {
		t.Errorf("the endpoint refused %d requests", srv.violations)
	}
}

// A request that forbids thinking replays nothing, whatever came
// before.
//
// The state is part of the contract: the Messages API refuses an
// unexpected thinking block, so a request that says "no thinking" must
// not carry one.
func TestARequestThatForbidsThinkingReplaysNothing(t *testing.T) {
	a := &AnthropicCompatible{Endpoint: "https://endpoint.test/anthropic"}
	key := ReasoningKey("claude-sonnet-4-5", WireAnthropic)
	messages := []Message{
		{Role: "user", Content: "hi"},
		{Role: "assistant", Content: "the answer",
			ReasoningKey: key,
			Origin:       string(a.origin()),
			Reasoning:    []ReasoningBlock{{Type: ReasoningThinking, Text: "it", Signature: "sig"}}},
	}
	_, conversation, err := a.anthropicMessages(messages,
		Model{ID: "claude-sonnet-4-5"}, ThinkingOff)
	if err != nil {
		t.Fatal(err)
	}
	for _, block := range conversation[1].Content {
		if block.Type == ReasoningThinking {
			t.Errorf("a thinking block was attached to a request that forbids it: %+v", block)
		}
	}

	// A request that asks for thinking replays it, signature and all.
	_, conversation, err = a.anthropicMessages(messages,
		Model{ID: "claude-sonnet-4-5"}, ThinkingAsked)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, block := range conversation[1].Content {
		if block.Type == ReasoningThinking {
			found = true
			if block.Signature != "sig" {
				t.Errorf("the signature was changed: %+v", block)
			}
		}
	}
	if !found {
		t.Errorf("the thinking blocks were not replayed: %+v", conversation[1].Content)
	}
}

// A record written before the thinking of the producing request was
// kept says nothing about how that request asked for thinking.
//
// Nothing is guessed about it: the careful reading is that the
// provider thinks only when asked, so a request that says nothing
// replays nothing.
func TestARecordWithoutThinkingIsTreatedWithCare(t *testing.T) {
	a := &AnthropicCompatible{Endpoint: "https://endpoint.test/anthropic"}
	key := ReasoningKey("claude-sonnet-4-5", WireAnthropic)
	messages := []Message{
		{Role: "user", Content: "hi"},
		{Role: "assistant", Content: "the answer",
			ReasoningKey: key,
			Origin:       string(a.origin()),
			Reasoning:    []ReasoningBlock{{Type: ReasoningThinking, Text: "it"}}},
	}
	_, conversation, err := a.anthropicMessages(messages,
		Model{ID: "claude-sonnet-4-5"}, ThinkingUnset)
	if err != nil {
		t.Fatal(err)
	}
	for _, block := range conversation[1].Content {
		if block.Type == ReasoningThinking {
			t.Errorf("a legacy record was replayed on its own evidence: %+v", block)
		}
	}
}

// The alias keeps its blocks across several continuations, and the
// blocks it keeps are the ones the provider sent.
func TestTheAliasSurvivesSeveralContinuations(t *testing.T) {
	srv := &anthropicEndpoint{t: t, deepSeek: true, steps: []string{"read", "list", ""}}
	ts := httptest.NewServer(srv)
	defer ts.Close()

	a := srv.adapter(ts)
	tools := []Tool{{Name: "read"}, {Name: "list"}}
	model := Model{ID: "claude-sonnet-4-5"}
	messages := []Message{{Role: "user", Content: "read the file"}}
	for step := 0; step < 3; step++ {
		resp, err := a.ChatStreamTools(context.Background(), messages, model, tools, nil)
		if err != nil {
			t.Fatalf("step %d: %v", step, err)
		}
		messages = append(messages, Message{
			Role: "assistant", Content: resp.Content, ToolCalls: resp.ToolCalls,
			ReasoningKey: resp.ReasoningKey, Reasoning: resp.ReasoningBlocks, Origin: resp.Origin,
			Thinking: resp.Thinking,
		})
		for _, call := range resp.ToolCalls {
			messages = append(messages, Message{
				Role: "tool", ToolCallID: call.ID, Name: call.Name, Content: "the result",
			})
		}
	}
	if srv.violations != 0 {
		t.Errorf("the endpoint refused %d requests", srv.violations)
	}
	// Every assistant turn of the last request carries its thinking.
	messages2, _ := srv.last()["messages"].([]any)
	turns := 0
	for _, raw := range messages2 {
		msg, _ := raw.(map[string]any)
		if msg["role"] != "assistant" {
			continue
		}
		turns++
		blocks, _ := msg["content"].([]any)
		thinking := false
		for _, rawBlock := range blocks {
			block, _ := rawBlock.(map[string]any)
			if block["type"] == "thinking" {
				thinking = true
			}
		}
		if !thinking {
			t.Errorf("the assistant turn %d carries no thinking: %+v", turns, blocks)
		}
	}
	if turns != 2 {
		t.Errorf("the assistant turns = %d", turns)
	}
}

// The blocks are the provider's own, byte for byte.
//
// A signature that the provider gave is the provider's to verify, and a
// block that carries none carries no signature at all.
func TestTheAliasKeepsTheProvidersOwnBlocks(t *testing.T) {
	srv := &anthropicEndpoint{t: t, deepSeek: true, steps: []string{"read"}}
	ts := httptest.NewServer(srv)
	defer ts.Close()

	a := srv.adapter(ts)
	resp, err := a.ChatStreamTools(context.Background(),
		[]Message{{Role: "user", Content: "read the file"}},
		Model{ID: "claude-sonnet-4-5"}, []Tool{{Name: "read"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	follow := []Message{
		{Role: "user", Content: "read the file"},
		{Role: "assistant", Content: resp.Content, ToolCalls: resp.ToolCalls,
			ReasoningKey: resp.ReasoningKey, Reasoning: resp.ReasoningBlocks, Origin: resp.Origin,
			Thinking: resp.Thinking},
		{Role: "tool", ToolCallID: "call_read", Name: "read", Content: "the file"},
	}
	if _, err := a.ChatStreamTools(context.Background(), follow,
		Model{ID: "claude-sonnet-4-5"}, []Tool{{Name: "read"}}, nil); err != nil {
		t.Fatal(err)
	}
	blocks := srv.thinkingBlocks()
	if len(blocks) != 1 {
		t.Fatalf("the replayed thinking = %+v", blocks)
	}
	if blocks[0]["thinking"] != "the model weighed it" {
		t.Errorf("the thinking was changed: %+v", blocks[0])
	}
	if _, ok := blocks[0]["signature"]; ok {
		t.Errorf("a signature was invented: %+v", blocks[0])
	}
}

// The whole answer carries the same blocks as the stream.
func TestTheAliasCapturesTheWholeAnswer(t *testing.T) {
	srv := &anthropicEndpoint{t: t, deepSeek: true, steps: []string{"read"}}
	ts := httptest.NewServer(srv)
	defer ts.Close()

	a := srv.adapter(ts)
	resp, err := a.Chat(context.Background(),
		[]Message{{Role: "user", Content: "read the file"}},
		Model{ID: "claude-sonnet-4-5"})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.ReasoningBlocks) != 1 {
		t.Fatalf("the reasoning blocks = %+v", resp.ReasoningBlocks)
	}
	if resp.Thinking != ThinkingUnset {
		t.Errorf("the thinking of the producing request = %q", resp.Thinking)
	}
}

// The effort may be configured between the turns.
//
// A request that asks for thinking replays what came before, whatever
// the request that produced it asked for: the provider refuses a
// continued thinking turn whose blocks are missing.
func TestTheEffortMayBeConfiguredBetweenTheTurns(t *testing.T) {
	srv := &anthropicEndpoint{t: t, deepSeek: true, steps: []string{"read", ""}}
	ts := httptest.NewServer(srv)
	defer ts.Close()

	a := srv.adapter(ts)
	tools := []Tool{{Name: "read"}}
	// The first turn asked for nothing and the provider thought anyway.
	plain := Model{ID: "claude-sonnet-4-5"}
	resp, err := a.ChatStreamTools(context.Background(),
		[]Message{{Role: "user", Content: "read the file"}},
		plain, tools, nil)
	if err != nil {
		t.Fatal(err)
	}
	// The operator configures an effort: the next request asks for
	// thinking, and the turn before it is replayed whole.
	chosen := Model{ID: "claude-sonnet-4-5", Effort: "high"}
	follow := []Message{
		{Role: "user", Content: "read the file"},
		{Role: "assistant", Content: resp.Content, ToolCalls: resp.ToolCalls,
			ReasoningKey: resp.ReasoningKey, Reasoning: resp.ReasoningBlocks, Origin: resp.Origin,
			Thinking: resp.Thinking},
		{Role: "tool", ToolCallID: "call_read", Name: "read", Content: "the file"},
	}
	if _, err := a.ChatStreamTools(context.Background(), follow, chosen, tools, nil); err != nil {
		t.Fatal(err)
	}
	if len(srv.thinkingBlocks()) == 0 {
		t.Errorf("the earlier turn was not replayed: %+v", srv.assistantBlocks())
	}
	if srv.violations != 0 {
		t.Errorf("the endpoint refused %d requests", srv.violations)
	}
}

// The effort may also be taken away between the turns.
//
// What happens then depends on what the provider is, and HAL cannot
// tell a Claude model from a DeepSeek alias: the name is the same on
// both. The careful reading is kept — a request that no longer asks
// for thinking replays nothing that was produced by a request that did
// ask — because a request that forbids thinking must not carry a
// thinking block.
//
// The cost of that care is the alias: a DeepSeek endpoint whose effort
// was taken away keeps thinking by default, and its blocks may be
// recovered from the tool-call identifiers rather than sent back. That
// is the endpoint-binding caveat, and it is not settled here.
func TestTheEffortMayBeTakenAwayBetweenTheTurns(t *testing.T) {
	a := &AnthropicCompatible{Endpoint: "https://endpoint.test/anthropic"}
	key := ReasoningKey("claude-sonnet-4-5", WireAnthropic)
	asked := []Message{
		{Role: "user", Content: "hi"},
		{Role: "assistant", Content: "the answer",
			ReasoningKey: key, Origin: string(a.origin()), Thinking: ThinkingAsked,
			Reasoning: []ReasoningBlock{{Type: ReasoningThinking, Text: "it"}}},
	}
	_, conversation, err := a.anthropicMessages(asked,
		Model{ID: "claude-sonnet-4-5"}, ThinkingUnset)
	if err != nil {
		t.Fatal(err)
	}
	for _, block := range conversation[1].Content {
		if block.Type == ReasoningThinking {
			t.Errorf("a block from a request that asked was replayed on one that did not: %+v", block)
		}
	}

	// A turn that asked for nothing and was answered with thinking is
	// replayed: the provider thinks by default, and it said so.
	unset := []Message{
		{Role: "user", Content: "hi"},
		{Role: "assistant", Content: "the answer",
			ReasoningKey: key, Origin: string(a.origin()), Thinking: ThinkingUnset,
			Reasoning: []ReasoningBlock{{Type: ReasoningThinking, Text: "it"}}},
	}
	_, conversation, err = a.anthropicMessages(unset,
		Model{ID: "claude-sonnet-4-5"}, ThinkingUnset)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, block := range conversation[1].Content {
		if block.Type == ReasoningThinking {
			found = true
		}
	}
	if !found {
		t.Errorf("the thinking of a provider that thinks by default was not replayed: %+v",
			conversation[1].Content)
	}
}

// The DeepSeek model of the Messages wire keeps its blocks when the
// effort is taken away.
//
// Its family thinks by default, so a request that says nothing is a
// thinking request there, and the blocks come back with it.
func TestTheDeepSeekModelKeepsItsBlocksWhenTheEffortIsTakenAway(t *testing.T) {
	srv := &anthropicEndpoint{t: t, deepSeek: true, steps: []string{"read", ""}}
	ts := httptest.NewServer(srv)
	defer ts.Close()

	a := srv.adapter(ts)
	tools := []Tool{{Name: "read"}}
	chosen := Model{ID: "deepseek-flash", Effort: "high"}
	resp, err := a.ChatStreamTools(context.Background(),
		[]Message{{Role: "user", Content: "read the file"}},
		chosen, tools, nil)
	if err != nil {
		t.Fatal(err)
	}
	// The operator takes the effort away: the request says nothing, and
	// the provider thinks by default.
	plain := Model{ID: "deepseek-flash"}
	follow := []Message{
		{Role: "user", Content: "read the file"},
		{Role: "assistant", Content: resp.Content, ToolCalls: resp.ToolCalls,
			ReasoningKey: resp.ReasoningKey, Reasoning: resp.ReasoningBlocks, Origin: resp.Origin,
			Thinking: resp.Thinking},
		{Role: "tool", ToolCallID: "call_read", Name: "read", Content: "the file"},
	}
	if _, err := a.ChatStreamTools(context.Background(), follow, plain, tools, nil); err != nil {
		t.Fatal(err)
	}
	if len(srv.thinkingBlocks()) == 0 {
		t.Errorf("the earlier turn was not replayed: %+v", srv.assistantBlocks())
	}
	if srv.violations != 0 {
		t.Errorf("the endpoint refused %d requests", srv.violations)
	}
}

// The Claude model keeps its behaviour when the effort is taken away.
//
// A request that no longer asks for thinking must not carry thinking
// blocks: the Messages API refuses an unexpected block. The endpoint
// here is the genuine one, and it says so by answering without
// thinking.
func TestTheClaudeModelDropsItsBlocksWhenTheEffortIsTakenAway(t *testing.T) {
	srv := &anthropicEndpoint{t: t, signed: true, steps: []string{"read", ""}}
	ts := httptest.NewServer(srv)
	defer ts.Close()

	a := srv.adapter(ts)
	tools := []Tool{{Name: "read"}}
	chosen := Model{ID: "claude-sonnet-4-5", Effort: "high"}
	resp, err := a.ChatStreamTools(context.Background(),
		[]Message{{Role: "user", Content: "read the file"}},
		chosen, tools, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.ReasoningBlocks) != 1 || resp.ReasoningBlocks[0].Signature != "sig-abc" {
		t.Fatalf("the reasoning blocks = %+v", resp.ReasoningBlocks)
	}
	// The operator takes the effort away.
	plain := Model{ID: "claude-sonnet-4-5"}
	follow := []Message{
		{Role: "user", Content: "read the file"},
		{Role: "assistant", Content: resp.Content, ToolCalls: resp.ToolCalls,
			ReasoningKey: resp.ReasoningKey, Reasoning: resp.ReasoningBlocks, Origin: resp.Origin,
			Thinking: resp.Thinking},
		{Role: "tool", ToolCallID: "call_read", Name: "read", Content: "the file"},
	}
	if _, err := a.ChatStreamTools(context.Background(), follow, plain, tools, nil); err != nil {
		t.Fatal(err)
	}
	if len(srv.thinkingBlocks()) != 0 {
		t.Errorf("a thinking block was attached to a request that does not ask for thinking: %+v",
			srv.assistantBlocks())
	}
	if srv.violations != 0 {
		t.Errorf("the endpoint refused %d requests", srv.violations)
	}
}

// The protocols stay apart.
//
// The alias shares the Claude protocol, because its name is a Claude
// name: HAL cannot tell it from a Claude model, and that is the
// endpoint-binding caveat. What it never shares is the DeepSeek
// protocol of the OpenAI dialect, or the DeepSeek protocol of the
// Messages wire.
func TestTheAliasDoesNotCrossTheProtocols(t *testing.T) {
	a := &AnthropicCompatible{Endpoint: "https://endpoint.test/anthropic"}
	aliasKey := ReasoningKey("claude-sonnet-4-5", WireAnthropic)
	openAIKey := ReasoningKey("deepseek-flash", WireOpenAI)
	messagesKey := ReasoningKey("deepseek-flash", WireAnthropic)
	if aliasKey == openAIKey || aliasKey == messagesKey {
		t.Fatalf("the protocols are not apart: %q %q %q", aliasKey, openAIKey, messagesKey)
	}

	// A block of the OpenAI dialect is not replayed on the Messages wire.
	//
	// The record names the endpoint that produced it: the protocol
	// keeps the two apart, and the origin says whose block it is.
	_, conversation, err := a.anthropicMessages([]Message{
		{Role: "user", Content: "hi"},
		{Role: "assistant", Content: "the answer",
			ReasoningKey: openAIKey,
			Origin:       string(a.origin()),
			Reasoning:    []ReasoningBlock{{Type: ReasoningPlain, Text: "it"}}},
	}, Model{ID: "claude-sonnet-4-5"}, ThinkingAsked)
	if err != nil {
		t.Fatal(err)
	}
	for _, block := range conversation[1].Content {
		if block.Type == ReasoningThinking {
			t.Errorf("a block of another protocol was replayed: %+v", block)
		}
	}

	// A block of the Messages wire is not sent as reasoning_content.
	openai := &wireServer{t: t, answers: []string{`{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`}}
	ots := httptest.NewServer(openai)
	defer ots.Close()
	p := &OpenAICompatible{Endpoint: ots.URL}
	openaiMessages := []Message{
		{Role: "user", Content: "hi"},
		{Role: "assistant", Content: "hello",
			ReasoningKey: messagesKey,
			Origin:       string(p.origin()),
			Reasoning:    []ReasoningBlock{{Type: ReasoningThinking, Text: "it"}}},
	}
	if _, err := p.ChatStreamTools(context.Background(), openaiMessages,
		Model{ID: "deepseek-flash"}, []Tool{{Name: "read"}}, nil); err != nil {
		t.Fatal(err)
	}
	for _, msg := range openai.messages() {
		if _, ok := msg["reasoning_content"]; ok {
			t.Errorf("a Messages-wire block was sent as reasoning_content: %v", msg)
		}
	}
}
