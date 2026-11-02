package tui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/flowbyte-com/HAL9001/internal/engine"
)

type scriptEngine struct {
	replies map[string]string
	calls   int
}

func (s *scriptEngine) RespondStream(_ context.Context, input string, emit func(string)) (string, error) {
	s.calls++
	if r, ok := s.replies[input]; ok {
		return r, nil
	}
	return "echo: " + input, nil
}

func testModel() Model {
	m := New(&scriptEngine{replies: map[string]string{"hi": "hello there"}})
	m.SetColor(false)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	return updated.(Model)
}

func pressKey(m Model, code rune, mod tea.KeyMod, text string) Model {
	updated, _ := m.Update(tea.KeyPressMsg{Code: code, Mod: mod, Text: text})
	return updated.(Model)
}

func typeText(m Model, s string) Model {
	for _, r := range s {
		m = pressKey(m, r, 0, string(r))
	}
	return m
}

// Enter inserts a newline and never submits: multiline input does not send
// each line as a separate message.
func TestEnterInsertsNewlineWithoutSubmit(t *testing.T) {
	m := testModel()
	m = typeText(m, "line one")
	m = pressKey(m, tea.KeyEnter, 0, "")
	m = typeText(m, "line two")
	if got := m.Composer().Value(); got != "line one\nline two" {
		t.Fatalf("composer = %q, want two lines", got)
	}
	if len(m.Lines()) != 0 {
		t.Fatalf("nothing must be submitted on Enter, got %v", m.Lines())
	}
}

// Ctrl+S sends exactly once; the reply renders as a HAL line.
func TestCtrlSSends(t *testing.T) {
	m := testModel()
	m = typeText(m, "hi")
	m = pressKey(m, 's', tea.ModCtrl, "")
	if !m.Sending() {
		t.Fatal("engine call should be in flight after Ctrl+S")
	}
	if len(m.Lines()) != 1 || m.Lines()[0] != "You > hi" {
		t.Fatalf("user line wrong: %v", m.Lines())
	}
	m, _ = func() (Model, tea.Cmd) {
		updated, cmd := m.InjectResponse("hello there")
		return updated.(Model), cmd
	}()
	if m.Sending() {
		t.Error("sending flag must clear on response")
	}
	if len(m.Lines()) != 2 || m.Lines()[1] != "HAL > hello there" {
		t.Fatalf("conversation wrong: %v", m.Lines())
	}
}

// Empty composer never sends.
func TestEmptySendIgnored(t *testing.T) {
	m := testModel()
	m = pressKey(m, 's', tea.ModCtrl, "")
	if m.Sending() || len(m.Lines()) != 0 {
		t.Error("empty composer must not send")
	}
}

// Ctrl+C quits cleanly.
func TestQuit(t *testing.T) {
	m := testModel()
	updated, cmd := m.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	if cmd == nil {
		t.Fatal("quit must return tea.Quit")
	}
	if msg := cmd(); msg == nil {
		t.Fatal("quit command must produce a message")
	}
	_ = updated
}

// Resize reflows viewport and composer.
func TestResize(t *testing.T) {
	m := testModel()
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = updated.(Model)
	if m.Viewport().Width() != 100 {
		t.Errorf("viewport width = %d, want 100", m.Viewport().Width())
	}
	if m.Composer().Width() <= 0 {
		t.Error("composer must have a width after resize")
	}
	view := string(m.View().Content)
	if !strings.Contains(view, "HAL9001") {
		t.Error("view must carry the brand heading")
	}
	if !strings.Contains(view, "[444 read-only]") {
		t.Error("the 444 indicator must always be visible")
	}
}

// Scroll: after enough lines, PgUp leaves the bottom.
func TestScroll(t *testing.T) {
	m := testModel()
	for i := 0; i < 40; i++ {
		m.appendLine("line")
	}
	if !m.Viewport().AtBottom() {
		t.Fatal("fresh output should sit at the bottom")
	}
	m = pressKey(m, tea.KeyPgUp, 0, "")
	if m.Viewport().AtBottom() {
		t.Error("PgUp must scroll away from the bottom")
	}
	m = pressKey(m, tea.KeyPgDown, 0, "")
	if !m.Viewport().AtBottom() {
		t.Error("PgDn must return to the bottom")
	}
}

// Monochrome: with color disabled the view contains no ANSI escapes and
// no lobster; the eye and labels stay legible.
func TestMonochromeView(t *testing.T) {
	m := testModel()
	m = typeText(m, "hi")
	m = pressKey(m, 's', tea.ModCtrl, "")
	m, _ = func() (Model, tea.Cmd) {
		updated, cmd := m.InjectResponse("hello")
		return updated.(Model), cmd
	}()
	view := string(m.View().Content)
	if strings.Contains(view, "\x1b[") {
		t.Errorf("monochrome view must not contain escapes:\n%q", view)
	}
	if strings.Contains(view, "🦞") {
		t.Error("view must never contain the lobster emoji")
	}
	for _, want := range []string{"● HAL9001", "You > hi", "hello", "[444 read-only]"} {
		if !strings.Contains(view, want) {
			t.Errorf("view missing %q:\n%s", want, view)
		}
	}
}

// Color mode keeps the red eye; the content stays identical otherwise.
func TestColorView(t *testing.T) {
	m := New(&scriptEngine{})
	m.SetColor(true)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(Model)
	view := string(m.View().Content)
	if !strings.Contains(view, "\x1b[") {
		t.Error("color view should style the heading")
	}
	if !strings.Contains(view, "HAL9001") {
		t.Error("color view must name the brand")
	}
}

func TestColorEnabledHonoursNoColor(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	t.Setenv("TERM", "xterm-256color")
	if ColorEnabled() {
		t.Error("NO_COLOR must disable color")
	}
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "dumb")
	if ColorEnabled() {
		t.Error("dumb terminals must disable color")
	}
}

func TestNoProviderNoticeWithoutFabrication(t *testing.T) {
	m := testModel()
	m = typeText(m, "remember this")
	m = pressKey(m, 's', tea.ModCtrl, "")
	updated, _ := m.Update(errorMsg{err: engine.ErrNoProvider})
	m = updated.(Model)
	if m.Sending() {
		t.Error("sending must clear")
	}
	if len(m.Lines()) != 1 {
		t.Fatalf("no assistant line may be fabricated, got %v", m.Lines())
	}
	if !strings.Contains(m.Notice(), "No provider configured") {
		t.Errorf("notice must explain, got %q", m.Notice())
	}
}

func TestCtrlMOpensPickerAndEscReturns(t *testing.T) {
	hooks := Hooks{
		ListModels: func() []ModelInfo {
			return []ModelInfo{{ID: "a", Active: true}, {ID: "b"}}
		},
	}
	m := NewWithHooks(&scriptEngine{}, hooks)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(Model)
	m = pressKey(m, 'm', tea.ModCtrl, "")
	view := string(m.View().Content)
	if !strings.Contains(view, "Models") || !strings.Contains(view, "(active)") {
		t.Fatalf("picker must list models with the active mark:\n%s", view)
	}
	m = pressKey(m, tea.KeyEscape, 0, "")
	view = string(m.View().Content)
	if strings.Contains(view, "Models (Enter switches") {
		t.Fatal("Esc must return to the conversation")
	}
}

func TestPickerSwitchesModel(t *testing.T) {
	switched := ""
	hooks := Hooks{
		ListModels: func() []ModelInfo {
			var active string
			if switched != "" {
				active = switched
			} else {
				active = "a"
			}
			return []ModelInfo{{ID: "a", Active: active == "a"}, {ID: "b", Active: active == "b"}}
		},
		SwitchModel: func(id string) (Engine, error) { switched = id; return nil, nil },
	}
	m := NewWithHooks(&scriptEngine{}, hooks)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(Model)
	m = pressKey(m, 'm', tea.ModCtrl, "")
	m = pressKey(m, tea.KeyDown, 0, "")
	m = pressKey(m, tea.KeyEnter, 0, "")
	if switched != "b" {
		t.Fatalf("switched = %q, want b", switched)
	}
	if !strings.Contains(m.Notice(), "Model: b") {
		t.Errorf("notice should confirm the switch, got %q", m.Notice())
	}
}

func TestCtrlNStartsNewConversation(t *testing.T) {
	news := 0
	hooks := Hooks{NewSession: func() (Engine, error) { news++; return nil, nil }}
	m := NewWithHooks(&scriptEngine{}, hooks)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(Model)
	m = typeText(m, "old message")
	m = pressKey(m, 'n', tea.ModCtrl, "")
	if news != 1 {
		t.Fatal("Ctrl+N must start a new session")
	}
	if len(m.Lines()) != 0 {
		t.Errorf("new conversation must clear the view, got %v", m.Lines())
	}
}

func TestOnboardingValidation(t *testing.T) {
	if err := ValidateOnboard(OnboardData{}); err == nil {
		t.Error("empty onboarding must fail")
	}
	bad := OnboardData{Endpoint: "ftp://x", Model: "m", Key: "k"}
	if err := ValidateOnboard(bad); err == nil {
		t.Error("non-HTTP endpoint must fail")
	}
	good := OnboardData{Endpoint: "https://api.example.com/v1", Model: "m", Key: "k"}
	if err := ValidateOnboard(good); err != nil {
		t.Errorf("valid onboarding rejected: %v", err)
	}
	local := OnboardData{Endpoint: "http://localhost:11434/v1", Model: "m", Key: "k"}
	if err := ValidateOnboard(local); err != nil {
		t.Errorf("localhost HTTP must be allowed: %v", err)
	}
}

func TestOnboardingSubmitSaves(t *testing.T) {
	var saved OnboardData
	hooks := Hooks{SaveOnboard: func(d OnboardData) (Engine, error) { saved = d; return nil, nil }}
	m := NewWithHooks(&scriptEngine{}, hooks)
	m.SetColor(false)
	m.enterOnboard(OnboardData{})
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(Model)
	for _, key := range []string{"https://api.example.com/v1", "m1", "k1"} {
		for _, r := range key {
			m = pressKey(m, r, 0, string(r))
		}
		if key != "k1" {
			m = pressKey(m, tea.KeyEnter, 0, "")
		}
	}
	m = pressKey(m, 's', tea.ModCtrl, "")
	if saved.Endpoint != "https://api.example.com/v1" || saved.Model != "m1" || saved.Key != "k1" {
		t.Fatalf("saved = %+v", saved)
	}
}

func TestEscapeCancelsStream(t *testing.T) {
	m := testModel()
	m = typeText(m, "hi")
	m = pressKey(m, 's', tea.ModCtrl, "")
	if !m.Sending() {
		t.Fatal("expected in-flight send")
	}
	m = pressKey(m, tea.KeyEscape, 0, "")
	if m.Sending() {
		t.Error("Esc must cancel the in-flight send")
	}
	if !strings.Contains(m.Notice(), "cancelled") {
		t.Errorf("notice = %q", m.Notice())
	}
}

func TestRenderMarkdownFallback(t *testing.T) {
	if got := renderMarkdown("**bold** and text", 0); !strings.Contains(got, "bold") {
		t.Errorf("fallback must keep content, got %q", got)
	}
}
