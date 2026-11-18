package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// The effort selector offers the levels the active model's own family
// documents, and nothing else: a level the provider does not have would
// be a guess it may reject.
func TestTheEffortPickerOffersTheModelsOwnLevels(t *testing.T) {
	cases := []struct {
		model  string
		levels []string
	}{
		{"MiniMax-M3.1-Flash", []string{"low", "medium", "high", "xhigh", "max"}},
		{"deepseek-reasoner", []string{"low", "high", "max"}},
	}
	for _, c := range cases {
		m := effortModel(c.model)
		m = runCommand(m, "/effort")
		if m.view != viewEffort {
			t.Errorf("model %q: the selector did not open", c.model)
			continue
		}
		rows := strings.Join(m.effortRows(), "\n")
		for _, level := range c.levels {
			if !strings.Contains(rows, level) {
				t.Errorf("model %q: the selector is missing %q:\n%s", c.model, level, rows)
			}
		}
		// A level the family does not document is not offered.
		for _, level := range []string{"medium", "xhigh"} {
			offered := false
			for _, l := range c.levels {
				if l == level {
					offered = true
				}
			}
			if offered {
				continue
			}
			for _, row := range m.effortRows() {
				if strings.Contains(row, " "+level) {
					t.Errorf("model %q: the selector offers %q:\n%s", c.model, level, rows)
				}
			}
		}
	}
}

// The picker applies the level the operator chose.
func TestTheEffortPickerAppliesTheChosenLevel(t *testing.T) {
	chosen := ""
	m := NewWithHooks(&scriptEngine{}, effortHooks("high", true, func(l string) { chosen = l }))
	m.SetColor(false)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	m = updated.(Model)
	m.SetModelName("MiniMax-M3.1-Flash")
	m.SetEffort("high", true)

	m = runCommand(m, "/effort")
	if m.view != viewEffort {
		t.Fatal("the selector did not open")
	}
	// The cursor starts on the level in effect.
	if m.effortCursor != 2 {
		t.Errorf("the cursor started on %d, want the level in effect (2)", m.effortCursor)
	}
	m = pressKey(m, tea.KeyDown, 0, "")
	m = pressKey(m, tea.KeyEnter, 0, "")
	if chosen != "xhigh" {
		t.Errorf("chosen = %q, want xhigh", chosen)
	}
	if m.view != viewChat {
		t.Error("the selector stayed open")
	}
}

// The keyboard stays inside the levels the model offers: the cursor
// never walks past the end of the list.
func TestTheEffortCursorStaysInsideTheOfferedLevels(t *testing.T) {
	m := effortModel("deepseek-reasoner")
	m = runCommand(m, "/effort")
	if m.effortCursor != 1 {
		t.Errorf("the cursor started on %d, want the level in effect (1)", m.effortCursor)
	}
	for i := 0; i < 10; i++ {
		m = pressKey(m, tea.KeyDown, 0, "")
	}
	if m.effortCursor != len(m.effortLevels)-1 {
		t.Errorf("the cursor walked to %d, want the last offered level (%d)", m.effortCursor, len(m.effortLevels)-1)
	}
	for i := 0; i < 10; i++ {
		m = pressKey(m, tea.KeyUp, 0, "")
	}
	if m.effortCursor != 0 {
		t.Errorf("the cursor walked to %d, want the first offered level (0)", m.effortCursor)
	}
}

// Changing the model changes the controls: the next selector offers the
// new model's own levels.
func TestChangingTheModelUpdatesTheEffortControls(t *testing.T) {
	m := effortModel("MiniMax-M3.1-Flash")
	m = runCommand(m, "/effort")
	if len(m.effortLevels) != 5 {
		t.Fatalf("the MiniMax selector offered %v", m.effortLevels)
	}
	m = pressKey(m, tea.KeyEscape, 0, "esc")

	// The operator switches to another model.
	m.SetModelName("deepseek-reasoner")
	m = runCommand(m, "/effort")
	if len(m.effortLevels) != 3 {
		t.Errorf("the DeepSeek selector offered %v", m.effortLevels)
	}
	rows := strings.Join(m.effortRows(), "\n")
	if strings.Contains(rows, "medium") {
		t.Errorf("the DeepSeek selector offers a level it does not have:\n%s", rows)
	}
}

// A model that is known to take no reasoning control gets an
// explanation, not an empty picker.
func TestAnUnsupportedModelGetsAnExplanation(t *testing.T) {
	m := NewWithHooks(&scriptEngine{}, effortHooks("", false, nil))
	m.SetColor(false)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	m = updated.(Model)
	m.SetModelName("gpt-4o")

	m = runCommand(m, "/effort")
	if m.view != viewChat {
		t.Error("a selector opened for a model without reasoning control")
	}
	if !strings.Contains(m.Notice(), "does not expose reasoning effort") {
		t.Errorf("notice = %q", m.Notice())
	}
}

// A model HAL knows nothing about gets a different explanation: unknown
// is not the same as unsupported.
func TestAnUnknownModelGetsItsOwnExplanation(t *testing.T) {
	m := NewWithHooks(&scriptEngine{}, effortHooks("", false, nil))
	m.SetColor(false)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	m = updated.(Model)
	m.SetModelName("some-model-nobody-documented")

	m = runCommand(m, "/effort")
	if m.view != viewChat {
		t.Error("a selector opened for a model HAL knows nothing about")
	}
	if !strings.Contains(m.Notice(), "doesn't know the reasoning controls") {
		t.Errorf("notice = %q", m.Notice())
	}
}

// A level the model does not take is refused rather than applied.
func TestTheEffortPickerRefusesALevelTheModelDoesNotTake(t *testing.T) {
	chosen := ""
	m := NewWithHooks(&scriptEngine{}, effortHooks("high", true, func(l string) { chosen = l }))
	m.SetColor(false)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	m = updated.(Model)
	m.SetModelName("deepseek-reasoner")
	m.SetEffort("high", true)

	// "medium" is a documented HAL level, but this model's family does
	// not take it.
	m = runCommand(m, "/effort medium")
	if chosen != "" {
		t.Errorf("a level the model does not take was applied: %q", chosen)
	}
	if !strings.Contains(m.Notice(), "This model takes") {
		t.Errorf("notice = %q", m.Notice())
	}
}

// Leaving the selector without choosing changes nothing.
func TestLeavingTheEffortSelectorChangesNothing(t *testing.T) {
	chosen := ""
	m := NewWithHooks(&scriptEngine{}, effortHooks("high", true, func(l string) { chosen = l }))
	m.SetColor(false)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	m = updated.(Model)
	m.SetModelName("MiniMax-M3.1-Flash")
	m.SetEffort("high", true)

	m = runCommand(m, "/effort")
	m = pressKey(m, tea.KeyDown, 0, "")
	m = pressKey(m, tea.KeyEscape, 0, "esc")
	if m.view != viewChat {
		t.Error("the selector stayed open")
	}
	if chosen != "" {
		t.Errorf("leaving the selector applied a level: %q", chosen)
	}
	if m.effort != "high" {
		t.Errorf("the level in effect changed to %q", m.effort)
	}
}

// effortModel returns a model with an effort-capable provider and the
// given model name.
func effortModel(modelID string) Model {
	m := NewWithHooks(&scriptEngine{}, effortHooks("high", true, func(string) {}))
	m.SetColor(false)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	m = updated.(Model)
	m.SetModelName(modelID)
	m.SetEffort("high", true)
	return m
}
