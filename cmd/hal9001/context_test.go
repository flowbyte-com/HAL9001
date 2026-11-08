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
// advertised maximum never becomes a guess, and a guess is never
// labelled verified.

func budgetApp(cfg config.Config, stored config.FileConfig) *app {
	return &app{cfg: cfg, stored: stored}
}

func TestTheOperatorLimitBecomesTheBudget(t *testing.T) {
	a := budgetApp(config.Config{
		Provider: "openai-compatible",
		Model:    "m",
		Models:   []config.ModelRecord{{ID: "m", ContextWindow: 100}},
	}, config.FileConfig{})
	model := a.modelFor()
	if model.ContextWindow != 100 {
		t.Fatalf("the operator's limit was lost: %#v", model)
	}
	if model.Budget.Limit != 100 {
		t.Fatalf("the budget does not carry the operator's limit: %#v", model.Budget)
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
	budget := a.contextBudget("m", a.operatorContextLimit("m"), a.outputTokens())
	if budget.Source != provider.ContextSourceProvisional {
		t.Fatalf("the source is wrong: %#v", budget)
	}
	if budget.Verified {
		t.Fatalf("a provisional budget is never verified: %#v", budget)
	}
	// A guess is reported, never enforced: the provider's own window
	// governs until the operator sets a limit.
	if budget.Limit != 0 {
		t.Fatalf("a guess must not be enforced: %#v", budget)
	}
}

func TestTheAdvertisedWindowBecomesTheBudget(t *testing.T) {
	a := budgetApp(config.Config{Provider: "openai-compatible", Model: "m", MaxTokens: 1000}, config.FileConfig{})
	a.recordContext("m", provider.ContextMetadata{
		ContextWindow: 200000,
		Source:        provider.ContextSourceProvider,
		Mode:          provider.ContextModeMax,
		Verified:      true,
	})
	budget := a.contextBudget("m", a.operatorContextLimit("m"), a.outputTokens())
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
	a.recordContext("m", provider.ContextMetadata{
		ContextWindow: 8192,
		Source:        provider.ContextSourceLocal,
		Mode:          provider.ContextModeLocal,
		Verified:      true,
	})
	budget := a.contextBudget("m", a.operatorContextLimit("m"), a.outputTokens())
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
		Models:    []config.ModelRecord{{ID: "m", ContextWindow: 4096}},
	}, config.FileConfig{})
	a.recordContext("m", provider.ContextMetadata{
		ContextWindow: 200000,
		Source:        provider.ContextSourceProvider,
		Verified:      true,
	})
	budget := a.contextBudget("m", a.operatorContextLimit("m"), a.outputTokens())
	if budget.Source != provider.ContextSourceOperator {
		t.Fatalf("the source is wrong: %#v", budget)
	}
	if budget.Limit != 4096 {
		t.Fatalf("the operator's limit was not the budget: %#v", budget)
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
			a.recordContext("m", provider.ContextMetadata{
				ContextWindow: 8192,
				Source:        provider.ContextSourceProvider,
				Mode:          provider.ContextModeMax,
				Verified:      true,
			})
		}
	}()
	for i := 0; i < 200; i++ {
		_ = a.contextBudget("m", a.operatorContextLimit("m"), a.outputTokens())
	}
	<-done
	budget := a.contextBudget("m", a.operatorContextLimit("m"), a.outputTokens())
	if budget.Source != provider.ContextSourceProvider {
		t.Fatalf("the recorded metadata was lost: %#v", budget)
	}
}

func TestTheBudgetDescriptionNamesTheSource(t *testing.T) {
	a := budgetApp(config.Config{
		Provider: "openai-compatible",
		Model:    "m",
		Models:   []config.ModelRecord{{ID: "m", ContextWindow: 4096}},
	}, config.FileConfig{})
	text := a.contextBudgetDescription()
	if text == "" {
		t.Fatal("the description is empty")
	}
	if !strings.Contains(text, "4096") || !strings.Contains(text, "operator") {
		t.Fatalf("the description does not name the budget: %q", text)
	}
}
