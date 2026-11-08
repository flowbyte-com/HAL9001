package provider

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

// The context budget.
//
// The budget is enforced on the fully assembled request. These tests
// hold HAL to the rules that make that safe: the safety instructions
// and the newest exchange survive, a tool call is never separated from
// its result, and a request that cannot be reduced is refused with an
// explanation instead of being truncated.

// budgetMessages builds a conversation of n exchanges after a system
// instruction.
func budgetMessages(n int) []Message {
	messages := []Message{{Role: "system", Content: "system instructions that must never be dropped"}}
	for i := 0; i < n; i++ {
		messages = append(messages,
			Message{Role: "user", Content: fmt.Sprintf("question %02d: what is the state of the build?", i)},
			Message{Role: "assistant", Content: fmt.Sprintf("answer %02d: the build is green.", i)},
		)
	}
	return messages
}

func TestTrimToBudgetBoundsTheRequest(t *testing.T) {
	messages := budgetMessages(41)
	budget := ContextBudget{Limit: 100, ReservedOutput: 20, Source: ContextSourceOperator}
	trimmed, report, err := TrimToBudget(messages, nil, budget)
	if err != nil {
		t.Fatalf("TrimToBudget: %v", err)
	}
	if report.DroppedExchanges == 0 {
		t.Fatalf("nothing was dropped: %#v", report)
	}
	if len(trimmed) >= len(messages) {
		t.Fatalf("the request was not bounded: %d of %d messages", len(trimmed), len(messages))
	}
	if report.EstimatedTokens > report.Limit {
		t.Fatalf("the request still does not fit: %d > %d", report.EstimatedTokens, report.Limit)
	}
	// The safety instructions survive.
	if trimmed[0].Role != "system" {
		t.Fatalf("the safety instructions were dropped: %#v", trimmed[0])
	}
	// The newest exchange survives, whole.
	joined := ""
	for _, m := range trimmed {
		joined += m.Content + "\n"
	}
	if !strings.Contains(joined, "question 40") {
		t.Fatalf("the newest exchange was dropped: %#v", trimmed)
	}
	if !strings.Contains(joined, "answer 40") {
		t.Fatalf("the newest exchange was broken: %#v", trimmed)
	}
	// The oldest exchange is gone.
	for _, m := range trimmed {
		if strings.Contains(m.Content, "question 00:") {
			t.Fatalf("the oldest exchange survived: %#v", m)
		}
	}
}

func TestTrimToBudgetKeepsTheRequestWhenItFits(t *testing.T) {
	messages := budgetMessages(3)
	budget := ContextBudget{Limit: 100000, Source: ContextSourceProvider}
	trimmed, report, err := TrimToBudget(messages, nil, budget)
	if err != nil {
		t.Fatalf("TrimToBudget: %v", err)
	}
	if report.DroppedExchanges != 0 {
		t.Fatalf("a request that fits was trimmed: %#v", report)
	}
	if len(trimmed) != len(messages) {
		t.Fatalf("the request was changed: %d of %d messages", len(trimmed), len(messages))
	}
}

func TestTrimToBudgetNeverSplitsAToolCallFromItsResult(t *testing.T) {
	messages := []Message{
		{Role: "system", Content: "instructions"},
		{Role: "user", Content: "read the file please"},
		{Role: "assistant", Content: "", ToolCalls: []ToolCall{{ID: "call-1", Name: "read_file", Arguments: map[string]any{"path": "a.txt"}}}},
		{Role: "tool", ToolCallID: "call-1", Name: "read_file", Content: "the file says hello"},
		{Role: "assistant", Content: "the file says hello"},
		{Role: "user", Content: "now read the second one"},
		{Role: "assistant", Content: "it says goodbye"},
	}
	budget := ContextBudget{Limit: 40, Source: ContextSourceOperator}
	trimmed, report, err := TrimToBudget(messages, nil, budget)
	if err != nil {
		t.Fatalf("TrimToBudget: %v", err)
	}
	if report.DroppedExchanges == 0 {
		t.Fatalf("nothing was dropped: %#v", report)
	}
	// A tool call and its result are one exchange: either both are
	// there or neither is.
	for _, m := range trimmed {
		if len(m.ToolCalls) > 0 {
			t.Fatalf("a tool call survived without its result: %#v", m)
		}
		if m.Role == "tool" {
			t.Fatalf("a tool result survived without its call: %#v", m)
		}
	}
}

func TestTrimToBudgetKeepsACompleteToolExchangeWhenItFits(t *testing.T) {
	messages := []Message{
		{Role: "system", Content: "instructions"},
		{Role: "user", Content: "read the file please"},
		{Role: "assistant", ToolCalls: []ToolCall{{ID: "call-1", Name: "read_file"}}},
		{Role: "tool", ToolCallID: "call-1", Name: "read_file", Content: "hello"},
	}
	budget := ContextBudget{Limit: 1000, Source: ContextSourceProvider}
	trimmed, _, err := TrimToBudget(messages, nil, budget)
	if err != nil {
		t.Fatalf("TrimToBudget: %v", err)
	}
	if len(trimmed) != len(messages) {
		t.Fatalf("the exchange was broken: %#v", trimmed)
	}
}

func TestTrimToBudgetRefusesAnIrreducibleRequest(t *testing.T) {
	// The newest message alone does not fit: the request cannot be
	// reduced, so it is refused rather than truncated.
	messages := []Message{
		{Role: "system", Content: "instructions"},
		{Role: "user", Content: strings.Repeat("a very long question ", 200)},
	}
	budget := ContextBudget{Limit: 50, Source: ContextSourceOperator}
	trimmed, report, err := TrimToBudget(messages, nil, budget)
	if err == nil {
		t.Fatalf("an oversized request was accepted: %#v", trimmed)
	}
	var budgetErr *ContextBudgetError
	if !errors.As(err, &budgetErr) {
		t.Fatalf("the error is not a context-budget error: %v", err)
	}
	if budgetErr.Estimated <= budgetErr.Limit {
		t.Fatalf("the estimate is wrong: %#v", budgetErr)
	}
	if !strings.Contains(err.Error(), "estimated") {
		t.Fatalf("the error does not say the count is an estimate: %v", err)
	}
	if !strings.Contains(err.Error(), "never dropped") {
		t.Fatalf("the error does not explain the refusal: %v", err)
	}
	if trimmed != nil {
		t.Fatalf("a refused request was trimmed anyway: %#v", trimmed)
	}
	if report.Limit != 50 {
		t.Fatalf("the report does not carry the limit: %#v", report)
	}
}

func TestTrimToBudgetRefusesWhenTheInstructionsAloneDoNotFit(t *testing.T) {
	messages := []Message{{Role: "system", Content: strings.Repeat("instruction ", 500)}}
	budget := ContextBudget{Limit: 10, Source: ContextSourceOperator}
	_, _, err := TrimToBudget(messages, nil, budget)
	var budgetErr *ContextBudgetError
	if !errors.As(err, &budgetErr) {
		t.Fatalf("the error is not a context-budget error: %v", err)
	}
	if !strings.Contains(err.Error(), "must never be dropped") {
		t.Fatalf("the error does not explain the refusal: %v", err)
	}
}

func TestTrimToBudgetCountsTheToolDefinitions(t *testing.T) {
	messages := []Message{
		{Role: "system", Content: "instructions"},
		{Role: "user", Content: "hello"},
	}
	tools := []Tool{{
		Name:        "read_file",
		Description: strings.Repeat("reads a file. ", 100),
		Parameters:  `{"type":"object","properties":{"path":{"type":"string"}}}`,
	}}
	// The request fits without the tools and does not fit with them.
	small := ContextBudget{Limit: EstimateTokens(messages, nil), Source: ContextSourceOperator}
	if _, _, err := TrimToBudget(messages, nil, small); err != nil {
		t.Fatalf("the request should fit without the tools: %v", err)
	}
	if _, _, err := TrimToBudget(messages, tools, small); err == nil {
		t.Fatal("the tool definitions were not counted")
	}
}

func TestBudgetForKeepsTheProvenance(t *testing.T) {
	operator := BudgetFor(ContextMetadata{
		ContextWindow: 100,
		Source:        ContextSourceOperator,
		Mode:          ContextModeConfigured,
		Verified:      true,
	}, 20)
	if operator.Limit != 100 {
		t.Fatalf("the operator's limit was not kept: %#v", operator)
	}
	if operator.Source != ContextSourceOperator || operator.Mode != ContextModeConfigured {
		t.Fatalf("the provenance was lost: %#v", operator)
	}
	if !operator.Verified {
		t.Fatalf("an operator limit is verified: %#v", operator)
	}

	advertised := BudgetFor(ContextMetadata{
		ContextWindow: 200000,
		Source:        ContextSourceProvider,
		Mode:          ContextModeMax,
		Verified:      true,
	}, 15000)
	if advertised.Limit != 200000-15000 {
		t.Fatalf("the answer was not reserved from the advertised window: %#v", advertised)
	}
	if advertised.ReservedOutput != 15000 {
		t.Fatalf("the reservation was lost: %#v", advertised)
	}

	provisional := BudgetFor(ProvisionalContext(), 15000)
	if provisional.Source != ContextSourceProvisional {
		t.Fatalf("the provisional source was lost: %#v", provisional)
	}
	if provisional.Verified {
		t.Fatalf("a provisional budget is never verified: %#v", provisional)
	}
	// A guess is reported, never enforced: the provider's own window
	// governs until the operator sets a limit or the endpoint reports
	// one.
	if provisional.Limit != 0 {
		t.Fatalf("a guess must not be enforced: %#v", provisional)
	}
	if !strings.Contains(DescribeBudget(provisional), "provisional") {
		t.Fatalf("the description does not name the source: %q", DescribeBudget(provisional))
	}
}

func TestBudgetForNeverHoldsBackMoreThanHalfOfAWindow(t *testing.T) {
	// A small advertised window must still leave room for the request.
	budget := BudgetFor(ContextMetadata{
		ContextWindow: 8000,
		Source:        ContextSourceProvider,
		Verified:      true,
	}, 15000)
	if budget.Limit <= 0 {
		t.Fatalf("the window left no room for the request: %#v", budget)
	}
	if budget.ReservedOutput > 8000/2 {
		t.Fatalf("more than half the window was reserved: %#v", budget)
	}
}

func TestDescribeBudgetNamesTheSource(t *testing.T) {
	text := DescribeBudget(ContextBudget{
		Limit:          17768,
		ReservedOutput: 15000,
		Source:         ContextSourceProvisional,
	})
	for _, want := range []string{"17768", "15000", "provisional"} {
		if !strings.Contains(text, want) {
			t.Fatalf("the description does not mention %q: %q", want, text)
		}
	}
	if strings.Contains(text, "exact") {
		t.Fatalf("the description claims an exact count: %q", text)
	}
}

func TestDroppedExchangesNoteCountsExchanges(t *testing.T) {
	budget := ContextBudget{Limit: 100, Source: ContextSourceOperator}
	note := DroppedExchangesNote(TrimReport{DroppedExchanges: 1}, budget)
	if !strings.Contains(note, "1 older exchange dropped") {
		t.Fatalf("note = %q", note)
	}
	note = DroppedExchangesNote(TrimReport{DroppedExchanges: 3}, budget)
	if !strings.Contains(note, "3 older exchanges dropped") {
		t.Fatalf("note = %q", note)
	}
	if DroppedExchangesNote(TrimReport{}, budget) != "" {
		t.Fatal("a trim that dropped nothing must not produce a note")
	}
}
