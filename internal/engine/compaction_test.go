package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/flowbyte-com/HAL9001/internal/provider"
	"github.com/flowbyte-com/HAL9001/internal/session"
)

// The manual compaction.
//
// A compaction replaces the older part of a conversation with a summary
// in the requests that follow. These tests watch the request the
// provider receives and the transcript on disk: the summary must stand
// in for the exchanges it covers, the transcript must not change, and
// every request the compaction itself makes must fit the same budget the
// ordinary turns obey.

// compactSpy records every request the provider receives and answers
// with a scripted summary.
type compactSpy struct {
	requests [][]provider.Message
	replies  []string
	usage    provider.Usage
	err      error
}

func (p *compactSpy) Name() string { return "compact" }

func (p *compactSpy) Chat(ctx context.Context, messages []provider.Message, _ provider.Model) (provider.Response, error) {
	if err := ctx.Err(); err != nil {
		return provider.Response{}, err
	}
	p.requests = append(p.requests, append([]provider.Message(nil), messages...))
	if p.err != nil {
		return provider.Response{}, p.err
	}
	if len(p.replies) == 0 {
		return provider.Response{Content: "## Objective\nnothing"}, nil
	}
	reply := p.replies[0]
	if len(p.replies) > 1 {
		p.replies = p.replies[1:]
	}
	return provider.Response{Content: reply, Stats: provider.TurnStats{Usage: p.usage}}, nil
}

// last returns the last request the provider received.
func (p *compactSpy) last(t *testing.T) []provider.Message {
	t.Helper()
	if len(p.requests) == 0 {
		t.Fatal("the provider was never called")
	}
	return p.requests[len(p.requests)-1]
}

// text is the whole content of a request.
func requestText(messages []provider.Message) string {
	var b strings.Builder
	for _, m := range messages {
		b.WriteString(m.Role)
		b.WriteString(": ")
		b.WriteString(m.Content)
		b.WriteString("\n")
	}
	return b.String()
}

// compactBudget is a budget with room for an ordinary conversation.
func compactBudget(limit int) provider.ContextBudget {
	return provider.ContextBudget{
		Limit:          limit,
		Total:          limit + 1000,
		ReservedOutput: 1000,
		Source:         provider.ContextSourceOperator,
		Mode:           provider.ContextModeConfigured,
		Verified:       true,
	}
}

// compactConversation fills a session with exchanges.
//
// Each exchange is the operator's question and HAL's answer, and each
// answer is long enough to be worth compacting.
func compactConversation(t *testing.T, st *session.Store, exchanges, size int) {
	t.Helper()
	for i := 0; i < exchanges; i++ {
		turn := session.Turn{ID: session.NewTurnID()}
		if _, err := st.AppendTurn(session.RoleUser,
			fmt.Sprintf("question %d: %s", i, strings.Repeat("q", size)), turn); err != nil {
			t.Fatal(err)
		}
		if _, err := st.AppendTurn(session.RoleAssistant,
			fmt.Sprintf("answer %d: %s", i, strings.Repeat("a", size)), turn); err != nil {
			t.Fatal(err)
		}
	}
}

// compactEngine builds an engine with a spy provider and a budget.
func compactEngine(t *testing.T, st *session.Store, spy provider.Provider, budget provider.ContextBudget) *Engine {
	t.Helper()
	return New(st, spy, provider.Model{ID: "mock", Budget: budget}).
		WithSystemPrompt("You are HAL9001.")
}

func TestCompactionReplacesTheOlderExchangesInTheRequest(t *testing.T) {
	st, err := session.Open(t.TempDir(), "chat")
	if err != nil {
		t.Fatal(err)
	}
	compactConversation(t, st, 5, 40)
	spy := &compactSpy{replies: []string{"## Objective\nship the parser\n## Next actions\nwrite the tests"}}
	eng := compactEngine(t, st, spy, compactBudget(4000))

	report, err := eng.Compact(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if report.CoveredExchanges != 3 {
		t.Errorf("the covered exchanges = %d, want 3", report.CoveredExchanges)
	}
	if report.KeptExchanges != 2 {
		t.Errorf("the kept exchanges = %d, want 2", report.KeptExchanges)
	}
	// The next request carries the summary and not the older exchanges.
	reply, err := eng.Respond(context.Background(), "what is next?")
	if err != nil {
		t.Fatal(err)
	}
	if reply == "" {
		t.Fatal("the turn produced no answer")
	}
	text := requestText(spy.last(t))
	if !strings.Contains(text, "ship the parser") {
		t.Errorf("the request must carry the summary:\n%s", text)
	}
	if strings.Contains(text, "question 0:") || strings.Contains(text, "question 1:") {
		t.Errorf("the request must not carry the covered exchanges:\n%s", text)
	}
	if !strings.Contains(text, "question 4:") {
		t.Errorf("the recent conversation must stay available:\n%s", text)
	}
}

func TestTheCompactedConversationIsDistinguishable(t *testing.T) {
	st, err := session.Open(t.TempDir(), "chat")
	if err != nil {
		t.Fatal(err)
	}
	compactConversation(t, st, 5, 40)
	spy := &compactSpy{replies: []string{"## Objective\nship the parser"}}
	eng := compactEngine(t, st, spy, compactBudget(4000))
	if _, err := eng.Compact(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.Respond(context.Background(), "next?"); err != nil {
		t.Fatal(err)
	}
	text := requestText(spy.last(t))
	if !strings.Contains(text, "Compacted conversation") {
		t.Errorf("the summary must be labelled:\n%s", text)
	}
	if !strings.Contains(text, "not a verified record") {
		t.Errorf("the summary must not look like a verified record:\n%s", text)
	}
}

func TestCompactionLeavesTheTranscriptUnchanged(t *testing.T) {
	st, err := session.Open(t.TempDir(), "chat")
	if err != nil {
		t.Fatal(err)
	}
	compactConversation(t, st, 5, 40)
	before, err := st.History()
	if err != nil {
		t.Fatal(err)
	}
	spy := &compactSpy{replies: []string{"## Objective\nship the parser"}}
	eng := compactEngine(t, st, spy, compactBudget(4000))
	if _, err := eng.Compact(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	after, err := st.History()
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Fatalf("the transcript changed: %d messages became %d", len(before), len(after))
	}
	for i := range before {
		if before[i].Content != after[i].Content {
			t.Errorf("message %d changed:\n%q\n%q", i, before[i].Content, after[i].Content)
		}
		if before[i].Role != after[i].Role {
			t.Errorf("message %d changed its role", i)
		}
	}
}

func TestTheVerifiedEvidenceSurvivesTheCompaction(t *testing.T) {
	st, err := session.Open(t.TempDir(), "chat")
	if err != nil {
		t.Fatal(err)
	}
	// An older exchange with a verified tool execution.
	turn := session.Turn{ID: session.NewTurnID()}
	if _, err := st.AppendTurn(session.RoleUser, "write the file", turn); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AppendTurn(session.RoleAssistant, "written", turn); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AppendTurn(session.RoleAssistant, "the file was written", session.Turn{
		ID: turn.ID,
		Tools: []session.ToolExecution{{
			TurnID: turn.ID, Tool: "write_file", Action: "write_file",
			Target: "notes.txt", Mode: "666", Status: session.ToolSuccess, Summary: "wrote 13 bytes",
		}},
	}); err != nil {
		t.Fatal(err)
	}
	compactConversation(t, st, 4, 40)
	spy := &compactSpy{replies: []string{"## Objective\nship the parser"}}
	eng := compactEngine(t, st, spy, compactBudget(4000))
	if _, err := eng.Compact(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.Respond(context.Background(), "what ran?"); err != nil {
		t.Fatal(err)
	}
	text := requestText(spy.last(t))
	// The verified record of what ran is carried separately from the
	// summary, and a compaction never drops it.
	if !strings.Contains(text, "write_file") {
		t.Errorf("the verified evidence must survive the compaction:\n%s", text)
	}
}

func TestTheCompactionMakesTheContextSmaller(t *testing.T) {
	st, err := session.Open(t.TempDir(), "chat")
	if err != nil {
		t.Fatal(err)
	}
	compactConversation(t, st, 8, 400)
	spy := &compactSpy{replies: []string{"## Objective\nship the parser"}}
	eng := compactEngine(t, st, spy, compactBudget(20000))
	report, err := eng.Compact(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if report.AfterTokens >= report.BeforeTokens {
		t.Errorf("the compaction did not make the context smaller: %d → %d",
			report.BeforeTokens, report.AfterTokens)
	}
	if report.AfterTokens <= 0 || report.BeforeTokens <= 0 {
		t.Errorf("the estimates are missing: %d → %d", report.BeforeTokens, report.AfterTokens)
	}
}

func TestAFailedCompactionLeavesTheContextUnchanged(t *testing.T) {
	st, err := session.Open(t.TempDir(), "chat")
	if err != nil {
		t.Fatal(err)
	}
	compactConversation(t, st, 5, 40)
	spy := &compactSpy{err: errors.New("the provider refused")}
	eng := compactEngine(t, st, spy, compactBudget(4000))
	if _, err := eng.Compact(context.Background(), nil); err == nil {
		t.Fatal("the compaction must fail")
	}
	if record := st.ReadCompaction(); record != nil {
		t.Error("a failed compaction must not be committed")
	}
	// The next request still carries the whole conversation.
	if _, err := eng.Respond(context.Background(), "next?"); err == nil {
		t.Fatal("the provider must fail here too")
	}
	text := requestText(spy.last(t))
	if !strings.Contains(text, "question 0:") {
		t.Errorf("the conversation must be untouched:\n%s", text)
	}
}

func TestACancelledCompactionStoresNothing(t *testing.T) {
	st, err := session.Open(t.TempDir(), "chat")
	if err != nil {
		t.Fatal(err)
	}
	compactConversation(t, st, 5, 40)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	spy := &compactSpy{replies: []string{"## Objective\nship the parser"}}
	eng := compactEngine(t, st, spy, compactBudget(4000))
	if _, err := eng.Compact(ctx, nil); err == nil {
		t.Fatal("a cancelled compaction must fail")
	}
	if record := st.ReadCompaction(); record != nil {
		t.Error("a cancelled compaction must not be committed")
	}
}

func TestTheCompactionIsWrittenInBoundedSteps(t *testing.T) {
	st, err := session.Open(t.TempDir(), "chat")
	if err != nil {
		t.Fatal(err)
	}
	// A conversation that does not fit in one request.
	compactConversation(t, st, 8, 4000)
	budget := compactBudget(4000)
	spy := &compactSpy{replies: []string{"## Objective\nship the parser"}}
	eng := compactEngine(t, st, spy, budget)
	report, err := eng.Compact(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(spy.requests) < 2 {
		t.Fatalf("the compaction must take more than one request, took %d", len(spy.requests))
	}
	if report.Steps != len(spy.requests) {
		t.Errorf("the report says %d steps, the provider saw %d requests",
			report.Steps, len(spy.requests))
	}
	// Every request the compaction made fits the budget.
	for i, request := range spy.requests {
		estimate := provider.EstimateTokens(request, nil)
		if estimate > budget.Limit {
			t.Errorf("request %d needs about %d tokens, the budget allows %d",
				i, estimate, budget.Limit)
		}
	}
}

func TestTheCompactionFitsTheProvisionalEightThousandWindow(t *testing.T) {
	st, err := session.Open(t.TempDir(), "chat")
	if err != nil {
		t.Fatal(err)
	}
	compactConversation(t, st, 8, 4000)
	// The window of an unknown model: 8192 in all, 1024 of them
	// reserved for the answer.
	budget := provider.ContextBudget{
		Limit:          provider.ProvisionalContextWindow - provider.ProvisionalMaxOutputTokens,
		Total:          provider.ProvisionalContextWindow,
		ReservedOutput: provider.ProvisionalMaxOutputTokens,
		Source:         provider.ContextSourceProvisional,
		Mode:           provider.ContextModeProvisional,
	}
	spy := &compactSpy{replies: []string{"## Objective\nship the parser"}}
	eng := compactEngine(t, st, spy, budget)
	report, err := eng.Compact(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(spy.requests) < 2 {
		t.Fatalf("the compaction must take more than one request, took %d", len(spy.requests))
	}
	for i, request := range spy.requests {
		estimate := provider.EstimateTokens(request, nil)
		if estimate > budget.Limit {
			t.Errorf("request %d needs about %d tokens, the budget allows %d",
				i, estimate, budget.Limit)
		}
	}
	// The context that is left fits the window too.
	if report.AfterTokens > budget.Limit {
		t.Errorf("the compacted context needs about %d tokens, the budget allows %d",
			report.AfterTokens, budget.Limit)
	}
}

func TestAnExchangeTooLargeToSummariseIsRefused(t *testing.T) {
	st, err := session.Open(t.TempDir(), "chat")
	if err != nil {
		t.Fatal(err)
	}
	// One exchange that does not fit the budget at all.
	turn := session.Turn{ID: session.NewTurnID()}
	if _, err := st.AppendTurn(session.RoleUser, strings.Repeat("x", 40000), turn); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AppendTurn(session.RoleAssistant, strings.Repeat("y", 40000), turn); err != nil {
		t.Fatal(err)
	}
	compactConversation(t, st, 3, 40)
	spy := &compactSpy{replies: []string{"## Objective\nship the parser"}}
	eng := compactEngine(t, st, spy, compactBudget(2000))
	if _, err := eng.Compact(context.Background(), nil); err == nil {
		t.Fatal("the compaction must be refused")
	}
	if record := st.ReadCompaction(); record != nil {
		t.Error("a refused compaction must not be committed")
	}
}

func TestTheCompactionOffersNoTools(t *testing.T) {
	st, err := session.Open(t.TempDir(), "chat")
	if err != nil {
		t.Fatal(err)
	}
	compactConversation(t, st, 5, 40)
	spy := &compactSpy{replies: []string{"## Objective\nship the parser"}}
	eng := compactEngine(t, st, spy, compactBudget(4000))
	if _, err := eng.Compact(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	for _, request := range spy.requests {
		for _, m := range request {
			if m.Role == "tool" {
				t.Error("a compaction request must not carry tool results")
			}
		}
	}
}

func TestThereIsNothingToCompactInAShortConversation(t *testing.T) {
	st, err := session.Open(t.TempDir(), "chat")
	if err != nil {
		t.Fatal(err)
	}
	compactConversation(t, st, 2, 40)
	spy := &compactSpy{replies: []string{"## Objective\nship the parser"}}
	eng := compactEngine(t, st, spy, compactBudget(4000))
	if _, err := eng.Compact(context.Background(), nil); err == nil {
		t.Fatal("the compaction must be refused")
	}
	if len(spy.requests) != 0 {
		t.Errorf("the provider must not be asked, saw %d requests", len(spy.requests))
	}
	if record := st.ReadCompaction(); record != nil {
		t.Error("nothing must be committed")
	}
}

func TestTheCompactionReportsWhatItCost(t *testing.T) {
	st, err := session.Open(t.TempDir(), "chat")
	if err != nil {
		t.Fatal(err)
	}
	compactConversation(t, st, 5, 40)
	spy := &compactSpy{replies: []string{"## Objective\nship the parser"}}
	spy.usage = provider.Usage{PromptTokens: 1200, CompletionTokens: 300}
	eng := compactEngine(t, st, spy, compactBudget(4000))
	report, err := eng.Compact(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if report.InputTokens != 1200 || report.OutputTokens != 300 {
		t.Errorf("the cost = %d in, %d out", report.InputTokens, report.OutputTokens)
	}
}

func TestTheCompactionOnlyHappensWhenTheOperatorAsks(t *testing.T) {
	st, err := session.Open(t.TempDir(), "chat")
	if err != nil {
		t.Fatal(err)
	}
	compactConversation(t, st, 5, 40)
	spy := &compactSpy{replies: []string{"## Objective\nship the parser", "an answer"}}
	eng := compactEngine(t, st, spy, compactBudget(4000))
	// An ordinary turn asks the provider exactly once and compacts
	// nothing.
	if _, err := eng.Respond(context.Background(), "hello"); err != nil {
		t.Fatal(err)
	}
	if len(spy.requests) != 1 {
		t.Fatalf("the provider was asked %d times", len(spy.requests))
	}
	if record := st.ReadCompaction(); record != nil {
		t.Error("an ordinary turn must not compact anything")
	}
}

func TestASecondCompactionReplacesTheFirst(t *testing.T) {
	st, err := session.Open(t.TempDir(), "chat")
	if err != nil {
		t.Fatal(err)
	}
	compactConversation(t, st, 6, 40)
	spy := &compactSpy{replies: []string{"## Objective\nthe first summary", "## Objective\nthe second summary"}}
	eng := compactEngine(t, st, spy, compactBudget(4000))
	if _, err := eng.Compact(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	compactConversation(t, st, 4, 40)
	if _, err := eng.Compact(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	record := st.ReadCompaction()
	if record == nil {
		t.Fatal("the compaction was not recorded")
	}
	if !strings.Contains(record.Summary, "the second summary") {
		t.Errorf("the record = %q", record.Summary)
	}
	if record.CoveredMessages <= 8 {
		t.Errorf("the covered messages = %d, want more than the first cut", record.CoveredMessages)
	}
	// The request carries the newest summary and the recent exchanges.
	if _, err := eng.Respond(context.Background(), "next?"); err != nil {
		t.Fatal(err)
	}
	text := requestText(spy.last(t))
	if !strings.Contains(text, "the second summary") {
		t.Errorf("the request must carry the newest summary:\n%s", text)
	}
	if strings.Contains(text, "question 0:") {
		t.Errorf("the request must not carry the covered exchanges:\n%s", text)
	}
}
