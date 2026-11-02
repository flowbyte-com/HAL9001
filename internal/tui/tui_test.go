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
	if !strings.Contains(view, "444 READ ONLY") {
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

func TestComposerBorderlessGrowsToMax(t *testing.T) {
	m := testModel()
	view := string(m.View().Content)
	if strings.Contains(view, "┃") {
		t.Error("composer must not render line-number gutters")
	}
	for i := 0; i < 20; i++ {
		m = typeText(m, "line\n")
	}
	if h := m.Composer().Height(); h != 8 {
		t.Errorf("composer height = %d, want cap 8", h)
	}
	m2 := testModel()
	m2 = typeText(m2, "one\ntwo\nthree")
	if h := m2.Composer().Height(); h != 4 {
		t.Errorf("composer height = %d, want 4 for three lines", h)
	}
}

func TestComposerRulesPresent(t *testing.T) {
	m := testModel()
	m.SetColor(false)
	view := string(m.View().Content)
	rules := 0
	for _, line := range strings.Split(view, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed != "" && strings.Trim(trimmed, "─") == "" {
			rules++
		}
	}
	if rules < 2 {
		t.Errorf("expected thin rules above and below the composer, found %d:\n%s", rules, view)
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
	m = pressKey(m, tea.KeyEnter, 0, "")
	if eng.calls != 0 {
		t.Fatal("Enter must never submit")
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
		m.appendLine("old line")
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
