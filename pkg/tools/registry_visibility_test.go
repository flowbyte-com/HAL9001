package tools

import (
	"context"
	"testing"
)

type visibilityTestTool struct{ name string }

func (t visibilityTestTool) Name() string               { return t.name }
func (t visibilityTestTool) Description() string        { return "visibility test tool" }
func (t visibilityTestTool) Parameters() map[string]any { return map[string]any{"type": "object"} }
func (t visibilityTestTool) Execute(context.Context, map[string]any) *ToolResult {
	return NewToolResult(t.name)
}

func TestVisibilityProfileFiltersVisibleAndCallableTools(t *testing.T) {
	registry := NewToolRegistry()
	registry.Register(visibilityTestTool{name: "alpha"})
	registry.Register(visibilityTestTool{name: "beta"})
	registry.Register(visibilityTestTool{name: "gamma"})

	registry.SetVisibleTools([]string{"alpha"})
	if got := registry.List(); len(got) != 1 || got[0] != "alpha" {
		t.Fatalf("List() = %v, want [alpha]", got)
	}
	if _, ok := registry.Get("alpha"); !ok {
		t.Fatal("visible tool is not callable")
	}
	if _, ok := registry.Get("beta"); ok {
		t.Fatal("hidden tool is callable")
	}
	if got := registry.GetDefinitions(); len(got) != 1 {
		t.Fatalf("GetDefinitions() = %v", got)
	}
	if got := registry.ToProviderDefs(); len(got) != 1 || got[0].Function.Name != "alpha" {
		t.Fatalf("ToProviderDefs() = %v", got)
	}
	if got := registry.GetSummaries(); len(got) != 1 {
		t.Fatalf("GetSummaries() = %v", got)
	}
	if !registry.HasRegistered("beta") {
		t.Fatal("hidden tool should remain installed")
	}

	registry.SetVisibleTools(nil)
	if got := registry.List(); len(got) != 3 {
		t.Fatalf("List() after clearing profile = %v", got)
	}
}

func TestVisibilityProfileBlocksLateOverwriteButStoresHiddenTools(t *testing.T) {
	registry := NewToolRegistry()
	registry.Register(visibilityTestTool{name: "read"})
	registry.SetVisibleTools([]string{"read"})

	registry.Register(visibilityTestTool{name: "late"})
	if _, ok := registry.Get("late"); ok {
		t.Fatal("late tool became callable")
	}
	if !registry.HasRegistered("late") {
		t.Fatal("late tool should remain installed")
	}

	registry.Register(visibilityTestTool{name: "read"})
	visible, ok := registry.Get("read")
	if !ok {
		t.Fatal("visible tool disappeared")
	}
	if visible.(visibilityTestTool).name != "read" {
		t.Fatalf("visible tool was replaced: %#v", visible)
	}

	clone := registry.Clone()
	clone.Register(visibilityTestTool{name: "clone-late"})
	if _, ok := clone.Get("clone-late"); ok {
		t.Fatal("clone lost the visibility profile")
	}
	if got := clone.List(); len(got) != 1 || got[0] != "read" {
		t.Fatalf("clone List() = %v", got)
	}
}
