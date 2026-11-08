package tui

import (
	"strings"
	"testing"
)

// The management panels draw names that an endpoint supplied: an
// identifier, a display name, a base URL and a review note. A hostile
// endpoint must not be able to move the cursor, clear the screen or
// forge an extra row with control characters in its own catalogue.

// hostile is a name that tries to escape its row.
const hostile = "\x1b[31mred\x1b[0m\nforged row\tend"

// assertNoHostile checks that no row carries an escape or a line break
// that an endpoint supplied.
//
// The theme's own styling is allowed: it is HAL's own text, and the
// operator asked for it. The escape an endpoint supplied is not.
func assertNoHostile(t *testing.T, rows []string) {
	t.Helper()
	for i, row := range rows {
		if strings.Contains(row, "\x1b[31m") {
			t.Errorf("row %d carries an escape an endpoint supplied: %q", i, row)
		}
		if strings.ContainsAny(row, "\n\r") {
			t.Errorf("row %d carries a line break an endpoint supplied: %q", i, row)
		}
	}
}

func TestOneLineFlattensAName(t *testing.T) {
	flat := oneLine(hostile)
	if strings.ContainsAny(flat, "\n\r\t\x1b") {
		t.Errorf("the flattened name carries a control character: %q", flat)
	}
	if !strings.Contains(flat, "red") {
		t.Errorf("the flattened name lost its text: %q", flat)
	}
}

func TestTheEndpointPanelFlattensTheEndpointNames(t *testing.T) {
	hooks := Hooks{
		Endpoints: func() []EndpointInfo {
			return []EndpointInfo{
				{
					ID: hostile, Name: hostile, Protocol: "openai-compatible",
					WireProtocol: "openai-compatible", BaseURL: hostile,
					Credential: "env:MINIMAX_API_KEY", Active: true,
					Models: []ModelInfo{{ID: hostile, Active: true}},
					Review: hostile,
				},
			}
		},
	}
	m := NewWithHooks(&scriptEngine{}, hooks)
	m = runCommand(m, "/model list")
	if m.view != viewModels {
		t.Fatalf("the view = %v", m.view)
	}
	rows := m.modelCfgRows()
	assertNoHostile(t, rows)
	// The forged row did not become a row of its own: the panel is the
	// title, a blank line, the lines, a blank line and the footer.
	lines := len(m.modelCfgLines)
	if len(rows) != lines+4 {
		t.Errorf("the panel has %d rows for %d lines: a name forged a row", len(rows), lines)
	}
	// The name is still visible: it is flattened, not dropped.
	if !strings.Contains(strings.Join(rows, "\n"), "red") {
		t.Errorf("the endpoint name was dropped:\n%s", strings.Join(rows, "\n"))
	}
}

func TestTheModelPickerFlattensTheModelNames(t *testing.T) {
	hooks := Hooks{
		ListModels: func() []ModelInfo {
			return []ModelInfo{
				{ID: hostile, Endpoint: hostile, Review: hostile},
			}
		},
	}
	m := NewWithHooks(&scriptEngine{}, hooks)
	m.enterPicker()
	rows := m.pickerRows()
	assertNoHostile(t, rows)
	if !strings.Contains(strings.Join(rows, "\n"), "red") {
		t.Errorf("the model name was dropped:\n%s", strings.Join(rows, "\n"))
	}
}
