package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// A paste into a management form lands in the focused field. It never
// runs a menu shortcut and never saves.
func TestPasteIntoManagementFormInsertsText(t *testing.T) {
	m := testModel()
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(Model)
	m.hooks = testManageHooks()
	// Open the model form directly.
	m.openAddModelForm("primary")
	if m.manage.screen != manageForm {
		t.Fatalf("screen = %v, want form", m.manage.screen)
	}
	pasted := "https://api.deepseek.com/anthropic"
	updated, _ = m.Update(tea.PasteMsg{Content: pasted})
	m = updated.(Model)
	if got := m.manage.fields[m.manage.focus].input.Value(); !strings.Contains(got, "deepseek") {
		t.Errorf("the paste did not land in the field: %q", got)
	}
	// The paste did not save or leave the form.
	if m.manage.screen != manageForm {
		t.Errorf("a paste left the form")
	}
}

func TestSingleLinePasteNormalisesLineBreaks(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		want string
	}{
		{"crlf", "https://example.com/v1\r\n", "https://example.com/v1"},
		{"trailing newline", "deepseek-flash\n", "deepseek-flash"},
		{"interior newline", "line-one\nline-two", "line-one line-two"},
		{"tab", "a\tb", "a b"},
		{"surrounding spaces", "  DEEPSEEK_API_KEY  ", "DEEPSEEK_API_KEY"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := normaliseSingleLinePaste(tc.in); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// A paste with newlines into a single-line field is flattened, not
// executed as Enter.
func TestPasteWithNewlinesDoesNotChangeFocus(t *testing.T) {
	m := testModel()
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(Model)
	m.hooks = testManageHooks()
	m.openAddModelForm("primary")
	focus := m.manage.focus
	updated, _ = m.Update(tea.PasteMsg{Content: "first\nsecond"})
	m = updated.(Model)
	if m.manage.focus != focus {
		t.Errorf("a pasted newline moved focus %d -> %d", focus, m.manage.focus)
	}
	if got := m.manage.fields[focus].input.Value(); !strings.Contains(got, "first") {
		t.Errorf("the pasted text was lost: %q", got)
	}
}

func testManageHooks() Hooks {
	return Hooks{
		Endpoints: func() []EndpointInfo {
			return []EndpointInfo{{ID: "primary", Name: "Primary", Protocol: "openai-compatible", BaseURL: "http://127.0.0.1:1/v1"}}
		},
	}
}
