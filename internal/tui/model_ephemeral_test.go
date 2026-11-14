package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// A flag or runtime override rides along in the model list for
// visibility, but it names no persisted endpoint/model pair. The
// removal, edit, context and show pickers must offer stored pairs
// only; the Use picker and the quick picker keep the row visible but
// never act on it.

// ephemeralHooks lists two stored models and one flag-sourced phantom:
// MiniMax-M3.1-Flash shown under deepseek-flash, where it is not stored.
// The phantom carries the active mark when the override is the
// effective model, exactly as the real listing marks it.
func ephemeralHooks(switched *[2]string, ephemeralActive bool) Hooks {
	return Hooks{
		Endpoints: func() []EndpointInfo {
			return []EndpointInfo{
				{ID: "deepseek-flash", Name: "DeepSeek", Protocol: "openai-compatible",
					Models: []ModelInfo{{ID: "deepseek-flash", Endpoint: "deepseek-flash"}}},
				{ID: "minimax", Name: "MiniMax", Protocol: "openai-compatible",
					Models: []ModelInfo{{ID: "MiniMax-M3.1-Flash", Endpoint: "minimax"}}},
			}
		},
		ListModels: func() []ModelInfo {
			return []ModelInfo{
				{ID: "deepseek-flash", Label: "deepseek-flash", Endpoint: "deepseek-flash"},
				{ID: "MiniMax-M3.1-Flash", Label: "MiniMax-M3.1-Flash", Endpoint: "minimax"},
				{ID: "MiniMax-M3.1-Flash", Label: "MiniMax-M3.1-Flash (flags)", Endpoint: "deepseek-flash",
					Active: ephemeralActive, Ephemeral: true},
			}
		},
		SwitchModel: func(endpoint, id string) (Engine, error) {
			if switched != nil {
				*switched = [2]string{endpoint, id}
			}
			return nil, nil
		},
		DeleteModel: func(endpoint, id, replacement string) error {
			if switched != nil {
				*switched = [2]string{endpoint, id}
			}
			return nil
		},
	}
}

// The remove picker offers stored pairs only: the phantom flag entry is
// not selectable, so removal cannot target a pair storage does not hold.
func TestRemovePickerHidesFlagEntries(t *testing.T) {
	m := manageModel(t, ephemeralHooks(nil, true))
	m = runCommand(m, "/model remove")
	if m.manage.screen != managePick {
		t.Fatalf("the screen = %v, want the remove picker", m.manage.screen)
	}
	for _, item := range m.manage.items {
		if item.ephemeral {
			t.Errorf("an ephemeral row is offered for removal: %+v", item)
		}
	}
	if len(m.manage.items) != 2 {
		t.Fatalf("the picker holds %d rows, want the 2 stored pairs", len(m.manage.items))
	}
	if view := manageView(m); strings.Contains(view, "(flags)") {
		t.Errorf("the remove picker shows a flags entry:\n%s", view)
	}
}

// Removing the real pair deletes exactly that pair and leaves the
// other endpoint and the active model alone.
func TestRemoveRealPairUsesItsOwnEndpoint(t *testing.T) {
	var deleted [2]string
	hooks := ephemeralHooks(&deleted, true)
	m := manageModel(t, hooks)
	m = runCommand(m, "/model remove")
	// The second row is minimax's stored model.
	m = pressKey(m, tea.KeyDown, 0, "")
	m = pressKey(m, tea.KeyEnter, 0, "")
	if !strings.Contains(manageView(m), "Remove model minimax \u00b7 MiniMax-M3.1-Flash?") {
		t.Fatalf("the confirmation names the wrong pair:\n%s", manageView(m))
	}
	m = pressKey(m, tea.KeyEnter, 0, "")
	if deleted != [2]string{"minimax", "MiniMax-M3.1-Flash"} {
		t.Errorf("deleted = %v, want the minimax pair", deleted)
	}
	if !strings.Contains(m.Notice(), "Removed model MiniMax-M3.1-Flash from endpoint minimax") {
		t.Errorf("notice = %q", m.Notice())
	}
}

// Cancelling the removal changes nothing and removes nothing.
func TestCancelledRemovalChangesNothing(t *testing.T) {
	var deleted [2]string
	deleted = [2]string{"", ""}
	m := manageModel(t, ephemeralHooks(&deleted, true))
	m = runCommand(m, "/model remove")
	m = pressKey(m, tea.KeyDown, 0, "")
	m = pressKey(m, tea.KeyEnter, 0, "")
	m = pressKey(m, tea.KeyEscape, 0, "")
	if deleted != [2]string{"", ""} {
		t.Errorf("a cancelled removal deleted %v", deleted)
	}
}

// Choosing the flag entry in Use switches nothing: it is already the
// effective model, and there is no stored pair to switch to.
func TestUseOfFlagEntrySwitchesNothing(t *testing.T) {
	var switched [2]string
	m := manageModel(t, ephemeralHooks(&switched, true))
	m = runCommand(m, "/model use")
	// The flag entry stays visible as the effective model.
	found := false
	for i, item := range m.manage.items {
		if item.ephemeral {
			found = true
			for m.manage.cursor < i {
				m = pressKey(m, tea.KeyDown, 0, "")
			}
		}
	}
	if !found {
		t.Fatal("the Use picker hides the effective flag model")
	}
	m = pressKey(m, tea.KeyEnter, 0, "")
	if switched != [2]string{"", ""} {
		t.Errorf("choosing the flag entry switched to %v", switched)
	}
	if !strings.Contains(m.Notice(), "already in effect") {
		t.Errorf("notice = %q, want the already-effective explanation", m.Notice())
	}
	if m.view != viewChat {
		t.Errorf("the view = %v, want the conversation back", m.view)
	}
}

// An ephemeral row without the active mark is never labelled active:
// choosing it explains there is nothing stored to switch to.
func TestUseOfUnmarkedFlagEntryDoesNotClaimActive(t *testing.T) {
	var switched [2]string
	m := manageModel(t, ephemeralHooks(&switched, false))
	m = runCommand(m, "/model use")
	found := false
	for i, item := range m.manage.items {
		if item.ephemeral {
			found = true
			for m.manage.cursor < i {
				m = pressKey(m, tea.KeyDown, 0, "")
			}
		}
	}
	if !found {
		t.Fatal("the Use picker hides the flag entry")
	}
	m = pressKey(m, tea.KeyEnter, 0, "")
	if switched != [2]string{"", ""} {
		t.Errorf("choosing the flag entry switched to %v", switched)
	}
	if strings.Contains(m.Notice(), "already in effect") {
		t.Errorf("notice claims active without proof: %q", m.Notice())
	}
	if !strings.Contains(m.Notice(), "not a stored model") {
		t.Errorf("notice = %q, want the not-stored explanation", m.Notice())
	}
}

// The quick picker shows the flag entry but Enter on it is a no-op:
// no switch is sent for a pair storage does not hold.
func TestQuickPickerFlagEntryIsNoOp(t *testing.T) {
	var switched [2]string
	m := manageModel(t, ephemeralHooks(&switched, true))
	m.enterPicker()
	if m.view != viewPicker {
		t.Fatalf("the view = %v, want the picker", m.view)
	}
	// The cursor starts on the active row, which is the flag entry here.
	if got := m.pickerModels[m.pickerCursor]; !got.Ephemeral {
		t.Fatalf("the cursor is not on the flag entry: %+v", got)
	}
	updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated.(Model)
	if switched != [2]string{"", ""} {
		t.Errorf("the quick picker switched to %v", switched)
	}
	if !strings.Contains(m.pickerNotice, "already in effect") {
		t.Errorf("picker notice = %q", m.pickerNotice)
	}
	if m.view != viewPicker {
		t.Errorf("the view = %v, want to stay in the picker", m.view)
	}
}

// The quick picker does not claim an unmarked override is active either.
func TestQuickPickerUnmarkedFlagEntryIsNeutral(t *testing.T) {
	var switched [2]string
	m := manageModel(t, ephemeralHooks(&switched, false))
	m.enterPicker()
	// No row is marked active, so the cursor starts at the top; walk to
	// the flag entry at the end of the list.
	for i := 0; i < len(m.pickerModels)+1; i++ {
		if m.pickerCursor < len(m.pickerModels) && m.pickerModels[m.pickerCursor].Ephemeral {
			break
		}
		m = pressKey(m, tea.KeyDown, 0, "")
	}
	if m.pickerCursor >= len(m.pickerModels) || !m.pickerModels[m.pickerCursor].Ephemeral {
		t.Fatal("cannot reach the flag entry")
	}
	updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated.(Model)
	if switched != [2]string{"", ""} {
		t.Errorf("the quick picker switched to %v", switched)
	}
	if strings.Contains(m.pickerNotice, "already in effect") {
		t.Errorf("picker notice claims active without proof: %q", m.pickerNotice)
	}
	if !strings.Contains(m.pickerNotice, "not a stored model") {
		t.Errorf("picker notice = %q", m.pickerNotice)
	}
}

// The edit picker hides flag entries like the remove picker does.
func TestEditPickerHidesFlagEntries(t *testing.T) {
	m := manageModel(t, ephemeralHooks(nil, true))
	m = runCommand(m, "/model edit")
	for _, item := range m.manage.items {
		if item.ephemeral {
			t.Errorf("an ephemeral row is offered for editing: %+v", item)
		}
	}
	if len(m.manage.items) != 2 {
		t.Errorf("the picker holds %d rows, want the 2 stored pairs", len(m.manage.items))
	}
}

// Two endpoints may legitimately hold the same model id: each row keeps
// its own endpoint, and removing one leaves the other alone.
func TestSameIdUnderTwoEndpointsKeepsOwnership(t *testing.T) {
	var deleted [2]string
	hooks := Hooks{
		Endpoints: func() []EndpointInfo {
			return []EndpointInfo{
				{ID: "minimax", Name: "MiniMax", Models: []ModelInfo{{ID: "shared", Endpoint: "minimax"}}},
				{ID: "deepseek-flash", Name: "DeepSeek", Models: []ModelInfo{{ID: "shared", Endpoint: "deepseek-flash"}}},
			}
		},
		ListModels: func() []ModelInfo {
			return []ModelInfo{
				{ID: "shared", Label: "shared", Endpoint: "minimax"},
				{ID: "shared", Label: "shared", Endpoint: "deepseek-flash"},
			}
		},
		DeleteModel: func(endpoint, id, replacement string) error {
			deleted = [2]string{endpoint, id}
			return nil
		},
	}
	m := manageModel(t, hooks)
	m = runCommand(m, "/model remove")
	if len(m.manage.items) != 2 {
		t.Fatalf("the picker holds %d rows, want both stored pairs", len(m.manage.items))
	}
	// The second row is deepseek-flash's own model.
	m = pressKey(m, tea.KeyDown, 0, "")
	m = pressKey(m, tea.KeyEnter, 0, "")
	if !strings.Contains(manageView(m), "Remove model deepseek-flash \u00b7 shared?") {
		t.Fatalf("the confirmation names the wrong pair:\n%s", manageView(m))
	}
	m = pressKey(m, tea.KeyEnter, 0, "")
	if deleted != [2]string{"deepseek-flash", "shared"} {
		t.Errorf("deleted = %v, want exactly the deepseek-flash pair", deleted)
	}
}
