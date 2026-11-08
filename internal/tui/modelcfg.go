package tui

import (
	"fmt"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
)

// The endpoint and model management commands.
//
// They follow the same style as the rest of the interface: a slash command
// with arguments, a notice that says what happened, and a panel that shows
// a list. Nothing here writes a file by itself: every change goes through
// a hook, and the application validates it before it is stored.
//
// No model-facing tool reaches any of this. A configuration change is an
// operator action, and the model can neither make one nor cause one as a
// side effect of another task.

// modelCfgUsage is the usage line of the /model command.
const modelCfgUsage = "/model <subcommand> [model]"

// endpointCfgUsage is the usage line of the /endpoint command.
const endpointCfgUsage = "/endpoint <subcommand> [id]"

// modelCommand runs the /model command family.
func (m *Model) modelCommand(args []string) tea.Cmd {
	if len(args) == 0 {
		m.enterPicker()
		return nil
	}
	switch strings.ToLower(args[0]) {
	case "list":
		return m.showEndpoints()
	case "add":
		return m.addModel(args[1:])
	case "edit":
		return m.editModel(args[1:])
	case "remove", "rm", "delete":
		return m.removeModel(args[1:])
	case "use", "select":
		return m.selectModel(args[1:])
	case "show", "info":
		return m.showModel(args[1:])
	case "context", "ctx":
		return m.modelContext(args[1:])
	case "discover", "models":
		return m.discoverModels(args[1:])
	default:
		m.notice = "Usage: " + modelCfgUsage
		return nil
	}
}

// endpointCommand runs the /endpoint command family.
func (m *Model) endpointCommand(args []string) tea.Cmd {
	if len(args) == 0 {
		return m.showEndpoints()
	}
	switch strings.ToLower(args[0]) {
	case "list":
		return m.showEndpoints()
	case "add":
		return m.addEndpoint(args[1:])
	case "edit":
		return m.editEndpoint(args[1:])
	case "remove", "rm", "delete":
		return m.removeEndpoint(args[1:])
	default:
		m.notice = "Usage: " + endpointCfgUsage
		return nil
	}
}

// showEndpoints lists the endpoints and their models in the panel.
func (m *Model) showEndpoints() tea.Cmd {
	if m.hooks.Endpoints == nil {
		m.notice = "Endpoint management is not wired up."
		return nil
	}
	endpoints := m.hooks.Endpoints()
	lines := []string{}
	if len(endpoints) == 0 {
		lines = append(lines, "  (no endpoints configured — add one with /endpoint add)")
	}
	for _, e := range endpoints {
		marker := "  "
		if e.Active {
			marker = "▸ "
		}
		lines = append(lines, fmt.Sprintf("%s%s · %s · %s", marker, e.Name, e.Protocol, e.BaseURL))
		lines = append(lines, fmt.Sprintf("    id %s · wire %s · credential %s", e.ID, wireLabel(e.WireProtocol), e.Credential))
		if e.Review != "" {
			lines = append(lines, "    needs review: "+e.Review)
		}
		if len(e.Models) == 0 {
			lines = append(lines, "    (no models)")
		}
		for _, model := range e.Models {
			state := ""
			if model.Active {
				state = " (active)"
			}
			lines = append(lines, fmt.Sprintf("    %s%s", model.ID, state))
			if model.Review != "" {
				lines = append(lines, "      needs review: "+model.Review)
			}
		}
	}
	m.openModelCfg("Endpoints", lines)
	return nil
}

// refreshEngine adopts the engine the current configuration describes.
//
// A change of endpoint, credential or model settings changes the
// provider itself. The interface keeps the engine it was given at
// startup, so it has to be told about the new one: an interface that
// kept the old engine would keep talking to the old endpoint.
func (m *Model) refreshEngine() {
	if m.hooks.RefreshEngine == nil {
		return
	}
	if eng := m.hooks.RefreshEngine(); eng != nil {
		m.engine = eng
	}
}

// addModel stores a new model under one endpoint.
//
// Adding a model never changes the active model: the operator selects one
// explicitly with /model use.
func (m *Model) addModel(args []string) tea.Cmd {
	if m.hooks.AddModel == nil {
		m.notice = "Model management is not wired up."
		return nil
	}
	endpoint, id, rest, err := modelArgs(args, "add <endpoint> <model-id> [label]")
	if err != nil {
		m.notice = err.Error()
		return nil
	}
	spec := ModelSpec{ID: id, Label: strings.Join(rest, " ")}
	if err := m.hooks.AddModel(endpoint, spec); err != nil {
		m.notice = "Cannot add the model: " + err.Error()
		return nil
	}
	m.refreshEngine()
	m.notice = fmt.Sprintf("Added model %s to endpoint %s. The active model is unchanged.", id, endpoint)
	return nil
}

// editModel changes one model's settings.
func (m *Model) editModel(args []string) tea.Cmd {
	if m.hooks.EditModel == nil {
		m.notice = "Model management is not wired up."
		return nil
	}
	endpoint, id, rest, err := modelArgs(args, "edit <endpoint> <model-id> <key=value>…")
	if err != nil {
		m.notice = err.Error()
		return nil
	}
	if len(rest) == 0 {
		m.notice = "Usage: /model edit <endpoint> <model-id> <key=value>…"
		return nil
	}
	spec, err := modelSpecFromPairs(rest)
	if err != nil {
		m.notice = err.Error()
		return nil
	}
	spec.ID = id
	if err := m.hooks.EditModel(endpoint, id, spec); err != nil {
		m.notice = "Cannot change the model: " + err.Error()
		return nil
	}
	m.refreshEngine()
	m.notice = fmt.Sprintf("Model %s of endpoint %s updated.", id, endpoint)
	return nil
}

// removeModel deletes one model.
//
// The active model is never removed silently: the operator either names a
// replacement or is told exactly what to do.
func (m *Model) removeModel(args []string) tea.Cmd {
	if m.hooks.DeleteModel == nil {
		m.notice = "Model management is not wired up."
		return nil
	}
	endpoint, id, rest, err := modelArgs(args, "remove <endpoint> <model-id> [--use endpoint/model]")
	if err != nil {
		m.notice = err.Error()
		return nil
	}
	replacement, err := replacementFromPairs(rest)
	if err != nil {
		m.notice = err.Error()
		return nil
	}
	if err := m.hooks.DeleteModel(endpoint, id, replacement); err != nil {
		m.notice = "Cannot remove the model: " + err.Error()
		return nil
	}
	m.refreshEngine()
	m.notice = fmt.Sprintf("Removed model %s from endpoint %s.", id, endpoint)
	return nil
}

// selectModel makes one model the active one.
func (m *Model) selectModel(args []string) tea.Cmd {
	if m.hooks.SwitchModel == nil {
		m.notice = "Model management is not wired up."
		return nil
	}
	endpoint, id, _, err := modelArgs(args, "use <endpoint> <model-id>")
	if err != nil {
		m.notice = err.Error()
		return nil
	}
	eng, err := m.hooks.SwitchModel(endpoint, id)
	if err != nil {
		m.notice = "Cannot switch: " + err.Error()
		return nil
	}
	if eng != nil {
		m.engine = eng
	}
	m.model = id
	if m.hooks.Effort != nil {
		m.SetEffort(m.hooks.Effort())
	}
	m.notice = fmt.Sprintf("Model: %s (%s) — applies to the next request.", id, endpoint)
	return nil
}

// showModel inspects one model's settings.
func (m *Model) showModel(args []string) tea.Cmd {
	if m.hooks.ModelDetails == nil {
		m.notice = "Model management is not wired up."
		return nil
	}
	endpoint, id, _, err := modelArgs(args, "show <endpoint> <model-id>")
	if err != nil {
		m.notice = err.Error()
		return nil
	}
	lines, err := m.hooks.ModelDetails(endpoint, id)
	if err != nil {
		m.notice = "Cannot show the model: " + err.Error()
		return nil
	}
	m.openModelCfg("Model "+id, lines)
	return nil
}

// modelContext configures the context limits of one model.
func (m *Model) modelContext(args []string) tea.Cmd {
	if m.hooks.EditModel == nil {
		m.notice = "Model management is not wired up."
		return nil
	}
	endpoint, id, rest, err := modelArgs(args, "context <endpoint> <model-id> auto|manual [tokens]")
	if err != nil {
		m.notice = err.Error()
		return nil
	}
	if len(rest) == 0 {
		m.notice = "Usage: /model context <endpoint> <model-id> auto|manual [tokens]"
		return nil
	}
	mode := strings.ToLower(rest[0])
	switch mode {
	case "auto", "manual":
	default:
		m.notice = "Usage: /model context <endpoint> <model-id> auto|manual [tokens]"
		return nil
	}
	spec := ModelSpec{ContextMode: mode}
	if len(rest) > 1 {
		tokens, err := strconv.Atoi(rest[1])
		if err != nil || tokens <= 0 {
			m.notice = "The context window must be a positive number of tokens."
			return nil
		}
		spec.ContextWindow = tokens
	}
	if mode == "auto" {
		spec.ContextWindow = 0
	}
	if err := m.hooks.EditModel(endpoint, id, spec); err != nil {
		m.notice = "Cannot change the context limits: " + err.Error()
		return nil
	}
	if mode == "manual" {
		m.notice = fmt.Sprintf("Context limit for %s: manual, %d tokens.", id, spec.ContextWindow)
		return nil
	}
	m.notice = fmt.Sprintf("Context limit for %s: automatic.", id)
	return nil
}

// discoverModels probes the provider's catalogue.
//
// Discovery confirms identifiers and reports which ones are not configured
// yet. It never adds a model and never moves one between endpoints.
func (m *Model) discoverModels(args []string) tea.Cmd {
	if m.hooks.Discover == nil {
		m.notice = "Model discovery is not wired up."
		return nil
	}
	if len(args) == 0 {
		m.notice = "Usage: /model discover <endpoint>"
		return nil
	}
	endpoint := args[0]
	ids, err := m.hooks.Discover(endpoint)
	if err != nil {
		m.notice = "Cannot read the catalogue: " + err.Error()
		return nil
	}
	configured := map[string]bool{}
	if m.hooks.Endpoints != nil {
		for _, e := range m.hooks.Endpoints() {
			if e.ID != endpoint {
				continue
			}
			for _, model := range e.Models {
				configured[model.ID] = true
			}
		}
	}
	lines := []string{fmt.Sprintf("  %s reports %d model(s):", endpoint, len(ids))}
	missing := 0
	for _, id := range ids {
		if configured[id] {
			lines = append(lines, "    "+id+" · configured")
			continue
		}
		missing++
		lines = append(lines, "    "+id+" · not configured — /model add "+endpoint+" "+id)
	}
	if missing == 0 {
		lines = append(lines, "  every reported model is configured")
	}
	m.openModelCfg("Catalogue of "+endpoint, lines)
	return nil
}

// addEndpoint stores a new endpoint.
func (m *Model) addEndpoint(args []string) tea.Cmd {
	if m.hooks.SaveEndpoint == nil {
		m.notice = "Endpoint management is not wired up."
		return nil
	}
	if len(args) < 4 {
		m.notice = "Usage: /endpoint add <id> <name> <protocol> <url> [credential-env]"
		return nil
	}
	spec := EndpointSpec{
		ID:            args[0],
		Name:          args[1],
		Protocol:      args[2],
		BaseURL:       args[3],
		Discovery:     true,
		WireProtocol:  "",
		CredentialEnv: "",
	}
	if len(args) > 4 {
		spec.CredentialEnv = args[4]
	}
	if err := m.hooks.SaveEndpoint(spec); err != nil {
		m.notice = "Cannot add the endpoint: " + err.Error()
		return nil
	}
	m.refreshEngine()
	m.notice = fmt.Sprintf("Added endpoint %s (%s).", spec.ID, spec.Name)
	return nil
}

// editEndpoint changes one endpoint's settings.
func (m *Model) editEndpoint(args []string) tea.Cmd {
	if m.hooks.SaveEndpoint == nil {
		m.notice = "Endpoint management is not wired up."
		return nil
	}
	if len(args) < 1 {
		m.notice = "Usage: /endpoint edit <id> <key=value>…"
		return nil
	}
	if len(args) < 2 {
		m.notice = "Usage: /endpoint edit <id> <key=value>…"
		return nil
	}
	spec, err := endpointSpecFromPairs(args[1:])
	if err != nil {
		m.notice = err.Error()
		return nil
	}
	spec.ID = args[0]
	if err := m.hooks.SaveEndpoint(spec); err != nil {
		m.notice = "Cannot change the endpoint: " + err.Error()
		return nil
	}
	m.refreshEngine()
	m.notice = fmt.Sprintf("Endpoint %s updated.", spec.ID)
	return nil
}

// removeEndpoint deletes one endpoint and its models.
func (m *Model) removeEndpoint(args []string) tea.Cmd {
	if m.hooks.DeleteEndpoint == nil {
		m.notice = "Endpoint management is not wired up."
		return nil
	}
	if len(args) == 0 {
		m.notice = "Usage: /endpoint remove <id> [--use endpoint/model]"
		return nil
	}
	replacement, err := replacementFromPairs(args[1:])
	if err != nil {
		m.notice = err.Error()
		return nil
	}
	if err := m.hooks.DeleteEndpoint(args[0], replacement); err != nil {
		m.notice = "Cannot remove the endpoint: " + err.Error()
		return nil
	}
	m.refreshEngine()
	m.notice = fmt.Sprintf("Removed endpoint %s.", args[0])
	return nil
}

// openModelCfg opens the management panel with the given lines.
func (m *Model) openModelCfg(title string, lines []string) {
	m.modelCfgTitle = title
	m.modelCfgLines = lines
	m.panelOffset = 0
	m.view = viewModels
}

// updateModelCfg handles the management panel: it scrolls and closes.
func (m Model) updateModelCfg(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.ready = true
		return m, nil
	case tea.KeyPressMsg:
		switch msg.String() {
		case keyQuit, keyQuit2:
			return m, tea.Quit
		case "esc":
			m.view = viewChat
			m.modelCfgLines = nil
			m.modelCfgTitle = ""
			return m, nil
		case "up", "shift+tab":
			if m.panelOffset > 0 {
				m.panelOffset--
			}
			return m, nil
		case "down", "tab", "pgdown":
			m.panelOffset++
			return m, nil
		}
	}
	return m, nil
}

// modelCfgRows renders the management panel.
//
// Every line is flattened before it is drawn. A model identifier and an
// endpoint name can come from the provider, and a hostile endpoint must
// not be able to move the cursor, clear the screen or forge an extra row
// with control characters in its own catalogue.
func (m Model) modelCfgRows() []string {
	st := m.theme.styles()
	title := m.modelCfgTitle
	if title == "" {
		title = "Endpoints"
	}
	rows := []string{oneLine(title) + " (Esc back)", ""}
	for _, line := range m.modelCfgLines {
		rows = append(rows, oneLine(line))
	}
	rows = append(rows, "", st.footer.Render("  Up and down scroll · Esc back"))
	return rows
}

// modelArgs splits the arguments of a model command into an endpoint, a
// model identifier and the rest.
func modelArgs(args []string, usage string) (endpoint, id string, rest []string, err error) {
	if len(args) < 2 {
		return "", "", nil, fmt.Errorf("Usage: /model %s", usage)
	}
	return args[0], args[1], args[2:], nil
}

// replacementFromPairs reads the optional "--use" replacement.
//
// The replacement names a model, either as one word ("endpoint/model") or
// as two ("--use endpoint model").
func replacementFromPairs(args []string) (string, error) {
	for i := 0; i < len(args); i++ {
		if args[i] != "--use" {
			continue
		}
		if i+1 >= len(args) {
			return "", fmt.Errorf("The replacement must name a model as \"endpoint/model\".")
		}
		value := args[i+1]
		if strings.Contains(value, "/") {
			return value, nil
		}
		if i+2 < len(args) && !strings.HasPrefix(args[i+2], "-") {
			return value + "/" + args[i+2], nil
		}
		return "", fmt.Errorf("The replacement must name a model as \"endpoint/model\".")
	}
	return "", nil
}

// modelSpecFromPairs reads "key=value" arguments into a model spec.
//
// A value that contains a space may be quoted: name="DeepSeek EU" is one
// setting, not two.
func modelSpecFromPairs(args []string) (ModelSpec, error) {
	var spec ModelSpec
	for i := 0; i < len(args); i++ {
		arg := args[i]
		key, value, ok := strings.Cut(arg, "=")
		if !ok {
			return spec, fmt.Errorf("%q is not a setting: use key=value.", arg)
		}
		if strings.HasPrefix(value, "\"") && !strings.HasSuffix(value, "\"") {
			closed, joined, consumed := quotedValue(args, i+1, value)
			if !closed {
				return spec, fmt.Errorf("the value of %q is not closed", key)
			}
			value = joined
			i += consumed
		}
		key = strings.ToLower(strings.TrimSpace(key))
		switch key {
		case "label":
			spec.Label = value
		case "effort":
			spec.Effort = strings.ToLower(strings.TrimSpace(value))
		case "context_mode", "context-mode":
			spec.ContextMode = strings.ToLower(strings.TrimSpace(value))
		case "context_window", "context-window":
			tokens, err := strconv.Atoi(strings.TrimSpace(value))
			if err != nil || tokens < 0 {
				return spec, fmt.Errorf("The context window must be a number of tokens.")
			}
			spec.ContextWindow = tokens
		case "max_output_tokens", "max-output-tokens":
			tokens, err := strconv.Atoi(strings.TrimSpace(value))
			if err != nil || tokens < 0 {
				return spec, fmt.Errorf("The output limit must be a number of tokens.")
			}
			spec.MaxOutputTokens = tokens
		default:
			return spec, fmt.Errorf("%q is not a model setting. Use label, effort, context_mode, context_window or max_output_tokens.", key)
		}
	}
	return spec, nil
}

// endpointSpecFromPairs reads "key=value" arguments into an endpoint spec.
//
// A value that contains a space may be quoted: name="DeepSeek EU" is one
// setting, not two.
func endpointSpecFromPairs(args []string) (EndpointSpec, error) {
	var spec EndpointSpec
	for i := 0; i < len(args); i++ {
		arg := args[i]
		key, value, ok := strings.Cut(arg, "=")
		if !ok {
			return spec, fmt.Errorf("%q is not a setting: use key=value.", arg)
		}
		if strings.HasPrefix(value, "\"") && !strings.HasSuffix(value, "\"") {
			closed, joined, consumed := quotedValue(args, i+1, value)
			if !closed {
				return spec, fmt.Errorf("the value of %q is not closed", key)
			}
			value = joined
			i += consumed
		}
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "name":
			spec.Name = value
		case "protocol":
			spec.Protocol = strings.ToLower(strings.TrimSpace(value))
		case "wire_protocol", "wire-protocol":
			spec.WireProtocol = strings.ToLower(strings.TrimSpace(value))
		case "base_url", "base-url", "url":
			spec.BaseURL = value
		case "credential_env", "credential-env", "env":
			spec.CredentialEnv = value
		case "credential", "key":
			spec.Credential = value
		case "discovery":
			spec.Discovery = strings.EqualFold(strings.TrimSpace(value), "on") ||
				strings.EqualFold(strings.TrimSpace(value), "true") ||
				strings.EqualFold(strings.TrimSpace(value), "yes")
		default:
			return spec, fmt.Errorf("%q is not an endpoint setting. Use name, protocol, wire_protocol, base_url, credential_env or discovery.", key)
		}
	}
	return spec, nil
}

// quotedValue joins the words of a quoted value.
//
// It reports whether the closing quote was found, the value without its
// quotes, and how many words it consumed.
func quotedValue(args []string, start int, first string) (bool, string, int) {
	parts := []string{strings.TrimPrefix(first, "\"")}
	for i := start; i < len(args); i++ {
		word := args[i]
		if strings.HasSuffix(word, "\"") {
			parts = append(parts, strings.TrimSuffix(word, "\""))
			return true, strings.Join(parts, " "), i - start + 1
		}
		parts = append(parts, word)
	}
	return false, "", len(args) - start
}

// wireLabel names a wire protocol for the operator.
func wireLabel(wire string) string {
	if wire == "" {
		return "unset"
	}
	return wire
}
