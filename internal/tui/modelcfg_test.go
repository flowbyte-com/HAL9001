package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// runCommand types a slash command into the composer and presses enter.
func runCommand(m Model, command string) Model {
	m = typeText(m, command)
	return pressKey(m, tea.KeyEnter, 0, "")
}

func TestModelAddKeepsTheActiveModelAndSaysSo(t *testing.T) {
	var endpoint, id string
	hooks := Hooks{
		AddModel: func(e string, spec ModelSpec) error {
			endpoint, id = e, spec.ID
			return nil
		},
	}
	m := NewWithHooks(&scriptEngine{}, hooks)
	m = runCommand(m, "/model add minimax deepseek-chat")
	if endpoint != "minimax" || id != "deepseek-chat" {
		t.Fatalf("the hook was called with (%q, %q)", endpoint, id)
	}
	if !strings.Contains(m.notice, "active model is unchanged") {
		t.Errorf("the notice does not say that the active model is unchanged: %q", m.notice)
	}
}

func TestModelAddReportsAFailure(t *testing.T) {
	hooks := Hooks{
		AddModel: func(string, ModelSpec) error { return errOnboard("the model already exists") },
	}
	m := NewWithHooks(&scriptEngine{}, hooks)
	m = runCommand(m, "/model add minimax deepseek-chat")
	if !strings.Contains(m.notice, "already exists") {
		t.Errorf("the notice = %q", m.notice)
	}
}

func TestModelUseSwitchesTheScopedModel(t *testing.T) {
	var endpoint, id string
	hooks := Hooks{
		SwitchModel: func(e, m string) (Engine, error) {
			endpoint, id = e, m
			return nil, nil
		},
	}
	m := NewWithHooks(&scriptEngine{}, hooks)
	m = runCommand(m, "/model use local llama3.2")
	if endpoint != "local" || id != "llama3.2" {
		t.Fatalf("the hook was called with (%q, %q)", endpoint, id)
	}
	if m.model != "llama3.2" {
		t.Errorf("the active model = %q", m.model)
	}
}

func TestModelRemovePassesTheReplacementThrough(t *testing.T) {
	var endpoint, id, replacement string
	hooks := Hooks{
		DeleteModel: func(e, m, r string) error {
			endpoint, id, replacement = e, m, r
			return nil
		},
	}
	m := NewWithHooks(&scriptEngine{}, hooks)
	m = runCommand(m, "/model remove minimax deekseek-flash --use local/llama3.2")
	if endpoint != "minimax" || id != "deekseek-flash" {
		t.Fatalf("the hook was called with (%q, %q)", endpoint, id)
	}
	if replacement != "local/llama3.2" {
		t.Errorf("the replacement = %q", replacement)
	}
}

func TestModelRemoveWithoutAReplacementPassesAnEmptyOne(t *testing.T) {
	var replacement string
	hooks := Hooks{
		DeleteModel: func(_, _, r string) error { replacement = r; return nil },
	}
	m := NewWithHooks(&scriptEngine{}, hooks)
	m = runCommand(m, "/model remove minimax deekseek-flash")
	if replacement != "" {
		t.Errorf("the replacement = %q, want empty", replacement)
	}
}

func TestModelRemoveRefusesAMalformedReplacement(t *testing.T) {
	called := false
	hooks := Hooks{
		DeleteModel: func(_, _, _ string) error { called = true; return nil },
	}
	m := NewWithHooks(&scriptEngine{}, hooks)
	m = runCommand(m, "/model remove minimax deekseek-flash --use llama3.2")
	if called {
		t.Error("the removal was carried out with a malformed replacement")
	}
	if !strings.Contains(m.notice, "endpoint/model") {
		t.Errorf("the notice = %q", m.notice)
	}
}

func TestModelEditParsesTheSettings(t *testing.T) {
	var spec ModelSpec
	hooks := Hooks{
		EditModel: func(_, _ string, s ModelSpec) error { spec = s; return nil },
	}
	m := NewWithHooks(&scriptEngine{}, hooks)
	m = runCommand(m, "/model edit minimax MiniMax-M3.1-Flash effort=high context_window=4096 max_output_tokens=2048")
	if spec.Effort != "high" {
		t.Errorf("effort = %q", spec.Effort)
	}
	if spec.ContextWindow != 4096 {
		t.Errorf("context window = %d", spec.ContextWindow)
	}
	if spec.MaxOutputTokens != 2048 {
		t.Errorf("output limit = %d", spec.MaxOutputTokens)
	}
}

func TestModelEditRefusesAnUnknownSetting(t *testing.T) {
	called := false
	hooks := Hooks{
		EditModel: func(_, _ string, _ ModelSpec) error { called = true; return nil },
	}
	m := NewWithHooks(&scriptEngine{}, hooks)
	m = runCommand(m, "/model edit minimax m colour=blue")
	if called {
		t.Error("an unknown setting was carried out")
	}
	if !strings.Contains(m.notice, "not a model setting") {
		t.Errorf("the notice = %q", m.notice)
	}
}

func TestModelContextConfiguresTheLimit(t *testing.T) {
	var spec ModelSpec
	hooks := Hooks{
		EditModel: func(_, _ string, s ModelSpec) error { spec = s; return nil },
	}
	m := NewWithHooks(&scriptEngine{}, hooks)
	m = runCommand(m, "/model context minimax MiniMax-M3.1-Flash manual 4096")
	if spec.ContextMode != "manual" || spec.ContextWindow != 4096 {
		t.Fatalf("the spec = %+v", spec)
	}
	if !strings.Contains(m.notice, "manual") {
		t.Errorf("the notice = %q", m.notice)
	}
	// Automatic mode clears the explicit limit.
	m = runCommand(m, "/model context minimax MiniMax-M3.1-Flash auto")
	if spec.ContextMode != "auto" || spec.ContextWindow != 0 {
		t.Errorf("the spec = %+v", spec)
	}
}

func TestModelContextRefusesANonNumericLimit(t *testing.T) {
	called := false
	hooks := Hooks{
		EditModel: func(_, _ string, _ ModelSpec) error { called = true; return nil },
	}
	m := NewWithHooks(&scriptEngine{}, hooks)
	m = runCommand(m, "/model context minimax m manual lots")
	if called {
		t.Error("a non-numeric limit was carried out")
	}
}

func TestModelListShowsTheEndpoints(t *testing.T) {
	hooks := Hooks{
		Endpoints: func() []EndpointInfo {
			return []EndpointInfo{
				{
					ID: "minimax", Name: "MiniMax", Protocol: "openai-compatible",
					WireProtocol: "openai-compatible", BaseURL: "https://api.minimax.io/v1",
					Credential: "env:MINIMAX_API_KEY", Active: true,
					Models: []ModelInfo{{ID: "MiniMax-M3.1-Flash", Active: true}},
				},
				{ID: "local", Name: "Local", Protocol: "local", BaseURL: "http://127.0.0.1:11434/v1"},
			}
		},
	}
	m := NewWithHooks(&scriptEngine{}, hooks)
	m = runCommand(m, "/model list")
	if m.view != viewModels {
		t.Fatalf("the view = %v", m.view)
	}
	body := strings.Join(m.modelCfgLines, "\n")
	if !strings.Contains(body, "MiniMax") || !strings.Contains(body, "env:MINIMAX_API_KEY") {
		t.Errorf("the panel does not describe the endpoint:\n%s", body)
	}
	if !strings.Contains(body, "MiniMax-M3.1-Flash") {
		t.Errorf("the panel does not list the models:\n%s", body)
	}
	if strings.Contains(body, "sk-") {
		t.Errorf("the panel carries a secret:\n%s", body)
	}
}

func TestEndpointAddBuildsTheSpec(t *testing.T) {
	var spec EndpointSpec
	hooks := Hooks{
		SaveEndpoint: func(s EndpointSpec) error { spec = s; return nil },
	}
	m := NewWithHooks(&scriptEngine{}, hooks)
	m = runCommand(m, "/endpoint add deepseek DeepSeek openai-compatible https://api.deepseek.com/v1 DEEPSEEK_API_KEY")
	if spec.ID != "deepseek" || spec.Name != "DeepSeek" {
		t.Errorf("the spec = %+v", spec)
	}
	if spec.Protocol != "openai-compatible" || spec.BaseURL != "https://api.deepseek.com/v1" {
		t.Errorf("the spec = %+v", spec)
	}
	if spec.CredentialEnv != "DEEPSEEK_API_KEY" {
		t.Errorf("the credential reference = %q", spec.CredentialEnv)
	}
}

func TestEndpointEditParsesTheSettings(t *testing.T) {
	var spec EndpointSpec
	hooks := Hooks{
		SaveEndpoint: func(s EndpointSpec) error { spec = s; return nil },
	}
	m := NewWithHooks(&scriptEngine{}, hooks)
	m = runCommand(m, "/endpoint edit deepseek name=\"DeepSeek EU\" protocol=anthropic-compatible wire_protocol=anthropic-compatible base_url=https://api.deepseek.com/anthropic")
	if spec.Name != "DeepSeek EU" {
		t.Errorf("the name = %q", spec.Name)
	}
	if spec.Protocol != "anthropic-compatible" || spec.WireProtocol != "anthropic-compatible" {
		t.Errorf("the spec = %+v", spec)
	}
	if spec.BaseURL != "https://api.deepseek.com/anthropic" {
		t.Errorf("the base url = %q", spec.BaseURL)
	}
}

func TestEndpointEditRefusesAnUnknownSetting(t *testing.T) {
	called := false
	hooks := Hooks{
		SaveEndpoint: func(EndpointSpec) error { called = true; return nil },
	}
	m := NewWithHooks(&scriptEngine{}, hooks)
	m = runCommand(m, "/endpoint edit deepseek secret=hunter2")
	if called {
		t.Error("an unknown setting was carried out")
	}
	if !strings.Contains(m.notice, "not an endpoint setting") {
		t.Errorf("the notice = %q", m.notice)
	}
}

func TestUnknownSubcommandShowsTheUsage(t *testing.T) {
	m := NewWithHooks(&scriptEngine{}, Hooks{})
	m = runCommand(m, "/model frobnicate")
	if !strings.Contains(m.notice, "Usage:") {
		t.Errorf("the notice = %q", m.notice)
	}
}

func TestModelAndEndpointAreInTheCommandTable(t *testing.T) {
	names := map[string]bool{}
	for _, c := range commandTable {
		names[c.name] = true
	}
	if !names["/model"] {
		t.Error("/model is not in the command table")
	}
	if !names["/endpoint"] {
		t.Error("/endpoint is not in the command table")
	}
}

func TestModelPanelClosesWithEscape(t *testing.T) {
	hooks := Hooks{Endpoints: func() []EndpointInfo { return nil }}
	m := NewWithHooks(&scriptEngine{}, hooks)
	m = runCommand(m, "/model list")
	if m.view != viewModels {
		t.Fatalf("the view = %v", m.view)
	}
	m = pressKey(m, tea.KeyEscape, 0, "")
	if m.view != viewChat {
		t.Errorf("the view = %v, want the chat", m.view)
	}
}
