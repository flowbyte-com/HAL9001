package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flowbyte-com/HAL9001/internal/config"
	"github.com/flowbyte-com/HAL9001/internal/session"
)

// The reasoning level reaches the provider the way that provider's own
// documentation describes it.
//
// MiniMax takes the level as written on the OpenAI-compatible wire;
// DeepSeek takes the value its own table documents; a model whose wire
// documents no such parameter gets nothing.
func TestTheReasoningLevelReachesTheProvider(t *testing.T) {
	cases := []struct {
		name     string
		model    string
		level    string
		want     any
		present  bool
		protocol config.Protocol
	}{
		{
			name: "minimax takes the level as written", model: "MiniMax-M3.1-Flash",
			level: "high", want: "high", present: true, protocol: config.ProtocolOpenAI,
		},
		{
			name: "deepseek takes the mapped value", model: "deepseek-flash",
			level: "medium", want: "high", present: true, protocol: config.ProtocolOpenAI,
		},
		{
			name: "a model outside the tables gets nothing", model: "gpt-4o",
			level: "high", present: false, protocol: config.ProtocolOpenAI,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cap := &captureServer{}
			srv := httptest.NewServer(cap.handler())
			defer srv.Close()
			a := effortApp(t, srv.URL, c.protocol, config.ModelRecord{ID: c.model, Effort: c.level})
			if _, err := a.eng.RespondStream(context.Background(), "hi", nil); err != nil {
				t.Fatal(err)
			}
			got, ok := cap.body["reasoning_effort"]
			if ok != c.present {
				t.Fatalf("reasoning_effort present = %v, want %v (%v)", ok, c.present, cap.body)
			}
			if c.present && got != c.want {
				t.Errorf("reasoning_effort = %v, want %v", got, c.want)
			}
		})
	}
}

// The Claude family takes its reasoning control on the Messages wire,
// as the extended-thinking block the adapter documents.
//
// A model with no configured level carries no block: the operator's own
// choice is what turns thinking on.
func TestTheClaudeThinkingBlockReachesTheProvider(t *testing.T) {
	var captured map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		captured = body
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"text","text":"ok"}],"model":"m","stop_reason":"end_turn"}`))
	}))
	defer srv.Close()

	// No level: no thinking block.
	a := effortApp(t, srv.URL, config.ProtocolAnthropic, config.ModelRecord{ID: "claude-sonnet-4-5"})
	if _, err := a.eng.RespondStream(context.Background(), "hi", nil); err != nil {
		t.Fatal(err)
	}
	if _, ok := captured["thinking"]; ok {
		t.Fatalf("a model with no level carried a thinking block: %v", captured)
	}

	// A level: the block, and the budget that follows it.
	a = effortApp(t, srv.URL, config.ProtocolAnthropic, config.ModelRecord{ID: "claude-sonnet-4-5", Effort: "high"})
	if _, err := a.eng.RespondStream(context.Background(), "hi", nil); err != nil {
		t.Fatal(err)
	}
	thinking, ok := captured["thinking"].(map[string]any)
	if !ok {
		t.Fatalf("no thinking block: %v", captured)
	}
	if thinking["type"] != "enabled" {
		t.Errorf("thinking = %v", thinking)
	}
	if got, _ := thinking["budget_tokens"].(float64); got != 8192 {
		t.Errorf("budget = %v, want 8192", thinking["budget_tokens"])
	}
}

// The configured default and the operator's own level are two different
// things: a model with nothing stored starts at its documented default,
// and the stored level replaces it.
func TestTheConfiguredDefaultAndTheStoredLevelAreDifferent(t *testing.T) {
	cases := []struct {
		model string
		level string
		want  string
	}{
		{"MiniMax-M3.1-Flash", "", "medium"},
		{"MiniMax-M3.1-Flash", "low", "low"},
		{"deepseek-flash", "", "high"},
		{"deepseek-flash", "max", "max"},
		// A Claude model starts with no extended thinking.
		{"claude-sonnet-4-5", "", ""},
		{"claude-sonnet-4-5", "high", "high"},
	}
	for _, c := range cases {
		got := effortForRecord(config.ModelRecord{ID: c.model, Effort: c.level})
		if got != c.want {
			t.Errorf("effortForRecord(%q, %q) = %q, want %q", c.model, c.level, got, c.want)
		}
	}
}

// The runtime model override is a temporary choice for this run: it
// keeps the stored settings of a model HAL knows, and drops them for one
// it does not, so an override never inherits a level it was not given.
func TestTheRuntimeModelOverrideKeepsTheStoredSettings(t *testing.T) {
	stored := config.FileConfig{Version: config.ConfigVersion}
	stored.Endpoints = []config.EndpointRecord{{
		ID: "minimax", Name: "MiniMax", Protocol: config.ProtocolOpenAI,
		BaseURL: "http://example.invalid", CredentialEnv: "MINIMAX_API_KEY",
		Models: []config.ModelRecord{
			{ID: "MiniMax-M3.1-Flash", Effort: "low", ContextMode: config.ContextAuto},
			{ID: "MiniMax-M2", Effort: "max", ContextMode: config.ContextAuto},
		},
	}}
	stored.SetActive("minimax", "MiniMax-M3.1-Flash")

	a := &app{cfg: config.Config{StoreDir: t.TempDir()}, stored: stored}

	// No override: the active model's own level.
	if got := a.effortFor("MiniMax-M3.1-Flash"); got != "low" {
		t.Errorf("the active model's level = %q, want low", got)
	}
	// An override that names a stored model keeps that model's level.
	t.Setenv(config.EnvModel, "MiniMax-M2")
	if got := a.effortFor("MiniMax-M2"); got != "max" {
		t.Errorf("the overridden model's level = %q, want max", got)
	}
	// An override that names a model HAL does not know carries no level:
	// nothing is inherited.
	t.Setenv(config.EnvModel, "MiniMax-M2-ultra")
	if got := a.effortFor("MiniMax-M2-ultra"); got != "" {
		t.Errorf("an unknown model carried the level %q", got)
	}
}

// The visibility of the provider's reasoning is the operator's own
// preference, and it is written to the configuration like the theme.
func TestTheVisibilityPreferenceIsWrittenToTheConfiguration(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	t.Setenv("HAL9001_CONFIG", cfgPath)

	a := &app{cfg: config.Config{StoreDir: dir}, stored: config.FileConfig{Version: config.ConfigVersion}}
	if a.currentShowThinking() {
		t.Error("the preference starts on")
	}
	if err := a.saveShowThinking(true); err != nil {
		t.Fatal(err)
	}
	if !a.currentShowThinking() {
		t.Error("the preference was not remembered")
	}
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	var probe map[string]any
	if err := json.Unmarshal(raw, &probe); err != nil {
		t.Fatal(err)
	}
	if probe["show_thinking"] != true {
		t.Errorf("the configuration does not carry the preference: %s", raw)
	}

	// The file is read back with the preference in it.
	loaded, err := config.LoadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if !loaded.ShowThinking {
		t.Error("the preference did not survive a reload")
	}
}

// The configuration keeps the preference under its own name, and the
// rest of the file is untouched by it.
func TestTheVisibilityPreferenceHasItsOwnName(t *testing.T) {
	raw, err := json.Marshal(config.FileConfig{Version: config.ConfigVersion, ShowThinking: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"show_thinking":true`) {
		t.Errorf("the preference is missing: %s", raw)
	}
	// The default is hidden: the field is left out entirely.
	raw, err = json.Marshal(config.FileConfig{Version: config.ConfigVersion})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "show_thinking") {
		t.Errorf("the default preference was written: %s", raw)
	}
}

// effortApp builds an application whose active model is one record, on
// an endpoint that records the request.
// The footer names the level the provider will get.
//
// A stored level that the provider never sees — a legacy or hand-edited
// "medium" on a DeepSeek record — rides at "high" on the wire, and the
// footer says "high". A footer that names one level while the wire
// carries another is a disagreement the operator cannot see through.
func TestTheFooterNamesTheLevelTheProviderGets(t *testing.T) {
	cases := []struct {
		name   string
		model  string
		effort string
		want   string
	}{
		{"a mapped level", "deepseek-flash", "medium", "high"},
		{"a level the provider takes as written", "deepseek-flash", "low", "low"},
		{"a family that takes the level as written", "MiniMax-M3.1-Flash", "xhigh", "xhigh"},
		{"the model's own default", "deepseek-flash", "", "high"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a := effortApp(t, "http://127.0.0.1:1", config.ProtocolOpenAI,
				config.ModelRecord{ID: c.model, Effort: c.effort})
			level, ok := a.currentEffort()
			if !ok {
				t.Fatalf("the model %q reported no effort control", c.model)
			}
			if level != c.want {
				t.Errorf("the footer names %q, want %q", level, c.want)
			}
		})
	}
}

func effortApp(t *testing.T, baseURL string, protocol config.Protocol, record config.ModelRecord) *app {
	t.Helper()
	store, err := session.Open(t.TempDir(), "test")
	if err != nil {
		t.Fatal(err)
	}
	stored := config.FileConfig{Version: config.ConfigVersion}
	stored.Endpoints = []config.EndpointRecord{{
		ID: "ep", Name: "Endpoint", Protocol: protocol,
		BaseURL: baseURL, CredentialEnv: "TEST_API_KEY",
		Models: []config.ModelRecord{record},
	}}
	stored.SetActive("ep", record.ID)
	a := &app{
		cfg:     config.Config{StoreDir: t.TempDir()},
		stored:  stored,
		store:   store,
		session: "test",
	}
	a.rebuildEngine()
	return a
}
