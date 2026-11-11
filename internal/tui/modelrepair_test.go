package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// The repaired management workflow.
//
// The operator reported three defects: adding a model wanted an
// endpoint id where they had typed an address, editing a model showed
// too little and offered no way back to a default, and Esc left the
// whole interface instead of one step of it.
//
// These tests are about the repair: the endpoint is chosen from the
// endpoints HAL knows, the settings of a model are shown and changed
// one at a time, the defaults can be restored explicitly, and Esc walks
// back one level at a time.

// manageRepairHooks are the hooks of a configuration with two endpoints
// and two models.
func manageRepairHooks() Hooks {
	return Hooks{
		ListModels: func() []ModelInfo {
			return []ModelInfo{
				{ID: "flash", Label: "Flash", Endpoint: "primary", Active: true},
				{ID: "deepseek", Endpoint: "primary"},
			}
		},
		Endpoints: func() []EndpointInfo {
			return []EndpointInfo{
				{ID: "primary", Name: "Primary", Protocol: "openai-compatible",
					BaseURL: "https://api.primary.example/v1", Credential: "env:PRIMARY_KEY",
					CredentialEnv: "PRIMARY_KEY", Active: true,
					Models: []ModelInfo{{ID: "flash", Endpoint: "primary", Active: true}}},
				{ID: "secondary", Name: "Secondary", Protocol: "openai-compatible",
					BaseURL: "https://api.secondary.example/v1", Credential: "env:SECONDARY_KEY",
					CredentialEnv: "SECONDARY_KEY",
					Models:        []ModelInfo{{ID: "other", Endpoint: "secondary"}}},
			}
		},
		ModelDetail: func(endpoint, model string) (ModelDetail, error) {
			return ModelDetail{
				Settings: ModelSpec{ID: model, Label: "Flash", Effort: "medium",
					ContextMode: "manual", ContextWindow: 64000, MaxOutputTokens: 4096},
				Context:    []string{"  mode            manual"},
				Provenance: []string{"  source          discovered"},
			}, nil
		},
		EndpointDetail: func(endpoint string) (EndpointDetail, error) {
			on := true
			return EndpointDetail{
				Settings: EndpointSpec{ID: endpoint, Name: "Primary", Protocol: "openai-compatible",
					BaseURL: "https://api.primary.example/v1", CredentialEnv: "PRIMARY_KEY",
					Discovery: &on},
			}, nil
		},
		CredentialEnvSet: func(name string) bool {
			return name == "PRIMARY_KEY"
		},
		AddModel: func(string, ModelSpec) error {
			return nil
		},
		SaveEndpoint: func(EndpointSpec) error {
			return nil
		},
		TestEndpoint: func(EndpointSpec) (string, error) {
			return "reachable, 1 model", nil
		},
	}
}

// The endpoint is chosen from the endpoints HAL knows, and a URL is
// never mistaken for an endpoint id.
func TestTheAddScreenNeverAsksForAnEndpointId(t *testing.T) {
	m := manageModel(t, manageRepairHooks())
	m = runCommand(m, "/model add")
	if m.manage.screen != managePick {
		t.Fatalf("the screen = %v, want the endpoint list", m.manage.screen)
	}
	view := manageView(m)
	// The rows name the id and the address: they are different things.
	if !strings.Contains(view, "id primary") {
		t.Errorf("the list must name the endpoint id:\n%s", view)
	}
	if !strings.Contains(view, "https://api.primary.example/v1") {
		t.Errorf("the list must name the address:\n%s", view)
	}
	// There is no free-text endpoint field to type a URL into.
	if strings.Contains(view, "Endpoint ") {
		t.Errorf("the screen must not ask for an endpoint id:\n%s", view)
	}
}

// The command that names an endpoint explains that an address is not an
// endpoint id.
func TestTheAddCommandExplainsTheAddressAndTheId(t *testing.T) {
	m := manageModel(t, manageRepairHooks())
	m = runCommand(m, "/model add https://api.deepseek.com/v1 deepseek-chat")
	if !strings.Contains(m.notice, "API URL") {
		t.Errorf("the notice = %q", m.notice)
	}
	if !strings.Contains(m.notice, "primary") {
		t.Errorf("the notice must name the endpoints that exist: %q", m.notice)
	}
}

// The settings of a model are shown with their current values.
func TestTheEditScreenShowsTheCurrentValues(t *testing.T) {
	m := manageModel(t, manageRepairHooks())
	m = runCommand(m, "/model edit")
	m = pressKey(m, tea.KeyEnter, 0, "")
	view := manageView(m)
	for _, want := range []string{
		"Label", "Flash", "Effort", "medium", "Output limit", "4096 tokens",
		"Context", "manual", "identity", "never change",
	} {
		if !strings.Contains(view, want) {
			t.Errorf("the screen must show %q:\n%s", want, view)
		}
	}
}

// The effort is restored to the model's own default, and the restore is
// an explicit act.
func TestTheEffortCanBeRestoredToTheDefault(t *testing.T) {
	var spec ModelSpec
	hooks := manageRepairHooks()
	hooks.EditModel = func(_, _ string, s ModelSpec) error {
		spec = s
		return nil
	}
	m := manageModel(t, hooks)
	m = runCommand(m, "/model edit")
	m = pressKey(m, tea.KeyEnter, 0, "")
	// The effort is opened and the model's own default is chosen.
	m = pressKey(m, 'e', 0, "e")
	if m.manage.screen != managePick {
		t.Fatalf("the screen = %v, want the effort list", m.manage.screen)
	}
	view := manageView(m)
	if !strings.Contains(view, "the model's own default") {
		t.Errorf("the list must offer the default:\n%s", view)
	}
	for _, level := range []string{"low", "medium", "high", "xhigh", "max"} {
		if !strings.Contains(view, level) {
			t.Errorf("the list must offer %q:\n%s", level, view)
		}
	}
	m = pressKey(m, tea.KeyEnter, 0, "")
	m = pressKey(m, 's', 0, "s")
	if !spec.ResetEffort {
		t.Errorf("the spec = %+v, want the effort restored", spec)
	}
	if spec.Effort != "" {
		t.Errorf("the effort = %q, want none", spec.Effort)
	}
}

// The output limit is restored to the provider's own default.
func TestTheOutputLimitCanBeRestoredToTheDefault(t *testing.T) {
	var spec ModelSpec
	hooks := manageRepairHooks()
	hooks.EditModel = func(_, _ string, s ModelSpec) error {
		spec = s
		return nil
	}
	m := manageModel(t, hooks)
	m = runCommand(m, "/model edit")
	m = pressKey(m, tea.KeyEnter, 0, "")
	m = pressKey(m, 'o', 0, "o")
	m = pressKey(m, 'p', 0, "p")
	m = pressKey(m, 's', 0, "s")
	if !spec.ResetOutputTokens {
		t.Errorf("the spec = %+v, want the output limit restored", spec)
	}
	if spec.MaxOutputTokens != 0 {
		t.Errorf("the output limit = %d, want none", spec.MaxOutputTokens)
	}
}

// An empty field is not a reset: the settings that were not touched
// keep their values.
func TestAnEmptyFieldIsNotAReset(t *testing.T) {
	var spec ModelSpec
	hooks := manageRepairHooks()
	hooks.EditModel = func(_, _ string, s ModelSpec) error {
		spec = s
		return nil
	}
	m := manageModel(t, hooks)
	m = runCommand(m, "/model edit")
	m = pressKey(m, tea.KeyEnter, 0, "")
	// The effort form is opened and left empty.
	m = pressKey(m, 'e', 0, "e")
	m = pressKey(m, tea.KeyEscape, 0, "")
	m = pressKey(m, 's', 0, "s")
	if spec.ResetEffort || spec.Effort != "" {
		t.Errorf("the spec = %+v, want the effort untouched", spec)
	}
	if spec.ResetOutputTokens || spec.MaxOutputTokens != 0 {
		t.Errorf("the spec = %+v, want the output limit untouched", spec)
	}
}

// Esc walks back one level at a time, and the top of the hierarchy is
// the conversation.
func TestEscWalksBackOneLevelAtATime(t *testing.T) {
	m := manageModel(t, manageRepairHooks())
	m = runCommand(m, "/model edit")
	if m.manage.screen != managePick {
		t.Fatalf("the screen = %v, want the model list", m.manage.screen)
	}
	// The list returns to the menu it came from.
	m = pressKey(m, tea.KeyEscape, 0, "")
	if m.manage.screen != manageMenu {
		t.Fatalf("the screen = %v, want the menu", m.manage.screen)
	}
	if m.view != viewManage {
		t.Fatalf("the view = %v, want the management interface", m.view)
	}
	// The menu returns to the conversation.
	m = pressKey(m, tea.KeyEscape, 0, "")
	if m.view != viewChat {
		t.Errorf("the view = %v, want the conversation", m.view)
	}
}

// A form is left without being committed, and what was typed in it is
// still there when the operator comes back.
func TestALeavingFormNeverCommitsAndKeepsItsInput(t *testing.T) {
	called := false
	hooks := manageRepairHooks()
	hooks.AddModel = func(string, ModelSpec) error {
		called = true
		return nil
	}
	m := manageModel(t, hooks)
	m = runCommand(m, "/model add")
	m = pressKey(m, tea.KeyEnter, 0, "")
	if m.manage.screen != manageForm {
		t.Fatalf("the screen = %v, want the model form", m.manage.screen)
	}
	m = typeText(m, "deepseek-chat")
	// The form is left without being saved.
	m = pressKey(m, tea.KeyEscape, 0, "")
	if called {
		t.Fatal("the model was added although the form was left")
	}
	if m.manage.screen != managePick {
		t.Fatalf("the screen = %v, want the endpoint list", m.manage.screen)
	}
	// The endpoint is chosen again and the model id is still there.
	m = pressKey(m, tea.KeyEnter, 0, "")
	if m.manage.screen != manageForm {
		t.Fatalf("the screen = %v, want the model form", m.manage.screen)
	}
	if got := m.manageValues()[0]; got != "deepseek-chat" {
		t.Errorf("the model id = %q, want the typed value", got)
	}
}

// A confirmation returns to the list it came from when it is cancelled.
func TestACancelledConfirmationReturnsToItsParent(t *testing.T) {
	called := false
	hooks := manageRepairHooks()
	hooks.DeleteModel = func(string, string, string) error {
		called = true
		return nil
	}
	m := manageModel(t, hooks)
	m = runCommand(m, "/model remove")
	m = pressKey(m, tea.KeyEnter, 0, "")
	if m.manage.screen != manageConfirm {
		t.Fatalf("the screen = %v, want the confirmation", m.manage.screen)
	}
	m = pressKey(m, tea.KeyEscape, 0, "")
	if called {
		t.Fatal("the model was removed although the operator cancelled")
	}
	if m.manage.screen != managePick {
		t.Errorf("the screen = %v, want the model list", m.manage.screen)
	}
}

// The operator can add an endpoint from inside Add Model and come back
// to the task they started.
func TestAnEndpointCanBeCreatedFromInsideAddModel(t *testing.T) {
	var saved EndpointSpec
	var added string
	hooks := manageRepairHooks()
	// The endpoint list grows with the endpoints that are stored, the
	// way the configuration does.
	endpoints := hooks.Endpoints
	hooks.Endpoints = func() []EndpointInfo {
		list := endpoints()
		if saved.ID == "" {
			return list
		}
		for _, e := range list {
			if e.ID == saved.ID {
				return list
			}
		}
		return append(list, EndpointInfo{ID: saved.ID, Name: saved.Name, Protocol: saved.Protocol})
	}
	hooks.SaveEndpoint = func(s EndpointSpec) error {
		saved = s
		return nil
	}
	hooks.AddModel = func(endpoint string, spec ModelSpec) error {
		added = endpoint + "/" + spec.ID
		return nil
	}
	m := manageModel(t, hooks)
	m = runCommand(m, "/model add")
	// The last row of the list creates an endpoint.
	m = pressKey(m, tea.KeyDown, 0, "")
	m = pressKey(m, tea.KeyDown, 0, "")
	m = pressKey(m, tea.KeyEnter, 0, "")
	if m.manage.screen != manageMenu {
		t.Fatalf("the screen = %v, want the endpoint screen", m.manage.screen)
	}
	// The endpoint is described and saved.
	m = pressKey(m, 'i', 0, "i")
	m = typeText(m, "deepseek")
	m = pressKey(m, 's', tea.ModCtrl, "")
	m = pressKey(m, 'n', 0, "n")
	m = typeText(m, "DeepSeek")
	m = pressKey(m, 's', tea.ModCtrl, "")
	m = pressKey(m, 'u', 0, "u")
	m = typeText(m, "https://api.deepseek.com/v1")
	m = pressKey(m, 's', tea.ModCtrl, "")
	m = pressKey(m, 's', 0, "s")
	if saved.ID != "deepseek" {
		t.Fatalf("the endpoint = %+v", saved)
	}
	// The operator is back in Add Model, with the new endpoint chosen.
	if m.manage.screen != manageForm {
		t.Fatalf("the screen = %v, want the model form", m.manage.screen)
	}
	m = typeText(m, "deepseek-chat")
	m = pressKey(m, 's', tea.ModCtrl, "")
	if added != "deepseek/deepseek-chat" {
		t.Errorf("the model was added as %q", added)
	}
}

// The credential of an endpoint is a reference, and the screens say
// whether the variable holds a value without ever showing it.
func TestTheCredentialIsAReferenceAndItsAvailabilityIsChecked(t *testing.T) {
	m := manageModel(t, manageRepairHooks())
	m = runCommand(m, "/endpoint edit")
	m = pressKey(m, tea.KeyEnter, 0, "")
	view := manageView(m)
	if !strings.Contains(view, "Credential source") {
		t.Errorf("the screen must name the credential source:\n%s", view)
	}
	if !strings.Contains(view, "PRIMARY_KEY is set in this environment") {
		t.Errorf("the screen must say whether the variable is set:\n%s", view)
	}
	// The list of references names the ones that exist and the ones the
	// endpoint's own name suggests.
	m = pressKey(m, 'k', 0, "k")
	view = manageView(m)
	if !strings.Contains(view, "SECONDARY_KEY") {
		t.Errorf("the screen must offer the references that exist:\n%s", view)
	}
	if !strings.Contains(view, "already used by endpoint secondary") {
		t.Errorf("the screen must say who uses it:\n%s", view)
	}
	// The value of the variable is nowhere on the screen.
	for _, secret := range []string{"sk-", "secret", "test-key"} {
		if strings.Contains(view, secret) {
			t.Errorf("the screen must never show a credential: %q\n%s", secret, view)
		}
	}
}

// The credential of a new endpoint may point at a variable another
// endpoint already uses, and the choice is explicit.
func TestTheCredentialCanPointAtAnExistingReference(t *testing.T) {
	var saved EndpointSpec
	hooks := manageRepairHooks()
	hooks.SaveEndpoint = func(s EndpointSpec) error {
		saved = s
		return nil
	}
	m := manageModel(t, hooks)
	m = runCommand(m, "/endpoint add")
	// The endpoint has an id and an address before it is stored.
	m = pressKey(m, 'i', 0, "i")
	m = typeText(m, "third")
	m = pressKey(m, 's', tea.ModCtrl, "")
	m = pressKey(m, 'u', 0, "u")
	m = typeText(m, "https://api.third.example/v1")
	m = pressKey(m, 's', tea.ModCtrl, "")
	// The credential is pointed at the variable another endpoint uses.
	m = pressKey(m, 'k', 0, "k")
	if m.manage.screen != managePick {
		t.Fatalf("the screen = %v, want the credential list", m.manage.screen)
	}
	view := manageView(m)
	if !strings.Contains(view, "SECONDARY_KEY") {
		t.Errorf("the list must offer the references that exist:\n%s", view)
	}
	if !strings.Contains(view, "already used by endpoint secondary") {
		t.Errorf("the list must say who uses it:\n%s", view)
	}
	m = pressKey(m, tea.KeyDown, 0, "")
	m = pressKey(m, tea.KeyEnter, 0, "")
	m = pressKey(m, 's', 0, "s")
	if saved.CredentialEnv != "SECONDARY_KEY" {
		t.Errorf("the credential source = %q", saved.CredentialEnv)
	}
	// The key itself is never stored.
	if saved.Credential != "" {
		t.Errorf("the credential = %q, want none", saved.Credential)
	}
}

// Editing an endpoint keeps its authentication unless the operator
// names another one.
func TestEditingAnEndpointKeepsItsCredential(t *testing.T) {
	var saved EndpointSpec
	hooks := manageRepairHooks()
	hooks.SaveEndpoint = func(s EndpointSpec) error {
		saved = s
		return nil
	}
	m := manageModel(t, hooks)
	m = runCommand(m, "/endpoint edit")
	m = pressKey(m, tea.KeyEnter, 0, "")
	// The name is changed and nothing else.
	m = pressKey(m, 'n', 0, "n")
	m = typeText(m, "Renamed")
	m = pressKey(m, 's', tea.ModCtrl, "")
	m = pressKey(m, 's', 0, "s")
	if saved.Name != "Renamed" {
		t.Errorf("the name = %q", saved.Name)
	}
	if saved.CredentialEnv != "" {
		t.Errorf("the credential source = %q, want the endpoint to keep its own", saved.CredentialEnv)
	}
}

// The endpoint is tested only when the operator asks for it.
func TestTheConnectionTestHappensOnlyWhenTheOperatorAsks(t *testing.T) {
	tested := 0
	hooks := manageRepairHooks()
	hooks.TestEndpoint = func(EndpointSpec) (string, error) {
		tested++
		return "reachable, 1 model", nil
	}
	m := manageModel(t, hooks)
	m = runCommand(m, "/endpoint add")
	if tested != 0 {
		t.Fatalf("the endpoint was tested %d time(s) without an explicit action", tested)
	}
	// The endpoint needs an address before it can be asked anything.
	m = pressKey(m, 'u', 0, "u")
	m = typeText(m, "https://api.third.example/v1")
	m = pressKey(m, 's', tea.ModCtrl, "")
	m = pressKey(m, 't', 0, "t")
	if tested != 1 {
		t.Fatalf("the endpoint was tested %d time(s), want once", tested)
	}
	if !strings.Contains(m.manage.notice, "reachable") {
		t.Errorf("the notice = %q", m.manage.notice)
	}
}

// The management screens never change the active model.
func TestTheManagementWorkflowNeverChangesTheActiveModel(t *testing.T) {
	switched := false
	hooks := manageRepairHooks()
	hooks.SwitchModel = func(string, string) (Engine, error) {
		switched = true
		return nil, nil
	}
	m := manageModel(t, hooks)
	for _, command := range []string{"/model", "/model add", "/model edit",
		"/model remove", "/model context", "/model show", "/model discover",
		"/endpoint", "/endpoint add", "/endpoint edit", "/endpoint remove"} {
		m = runCommand(m, command)
		if switched {
			t.Fatalf("%q changed the active model", command)
		}
		// The screens are walked through and left again.
		for i := 0; i < 4; i++ {
			m = pressKey(m, tea.KeyEscape, 0, "")
		}
		if switched {
			t.Fatalf("%q changed the active model", command)
		}
	}
}
