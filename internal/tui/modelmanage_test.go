package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// The interactive management of endpoints and models.
//
// The commands with all their arguments work as they always did. These
// tests are about the screens: the operator asks for an action without
// spelling it out, and the interface asks for what the action needs.

func manageHooks() Hooks {
	return Hooks{
		ListModels: func() []ModelInfo {
			return []ModelInfo{
				{ID: "flash", Label: "Flash", Endpoint: "primary", Active: true},
				{ID: "deepseek", Endpoint: "primary"},
			}
		},
		Endpoints: func() []EndpointInfo {
			return []EndpointInfo{
				{ID: "primary", Name: "Primary", Protocol: "openai-compatible", Active: true,
					Models: []ModelInfo{{ID: "flash", Endpoint: "primary", Active: true}}},
				{ID: "secondary", Name: "Secondary", Protocol: "openai-compatible",
					Models: []ModelInfo{{ID: "other", Endpoint: "secondary"}}},
			}
		},
		ModelDetail: func(endpoint, model string) (ModelDetail, error) {
			return ModelDetail{
				Settings: ModelSpec{ID: model, Label: "Flash", Effort: "medium", ContextMode: "auto"},
				Context:  []string{"  mode            auto"},
			}, nil
		},
		EndpointDetail: func(endpoint string) (EndpointDetail, error) {
			on := true
			return EndpointDetail{
				Settings: EndpointSpec{ID: endpoint, Name: "Primary", Protocol: "openai-compatible",
					BaseURL: "https://api.example.com/v1", Discovery: &on},
			}, nil
		},
	}
}

// manageModel builds an interface with the management hooks and a size.
func manageModel(t *testing.T, hooks Hooks) Model {
	t.Helper()
	m := NewWithHooks(&scriptEngine{}, hooks)
	m.SetColor(false)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	return updated.(Model)
}

// manageView is the screen the interface shows.
func manageView(m Model) string {
	return string(m.View().Content)
}

func TestTheManagementInterfaceListsEveryAction(t *testing.T) {
	m := manageModel(t, manageHooks())
	m = runCommand(m, "/model")
	if m.view != viewManage {
		t.Fatalf("the view = %v, want the management interface", m.view)
	}
	view := manageView(m)
	for _, want := range []string{"Use", "Add", "Edit", "Remove", "Context",
		"Show", "Discover", "List"} {
		if !strings.Contains(view, want) {
			t.Errorf("the interface must offer %q:\n%s", want, view)
		}
	}
	// The command that does the same thing is named, so the operator can
	// always type it instead.
	if !strings.Contains(view, "/model edit") {
		t.Errorf("the interface must name the commands:\n%s", view)
	}
}

func TestTheEndpointInterfaceUsesTheSameConventions(t *testing.T) {
	m := manageModel(t, manageHooks())
	m = runCommand(m, "/endpoint")
	if m.view != viewManage {
		t.Fatalf("the view = %v, want the management interface", m.view)
	}
	view := manageView(m)
	for _, want := range []string{"List", "Add", "Edit", "Remove", "/endpoint edit"} {
		if !strings.Contains(view, want) {
			t.Errorf("the interface must offer %q:\n%s", want, view)
		}
	}
}

func TestAnUnknownActionOpensTheInterfaceAndSaysWhatIsWrong(t *testing.T) {
	m := manageModel(t, manageHooks())
	m = runCommand(m, "/model frobnicate")
	if m.view != viewManage {
		t.Fatalf("the view = %v, want the management interface", m.view)
	}
	if !strings.Contains(m.manage.notice, "frobnicate") {
		t.Errorf("the notice = %q", m.manage.notice)
	}
}

func TestAHalfSpelledCommandSaysWhatIsMissing(t *testing.T) {
	m := manageModel(t, manageHooks())
	m = runCommand(m, "/model edit primary")
	if m.view != viewManage {
		t.Fatalf("the view = %v, want the management interface", m.view)
	}
	if !strings.Contains(m.manage.notice, "model id") {
		t.Errorf("the notice = %q", m.manage.notice)
	}
	if !strings.Contains(m.manage.notice, "/model edit") {
		t.Errorf("the notice must name the command: %q", m.manage.notice)
	}
}

func TestTheEditScreenAsksForTheModelAndThenForItsSettings(t *testing.T) {
	var edited string
	var spec ModelSpec
	hooks := manageHooks()
	hooks.EditModel = func(endpoint, model string, s ModelSpec) error {
		edited = endpoint + "/" + model
		spec = s
		return nil
	}
	m := manageModel(t, hooks)
	m = runCommand(m, "/model edit")
	if m.manage.screen != managePick {
		t.Fatalf("the screen = %v, want the model list", m.manage.screen)
	}
	if !strings.Contains(manageView(m), "deepseek") {
		t.Errorf("the model list must name the models:\n%s", manageView(m))
	}
	// The second model is chosen.
	m = pressKey(m, tea.KeyDown, 0, "")
	m = pressKey(m, tea.KeyEnter, 0, "")
	if m.manage.screen != manageMenu {
		t.Fatalf("the screen = %v, want the settings menu", m.manage.screen)
	}
	view := manageView(m)
	if !strings.Contains(view, "Edit model primary · deepseek") {
		t.Errorf("the screen must name the model:\n%s", view)
	}
	// The current values are shown next to the settings they belong to.
	for _, want := range []string{"Label", "Effort", "Output limit", "Context", "Flash", "medium"} {
		if !strings.Contains(view, want) {
			t.Errorf("the screen must show %q:\n%s", want, view)
		}
	}
	// The label is changed and the changes are saved.
	m = pressKey(m, 'l', 0, "l")
	if m.manage.screen != manageForm {
		t.Fatalf("the screen = %v, want the label form", m.manage.screen)
	}
	m = typeText(m, "DeepSeek EU")
	m = pressKey(m, 's', tea.ModCtrl, "")
	if edited != "" {
		t.Fatal("the label was written before the operator saved the changes")
	}
	m = pressKey(m, 's', 0, "s")
	if edited != "primary/deepseek" {
		t.Fatalf("the hook was called with %q", edited)
	}
	if spec.Label != "DeepSeek EU" {
		t.Errorf("the label = %q", spec.Label)
	}
	// Only the setting that changed is sent: the effort and the output
	// limit keep their values.
	if spec.Effort != "" || spec.MaxOutputTokens != 0 {
		t.Errorf("the spec = %+v, want only the label", spec)
	}
	if m.view != viewChat {
		t.Errorf("the view = %v, want the conversation", m.view)
	}
	if !strings.Contains(m.notice, "updated") {
		t.Errorf("the notice = %q", m.notice)
	}
}

// A settings screen that is left without saving writes nothing.
func TestACancelledEditChangesNothing(t *testing.T) {
	called := false
	hooks := manageHooks()
	hooks.EditModel = func(string, string, ModelSpec) error {
		called = true
		return nil
	}
	m := manageModel(t, hooks)
	m = runCommand(m, "/model edit")
	m = pressKey(m, tea.KeyEnter, 0, "")
	m = pressKey(m, 'l', 0, "l")
	m = typeText(m, "never saved")
	m = pressKey(m, tea.KeyEscape, 0, "")
	if called {
		t.Error("a cancelled edit changed the configuration")
	}
	if m.manage.screen == manageForm {
		t.Error("the cancelled form stayed open")
	}
}

// A change the configuration refuses keeps the screen open and says why.
func TestARefusedEditKeepsTheSettingsOpen(t *testing.T) {
	hooks := manageHooks()
	hooks.EditModel = func(string, string, ModelSpec) error {
		return errOnboard("model \"deepseek\" has an unknown effort level \"turbo\"")
	}
	m := manageModel(t, hooks)
	m = runCommand(m, "/model edit")
	m = pressKey(m, tea.KeyDown, 0, "")
	m = pressKey(m, tea.KeyEnter, 0, "")
	m = pressKey(m, 'l', 0, "l")
	m = typeText(m, "kept")
	m = pressKey(m, 's', tea.ModCtrl, "")
	m = pressKey(m, 's', 0, "s")
	if m.view != viewManage {
		t.Fatalf("the view = %v, want the management interface", m.view)
	}
	if !strings.Contains(m.manage.notice, "unknown effort level") {
		t.Errorf("the notice = %q", m.manage.notice)
	}
}

func TestTheContextScreenShowsTheSettingsAndWhereTheyCameFrom(t *testing.T) {
	var spec ModelSpec
	hooks := manageHooks()
	hooks.EditModel = func(_, _ string, s ModelSpec) error {
		spec = s
		return nil
	}
	m := manageModel(t, hooks)
	m = runCommand(m, "/model context")
	m = pressKey(m, tea.KeyEnter, 0, "")
	view := manageView(m)
	if !strings.Contains(view, "mode            auto") {
		t.Errorf("the screen must show the context settings:\n%s", view)
	}
	// The mode is changed to manual.
	m = typeText(m, "manual")
	m = pressKey(m, 's', tea.ModCtrl, "")
	if spec.ContextMode != "manual" {
		t.Errorf("the mode = %q", spec.ContextMode)
	}
}

// A window belongs to the manual mode: the two are set together, so the
// record never contradicts itself.
func TestATypedWindowMakesTheModeManual(t *testing.T) {
	var spec ModelSpec
	hooks := manageHooks()
	hooks.EditModel = func(_, _ string, s ModelSpec) error {
		spec = s
		return nil
	}
	m := manageModel(t, hooks)
	m = runCommand(m, "/model context")
	m = pressKey(m, tea.KeyEnter, 0, "")
	// The mode is left as it is, and a window is typed.
	m = pressKey(m, tea.KeyTab, 0, "")
	m = typeText(m, "120000")
	m = pressKey(m, 's', tea.ModCtrl, "")
	if spec.ContextMode != "manual" {
		t.Errorf("the mode = %q, want manual", spec.ContextMode)
	}
	if spec.ContextWindow != 120000 {
		t.Errorf("the window = %d", spec.ContextWindow)
	}
}

// A limit of zero is the provider's own default, and the configuration
// keeps a zero unnamed: the screen says so instead of reporting a
// change that did not happen.
func TestTheOutputLimitRefusesZero(t *testing.T) {
	called := false
	hooks := manageHooks()
	hooks.EditModel = func(string, string, ModelSpec) error {
		called = true
		return nil
	}
	m := manageModel(t, hooks)
	m = runCommand(m, "/model edit")
	m = pressKey(m, tea.KeyDown, 0, "")
	m = pressKey(m, tea.KeyEnter, 0, "")
	m = pressKey(m, 'o', 0, "o")
	if m.manage.screen != manageMenu {
		t.Fatalf("the screen = %v, want the output menu", m.manage.screen)
	}
	m = pressKey(m, 't', 0, "t")
	if m.manage.screen != manageForm {
		t.Fatalf("the screen = %v, want the number form", m.manage.screen)
	}
	m = typeText(m, "0")
	m = pressKey(m, 's', tea.ModCtrl, "")
	if called {
		t.Error("the change was stored although the limit cannot be zero")
	}
	if m.manage.screen != manageForm {
		t.Fatalf("the screen = %v, want the form to stay open", m.manage.screen)
	}
	if !strings.Contains(m.manage.notice, "provider's own default") {
		t.Errorf("the notice = %q", m.manage.notice)
	}
}

func TestTheShowScreenDisplaysTheModelDetails(t *testing.T) {
	hooks := manageHooks()
	hooks.ModelDetails = func(endpoint, model string) ([]string, error) {
		return []string{"  id              " + model}, nil
	}
	m := manageModel(t, hooks)
	m = runCommand(m, "/model show")
	m = pressKey(m, tea.KeyEnter, 0, "")
	if m.view != viewModels {
		t.Fatalf("the view = %v, want the details panel", m.view)
	}
	if !strings.Contains(manageView(m), "id              flash") {
		t.Errorf("the panel must show the details:\n%s", manageView(m))
	}
}

func TestTheRemoveScreenAsksBeforeItRemoves(t *testing.T) {
	var endpoint, model, replacement string
	called := false
	hooks := manageHooks()
	hooks.DeleteModel = func(e, m, r string) error {
		endpoint, model, replacement = e, m, r
		called = true
		return nil
	}
	m := manageModel(t, hooks)
	m = runCommand(m, "/model remove")
	m = pressKey(m, tea.KeyDown, 0, "")
	m = pressKey(m, tea.KeyEnter, 0, "")
	if m.manage.screen != manageConfirm {
		t.Fatalf("the screen = %v, want the confirmation", m.manage.screen)
	}
	if called {
		t.Fatal("the model was removed before the operator confirmed")
	}
	// Esc keeps the model.
	m = pressKey(m, tea.KeyEscape, 0, "")
	if called {
		t.Fatal("the model was removed although the operator cancelled")
	}
	// The confirmation removes it.
	m = runCommand(m, "/model remove")
	m = pressKey(m, tea.KeyDown, 0, "")
	m = pressKey(m, tea.KeyEnter, 0, "")
	m = pressKey(m, tea.KeyEnter, 0, "")
	if !called {
		t.Fatal("the model was not removed")
	}
	if endpoint != "primary" || model != "deepseek" {
		t.Errorf("the hook was called with (%q, %q)", endpoint, model)
	}
	if replacement != "" {
		t.Errorf("the replacement = %q, want none for a model that is not active", replacement)
	}
}

func TestRemovingTheActiveModelAsksForTheReplacement(t *testing.T) {
	var replacement string
	hooks := manageHooks()
	hooks.DeleteModel = func(_, _, r string) error {
		replacement = r
		return nil
	}
	m := manageModel(t, hooks)
	m = runCommand(m, "/model remove")
	m = pressKey(m, tea.KeyEnter, 0, "")
	if m.manage.screen != manageConfirm {
		t.Fatalf("the screen = %v, want the confirmation", m.manage.screen)
	}
	if !strings.Contains(m.manage.message, "active model") {
		t.Errorf("the message = %q", m.manage.message)
	}
	// The confirmation asks for the model that takes its place.
	m = pressKey(m, tea.KeyEnter, 0, "")
	if m.manage.screen != managePick {
		t.Fatalf("the screen = %v, want the replacement list", m.manage.screen)
	}
	m = pressKey(m, tea.KeyDown, 0, "")
	m = pressKey(m, tea.KeyEnter, 0, "")
	if replacement == "" {
		t.Error("the removal did not name a replacement")
	}
}

// The endpoint is chosen from the endpoints HAL knows: a URL is never
// typed where an endpoint id belongs.
func TestTheAddScreenChoosesTheEndpointBeforeTheModel(t *testing.T) {
	var endpoint, id string
	hooks := manageHooks()
	hooks.AddModel = func(e string, spec ModelSpec) error {
		endpoint, id = e, spec.ID
		return nil
	}
	m := manageModel(t, hooks)
	m = runCommand(m, "/model add")
	if m.manage.screen != managePick {
		t.Fatalf("the screen = %v, want the endpoint list", m.manage.screen)
	}
	view := manageView(m)
	for _, want := range []string{"primary", "secondary", "Create an endpoint"} {
		if !strings.Contains(view, want) {
			t.Errorf("the screen must offer %q:\n%s", want, view)
		}
	}
	// The endpoint is chosen and the model form opens.
	m = pressKey(m, tea.KeyEnter, 0, "")
	if m.manage.screen != manageForm {
		t.Fatalf("the screen = %v, want the model form", m.manage.screen)
	}
	view = manageView(m)
	for _, want := range []string{"Model id", "Label", "endpoint      primary"} {
		if !strings.Contains(view, want) {
			t.Errorf("the form must show %q:\n%s", want, view)
		}
	}
	// The model id is typed and the form is saved.
	m = typeText(m, "new-model")
	m = pressKey(m, 's', tea.ModCtrl, "")
	if endpoint != "primary" || id != "new-model" {
		t.Errorf("the hook was called with (%q, %q)", endpoint, id)
	}
}

// The endpoint screens ask for what they need, and keep the credential
// a reference and never the key itself.
func TestTheEndpointScreensAskForWhatTheyNeed(t *testing.T) {
	var spec EndpointSpec
	called := false
	hooks := manageHooks()
	hooks.SaveEndpoint = func(s EndpointSpec) error {
		spec = s
		called = true
		return nil
	}
	m := manageModel(t, hooks)
	m = runCommand(m, "/endpoint add")
	if m.manage.screen != manageMenu {
		t.Fatalf("the screen = %v, want the endpoint menu", m.manage.screen)
	}
	view := manageView(m)
	for _, want := range []string{"Id", "Name", "Protocol", "Base url", "Credential source"} {
		if !strings.Contains(view, want) {
			t.Errorf("the screen must ask for %q:\n%s", want, view)
		}
	}
	// The id is typed.
	m = pressKey(m, 'i', 0, "i")
	if m.manage.screen != manageForm {
		t.Fatalf("the screen = %v, want the id form", m.manage.screen)
	}
	m = typeText(m, "second")
	m = pressKey(m, 's', tea.ModCtrl, "")
	// The name is typed.
	m = pressKey(m, 'n', 0, "n")
	m = typeText(m, "Second")
	m = pressKey(m, 's', tea.ModCtrl, "")
	// The address is typed.
	m = pressKey(m, 'u', 0, "u")
	m = typeText(m, "https://api.example.com/v1")
	m = pressKey(m, 's', tea.ModCtrl, "")
	// The endpoint is saved.
	m = pressKey(m, 's', 0, "s")
	if !called {
		t.Fatal("the endpoint was not stored")
	}
	if spec.ID != "second" || spec.Name != "Second" {
		t.Errorf("the spec = %+v", spec)
	}
	if spec.BaseURL != "https://api.example.com/v1" {
		t.Errorf("the base url = %q", spec.BaseURL)
	}
	// The credential itself is never a field: only the variable that
	// holds it.
	if spec.Credential != "" {
		t.Errorf("the credential = %q, want none", spec.Credential)
	}
}

func TestTheEndpointEditScreenKeepsTheSettingsItWasNotAskedToChange(t *testing.T) {
	var spec EndpointSpec
	hooks := manageHooks()
	hooks.SaveEndpoint = func(s EndpointSpec) error {
		spec = s
		return nil
	}
	m := manageModel(t, hooks)
	m = runCommand(m, "/endpoint edit")
	m = pressKey(m, tea.KeyEnter, 0, "")
	if m.manage.screen != manageMenu {
		t.Fatalf("the screen = %v, want the endpoint menu", m.manage.screen)
	}
	// The name is changed.
	m = pressKey(m, 'n', 0, "n")
	m = typeText(m, "Renamed")
	m = pressKey(m, 's', tea.ModCtrl, "")
	m = pressKey(m, 's', 0, "s")
	if spec.Name != "Renamed" {
		t.Errorf("the name = %q", spec.Name)
	}
	if spec.Protocol != "" || spec.BaseURL != "" {
		t.Errorf("the spec = %+v, want only the name", spec)
	}
	if spec.ID != "primary" {
		t.Errorf("the id = %q", spec.ID)
	}
}

func TestTheEndpointRemoveScreenAsksBeforeItRemoves(t *testing.T) {
	var id, replacement string
	called := false
	hooks := manageHooks()
	hooks.DeleteEndpoint = func(e, r string) error {
		id, replacement = e, r
		called = true
		return nil
	}
	m := manageModel(t, hooks)
	m = runCommand(m, "/endpoint remove")
	m = pressKey(m, tea.KeyEnter, 0, "")
	if m.manage.screen != manageConfirm {
		t.Fatalf("the screen = %v, want the confirmation", m.manage.screen)
	}
	if called {
		t.Fatal("the endpoint was removed before the operator confirmed")
	}
	// The confirmation asks for the model that takes the place of the
	// active one.
	m = pressKey(m, tea.KeyEnter, 0, "")
	if m.manage.screen != managePick {
		t.Fatalf("the screen = %v, want the replacement list", m.manage.screen)
	}
	m = pressKey(m, tea.KeyEnter, 0, "")
	if !called {
		t.Fatal("the endpoint was not removed")
	}
	if id != "primary" {
		t.Errorf("the endpoint = %q", id)
	}
	if replacement != "secondary/other" {
		t.Errorf("the replacement = %q", replacement)
	}
}

func TestTheManagementScreensNeverChangeTheActiveModel(t *testing.T) {
	switched := false
	hooks := manageHooks()
	hooks.SwitchModel = func(string, string) (Engine, error) {
		switched = true
		return nil, nil
	}
	m := manageModel(t, hooks)
	for _, command := range []string{"/model", "/model list", "/model edit",
		"/model remove", "/model context", "/model show", "/model discover",
		"/endpoint", "/endpoint list", "/endpoint add", "/endpoint edit",
		"/endpoint remove"} {
		m = runCommand(m, command)
		if switched {
			t.Fatalf("%q changed the active model", command)
		}
	}
}

func TestTheDiscoveryHappensOnlyWhenTheOperatorAsksForIt(t *testing.T) {
	discovered := 0
	hooks := manageHooks()
	hooks.Discover = func(string) ([]string, error) {
		discovered++
		return []string{"flash", "other"}, nil
	}
	m := manageModel(t, hooks)
	m = runCommand(m, "/model")
	m = pressKey(m, tea.KeyEscape, 0, "")
	m = runCommand(m, "/model list")
	m = pressKey(m, tea.KeyEscape, 0, "")
	if discovered != 0 {
		t.Fatalf("the provider was asked %d time(s) without an explicit action", discovered)
	}
	// The operator names the endpoint and asks for its catalogue.
	m = runCommand(m, "/model discover")
	m = pressKey(m, tea.KeyEnter, 0, "")
	if discovered != 1 {
		t.Fatalf("the provider was asked %d time(s), want once", discovered)
	}
	if m.view != viewModels {
		t.Fatalf("the view = %v, want the catalogue", m.view)
	}
	if !strings.Contains(manageView(m), "other") {
		t.Errorf("the catalogue must name the models:\n%s", manageView(m))
	}
}

func TestTheManagementInterfaceIsReachableWithTheKeyboard(t *testing.T) {
	m := manageModel(t, manageHooks())
	m = runCommand(m, "/model")
	// The letter of an action runs it, and Esc leaves the interface.
	m = pressKey(m, 'l', 0, "l")
	if m.view != viewModels {
		t.Fatalf("the view = %v, want the endpoints", m.view)
	}
	m = pressKey(m, tea.KeyEscape, 0, "")
	m = runCommand(m, "/model")
	m = pressKey(m, tea.KeyEscape, 0, "")
	if m.view != viewChat {
		t.Errorf("the view = %v, want the conversation", m.view)
	}
}

func TestTheManagementInterfaceScrollsInASmallTerminal(t *testing.T) {
	m := NewWithHooks(&scriptEngine{}, manageHooks())
	m.SetColor(false)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 60, Height: 12})
	m = updated.(Model)
	m = runCommand(m, "/model")
	view := manageView(m)
	if strings.Contains(view, "Discover") {
		t.Errorf("the whole menu must not fit in a short terminal:\n%s", view)
	}
	// The last action is reachable by scrolling.
	for i := 0; i < 8; i++ {
		m = pressKey(m, tea.KeyDown, 0, "")
	}
	if !strings.Contains(manageView(m), "Discover") {
		t.Errorf("the last action must be reachable:\n%s", manageView(m))
	}
}
