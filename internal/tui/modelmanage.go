package tui

import (
	"fmt"
	"strconv"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
)

// The interactive management of endpoints and models.
//
// Every command of the /model and /endpoint families works with all its
// arguments, exactly as before. These screens are what the operator gets
// when a command is missing its arguments: the same action, asked for
// step by step.
//
// Nothing here contacts the provider on its own. The only step that
// reaches the network is the one the operator names: the discovery of a
// catalogue. A screen that shows what is already configured never asks
// the provider anything, and it never changes the active model.

// manageScreen is the step of a management workflow.
type manageScreen int

const (
	// manageMenu lists the actions of the interface.
	manageMenu manageScreen = iota
	// managePick asks the operator to choose a model or an endpoint.
	managePick
	// manageForm asks for the values of a change.
	manageForm
	// manageConfirm asks the operator to confirm a removal.
	manageConfirm
)

// manageKind is what the operator is managing.
type manageKind int

const (
	manageModels manageKind = iota
	manageEndpoints
)

// manageAction is one action of the management interface.
//
// Every action is a key in the menu and a command: the operator can
// always type the command instead of walking through the screen.
type manageAction struct {
	// key is the letter that runs the action.
	key string
	// name is what the action is called.
	name string
	// what says what the action does.
	what string
	// command is the command that does the same thing.
	command string
	// run starts the action.
	run func(*Model) tea.Cmd
}

// manageItem is one row of a selection list.
type manageItem struct {
	// name is the row's text.
	name string
	// detail says more about the row.
	detail string
	// endpoint and model name what the row stands for.
	endpoint string
	model    string
	// active marks the model or endpoint that is in use.
	active bool
}

// manageField is one editable field of a form.
type manageField struct {
	// label names the field.
	label string
	// hint says what the field accepts.
	hint string
	// input holds the value.
	input textinput.Model
}

// manageState is the state of the management interface.
type manageState struct {
	// screen is the step of the workflow.
	screen manageScreen
	// back is the step Esc returns to.
	back manageScreen
	// kind is what the workflow is about.
	kind manageKind
	// title names the step.
	title string
	// actions are the actions of the menu.
	actions []manageAction
	// cursor is the chosen action or list row.
	cursor int
	// items are the rows of a selection list.
	items []manageItem
	// fields are the fields of a form.
	fields []manageField
	// focus is the focused field of a form.
	focus int
	// lines are the read-only lines a screen shows above its rows.
	lines []string
	// message explains the step.
	message string
	// notice is the result of the last attempt.
	notice string
	// target is the model or endpoint the workflow is about.
	target string
	// parent is the endpoint of the target model.
	parent string
	// replacement asks for the model that takes the place of the active one.
	replacement bool
	// chosen runs the action of a selection.
	chosen func(*Model, manageItem) tea.Cmd
	// save commits a form.
	save func(*Model) tea.Cmd
}

// modelActions are the actions of the model management interface.
//
// The keys are the same in the endpoint interface, so the two families
// are learned once.
func modelActions() []manageAction {
	return []manageAction{
		{key: "U", name: "Use", what: "use another model for the next request", command: "/model use", run: (*Model).manageUse},
		{key: "A", name: "Add", what: "add a model to an endpoint", command: "/model add", run: (*Model).manageAddModel},
		{key: "E", name: "Edit", what: "change a model's label, effort and output limit", command: "/model edit", run: (*Model).manageEditModel},
		{key: "R", name: "Remove", what: "remove a model", command: "/model remove", run: (*Model).manageRemoveModel},
		{key: "C", name: "Context", what: "the context window and where it came from", command: "/model context", run: (*Model).manageContextModel},
		{key: "S", name: "Show", what: "everything HAL knows about a model", command: "/model show", run: (*Model).manageShowModel},
		{key: "D", name: "Discover", what: "ask the provider which models it serves", command: "/model discover", run: (*Model).manageDiscover},
		{key: "L", name: "List", what: "the endpoints and their models", command: "/model list", run: (*Model).manageList},
	}
}

// endpointActions are the actions of the endpoint management interface.
func endpointActions() []manageAction {
	return []manageAction{
		{key: "L", name: "List", what: "the endpoints and their models", command: "/endpoint list", run: (*Model).manageList},
		{key: "A", name: "Add", what: "add a provider endpoint", command: "/endpoint add", run: (*Model).manageAddEndpoint},
		{key: "E", name: "Edit", what: "change an endpoint's settings", command: "/endpoint edit", run: (*Model).manageEditEndpoint},
		{key: "R", name: "Remove", what: "remove an endpoint and its models", command: "/endpoint remove", run: (*Model).manageRemoveEndpoint},
	}
}

// openModelMenu opens the model management interface.
func (m *Model) openModelMenu() {
	m.manage = manageState{
		screen:  manageMenu,
		kind:    manageModels,
		title:   "Models",
		actions: modelActions(),
	}
	m.panelOffset = 0
	m.view = viewManage
}

// openEndpointMenu opens the endpoint management interface.
func (m *Model) openEndpointMenu() {
	m.manage = manageState{
		screen:  manageMenu,
		kind:    manageEndpoints,
		title:   "Endpoints",
		actions: endpointActions(),
	}
	m.panelOffset = 0
	m.view = viewManage
}

// enterManage puts the management interface on the screen.
//
// Every step of a workflow opens it, whether the operator came from the
// menu or straight from a command.
func (m *Model) enterManage() {
	m.view = viewManage
}

// updateManage handles the management interface.
func (m Model) updateManage(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.ready = true
		m.sizeManageFields()
		return m, nil
	case tea.KeyPressMsg:
		return m.manageKey(msg)
	}
	return m, nil
}

// manageKey handles one key of the management interface.
func (m Model) manageKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case keyQuit, keyQuit2:
		return m, tea.Quit
	}
	switch m.manage.screen {
	case manageMenu:
		return m.manageMenuKey(msg)
	case managePick:
		return m.managePickKey(msg)
	case manageForm:
		return m.manageFormKey(msg)
	case manageConfirm:
		return m.manageConfirmKey(msg)
	}
	return m, nil
}

// manageMenuKey handles a key of the action menu.
func (m Model) manageMenuKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.closeManage("")
		return m, nil
	case "up", "shift+tab":
		if m.manage.cursor > 0 {
			m.manage.cursor--
		}
		m.manageScroll()
		return m, nil
	case "down", "tab":
		if m.manage.cursor < len(m.manage.actions)-1 {
			m.manage.cursor++
		}
		m.manageScroll()
		return m, nil
	case "enter":
		if len(m.manage.actions) == 0 {
			return m, nil
		}
		return m, m.manage.actions[m.manage.cursor].run(&m)
	}
	// A letter runs the action it names.
	key := strings.ToLower(msg.String())
	if len(key) == 1 {
		for _, action := range m.manage.actions {
			if strings.EqualFold(action.key, key) {
				return m, action.run(&m)
			}
		}
	}
	return m, nil
}

// managePickKey handles a key of a selection list.
func (m Model) managePickKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.manageBack()
		return m, nil
	case "up", "shift+tab":
		if m.manage.cursor > 0 {
			m.manage.cursor--
		}
		m.manageScroll()
		return m, nil
	case "down", "tab":
		if m.manage.cursor < len(m.manage.items)-1 {
			m.manage.cursor++
		}
		m.manageScroll()
		return m, nil
	case "enter":
		if len(m.manage.items) == 0 || m.manage.chosen == nil {
			return m, nil
		}
		item := m.manage.items[m.manage.cursor]
		return m, m.manage.chosen(&m, item)
	}
	return m, nil
}

// manageFormKey handles a key of a form.
func (m Model) manageFormKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.manageBack()
		return m, nil
	case "tab", "down", "enter":
		m.manageFocus(1)
		return m, nil
	case "shift+tab", "up":
		m.manageFocus(-1)
		return m, nil
	case "ctrl+s":
		if m.manage.save == nil {
			return m, nil
		}
		return m, m.manage.save(&m)
	case "ctrl+u":
		// The field is emptied, so a value that was filled in for the
		// operator can be replaced instead of appended to.
		if len(m.manage.fields) == 0 {
			return m, nil
		}
		fields := make([]manageField, len(m.manage.fields))
		copy(fields, m.manage.fields)
		fields[m.manage.focus].input.SetValue("")
		m.manage.fields = fields
		return m, nil
	}
	// Every other key belongs to the focused field.
	if len(m.manage.fields) == 0 {
		return m, nil
	}
	fields := make([]manageField, len(m.manage.fields))
	copy(fields, m.manage.fields)
	var cmd tea.Cmd
	fields[m.manage.focus].input, cmd = fields[m.manage.focus].input.Update(msg)
	m.manage.fields = fields
	return m, cmd
}

// manageConfirmKey handles a key of a confirmation.
func (m Model) manageConfirmKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.manageBack()
		return m, nil
	case "enter":
		return m, m.manageConfirm()
	}
	return m, nil
}

// manageConfirm runs the confirmation the operator is looking at.
//
// The active model is never removed silently: the operator names the
// model that takes its place, and the removal and the switch happen
// together.
func (m *Model) manageConfirm() tea.Cmd {
	if !m.manage.replacement {
		return m.runRemoval("")
	}
	items := m.replacementItems()
	if len(items) == 0 {
		m.manage.notice = "There is no other model to use instead."
		return nil
	}
	target, parent := m.manage.target, m.manage.parent
	m.manage.screen = managePick
	m.manage.back = manageConfirm
	m.manage.cursor = 0
	m.manage.title = "Choose the model to use instead"
	m.manage.message = "Enter removes " + target + " and uses the chosen model."
	m.manage.items = items
	m.manage.chosen = func(m *Model, item manageItem) tea.Cmd {
		return m.runRemoval(item.endpoint + "/" + item.model)
	}
	m.enterManage()
	_ = parent
	return nil
}

// runRemoval removes the target of the workflow.
func (m *Model) runRemoval(replacement string) tea.Cmd {
	switch m.manage.kind {
	case manageEndpoints:
		if m.hooks.DeleteEndpoint == nil {
			m.manage.notice = "Endpoint management is not wired up."
			return nil
		}
		if err := m.hooks.DeleteEndpoint(m.manage.target, replacement); err != nil {
			m.manage.notice = "Cannot remove the endpoint: " + err.Error()
			return nil
		}
		m.refreshEngine()
		m.closeManage("Removed endpoint " + m.manage.target + ".")
		return nil
	default:
		if m.hooks.DeleteModel == nil {
			m.manage.notice = "Model management is not wired up."
			return nil
		}
		if err := m.hooks.DeleteModel(m.manage.parent, m.manage.target, replacement); err != nil {
			m.manage.notice = "Cannot remove the model: " + err.Error()
			return nil
		}
		m.refreshEngine()
		m.closeManage("Removed model " + m.manage.target + " from endpoint " + m.manage.parent + ".")
		return nil
	}
}

// manageUse opens the picker.
//
// The active model changes only when the operator names the model they
// want: the picker never switches by itself.
func (m *Model) manageUse() tea.Cmd {
	m.manage.kind = manageModels
	m.enterPicker()
	return nil
}

// manageList shows the endpoints and their models.
func (m *Model) manageList() tea.Cmd {
	return m.showEndpoints()
}

// manageAddModel asks for the fields of a new model.
func (m *Model) manageAddModel() tea.Cmd {
	m.manage.kind = manageModels
	if m.hooks.AddModel == nil {
		m.manage.notice = "Model management is not wired up."
		return nil
	}
	fields := []manageField{
		{label: "Endpoint", hint: "the endpoint the model belongs to", input: namedInput("", m.defaultEndpoint(), false)},
		{label: "Model id", hint: "the identifier the provider uses", input: namedInput("", "", false)},
		{label: "Label", hint: "the name HAL shows; optional", input: namedInput("", "", false)},
	}
	m.openForm("Add a model",
		"The model is added to the endpoint. The active model does not change.",
		fields, (*Model).saveNewModel)
	return nil
}

// manageEditModel asks which model to edit, and then for its settings.
func (m *Model) manageEditModel() tea.Cmd {
	m.manage.kind = manageModels
	items := m.modelItems()
	if len(items) == 0 {
		m.manage.notice = "There is no model to edit. Add one with /model add."
		return nil
	}
	m.managePickModel("Choose a model to edit",
		"Enter opens the settings of the chosen model.", items,
		func(m *Model, item manageItem) tea.Cmd { return m.openModelEditForm(item) })
	return nil
}

// manageRemoveModel asks which model to remove, and then confirms.
func (m *Model) manageRemoveModel() tea.Cmd {
	m.manage.kind = manageModels
	items := m.modelItems()
	if len(items) == 0 {
		m.manage.notice = "There is no model to remove."
		return nil
	}
	m.managePickModel("Choose a model to remove",
		"Enter asks before anything is removed.", items,
		func(m *Model, item manageItem) tea.Cmd { return m.confirmModelRemoval(item) })
	return nil
}

// manageContextModel asks which model's context to look at, and then for
// the limit itself.
func (m *Model) manageContextModel() tea.Cmd {
	m.manage.kind = manageModels
	items := m.modelItems()
	if len(items) == 0 {
		m.manage.notice = "There is no model to look at. Add one with /model add."
		return nil
	}
	m.managePickModel("Choose a model",
		"Enter shows the context window of the chosen model and where it came from.", items,
		func(m *Model, item manageItem) tea.Cmd { return m.openModelContextForm(item) })
	return nil
}

// manageShowModel asks which model to show, and then shows it.
func (m *Model) manageShowModel() tea.Cmd {
	m.manage.kind = manageModels
	items := m.modelItems()
	if len(items) == 0 {
		m.manage.notice = "There is no model to show. Add one with /model add."
		return nil
	}
	m.managePickModel("Choose a model to show",
		"Enter shows everything HAL knows about the chosen model.", items,
		func(m *Model, item manageItem) tea.Cmd { return m.showModelDetails(item) })
	return nil
}

// manageDiscover asks which endpoint to read the catalogue of.
//
// The provider is asked only when the operator names the endpoint: no
// screen of this interface contacts a provider on its own.
func (m *Model) manageDiscover() tea.Cmd {
	m.manage.kind = manageModels
	if m.hooks.Discover == nil {
		m.manage.notice = "Model discovery is not wired up."
		return nil
	}
	items := m.endpointItems()
	if len(items) == 0 {
		m.manage.notice = "There is no endpoint to ask. Add one with /endpoint add."
		return nil
	}
	m.managePickModel("Choose an endpoint to ask",
		"Enter asks the provider for its catalogue.", items,
		func(m *Model, item manageItem) tea.Cmd { return m.discoverEndpoint(item.endpoint) })
	return nil
}

// manageAddEndpoint asks for the fields of a new endpoint.
func (m *Model) manageAddEndpoint() tea.Cmd {
	m.manage.kind = manageEndpoints
	if m.hooks.SaveEndpoint == nil {
		m.manage.notice = "Endpoint management is not wired up."
		return nil
	}
	fields := []manageField{
		{label: "Id", hint: "letters, digits, _ and -; it never changes", input: namedInput("", "", false)},
		{label: "Name", hint: "the name HAL shows", input: namedInput("", "", false)},
		{label: "Protocol", hint: "openai-compatible, anthropic-compatible, local or custom; empty means openai-compatible", input: namedInput("openai-compatible", "", false)},
		{label: "Base url", hint: "https://…", input: namedInput("", "", false)},
		{label: "Credential env", hint: "the variable that holds the key; the key itself never enters the file", input: namedInput("", "", false)},
	}
	m.openForm("Add an endpoint",
		"The endpoint is added. The active model does not change.",
		fields, (*Model).saveNewEndpoint)
	return nil
}

// manageEditEndpoint asks which endpoint to edit, and then for its
// settings.
func (m *Model) manageEditEndpoint() tea.Cmd {
	m.manage.kind = manageEndpoints
	items := m.endpointItems()
	if len(items) == 0 {
		m.manage.notice = "There is no endpoint to edit. Add one with /endpoint add."
		return nil
	}
	m.managePickModel("Choose an endpoint to edit",
		"Enter opens the settings of the chosen endpoint.", items,
		func(m *Model, item manageItem) tea.Cmd { return m.openEndpointEditForm(item) })
	return nil
}

// manageRemoveEndpoint asks which endpoint to remove, and then confirms.
func (m *Model) manageRemoveEndpoint() tea.Cmd {
	m.manage.kind = manageEndpoints
	items := m.endpointItems()
	if len(items) == 0 {
		m.manage.notice = "There is no endpoint to remove."
		return nil
	}
	m.managePickModel("Choose an endpoint to remove",
		"Enter asks before anything is removed.", items,
		func(m *Model, item manageItem) tea.Cmd { return m.confirmEndpointRemoval(item) })
	return nil
}

// openModelEditForm asks for the settings of one model.
func (m *Model) openModelEditForm(item manageItem) tea.Cmd {
	if m.hooks.ModelDetail == nil {
		m.manage.notice = "Model management is not wired up."
		return nil
	}
	detail, err := m.hooks.ModelDetail(item.endpoint, item.model)
	if err != nil {
		m.manage.notice = "Cannot read the model: " + err.Error()
		return nil
	}
	fields := []manageField{
		{label: "Label", hint: "the name HAL shows", input: namedInput(detail.Settings.Label, "", false)},
		{label: "Effort", hint: "low, medium, high, xhigh or max", input: namedInput(detail.Settings.Effort, "", false)},
		{label: "Output limit", hint: "tokens; empty keeps the provider's default", input: namedInput(limitValue(detail.Settings.MaxOutputTokens), "", false)},
	}
	m.openForm("Edit model "+item.endpoint+" · "+item.model,
		"An empty field keeps the current value.", fields,
		func(m *Model) tea.Cmd { return m.saveModelEdit(item, detail) })
	return nil
}

// openModelContextForm asks for the context window of one model.
//
// The settings and where they came from are shown above the fields: the
// operator sees what the limit is before changing it.
func (m *Model) openModelContextForm(item manageItem) tea.Cmd {
	if m.hooks.ModelDetail == nil {
		m.manage.notice = "Model management is not wired up."
		return nil
	}
	detail, err := m.hooks.ModelDetail(item.endpoint, item.model)
	if err != nil {
		m.manage.notice = "Cannot read the model: " + err.Error()
		return nil
	}
	fields := []manageField{
		{label: "Mode", hint: "auto uses the provider's own window, manual uses yours", input: namedInput(detail.Settings.ContextMode, "", false)},
		{label: "Window", hint: "tokens; used in manual mode", input: namedInput(limitValue(detail.Settings.ContextWindow), "", false)},
	}
	m.manage.lines = detail.Context
	m.openForm("Context of "+item.endpoint+" · "+item.model,
		"An empty field keeps the current value.", fields,
		func(m *Model) tea.Cmd { return m.saveModelContext(item, detail) })
	return nil
}

// openEndpointEditForm asks for the settings of one endpoint.
//
// The credential is never a field: only the name of the environment
// variable that holds it, so a secret never reaches the screen.
func (m *Model) openEndpointEditForm(item manageItem) tea.Cmd {
	if m.hooks.EndpointDetail == nil {
		m.manage.notice = "Endpoint management is not wired up."
		return nil
	}
	detail, err := m.hooks.EndpointDetail(item.endpoint)
	if err != nil {
		m.manage.notice = "Cannot read the endpoint: " + err.Error()
		return nil
	}
	fields := []manageField{
		{label: "Name", hint: "the name HAL shows", input: namedInput(detail.Settings.Name, "", false)},
		{label: "Protocol", hint: "openai-compatible, anthropic-compatible, local or custom", input: namedInput(detail.Settings.Protocol, "", false)},
		{label: "Wire protocol", hint: "openai-compatible or anthropic-compatible; a custom endpoint needs one", input: namedInput(detail.Settings.WireProtocol, "", false)},
		{label: "Base url", hint: "https://…", input: namedInput(detail.Settings.BaseURL, "", false)},
		{label: "Credential env", hint: "the variable that holds the key; the key itself never enters the file", input: namedInput(detail.Settings.CredentialEnv, "", false)},
		{label: "Discovery", hint: "on or off", input: namedInput(onOffValue(detail.Settings.Discovery), "", false)},
	}
	m.openForm("Edit endpoint "+item.endpoint,
		"An empty field keeps the current value.", fields,
		func(m *Model) tea.Cmd { return m.saveEndpointEdit(item, detail) })
	return nil
}

// saveNewModel stores the model the operator described.
func (m *Model) saveNewModel() tea.Cmd {
	values := m.manageValues()
	endpoint, id, label := values[0], values[1], values[2]
	if endpoint == "" || id == "" {
		m.manage.notice = "The endpoint and the model id are required."
		return nil
	}
	if m.hooks.AddModel == nil {
		m.manage.notice = "Model management is not wired up."
		return nil
	}
	if err := m.hooks.AddModel(endpoint, ModelSpec{ID: id, Label: label}); err != nil {
		m.manage.notice = "Cannot add the model: " + err.Error()
		return nil
	}
	m.refreshEngine()
	m.closeManage("Added model " + id + " to endpoint " + endpoint + ". The active model is unchanged.")
	return nil
}

// saveModelEdit stores the settings the operator changed.
//
// Only the settings that changed are sent: the configuration handlers
// change what they are given and keep the rest.
func (m *Model) saveModelEdit(item manageItem, before ModelDetail) tea.Cmd {
	if m.hooks.EditModel == nil {
		m.manage.notice = "Model management is not wired up."
		return nil
	}
	values := m.manageValues()
	var spec ModelSpec
	changed := false
	if manageChanged(values[0], before.Settings.Label) {
		spec.Label = values[0]
		changed = true
	}
	if manageChanged(values[1], before.Settings.Effort) {
		spec.Effort = values[1]
		changed = true
	}
	if values[2] != "" && values[2] != limitValue(before.Settings.MaxOutputTokens) {
		tokens, err := strconv.Atoi(values[2])
		if err != nil || tokens <= 0 {
			// Zero is the provider's own default, and the
			// configuration keeps a zero unnamed: it cannot be
			// written. Saying so is better than reporting a change
			// that did not happen.
			m.manage.notice = "The output limit must be a number of tokens. " +
				"The provider's own default cannot be restored here."
			return nil
		}
		spec.MaxOutputTokens = tokens
		changed = true
	}
	if !changed {
		m.closeManage("Nothing changed.")
		return nil
	}
	if err := m.hooks.EditModel(item.endpoint, item.model, spec); err != nil {
		m.manage.notice = "Cannot change the model: " + err.Error()
		return nil
	}
	m.refreshEngine()
	m.closeManage("Model " + item.model + " of endpoint " + item.endpoint + " updated.")
	return nil
}

// saveModelContext stores the context limit the operator chose.
func (m *Model) saveModelContext(item manageItem, before ModelDetail) tea.Cmd {
	if m.hooks.EditModel == nil {
		m.manage.notice = "Model management is not wired up."
		return nil
	}
	values := m.manageValues()
	mode := strings.ToLower(values[0])
	switch mode {
	case "", "auto", "manual":
	default:
		m.manage.notice = "The context mode is auto or manual."
		return nil
	}
	window := 0
	if values[1] != "" {
		tokens, err := strconv.Atoi(values[1])
		if err != nil || tokens <= 0 {
			m.manage.notice = "The context window must be a positive number of tokens."
			return nil
		}
		window = tokens
	}
	// A window belongs to the manual mode. The two are set together, so
	// the record never says "the provider's own window" and names one
	// at the same time.
	if window > 0 && mode != "manual" {
		mode = "manual"
	}
	var spec ModelSpec
	if mode != "" && mode != before.Settings.ContextMode {
		spec.ContextMode = mode
	}
	if window > 0 && window != before.Settings.ContextWindow {
		spec.ContextWindow = window
	}
	if spec.ContextMode == "" && spec.ContextWindow == 0 {
		m.closeManage("Nothing changed.")
		return nil
	}
	if err := m.hooks.EditModel(item.endpoint, item.model, spec); err != nil {
		m.manage.notice = "Cannot change the context limits: " + err.Error()
		return nil
	}
	m.refreshEngine()
	m.closeManage("Context limit for " + item.model + ": " + contextSummary(spec, before))
	return nil
}

// saveNewEndpoint stores the endpoint the operator described.
func (m *Model) saveNewEndpoint() tea.Cmd {
	if m.hooks.SaveEndpoint == nil {
		m.manage.notice = "Endpoint management is not wired up."
		return nil
	}
	values := m.manageValues()
	id, name, protocol, baseURL, credentialEnv := values[0], values[1], values[2], values[3], values[4]
	if id == "" {
		m.manage.notice = "The endpoint needs an id."
		return nil
	}
	discovery := true
	spec := EndpointSpec{
		ID:            id,
		Name:          name,
		Protocol:      protocol,
		BaseURL:       baseURL,
		CredentialEnv: credentialEnv,
		Discovery:     &discovery,
	}
	if err := m.hooks.SaveEndpoint(spec); err != nil {
		m.manage.notice = "Cannot add the endpoint: " + err.Error()
		return nil
	}
	m.refreshEngine()
	m.closeManage("Added endpoint " + id + ".")
	return nil
}

// saveEndpointEdit stores the settings the operator changed.
func (m *Model) saveEndpointEdit(item manageItem, before EndpointDetail) tea.Cmd {
	if m.hooks.SaveEndpoint == nil {
		m.manage.notice = "Endpoint management is not wired up."
		return nil
	}
	values := m.manageValues()
	var spec EndpointSpec
	changed := false
	if manageChanged(values[0], before.Settings.Name) {
		spec.Name = values[0]
		changed = true
	}
	if manageChanged(values[1], before.Settings.Protocol) {
		spec.Protocol = values[1]
		changed = true
	}
	if manageChanged(values[2], before.Settings.WireProtocol) {
		spec.WireProtocol = values[2]
		changed = true
	}
	if manageChanged(values[3], before.Settings.BaseURL) {
		spec.BaseURL = values[3]
		changed = true
	}
	if manageChanged(values[4], before.Settings.CredentialEnv) {
		spec.CredentialEnv = values[4]
		changed = true
	}
	discovery := discoveryEnabled(before.Settings.Discovery)
	if values[5] != "" {
		parsed, err := parseOnOff(values[5])
		if err != nil {
			m.manage.notice = "Discovery is on or off."
			return nil
		}
		discovery = parsed
	}
	if discovery != discoveryEnabled(before.Settings.Discovery) {
		spec.Discovery = &discovery
		changed = true
	}
	if !changed {
		m.closeManage("Nothing changed.")
		return nil
	}
	spec.ID = item.endpoint
	if err := m.hooks.SaveEndpoint(spec); err != nil {
		m.manage.notice = "Cannot change the endpoint: " + err.Error()
		return nil
	}
	m.refreshEngine()
	m.closeManage("Endpoint " + item.endpoint + " updated.")
	return nil
}

// confirmModelRemoval asks before a model is removed.
func (m *Model) confirmModelRemoval(item manageItem) tea.Cmd {
	m.enterManage()
	m.manage.screen = manageConfirm
	m.manage.back = manageMenu
	m.manage.title = "Remove model " + item.endpoint + " · " + item.model + "?"
	m.manage.target = item.model
	m.manage.parent = item.endpoint
	m.manage.replacement = item.active
	if item.active {
		m.manage.message = "This is the active model. Choose the model to use instead: the removal and the switch happen together."
		return nil
	}
	m.manage.message = "The model is removed from endpoint " + item.endpoint + ". The active model does not change."
	return nil
}

// confirmEndpointRemoval asks before an endpoint is removed.
func (m *Model) confirmEndpointRemoval(item manageItem) tea.Cmd {
	m.enterManage()
	m.manage.screen = manageConfirm
	m.manage.back = manageMenu
	m.manage.title = "Remove endpoint " + item.endpoint + "?"
	m.manage.target = item.endpoint
	m.manage.parent = ""
	m.manage.replacement = item.active
	if item.active {
		m.manage.message = "This endpoint holds the active model. Choose the model to use instead: the removal and the switch happen together."
		return nil
	}
	m.manage.message = "The endpoint and its models are removed. The active model does not change."
	return nil
}

// showModelDetails opens the details of one model.
func (m *Model) showModelDetails(item manageItem) tea.Cmd {
	if m.hooks.ModelDetails == nil {
		m.manage.notice = "Model management is not wired up."
		return nil
	}
	lines, err := m.hooks.ModelDetails(item.endpoint, item.model)
	if err != nil {
		m.manage.notice = "Cannot show the model: " + err.Error()
		return nil
	}
	m.openModelCfg("Model "+item.model, lines)
	return nil
}

// discoverEndpoint asks one endpoint for its catalogue.
func (m *Model) discoverEndpoint(endpoint string) tea.Cmd {
	if m.hooks.Discover == nil {
		m.manage.notice = "Model discovery is not wired up."
		return nil
	}
	ids, err := m.hooks.Discover(endpoint)
	if err != nil {
		m.manage.notice = "Cannot read the catalogue: " + err.Error()
		return nil
	}
	m.openCatalogue(endpoint, ids)
	return nil
}

// managePickModel opens a selection list.
//
// The chosen row is handed to the action that asked for it.
func (m *Model) managePickModel(title, message string, items []manageItem,
	then func(*Model, manageItem) tea.Cmd) {
	m.manage.screen = managePick
	m.manage.back = manageMenu
	m.manage.title = title
	m.manage.message = message
	m.manage.items = items
	m.manage.cursor = 0
	m.manage.chosen = then
	m.manage.notice = ""
	m.panelOffset = 0
	m.enterManage()
}

// openForm opens a form with the given fields.
func (m *Model) openForm(title, message string, fields []manageField, save func(*Model) tea.Cmd) {
	m.manage.screen = manageForm
	m.manage.back = manageMenu
	m.manage.title = title
	m.manage.message = message
	m.manage.fields = fields
	m.manage.focus = 0
	m.manage.save = save
	m.manage.notice = ""
	m.manage.cursor = 0
	m.panelOffset = 0
	m.enterManage()
	m.sizeManageFields()
	for i := range m.manage.fields {
		m.manage.fields[i].input.Blur()
	}
	if len(m.manage.fields) > 0 {
		m.manage.fields[0].input.Focus()
	}
}

// manageFocus moves the focus of a form.
func (m *Model) manageFocus(delta int) {
	if len(m.manage.fields) == 0 {
		return
	}
	m.manage.fields[m.manage.focus].input.Blur()
	m.manage.focus = (m.manage.focus + delta + len(m.manage.fields)) % len(m.manage.fields)
	m.manage.fields[m.manage.focus].input.Focus()
}

// manageBack returns to the step the operator came from.
//
// Nothing is changed on the way back: a cancelled form keeps the
// configuration exactly as it was.
func (m *Model) manageBack() {
	switch m.manage.back {
	case managePick, manageForm, manageConfirm:
		m.manage.screen = m.manage.back
		m.manage.back = manageMenu
		m.manage.cursor = 0
		m.manage.notice = ""
		m.panelOffset = 0
	default:
		m.closeManage("")
	}
}

// closeManage leaves the management interface.
func (m *Model) closeManage(notice string) {
	m.manage = manageState{}
	m.view = viewChat
	if notice != "" {
		m.notice = notice
	}
}

// manageScroll keeps the chosen row inside the panel.
//
// The rows of a screen start below its title, so the chosen row is not
// at the index of the list it belongs to.
func (m *Model) manageScroll() {
	row := 2 + m.manage.cursor
	total := 2 + len(m.manage.items)
	if m.manage.screen == manageMenu {
		total = 2 + len(m.manage.actions) + 3
		if m.manage.kind == manageModels {
			total++
		}
	}
	m.ensureVisible(row, total, contentHeight(m.height))
}

// sizeManageFields gives the fields the width of the panel.
func (m *Model) sizeManageFields() {
	width := m.width - 24
	if width < 8 {
		width = 8
	}
	for i := range m.manage.fields {
		m.manage.fields[i].input.SetWidth(width)
	}
}

// manageValues are the values of the form's fields.
func (m *Model) manageValues() []string {
	out := make([]string, 0, len(m.manage.fields))
	for _, field := range m.manage.fields {
		out = append(out, strings.TrimSpace(field.input.Value()))
	}
	return out
}

// modelItems lists the configured models.
func (m *Model) modelItems() []manageItem {
	var out []manageItem
	if m.hooks.ListModels == nil {
		return nil
	}
	for _, info := range m.hooks.ListModels() {
		name := info.ID
		if info.Endpoint != "" {
			name = info.Endpoint + " · " + info.ID
		}
		detail := info.Label
		if detail == info.ID {
			detail = ""
		}
		out = append(out, manageItem{
			name:     name,
			detail:   detail,
			endpoint: info.Endpoint,
			model:    info.ID,
			active:   info.Active,
		})
	}
	return out
}

// endpointItems lists the configured endpoints.
func (m *Model) endpointItems() []manageItem {
	var out []manageItem
	if m.hooks.Endpoints == nil {
		return nil
	}
	for _, endpoint := range m.hooks.Endpoints() {
		out = append(out, manageItem{
			name:     endpoint.Name,
			detail:   endpoint.ID + " · " + endpoint.Protocol,
			endpoint: endpoint.ID,
			active:   endpoint.Active,
		})
	}
	return out
}

// replacementItems lists the models that can take the place of the one
// that is going away.
//
// The model that is being removed is never in the list, and a model of
// the endpoint that is being removed is never in it either: a
// replacement inside a removed endpoint would point at nothing.
func (m *Model) replacementItems() []manageItem {
	var out []manageItem
	if m.hooks.Endpoints == nil {
		return nil
	}
	for _, endpoint := range m.hooks.Endpoints() {
		if m.manage.kind == manageEndpoints && endpoint.ID == m.manage.target {
			continue
		}
		for _, model := range endpoint.Models {
			if m.manage.kind == manageModels && endpoint.ID == m.manage.parent && model.ID == m.manage.target {
				continue
			}
			out = append(out, manageItem{
				name:     endpoint.ID + " · " + model.ID,
				endpoint: endpoint.ID,
				model:    model.ID,
			})
		}
	}
	return out
}

// defaultEndpoint is the endpoint a new model goes under.
func (m *Model) defaultEndpoint() string {
	if m.hooks.Endpoints == nil {
		return ""
	}
	endpoints := m.hooks.Endpoints()
	for _, endpoint := range endpoints {
		if endpoint.Active {
			return endpoint.ID
		}
	}
	if len(endpoints) == 1 {
		return endpoints[0].ID
	}
	return ""
}

// manageChanged reports whether the operator changed a field.
//
// An empty field keeps the current value: the configuration handlers
// change the settings they are given, and an empty value is not one of
// them.
func manageChanged(now, before string) bool {
	now = strings.TrimSpace(now)
	return now != "" && now != strings.TrimSpace(before)
}

// limitValue is a token limit as the operator sees it.
//
// Zero is the provider's own default, so it is shown as nothing to
// type.
func limitValue(tokens int) string {
	if tokens <= 0 {
		return ""
	}
	return strconv.Itoa(tokens)
}

// onOffValue is a flag as the operator sees it.
//
// A flag that was never named is shown as "on": it is the endpoint's own
// setting, and the operator changes it only by naming it.
func onOffValue(on *bool) string {
	if on == nil || *on {
		return "on"
	}
	return "off"
}

// discoveryEnabled is the value of a flag that may never have been named.
func discoveryEnabled(on *bool) bool {
	if on == nil {
		return true
	}
	return *on
}

// parseOnOff reads a flag the operator typed.
func parseOnOff(value string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "on", "true", "yes":
		return true, nil
	case "off", "false", "no":
		return false, nil
	case "":
		return false, fmt.Errorf("the flag is empty")
	default:
		return false, fmt.Errorf("the flag is unknown")
	}
}

// contextSummary says what the context limit became.
//
// The window that is reported is the one the record holds after the
// change: a mode that was switched to automatic clears the window, and
// a window that was left alone keeps its value.
func contextSummary(spec ModelSpec, before ModelDetail) string {
	mode := spec.ContextMode
	if mode == "" {
		mode = before.Settings.ContextMode
	}
	if mode == "auto" {
		return "automatic."
	}
	window := spec.ContextWindow
	if window == 0 {
		window = before.Settings.ContextWindow
	}
	if window > 0 {
		return fmt.Sprintf("manual, %d tokens.", window)
	}
	return "manual."
}

// manageRows renders the management interface.
//
// Every name that can come from the provider is flattened before it is
// drawn: a hostile endpoint must not be able to move the cursor or
// forge a row with control characters in its own name.
func (m Model) manageRows() []string {
	st := m.theme.styles()
	rows := []string{oneLine(m.manage.title) + " (" + m.manageKeys() + ")", ""}
	for _, line := range m.manage.lines {
		rows = append(rows, oneLine(line))
	}
	if len(m.manage.lines) > 0 {
		rows = append(rows, "")
	}
	switch m.manage.screen {
	case manageMenu:
		for i, action := range m.manage.actions {
			marker := "  "
			if i == m.manage.cursor {
				marker = "▸ "
			}
			line := marker + action.key + "  " + padTo(action.name, 9) + action.what
			if i == m.manage.cursor {
				line = st.active.Render(line)
			}
			rows = append(rows, line)
		}
		rows = append(rows, "")
		rows = append(rows, "  "+st.footer.Render("Every action is also a command:"))
		commands := ""
		for _, action := range m.manage.actions {
			if commands != "" {
				commands += " · "
			}
			commands += action.command
		}
		rows = append(rows, "  "+st.footer.Render(commands))
		if m.manage.kind == manageModels {
			rows = append(rows, "  "+st.footer.Render("Settings: label, effort, context_mode, context_window, max_output_tokens"))
		}
	case managePick:
		if len(m.manage.items) == 0 {
			rows = append(rows, "  (there is nothing to choose)")
		}
		for i, item := range m.manage.items {
			marker := "  "
			if i == m.manage.cursor {
				marker = "▸ "
			}
			line := marker + oneLine(item.name)
			if item.detail != "" {
				line += " · " + oneLine(item.detail)
			}
			if item.active {
				line += " (active)"
			}
			if i == m.manage.cursor {
				line = st.active.Render(line)
			}
			rows = append(rows, line)
		}
	case manageForm:
		for i, field := range m.manage.fields {
			marker := "  "
			if i == m.manage.focus {
				marker = "▸ "
			}
			rows = append(rows, marker+padTo(field.label, 16)+field.input.View())
			if field.hint != "" && i == m.manage.focus {
				rows = append(rows, "    "+st.footer.Render(oneLine(field.hint)))
			}
		}
	case manageConfirm:
		rows = append(rows, "  "+oneLine(m.manage.message))
	}
	if m.manage.message != "" && m.manage.screen != manageConfirm {
		rows = append(rows, "", "  "+oneLine(m.manage.message))
	}
	if m.manage.notice != "" {
		rows = append(rows, "", "  "+st.footer.Render(oneLine(m.manage.notice)))
	}
	return rows
}

// manageKeys says what the keys of the current screen do.
func (m Model) manageKeys() string {
	switch m.manage.screen {
	case managePick:
		return "Up and down choose · Enter picks · Esc back"
	case manageForm:
		return "Tab moves · Ctrl+S saves · Esc cancels"
	case manageConfirm:
		return "Enter confirms · Esc keeps it"
	default:
		return "Up and down choose · Enter runs · Esc back"
	}
}
