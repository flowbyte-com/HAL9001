package provider

import "testing"

// The level the provider receives is not always the level HAL stores.
//
// A family that documents fewer levels maps the others: a stored
// "medium" rides at "high" on the DeepSeek wire. The interface has to
// show what the provider will get, because a footer that names one
// level while the wire carries another is a disagreement the operator
// cannot see through.
func TestTheEffectiveLevelIsTheOneTheProviderGets(t *testing.T) {
	cases := []struct {
		model string
		level string
		want  string
	}{
		{"deepseek-flash", "medium", "high"},
		{"deepseek-flash", "xhigh", "high"},
		{"deepseek-flash", "low", "low"},
		{"deepseek-flash", "max", "max"},
		{"MiniMax-M3.1-Flash", "medium", "medium"},
		{"MiniMax-M3.1-Flash", "xhigh", "xhigh"},
		// A level that is not documented is not sent at all.
		{"deepseek-flash", "turbo", ""},
		{"MiniMax-M3.1-Flash", "", ""},
		// A model with no documented control keeps the level it was
		// given: nothing is mapped onto nothing.
		{"gpt-4o", "high", "high"},
	}
	for _, c := range cases {
		if got := EffectiveEffort(c.model, c.level); got != c.want {
			t.Errorf("EffectiveEffort(%q, %q) = %q, want %q", c.model, c.level, got, c.want)
		}
	}
}

// The Claude table names the families that take a thinking block.
//
// A version number in a name says nothing about what the API accepts:
// the extended-thinking block arrived with Claude 3.7, and the models
// before it take none.
func TestTheClaudeTableNamesItsFamilies(t *testing.T) {
	for _, model := range []string{
		"claude-3-7-sonnet", "claude-3-7-sonnet-20250219",
		"claude-opus-4-20250514", "claude-opus-4-1", "claude-sonnet-4-5",
		"claude-haiku-4-5", "claude-4-sonnet",
	} {
		if !anthropicThinkingCapable(model) {
			t.Errorf("%q was not claimed as a thinking model", model)
		}
	}
	for _, model := range []string{
		"claude-2.4", "claude-2.1", "claude-instant-1.2",
		"claude-3-opus", "claude-3-sonnet", "claude-3-haiku",
		"claude-3-5-sonnet", "claude-3-5-haiku",
	} {
		if anthropicThinkingCapable(model) {
			t.Errorf("%q was claimed as a thinking model", model)
		}
		if !anthropicNoThinking(model) {
			t.Errorf("%q was not reported as a model that takes no thinking", model)
		}
	}
}
