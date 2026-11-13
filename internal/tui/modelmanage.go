package tui

import (
	"fmt"
	"strconv"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"

	"github.com/flowbyte-com/HAL9001/internal/provider"
)

// The interactive management of endpoints and models.
//
// Every command of the /model and /endpoint families works with all its
// arguments, exactly as before. These screens are what the operator gets
// when a command is missing its arguments: the same action, asked for
// step by step.
//
// The screens are a hierarchy. A menu offers actions, a list offers
// choices, a form asks for values and a confirmation asks once. Every
// step knows the step it came from, so Esc walks back one level at a
// time and the top of the hierarchy is the conversation.
//
// Nothing here contacts the provider on its own. The only steps that
// reach the network are the ones the operator names: the discovery of a
// catalogue and the connection test of an endpoint. A screen that shows
// what is already configured never asks the provider anything, and it
// never changes the active model.

// manageScreen is the step of a management workflow.
type manageScreen int

const (
	// manageMenu lists the actions of a step.
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

// The values a row of a list can carry that are not an endpoint or a
// model.
const (
	// manageCreateEndpoint is the row that opens the endpoint screens
	// instead of choosing something that already exists.
	manageCreateEndpoint = "\x00create-endpoint"
	// manageOtherCredential is the row that asks for the name of
	// another environment variable.
	manageOtherCredential = "\x00other-credential"
	// manageEnterKey is the row that asks for a literal API key in a
	// masked field.
	manageEnterKey = "\x00enter-key"
	// manageNoCredential is the row that clears any credential.
	manageNoCredential = "\x00no-credential"
)

// manageAction is one action of a menu.
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
	// whatFn says what the action does, in terms of the value it
	// holds.
	//
	// A row whose description depends on its own value says it here,
	// so the description is never stale: the credential of an endpoint
	// says whether its variable holds a value, and that changes the
	// moment the operator names another variable.
	whatFn func(value string) string
	// value is what the action currently holds.
	//
	// It is shown in a column of its own, so the operator sees the
	// current setting next to the name of the setting.
	value string
	// empty is what the row shows when it holds nothing yet. It is
	// shown in place of the value, and it is never stored.
	empty string
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
	// value is what the row carries: the identifier of a setting, the
	// name of a variable, or one of the markers above.
	value string
	// mark says what the row is: "active" for the model in use,
	// "current" for the setting that is in force.
	mark string
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

// manageChange is one change the operator has chosen but not saved.
type manageChange struct {
	// value is the new value.
	value string
	// reset says the value is restored to its default.
	reset bool
}

// manageState is the state of the management interface.
type manageState struct {
	// screen is the step of the workflow.
	screen manageScreen
	// kind is what the workflow is about.
	kind manageKind
	// root marks the menu of a family: the step every workflow of that
	// family grows out of, and the step Esc returns to.
	root bool
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
	// save commits a form or a menu of settings.
	save func(*Model) tea.Cmd
	// keys overrides the hint of the screen. It is empty when the
	// default hint of the screen type is the right one.
	keys string
	// pending holds the changes the operator has chosen in a settings
	// menu and not saved yet.
	pending map[string]manageChange
	// endpointBefore is the endpoint as it was before the operator
	// touched it.
	//
	// An edit sends only what changed, and only the endpoint that is
	// being edited can say what it held before.
	endpointBefore *EndpointDetail
	// after is called with the id of an endpoint that was just stored.
	//
	// It is how a workflow that opened the endpoint screens goes on
	// where it left off.
	after func(*Model, string) tea.Cmd
	// endpointCredential is a literal key the operator typed in a
	// masked field and has not saved yet. It is never displayed, never
	// stored in a draft and never logged: it travels only to the
	// configuration writer, and it is cleared on save or on leave.
	endpointCredential string
	// endpointHasLiteral reports whether the endpoint being edited
	// already holds a literal key. The value itself is never held: only
	// whether there is one.
	endpointHasLiteral bool
	// endpointClearCredential asks for any credential to be removed.
	endpointClearCredential bool
	// draft holds what the operator typed in the form of this step.
	//
	// The values belong to the step, not to the form that is drawn: a
	// form that is left and come back to still holds them. A masked
	// key is never held here.
	draft []string
}

// modelActions are the actions of the model management interface.
//
// The keys are the same in the endpoint interface, so the two families
// are learned once.
func modelActions() []manageAction {
	return []manageAction{
		{key: "U", name: "Use", what: "use another model for the next request", command: "/model use", run: (*Model).manageUse},
		{key: "A", name: "Add", what: "add a model to an endpoint", command: "/model add", run: (*Model).manageAddModel},
		{key: "E", name: "Edit", what: "change a model's settings", command: "/model edit", run: (*Model).manageEditModel},
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
	m.manageRoot(manageModels)
}

// openEndpointMenu opens the endpoint management interface.
func (m *Model) openEndpointMenu() {
	m.manageRoot(manageEndpoints)
}

// manageRoot puts the management interface at its top.
//
// Every workflow of the interface grows out of the menu of its family:
// a command that opens a screen of its own (/model edit) has the menu
// below it, so Esc walks back to the menu, and Esc from the menu
// returns to the conversation.
//
// A menu that is already open is left alone: the operator who walked
// into a screen from the menu keeps the menu below them.
func (m *Model) manageRoot(kind manageKind) {
	if m.manage.root && m.manage.kind == kind {
		// The menu is already the top of the hierarchy.
		return
	}
	menu := manageState{
		screen:  manageMenu,
		kind:    kind,
		root:    true,
		title:   "Models",
		actions: modelActions(),
	}
	if kind == manageEndpoints {
		menu.title = "Endpoints"
		menu.actions = endpointActions()
	}
	m.manage = menu
	m.manageStack = nil
	m.panelOffset = 0
	m.enterManage()
}

// enterManage puts the management interface on the screen.
//
// Every step of a workflow opens it, whether the operator came from the
// menu or straight from a command.
func (m *Model) enterManage() {
	m.view = viewManage
}

// managePush saves the current step and shows a new one.
//
// The saved step is a copy, so what the operator typed in a form is
// still there when they come back to it.
func (m *Model) managePush() {
	m.manageStack = append(m.manageStack, m.manage)
}

// managePop returns to the step that was saved.
//
// It reports whether there was one: the top of the hierarchy is the
// conversation, and there is no step below it.
func (m *Model) managePop() bool {
	if len(m.manageStack) == 0 {
		return false
	}
	last := len(m.manageStack) - 1
	m.manage = m.manageStack[last]
	m.manageStack = m.manageStack[:last]
	return true
}

// updateManage handles the management interface.
func (m Model) updateManage(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.ready = true
		m.sizeManageFields()
		return m, nil
	case tea.PasteMsg:
		// A paste belongs to the focused field, never to a menu
		// shortcut: pasted text is inserted, never executed. A
		// single-line field normalises unexpected line breaks.
		if m.manage.screen == manageForm && len(m.manage.fields) > 0 {
			fields := make([]manageField, len(m.manage.fields))
			copy(fields, m.manage.fields)
			normalised := normaliseSingleLinePaste(string(msg.Content))
			fields[m.manage.focus].input, _ = fields[m.manage.focus].input.Update(tea.PasteMsg{Content: normalised})
			m.manage.fields = fields
			return m, nil
		}
		return m, nil
	case tea.KeyPressMsg:
		return m.manageKey(msg)
	}
	return m, nil
}

// normaliseSingleLinePaste safely normalises a paste into a
// single-line field.
//
// A terminal paste may carry CRLF line endings, tabs or line breaks
// copied with the value: a URL with a trailing newline, a key with a
// line break. A single-line field holds one line, so line breaks and
// tabs become spaces and surrounding whitespace is trimmed. Nothing
// executes: the result is inserted as text, never as keys.
func normaliseSingleLinePaste(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\t", " ")
	return strings.TrimSpace(s)
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
		m.manageBack()
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
	case "ctrl+s":
		// A menu of settings is saved with the same key a form is.
		if m.manage.save == nil {
			return m, nil
		}
		return m, m.manage.save(&m)
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
	target := m.manage.target
	kind := m.manage.kind
	parent := m.manage.parent
	m.managePush()
	m.manage.screen = managePick
	m.manage.kind = kind
	m.manage.target = target
	m.manage.parent = parent
	m.manage.title = "Choose the model to use instead"
	m.manage.message = "Enter removes " + target + " and uses the chosen model."
	m.manage.items = items
	m.manage.cursor = 0
	m.manage.chosen = func(m *Model, item manageItem) tea.Cmd {
		return m.runRemoval(item.endpoint + "/" + item.model)
	}
	m.enterManage()
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

// manageUse asks which model to use.
//
// The list is a step of the workflow: Esc returns to the menu the
// operator came from. The active model changes only when the operator
// names the model they want: the list never switches by itself.
func (m *Model) manageUse() tea.Cmd {
	m.manageRoot(manageModels)
	m.manage.kind = manageModels
	if m.hooks.SwitchModel == nil {
		m.manage.notice = "Switching is not wired up."
		return nil
	}
	items := m.modelItems()
	if len(items) == 0 {
		m.manage.notice = "There is no model to use. Add one with /model add."
		return nil
	}
	m.managePickModel("Choose the model to use",
		"Enter uses the chosen model for the next request.", items,
		func(m *Model, item manageItem) tea.Cmd { return m.switchToModel(item) })
	return nil
}

// switchToModel makes the chosen model the active one.
//
// The switch is the only thing that changes the active model, and it
// happens here, where the operator named the model.
func (m *Model) switchToModel(item manageItem) tea.Cmd {
	eng, err := m.hooks.SwitchModel(item.endpoint, item.model)
	if err != nil {
		m.manage.notice = "Switch failed: " + err.Error()
		return nil
	}
	if eng != nil {
		m.engine = eng
	}
	m.model = item.model
	if m.hooks.Effort != nil {
		m.SetEffort(m.hooks.Effort())
	}
	m.closeManage("Model: " + item.model + " (applies to the next request)")
	return nil
}

// manageList shows the endpoints and their models.
func (m *Model) manageList() tea.Cmd {
	return m.showEndpoints()
}

// manageAddModel asks which endpoint the new model belongs to.
//
// The endpoint is chosen from the endpoints HAL knows. A URL is not an
// endpoint id, and the screen never lets the two be confused.
func (m *Model) manageAddModel() tea.Cmd {
	m.manageRoot(manageModels)
	m.manage.kind = manageModels
	if m.hooks.AddModel == nil {
		m.manage.notice = "Model management is not wired up."
		return nil
	}
	m.managePush()
	m.openEndpointPicker("Add a model",
		"Choose the endpoint the model belongs to. Enter opens the model form.",
		func(m *Model, item manageItem) tea.Cmd {
			if item.value == manageCreateEndpoint {
				return m.openNewEndpoint(func(m *Model, id string) tea.Cmd {
					return addModelAfterEndpoint(m, id)
				})
			}
			return m.openAddModelForm(item.endpoint)
		})
	return nil
}

// addModelAfterEndpoint continues the Add Model workflow with the
// endpoint that was just created.
//
// The operator is taken back to the step they came from and the model
// form opens with the new endpoint chosen, so the task they started is
// finished without starting over.
//
// It is a function and not a method on purpose: a method value would
// bind the model of the moment the workflow was opened, and the
// endpoint screen runs on a later one.
func addModelAfterEndpoint(m *Model, endpointID string) tea.Cmd {
	// The endpoint screen is left: the operator is back in the step
	// they came from.
	m.manageBack()
	// The endpoint list is rebuilt: the new endpoint belongs in it.
	m.manage.items = m.endpointPickerItems()
	m.manage.cursor = m.endpointRowOf(endpointID)
	m.manage.chosen = func(m *Model, item manageItem) tea.Cmd {
		if item.value == manageCreateEndpoint {
			return m.openNewEndpoint(func(m *Model, id string) tea.Cmd {
				return addModelAfterEndpoint(m, id)
			})
		}
		return m.openAddModelForm(item.endpoint)
	}
	return m.openAddModelForm(endpointID)
}

// manageEditModel asks which model to edit, and then for its settings.
func (m *Model) manageEditModel() tea.Cmd {
	m.manageRoot(manageModels)
	m.manage.kind = manageModels
	items := m.modelItems()
	if len(items) == 0 {
		m.manage.notice = "There is no model to edit. Add one with /model add."
		return nil
	}
	m.managePickModel("Choose a model to edit",
		"Enter opens the settings of the chosen model.", items,
		func(m *Model, item manageItem) tea.Cmd { return m.openModelSettings(item) })
	return nil
}

// manageRemoveModel asks which model to remove, and then confirms.
func (m *Model) manageRemoveModel() tea.Cmd {
	m.manageRoot(manageModels)
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
	m.manageRoot(manageModels)
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
	m.manageRoot(manageModels)
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
	m.manageRoot(manageModels)
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
	m.manageRoot(manageEndpoints)
	m.manage.kind = manageEndpoints
	if m.hooks.SaveEndpoint == nil {
		m.manage.notice = "Endpoint management is not wired up."
		return nil
	}
	return m.openNewEndpoint(nil)
}

// manageEditEndpoint asks which endpoint to edit, and then for its
// settings.
func (m *Model) manageEditEndpoint() tea.Cmd {
	m.manageRoot(manageEndpoints)
	m.manage.kind = manageEndpoints
	items := m.endpointItems()
	if len(items) == 0 {
		m.manage.notice = "There is no endpoint to edit. Add one with /endpoint add."
		return nil
	}
	m.managePickModel("Choose an endpoint to edit",
		"Enter opens the settings of the chosen endpoint.", items,
		func(m *Model, item manageItem) tea.Cmd { return m.openEndpointSettings(item) })
	return nil
}

// manageRemoveEndpoint asks which endpoint to remove, and then confirms.
func (m *Model) manageRemoveEndpoint() tea.Cmd {
	m.manageRoot(manageEndpoints)
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

// openAddModelForm asks for the model that goes under the chosen
// endpoint.
//
// The endpoint is shown, not asked for: it was chosen already, and a
// second chance to type it is a second chance to type a URL.
func (m *Model) openAddModelForm(endpointID string) tea.Cmd {
	draft := m.stepDraft()
	fields := []manageField{
		{label: "Model id", hint: "the identifier the provider uses, e.g. deepseek-chat",
			input: namedInput("", draftValue(draft, 0), false)},
		{label: "Label", hint: "the name HAL shows; optional",
			input: namedInput("", draftValue(draft, 1), false)},
	}
	m.managePush()
	m.manage.lines = m.endpointLines(endpointID)
	m.openForm("Add a model to "+endpointID,
		"The model is added to the endpoint. The active model does not change.",
		fields, func(m *Model) tea.Cmd { return m.saveNewModel(endpointID) })
	return nil
}

// openModelSettings opens the settings of one model.
//
// The screen is a menu of the settings, each with its current value
// next to it. A setting is opened, changed and staged; the changes are
// written when the operator saves them, and leaving the screen without
// saving writes nothing.
func (m *Model) openModelSettings(item manageItem) tea.Cmd {
	if m.hooks.ModelDetail == nil {
		m.manage.notice = "Model management is not wired up."
		return nil
	}
	detail, err := m.hooks.ModelDetail(item.endpoint, item.model)
	if err != nil {
		m.manage.notice = "Cannot read the model: " + err.Error()
		return nil
	}
	before := detail
	m.managePush()
	m.manage.screen = manageMenu
	m.manage.title = "Edit model " + item.endpoint + " · " + item.model
	m.manage.message = "Enter opens a setting. Ctrl+S writes the changes. Esc leaves them unwritten."
	m.manage.keys = "Up and down choose · Enter opens · Ctrl+S saves · Esc back"
	m.manage.pending = map[string]manageChange{}
	m.manage.lines = m.modelIdentityLines(item, detail)
	m.manage.actions = []manageAction{
		{key: "L", name: "Label", value: labelValue(detail.Settings.Label),
			what: "the name HAL shows", run: func(m *Model) tea.Cmd { return m.openLabelForm(item, before) }},
		{key: "E", name: "Effort", value: effortDisplay(detail.Settings.Effort, item.model),
			what: "the reasoning level (effective: what the next request will carry)", run: func(m *Model) tea.Cmd { return m.openEffortChoice(item, before) }},
		{key: "O", name: "Output limit", value: outputValue(detail.Settings.MaxOutputTokens),
			what: "the room the answer may use", run: func(m *Model) tea.Cmd { return m.openOutputChoice(item, before) }},
		{key: "C", name: "Context", value: contextValue(detail),
			what: "the context window and where it came from", run: func(m *Model) tea.Cmd { return m.openContextMenu(item, before) }},
		{key: "S", name: "Save", value: "",
			what: "write the changes", run: func(m *Model) tea.Cmd { return m.saveModelSettings(item, before) }},
	}
	m.manage.save = func(m *Model) tea.Cmd { return m.saveModelSettings(item, before) }
	m.manage.notice = ""
	m.manage.cursor = 0
	m.panelOffset = 0
	m.enterManage()
	return nil
}

// openLabelForm asks for the display label of a model.
//
// The field starts empty and the current label is stated in the hint:
// typing a name replaces it, and an empty field keeps it.
func (m *Model) openLabelForm(item manageItem, before ModelDetail) tea.Cmd {
	m.managePush()
	fields := []manageField{{
		label: "Label",
		hint:  currentHint("the name HAL shows", before.Settings.Label),
		input: namedInput("", "", false),
	}}
	m.openForm("Label of "+item.endpoint+" · "+item.model,
		"An empty field keeps the current value.",
		fields, func(m *Model) tea.Cmd {
			value := strings.TrimSpace(m.manageValues()[0])
			if value != "" {
				m.manageSetPending("label", manageChange{value: value})
			}
			m.manageBack()
			return nil
		})
	return nil
}

// openEffortChoice asks which reasoning level the model should use.
//
// The levels are the documented ones, and the model's own default is
// one of the choices: restoring it is an explicit act, never what an
// empty field means.
func (m *Model) openEffortChoice(item manageItem, before ModelDetail) tea.Cmd {
	defaults := provider.DefaultEffortFor(item.model)
	items := []manageItem{{
		name:   "the model's own default",
		detail: effortDefaultDetail(defaults),
		value:  "",
		mark:   markFor(before.Settings.Effort == ""),
	}}
	for _, level := range provider.EffortLevels {
		items = append(items, manageItem{
			name:  level,
			value: level,
			mark:  markFor(before.Settings.Effort == level),
		})
	}
	m.managePush()
	m.openChoice("Effort of "+item.endpoint+" · "+item.model,
		"Enter chooses the level. The default restores the model's own.",
		items, func(m *Model, choice manageItem) tea.Cmd {
			m.manageSetPending("effort", manageChange{value: choice.value, reset: choice.value == ""})
			m.manageBack()
			return nil
		})
	return nil
}

// openOutputChoice asks what the answer may use.
//
// The provider's own limit is one of the choices: restoring it is an
// explicit act, never what an empty field means.
func (m *Model) openOutputChoice(item manageItem, before ModelDetail) tea.Cmd {
	m.managePush()
	m.manage.screen = manageMenu
	m.manage.title = "Output limit of " + item.endpoint + " · " + item.model
	m.manage.message = "Enter chooses how much room the answer may use."
	m.manage.keys = "Up and down choose · Enter runs · Esc back"
	m.manage.lines = nil
	m.manage.actions = []manageAction{
		{key: "P", name: "Provider default", value: "",
			what: "restore the provider's own limit", run: func(m *Model) tea.Cmd {
				m.manageSetPending("output", manageChange{reset: true})
				m.manageBack()
				return nil
			}},
		{key: "T", name: "Type a number", value: limitValue(before.Settings.MaxOutputTokens),
			what: "set an explicit limit in tokens", run: func(m *Model) tea.Cmd {
				return m.openOutputForm(item, before)
			}},
	}
	m.manage.notice = ""
	m.manage.cursor = 0
	m.panelOffset = 0
	m.enterManage()
	return nil
}

// openOutputForm asks for the number of tokens the answer may use.
//
// The field starts empty and the current limit is stated in the hint:
// typing a number replaces it, and an empty field keeps it.
func (m *Model) openOutputForm(item manageItem, before ModelDetail) tea.Cmd {
	m.managePush()
	fields := []manageField{{
		label: "Output limit",
		hint:  currentHint("tokens", limitValue(before.Settings.MaxOutputTokens)),
		input: namedInput("", "", false),
	}}
	m.openForm("Output limit of "+item.endpoint+" · "+item.model,
		"An empty field keeps the current value.",
		fields, func(m *Model) tea.Cmd {
			value := strings.TrimSpace(m.manageValues()[0])
			if value != "" {
				tokens, err := strconv.Atoi(value)
				if err != nil || tokens <= 0 {
					m.manage.notice = "The output limit must be a number of tokens. " +
						"To restore the provider's own default, choose it above."
					return nil
				}
				m.manageSetPending("output", manageChange{value: strconv.Itoa(tokens)})
			}
			m.manageBack()
			return nil
		})
	return nil
}

// openContextMenu opens the context settings of one model.
//
// The window and where it came from are shown above the settings: the
// operator sees the limit before changing it.
func (m *Model) openContextMenu(item manageItem, before ModelDetail) tea.Cmd {
	m.managePush()
	m.manage.screen = manageMenu
	m.manage.title = "Context of " + item.endpoint + " · " + item.model
	m.manage.message = "Enter opens a setting. Esc leaves it as it is."
	m.manage.keys = "Up and down choose · Enter opens · Esc back"
	m.manage.lines = append(append([]string{}, before.Context...), before.Provenance...)
	m.manage.actions = []manageAction{
		{key: "M", name: "Mode", value: before.Settings.ContextMode,
			what: "auto uses the provider's own window, manual uses yours",
			run:  func(m *Model) tea.Cmd { return m.openContextModeChoice(item, before) }},
		{key: "W", name: "Window", value: limitValue(before.Settings.ContextWindow),
			what: "tokens; a window belongs to the manual mode",
			run:  func(m *Model) tea.Cmd { return m.openContextWindowForm(item, before) }},
	}
	m.manage.notice = ""
	m.manage.cursor = 0
	m.panelOffset = 0
	m.enterManage()
	return nil
}

// openContextModeChoice asks whether the window is the provider's or
// the operator's.
func (m *Model) openContextModeChoice(item manageItem, before ModelDetail) tea.Cmd {
	items := []manageItem{
		{name: "auto", detail: "the window the provider reported for this model",
			value: "auto", mark: markFor(before.Settings.ContextMode == "auto")},
		{name: "manual", detail: "the operator's own limit",
			value: "manual", mark: markFor(before.Settings.ContextMode == "manual")},
	}
	m.managePush()
	m.openChoice("Context mode of "+item.endpoint+" · "+item.model,
		"Enter chooses the mode.",
		items, func(m *Model, choice manageItem) tea.Cmd {
			m.manageSetPending("context_mode", manageChange{value: choice.value})
			m.manageBack()
			return nil
		})
	return nil
}

// openContextWindowForm asks for the context window of one model.
//
// The field starts empty and the current window is stated in the hint:
// typing a number replaces it, and an empty field keeps it.
func (m *Model) openContextWindowForm(item manageItem, before ModelDetail) tea.Cmd {
	m.managePush()
	fields := []manageField{{
		label: "Context window",
		hint:  currentHint("tokens", limitValue(before.Settings.ContextWindow)),
		input: namedInput("", "", false),
	}}
	m.openForm("Context window of "+item.endpoint+" · "+item.model,
		"An empty field keeps the current value.",
		fields, func(m *Model) tea.Cmd {
			value := strings.TrimSpace(m.manageValues()[0])
			if value != "" {
				tokens, err := strconv.Atoi(value)
				if err != nil || tokens <= 0 {
					m.manage.notice = "The context window must be a positive number of tokens."
					return nil
				}
				m.manageSetPending("context_window", manageChange{value: strconv.Itoa(tokens)})
			}
			m.manageBack()
			return nil
		})
	return nil
}

// saveModelSettings writes the changes the operator chose.
//
// Only the settings that were changed are sent: the configuration
// handlers change what they are given and keep the rest.
func (m *Model) saveModelSettings(item manageItem, before ModelDetail) tea.Cmd {
	if m.hooks.EditModel == nil {
		m.manage.notice = "Model management is not wired up."
		return nil
	}
	spec, changed := m.stagedModelSpec()
	if !changed {
		m.closeManage("Nothing changed.")
		return nil
	}
	if err := m.hooks.EditModel(item.endpoint, item.model, spec); err != nil {
		m.manage.notice = "Cannot change the model: " + err.Error()
		return nil
	}
	m.refreshEngine()
	m.syncEffortFromHooks()
	m.closeManage("Model " + item.model + " of endpoint " + item.endpoint + " updated.")
	return nil
}

// syncEffortFromHooks re-reads the active reasoning level after a change
// that may have touched it.
//
// The engine is rebuilt with the stored settings, and the footer must
// show the same effective level the next request will carry. An
// already-running request keeps the provider it started with: only the
// next request uses the new settings.
func (m *Model) syncEffortFromHooks() {
	if m.hooks.Effort == nil {
		return
	}
	level, supported := m.hooks.Effort()
	m.SetEffort(level, supported)
}

// stagedModelSpec is the change the operator chose, as the configuration
// handlers take it.
//
// A setting that was not touched is not in it, and a setting that was
// restored to its default says so with its own flag: an empty value
// would be read as "unchanged", and the reset would be lost.
func (m *Model) stagedModelSpec() (ModelSpec, bool) {
	var spec ModelSpec
	changed := false
	if c, ok := m.manage.pending["label"]; ok {
		spec.Label = c.value
		changed = true
	}
	if c, ok := m.manage.pending["effort"]; ok {
		if c.reset {
			spec.ResetEffort = true
		} else {
			spec.Effort = c.value
		}
		changed = true
	}
	if c, ok := m.manage.pending["output"]; ok {
		if c.reset {
			spec.ResetOutputTokens = true
		} else {
			tokens, err := strconv.Atoi(c.value)
			if err == nil && tokens > 0 {
				spec.MaxOutputTokens = tokens
			}
		}
		changed = true
	}
	if c, ok := m.manage.pending["context_mode"]; ok {
		spec.ContextMode = c.value
		changed = true
	}
	if c, ok := m.manage.pending["context_window"]; ok {
		tokens, err := strconv.Atoi(c.value)
		if err == nil && tokens > 0 {
			spec.ContextWindow = tokens
			// A window belongs to the manual mode: the two are set
			// together, so the record never says "the provider's own
			// window" and names one at the same time.
			if _, ok := m.manage.pending["context_mode"]; !ok && spec.ContextMode == "" {
				spec.ContextMode = "manual"
			}
		}
		changed = true
	}
	return spec, changed
}

// manageSetPending records a change the operator chose.
//
// The changes belong to the step that asked for them, so a change made
// in a child screen is still there when the operator comes back to the
// step that collects them.
func (m *Model) manageSetPending(key string, change manageChange) {
	if m.manage.pending == nil {
		m.manage.pending = map[string]manageChange{}
	}
	m.manage.pending[key] = change
}

// manageSetValue changes the value a row of the step below shows.
//
// The rows of a step are shared by the frame that was saved and the one
// that is on screen, so a child screen can change what the step below
// shows without knowing anything about it.
func (m *Model) manageSetValue(key, value string) {
	if len(m.manageStack) == 0 {
		return
	}
	frame := &m.manageStack[len(m.manageStack)-1]
	for i := range frame.actions {
		if frame.actions[i].key == key {
			frame.actions[i].value = value
			return
		}
	}
}

// manageValue reads the value a row of the current step shows.
func (m *Model) manageValue(key string) string {
	for _, action := range m.manage.actions {
		if action.key == key {
			return action.value
		}
	}
	return ""
}

// manageSetValueBelow changes the value a row of the step below the one
// on screen shows.
//
// Some workflows go through a screen that is only a way to reach a
// value: the list of the variables an endpoint may point at, for
// instance. The value belongs to the screen above that one, and this
// is how the child screen reaches it.
func (m *Model) manageSetValueBelow(key, value string) {
	if len(m.manageStack) < 2 {
		m.manageSetValue(key, value)
		return
	}
	frame := &m.manageStack[len(m.manageStack)-2]
	for i := range frame.actions {
		if frame.actions[i].key == key {
			frame.actions[i].value = value
			return
		}
	}
}

// stepDraft is what the operator typed in the form of this step.
//
// The values belong to the step, not to the form that is drawn: a form
// that is left and come back to still holds them.
func (m *Model) stepDraft() []string {
	return m.manage.draft
}

// draftValue is one value of a draft.
func draftValue(draft []string, index int) string {
	if index < 0 || index >= len(draft) {
		return ""
	}
	return draft[index]
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
		{label: "Mode", hint: currentHint("auto uses the provider's own window, manual uses yours", detail.Settings.ContextMode),
			input: namedInput("", "", false)},
		{label: "Window", hint: currentHint("tokens; used in manual mode", limitValue(detail.Settings.ContextWindow)),
			input: namedInput("", "", false)},
	}
	m.manage.lines = append(append([]string{}, detail.Context...), detail.Provenance...)
	m.openForm("Context of "+item.endpoint+" · "+item.model,
		"An empty field keeps the current value.", fields,
		func(m *Model) tea.Cmd { return m.saveModelContext(item, detail) })
	return nil
}

// saveNewModel stores the model the operator described.
func (m *Model) saveNewModel(endpointID string) tea.Cmd {
	values := m.manageValues()
	id, label := values[0], values[1]
	if strings.TrimSpace(id) == "" {
		m.manage.notice = "The model id is required."
		return nil
	}
	// The endpoint is checked before anything is written: a model is
	// never attached to an endpoint that does not exist.
	if !m.endpointKnown(endpointID) {
		m.manage.notice = m.unknownEndpointNotice(endpointID)
		return nil
	}
	if m.hooks.AddModel == nil {
		m.manage.notice = "Model management is not wired up."
		return nil
	}
	if err := m.hooks.AddModel(endpointID, ModelSpec{ID: id, Label: label}); err != nil {
		m.manage.notice = "Cannot add the model: " + err.Error()
		return nil
	}
	m.refreshEngine()
	m.closeManage("Added model " + id + " to endpoint " + endpointID + ". The active model is unchanged.")
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
	m.syncEffortFromHooks()
	m.closeManage("Context limit for " + item.model + ": " + contextSummary(spec, before))
	return nil
}

// confirmModelRemoval asks before a model is removed.
//
// The confirmation is a step of its own: Esc keeps the model and
// returns to the list it came from.
func (m *Model) confirmModelRemoval(item manageItem) tea.Cmd {
	m.managePush()
	m.enterManage()
	m.manage.screen = manageConfirm
	m.manage.title = "Remove model " + item.endpoint + " · " + item.model + "?"
	m.manage.target = item.model
	m.manage.parent = item.endpoint
	m.manage.replacement = item.mark == "active"
	if item.mark == "active" {
		m.manage.message = "This is the active model. Choose the model to use instead: the removal and the switch happen together."
		return nil
	}
	m.manage.message = "The model is removed from endpoint " + item.endpoint + ". The active model does not change."
	return nil
}

// confirmEndpointRemoval asks before an endpoint is removed.
//
// The confirmation is a step of its own: Esc keeps the endpoint and
// returns to the list it came from.
func (m *Model) confirmEndpointRemoval(item manageItem) tea.Cmd {
	m.managePush()
	m.enterManage()
	m.manage.screen = manageConfirm
	m.manage.title = "Remove endpoint " + item.endpoint + "?"
	m.manage.target = item.endpoint
	m.manage.parent = ""
	m.manage.replacement = item.mark == "active"
	if item.mark == "active" {
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
	m.managePush()
	m.manage.screen = managePick
	m.manage.title = title
	m.manage.message = message
	m.manage.items = items
	m.manage.cursor = 0
	m.manage.chosen = then
	m.manage.notice = ""
	m.manage.actions = nil
	m.panelOffset = 0
	m.enterManage()
}

// openEndpointPicker asks which endpoint to use.
//
// The rows carry the endpoint's id, its protocol and its address: the
// three things that are easy to confuse. The last row creates a new
// endpoint, so the operator never has to leave the workflow to get one.
func (m *Model) openEndpointPicker(title, message string,
	then func(*Model, manageItem) tea.Cmd) {
	m.manage.screen = managePick
	m.manage.title = title
	m.manage.message = message
	m.manage.items = m.endpointPickerItems()
	m.manage.cursor = 0
	m.manage.chosen = then
	m.manage.notice = ""
	m.manage.actions = nil
	m.panelOffset = 0
	m.enterManage()
}

// endpointPickerItems are the endpoints HAL knows, and the row that
// creates one.
//
// The last row is always there: an operator who needs a new endpoint
// should not have to remember the command for it and come back.
func (m *Model) endpointPickerItems() []manageItem {
	items := m.endpointItems()
	detail := "add an endpoint and come back here"
	if len(items) == 0 {
		detail = "there is no endpoint yet — HAL needs one before a model can be added"
	}
	return append(items, manageItem{
		name:   "Create an endpoint",
		detail: detail,
		value:  manageCreateEndpoint,
	})
}

// endpointRowOf is the row of an endpoint in the picker.
func (m *Model) endpointRowOf(endpointID string) int {
	for i, item := range m.manage.items {
		if item.endpoint == endpointID {
			return i
		}
	}
	return 0
}

// openChoice opens a list of choices.
//
// The list is a step of its own: Esc leaves it and returns to the step
// that asked for the choice.
func (m *Model) openChoice(title, message string, items []manageItem,
	then func(*Model, manageItem) tea.Cmd) {
	m.manage.screen = managePick
	m.manage.title = title
	m.manage.message = message
	m.manage.items = items
	m.manage.cursor = 0
	m.manage.chosen = then
	m.manage.notice = ""
	m.manage.actions = nil
	m.manage.keys = ""
	m.manage.lines = nil
	m.panelOffset = 0
	m.enterManage()
}

// openForm opens a form with the given fields.
//
// The form is a step of its own: Esc leaves it and returns to the step
// that asked for the values.
func (m *Model) openForm(title, message string, fields []manageField, save func(*Model) tea.Cmd) {
	m.manage.screen = manageForm
	m.manage.title = title
	m.manage.message = message
	m.manage.fields = fields
	m.manage.focus = 0
	m.manage.save = save
	m.manage.notice = ""
	m.manage.chosen = nil
	m.manage.actions = nil
	m.manage.keys = ""
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

// manageBack returns to the previous logical screen.
//
// Nothing is changed on the way back: a cancelled form keeps the
// configuration exactly as it was. What the operator typed is kept in
// the step they came from, so a form that is left and come back to
// still holds it. A masked key is never kept: it is cleared on leave,
// whether the operator saved it or cancelled it.
//
// At the top of the hierarchy there is no step below: Esc leaves the
// management interface and returns to the conversation.
func (m *Model) manageBack() {
	// A masked key never survives in a draft, a field or a notice.
	if m.manageContainsSecret() {
		m.scrubManageFields()
		m.manage.endpointCredential = ""
		// A cancelled key entry must not clear an existing credential:
		// only an explicit save or an explicit removal does.
		if len(m.manageStack) > 0 {
			m.manageStack[len(m.manageStack)-1].draft = nil
		}
	} else if m.manage.screen == manageForm && len(m.manageStack) > 0 {
		// The values of a form belong to the step below it.
		m.manageStack[len(m.manageStack)-1].draft = m.manageValues()
	}
	if !m.managePop() {
		m.closeManage("")
		// Leaving the interface clears any typed key that was never
		// saved: it must not survive in memory longer than it must.
		m.manage.endpointCredential = ""
		return
	}
	m.manage.notice = ""
	m.panelOffset = 0
	m.refreshPendingRows()
	m.sizeManageFields()
}

// manageContainsSecret reports whether the form on screen holds a
// masked key. The value itself is never read: only whether a field is
// masked.
func (m *Model) manageContainsSecret() bool {
	if m.manage.screen != manageForm {
		return false
	}
	for _, field := range m.manage.fields {
		if field.input.EchoMode == textinput.EchoPassword {
			return true
		}
	}
	return false
}

// refreshPendingRows shows the changes the operator has staged.
//
// The row of a setting shows what the setting will be once the changes
// are written, so the operator sees the result of their work before
// they save it.
func (m *Model) refreshPendingRows() {
	if len(m.manage.pending) == 0 {
		return
	}
	for i := range m.manage.actions {
		action := &m.manage.actions[i]
		switch action.key {
		case "L":
			if change, ok := m.manage.pending["label"]; ok {
				action.value = labelValue(change.value)
			}
		case "E":
			if change, ok := m.manage.pending["effort"]; ok {
				if change.reset {
					action.value = "the model's default"
				} else {
					action.value = change.value
				}
			}
		case "O":
			if change, ok := m.manage.pending["output"]; ok {
				if change.reset {
					action.value = "the provider's default"
				} else {
					action.value = change.value + " tokens"
				}
			}
		case "C":
			if change, ok := m.manage.pending["context_mode"]; ok {
				action.value = change.value
			} else if change, ok := m.manage.pending["context_window"]; ok {
				action.value = "manual, " + change.value + " tokens"
			}
		}
	}
}

// closeManage leaves the management interface.
func (m *Model) closeManage(notice string) {
	m.manage = manageState{}
	m.manageStack = nil
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
	row := 2 + len(m.manage.lines) + m.manage.cursor
	total := 2 + len(m.manage.lines)
	switch m.manage.screen {
	case manageMenu:
		total += len(m.manage.actions) + 3
		if m.manage.kind == manageModels {
			total++
		}
	case managePick:
		total += len(m.manage.items)
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
			mark:     markActive(info.Active),
		})
	}
	return out
}

// endpointItems lists the configured endpoints.
//
// Every row names the endpoint's id and its address: they are different
// things, and the operator has to be able to tell them apart.
func (m *Model) endpointItems() []manageItem {
	var out []manageItem
	if m.hooks.Endpoints == nil {
		return nil
	}
	for _, endpoint := range m.hooks.Endpoints() {
		detail := "id " + endpoint.ID
		if endpoint.Protocol != "" {
			detail += " · " + endpoint.Protocol
		}
		if endpoint.BaseURL != "" {
			detail += " · " + endpoint.BaseURL
		}
		out = append(out, manageItem{
			name:     endpoint.Name,
			detail:   detail,
			endpoint: endpoint.ID,
			mark:     markActive(endpoint.Active),
		})
	}
	return out
}

// endpointLines describe one endpoint for a form.
//
// The id, the address and the protocol are three different things, and
// the form shows all three: a URL typed where an id belongs is the
// mistake this screen exists to prevent.
func (m *Model) endpointLines(endpointID string) []string {
	lines := []string{}
	if m.hooks.Endpoints != nil {
		for _, endpoint := range m.hooks.Endpoints() {
			if endpoint.ID != endpointID {
				continue
			}
			lines = append(lines, "  endpoint      "+endpoint.ID+"  ("+endpoint.Name+")")
			if endpoint.Protocol != "" {
				lines = append(lines, "  protocol      "+endpoint.Protocol)
			}
			if endpoint.BaseURL != "" {
				lines = append(lines, "  base url      "+endpoint.BaseURL)
			}
			return lines
		}
	}
	return append(lines, "  endpoint      "+endpointID)
}

// modelIdentityLines say what a model is and what may not be changed.
//
// The model id and the endpoint are the model's identity, and they never
// change. A model that belongs to the wrong endpoint is moved by adding
// it to the right one, and the screen says so instead of pretending an
// ordinary edit can do it.
func (m *Model) modelIdentityLines(item manageItem, detail ModelDetail) []string {
	lines := []string{
		"  " + padTo("id", 16) + item.model,
		"  " + padTo("endpoint", 16) + item.endpoint,
		"  " + padTo("identity", 16) + "the model id and its endpoint never change — to move a model, add it to the other endpoint",
	}
	return append(lines, detail.Provenance...)
}

// endpointKnown reports whether HAL knows an endpoint with this id.
//
// A configuration that cannot be listed is not refused here: the
// handler that stores the change is the one that decides whether the
// endpoint exists.
func (m *Model) endpointKnown(id string) bool {
	if m.hooks.Endpoints == nil {
		return true
	}
	for _, endpoint := range m.hooks.Endpoints() {
		if endpoint.ID == id {
			return true
		}
	}
	return false
}

// endpointIDs are the ids of the endpoints HAL knows.
func (m *Model) endpointIDs() []string {
	var out []string
	if m.hooks.Endpoints == nil {
		return out
	}
	for _, endpoint := range m.hooks.Endpoints() {
		out = append(out, endpoint.ID)
	}
	return out
}

// unknownEndpointNotice explains that HAL does not know this endpoint.
//
// A URL typed where an endpoint id belongs is the common mistake, and
// the message says so and names the endpoints that do exist.
func (m *Model) unknownEndpointNotice(value string) string {
	known := m.endpointIDs()
	if looksLikeURL(value) {
		if len(known) == 0 {
			return fmt.Sprintf("%q is an API URL, not an endpoint id, and no endpoint is configured yet. Add one with /endpoint add.", value)
		}
		return fmt.Sprintf("%q is an API URL, not an endpoint id. The endpoints HAL knows are: %s.",
			value, strings.Join(known, ", "))
	}
	if len(known) == 0 {
		return fmt.Sprintf("There is no endpoint with the id %q, and none is configured yet. Add one with /endpoint add.", value)
	}
	return fmt.Sprintf("There is no endpoint with the id %q. The endpoints HAL knows are: %s.",
		value, strings.Join(known, ", "))
}

// looksLikeURL reports whether a value is an address rather than a name.
func looksLikeURL(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	return strings.Contains(value, "://") ||
		strings.HasPrefix(strings.ToLower(value), "http:") ||
		strings.HasPrefix(strings.ToLower(value), "https:")
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

// labelValue is a display label as the operator sees it.
func labelValue(label string) string {
	if strings.TrimSpace(label) == "" {
		return "(none)"
	}
	return label
}

// effortValue is the reasoning level as the operator sees it.
//
// An unnamed effort is the model's own default, and the screen says so
// instead of showing nothing. When the default is known, it is named:
// the footer shows the effective level, and the editor shows the same
// number with its provenance.
func effortValue(effort string) string {
	if strings.TrimSpace(effort) == "" {
		return "the model's default"
	}
	return effort
}

// effortDisplay is the effective reasoning level as the operator sees
// it: the stored level when one is set, otherwise the model's own
// default with its provenance, so the editor and the footer agree.
func effortDisplay(stored, modelID string) string {
	if strings.TrimSpace(stored) != "" {
		return stored
	}
	if defaults := provider.DefaultEffortFor(modelID); defaults != "" {
		return "the model's default (" + defaults + ")"
	}
	return "the model's default"
}

// effortDefaultDetail says what the model's own default is.
func effortDefaultDetail(defaults string) string {
	if defaults == "" {
		return "this model has no effort control of its own"
	}
	return "HAL sends " + defaults + " for this model"
}

// outputValue is the output limit as the operator sees it.
//
// A zero limit is the provider's own default, and the screen says so
// instead of showing nothing.
func outputValue(tokens int) string {
	if tokens <= 0 {
		return "the provider's default"
	}
	return strconv.Itoa(tokens) + " tokens"
}

// contextValue is the context limit as the operator sees it.
func contextValue(detail ModelDetail) string {
	mode := detail.Settings.ContextMode
	if mode == "" {
		mode = "auto"
	}
	if mode == "manual" && detail.Settings.ContextWindow > 0 {
		return "manual, " + strconv.Itoa(detail.Settings.ContextWindow) + " tokens"
	}
	return mode
}

// manageHasCommands reports whether a menu's actions are also commands.
//
// The top-level menus are; the menus inside a workflow are not, and
// saying otherwise would name a command that does not exist.
func manageHasCommands(actions []manageAction) bool {
	for _, action := range actions {
		if action.command != "" {
			return true
		}
	}
	return false
}

// markFor says whether a row is the setting that is in force.
func markFor(current bool) string {
	if current {
		return "current"
	}
	return ""
}

// markActive says whether a row is the model or endpoint in use.
func markActive(active bool) string {
	if active {
		return "active"
	}
	return ""
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

// manageValueWidth is the width of the column the values are shown in.
//
// The column is as wide as the longest value it holds, so a value never
// runs into the description that follows it. The description keeps a
// share of the line of its own, and a terminal too narrow for both
// gives the room to the value.
func (m Model) manageValueWidth() int {
	width := 0
	for _, action := range m.manage.actions {
		if w := lipgloss.Width(oneLine(action.value)); w > width {
			width = w
		}
	}
	width += 2
	room := m.width - 24 - 30
	if room < 12 {
		room = 12
	}
	if width > room {
		width = room
	}
	return width
}

// clipTo shortens a text to a width.
//
// A value that does not fit is cut with an ellipsis: the operator sees
// that there is more of it, and the row keeps its shape.
func clipTo(text string, width int) string {
	if width < 2 {
		return ""
	}
	if lipgloss.Width(text) <= width {
		return text
	}
	runes := []rune(text)
	for len(runes) > 0 && lipgloss.Width(string(runes))+1 > width {
		runes = runes[:len(runes)-1]
	}
	return string(runes) + "…"
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
		width := m.manageValueWidth()
		for i, action := range m.manage.actions {
			marker := "  "
			if i == m.manage.cursor {
				marker = "▸ "
			}
			line := marker + action.key + "  " + padTo(oneLine(action.name), 19)
			value := oneLine(action.value)
			if value == "" {
				value = oneLine(action.empty)
			}
			if value != "" {
				line += padTo(clipTo(value, width), width)
			}
			what := action.what
			if action.whatFn != nil {
				what = action.whatFn(action.value)
			}
			line += oneLine(what)
			if i == m.manage.cursor {
				line = st.active.Render(line)
			}
			rows = append(rows, line)
		}
		if manageHasCommands(m.manage.actions) {
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
			if item.mark != "" {
				line += " (" + item.mark + ")"
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
	if m.manage.keys != "" {
		return m.manage.keys
	}
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
