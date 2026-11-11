package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
)

// The endpoint screens.
//
// An endpoint is four different things and they are easy to confuse: an
// identifier HAL uses, a name the operator reads, an address the
// provider answers on, and the credential that authorises the calls.
// The screens keep them apart.
//
// The credential is a reference, never a secret. An endpoint names the
// environment variable that holds its key, and the screens check that
// the variable is there without ever showing what is in it. A key typed
// into a form is never stored: the configuration holds a reference, and
// the reference is the whole of it.

// The keys of the rows of an endpoint screen.
const (
	// endpointKeyID is the endpoint's own identifier.
	endpointKeyID = "I"
	// endpointKeyName is the name HAL shows.
	endpointKeyName = "N"
	// endpointKeyProtocol is the declared protocol type.
	endpointKeyProtocol = "P"
	// endpointKeyWire is the wire protocol HAL speaks.
	endpointKeyWire = "W"
	// endpointKeyURL is the address of the provider.
	endpointKeyURL = "U"
	// endpointKeyCredential is the environment variable that holds the
	// key.
	endpointKeyCredential = "K"
	// endpointKeyDiscovery is the catalogue flag.
	endpointKeyDiscovery = "D"
)

// endpointValues is what the operator has described of an endpoint.
//
// The values live in the rows of the screen, so every screen below the
// menu changes what the menu shows and nothing else.
type endpointValues struct {
	id            string
	name          string
	protocol      string
	wire          string
	baseURL       string
	credentialEnv string
	discovery     string
}

// endpointProtocols are the protocol types an endpoint may declare.
var endpointProtocols = []struct {
	value  string
	detail string
}{
	{"openai-compatible", "any OpenAI-style /chat/completions service"},
	{"anthropic-compatible", "any Anthropic-style /v1/messages service"},
	{"local", "a local server that speaks the OpenAI dialect"},
	{"custom", "any other service; it must name a wire protocol"},
}

// endpointWireProtocols are the wire protocols HAL speaks.
var endpointWireProtocols = []struct {
	value  string
	detail string
}{
	{"openai-compatible", "the OpenAI chat-completions dialect"},
	{"anthropic-compatible", "the Anthropic messages dialect"},
}

// openNewEndpoint asks for the fields of a new endpoint.
//
// The endpoint's identity, its address and its credential are three
// different things, and the screen shows them as three rows. The
// after-callback is called with the new endpoint's id once it is
// stored, so a workflow that came from here can go on where it left
// off.
func (m *Model) openNewEndpoint(after func(*Model, string) tea.Cmd) tea.Cmd {
	if m.hooks.SaveEndpoint == nil {
		m.manage.notice = "Endpoint management is not wired up."
		return nil
	}
	values := endpointValues{
		protocol:  "openai-compatible",
		discovery: "on",
	}
	m.managePush()
	m.manage.screen = manageMenu
	m.manage.title = "Add an endpoint"
	m.manage.message = "The endpoint is added. The active model does not change."
	m.manage.keys = "Up and down choose · Enter opens · Ctrl+S saves · Esc back"
	m.manage.actions = m.endpointRows(values)
	m.manage.endpointBefore = nil
	m.manage.after = after
	m.manage.save = func(m *Model) tea.Cmd { return m.saveEndpointValues(m.endpointValues(), nil, m.manage.after) }
	m.manage.notice = ""
	m.manage.cursor = 0
	m.panelOffset = 0
	m.enterManage()
	return nil
}

// openEndpointSettings asks for the settings of one endpoint.
//
// The endpoint keeps everything it is not asked to change: an edit that
// names the name alone leaves the address, the credential and the
// models exactly as they were.
func (m *Model) openEndpointSettings(item manageItem) tea.Cmd {
	if m.hooks.EndpointDetail == nil {
		m.manage.notice = "Endpoint management is not wired up."
		return nil
	}
	detail, err := m.hooks.EndpointDetail(item.endpoint)
	if err != nil {
		m.manage.notice = "Cannot read the endpoint: " + err.Error()
		return nil
	}
	before := detail
	values := endpointValues{
		id:            detail.Settings.ID,
		name:          detail.Settings.Name,
		protocol:      detail.Settings.Protocol,
		wire:          detail.Settings.WireProtocol,
		baseURL:       detail.Settings.BaseURL,
		credentialEnv: detail.Settings.CredentialEnv,
		discovery:     onOffValue(detail.Settings.Discovery),
	}
	m.managePush()
	m.manage.screen = manageMenu
	m.manage.title = "Edit endpoint " + item.endpoint
	m.manage.message = "Enter opens a setting. Ctrl+S writes the changes. Esc leaves them unwritten."
	m.manage.keys = "Up and down choose · Enter opens · Ctrl+S saves · Esc back"
	m.manage.lines = m.endpointLines(item.endpoint)
	m.manage.actions = m.endpointRows(values)
	m.manage.endpointBefore = &before
	m.manage.save = func(m *Model) tea.Cmd { return m.saveEndpointValues(m.endpointValues(), &before, nil) }
	m.manage.notice = ""
	m.manage.cursor = 0
	m.panelOffset = 0
	m.enterManage()
	return nil
}

// endpointRows are the rows of an endpoint screen.
//
// Every row shows what the endpoint currently holds, so the operator
// never has to remember what they typed before.
func (m *Model) endpointRows(values endpointValues) []manageAction {
	return []manageAction{
		{key: endpointKeyID, name: "Id", value: values.id,
			what: "the endpoint's own name; letters, digits, _ and -",
			run: func(m *Model) tea.Cmd {
				return m.openEndpointValueForm("Id", endpointKeyID, m.endpointValue(endpointKeyID),
					"letters, digits, _ and -; the id never changes once the endpoint exists")
			}},
		{key: endpointKeyName, name: "Name", value: values.name,
			what: "the name HAL shows",
			run: func(m *Model) tea.Cmd {
				return m.openEndpointValueForm("Name", endpointKeyName, m.endpointValue(endpointKeyName),
					"the name HAL shows for this endpoint")
			}},
		{key: endpointKeyProtocol, name: "Protocol", value: values.protocol,
			what: "the kind of service this is",
			run:  func(m *Model) tea.Cmd { return m.openProtocolChoice() }},
		{key: endpointKeyWire, name: "Wire protocol", value: values.wire,
			what: "the dialect HAL speaks; a custom endpoint needs one",
			run:  func(m *Model) tea.Cmd { return m.openWireChoice() }},
		{key: endpointKeyURL, name: "Base url", value: values.baseURL,
			what: "the address of the provider, e.g. https://api.deepseek.com/v1",
			run: func(m *Model) tea.Cmd {
				return m.openEndpointValueForm("Base url", endpointKeyURL, m.endpointValue(endpointKeyURL),
					"the address of the provider; this is not the endpoint's id")
			}},
		{key: endpointKeyCredential, name: "Credential source", value: values.credentialEnv,
			empty: "(none)", whatFn: func(value string) string { return m.credentialStatus(value) },
			run: func(m *Model) tea.Cmd { return m.openCredentialChoice() }},
		{key: endpointKeyDiscovery, name: "Discovery", value: values.discovery,
			what: "whether HAL may ask the provider for its catalogue",
			run:  func(m *Model) tea.Cmd { return m.openDiscoveryChoice() }},
		{key: "T", name: "Test", value: "",
			what: "ask the endpoint whether it answers",
			run:  func(m *Model) tea.Cmd { return m.testEndpointValues(m.endpointValues()) }},
		{key: "S", name: "Save", value: "",
			what: "write the endpoint",
			run: func(m *Model) tea.Cmd {
				return m.saveEndpointValues(m.endpointValues(), m.manage.endpointBefore, m.manage.after)
			}},
	}
}

// openEndpointValueForm asks for one value of an endpoint.
//
// The value belongs to the row it came from: the form hands it back and
// the row shows it. The field starts empty and the current value is
// stated in the hint, so typing a new value replaces the old one
// instead of being appended to it.
func (m *Model) openEndpointValueForm(label, key, value, hint string) tea.Cmd {
	m.managePush()
	fields := []manageField{{
		label: label,
		hint:  currentHint(hint, value),
		input: namedInput("", "", false),
	}}
	m.openForm(label, "An empty field keeps the current value.", fields,
		func(m *Model) tea.Cmd {
			typed := strings.TrimSpace(m.manageValues()[0])
			if typed != "" {
				m.manageSetValue(key, typed)
			}
			m.manageBack()
			return nil
		})
	return nil
}

// currentHint says what a field holds now.
//
// The value is stated in words rather than shown as a placeholder: a
// placeholder reads like a suggestion, and the operator cannot tell
// whether the field holds the old value or nothing at all.
func currentHint(what, value string) string {
	if strings.TrimSpace(value) == "" {
		return what
	}
	return what + "; it is " + value + " now — type a new value to replace it"
}

// openProtocolChoice asks which kind of service the endpoint is.
func (m *Model) openProtocolChoice() tea.Cmd {
	current := m.endpointValue(endpointKeyProtocol)
	items := make([]manageItem, 0, len(endpointProtocols))
	for _, p := range endpointProtocols {
		items = append(items, manageItem{
			name:   p.value,
			detail: p.detail,
			value:  p.value,
			mark:   markFor(current == p.value),
		})
	}
	m.managePush()
	m.openChoice("Protocol of the endpoint",
		"Enter chooses the protocol.",
		items, func(m *Model, choice manageItem) tea.Cmd {
			m.manageSetValue(endpointKeyProtocol, choice.value)
			m.manageBack()
			return nil
		})
	return nil
}

// openWireChoice asks which dialect HAL speaks to the endpoint.
func (m *Model) openWireChoice() tea.Cmd {
	current := m.endpointValue(endpointKeyWire)
	items := make([]manageItem, 0, len(endpointWireProtocols))
	for _, w := range endpointWireProtocols {
		items = append(items, manageItem{
			name:   w.value,
			detail: w.detail,
			value:  w.value,
			mark:   markFor(current == w.value),
		})
	}
	m.managePush()
	m.openChoice("Wire protocol of the endpoint",
		"Enter chooses the dialect. A custom endpoint must name one.",
		items, func(m *Model, choice manageItem) tea.Cmd {
			m.manageSetValue(endpointKeyWire, choice.value)
			m.manageBack()
			return nil
		})
	return nil
}

// openDiscoveryChoice asks whether the catalogue may be probed.
func (m *Model) openDiscoveryChoice() tea.Cmd {
	current := m.endpointValue(endpointKeyDiscovery)
	items := []manageItem{
		{name: "on", detail: "HAL may ask the provider which models it serves",
			value: "on", mark: markFor(current == "on")},
		{name: "off", detail: "the provider is never asked for its catalogue",
			value: "off", mark: markFor(current == "off")},
	}
	m.managePush()
	m.openChoice("Discovery of the endpoint",
		"Enter chooses whether the catalogue may be probed.",
		items, func(m *Model, choice manageItem) tea.Cmd {
			m.manageSetValue(endpointKeyDiscovery, choice.value)
			m.manageBack()
			return nil
		})
	return nil
}

// openCredentialChoice asks where the credential comes from.
//
// The choices are the environment variables the operator can point the
// endpoint at: the ones other endpoints already use, the one this
// endpoint's own name suggests, and any other name. The value of a
// variable is never shown, only whether there is one.
func (m *Model) openCredentialChoice() tea.Cmd {
	items := m.credentialCandidates(m.endpointValue(endpointKeyName))
	m.managePush()
	m.openChoice("Credential of the endpoint",
		"Enter chooses the variable that holds the key. The key itself never enters the file.",
		items, func(m *Model, choice manageItem) tea.Cmd {
			if choice.value == manageOtherCredential {
				return m.openCredentialNameForm()
			}
			m.manageSetValue(endpointKeyCredential, choice.value)
			m.manageBack()
			return nil
		})
	return nil
}

// openCredentialNameForm asks for the name of another variable.
//
// The name is asked for in a step of its own, and the endpoint screen
// is the step it comes from: naming the variable ends the choice and
// the row of the endpoint shows the name.
func (m *Model) openCredentialNameForm() tea.Cmd {
	current := m.endpointValue(endpointKeyCredential)
	m.managePush()
	fields := []manageField{{
		label: "Credential source",
		hint:  currentHint("the name of the environment variable, e.g. DEEPSEEK_API_KEY — the key itself never enters the file", current),
		input: namedInput("", "", false),
	}}
	m.openForm("Credential of the endpoint",
		"An empty field keeps the current value.",
		fields, func(m *Model) tea.Cmd {
			typed := strings.TrimSpace(m.manageValues()[0])
			if typed != "" {
				// The name belongs to the endpoint screen, and the
				// list of choices is only the way to reach it.
				m.manageSetValueBelow(endpointKeyCredential, typed)
			}
			m.manageBack()
			m.manageBack()
			return nil
		})
	return nil
}

// credentialCandidates are the environment variables an endpoint may
// point at.
//
// The ones other endpoints already use come first: they are the ones
// the operator already has. A new name is suggested from the
// endpoint's own name, because that is what the operator would have
// called it.
func (m *Model) credentialCandidates(name string) []manageItem {
	var out []manageItem
	seen := map[string]bool{}
	if m.hooks.Endpoints != nil {
		for _, endpoint := range m.hooks.Endpoints() {
			ref := strings.TrimSpace(endpoint.CredentialEnv)
			if ref == "" || seen[ref] {
				continue
			}
			seen[ref] = true
			out = append(out, manageItem{
				name:   ref,
				detail: "already used by endpoint " + endpoint.ID,
				value:  ref,
			})
		}
	}
	suggested := suggestedCredentialEnv(name)
	if suggested != "" && !seen[suggested] {
		out = append(out, manageItem{
			name:   suggested,
			detail: "suggested for this endpoint",
			value:  suggested,
		})
	}
	return append(out, manageItem{
		name:   "Another variable…",
		detail: "name another environment variable",
		value:  manageOtherCredential,
	})
}

// suggestedCredentialEnv names the environment variable an endpoint
// would use.
//
// The name is the endpoint's own name, upper-cased: "DeepSeek" becomes
// DEEPSEEK_API_KEY. It is a suggestion and nothing more: the operator
// may name any variable they like.
func suggestedCredentialEnv(name string) string {
	cleaned := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return r
		default:
			return '_'
		}
	}, strings.TrimSpace(name))
	cleaned = strings.Trim(cleaned, "_")
	if cleaned == "" {
		return ""
	}
	return strings.ToUpper(cleaned) + "_API_KEY"
}

// credentialStatus says whether a variable holds a value.
//
// It never says what the value is: only whether there is one, so the
// operator can tell a variable they have to set from one they have
// already set.
func (m *Model) credentialStatus(name string) string {
	name = strings.TrimSpace(name)
	switch {
	case name == "":
		return "no variable named — the endpoint has no credential"
	case m.hooks.CredentialEnvSet == nil:
		return "the variable that holds the key"
	case m.hooks.CredentialEnvSet(name):
		return name + " is set in this environment"
	default:
		return name + " is not set in this environment"
	}
}

// endpointValue reads one value of the endpoint the operator described.
func (m *Model) endpointValue(key string) string {
	return m.manageValue(key)
}

// endpointSpecFromRows is the endpoint the operator described.
//
// Only the fields that were named are filled in: an edit that changes
// the name alone sends the name alone, and everything else the
// endpoint holds stays where it is.
func (m *Model) endpointSpecFromRows(before *EndpointDetail) EndpointSpec {
	values := m.endpointValues()
	spec := EndpointSpec{}
	if values.id != "" && (before == nil || values.id != before.Settings.ID) {
		spec.ID = values.id
	}
	if values.name != "" && (before == nil || values.name != before.Settings.Name) {
		spec.Name = values.name
	}
	if values.protocol != "" && (before == nil || values.protocol != before.Settings.Protocol) {
		spec.Protocol = values.protocol
	}
	if values.wire != "" && (before == nil || values.wire != before.Settings.WireProtocol) {
		spec.WireProtocol = values.wire
	}
	if values.baseURL != "" && (before == nil || values.baseURL != before.Settings.BaseURL) {
		spec.BaseURL = values.baseURL
	}
	if values.credentialEnv != "" && (before == nil || values.credentialEnv != before.Settings.CredentialEnv) {
		spec.CredentialEnv = values.credentialEnv
	}
	if values.discovery != "" {
		parsed, err := parseOnOff(values.discovery)
		if err == nil {
			current := true
			if before != nil && before.Settings.Discovery != nil {
				current = *before.Settings.Discovery
			}
			if parsed != current {
				spec.Discovery = &parsed
			}
		}
	}
	return spec
}

// endpointValues reads the endpoint the operator described.
func (m *Model) endpointValues() endpointValues {
	return endpointValues{
		id:            m.manageValue(endpointKeyID),
		name:          m.manageValue(endpointKeyName),
		protocol:      m.manageValue(endpointKeyProtocol),
		wire:          m.manageValue(endpointKeyWire),
		baseURL:       m.manageValue(endpointKeyURL),
		credentialEnv: m.manageValue(endpointKeyCredential),
		discovery:     m.manageValue(endpointKeyDiscovery),
	}
}

// saveEndpointValues stores the endpoint the operator described.
//
// A new endpoint needs an id, a name, a protocol and an address; the
// configuration handlers say so when one of them is missing. An edit
// sends only what changed.
func (m *Model) saveEndpointValues(values endpointValues, before *EndpointDetail,
	after func(*Model, string) tea.Cmd) tea.Cmd {
	if m.hooks.SaveEndpoint == nil {
		m.manage.notice = "Endpoint management is not wired up."
		return nil
	}
	spec := m.endpointSpecFromRows(before)
	if before == nil {
		// A new endpoint needs its identity in full.
		spec.ID = values.id
		spec.Name = values.name
		spec.Protocol = values.protocol
		spec.BaseURL = values.baseURL
		if spec.WireProtocol == "" {
			spec.WireProtocol = values.wire
		}
		spec.CredentialEnv = values.credentialEnv
		discovery := true
		if values.discovery != "" {
			if parsed, err := parseOnOff(values.discovery); err == nil {
				discovery = parsed
			}
		}
		spec.Discovery = &discovery
		if strings.TrimSpace(spec.ID) == "" {
			m.manage.notice = "The endpoint needs an id."
			return nil
		}
	} else {
		spec.ID = before.Settings.ID
	}
	if err := m.hooks.SaveEndpoint(spec); err != nil {
		m.manage.notice = "Cannot save the endpoint: " + err.Error()
		return nil
	}
	m.refreshEngine()
	if after != nil {
		return after(m, spec.ID)
	}
	m.closeManage("Endpoint " + spec.ID + " saved.")
	return nil
}

// testEndpointValues asks the endpoint whether it answers.
//
// The test is never run on its own: the operator asks for it. It lists
// the endpoint's catalogue, which asks the provider a question but runs
// no conversation and spends nothing. The report never carries the
// credential.
func (m *Model) testEndpointValues(values endpointValues) tea.Cmd {
	if m.hooks.TestEndpoint == nil {
		m.manage.notice = "Testing an endpoint is not wired up."
		return nil
	}
	spec := EndpointSpec{
		ID:            values.id,
		Name:          values.name,
		Protocol:      values.protocol,
		WireProtocol:  values.wire,
		BaseURL:       values.baseURL,
		CredentialEnv: values.credentialEnv,
	}
	if spec.Protocol == "" {
		spec.Protocol = "openai-compatible"
	}
	if strings.TrimSpace(spec.BaseURL) == "" {
		m.manage.notice = "The endpoint has no address yet — set the base url first."
		return nil
	}
	report, err := m.hooks.TestEndpoint(spec)
	if err != nil {
		m.manage.notice = "The endpoint did not answer: " + err.Error()
		return nil
	}
	m.manage.notice = report
	return nil
}
