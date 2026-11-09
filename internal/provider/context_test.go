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
	// The operator's window is a total: the answer comes out of it.
	operator, err := BudgetFor(ContextMetadata{
		ContextWindow: 8192,
		Source:        ContextSourceOperator,
		Mode:          ContextModeConfigured,
		Verified:      true,
	}, OutputAllowance{Tokens: 4096, Configured: true})
	if err != nil {
		t.Fatalf("budget: %v", err)
	}
	if operator.Limit != 4096 {
		t.Fatalf("the answer was not reserved from the operator's window: %#v", operator)
	}
	if operator.ReservedOutput != 4096 {
		t.Fatalf("the reservation was lost: %#v", operator)
	}
	if operator.Source != ContextSourceOperator || operator.Mode != ContextModeConfigured {
		t.Fatalf("the provenance was lost: %#v", operator)
	}
	if !operator.Verified {
		t.Fatalf("an operator limit is verified: %#v", operator)
	}

	advertised, err := BudgetFor(ContextMetadata{
		ContextWindow: 200000,
		Source:        ContextSourceProvider,
		Mode:          ContextModeMax,
		Verified:      true,
	}, OutputAllowance{Tokens: 15000})
	if err != nil {
		t.Fatalf("budget: %v", err)
	}
	if advertised.Limit != 200000-15000 {
		t.Fatalf("the answer was not reserved from the advertised window: %#v", advertised)
	}
	if advertised.ReservedOutput != 15000 {
		t.Fatalf("the reservation was lost: %#v", advertised)
	}

	provisional, err := BudgetFor(ProvisionalContext(), OutputAllowance{Tokens: 15000})
	if err != nil {
		t.Fatalf("budget: %v", err)
	}
	if provisional.Source != ContextSourceProvisional {
		t.Fatalf("the provisional source was lost: %#v", provisional)
	}
	if provisional.Verified {
		t.Fatalf("a provisional budget is never verified: %#v", provisional)
	}
	// A provisional window is a conservative total, not an unlimited
	// history: the request is bounded even when nobody reported
	// anything, and the answer's room is the provisional one.
	if provisional.Total != ProvisionalContextWindow {
		t.Fatalf("the provisional total was lost: %#v", provisional)
	}
	if provisional.ReservedOutput != ProvisionalMaxOutputTokens {
		t.Fatalf("the provisional output cap was lost: %#v", provisional)
	}
	if provisional.Limit != ProvisionalContextWindow-ProvisionalMaxOutputTokens {
		t.Fatalf("the provisional total was not measured: %#v", provisional)
	}
	if provisional.Limit <= 0 {
		t.Fatalf("a provisional budget must still bound the request: %#v", provisional)
	}
	if !strings.Contains(DescribeBudget(provisional), "provisional") {
		t.Fatalf("the description does not name the source: %q", DescribeBudget(provisional))
	}
}

// The provisional record's own output cap is operational: it is the
// number the answer gets, and it reaches the wire.
//
// An allowance the operator configured is never replaced by it: their
// number is the one that counts.
func TestTheProvisionalOutputCapIsOperational(t *testing.T) {
	budget, err := BudgetFor(ProvisionalContext(), OutputAllowance{Tokens: 15000})
	if err != nil {
		t.Fatalf("budget: %v", err)
	}
	if budget.ReservedOutput != ProvisionalMaxOutputTokens {
		t.Fatalf("the provisional output cap was not applied: %#v", budget)
	}
	if cap := OutputCap(Model{Budget: budget}, 15000); cap != ProvisionalMaxOutputTokens {
		t.Fatalf("the wire cap = %d, want %d", cap, ProvisionalMaxOutputTokens)
	}

	configured, err := BudgetFor(ProvisionalContext(),
		OutputAllowance{Tokens: 2048, Configured: true})
	if err != nil {
		t.Fatalf("budget: %v", err)
	}
	if configured.ReservedOutput != 2048 {
		t.Fatalf("the operator's own cap was replaced: %#v", configured)
	}
	if cap := OutputCap(Model{Budget: configured}, 15000); cap != 2048 {
		t.Fatalf("the wire cap = %d, want 2048", cap)
	}
}

// The protocol's floor is the smallest cap the provider accepts, and it
// is never lowered.
func TestTheProtocolFloorIsNeverLowered(t *testing.T) {
	budget, err := BudgetFor(ContextMetadata{
		ContextWindow: 8192,
		Source:        ContextSourceProvider,
		Verified:      true,
	}, OutputAllowance{Tokens: 512, Minimum: 5120})
	if err != nil {
		t.Fatalf("budget: %v", err)
	}
	if budget.ReservedOutput != 5120 {
		t.Fatalf("the floor was lowered: %#v", budget)
	}
	if budget.Limit != 8192-5120 {
		t.Fatalf("the limit is not what is left of the window: %#v", budget)
	}
}

// The window is a total in every case: the operator's own limit, the
// provider's advertisement, a local runtime's allocation and a record
// verified earlier are all measured the same way.
func TestEveryWindowIsMeasuredAsATotal(t *testing.T) {
	for _, source := range []ContextSource{
		ContextSourceOperator,
		ContextSourceProvider,
		ContextSourceLocal,
		ContextSourceCached,
	} {
		budget, err := BudgetFor(ContextMetadata{
			ContextWindow: 8192,
			Source:        source,
			Verified:      true,
		}, OutputAllowance{Tokens: 4096, Configured: true})
		if err != nil {
			t.Fatalf("%s: %v", source, err)
		}
		if budget.Limit != 4096 {
			t.Errorf("%s: the limit = %d, want 4096", source, budget.Limit)
		}
		if budget.ReservedOutput != 4096 {
			t.Errorf("%s: the reservation = %d, want 4096", source, budget.ReservedOutput)
		}
	}
}

// A configured output allowance that does not fit inside the window is a
// contradiction between two numbers the operator wrote down. It is
// refused, with both numbers named.
func TestAConfiguredAllowanceThatDoesNotFitIsRefused(t *testing.T) {
	for _, reserved := range []int{8192, 9000, 100000} {
		budget, err := BudgetFor(ContextMetadata{
			ContextWindow: 8192,
			Source:        ContextSourceOperator,
			Verified:      true,
		}, OutputAllowance{Tokens: reserved, Configured: true})
		if err == nil {
			t.Fatalf("reserved %d: the contradiction was accepted: %#v", reserved, budget)
		}
		var allowance *ContextAllowanceError
		if !errors.As(err, &allowance) {
			t.Fatalf("reserved %d: the error is not an allowance error: %v", reserved, err)
		}
		if allowance.Total != 8192 || allowance.Reserved != reserved {
			t.Errorf("reserved %d: the error does not name both numbers: %v", reserved, err)
		}
		if budget.Limit != 0 {
			t.Errorf("reserved %d: a refused budget must not carry a limit: %#v", reserved, budget)
		}
	}
}

// A default that does not fit is reduced conservatively: a window must
// always leave room for the request.
func TestADefaultThatDoesNotFitIsReducedConservatively(t *testing.T) {
	budget, err := BudgetFor(ContextMetadata{
		ContextWindow: 8000,
		Source:        ContextSourceProvider,
		Verified:      true,
	}, OutputAllowance{Tokens: 15000})
	if err != nil {
		t.Fatalf("budget: %v", err)
	}
	if budget.Limit <= 0 {
		t.Fatalf("the window left no room for the request: %#v", budget)
	}
	if budget.ReservedOutput > 8000/2 {
		t.Fatalf("more than half the window was reserved: %#v", budget)
	}
	if budget.Limit != 8000-budget.ReservedOutput {
		t.Fatalf("the limit is not what is left of the window: %#v", budget)
	}
}

// A window that is known never produces a zero or a negative budget: a
// zero budget would be read as "no budget at all".
func TestAKnownWindowNeverProducesAZeroBudget(t *testing.T) {
	for _, total := range []int{2, 3, 7, 100, 8192} {
		budget, err := BudgetFor(ContextMetadata{
			ContextWindow: total,
			Source:        ContextSourceProvider,
			Verified:      true,
		}, OutputAllowance{Tokens: 15000})
		if err != nil {
			t.Fatalf("total %d: %v", total, err)
		}
		if budget.Limit <= 0 {
			t.Errorf("total %d: the budget = %d", total, budget.Limit)
		}
		if budget.Limit != total-budget.ReservedOutput {
			t.Errorf("total %d: the limit is not what is left of the window: %#v", total, budget)
		}
	}
}

// A window that cannot hold anything at all is refused with a clear
// error. It never becomes a zero budget, which would be read as "no
// budget at all" and would let an unbounded request through.
func TestAWindowThatCanHoldNothingIsRefused(t *testing.T) {
	budget, err := BudgetFor(ContextMetadata{
		ContextWindow: 1,
		Source:        ContextSourceProvider,
		Verified:      true,
	}, OutputAllowance{Tokens: 1})
	if err == nil {
		t.Fatalf("an impossible window was accepted: %#v", budget)
	}
	var allowance *ContextAllowanceError
	if !errors.As(err, &allowance) {
		t.Fatalf("the error is not an allowance error: %v", err)
	}
	if budget.Limit != 0 {
		t.Errorf("a refused budget must not carry a limit: %#v", budget)
	}
}

// A window that is not known at all leaves the provider's own limit in
// force, and the budget says so with a zero limit.
func TestAnUnknownWindowLeavesTheProvidersLimitInForce(t *testing.T) {
	budget, err := BudgetFor(ContextMetadata{Source: ContextSourceProvisional},
		OutputAllowance{Tokens: 4096})
	if err != nil {
		t.Fatalf("budget: %v", err)
	}
	if budget.Limit != 0 {
		t.Fatalf("the limit = %d, want 0", budget.Limit)
	}
	if !strings.Contains(DescribeBudget(budget), "provider's own context window") {
		t.Fatalf("the description does not say so: %q", DescribeBudget(budget))
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

// The wire cap is the budget's reservation.
//
// What the budget says the answer may use is what the provider is asked
// for, so the two can never disagree.
func TestTheWireCapIsTheBudgetsReservation(t *testing.T) {
	model := Model{Budget: ContextBudget{Limit: 4096, ReservedOutput: 1024}}
	if got := OutputCap(model, 15000); got != 1024 {
		t.Fatalf("the wire cap = %d, want the reservation 1024", got)
	}
	// Without a budget in force the model's own cap is used.
	if got := OutputCap(Model{MaxTokens: 77}, 15000); got != 77 {
		t.Fatalf("the wire cap = %d, want 77", got)
	}
	// And without one either, the protocol's default.
	if got := OutputCap(Model{}, 15000); got != 15000 {
		t.Fatalf("the wire cap = %d, want 15000", got)
	}
}

// The estimate carries a bounded safety margin.
//
// A normal HAL request measured about 5086 real tokens against 4115
// estimated, and dense code and non-English text undercount by more.
// The margin makes the number conservative. It is bounded: it is a
// documented factor and not a tokenizer.
func TestTheEstimateCarriesASafetyMargin(t *testing.T) {
	messages := []Message{{Role: "user", Content: strings.Repeat("word ", 100)}}
	raw := rawEstimateTokens(messages, nil)
	estimate := EstimateTokens(messages, nil)
	if estimate <= raw {
		t.Fatalf("the estimate = %d, the raw count = %d", estimate, raw)
	}
	if estimate != (raw*150+99)/100 {
		t.Fatalf("the estimate = %d, want the raw count times the margin", estimate)
	}
	if estimate > raw*2 {
		t.Fatalf("the margin is not bounded: %d against %d", estimate, raw)
	}
	if EstimateTokens(nil, nil) != 0 {
		t.Fatal("an empty request is not empty")
	}
}
