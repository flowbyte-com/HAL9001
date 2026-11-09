package main

import (
	"strings"
	"testing"

	"github.com/flowbyte-com/HAL9001/internal/config"
	"github.com/flowbyte-com/HAL9001/internal/provider"
)

// The resolved context budget.
//
// The application resolves the budget; the engine enforces it. These
// tests hold the resolution to the rules: the operator's own limit wins,
// a local runtime's allocation beats an advertised maximum, an
// advertised maximum never becomes a guess, a guess is never labelled
// verified, and the window is a total in every case.

func budgetApp(cfg config.Config, stored config.FileConfig) *app {
	return &app{cfg: cfg, stored: stored}
}

// budgetKey is the identity of a model of the unnamed endpoint, for a
// configuration that has no endpoint records.
func budgetKey(model string) contextKey {
	return contextKey{model: model}
}

// allowance is the room the answer may use, for a test that does not
// care where the number came from.
func allowance(tokens int) provider.OutputAllowance {
	return provider.OutputAllowance{Tokens: tokens}
}

func TestTheOperatorLimitBecomesTheBudget(t *testing.T) {
	a := budgetApp(config.Config{
		Provider: "openai-compatible",
		Model:    "m",
		Models:   []config.ModelRecord{{ID: "m", ContextWindow: 8192, ContextMode: config.ContextManual}},
	}, config.FileConfig{})
	model := a.modelFor()
	if model.ContextWindow != 8192 {
		t.Fatalf("the operator's limit was lost: %#v", model)
	}
	// The window is a total: the room for the answer comes out of it.
	if model.Budget.Limit != 8192-model.Budget.ReservedOutput {
		t.Fatalf("the budget does not carry the operator's window: %#v", model.Budget)
	}
	if model.Budget.Source != provider.ContextSourceOperator {
		t.Fatalf("the provenance was lost: %#v", model.Budget)
	}
	if !model.Budget.Verified {
		t.Fatalf("an operator limit is verified: %#v", model.Budget)
	}
}

func TestWithoutAKnownWindowTheBudgetIsProvisional(t *testing.T) {
	a := budgetApp(config.Config{Provider: "openai-compatible", Model: "m"}, config.FileConfig{})
	budget, err := a.contextBudget(budgetKey("m"), a.operatorContextLimit("m"), allowance(4096))
	if err != nil {
		t.Fatalf("budget: %v", err)
	}
	if budget.Source != provider.ContextSourceProvisional {
		t.Fatalf("the source is wrong: %#v", budget)
	}
	if budget.Verified {
		t.Fatalf("a provisional budget is never verified: %#v", budget)
	}
	// A provisional window is a conservative total: the conversation is
	// bounded even when nobody reported anything, and the number is
	// never presented as a fact about the model.
	if budget.Limit <= 0 {
		t.Fatalf("a provisional budget must still bound the request: %#v", budget)
	}
	if budget.Limit != provider.ProvisionalContextWindow-budget.ReservedOutput {
		t.Fatalf("the provisional total was not measured: %#v", budget)
	}
}

func TestTheAdvertisedWindowBecomesTheBudget(t *testing.T) {
	a := budgetApp(config.Config{Provider: "openai-compatible", Model: "m", MaxTokens: 1000}, config.FileConfig{})
	a.recordContext(budgetKey("m"), 0, provider.ContextMetadata{
		ContextWindow: 200000,
		Source:        provider.ContextSourceProvider,
		Mode:          provider.ContextModeMax,
		Verified:      true,
	})
	budget, err := a.contextBudget(budgetKey("m"), a.operatorContextLimit("m"), allowance(1000))
	if err != nil {
		t.Fatalf("budget: %v", err)
	}
	if budget.Source != provider.ContextSourceProvider {
		t.Fatalf("the source is wrong: %#v", budget)
	}
	if budget.Limit != 200000-1000 {
		t.Fatalf("the answer was not reserved from the advertised window: %#v", budget)
	}
	if budget.ReservedOutput != 1000 {
		t.Fatalf("the reservation was lost: %#v", budget)
	}
}

func TestALocalAllocationBeatsTheAdvertisedMaximum(t *testing.T) {
	a := budgetApp(config.Config{Provider: "openai-compatible", Model: "m", MaxTokens: 1000}, config.FileConfig{})
	a.recordContext(budgetKey("m"), 0, provider.ContextMetadata{
		ContextWindow: 8192,
		Source:        provider.ContextSourceLocal,
		Mode:          provider.ContextModeLocal,
		Verified:      true,
	})
	budget, err := a.contextBudget(budgetKey("m"), a.operatorContextLimit("m"), allowance(1000))
	if err != nil {
		t.Fatalf("budget: %v", err)
	}
	if budget.Source != provider.ContextSourceLocal {
		t.Fatalf("the source is wrong: %#v", budget)
	}
	if budget.Limit >= 8192 {
		t.Fatalf("the allocation was not the budget in force: %#v", budget)
	}
}

func TestTheOperatorLimitBeatsEverything(t *testing.T) {
	a := budgetApp(config.Config{
		Provider:  "openai-compatible",
		Model:     "m",
		MaxTokens: 1000,
		Models:    []config.ModelRecord{{ID: "m", ContextWindow: 4096, ContextMode: config.ContextManual}},
	}, config.FileConfig{})
	a.recordContext(budgetKey("m"), 0, provider.ContextMetadata{
		ContextWindow: 200000,
		Source:        provider.ContextSourceProvider,
		Verified:      true,
	})
	budget, err := a.contextBudget(budgetKey("m"), a.operatorContextLimit("m"), allowance(1000))
	if err != nil {
		t.Fatalf("budget: %v", err)
	}
	if budget.Source != provider.ContextSourceOperator {
		t.Fatalf("the source is wrong: %#v", budget)
	}
	if budget.Limit != 4096-1000 {
		t.Fatalf("the operator's window was not the budget: %#v", budget)
	}
}

// The recorded metadata is written by the background lookup and read by
// the resolution. Both must be safe at the same time.
func TestTheRecordedMetadataIsSafeToWriteAndReadAtOnce(t *testing.T) {
	a := budgetApp(config.Config{Provider: "openai-compatible", Model: "m"}, config.FileConfig{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 200; i++ {
			a.recordContext(budgetKey("m"), 0, provider.ContextMetadata{
				ContextWindow: 8192,
				Source:        provider.ContextSourceProvider,
				Mode:          provider.ContextModeMax,
				Verified:      true,
			})
		}
	}()
	for i := 0; i < 200; i++ {
		_, _ = a.contextBudget(budgetKey("m"), a.operatorContextLimit("m"), allowance(1000))
	}
	<-done
	budget, err := a.contextBudget(budgetKey("m"), a.operatorContextLimit("m"), allowance(1000))
	if err != nil {
		t.Fatalf("budget: %v", err)
	}
	if budget.Source != provider.ContextSourceProvider {
		t.Fatalf("the recorded metadata was lost: %#v", budget)
	}
}

func TestTheBudgetDescriptionNamesTheSource(t *testing.T) {
	a := budgetApp(config.Config{
		Provider:  "openai-compatible",
		Model:     "m",
		MaxTokens: 1024,
		Models:    []config.ModelRecord{{ID: "m", ContextWindow: 4096, ContextMode: config.ContextManual}},
	}, config.FileConfig{})
	text := a.contextBudgetDescription()
	if text == "" {
		t.Fatal("the description is empty")
	}
	// The window is a total: 4096 in all, 1024 of it reserved for the
	// answer, 3072 left for the request.
	for _, want := range []string{"3072", "1024", "operator"} {
		if !strings.Contains(text, want) {
			t.Fatalf("the description does not name %q: %q", want, text)
		}
	}
}

// The operator can see the whole budget.
//
// The total the window holds, the room held back for the answer, the
// room the request may use and the provenance of the numbers are all
// inspectable, and a provisional budget is recognisable as one.
func TestTheOperatorCanInspectTheBudget(t *testing.T) {
	a := budgetApp(config.Config{
		Provider: "openai-compatible", Model: "m",
	}, config.FileConfig{})
	budget, err := a.contextBudget(budgetKey("m"), 0, allowance(1024))
	if err != nil {
		t.Fatalf("budget: %v", err)
	}
	if budget.Total != provider.ProvisionalContextWindow {
		t.Fatalf("the total = %d, want %d", budget.Total, provider.ProvisionalContextWindow)
	}
	if budget.ReservedOutput != provider.ProvisionalMaxOutputTokens {
		t.Fatalf("the reservation = %d, want %d",
			budget.ReservedOutput, provider.ProvisionalMaxOutputTokens)
	}
	if budget.Limit != provider.ProvisionalContextWindow-provider.ProvisionalMaxOutputTokens {
		t.Fatalf("the effective budget = %d", budget.Limit)
	}
	text := provider.DescribeBudget(budget)
	for _, want := range []string{"8192", "1024", "7168", "provisional"} {
		if !strings.Contains(text, want) {
			t.Fatalf("the description does not name %q: %q", want, text)
		}
	}
}
