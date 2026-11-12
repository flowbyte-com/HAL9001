package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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

// compactWindow is the budget of a model with a window of its own: the
// limit is what is left for the request once the answer has been
// reserved.
func compactWindow(limit, total, reserved int) provider.ContextBudget {
	return provider.ContextBudget{
		Limit:          limit,
		Total:          total,
		ReservedOutput: reserved,
		Source:         provider.ContextSourceOperator,
		Mode:           provider.ContextModeConfigured,
		Verified:       true,
	}
}

// compactSizedExchange appends one exchange whose question and answer
// have the given sizes.
func compactSizedExchange(t *testing.T, st *session.Store, index, question, answer int) {
	t.Helper()
	turn := session.Turn{ID: session.NewTurnID()}
	if _, err := st.AppendTurn(session.RoleUser,
		fmt.Sprintf("question %d: %s", index, strings.Repeat("q", question)), turn); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AppendTurn(session.RoleAssistant,
		fmt.Sprintf("answer %d: %s", index, strings.Repeat("a", answer)), turn); err != nil {
		t.Fatal(err)
	}
}

// compactStoreCopy copies a session store into a directory of its own.
//
// A measurement that needs the request the provider receives must not
// change the conversation it measures, so the measurement runs on a
// copy.
func compactStoreCopy(t *testing.T, dir, name string) *session.Store {
	t.Helper()
	target := t.TempDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(target, entry.Name()), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	copied, err := session.Open(target, name)
	if err != nil {
		t.Fatal(err)
	}
	return copied
}

// compactWireSize is the size of the request the provider receives for
// an ordinary turn.
//
// It is measured on a copy of the conversation, so the measurement does
// not change the conversation: the number is the one the wire carries.
func compactWireSize(t *testing.T, dir, name string, budget provider.ContextBudget) int {
	t.Helper()
	copied := compactStoreCopy(t, dir, name)
	spy := &compactSpy{replies: []string{"## Objective\nthe answer"}}
	eng := compactEngine(t, copied, spy, budget)
	if _, err := eng.Respond(context.Background(), "what now?"); err != nil {
		t.Fatal(err)
	}
	wire := spy.last(t)
	if len(wire) == 0 {
		t.Fatal("the provider received nothing")
	}
	return provider.EstimateTokens(wire, nil)
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
	first := st.ReadCompaction()
	if first == nil {
		t.Fatal("the first compaction was not recorded")
	}
	if first.BeforeTokens <= 0 || first.AfterTokens <= 0 {
		t.Errorf("the first record carries no estimates: %+v", first)
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
	// The second record carries its own estimates, not the ones of the
	// first compaction.
	if record.BeforeTokens <= 0 || record.AfterTokens <= 0 {
		t.Errorf("the second record carries no estimates: %+v", record)
	}
	if record.BeforeTokens == first.BeforeTokens && record.AfterTokens == first.AfterTokens {
		t.Errorf("the second record repeats the estimates of the first: %+v", record)
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

// The record keeps the estimates the operator was shown.
//
// A record without them cannot say what the compaction did to the
// context, and the report is gone as soon as the screen is cleared.
func TestTheRecordKeepsTheEstimatesTheOperatorWasShown(t *testing.T) {
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
	record := st.ReadCompaction()
	if record == nil {
		t.Fatal("the compaction was not recorded")
	}
	if record.BeforeTokens != report.BeforeTokens {
		t.Errorf("the record says %d tokens before, the report said %d",
			record.BeforeTokens, report.BeforeTokens)
	}
	if record.AfterTokens != report.AfterTokens {
		t.Errorf("the record says %d tokens after, the report said %d",
			record.AfterTokens, report.AfterTokens)
	}
	// The numbers describe the stored context: the compaction made it
	// smaller, and it fits the budget it was made against.
	if record.AfterTokens >= record.BeforeTokens {
		t.Errorf("the record does not show a smaller context: %d → %d",
			record.BeforeTokens, record.AfterTokens)
	}
	if record.AfterTokens > report.Limit {
		t.Errorf("the compacted context needs about %d tokens, the budget allows %d",
			record.AfterTokens, report.Limit)
	}
}

// The estimates are the assembled context, not the tokens that were
// transmitted.
//
// The cost of a compaction is the provider's own usage, and it is
// reported beside the estimates. The two are never the same number.
func TestTheEstimatesAreNotTheProvidersUsage(t *testing.T) {
	st, err := session.Open(t.TempDir(), "chat")
	if err != nil {
		t.Fatal(err)
	}
	compactConversation(t, st, 6, 40)
	// The provider reports a usage of its own for the compaction.
	spy := &compactSpy{
		replies: []string{"## Objective\nship the parser"},
		usage:   provider.Usage{PromptTokens: 11, CompletionTokens: 13},
	}
	eng := compactEngine(t, st, spy, compactBudget(4000))
	report, err := eng.Compact(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if report.InputTokens != 11 || report.OutputTokens != 13 {
		t.Fatalf("the provider's usage was not reported: %+v", report)
	}
	record := st.ReadCompaction()
	if record == nil {
		t.Fatal("the compaction was not recorded")
	}
	// The record holds the estimates of the context, not the usage.
	if record.BeforeTokens == 24 || record.AfterTokens == 24 {
		t.Errorf("the record holds the provider's usage: %+v", record)
	}
	if record.BeforeTokens <= 0 || record.AfterTokens <= 0 {
		t.Errorf("the record holds no estimates: %+v", record)
	}
}

// A second compaction measures the context HAL assembles now.
//
// After a compaction, the context HAL assembles is the summary of that
// compaction together with the exchanges that are kept. The whole
// original transcript is not sent any more: measuring it would
// describe a context that never reaches the provider.
func TestASecondCompactionMeasuresTheContextThatIsAssembledNow(t *testing.T) {
	dir := t.TempDir()
	st, err := session.Open(dir, "chat")
	if err != nil {
		t.Fatal(err)
	}
	compactConversation(t, st, 8, 800)
	spy := &compactSpy{replies: []string{
		"## Objective\nthe first summary",
		"## Objective\nthe second summary",
	}}
	eng := compactEngine(t, st, spy, compactBudget(20000))
	first, err := eng.Compact(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	// The conversation grows after the first compaction.
	compactConversation(t, st, 3, 800)

	// The context that is assembled now is measured on a copy of the
	// conversation, so the measurement does not change it. It is the
	// request the provider receives for the next ordinary turn,
	// without the question that turn adds at the end.
	copied := compactStoreCopy(t, dir, "chat")
	measure := &compactSpy{replies: []string{"## Objective\nthe answer"}}
	measuring := compactEngine(t, copied, measure, compactBudget(20000))
	if _, err := measuring.Respond(context.Background(), "next?"); err != nil {
		t.Fatal(err)
	}
	wire := measure.last(t)
	if len(wire) < 2 {
		t.Fatal("the provider received nothing")
	}
	assembled := provider.EstimateTokens(wire[:len(wire)-1], nil)

	second, err := eng.Compact(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if second.BeforeTokens != assembled {
		t.Errorf("the second compaction measured %d tokens, the assembled context is %d",
			second.BeforeTokens, assembled)
	}
	// It is not the whole original transcript: the first measurement
	// described that transcript, and the transcript only grew since.
	if second.BeforeTokens >= first.BeforeTokens {
		t.Errorf("the second compaction measured the whole transcript again: %d, then %d",
			first.BeforeTokens, second.BeforeTokens)
	}
}

// The estimates are the assembled context, and the request that goes on
// the wire is fitted to the budget.
//
// The budget trims the request after the context is assembled, so the
// two numbers are not the same. The record holds the assembled one, and
// it says so.
func TestTheEstimatesAreTheAssembledContextAndNotTheRequestOnTheWire(t *testing.T) {
	dir := t.TempDir()
	st, err := session.Open(dir, "chat")
	if err != nil {
		t.Fatal(err)
	}
	// A conversation whose assembled context does not fit the window:
	// the older exchanges and the recent ones are both large.
	budget := compactWindow(7168, 8192, 1024)
	for i := 0; i < 4; i++ {
		compactSizedExchange(t, st, i, 2000, 2000)
	}
	for i := 4; i < 6; i++ {
		compactSizedExchange(t, st, i, 8000, 8000)
	}
	spy := &compactSpy{replies: []string{"## Objective\nship the parser"}}
	eng := compactEngine(t, st, spy, budget)
	report, err := eng.Compact(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	record := st.ReadCompaction()
	if record == nil {
		t.Fatal("the compaction was not recorded")
	}
	// The assembled context did not fit the window, and the record says
	// how big it was.
	if record.BeforeTokens <= budget.Limit {
		t.Errorf("the assembled context fitted the window: %d tokens, the limit is %d",
			record.BeforeTokens, budget.Limit)
	}
	// The request that goes on the wire is fitted to the window.
	wire := compactWireSize(t, dir, "chat", budget)
	if wire > budget.Limit {
		t.Errorf("the request on the wire is %d tokens, the limit is %d", wire, budget.Limit)
	}
	// The record holds the assembled context, not the request on the
	// wire: the two are different numbers, and the record is the
	// larger one.
	if record.AfterTokens == wire {
		t.Errorf("the record holds the size of the request on the wire: %d", wire)
	}
	if record.AfterTokens < wire {
		t.Errorf("the record holds %d tokens, the request on the wire is %d",
			record.AfterTokens, wire)
	}
	if report.AfterTokens != record.AfterTokens {
		t.Errorf("the report says %d tokens, the record says %d",
			report.AfterTokens, record.AfterTokens)
	}
}

// A compaction can shrink the assembled context while the request that
// goes on the wire grows.
//
// The trim drops the oldest exchanges and stops as soon as the request
// fits, so a conversation whose newest exchanges are small can be
// trimmed to a very small request. After a compaction the summary is
// protected and is never dropped, so the request that follows carries
// it — and it can be larger than the request that came before.
//
// This is why the two numbers are never described as a reduction of the
// tokens that were transmitted.
func TestTheCompactionCanReduceTheAssembledContextWhileTheRequestOnTheWireGrows(t *testing.T) {
	dir := t.TempDir()
	st, err := session.Open(dir, "chat")
	if err != nil {
		t.Fatal(err)
	}
	budget := compactWindow(7168, 8192, 1024)
	// The older exchanges are large enough to be summarised, the
	// second-newest is large enough to be dropped by the trim, and the
	// newest is small.
	for i := 0; i < 7; i++ {
		compactSizedExchange(t, st, i, 1000, 15000)
	}
	compactSizedExchange(t, st, 7, 100, 24000)
	compactSizedExchange(t, st, 8, 100, 100)

	// The request that goes on the wire before the compaction.
	before := compactWireSize(t, dir, "chat", budget)

	// The compaction. The summary is protected and is never dropped.
	summary := "## Objective\n" + strings.Repeat("the older work was done and verified. ", 60)
	spy := &compactSpy{replies: []string{summary}}
	eng := compactEngine(t, st, spy, budget)
	report, err := eng.Compact(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	record := st.ReadCompaction()
	if record == nil {
		t.Fatal("the compaction was not recorded")
	}
	// The request that goes on the wire after the compaction.
	after := compactWireSize(t, dir, "chat", budget)

	t.Logf("assembled context: %d → %d tokens; request on the wire: %d → %d tokens",
		record.BeforeTokens, record.AfterTokens, before, after)
	// The assembled context got smaller.
	if record.AfterTokens >= record.BeforeTokens {
		t.Errorf("the assembled context did not get smaller: %d → %d",
			record.BeforeTokens, record.AfterTokens)
	}
	// The request on the wire grew: the summary is protected and the
	// trim had dropped far more before the compaction.
	if after <= before {
		t.Errorf("the request on the wire did not grow: %d → %d", before, after)
	}
	// Both requests fit the window: the trim did its work.
	if before > budget.Limit || after > budget.Limit {
		t.Errorf("a request on the wire did not fit the window: %d and %d, the limit is %d",
			before, after, budget.Limit)
	}
	// The numbers the operator sees are the assembled ones, and they
	// are not the size of the request that was transmitted.
	if report.BeforeTokens != record.BeforeTokens || report.AfterTokens != record.AfterTokens {
		t.Errorf("the report and the record disagree: %+v and %+v", report, record)
	}
	if record.AfterTokens == after {
		t.Errorf("the record holds the size of the request on the wire: %d", after)
	}
}
