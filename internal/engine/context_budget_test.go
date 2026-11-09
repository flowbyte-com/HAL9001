package engine

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/flowbyte-com/HAL9001/internal/provider"
	"github.com/flowbyte-com/HAL9001/internal/session"
)

// The context budget.
//
// The budget is applied by the provider package: the engine hands it the
// fully assembled request and it decides what may be dropped. These
// tests watch the request the provider receives, not the configuration
// that describes it.
//
// The behaviour they pin down is the behaviour the operator was promised:
// a configured limit reaches the request, the offered functions count
// towards it, a request that cannot be reduced is refused before it is
// transmitted, a tool call never loses its result, and the conversation
// itself is never rewritten.

// budgetSpy records the complete request it receives.
type budgetSpy struct {
	requests [][]provider.Message
	tools    [][]provider.Tool
	reply    provider.Response
}

func (p *budgetSpy) Name() string { return "budget" }

func (p *budgetSpy) Chat(_ context.Context, messages []provider.Message, _ provider.Model) (provider.Response, error) {
	p.requests = append(p.requests, append([]provider.Message(nil), messages...))
	p.tools = append(p.tools, nil)
	return p.reply, nil
}

// last returns the last request the provider received.
func (p *budgetSpy) last(t *testing.T) []provider.Message {
	t.Helper()
	if len(p.requests) == 0 {
		t.Fatal("the provider was never called")
	}
	return p.requests[len(p.requests)-1]
}

// budgetConversation builds a session with a long conversation.
func budgetConversation(t *testing.T, exchanges int, size int) *session.Store {
	t.Helper()
	st, err := session.Open(t.TempDir(), "budget")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < exchanges; i++ {
		if _, err := st.AppendTurn(session.RoleUser,
			fmt.Sprintf("question %d %s", i, strings.Repeat("q", size)), session.Turn{}); err != nil {
			t.Fatal(err)
		}
		if _, err := st.AppendTurn(session.RoleAssistant,
			fmt.Sprintf("answer %d %s", i, strings.Repeat("a", size)), session.Turn{}); err != nil {
			t.Fatal(err)
		}
	}
	return st
}

// operatorBudget is the budget an operator-configured limit produces.
func operatorBudget(limit int) provider.ContextBudget {
	return provider.ContextBudget{
		Limit:          limit,
		ReservedOutput: 20,
		Source:         provider.ContextSourceOperator,
		Mode:           provider.ContextModeConfigured,
		Verified:       true,
	}
}

// A manual context limit is enforced on the request that is sent.
//
// The limit is not a label in the configuration: the request the
// provider receives fits inside it, and it is shorter than the
// conversation it was built from.
func TestTheManualContextLimitIsEnforcedOnTheRequest(t *testing.T) {
	st := budgetConversation(t, 40, 200)
	spy := &budgetSpy{reply: provider.Response{Content: "ok"}}
	eng := New(st, spy, provider.Model{ID: "m", ContextWindow: 500, Budget: operatorBudget(500)})

	if _, err := eng.Respond(context.Background(), "and now?"); err != nil {
		t.Fatal(err)
	}
	request := spy.last(t)
	if got := provider.EstimateTokens(request, nil); got > 500 {
		t.Errorf("the request is %d tokens, over the limit of 500", got)
	}
	// The conversation is longer than the request: something was left
	// out on purpose.
	if len(request) >= 82 {
		t.Errorf("the request carries %d messages, the whole conversation", len(request))
	}
	// The current message is never the thing that is dropped.
	last := request[len(request)-1]
	if last.Role != "user" || last.Content != "and now?" {
		t.Errorf("the last message is %+v, want the current user message", last)
	}
	// The conversation itself is untouched: there is no automatic
	// compaction in this milestone.
	history, err := st.History()
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 82 {
		t.Errorf("the session holds %d messages, want 82: the request was trimmed, the conversation was not", len(history))
	}
}

// The offered functions travel with every request, so they count
// towards the budget.
func TestTheContextLimitCountsTheOfferedFunctions(t *testing.T) {
	st := budgetConversation(t, 10, 100)
	spy := &budgetSpy{reply: provider.Response{Content: "ok"}}
	eng := New(st, spy, provider.Model{ID: "m", ContextWindow: 400, Budget: operatorBudget(400)})
	tools := []provider.Tool{{
		Name:        "a_tool_with_a_long_name",
		Description: strings.Repeat("d", 400),
		Parameters:  strings.Repeat("p", 400),
	}}
	messages := []provider.Message{
		{Role: "system", Content: "system"},
		{Role: "user", Content: "the current question"},
	}
	if _, err := eng.chat(context.Background(), messages, tools, nil); err != nil {
		t.Fatalf("the turn failed: %v", err)
	}
	request := spy.last(t)
	if got := provider.EstimateTokens(request, tools); got > 400 {
		t.Errorf("the request is %d tokens, over the limit of 400", got)
	}
}

// A request that cannot fit is refused with an explicit error. The
// provider is never called with a request that does not fit, and the
// operator's message is never silently dropped.
func TestARequestThatCannotFitIsRefused(t *testing.T) {
	st, err := session.Open(t.TempDir(), "budget")
	if err != nil {
		t.Fatal(err)
	}
	spy := &budgetSpy{reply: provider.Response{Content: "ok"}}
	eng := New(st, spy, provider.Model{ID: "m", ContextWindow: 20, Budget: operatorBudget(20)})

	_, err = eng.Respond(context.Background(), strings.Repeat("x", 400))
	if err == nil {
		t.Fatal("a request that cannot fit was sent anyway")
	}
	if !strings.Contains(err.Error(), "context budget") {
		t.Errorf("the refusal does not explain itself: %v", err)
	}
	if len(spy.requests) != 0 {
		t.Errorf("the provider was called %d time(s) with a request that does not fit", len(spy.requests))
	}
	// The operator's message is still in the conversation: it was not
	// dropped, the turn failed.
	history, err := st.History()
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 1 || history[0].Role != session.RoleUser {
		t.Fatalf("the operator's message was dropped: %+v", history)
	}
}

// A tool result never loses its call, and a tool call never loses its
// result. Whole exchanges are dropped, never half of one.
func TestTheContextLimitNeverOrphansAToolExchange(t *testing.T) {
	messages := []provider.Message{
		{Role: "system", Content: "system"},
		{Role: "user", Content: "first question"},
		{Role: "assistant", ToolCalls: []provider.ToolCall{{ID: "call-1", Name: "read"}}},
		{Role: "tool", ToolCallID: "call-1", Name: "read", Content: "the first result"},
		{Role: "user", Content: "second question"},
		{Role: "assistant", ToolCalls: []provider.ToolCall{{ID: "call-2", Name: "read"}}},
		{Role: "tool", ToolCallID: "call-2", Name: "read", Content: "the second result"},
		{Role: "user", Content: "third question"},
	}
	// The limits are expressed in HAL's own estimate, so the case does
	// not depend on the size of the safety margin: the smallest one
	// leaves room for the instructions and the current exchange, and
	// the largest one leaves room for everything.
	minimum := provider.EstimateTokens(
		[]provider.Message{messages[0], messages[len(messages)-1]}, nil)
	whole := provider.EstimateTokens(messages, nil)
	limits := []int{minimum, minimum + 3, minimum + 6, minimum + 9, minimum + 12}
	if whole > limits[len(limits)-1] {
		limits = append(limits, whole)
	}
	for _, limit := range limits {
		kept, _, err := provider.TrimToBudget(messages, nil, operatorBudget(limit))
		if err != nil {
			t.Fatalf("limit %d: %v", limit, err)
		}
		calls := map[string]bool{}
		results := map[string]bool{}
		for _, m := range kept {
			for _, call := range m.ToolCalls {
				calls[call.ID] = true
			}
			if m.Role == "tool" {
				results[m.ToolCallID] = true
			}
		}
		for id := range results {
			if !calls[id] {
				t.Errorf("limit %d: the result of %q has no call", limit, id)
			}
		}
		for id := range calls {
			if !results[id] {
				t.Errorf("limit %d: the call %q has no result", limit, id)
			}
		}
		// The current message is never dropped.
		if kept[len(kept)-1].Role != "user" || kept[len(kept)-1].Content != "third question" {
			t.Errorf("limit %d: the current message was dropped: %+v", limit, kept[len(kept)-1])
		}
		// The oldest exchange goes first, and no exchange is cut in
		// half.
		if limit < whole && len(kept) == len(messages) {
			t.Errorf("limit %d: nothing was dropped", limit)
		}
	}
	// A limit that even the current exchange does not fit is refused
	// with an explicit error, never a broken request.
	if _, _, err := provider.TrimToBudget(messages, nil, operatorBudget(minimum-1)); err == nil {
		t.Error("a request that cannot fit was trimmed anyway")
	}
}

// The request that carries a tool exchange keeps the exchange whole,
// even when the older conversation is dropped to make room.
func TestTheRequestKeepsTheToolExchangeWhole(t *testing.T) {
	st := budgetConversation(t, 20, 200)
	spy := &budgetSpy{reply: provider.Response{Content: "ok"}}
	eng := New(st, spy, provider.Model{ID: "m", ContextWindow: 400, Budget: operatorBudget(400)})

	messages := []provider.Message{
		{Role: "system", Content: "system"},
		{Role: "user", Content: "the current question"},
		{Role: "assistant", ToolCalls: []provider.ToolCall{{ID: "call-9", Name: "read"}}},
		{Role: "tool", ToolCallID: "call-9", Name: "read", Content: "the result"},
	}
	if _, err := eng.chat(context.Background(), messages, nil, nil); err != nil {
		t.Fatalf("the turn failed: %v", err)
	}
	request := spy.last(t)
	seenCall, seenResult := false, false
	for _, m := range request {
		for _, call := range m.ToolCalls {
			if call.ID == "call-9" {
				seenCall = true
			}
		}
		if m.Role == "tool" && m.ToolCallID == "call-9" {
			seenResult = true
		}
	}
	if !seenCall || !seenResult {
		t.Errorf("the tool exchange was split: call=%v result=%v", seenCall, seenResult)
	}
}
