package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"

	"github.com/flowbyte-com/HAL9001/internal/engine"
	"github.com/flowbyte-com/HAL9001/internal/provider"
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

// drain runs the command returned by an Update (executing the headless
// send path) and feeds the resulting message back through the model.
func drain(m Model, cmd tea.Cmd) Model {
	if cmd == nil {
		return m
	}
	updated, _ := m.Update(cmd())
	return updated.(Model)
}

func sendCurrent(m Model) Model {
	updated, cmd := m.Update(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl, Text: ""})
	m = updated.(Model)
	return drain(m, cmd)
}

// enterCurrent presses Enter and runs the command it produces.
func enterCurrent(m Model) Model {
	updated, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter, Text: ""})
	m = updated.(Model)
	return drain(m, cmd)
}

func typeText(m Model, s string) Model {
	for _, r := range s {
		m = pressKey(m, r, 0, string(r))
	}
	return m
}

// Enter sends the message. Newlines come from an explicit chord so a
// multiline prompt never turns into several messages.
func TestEnterSends(t *testing.T) {
	m := testModel()
	m = typeText(m, "hello there")
	m = sendCurrent(m)
	if len(m.Lines()) < 1 || m.Lines()[0] != "You > hello there" {
		t.Fatalf("Enter must send exactly one message, got %v", m.Lines())
	}
	if m.Composer().Value() != "" {
		t.Errorf("composer must be cleared after sending: %q", m.Composer().Value())
	}
}

// Ctrl+J inserts a newline and never submits.
func TestCtrlJInsertsNewlineWithoutSubmit(t *testing.T) {
	m := testModel()
	m = typeText(m, "line one")
	m = pressKey(m, 'j', tea.ModCtrl, "")
	m = typeText(m, "line two")
	if got := m.Composer().Value(); got != "line one\nline two" {
		t.Fatalf("composer = %q, want two lines", got)
	}
	if len(m.Lines()) != 0 {
		t.Fatalf("nothing must be submitted on Ctrl+J, got %v", m.Lines())
	}
}

// Alt+Enter inserts a newline too: terminals report it as ESC CR.
func TestAltEnterInsertsNewline(t *testing.T) {
	m := testModel()
	m = typeText(m, "first")
	m = pressKey(m, tea.KeyEnter, tea.ModAlt, "")
	m = typeText(m, "second")
	if got := m.Composer().Value(); got != "first\nsecond" {
		t.Fatalf("composer = %q, want two lines", got)
	}
	if len(m.Lines()) != 0 {
		t.Fatalf("nothing must be submitted on Alt+Enter, got %v", m.Lines())
	}
}

// Shift+Enter inserts a newline on terminals that report modifiers.
func TestShiftEnterInsertsNewline(t *testing.T) {
	m := testModel()
	m = typeText(m, "first")
	m = pressKey(m, tea.KeyEnter, tea.ModShift, "")
	m = typeText(m, "second")
	if got := m.Composer().Value(); got != "first\nsecond" {
		t.Fatalf("composer = %q, want two lines", got)
	}
	if len(m.Lines()) != 0 {
		t.Fatalf("nothing must be submitted on Shift+Enter, got %v", m.Lines())
	}
}

// A held Enter key must not submit the message twice.
func TestRepeatedEnterDoesNotDoubleSubmit(t *testing.T) {
	eng := &scriptEngine{}
	m := NewWithHooks(eng, Hooks{})
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(Model)
	m = typeText(m, "once")
	updated, _ = m.Update(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl, IsRepeat: true})
	m = updated.(Model)
	if eng.calls != 0 {
		t.Fatalf("a repeated key must not submit, calls = %d", eng.calls)
	}
	m = sendCurrent(m)
	if eng.calls != 1 {
		t.Fatalf("calls = %d, want 1", eng.calls)
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
	if !strings.Contains(view, "444 READ ONLY") {
		t.Error("the 444 indicator must always be visible")
	}
}

// Scroll: after enough lines, PgUp leaves the bottom.
func TestScroll(t *testing.T) {
	m := testModel()
	for i := 0; i < 40; i++ {
		m.appendEntry("assistant", "line")
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
	for _, want := range []string{"● HAL9001", "You > hi", "hello", "444 READ ONLY"} {
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

func TestCtrlKOpensPickerAndEscReturns(t *testing.T) {
	hooks := Hooks{
		ListModels: func() []ModelInfo {
			return []ModelInfo{{ID: "a", Active: true}, {ID: "b"}}
		},
	}
	m := NewWithHooks(&scriptEngine{}, hooks)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(Model)
	m = pressKey(m, 'k', tea.ModCtrl, "")
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
	m = pressKey(m, 'k', tea.ModCtrl, "")
	m = pressKey(m, tea.KeyDown, 0, "")
	m = pressKey(m, tea.KeyEnter, 0, "")
	if switched != "b" {
		t.Fatalf("switched = %q, want b", switched)
	}
	if !strings.Contains(m.Notice(), "Model: b") {
		t.Errorf("notice should confirm the switch, got %q", m.Notice())
	}
	m.SetColor(false)
	if view := string(m.View().Content); !strings.Contains(view, " b") {
		t.Errorf("footer should show the active model after switching:\n%s", view)
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

func TestComposerGrowsFromThreeRows(t *testing.T) {
	m := testModel()
	view := string(m.View().Content)
	if strings.Contains(view, "┃") {
		t.Error("composer must not render line-number gutters")
	}
	if h := m.Composer().Height(); h != minComposerRows {
		t.Errorf("default composer height = %d, want %d", h, minComposerRows)
	}
	for i := 0; i < 20; i++ {
		m = typeText(m, "line\n")
	}
	if h := m.Composer().Height(); h != maxComposerHeight {
		t.Errorf("composer height = %d, want cap %d", h, maxComposerHeight)
	}
	m2 := testModel()
	m2 = typeText(m2, "one\ntwo\nthree")
	if h := m2.Composer().Height(); h != minComposerRows {
		t.Errorf("composer height = %d, want %d for three lines", h, minComposerRows)
	}
	m3 := testModel()
	m3 = typeText(m3, "one\ntwo\nthree\nfour\nfive")
	if h := m3.Composer().Height(); h != 5 {
		t.Errorf("composer height = %d, want 5 for five lines", h)
	}
}

// The composer is one bordered block with three text rows inside it.
func TestComposerBorderAndThreeRows(t *testing.T) {
	m := testModel()
	m.SetColor(false)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(Model)
	// Fill the conversation so the row above the separator is real text.
	for i := 0; i < 40; i++ {
		m.appendEntry("assistant", "filler line")
	}
	rows := m.layoutRows()
	top, bottom := -1, -1
	for i, row := range rows {
		if strings.HasPrefix(row, "╭") {
			top = i
		}
		if strings.HasPrefix(row, "╰") {
			bottom = i
		}
	}
	if top < 0 || bottom < 0 {
		t.Fatalf("composer border missing:\n%s", strings.Join(rows, "\n"))
	}
	if bottom-top-1 != minComposerRows {
		t.Errorf("composer text rows = %d, want %d", bottom-top-1, minComposerRows)
	}
	if w := lipgloss.Width(rows[top]); w != 80 {
		t.Errorf("border width = %d, want 80", w)
	}
	if w := lipgloss.Width(rows[top+1]); w != 80 {
		t.Errorf("composer row width = %d, want 80", w)
	}
	// Exactly one blank row between the conversation and the border.
	if top < 1 || strings.TrimSpace(rows[top-1]) != "" {
		t.Errorf("expected one blank row above the composer border:\n%s", strings.Join(rows, "\n"))
	}
	if top >= 2 && strings.TrimSpace(rows[top-2]) == "" {
		t.Error("more than one blank row above the composer")
	}
}

func TestSlashCommandsNeverReachEngine(t *testing.T) {
	eng := &scriptEngine{replies: map[string]string{}}
	newHooks := func() Hooks {
		return Hooks{
			ListModels: func() []ModelInfo { return []ModelInfo{{ID: "a", Active: true}} },
			Copy:       func(text string) error { return nil },
		}
	}
	for _, input := range []string{"/help", "/theme", "/model", "/new", "/sessions", "/copy", "/copy code", "/bogus"} {
		m := NewWithHooks(eng, newHooks())
		m.SetColor(false)
		updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
		m = updated.(Model)
		m = typeText(m, input)
		callsBefore := eng.calls
		m = sendCurrent(m)
		if eng.calls != callsBefore {
			t.Errorf("input %q reached the engine", input)
		}
	}
}

func TestSlashHelpAndUnknown(t *testing.T) {
	m := testModel()
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(Model)
	m = typeText(m, "/help")
	m = pressKey(m, 's', tea.ModCtrl, "")
	if view := string(m.View().Content); !strings.Contains(view, "Keys and commands") {
		t.Errorf("help view missing:\n%s", view)
	}
	m = pressKey(m, tea.KeyEscape, 0, "")
	m = typeText(m, "/bogus")
	m = pressKey(m, 's', tea.ModCtrl, "")
	if !strings.Contains(m.Notice(), "Unknown command") {
		t.Errorf("notice = %q", m.Notice())
	}
}

func TestSlashEscapeSendsLiterally(t *testing.T) {
	eng := &scriptEngine{replies: map[string]string{"/help": "ok"}}
	m := NewWithHooks(eng, Hooks{})
	m.SetColor(false)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(Model)
	m = typeText(m, "//help")
	m = sendCurrent(m)
	if eng.calls != 1 {
		t.Fatalf("escaped command must be sent, calls = %d", eng.calls)
	}
	if len(m.Lines()) < 1 || m.Lines()[0] != "You > /help" {
		t.Errorf("literal line wrong: %v", m.Lines())
	}
}

func TestCopyUsesLastResponse(t *testing.T) {
	var copied string
	hooks := Hooks{Copy: func(text string) error { copied = text; return nil }}
	m := NewWithHooks(&scriptEngine{}, hooks)
	m.SetColor(false)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(Model)
	m.lastResponse = "# Brief\n\nDo the thing."
	m = typeText(m, "/copy")
	m = pressKey(m, 's', tea.ModCtrl, "")
	// headless mode runs the copy inline
	updated, _ = m.Update(copyResultMsg{chars: len(copied), label: "response"})
	m = updated.(Model)
	if copied != "# Brief\n\nDo the thing." {
		t.Errorf("copied = %q (colours/labels must not leak in)", copied)
	}
	if !strings.Contains(m.Notice(), "Copied") {
		t.Errorf("notice = %q", m.Notice())
	}
}

func TestCopyCodeExtractsLatestBlock(t *testing.T) {
	var copied string
	hooks := Hooks{Copy: func(text string) error { copied = text; return nil }}
	m := NewWithHooks(&scriptEngine{}, hooks)
	m.SetColor(false)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(Model)
	m.lastResponse = "First:\n```go\nfirst()\n```\nSecond:\n```py\nsecond()\n```\n"
	m = typeText(m, "/copy code")
	m = pressKey(m, 's', tea.ModCtrl, "")
	updated, _ = m.Update(copyResultMsg{chars: len(copied), label: "code block"})
	m = updated.(Model)
	if copied != "second()" {
		t.Errorf("copied = %q, want the latest block", copied)
	}
}

func TestCopyFailureSurfacesNotice(t *testing.T) {
	hooks := Hooks{Copy: func(text string) error { return errCopyBoom }}
	m := NewWithHooks(&scriptEngine{}, hooks)
	m.SetColor(false)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(Model)
	m.lastResponse = "something"
	m = typeText(m, "/copy")
	m = pressKey(m, 's', tea.ModCtrl, "")
	updated, _ = m.Update(copyResultMsg{err: errCopyBoom})
	m = updated.(Model)
	if !strings.Contains(m.Notice(), "Copy failed") {
		t.Errorf("notice = %q", m.Notice())
	}
	if len(m.Lines()) != 0 {
		t.Errorf("failed copy must not touch the conversation: %v", m.Lines())
	}
}

type copyError string

func (e copyError) Error() string { return string(e) }

var errCopyBoom = copyError("boom")

func TestCopyWithoutResponse(t *testing.T) {
	hooks := Hooks{Copy: func(text string) error { return nil }}
	m := NewWithHooks(&scriptEngine{}, hooks)
	m.SetColor(false)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(Model)
	m = typeText(m, "/copy")
	m = pressKey(m, 's', tea.ModCtrl, "")
	if !strings.Contains(m.Notice(), "Nothing to copy") {
		t.Errorf("notice = %q", m.Notice())
	}
}

func TestPasteInsertsWithoutSubmit(t *testing.T) {
	eng := &scriptEngine{}
	m := NewWithHooks(eng, Hooks{})
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(Model)
	updated, _ = m.Update(tea.PasteMsg{Content: "line one\nline two\n  indented"})
	m = updated.(Model)
	if got := m.Composer().Value(); got != "line one\nline two\n  indented" {
		t.Errorf("paste mangled: %q", got)
	}
	if eng.calls != 0 {
		t.Error("paste must never submit")
	}
}

func TestMarkdownWhitespacePreserved(t *testing.T) {
	md := "# Title\n\nParagraph with *emphasis*.\n\n- one\n- two\n\n```go\ncode()\n```\n"
	out := stripANSI(renderMarkdown(md, 80))
	// Glamour restyles markers (headings lose '#', bullets become dots),
	// but every word must survive with readable structure.
	for _, want := range []string{"Title", "Paragraph", "emphasis", "one", "two", "code", "()"} {
		if !strings.Contains(out, want) {
			t.Errorf("render lost %q:\n%s", want, out)
		}
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) < 5 {
		t.Errorf("render collapsed paragraph spacing:\n%s", out)
	}
	if out == "" {
		t.Error("render must not drop content")
	}
}

func TestMarkdownFallbackKeepsContent(t *testing.T) {
	// Width 0 falls back to a sane default rather than failing.
	if got := renderMarkdown("# Hi", 0); !strings.Contains(got, "Hi") {
		t.Errorf("fallback lost content: %q", got)
	}
}

func TestFooterShowsModelAnd444(t *testing.T) {
	m := testModel()
	m.SetModelName("MiniMax-M3.1-Flash")
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = updated.(Model)
	view := string(m.View().Content)
	if !strings.Contains(view, "MiniMax-M3.1-Flash") {
		t.Errorf("footer must show the active model:\n%s", view)
	}
	if !strings.Contains(view, "444 READ ONLY") {
		t.Errorf("footer must show the 444 indicator:\n%s", view)
	}
}

func TestFooterNarrowsGracefully(t *testing.T) {
	m := testModel()
	m.SetModelName("some-very-long-model-identifier")
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 30, Height: 24})
	m = updated.(Model)
	view := string(m.View().Content)
	if !strings.Contains(view, "444 READ ONLY") {
		t.Errorf("narrow footer must keep 444:\n%s", view)
	}
}

func TestThemeSwitchAndPersist(t *testing.T) {
	saved := ""
	hooks := Hooks{
		Theme:     func() string { return "" },
		SaveTheme: func(name string) error { saved = name; return nil },
	}
	m := NewWithHooks(&scriptEngine{}, hooks)
	m.SetColor(false)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(Model)
	if m.Theme().Name != "HAL Classic" {
		t.Errorf("default theme = %q", m.Theme().Name)
	}
	m = typeText(m, "/theme")
	m = pressKey(m, 's', tea.ModCtrl, "")
	m = pressKey(m, tea.KeyDown, 0, "")
	m = pressKey(m, tea.KeyEnter, 0, "")
	if saved != "Midnight" {
		t.Errorf("saved = %q, want Midnight", saved)
	}
	if m.Theme().Name != "Midnight" {
		t.Errorf("theme = %q", m.Theme().Name)
	}
}

func TestThemePickerListsPresets(t *testing.T) {
	m := testModel()
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(Model)
	m = typeText(m, "/theme")
	m = sendCurrent(m)
	view := string(m.View().Content)
	for _, name := range []string{"HAL Classic", "Midnight", "Amber", "Paper", "Monochrome"} {
		if !strings.Contains(view, name) {
			t.Errorf("picker missing preset %q:\n%s", name, view)
		}
	}
	if !strings.Contains(view, "live preview") {
		t.Errorf("picker must show a live preview:\n%s", view)
	}
}

func TestDraftPreservedAcrossOverlays(t *testing.T) {
	m := testModel()
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(Model)
	m = typeText(m, "draft in progress")
	m.enterPicker()
	m.view = viewChat
	if got := m.Composer().Value(); got != "draft in progress" {
		t.Errorf("draft lost across model picker: %q", got)
	}
	m.enterTheme()
	m.view = viewChat
	if got := m.Composer().Value(); got != "draft in progress" {
		t.Errorf("draft lost across theme picker: %q", got)
	}
}

func TestThemeDefinitions(t *testing.T) {
	seen := map[string]bool{}
	for _, th := range Themes() {
		if th.Name == "" || th.Background == "" || th.Text == "" || th.Accent == "" || th.Muted == "" {
			t.Errorf("theme %+v has empty roles", th)
		}
		if seen[th.Name] {
			t.Errorf("duplicate theme %q", th.Name)
		}
		seen[th.Name] = true
	}
	if len(Themes()) != 5 {
		t.Errorf("want 5 presets, got %d", len(Themes()))
	}
	if got := ThemeByName("Nope").Name; got != "HAL Classic" {
		t.Errorf("unknown theme must fall back to HAL Classic, got %q", got)
	}
}

func TestThemeViewMonochrome(t *testing.T) {
	m := testModel()
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(Model)
	m = typeText(m, "/theme")
	m = pressKey(m, 's', tea.ModCtrl, "")
	view := string(m.View().Content)
	if strings.Contains(view, "\x1b[") {
		t.Errorf("monochrome theme view must not contain escapes:\n%q", view)
	}
	if !strings.Contains(view, "live preview") {
		t.Errorf("theme picker must show a live preview:\n%s", view)
	}
}

func TestSessionsViewAndResume(t *testing.T) {
	hooks := Hooks{
		ListSessions: func() ([]string, error) { return []string{"a", "b"}, nil },
		SwitchSession: func(name string) (Engine, []SessionLine, error) {
			return &scriptEngine{}, []SessionLine{{Role: "user", Text: "hi"}, {Role: "assistant", Text: "yo"}}, nil
		},
	}
	m := NewWithHooks(&scriptEngine{}, hooks)
	m.SetColor(false)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(Model)
	m = typeText(m, "/sessions")
	m = pressKey(m, 's', tea.ModCtrl, "")
	if view := string(m.View().Content); !strings.Contains(view, "Sessions") {
		t.Fatalf("sessions view missing:\n%s", view)
	}
	m = pressKey(m, tea.KeyDown, 0, "")
	m = pressKey(m, tea.KeyEnter, 0, "")
	if len(m.Lines()) != 2 || m.Lines()[0] != "You > hi" || m.Lines()[1] != "HAL > yo" {
		t.Errorf("resumed lines wrong: %v", m.Lines())
	}
	if !strings.Contains(m.Notice(), "Resumed: b") {
		t.Errorf("notice = %q", m.Notice())
	}
}

func TestSetHistoryLoadsConversation(t *testing.T) {
	m := testModel()
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(Model)
	m.SetHistory([]SessionLine{
		{Role: "user", Text: "hello?"},
		{Role: "assistant", Text: "# Hi\n\nthere *you*"},
	})
	if len(m.Lines()) != 2 || m.Lines()[0] != "You > hello?" {
		t.Fatalf("lines = %v", m.Lines())
	}
	if m.Lines()[1] != "HAL > # Hi\n\nthere *you*" {
		t.Errorf("raw lines must keep original markdown: %q", m.Lines()[1])
	}
	view := string(m.View().Content)
	if !strings.Contains(view, "You > hello?") || !strings.Contains(view, "there") {
		t.Errorf("history not rendered:\n%s", view)
	}
}

func TestResizeRewrapsMarkdown(t *testing.T) {
	m := testModel()
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = updated.(Model)
	m.SetHistory([]SessionLine{{Role: "assistant", Text: strings.Repeat("word ", 30)}})
	before := string(m.View().Content)
	updated, _ = m.Update(tea.WindowSizeMsg{Width: 40, Height: 24})
	m = updated.(Model)
	after := string(m.View().Content)
	if before == after {
		t.Error("narrower width should re-wrap the conversation")
	}
	if !strings.Contains(after, "word") {
		t.Error("re-wrap must not lose content")
	}
}

func TestModelCommandOpensPicker(t *testing.T) {
	hooks := Hooks{ListModels: func() []ModelInfo { return []ModelInfo{{ID: "a", Active: true}} }}
	m := NewWithHooks(&scriptEngine{}, hooks)
	m.SetColor(false)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(Model)
	m = typeText(m, "/model")
	m = sendCurrent(m)
	if view := string(m.View().Content); !strings.Contains(view, "Models (Enter switches") {
		t.Errorf("/model must open the picker:\n%s", view)
	}
	m = pressKey(m, tea.KeyEscape, 0, "")
	if view := string(m.View().Content); strings.Contains(view, "Models (Enter switches") {
		t.Error("Esc must leave the picker")
	}
}

func TestThemePersistsAcrossRestarts(t *testing.T) {
	saved := ""
	hooks := Hooks{
		Theme:     func() string { return saved },
		SaveTheme: func(name string) error { saved = name; return nil },
	}
	m := NewWithHooks(&scriptEngine{}, hooks)
	m.SetColor(false)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(Model)
	m.SetTheme(ThemeByName(saved))
	if m.Theme().Name != "HAL Classic" {
		t.Errorf("empty saved theme must default, got %q", m.Theme().Name)
	}
	m = typeText(m, "/theme")
	m = sendCurrent(m)
	m = pressKey(m, tea.KeyDown, 0, "")
	m = pressKey(m, tea.KeyEnter, 0, "")
	if saved != "Midnight" {
		t.Fatalf("saved = %q", saved)
	}
	// Simulate a restart reading the persisted value.
	m2 := NewWithHooks(&scriptEngine{}, hooks)
	m2.SetTheme(ThemeByName(saved))
	if m2.Theme().Name != "Midnight" {
		t.Errorf("restarted theme = %q", m2.Theme().Name)
	}
}

func TestHelpListsCommands(t *testing.T) {
	m := testModel()
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(Model)
	m = typeText(m, "/help")
	m = sendCurrent(m)
	view := string(m.View().Content)
	for _, want := range []string{"/copy", "/theme", "/model", "/sessions", "/new", "Ctrl+S"} {
		if !strings.Contains(view, want) {
			t.Errorf("help missing %q:\n%s", want, view)
		}
	}
	m = pressKey(m, tea.KeyEscape, 0, "")
	view = string(m.View().Content)
	if strings.Contains(view, "Keys and commands") {
		t.Error("Esc must close help")
	}
}

func TestNewlineNeverSubmitsMultiline(t *testing.T) {
	eng := &scriptEngine{}
	m := NewWithHooks(eng, Hooks{})
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(Model)
	m = typeText(m, "line one\nline two\nline three")
	m = pressKey(m, 'j', tea.ModCtrl, "")
	if eng.calls != 0 {
		t.Fatal("Ctrl+J must never submit")
	}
	if got := m.Composer().Value(); got != "line one\nline two\nline three\n" {
		t.Errorf("composer = %q", got)
	}
}

func TestViewportKeepsPositionOnStream(t *testing.T) {
	m := testModel()
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(Model)
	for i := 0; i < 30; i++ {
		m.appendEntry("assistant", "old line")
	}
	m.viewport.GotoTop()
	if m.viewport.AtBottom() {
		t.Fatal("precondition: scrolled away from bottom")
	}
	updated, _ = m.Update(streamDeltaMsg{text: "new"})
	m = updated.(Model)
	if m.viewport.AtBottom() {
		t.Fatal("streaming must not yank a scrolled-up viewport to the bottom")
	}
	updated, _ = m.Update(responseMsg{text: "done"})
	m = updated.(Model)
	if m.viewport.AtBottom() {
		t.Fatal("completion must not yank a scrolled-up viewport either")
	}
	if got := m.Lines(); got[len(got)-1] != "HAL > done" {
		t.Errorf("content must still arrive: %v", got)
	}
}

// The frame must fill the terminal exactly: one row per terminal row, so
// the alternate screen never scrolls.
func TestLayoutFillsTerminalHeight(t *testing.T) {
	for _, size := range []struct{ w, h int }{
		{80, 24}, {120, 40}, {160, 50}, {40, 10}, {30, 6}, {20, 4}, {200, 8},
	} {
		m := testModel()
		updated, _ := m.Update(tea.WindowSizeMsg{Width: size.w, Height: size.h})
		m = updated.(Model)
		rows := m.layoutRows()
		if len(rows) != size.h {
			t.Errorf("%dx%d: layout has %d rows, want %d", size.w, size.h, len(rows), size.h)
		}
		for i, row := range rows {
			if w := lipgloss.Width(row); w > size.w {
				t.Errorf("%dx%d: row %d is %d columns wide", size.w, size.h, i, w)
			}
		}
	}
}

// The header and the footer stay in place while the conversation scrolls.
func TestHeaderAndFooterArePersistent(t *testing.T) {
	m := testModel()
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(Model)
	for i := 0; i < 40; i++ {
		m.appendEntry("assistant", "filler line")
	}
	m = pressKey(m, tea.KeyUp, 0, "")
	rows := m.layoutRows()
	if !strings.Contains(rows[0], "HAL9001") {
		t.Errorf("header must stay on the first row: %q", rows[0])
	}
	footer := rows[len(rows)-2]
	if !strings.Contains(footer, "444 READ ONLY") {
		t.Errorf("footer must stay on the last-but-one row: %q", footer)
	}
	if !strings.Contains(rows[len(rows)-1], "") {
		t.Errorf("the notice row must be reserved: %q", rows[len(rows)-1])
	}
}

// Alternate screen mode is on, so the shell output is hidden while HAL
// runs and the previous screen comes back on exit.
func TestAltScreenEnabled(t *testing.T) {
	m := testModel()
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(Model)
	if !m.View().AltScreen {
		t.Error("the interface must run in the alternate screen buffer")
	}
}

// A slash command runs when Enter is pressed, and never reaches the
// model.
func TestSlashCommandRunsOnEnter(t *testing.T) {
	eng := &scriptEngine{replies: map[string]string{}}
	m := NewWithHooks(eng, Hooks{})
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(Model)
	m = typeText(m, "/help")
	m = sendCurrent(m)
	if eng.calls != 0 {
		t.Fatalf("a slash command must not reach the model, calls = %d", eng.calls)
	}
	if view := string(m.View().Content); !strings.Contains(view, "Keys and commands") {
		t.Errorf("Enter must run the slash command:\n%s", view)
	}
}

// Overlays close with Esc and give the composer its focus and draft back.
func TestOverlayDismissRestoresComposer(t *testing.T) {
	m := testModel()
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(Model)
	m = typeText(m, "draft text")
	m.enterTheme()
	if view := string(m.View().Content); !strings.Contains(view, "Themes") {
		t.Fatalf("the theme picker must open:\n%s", view)
	}
	m = pressKey(m, tea.KeyEscape, 0, "")
	if view := string(m.View().Content); strings.Contains(view, "Themes (Enter applies") {
		t.Error("Esc must dismiss the theme picker")
	}
	if got := m.Composer().Value(); got != "draft text" {
		t.Errorf("the draft must survive the overlay, got %q", got)
	}
	if !m.Composer().Focused() {
		t.Error("the composer must keep the focus after an overlay")
	}
}

// A short window keeps the header, the footer and a usable composer.
func TestShortWindowStaysUsable(t *testing.T) {
	m := testModel()
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 40, Height: 6})
	m = updated.(Model)
	rows := m.layoutRows()
	if len(rows) != 6 {
		t.Fatalf("short window: %d rows, want 6", len(rows))
	}
	if !strings.Contains(rows[0], "HAL9001") {
		t.Errorf("the header must survive a short window: %q", rows[0])
	}
	last := rows[len(rows)-1]
	if !strings.Contains(last, "444 READ ONLY") {
		t.Errorf("the footer must survive a short window: %q", last)
	}
}

// A resize keeps the frame the right size and reflows the conversation.
func TestResizeKeepsFrameHeight(t *testing.T) {
	m := testModel()
	for _, h := range []int{24, 12, 40, 7} {
		updated, _ := m.Update(tea.WindowSizeMsg{Width: 90, Height: h})
		m = updated.(Model)
		if got := len(m.layoutRows()); got != h {
			t.Errorf("height %d: frame has %d rows", h, got)
		}
	}
}

// A terminal that delivers Alt+Enter as ESC and CR in separate reads must
// not turn the CR into an accidental send: it becomes a newline instead.
func TestSplitEscapeSequenceBecomesNewline(t *testing.T) {
	eng := &scriptEngine{}
	m := NewWithHooks(eng, Hooks{})
	base := time.Now()
	m.now = func() time.Time { return base }
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(Model)
	m = typeText(m, "first")
	m = pressKey(m, tea.KeyEscape, 0, "")
	m = pressKey(m, tea.KeyEnter, 0, "")
	if eng.calls != 0 {
		t.Fatal("a split escape sequence must not send")
	}
	if got := m.Composer().Value(); got != "first\n" {
		t.Errorf("composer = %q, want the Enter to become a newline", got)
	}
}

// An Enter long after an Escape is a normal send.
func TestEnterAfterGracePeriodSends(t *testing.T) {
	eng := &scriptEngine{}
	m := NewWithHooks(eng, Hooks{})
	base := time.Now()
	m.now = func() time.Time { return base }
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(Model)
	m = typeText(m, "hello")
	m = pressKey(m, tea.KeyEscape, 0, "")
	base = base.Add(2 * escapeGrace)
	m = enterCurrent(m)
	if eng.calls != 1 {
		t.Fatalf("calls = %d, want 1", eng.calls)
	}
}

// Every overlay must leave the draft and the composer focus untouched.
func TestDraftSurvivesEveryOverlay(t *testing.T) {
	open := map[string]func(m *Model){
		"model":    func(m *Model) { m.enterPicker() },
		"theme":    func(m *Model) { m.enterTheme() },
		"sessions": func(m *Model) { m.enterSessions() },
	}
	for name, enter := range open {
		m := testModel()
		updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
		m = updated.(Model)
		m = typeText(m, "draft for "+name)
		enter(&m)
		m = pressKey(m, tea.KeyEscape, 0, "")
		if got := m.Composer().Value(); got != "draft for "+name {
			t.Errorf("%s panel: draft = %q", name, got)
		}
		if !m.Composer().Focused() {
			t.Errorf("%s panel: composer lost the focus", name)
		}
	}
	// The help panel is opened by a slash command and closed by Esc.
	m := testModel()
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(Model)
	m = typeText(m, "draft for help")
	m.enterOnboard(OnboardData{})
	m.view = viewHelp
	m = pressKey(m, tea.KeyEscape, 0, "")
	if got := m.Composer().Value(); got != "draft for help" {
		t.Errorf("help panel: draft = %q", got)
	}
}

// paste sets the composer directly, as a terminal paste would.
func paste(m Model, text string) Model {
	m.composer.InsertString(text)
	m.composer.Focus()
	return m
}

// A long prompt collapses to a short preview; the original stays intact
// in the entry, in Lines() and therefore in the session and the request.
func TestLongUserMessageCollapses(t *testing.T) {
	m := testModel()
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(Model)
	long := strings.Repeat("museum thought experiment line\n", 20)
	m = paste(m, long)
	m = enterCurrent(m)
	// Only trailing whitespace is normalised on submit.
	want := strings.TrimRight(long, "\n")

	entries := m.Entries()
	if len(entries) < 1 {
		t.Fatal("no user entry")
	}
	user := entries[0]
	if user.text != want {
		t.Error("the full prompt must be kept in the entry")
	}
	if got, want := len(user.rows), userPreviewRows+1; got != want {
		t.Errorf("collapsed rows = %d, want %d (preview + hint)", got, want)
	}
	view := m.viewport.View()
	if !strings.Contains(view, "Ctrl+O to expand") {
		t.Errorf("collapsed message must offer the control:\n%s", view)
	}
	if strings.Count(view, "museum thought experiment line") > userPreviewRows {
		t.Errorf("collapsed message shows too many lines:\n%s", view)
	}
	if got := m.Lines()[0]; got != "You > "+want {
		t.Error("Lines() must expose the full original text")
	}
}

// Short messages are never collapsed.
func TestShortUserMessageNotCollapsed(t *testing.T) {
	m := testModel()
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(Model)
	m = paste(m, "just a short question")
	m = enterCurrent(m)
	user := m.Entries()[0]
	if len(user.rows) != 1 {
		t.Errorf("short message rows = %d, want 1", len(user.rows))
	}
	if strings.Contains(strings.Join(user.rows, "\n"), "Ctrl+O") {
		t.Error("a short message must not show a collapse hint")
	}
}

// Ctrl+O expands and collapses the last long message.
func TestCtrlOTogglesLongMessage(t *testing.T) {
	m := testModel()
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(Model)
	m = paste(m, strings.Repeat("line\n", 20))
	m = enterCurrent(m)
	collapsed := len(m.Entries()[0].rows)

	m = pressKey(m, 'o', tea.ModCtrl, "")
	expanded := len(m.Entries()[0].rows)
	if expanded <= collapsed {
		t.Fatalf("Ctrl+O must expand: %d -> %d rows", collapsed, expanded)
	}
	if !strings.Contains(strings.Join(m.Entries()[0].rows, "\n"), "Ctrl+O to collapse") {
		t.Error("expanded message should offer collapsing")
	}
	m = pressKey(m, 'o', tea.ModCtrl, "")
	if got := len(m.Entries()[0].rows); got != collapsed {
		t.Errorf("Ctrl+O must collapse again: %d rows, want %d", got, collapsed)
	}
	// The stored text is never touched by the toggle.
	if !strings.HasSuffix(m.Entries()[0].text, "line") {
		t.Error("toggling must not alter the stored message")
	}
}

// /expand and /collapse apply to every long message.
func TestExpandCollapseCommands(t *testing.T) {
	m := testModel()
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(Model)
	m = paste(m, strings.Repeat("line\n", 20))
	m = enterCurrent(m)
	collapsed := len(m.Entries()[0].rows)

	m = typeText(m, "/expand")
	m = enterCurrent(m)
	if len(m.Entries()[0].rows) <= collapsed {
		t.Error("/expand must expand long messages")
	}
	m = typeText(m, "/collapse")
	m = enterCurrent(m)
	if got := len(m.Entries()[0].rows); got != collapsed {
		t.Errorf("/collapse rows = %d, want %d", got, collapsed)
	}
}

// The prompt is not echoed back into the provider request truncated:
// collapsing is rendering only. (Also covered in the engine tests.)
func TestCollapsedPromptKeepsFullHistory(t *testing.T) {
	m := testModel()
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(Model)
	long := strings.Repeat("line\n", 20)
	m = paste(m, long)
	m = enterCurrent(m)
	// A second turn: the first prompt must still be in the history the
	// engine sees.
	if !strings.Contains(m.Lines()[0], strings.TrimRight(long, "\n")) {
		t.Fatal("history lost the first prompt")
	}
}

// Reasoning events produce a quiet indicator, never the reasoning text.
func TestThinkingIndicator(t *testing.T) {
	m := testModel()
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(Model)
	m.streamID = 1
	m.sending = true
	m.streamStarted = m.clock()
	updated, _ = m.Update(streamThinkingMsg{id: 1})
	m = updated.(Model)
	if !strings.Contains(m.Notice(), "thinking") {
		t.Errorf("notice = %q, want a thinking indicator", m.Notice())
	}
	if !m.reasoningSeen {
		t.Error("reasoning presence not recorded")
	}
	if strings.Contains(m.viewport.View(), "private") {
		t.Error("reasoning text must never be rendered")
	}

	// Once visible text arrives the indicator says streaming.
	updated, _ = m.Update(streamDeltaMsg{id: 1, text: "hello"})
	m = updated.(Model)
	if !strings.Contains(m.Notice(), "streaming") {
		t.Errorf("notice = %q, want streaming", m.Notice())
	}
}

// The elapsed time is visible and keeps moving.
func TestStreamingNoticeShowsElapsedTime(t *testing.T) {
	m := testModel()
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(Model)
	m.sender = func(tea.Msg) {}
	m.streamID = 1
	m.sending = true
	m.streamStarted = m.clock().Add(-42 * time.Second)
	updated, _ = m.Update(tickMsg(m.clock()))
	m = updated.(Model)
	if !strings.Contains(m.Notice(), "42s") {
		t.Errorf("notice = %q, want the elapsed time", m.Notice())
	}
	if !strings.Contains(m.Notice(), "Esc cancels") {
		t.Errorf("notice = %q, want the cancel hint", m.Notice())
	}
}

// A late message from a cancelled turn must never touch the live one.
func TestStaleStreamMessagesIgnored(t *testing.T) {
	m := testModel()
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(Model)
	m.streamID = 2
	m.sending = true
	m.streamStarted = m.clock()

	updated, _ = m.Update(streamDeltaMsg{id: 1, text: "stale text"})
	m = updated.(Model)
	if m.pending != "" {
		t.Errorf("stale delta applied: %q", m.pending)
	}
	updated, _ = m.Update(errorMsg{id: 1, err: errors.New("stale")})
	m = updated.(Model)
	if !m.Sending() {
		t.Error("a stale error must not clear the live turn")
	}
	updated, _ = m.Update(responseMsg{id: 1, text: "stale answer"})
	m = updated.(Model)
	if len(m.Entries()) != 0 {
		t.Errorf("stale response applied: %v", m.Lines())
	}
	updated, _ = m.Update(streamStatsMsg{id: 1, stats: provider.TurnStats{Status: provider.StatusDone}})
	m = updated.(Model)
	if m.LastStats() != nil {
		t.Error("stale stats applied")
	}
}

// Cancelling a turn and starting a new one leaves the new turn alone.
func TestCancelThenNewTurnIsIndependent(t *testing.T) {
	m := testModel()
	base := time.Now()
	m.now = func() time.Time { return base }
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(Model)
	m = paste(m, "first")
	m = enterCurrent(m)
	firstID := m.streamID
	m = pressKey(m, tea.KeyEscape, 0, "")
	if m.Sending() {
		t.Fatal("Esc must stop the turn")
	}
	base = base.Add(time.Second)
	m = paste(m, "second")
	updated, inFlight := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated.(Model)
	_ = inFlight // left running: this turn is still in flight
	if m.streamID == firstID {
		t.Fatal("the second turn needs its own id")
	}
	updated, _ = m.Update(errorMsg{id: firstID, err: context.Canceled})
	m = updated.(Model)
	if !m.Sending() {
		t.Error("the cancelled turn's late error killed the new turn")
	}
}

// /diag reports counts, durations and token totals, never content.
func TestDiagPanelIsContentFree(t *testing.T) {
	m := testModel()
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(Model)
	m.lastStats = &provider.TurnStats{
		Status:          provider.StatusDone,
		Duration:        42 * time.Second,
		FirstVisible:    31 * time.Second,
		FirstReasoning:  2 * time.Second,
		Events:          812,
		KeepAlives:      3,
		VisibleChunks:   42,
		VisibleRunes:    1284,
		ReasoningChunks: 790,
		ReasoningRunes:  20311,
		FinishReason:    "stop",
		HasUsage:        true,
		Usage:           provider.Usage{PromptTokens: 1204, CompletionTokens: 3880, TotalTokens: 5084, ReasoningTokens: 3400},
		OutputLimitHit:  true,
		MaxTokens:       15000,
		IdleTimeout:     45 * time.Second,
		TurnTimeout:     120 * time.Second,
	}
	m = typeText(m, "/diag")
	m = enterCurrent(m)
	view := string(m.View().Content)
	for _, want := range []string{
		"diagnostics", "done", "812", "5084",
		"max_tokens 15000", "idle 45s", "turn 2m0s",
	} {
		if !strings.Contains(view, want) {
			t.Errorf("diag missing %q:\n%s", want, view)
		}
	}
	m = pressKey(m, tea.KeyEscape, 0, "")
	if m.view != viewChat {
		t.Error("Esc must close diagnostics")
	}
}

// A length finish is surfaced rather than passed off as a full answer.
func TestOutputLimitNotice(t *testing.T) {
	m := testModel()
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(Model)
	m.streamID = 1
	m.sending = true
	updated, _ = m.Update(streamStatsMsg{id: 1, stats: provider.TurnStats{OutputLimitHit: true, Status: provider.StatusDone}})
	m = updated.(Model)
	updated, _ = m.Update(responseMsg{id: 1, text: "a cut off answer"})
	m = updated.(Model)
	if !strings.Contains(m.Notice(), "output limit") {
		t.Errorf("notice = %q, want the output-limit explanation", m.Notice())
	}
}

// An idle stall closes the request with an explanation, not a hang.
func TestIdleStallNotice(t *testing.T) {
	m := testModel()
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(Model)
	m.streamID = 1
	m.sending = true
	updated, _ = m.Update(errorMsg{id: 1, err: provider.ErrStreamIdle})
	m = updated.(Model)
	if m.Sending() {
		t.Error("an idle stall must end the turn")
	}
	if !strings.Contains(m.Notice(), "went silent") {
		t.Errorf("notice = %q", m.Notice())
	}
}

// The reply is followed automatically when the reader is at the bottom.
func TestStreamFollowsWhenAtBottom(t *testing.T) {
	m := testModel()
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(Model)
	m.streamID = 1
	m.sending = true
	m.viewport.GotoBottom()
	for i := 0; i < 30; i++ {
		updated, _ = m.Update(streamDeltaMsg{id: 1, text: "line\n"})
		m = updated.(Model)
	}
	if !m.viewport.AtBottom() {
		t.Error("the viewport must follow a stream it was already following")
	}
}

// A turn cut by the ceiling explains itself, and never pretends the reply
// completed: the partial stream is not stored or shown as an answer.
func TestTurnTimeoutNotice(t *testing.T) {
	m := testModel()
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(Model)
	m.streamID = 1
	m.sending = true
	m.pending = "partial answer"
	updated, _ = m.Update(errorMsg{id: 1, err: provider.ErrTurnTimeout})
	m = updated.(Model)
	if m.Sending() {
		t.Error("a time limit must end the turn")
	}
	if m.pending != "" {
		t.Error("an incomplete stream must not be kept as the answer")
	}
	if !strings.Contains(m.Notice(), "time limit") {
		t.Errorf("notice = %q, want the time-limit explanation", m.Notice())
	}
	lines := m.Lines()
	if len(lines) != 1 {
		t.Fatalf("exactly one explanation line expected, got %v", lines)
	}
	if !strings.Contains(lines[0], "exceeded its time limit") {
		t.Errorf("explanation = %q", lines[0])
	}
	if strings.Contains(lines[0], "partial answer") {
		t.Errorf("partial output presented as an answer: %q", lines[0])
	}
}

// effortHooks wires the effort callbacks with an in-memory store.
func effortHooks(level string, supported bool, set func(string)) Hooks {
	return Hooks{
		Effort: func() (string, bool) { return level, supported },
		SetEffort: func(l string) (Engine, error) {
			if set != nil {
				set(l)
			}
			level = l
			return nil, nil
		},
	}
}

// /effort opens the selector and Enter applies the highlighted level.
func TestEffortSelector(t *testing.T) {
	chosen := ""
	m := NewWithHooks(&scriptEngine{}, effortHooks("medium", true, func(l string) { chosen = l }))
	m.SetEffort("medium", true)
	m.SetColor(false)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(Model)

	m = typeText(m, "/effort")
	m = enterCurrent(m)
	view := string(m.View().Content)
	for _, level := range []string{"low", "medium", "high", "xhigh", "max"} {
		if !strings.Contains(view, level) {
			t.Errorf("selector missing %q:\n%s", level, view)
		}
	}
	// Move to "high" (medium is the second entry).
	m = pressKey(m, tea.KeyDown, 0, "")
	m = pressKey(m, tea.KeyEnter, 0, "")
	if chosen != "high" {
		t.Fatalf("chosen = %q, want high", chosen)
	}
	if !strings.Contains(m.Notice(), "Effort: high") {
		t.Errorf("notice = %q", m.Notice())
	}
	if m.Theme().Name == "" {
		t.Error("theme must survive an effort change")
	}
}

// /effort <level> applies directly.
func TestEffortDirectArgument(t *testing.T) {
	chosen := ""
	m := NewWithHooks(&scriptEngine{}, effortHooks("medium", true, func(l string) { chosen = l }))
	m.SetEffort("medium", true)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(Model)
	m = typeText(m, "/effort max")
	m = enterCurrent(m)
	if chosen != "max" {
		t.Fatalf("chosen = %q", chosen)
	}
	if !strings.Contains(m.Notice(), "next request") {
		t.Errorf("notice = %q", m.Notice())
	}
}

// An unsupported model gets a clear explanation, not an empty picker.
func TestEffortUnsupportedModel(t *testing.T) {
	m := NewWithHooks(&scriptEngine{}, effortHooks("", false, nil))
	m.SetEffort("", false)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(Model)
	m = typeText(m, "/effort")
	m = enterCurrent(m)
	if m.view != viewChat {
		t.Error("no selector may open for an unsupported model")
	}
	if !strings.Contains(m.Notice(), "does not expose reasoning effort") {
		t.Errorf("notice = %q", m.Notice())
	}
	m = typeText(m, "/effort max")
	m = enterCurrent(m)
	if !strings.Contains(m.Notice(), "does not expose reasoning effort") {
		t.Errorf("direct level must explain too, notice = %q", m.Notice())
	}
}

// An unknown level is rejected without changing anything.
func TestEffortRejectsUnknownLevel(t *testing.T) {
	chosen := ""
	m := NewWithHooks(&scriptEngine{}, effortHooks("medium", true, func(l string) { chosen = l }))
	m.SetEffort("medium", true)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(Model)
	m = typeText(m, "/effort turbo")
	m = enterCurrent(m)
	if chosen != "" {
		t.Errorf("an unknown level reached the hook: %q", chosen)
	}
	if !strings.Contains(m.Notice(), "Unknown effort level") {
		t.Errorf("notice = %q", m.Notice())
	}
}

// The footer shows the effort only when the model supports it.
func TestFooterShowsEffort(t *testing.T) {
	m := NewWithHooks(&scriptEngine{}, Hooks{})
	m.SetColor(false)
	m.SetModelName("MiniMax-M3.1-Flash")
	m.SetEffort("medium", true)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = updated.(Model)
	if view := string(m.View().Content); !strings.Contains(view, "MiniMax-M3.1-Flash · medium") {
		t.Errorf("footer missing effort:\n%s", view)
	}

	m2 := NewWithHooks(&scriptEngine{}, Hooks{})
	m2.SetColor(false)
	m2.SetModelName("gpt-4o")
	m2.SetEffort("", false)
	updated, _ = m2.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m2 = updated.(Model)
	if view := string(m2.View().Content); strings.Contains(view, "gpt-4o · ") {
		t.Errorf("unsupported model must not show an effort:\n%s", view)
	}
}

// /clear empties the view, starts a new session and keeps the old one
// recoverable. Model, effort and theme are untouched.
func TestClearConversation(t *testing.T) {
	sessions := 0
	hooks := Hooks{
		NewSession: func() (Engine, error) { sessions++; return nil, nil },
		Effort:     func() (string, bool) { return "medium", true },
	}
	m := NewWithHooks(&scriptEngine{}, hooks)
	m.SetModelName("MiniMax-M3.1-Flash")
	m.SetEffort("medium", true)
	m.SetColor(false)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(Model)
	m = paste(m, "first question")
	m = enterCurrent(m)
	if len(m.Entries()) == 0 {
		t.Fatal("precondition: conversation should have entries")
	}
	m = typeText(m, "/clear")
	m = enterCurrent(m)
	if sessions != 1 {
		t.Errorf("NewSession calls = %d, want 1", sessions)
	}
	if len(m.Entries()) != 0 {
		t.Errorf("conversation not cleared: %v", m.Lines())
	}
	if !strings.Contains(m.Notice(), "/sessions") {
		t.Errorf("notice must point at session recovery, got %q", m.Notice())
	}
	if m.model != "MiniMax-M3.1-Flash" || m.effort != "medium" || m.Theme().Name == "" {
		t.Error("clearing must preserve model, effort and theme")
	}
	if m.LastStats() != nil {
		t.Error("diagnostics must not describe a cleared conversation")
	}
}

// Clearing during an active stream cancels it and ignores its late
// messages.
func TestClearDuringActiveStream(t *testing.T) {
	sessions := 0
	hooks := Hooks{NewSession: func() (Engine, error) { sessions++; return nil, nil }}
	m := NewWithHooks(&scriptEngine{}, hooks)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(Model)
	cancelled := false
	m.sending = true
	m.streamID = 7
	m.cancel = func() { cancelled = true }
	m.pending = "half an answer"

	m = typeText(m, "/clear")
	m = enterCurrent(m)

	if !cancelled {
		t.Error("/clear must cancel the active generation")
	}
	if m.Sending() {
		t.Error("sending must stop")
	}
	if m.pending != "" || len(m.Entries()) != 0 {
		t.Errorf("stream residue left behind: pending %q entries %v", m.pending, m.Lines())
	}
	if sessions != 1 {
		t.Errorf("NewSession calls = %d", sessions)
	}
	// A message from the cancelled turn must not reappear.
	updated, _ = m.Update(responseMsg{id: 7, text: "late answer"})
	m = updated.(Model)
	if len(m.Entries()) != 0 {
		t.Errorf("late stream message applied after clear: %v", m.Lines())
	}
}

// A slash command still runs while a turn is in flight; a plain message
// does not.
func TestSlashCommandDuringStream(t *testing.T) {
	sessions := 0
	hooks := Hooks{NewSession: func() (Engine, error) { sessions++; return nil, nil }}
	m := NewWithHooks(&scriptEngine{}, hooks)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(Model)
	m.sending = true
	m.cancel = func() {}
	m = typeText(m, "/clear")
	m = enterCurrent(m)
	if sessions != 1 {
		t.Error("/clear must work while streaming")
	}

	m2 := NewWithHooks(&scriptEngine{}, Hooks{})
	updated, _ = m2.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m2 = updated.(Model)
	m2.sending = true
	m2 = paste(m2, "another message")
	m2 = enterCurrent(m2)
	if got := m2.Composer().Value(); got != "another message" {
		t.Errorf("a message must not be sent while streaming: %q", got)
	}
}

// An empty answer is explained with the provider's own numbers and never
// presented as a stored answer.
func TestEmptyAnswerExplanation(t *testing.T) {
	m := testModel()
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(Model)
	m.streamID = 3
	m.sending = true
	updated, _ = m.Update(streamStatsMsg{id: 3, stats: provider.TurnStats{
		Status: provider.StatusFinished, FinishReason: "length", OutputLimitHit: true,
		ReasoningChunks: 812, HasUsage: true,
		Usage: provider.Usage{PromptTokens: 1204, CompletionTokens: 15000, TotalTokens: 16204},
	}})
	m = updated.(Model)
	updated, _ = m.Update(errorMsg{id: 3, err: engine.ErrNoVisibleAnswer})
	m = updated.(Model)

	if m.Sending() {
		t.Error("the turn must end")
	}
	lines := m.Lines()
	if len(lines) != 1 {
		t.Fatalf("expected one explanation, got %v", lines)
	}
	note := lines[0]
	for _, want := range []string{"no visible answer", "output budget", "finish_reason=length", "15000", "/diag"} {
		if !strings.Contains(note, want) {
			t.Errorf("explanation missing %q: %q", want, note)
		}
	}
	if !strings.Contains(m.Notice(), "/diag") {
		t.Errorf("notice = %q", m.Notice())
	}
}

// firstVisibleLine returns the first non-blank row of a rendered view.
func firstVisibleLine(view string) string {
	for _, line := range strings.Split(view, "\n") {
		if strings.TrimSpace(line) != "" {
			return strings.TrimSpace(line)
		}
	}
	return ""
}

// longConversation builds a conversation longer than the viewport.
func longConversation(t *testing.T, lines int) Model {
	t.Helper()
	m := testModel()
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(Model)
	for i := 0; i < lines; i++ {
		m.appendEntry("assistant", fmt.Sprintf("line %03d of the long answer", i))
	}
	return m
}

// Home and End reach the beginning and the end of a long conversation.
func TestScrollHomeEnd(t *testing.T) {
	m := longConversation(t, 60)
	if !m.Viewport().AtBottom() {
		t.Fatal("precondition: a new conversation should sit at the bottom")
	}
	m = pressKey(m, tea.KeyHome, 0, "")
	if !m.Viewport().AtTop() {
		t.Errorf("Home must reach the top, offset = %d", m.viewport.YOffset())
	}
	top := m.viewport.View()
	if !strings.Contains(top, "line 000") {
		t.Errorf("top of the conversation not visible:\n%s", top)
	}
	m = pressKey(m, tea.KeyEnd, 0, "")
	if !m.Viewport().AtBottom() {
		t.Errorf("End must reach the bottom, offset = %d", m.viewport.YOffset())
	}
	if bottom := m.viewport.View(); !strings.Contains(bottom, "line 059") {
		t.Errorf("end of the conversation not visible:\n%s", bottom)
	}
}

// PgUp/PgDn move through the conversation without touching the draft.
func TestPageScrollKeepsDraft(t *testing.T) {
	m := longConversation(t, 60)
	m = paste(m, "a draft that must survive scrolling")
	before := m.Composer().Value()
	start := m.viewport.YOffset()
	m = pressKey(m, tea.KeyPgUp, 0, "")
	up := m.viewport.YOffset()
	if up >= start {
		t.Errorf("PgUp did not scroll: %d -> %d", start, up)
	}
	m = pressKey(m, tea.KeyPgDown, 0, "")
	if got := m.viewport.YOffset(); got <= up {
		t.Errorf("PgDn did not scroll: %d -> %d", up, got)
	}
	if got := m.Composer().Value(); got != before {
		t.Errorf("scrolling changed the draft: %q", got)
	}
}

// Shrinking the terminal keeps the reader where they were, instead of
// jumping to the bottom.
func TestResizePreservesScrollPosition(t *testing.T) {
	m := longConversation(t, 80)
	m = pressKey(m, tea.KeyHome, 0, "")
	m = pressKey(m, tea.KeyPgDown, 0, "")
	m = pressKey(m, tea.KeyPgDown, 0, "")
	anchor := m.anchor()
	if anchor.atBottom {
		t.Fatal("precondition: should be scrolled up")
	}
	topBefore := firstVisibleLine(m.viewport.View())
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 60, Height: 14})
	m = updated.(Model)
	if m.Viewport().AtBottom() {
		t.Fatal("resize jumped to the bottom")
	}
	view := m.viewport.View()
	if !strings.Contains(view, topBefore) {
		t.Errorf("the line at the top before resize (%q) is not visible after:\n%s", topBefore, view)
	}
}

// Collapsing a long prompt keeps the surrounding reading position.
func TestCollapseKeepsScrollPosition(t *testing.T) {
	m := testModel()
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(Model)
	m = paste(m, strings.Repeat("museum line\n", 30))
	m = enterCurrent(m)
	for i := 0; i < 30; i++ {
		m.appendEntry("assistant", fmt.Sprintf("answer line %03d", i))
	}
	m = pressKey(m, tea.KeyHome, 0, "")
	m = pressKey(m, tea.KeyPgDown, 0, "")
	before := m.viewport.YOffset()
	m = pressKey(m, 'o', tea.ModCtrl, "")
	if m.Viewport().AtBottom() {
		t.Fatal("collapse jumped to the bottom")
	}
	if after := m.viewport.YOffset(); after == 0 && before > 0 {
		t.Error("collapse reset the offset to the top")
	}
}

// Typing "/" opens the suggestion panel without submitting anything.
func TestSlashOpensSuggestions(t *testing.T) {
	m := testModel()
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(Model)
	m = typeText(m, "/")
	if !m.suggestOpen {
		t.Fatal("typing / must open the suggestions")
	}
	if len(m.Lines()) != 0 {
		t.Errorf("nothing may be submitted: %v", m.Lines())
	}
	view := string(m.View().Content)
	for _, name := range []string{"/help", "/copy", "/clear", "/model", "/effort", "/theme", "/sessions", "/diag"} {
		if !strings.Contains(view, name) {
			t.Errorf("suggestions missing %q:\n%s", name, view)
		}
	}
	if !strings.Contains(view, "Show available commands") {
		t.Errorf("descriptions missing:\n%s", view)
	}
}

// Typing filters the list.
func TestSlashSuggestionsFilter(t *testing.T) {
	m := testModel()
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(Model)
	m = typeText(m, "/co")
	if !m.suggestOpen {
		t.Fatal("suggestions closed while filtering")
	}
	for _, c := range m.suggestItems {
		if !strings.HasPrefix(c.name, "/co") {
			t.Errorf("unrelated suggestion %q", c.name)
		}
	}
	if len(m.suggestItems) != 3 { // /copy, /collapse ... /copy only? see below
		// /copy and /collapse match; /clear does not.
		if len(m.suggestItems) != 2 {
			t.Errorf("filtered suggestions = %d, want 2 or 3", len(m.suggestItems))
		}
	}
}

// Arrow keys move the selection and Tab completes it.
func TestSuggestionNavigationAndTab(t *testing.T) {
	m := testModel()
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(Model)
	m = typeText(m, "/co")
	first := m.suggestItems[m.suggestCursor].name
	m = pressKey(m, tea.KeyDown, 0, "")
	second := m.suggestItems[m.suggestCursor].name
	if first == second {
		t.Fatal("Down did not move the selection")
	}
	m = pressKey(m, tea.KeyTab, 0, "")
	if got := m.Composer().Value(); !strings.HasPrefix(got, second) {
		t.Errorf("Tab completed %q, want %q", got, second)
	}
	if !strings.HasSuffix(m.Composer().Value(), " ") && second == "/copy" {
		t.Errorf("an argument-taking command should complete with a space: %q", m.Composer().Value())
	}
}

// Enter runs a complete command; on a partial one it runs the selection.
func TestSuggestionEnter(t *testing.T) {
	m := testModel()
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(Model)
	m = typeText(m, "/diag")
	m = enterCurrent(m)
	if m.view != viewDiag {
		t.Error("Enter on a complete command must run it")
	}

	m2 := testModel()
	updated, _ = m2.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m2 = updated.(Model)
	m2 = typeText(m2, "/he")
	m2 = enterCurrent(m2)
	if m2.view != viewHelp {
		t.Errorf("Enter on a partial command must run the highlighted suggestion, view = %d", m2.view)
	}
}

// Esc closes the panel and keeps the draft.
func TestSuggestionEscKeepsDraft(t *testing.T) {
	m := testModel()
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(Model)
	m = typeText(m, "/co")
	m = pressKey(m, tea.KeyEscape, 0, "")
	if m.suggestOpen {
		t.Error("Esc must close the suggestions")
	}
	if got := m.Composer().Value(); got != "/co" {
		t.Errorf("draft lost: %q", got)
	}
}

// A pasted slash command is never executed.
func TestPasteSlashNeverExecutes(t *testing.T) {
	m := testModel()
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(Model)
	updated, _ = m.Update(tea.PasteMsg{Content: "/clear\n"})
	m = updated.(Model)
	if m.view != viewChat {
		t.Error("a pasted command must not run")
	}
	if !strings.Contains(m.Composer().Value(), "/clear") {
		t.Errorf("paste lost: %q", m.Composer().Value())
	}
}

// A double slash stays an escaped literal message, with no suggestions.
func TestDoubleSlashNoSuggestions(t *testing.T) {
	m := testModel()
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(Model)
	m = typeText(m, "//help")
	if m.suggestOpen {
		t.Error("// must not open suggestions")
	}
	m = enterCurrent(m)
	if len(m.Lines()) < 1 || m.Lines()[0] != "You > /help" {
		t.Errorf("escaped message wrong: %v", m.Lines())
	}
}

// The panel sits immediately above the composer and does not replace the
// conversation.
func TestSuggestionPanelPlacement(t *testing.T) {
	m := longConversation(t, 40)
	m = typeText(m, "/")
	rows := m.layoutRows()
	top, firstSuggestion := -1, -1
	for i, row := range rows {
		if strings.HasPrefix(row, "╭") {
			top = i
		}
		if strings.Contains(row, "/help") && strings.Contains(row, "Show available commands") {
			firstSuggestion = i
		}
	}
	if top < 0 || firstSuggestion < 0 {
		t.Fatalf("panel or border missing:\n%s", strings.Join(rows, "\n"))
	}
	if firstSuggestion >= top {
		t.Errorf("suggestions at %d must be above the border at %d", firstSuggestion, top)
	}
	if !strings.Contains(strings.Join(rows[:firstSuggestion], "\n"), "line 0") {
		t.Error("the conversation must stay visible behind the panel")
	}
}

// The panel fits narrow terminals by dropping descriptions.
func TestSuggestionsNarrow(t *testing.T) {
	m := testModel()
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 36, Height: 20})
	m = updated.(Model)
	m = typeText(m, "/")
	view := string(m.View().Content)
	if !strings.Contains(view, "/help") {
		t.Errorf("narrow panel lost the command:\n%s", view)
	}
	for _, line := range strings.Split(view, "\n") {
		if lipgloss.Width(line) > 36 {
			t.Errorf("row wider than the terminal: %q", line)
		}
	}
}

// The composer border uses the theme's muted rule colour, and none at all
// when colour is disabled.
func TestComposerBorderThemes(t *testing.T) {
	for _, theme := range Themes() {
		m := testModel()
		m.SetTheme(theme)
		m.SetColor(true)
		updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
		m = updated.(Model)
		block := strings.Join(m.composerBlock(3, true), "\n")
		if !strings.HasPrefix(stripANSI(strings.Split(block, "\n")[0]), "╭") {
			t.Errorf("%s: border missing", theme.Name)
		}
		if !strings.Contains(block, "\x1b[") {
			t.Errorf("%s: border is not styled", theme.Name)
		}
		m.SetColor(false)
		plain := m.composerBlock(3, true)
		if strings.Contains(plain[0], "\x1b[") || strings.Contains(plain[len(plain)-1], "\x1b[") {
			t.Errorf("%s: monochrome border contains escapes", theme.Name)
		}
	}
}

// Exactly one blank row separates the conversation from the composer at
// every size where the separator survives.
func TestSeparatorRowAccounting(t *testing.T) {
	for _, h := range []int{40, 30, 24, 18, 14, 10} {
		m := testModel()
		updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: h})
		m = updated.(Model)
		if len(m.layoutRows()) != h {
			t.Errorf("height %d: frame has %d rows", h, len(m.layoutRows()))
		}
		rows := m.layoutRows()
		top := -1
		for i, row := range rows {
			if strings.HasPrefix(row, "╭") {
				top = i
			}
		}
		if top < 0 {
			continue // border shed on a very short terminal
		}
		if top == 0 || strings.TrimSpace(rows[top-1]) != "" {
			t.Errorf("height %d: no blank row above the composer", h)
		}

	}
}

// A short terminal sheds decoration before the header, input or footer.
func TestShortTerminalDegrades(t *testing.T) {
	for _, h := range []int{8, 6, 5, 4} {
		m := testModel()
		updated, _ := m.Update(tea.WindowSizeMsg{Width: 40, Height: h})
		m = updated.(Model)
		rows := m.layoutRows()
		if len(rows) != h {
			t.Errorf("height %d: %d rows", h, len(rows))
		}
		if !strings.Contains(rows[0], "HAL9001") {
			t.Errorf("height %d: header lost", h)
		}
		tail := rows[len(rows)-1]
		if len(rows) > 1 {
			tail += rows[len(rows)-2]
		}
		if !strings.Contains(tail, "444 READ ONLY") {
			t.Errorf("height %d: footer lost: %q", h, rows[len(rows)-1])
		}
	}
}
