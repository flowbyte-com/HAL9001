package tui

import (
	"strings"
	"testing"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
)

// Synthetic secrets only. Values below are made up for tests and never
// belong to a real provider.
const (
	literalSecret      = "synthetic-literal-key-AAA111"
	literalReplacement = "synthetic-literal-key-BBB222"
)

// literalHooks builds management hooks for an endpoint editor with a
// captured save and test.
func literalHooks(detail EndpointDetail, saved *EndpointSpec, tested *EndpointSpec) Hooks {
	return Hooks{
		Endpoints: func() []EndpointInfo {
			return []EndpointInfo{
				{ID: "lit-ep", Name: "Lit EP", Protocol: "openai-compatible",
					BaseURL: "http://127.0.0.1:9/v1", CredentialEnv: detail.Settings.CredentialEnv},
				{ID: "other", Name: "Other", Protocol: "openai-compatible",
					BaseURL: "http://127.0.0.1:9/v1", CredentialEnv: "OTHER_VAR"},
			}
		},
		EndpointDetail: func(endpoint string) (EndpointDetail, error) {
			return detail, nil
		},
		CredentialEnvSet: func(name string) bool { return false },
		SaveEndpoint: func(s EndpointSpec) error {
			*saved = s
			return nil
		},
		TestEndpoint: func(s EndpointSpec) (string, error) {
			*tested = s
			return "reachable, 1 model", nil
		},
	}
}

// literalDetail is an endpoint record as the editor loads it.
func literalDetail(env string, hasLiteral bool) EndpointDetail {
	on := true
	return EndpointDetail{
		Settings: EndpointSpec{
			ID: "lit-ep", Name: "Lit EP", Protocol: "openai-compatible",
			BaseURL: "http://127.0.0.1:9/v1", CredentialEnv: env, Discovery: &on,
		},
		HasLiteralCredential: hasLiteral,
	}
}

// gotoChoice moves the cursor to the choice carrying value and picks it.
func gotoChoice(t *testing.T, m Model, value string) Model {
	t.Helper()
	for i := 0; i < len(m.manage.items)+1; i++ {
		if m.manage.cursor < len(m.manage.items) && m.manage.items[m.manage.cursor].value == value {
			return pressKey(m, tea.KeyEnter, 0, "")
		}
		m = pressKey(m, tea.KeyDown, 0, "")
	}
	t.Fatalf("no choice carries %q: %#v", value, m.manage.items)
	return m
}

// openLiteralForm walks from a fresh editor to the masked key form.
func openLiteralForm(t *testing.T, m Model) Model {
	t.Helper()
	m = pressKey(m, 'k', 0, "k")
	if m.manage.screen != managePick {
		t.Fatalf("the screen = %v, want the credential list", m.manage.screen)
	}
	return gotoChoice(t, m, manageEnterKey)
}

// TestALiteralKeyIsStagedAndSaved is Case A: the full entry flow.
func TestALiteralKeyIsStagedAndSaved(t *testing.T) {
	var saved, tested EndpointSpec
	m := manageModel(t, literalHooks(literalDetail("", false), &saved, &tested))
	m = runCommand(m, "/endpoint edit")
	m = pressKey(m, tea.KeyEnter, 0, "")
	m = openLiteralForm(t, m)
	if m.manage.screen != manageForm {
		t.Fatalf("the screen = %v, want the masked key form", m.manage.screen)
	}
	if got := m.manage.fields[0].input.EchoMode; got != textinput.EchoPassword {
		t.Fatalf("the key field is not masked: %v", got)
	}
	// Paste a synthetic key: it lands in the field and never on screen.
	updated, _ := m.Update(tea.PasteMsg{Content: literalSecret})
	m = updated.(Model)
	if got := m.manage.fields[0].input.Value(); got != literalSecret {
		t.Fatalf("the pasted key did not land in the field: %q", got)
	}
	if view := manageView(m); strings.Contains(view, literalSecret) {
		t.Fatalf("the masked form echoed the key:\n%s", view)
	}
	// Saving the form stages the key and returns to the editor.
	m = pressKey(m, 's', tea.ModCtrl, "")
	if m.manage.screen != manageMenu {
		t.Fatalf("the screen = %v, want the endpoint editor back", m.manage.screen)
	}
	if got := m.manage.endpointCredential; got != literalSecret {
		t.Fatalf("the staged credential was discarded: %q", got)
	}
	// The editor shows only an indicator, never the value.
	view := manageView(m)
	if strings.Contains(view, literalSecret) {
		t.Fatalf("the editor shows the key:\n%s", view)
	}
	if !strings.Contains(view, "save to store it") {
		t.Errorf("the editor shows no staged indicator:\n%s", view)
	}
	// Nothing is persisted before the endpoint Save.
	if saved.Credential != "" {
		t.Fatalf("the key was persisted before Save: %+v", saved)
	}
	// Explicit Test uses the newly staged key.
	m = pressKey(m, 't', 0, "t")
	if tested.Credential != literalSecret {
		t.Errorf("Test did not use the staged key: %+v", tested)
	}
	if tested.CredentialEnv != "" {
		t.Errorf("Test carried an env reference beside the key: %+v", tested)
	}
	if strings.Contains(m.manage.notice, literalSecret) {
		t.Errorf("the Test report shows the key: %q", m.manage.notice)
	}
	// Saving the endpoint stores the literal and clears the reference.
	m = pressKey(m, 's', 0, "s")
	if saved.Credential != literalSecret {
		t.Errorf("the saved credential = %q, want the staged key", saved.Credential)
	}
	if saved.CredentialEnv != "" {
		t.Errorf("the saved reference = %q, want it cleared", saved.CredentialEnv)
	}
	if saved.ClearCredential {
		t.Errorf("a literal save must not set the clear flag: %+v", saved)
	}
}

// TestCancellingTheKeyFormDiscardsTheKey is Case C, first path.
func TestCancellingTheKeyFormDiscardsTheKey(t *testing.T) {
	var saved, tested EndpointSpec
	m := manageModel(t, literalHooks(literalDetail("", false), &saved, &tested))
	m = runCommand(m, "/endpoint edit")
	m = pressKey(m, tea.KeyEnter, 0, "")
	m = openLiteralForm(t, m)
	updated, _ := m.Update(tea.PasteMsg{Content: literalSecret})
	m = updated.(Model)
	// Esc cancels the form instead of staging anything.
	m = pressKey(m, tea.KeyEscape, 0, "")
	if m.manage.screen != managePick {
		t.Fatalf("the screen = %v, want back at the credential list", m.manage.screen)
	}
	m = pressKey(m, tea.KeyEscape, 0, "")
	if m.manage.endpointCredential != "" {
		t.Errorf("a cancelled key stayed staged")
	}
	view := manageView(m)
	if strings.Contains(view, literalSecret) {
		t.Errorf("a cancelled key stayed on screen:\n%s", view)
	}
	for _, frame := range m.manageStack {
		for _, draft := range frame.draft {
			if strings.Contains(draft, literalSecret) {
				t.Errorf("a cancelled key stayed in a draft")
			}
		}
	}
	// Saving now stores no key at all.
	m = pressKey(m, 's', 0, "s")
	if saved.Credential != "" {
		t.Errorf("a cancelled key was persisted: %+v", saved)
	}
	if tested.Credential != "" {
		t.Errorf("a cancelled key reached Test: %+v", tested)
	}
}

// TestCancellingTheEditorDiscardsStagedChanges is Case C, second path.
func TestCancellingTheEditorDiscardsStagedChanges(t *testing.T) {
	hookCalls := 0
	var saved, tested EndpointSpec
	hooks := literalHooks(literalDetail("", false), &saved, &tested)
	hooks.SaveEndpoint = func(s EndpointSpec) error {
		hookCalls++
		saved = s
		return nil
	}
	m := manageModel(t, hooks)
	m = runCommand(m, "/endpoint edit")
	m = pressKey(m, tea.KeyEnter, 0, "")
	m = openLiteralForm(t, m)
	updated, _ := m.Update(tea.PasteMsg{Content: literalSecret})
	m = updated.(Model)
	m = pressKey(m, 's', tea.ModCtrl, "")
	if m.manage.endpointCredential == "" {
		t.Fatal("the key was not staged")
	}
	// Leaving the editor without saving writes nothing.
	m = pressKey(m, tea.KeyEscape, 0, "")
	m = pressKey(m, tea.KeyEscape, 0, "")
	m = pressKey(m, tea.KeyEscape, 0, "")
	if hookCalls != 0 {
		t.Fatalf("a cancelled edit was saved %d time(s): %+v", hookCalls, saved)
	}
}

// TestAFailedSaveReplacesNothing is Case C, third path.
func TestAFailedSaveReplacesNothing(t *testing.T) {
	var tested EndpointSpec
	saved := EndpointSpec{}
	hooks := literalHooks(literalDetail("OLD_VAR", false), &saved, &tested)
	hooks.SaveEndpoint = func(s EndpointSpec) error {
		return errOnboard("the endpoint cannot be used")
	}
	m := manageModel(t, hooks)
	m = runCommand(m, "/endpoint edit")
	m = pressKey(m, tea.KeyEnter, 0, "")
	m = openLiteralForm(t, m)
	updated, _ := m.Update(tea.PasteMsg{Content: literalSecret})
	m = updated.(Model)
	m = pressKey(m, 's', tea.ModCtrl, "")
	m = pressKey(m, 's', 0, "s")
	if !strings.Contains(m.manage.notice, "Cannot save") {
		t.Fatalf("the failure was not reported: %q", m.manage.notice)
	}
	if strings.Contains(m.manage.notice, literalSecret) {
		t.Errorf("the failure report shows the key: %q", m.manage.notice)
	}
	if m.manage.screen != manageMenu {
		t.Errorf("a refused save must keep the editor open")
	}
	if m.manage.endpointCredential != "" {
		t.Errorf("a failed save kept the staged key in memory")
	}
}

// TestEnvironmentToLiteralReplacesTheReference is Case D, first path.
func TestEnvironmentToLiteralReplacesTheReference(t *testing.T) {
	var saved, tested EndpointSpec
	m := manageModel(t, literalHooks(literalDetail("OLD_VAR", false), &saved, &tested))
	m = runCommand(m, "/endpoint edit")
	m = pressKey(m, tea.KeyEnter, 0, "")
	m = openLiteralForm(t, m)
	updated, _ := m.Update(tea.PasteMsg{Content: literalSecret})
	m = updated.(Model)
	m = pressKey(m, 's', tea.ModCtrl, "")
	m = pressKey(m, 's', 0, "s")
	if saved.Credential != literalSecret {
		t.Errorf("the saved credential = %q, want the new key", saved.Credential)
	}
	if saved.CredentialEnv != "" {
		t.Errorf("the old reference survived: %q", saved.CredentialEnv)
	}
}

// TestLiteralToEnvironmentClearsTheStoredKey is Case D, second path.
func TestLiteralToEnvironmentClearsTheStoredKey(t *testing.T) {
	var saved, tested EndpointSpec
	m := manageModel(t, literalHooks(literalDetail("", true), &saved, &tested))
	m = runCommand(m, "/endpoint edit")
	m = pressKey(m, tea.KeyEnter, 0, "")
	if view := manageView(m); !strings.Contains(view, "a stored key") {
		t.Fatalf("the editor must say a stored key exists:\n%s", view)
	}
	m = pressKey(m, 'k', 0, "k")
	m = gotoChoice(t, m, "OTHER_VAR")
	m = pressKey(m, 's', 0, "s")
	if saved.CredentialEnv != "OTHER_VAR" {
		t.Errorf("the saved reference = %q, want OTHER_VAR", saved.CredentialEnv)
	}
	if saved.Credential != "" {
		t.Errorf("the old literal survived beside the reference")
	}
}

// TestLiteralToReplacementKey is Case D, third path.
func TestLiteralToReplacementKey(t *testing.T) {
	var saved, tested EndpointSpec
	m := manageModel(t, literalHooks(literalDetail("", true), &saved, &tested))
	m = runCommand(m, "/endpoint edit")
	m = pressKey(m, tea.KeyEnter, 0, "")
	m = openLiteralForm(t, m)
	updated, _ := m.Update(tea.PasteMsg{Content: literalReplacement})
	m = updated.(Model)
	m = pressKey(m, 's', tea.ModCtrl, "")
	m = pressKey(m, 's', 0, "s")
	if saved.Credential != literalReplacement {
		t.Errorf("the saved credential was not replaced")
	}
}

// TestUnrelatedEditPreservesALiteral is Case D, fourth path.
func TestUnrelatedEditPreservesALiteral(t *testing.T) {
	var saved, tested EndpointSpec
	m := manageModel(t, literalHooks(literalDetail("", true), &saved, &tested))
	m = runCommand(m, "/endpoint edit")
	m = pressKey(m, tea.KeyEnter, 0, "")
	m = pressKey(m, 'n', 0, "n")
	m = typeText(m, "Renamed")
	m = pressKey(m, 's', tea.ModCtrl, "")
	m = pressKey(m, 's', 0, "s")
	if saved.Name != "Renamed" {
		t.Errorf("the name = %q", saved.Name)
	}
	if saved.Credential != "" || saved.CredentialEnv != "" || saved.ClearCredential {
		t.Errorf("an unrelated edit touched the credential: %+v", saved)
	}
}

// TestALiteralKeyNeverTravelsByCommand is a security guard: a literal
// key typed as key=value would stay in the conversation history, so the
// command refuses it and points at the masked field instead.
func TestALiteralKeyNeverTravelsByCommand(t *testing.T) {
	m := manageModel(t, literalHooks(literalDetail("", false), &EndpointSpec{}, &EndpointSpec{}))
	m = runCommand(m, "/endpoint edit lit-ep credential="+literalSecret)
	if !strings.Contains(m.manage.notice, "history") && !strings.Contains(m.notice, "history") {
		t.Errorf("the command must refuse a literal key: %q %q", m.manage.notice, m.notice)
	}
}

// errTestRefused is a hook failure for the failed-save test.
