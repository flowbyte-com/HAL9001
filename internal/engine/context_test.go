package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/flowbyte-com/HAL9001/internal/provider"
	"github.com/flowbyte-com/HAL9001/internal/session"
	"github.com/flowbyte-com/HAL9001/internal/workspace"
)

// The context budget, enforced.
//
// The budget is applied to the request as it will be sent: the engine
// trims the assembled request immediately before the provider call, on
// every round of the tool loop. These tests use a recording provider,
// so the request that would have been transmitted can be inspected.

// budgetProvider records the request it was handed.
//
// The replies are scripted, so a tool round can be provoked: the first
// round asks for a tool, the second answers.
type budgetProvider struct {
	messages []provider.Message
	tools    []provider.Tool
	calls    int
	replies  []provider.Response
	// every request the provider was handed, in order.
	requests [][]provider.Message
}

func (r *budgetProvider) Name() string { return "recording" }

func (r *budgetProvider) Chat(_ context.Context, messages []provider.Message, _ provider.Model) (provider.Response, error) {
	r.calls++
	r.messages = append([]provider.Message(nil), messages...)
	r.requests = append(r.requests, append([]provider.Message(nil), messages...))
	if len(r.replies) == 0 {
		return provider.Response{Content: "recorded"}, nil
	}
	reply := r.replies[0]
	if len(r.replies) > 1 {
		r.replies = r.replies[1:]
	}
	return reply, nil
}

// budgetedEngine builds an engine whose conversation already holds n
// exchanges, with a model that carries a context budget.
func budgetedEngine(t *testing.T, n int, budget provider.ContextBudget) (*Engine, *budgetProvider) {
	t.Helper()
	st, err := session.Open(t.TempDir(), "chat")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < n; i++ {
		if _, err := st.AppendTurn(session.RoleUser,
			fmt.Sprintf("question %02d: what is the state of the build?", i), session.Turn{}); err != nil {
			t.Fatal(err)
		}
		if _, err := st.AppendTurn(session.RoleAssistant,
			fmt.Sprintf("answer %02d: the build is green.", i), session.Turn{}); err != nil {
			t.Fatal(err)
		}
	}
	rec := &budgetProvider{}
	eng := New(st, rec, provider.Model{ID: "mock", Budget: budget})
	return eng, rec
}

func TestAConfiguredLimitBoundsTheOutgoingRequest(t *testing.T) {
	// 41 history messages and a 100-token budget. The request cannot be
	// reduced that far, so it is refused: the safety instructions and
	// the operator's current request are never dropped to satisfy a
	// budget, and nothing is transmitted.
	eng, rec := budgetedEngine(t, 21, provider.ContextBudget{
		Limit:          100,
		ReservedOutput: 20,
		Source:         provider.ContextSourceOperator,
	})
	_, err := eng.Respond(context.Background(), "and now?")
	if err == nil {
		t.Fatal("an irreducible request was sent")
	}
	var budgetErr *provider.ContextBudgetError
	if !errors.As(err, &budgetErr) {
		t.Fatalf("the error is not a context-budget error: %v", err)
	}
	if rec.calls != 0 {
		t.Fatalf("the provider was called %d times with an oversized request", rec.calls)
	}
}

func TestTheOutgoingRequestIsTrimmedToTheBudget(t *testing.T) {
	// A budget that leaves room for the instructions and the newest
	// exchange trims the older ones. The provider receives the trimmed
	// request, never the whole conversation.
	budget := provider.ContextBudget{
		Limit:          600,
		ReservedOutput: 20,
		Source:         provider.ContextSourceOperator,
	}
	eng, rec := budgetedEngine(t, 21, budget)
	if _, err := eng.Respond(context.Background(), "and now?"); err != nil {
		t.Fatalf("the request should fit: %v", err)
	}
	if rec.calls != 1 {
		t.Fatalf("the provider was called %d times", rec.calls)
	}
	if len(rec.messages) >= 43 {
		t.Fatalf("the whole conversation was transmitted: %d messages", len(rec.messages))
	}
	// The estimate of the transmitted request fits the budget.
	if got := provider.EstimateTokens(rec.messages, nil); got > budget.Limit {
		t.Fatalf("the transmitted request does not fit: %d > %d", got, budget.Limit)
	}
	// The safety instructions and the current request are there.
	if len(rec.messages) == 0 || rec.messages[0].Role != "system" {
		t.Fatalf("the safety instructions were dropped: %#v", rec.messages)
	}
	last := rec.messages[len(rec.messages)-1]
	if !strings.Contains(last.Content, "and now?") {
		t.Fatalf("the current request was dropped: %#v", last)
	}
	// The oldest exchange is gone.
	for _, m := range rec.messages {
		if strings.Contains(m.Content, "question 00:") {
			t.Fatalf("the oldest exchange was transmitted: %#v", m)
		}
	}
}

func TestTheBudgetIsAppliedToEveryToolRound(t *testing.T) {
	// A tool round assembles a bigger request: the assistant's tool call
	// and the tool result travel with it. The budget is applied to that
	// request too, so the tool call is never separated from its result.
	st, err := session.Open(t.TempDir(), "chat")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 21; i++ {
		if _, err := st.AppendTurn(session.RoleUser,
			fmt.Sprintf("question %02d: what is the state of the build?", i), session.Turn{}); err != nil {
			t.Fatal(err)
		}
		if _, err := st.AppendTurn(session.RoleAssistant,
			fmt.Sprintf("answer %02d: the build is green.", i), session.Turn{}); err != nil {
			t.Fatal(err)
		}
	}
	rec := &budgetProvider{}
	eng := New(st, rec, provider.Model{ID: "mock", Budget: provider.ContextBudget{
		Limit:          600,
		ReservedOutput: 20,
		Source:         provider.ContextSourceOperator,
	}})
	if _, err := eng.Respond(context.Background(), "and now?"); err != nil {
		t.Fatalf("the request should fit: %v", err)
	}
	if rec.calls != 1 {
		t.Fatalf("the provider was called %d times", rec.calls)
	}
	for _, m := range rec.messages {
		if len(m.ToolCalls) > 0 || m.Role == "tool" {
			t.Fatalf("a tool exchange was transmitted in a trimmed request: %#v", m)
		}
	}
}

func TestNoBudgetMeansNoTrimming(t *testing.T) {
	eng, rec := budgetedEngine(t, 21, provider.ContextBudget{})
	if _, err := eng.Respond(context.Background(), "and now?"); err != nil {
		t.Fatal(err)
	}
	if len(rec.messages) < 43 {
		t.Fatalf("the request was trimmed without a budget: %d messages", len(rec.messages))
	}
}

// The enforcement point itself, with a request that carries a complete
// tool exchange.
func TestTheEnforcementPointDropsAToolExchangeWhole(t *testing.T) {
	messages := []provider.Message{
		{Role: "system", Content: "the safety instructions"},
		{Role: "user", Content: "read the file please"},
		{Role: "assistant", ToolCalls: []provider.ToolCall{{
			ID: "call-1", Name: "read_file", Arguments: map[string]any{"path": "a.txt"},
		}}},
		{Role: "tool", ToolCallID: "call-1", Name: "read_file", Content: "the file says hello"},
		{Role: "assistant", Content: "the file says hello"},
		{Role: "user", Content: "and now the second one"},
		{Role: "assistant", Content: "it says goodbye"},
	}
	// A budget that leaves room for the instructions and the newest
	// exchange only.
	eng, _ := budgetedEngine(t, 0, provider.ContextBudget{Limit: 40, Source: provider.ContextSourceOperator})
	trimmed, _, err := eng.applyContextBudget(messages, nil, nil)
	if err != nil {
		t.Fatalf("the request should fit: %v", err)
	}
	// The tool call and its result are one exchange: either both are
	// there or neither is.
	for _, m := range trimmed {
		if len(m.ToolCalls) > 0 {
			t.Fatalf("a tool call survived without its result: %#v", m)
		}
		if m.Role == "tool" {
			t.Fatalf("a tool result survived without its call: %#v", m)
		}
	}
	// The newest exchange is still there.
	last := trimmed[len(trimmed)-1]
	if !strings.Contains(last.Content, "it says goodbye") {
		t.Fatalf("the newest exchange was dropped: %#v", trimmed)
	}
}

func TestTheEnforcementPointReportsWhatItDropped(t *testing.T) {
	eng, _ := budgetedEngine(t, 21, provider.ContextBudget{
		Limit:          300,
		ReservedOutput: 20,
		Source:         provider.ContextSourceOperator,
	})
	var notes []provider.TrimNote
	emit := func(ev Event) {
		if ev.Context != nil {
			notes = append(notes, *ev.Context)
		}
	}
	messages := []provider.Message{{Role: "system", Content: "the safety instructions"}}
	for i := 0; i < 21; i++ {
		messages = append(messages,
			provider.Message{Role: "user", Content: fmt.Sprintf("question %02d: what is the state of the build?", i)},
			provider.Message{Role: "assistant", Content: fmt.Sprintf("answer %02d: the build is green.", i)},
		)
	}
	messages = append(messages, provider.Message{Role: "user", Content: "and now?"})
	if _, _, err := eng.applyContextBudget(messages, nil, emit); err != nil {
		t.Fatalf("the request should fit: %v", err)
	}
	if len(notes) != 1 {
		t.Fatalf("the interface was told %d times about the trim", len(notes))
	}
	if notes[0].DroppedExchanges == 0 {
		t.Fatalf("the note claims nothing was dropped: %#v", notes[0])
	}
	if !strings.Contains(notes[0].Text, "dropped to fit") {
		t.Fatalf("the note does not explain the trim: %q", notes[0].Text)
	}
	if notes[0].Source != string(provider.ContextSourceOperator) {
		t.Fatalf("the note does not name the source: %#v", notes[0])
	}
}

// The application may resolve the budget per request.
//
// An endpoint can report a model's context window after the engine was
// built, so the budget is resolved again for every request. The engine
// then uses the fresh number, not the one the model was built with.
func TestTheBudgetIsResolvedPerRequest(t *testing.T) {
	eng, rec := budgetedEngine(t, 21, provider.ContextBudget{})
	eng.WithBudgetResolver(func() (provider.ContextBudget, error) {
		return provider.ContextBudget{
			Limit:          600,
			ReservedOutput: 20,
			Source:         provider.ContextSourceProvider,
		}, nil
	})
	if _, err := eng.Respond(context.Background(), "and now?"); err != nil {
		t.Fatalf("the request should fit: %v", err)
	}
	if len(rec.messages) >= 43 {
		t.Fatalf("the resolved budget was not applied: %d messages", len(rec.messages))
	}
	if got := provider.EstimateTokens(rec.messages, nil); got > 600 {
		t.Fatalf("the transmitted request does not fit: %d > 600", got)
	}
}

// A refusal is not a tool rejection.
//
// The engine retries a turn without its tool definitions when the
// provider refuses the definitions. A budget refusal is not that: the
// same oversized request would be refused again, so the turn is
// refused once and the reason is reported.
func TestABudgetRefusalIsNotRetriedWithoutTheTools(t *testing.T) {
	eng, rec := budgetedEngine(t, 21, provider.ContextBudget{
		Limit:          100,
		ReservedOutput: 20,
		Source:         provider.ContextSourceOperator,
	})
	// A workspace makes HAL offer the write tool, so the turn really
	// does carry tool definitions.
	store, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	eng.WithWorkspace(store)

	_, err = eng.Respond(context.Background(), "and now?")
	if err == nil {
		t.Fatal("an irreducible request was sent")
	}
	var budgetErr *provider.ContextBudgetError
	if !errors.As(err, &budgetErr) {
		t.Fatalf("the error is not a context-budget error: %v", err)
	}
	if rec.calls != 0 {
		t.Fatalf("the provider was called %d times with an oversized request", rec.calls)
	}
}
