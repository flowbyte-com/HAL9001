package provider

import (
	"context"
	"testing"
)

// The reasoning control of a model is a three-way answer: HAL knows it
// takes one, HAL knows it takes none, or HAL knows nothing about it.
//
// The third case matters: "HAL does not know" and "this model has no
// reasoning control" are different facts, and the interface says which
// one it is.
func TestEffortCapabilityIsThreeWay(t *testing.T) {
	cases := []struct {
		model string
		want  EffortSupport
	}{
		{"MiniMax-M3.1-Flash", EffortSupported},
		{"minimax-m2", EffortSupported},
		{"deepseek-reasoner", EffortSupported},
		{"claude-sonnet-4-5", EffortSupported},
		{"gpt-4o", EffortUnsupported},
		{"gpt-4-turbo", EffortUnsupported},
		{"llama-3.1-70b", EffortUnsupported},
		{"", EffortUnsupported},
		// A Claude model outside the families that take a thinking
		// block is unknown to HAL, not known to have none.
		{"claude-3-5-sonnet", EffortUnknown},
		{"some-model-nobody-documented", EffortUnknown},
	}
	for _, c := range cases {
		if got := EffortSupportFor(c.model); got != c.want {
			t.Errorf("EffortSupportFor(%q) = %v, want %v", c.model, got, c.want)
		}
	}
}

// The levels offered follow the model's own family: a level the
// provider does not document is never offered.
func TestTheOfferedLevelsFollowTheModel(t *testing.T) {
	cases := []struct {
		model string
		want  []string
	}{
		{"MiniMax-M3.1-Flash", []string{"low", "medium", "high", "xhigh", "max"}},
		{"deepseek-reasoner", []string{"low", "high", "max"}},
		{"claude-sonnet-4-5", []string{"low", "medium", "high", "xhigh", "max"}},
	}
	for _, c := range cases {
		got, ok := SupportedEffortLevels(c.model)
		if !ok {
			t.Errorf("SupportedEffortLevels(%q) reported no levels", c.model)
			continue
		}
		if len(got) != len(c.want) {
			t.Errorf("SupportedEffortLevels(%q) = %v, want %v", c.model, got, c.want)
			continue
		}
		for i, level := range c.want {
			if got[i] != level {
				t.Errorf("SupportedEffortLevels(%q) = %v, want %v", c.model, got, c.want)
				break
			}
		}
	}
	// A model HAL knows nothing about offers nothing.
	if levels, ok := SupportedEffortLevels("some-model-nobody-documented"); ok || levels != nil {
		t.Errorf("an unknown model offered %v", levels)
	}
}

// The level a model starts at is the model's own documented default.
//
// A Claude model starts with no extended thinking: its default is the
// absence of a thinking block, and nothing changes on the wire until the
// operator asks for one.
func TestTheDefaultLevelFollowsTheModel(t *testing.T) {
	cases := map[string]string{
		"MiniMax-M3.1-Flash": "medium",
		"deepseek-reasoner":  "high",
		"claude-sonnet-4-5":  "",
		"gpt-4o":             "",
		"":                   "",
	}
	for model, want := range cases {
		if got := DefaultEffortFor(model); got != want {
			t.Errorf("DefaultEffortFor(%q) = %q, want %q", model, got, want)
		}
	}
}

// A family never claims a longer name that merely starts the same way.
func TestAFamilyNeverClaimsALongerName(t *testing.T) {
	for _, model := range []string{"minimax-music", "deepseeking", "minimax-music-2"} {
		if EffortCapable(model) {
			t.Errorf("%q was claimed by a family", model)
		}
	}
	for _, model := range []string{"minimax-m2", "minimax-m", "deepseek-chat"} {
		if !EffortCapable(model) {
			t.Errorf("%q was not claimed by its family", model)
		}
	}
}

// The OpenAI-compatible wire carries the reasoning_effort parameter for
// the families whose documentation names it, and nothing else.
//
// DeepSeek takes the mapped value: its own table has low, high and max,
// so a HAL level of medium rides at high.
func TestOpenAIWireSendsTheDocumentedParameter(t *testing.T) {
	cases := []struct {
		model string
		level string
		want  any
		send  bool
	}{
		{"MiniMax-M3.1-Flash", "xhigh", "xhigh", true},
		{"MiniMax-M3.1-Flash", "medium", "medium", true},
		{"deepseek-reasoner", "low", "low", true},
		{"deepseek-reasoner", "high", "high", true},
		{"deepseek-reasoner", "max", "max", true},
		{"deepseek-reasoner", "medium", "high", true},
		{"deepseek-reasoner", "xhigh", "high", true},
		{"deepseek-reasoner", "turbo", nil, false},
		// A Claude model takes its reasoning control on the Messages
		// wire; the OpenAI dialect sends nothing for it.
		{"claude-sonnet-4-5", "high", nil, false},
		// A model nobody documented gets nothing.
		{"gpt-4o", "high", nil, false},
		{"some-model-nobody-documented", "high", nil, false},
	}
	for _, c := range cases {
		got, ok := OpenAIEffortFor(c.model, c.level)
		if ok != c.send {
			t.Errorf("OpenAIEffortFor(%q, %q) sent = %v, want %v", c.model, c.level, ok, c.send)
			continue
		}
		if ok && got != c.want {
			t.Errorf("OpenAIEffortFor(%q, %q) = %q, want %v", c.model, c.level, got, c.want)
		}
	}
}

// The request carries the parameter for a supported model and nothing
// for any other.
func TestOpenAIWireCarriesTheEffortParameter(t *testing.T) {
	for _, c := range []struct {
		model string
		level string
		want  string
	}{
		{"MiniMax-M3.1-Flash", "high", "high"},
		{"deepseek-reasoner", "medium", "high"},
		{"claude-sonnet-4-5", "high", ""},
		{"gpt-4o", "high", ""},
	} {
		var captured capturedRequest
		srv := streamServer(t, []string{contentLine("ok")}, &captured)
		p := &OpenAICompatible{Endpoint: srv.URL, APIKey: "k"}
		if _, err := p.ChatStream(context.Background(),
			[]Message{{Role: "user", Content: "x"}},
			Model{ID: c.model, Effort: c.level},
			func(Delta) error { return nil }); err != nil {
			t.Fatal(err)
		}
		got, present := captured.body["reasoning_effort"]
		if c.want == "" {
			if present {
				t.Errorf("model %q carried reasoning_effort %v", c.model, got)
			}
		} else if !present || got != c.want {
			t.Errorf("model %q carried reasoning_effort %v, want %q", c.model, got, c.want)
		}
		srv.Close()
	}
}

// The Messages wire carries the extended-thinking block for the models
// whose documentation names it.
//
// The budget follows the level, and a model with no level carries no
// block at all: the operator's own choice is what turns thinking on.
func TestAnthropicWireSendsTheThinkingBlock(t *testing.T) {
	f := newAnthropicFixture(t)
	client := &AnthropicCompatible{Endpoint: f.server.URL, APIKey: "k"}

	if _, err := client.Chat(context.Background(),
		[]Message{{Role: "user", Content: "hello"}},
		Model{ID: "claude-sonnet-4-5"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.last().Body["thinking"]; ok {
		t.Errorf("a model with no level carried a thinking block: %v", f.last().Body)
	}

	for _, c := range []struct {
		level string
		want  float64
	}{
		{"low", 1024},
		{"medium", 4096},
		{"high", 8192},
		{"xhigh", 16384},
		{"max", 32768},
	} {
		if _, err := client.Chat(context.Background(),
			[]Message{{Role: "user", Content: "hello"}},
			Model{ID: "claude-sonnet-4-5", Effort: c.level}); err != nil {
			t.Fatal(err)
		}
		thinking, ok := f.last().Body["thinking"].(map[string]any)
		if !ok {
			t.Fatalf("level %q carried no thinking block: %v", c.level, f.last().Body)
		}
		if thinking["type"] != "enabled" {
			t.Errorf("level %q: thinking = %v", c.level, thinking)
		}
		if got, _ := thinking["budget_tokens"].(float64); got != c.want {
			t.Errorf("level %q: budget = %v, want %v", c.level, thinking["budget_tokens"], c.want)
		}
	}
}

// A model outside the thinking families carries no block, whatever level
// it was given.
func TestAnthropicWireRefusesThinkingForOtherModels(t *testing.T) {
	f := newAnthropicFixture(t)
	client := &AnthropicCompatible{Endpoint: f.server.URL, APIKey: "k"}
	if _, err := client.Chat(context.Background(),
		[]Message{{Role: "user", Content: "hello"}},
		Model{ID: "gpt-4o", Effort: "high"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.last().Body["thinking"]; ok {
		t.Errorf("a model outside the thinking families carried a block: %v", f.last().Body)
	}
}

// DeepSeek on the Messages wire takes thinking as a plain toggle and
// the effort as output_config.effort.
func TestAnthropicWireSendsDeepSeekEffort(t *testing.T) {
	f := newAnthropicFixture(t)
	client := &AnthropicCompatible{Endpoint: f.server.URL, APIKey: "k"}
	if _, err := client.Chat(context.Background(),
		[]Message{{Role: "user", Content: "hello"}},
		Model{ID: "deepseek-reasoner", Effort: "medium"}); err != nil {
		t.Fatal(err)
	}
	body := f.last().Body
	if _, ok := body["thinking"]; !ok {
		t.Errorf("no thinking block: %v", body)
	}
	config, ok := body["output_config"].(map[string]any)
	if !ok {
		t.Fatalf("no output_config: %v", body)
	}
	if config["effort"] != "high" {
		t.Errorf("effort = %v, want high", config["effort"])
	}
}

// The thinking floor follows the level: the Messages API refuses a cap
// that does not exceed the thinking budget.
func TestTheThinkingFloorFollowsTheLevel(t *testing.T) {
	if got := AnthropicThinkingFloor("claude-sonnet-4-5", ""); got != 0 {
		t.Errorf("the floor without a level = %d, want 0", got)
	}
	if got := AnthropicThinkingFloor("claude-sonnet-4-5", "high"); got != 8192+anthropicThinkingMargin {
		t.Errorf("the floor = %d", got)
	}
	if got := AnthropicThinkingFloor("gpt-4o", "high"); got != 0 {
		t.Errorf("the floor for a model without a block = %d, want 0", got)
	}
}
