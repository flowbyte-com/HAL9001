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
	m.manage.endpointCredential = ""
	m.manage.endpointHasLiteral = false
	m.manage.endpointClearCredential = false
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
// models exactly as they were. The id never changes: it is the stable
// reference other records point at. To move an endpoint, add a new one
// and add its models there.
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
	m.manage.message = "Enter opens a setting. Ctrl+S writes the changes. Esc leaves them unwritten. The address, protocol, wire, credential and discovery belong to every model of this endpoint."
	m.manage.keys = "Up and down choose · Enter opens · Ctrl+S saves · Esc back"
	m.manage.lines = append(m.endpointLines(item.endpoint),
		"  affects       every model of this endpoint shares the address, protocol, wire and credential",
		"  identity      the id never changes — to move, add a new endpoint and add its models there")
	m.manage.actions = m.endpointRows(values)
	m.manage.endpointBefore = &before
	m.manage.endpointCredential = ""
	m.manage.endpointHasLiteral = detail.HasLiteralCredential
	m.manage.endpointClearCredential = false
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
	credentialValue := values.credentialEnv
	credentialEmpty := "(none)"
	if m.manage.endpointCredential != "" {
		credentialValue = "(a new key was typed — save to store it)"
		credentialEmpty = ""
	} else if m.manage.endpointClearCredential {
		credentialValue = ""
		credentialEmpty = "(will be removed)"
	} else if m.manage.endpointHasLiteral && strings.TrimSpace(values.credentialEnv) == "" {
		credentialValue = ""
		credentialEmpty = "(a stored key)"
	}
	return []manageAction{
		{key: endpointKeyID, name: "Id", value: values.id,
			what: "the endpoint's own name; letters, digits, _ and -",
			run: func(m *Model) tea.Cmd {
				if m.manage.endpointBefore != nil {
					m.manage.notice = "The id never changes: it is the stable reference. To move, add a new endpoint and add its models there."
					return nil
				}
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
		{key: endpointKeyCredential, name: "Credential source", value: credentialValue,
			empty: credentialEmpty, whatFn: func(value string) string { return m.credentialStatusForRow(value) },
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
// The choices are the environment variable the endpoint points at —
// / preferred, because the key itself never enters the file — or a
// literal key typed into a masked field. The provider is identified
// from the endpoint's address and its configured identity, never from
// a model name and never from the full display name: an endpoint called
// DeepSeek-Flash still suggests DEEPSEEK_API_KEY. The value of a
// variable — and the value of a typed key — is never shown, only
// whether there is one.
func (m *Model) openCredentialChoice() tea.Cmd {
	items := m.credentialCandidates(
		m.endpointValue(endpointKeyURL),
		m.endpointValue(endpointKeyID),
		m.endpointValue(endpointKeyName))
	m.managePush()
	m.openChoice("Credential of the endpoint",
		"Enter chooses the variable that holds the key (preferred: the key itself never enters the file), or type a key. Esc keeps what is there.",
		items, func(m *Model, choice manageItem) tea.Cmd {
			switch choice.value {
			case manageOtherCredential:
				return m.openCredentialNameForm()
			case manageEnterKey:
				return m.openCredentialKeyForm()
			case manageNoCredential:
				m.manage.endpointCredential = ""
				m.manage.endpointClearCredential = true
				m.manageSetValue(endpointKeyCredential, "")
				m.refreshCredentialRow()
				m.manageBack()
				return nil
			default:
				m.manageSetValue(endpointKeyCredential, choice.value)
				m.manage.endpointCredential = ""
				m.manage.endpointClearCredential = false
				m.refreshCredentialRow()
				m.manageBack()
				return nil
			}
		})
	return nil
}

// openCredentialKeyForm asks for a literal API key in a masked field.
//
// The key is stored locally in plaintext in the configuration file
// (mode 0600): the file permission keeps other users out, but the key
// itself is readable by anyone who can read the file or its backup. An
// environment variable is preferred for exactly this reason. The field
// is masked, it accepts a paste, and the value is never shown again,
// never logged and never put into a diagnostic. Leaving the screen
// without saving — or saving — clears the field.
func (m *Model) openCredentialKeyForm() tea.Cmd {
	m.managePush()
	fields := []manageField{{
		label: "API key",
		hint:  "paste the key — it is masked, it is stored in plaintext (mode 0600), and an environment variable is preferred",
		input: namedInput("", "", true),
	}}
	m.openForm("API key of the endpoint",
		"The key is masked and never shown again. It is stored locally in plaintext (mode 0600). Prefer an environment variable. Esc keeps what is there.",
		fields, func(m *Model) tea.Cmd {
			typed := strings.TrimSpace(m.manageValues()[0])
			// Scrub the field before leaving it: the value must not
			// survive in the form or in a draft.
			m.scrubManageFields()
			if typed != "" {
				m.manage.endpointCredential = typed
				m.manage.endpointClearCredential = false
				m.manageSetValueBelow(endpointKeyCredential, "")
				m.refreshCredentialRow()
			}
			m.manageBack()
			m.manageBack()
			return nil
		})
	return nil
}

// scrubManageFields clears the values of the form on screen, so a
// secret typed into it does not survive in memory longer than it must.
func (m *Model) scrubManageFields() {
	for i := range m.manage.fields {
		m.manage.fields[i].input.SetValue("")
	}
	if len(m.manageStack) > 0 {
		m.manageStack[len(m.manageStack)-1].draft = nil
	}
}

// refreshCredentialRow redraws the credential row after the operator
// chose a variable, typed a key or cleared the credential.
func (m *Model) refreshCredentialRow() {
	m.refreshPendingRows()
	// Rebuild the endpoint rows so the credential row shows the new
	// state: the row below the choice must say what will be saved.
	if len(m.manageStack) == 0 {
		return
	}
	frame := &m.manageStack[len(m.manageStack)-1]
	for i := range frame.actions {
		if frame.actions[i].key != endpointKeyCredential {
			continue
		}
		if m.manage.endpointCredential != "" {
			frame.actions[i].value = "(a new key was typed — save to store it)"
		} else if m.manage.endpointClearCredential {
			frame.actions[i].value = ""
		} else {
			// The value was already set by manageSetValue: keep it.
		}
	}
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
			m.scrubManageFields()
			if typed != "" {
				// The name belongs to the endpoint screen, and the
				// list of choices is only the way to reach it.
				m.manageSetValueBelow(endpointKeyCredential, typed)
				m.manage.endpointCredential = ""
				m.manage.endpointClearCredential = false
				m.refreshCredentialRow()
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
// the operator already has. A provider-based suggestion comes next: the
// endpoint's address names its provider, and the provider names its
// variable. An endpoint whose address is unknown falls back to its
// configured identity, and an endpoint no provider claims falls back to
// a name-based suggestion that the operator edits. The suggestion is
// never applied on its own: the operator chooses it explicitly.
func (m *Model) credentialCandidates(baseURL, id, name string) []manageItem {
	var out []manageItem
	seen := map[string]bool{}
	current := strings.TrimSpace(m.endpointValue(endpointKeyCredential))
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
				mark:   markFor(ref == current),
			})
		}
	}
	suggested := suggestedProviderCredentialEnv(baseURL, id, name)
	if suggested != "" && !seen[suggested] {
		out = append(out, manageItem{
			name:   suggested,
			detail: "suggested for this provider",
			value:  suggested,
			mark:   markFor(suggested == current),
		})
	} else if suggested == "" {
		// No provider claims this endpoint: offer an editable
		// name-based suggestion rather than assuming one is correct.
		fallback := suggestedCredentialEnv(name)
		if fallback == "" {
			fallback = suggestedCredentialEnv(id)
		}
		if fallback != "" && !seen[fallback] {
			out = append(out, manageItem{
				name:   fallback,
				detail: "suggested for this endpoint — edit if it is wrong",
				value:  fallback,
				mark:   markFor(fallback == current),
			})
		}
	}
	out = append(out,
		manageItem{
			name:   "Enter an API key…",
			detail: "type or paste a key once — stored in plaintext (mode 0600); an environment variable is preferred",
			value:  manageEnterKey,
		},
		manageItem{
			name:   "Another variable…",
			detail: "name another environment variable",
			value:  manageOtherCredential,
		},
		manageItem{
			name:   "No credential",
			detail: "this endpoint needs no key (local servers)",
			value:  manageNoCredential,
		})
	return out
}

// providerCredentialEnvs maps a recognised provider to the environment
// variable that holds its key. The variable is a suggestion: the
// operator may name any variable, and an explicit choice always wins.
var providerCredentialEnvs = map[string]string{
	"deepseek":    "DEEPSEEK_API_KEY",
	"minimax":     "MINIMAX_API_KEY",
	"opencode-go": "OPENCODE_GO_API_KEY",
}

// suggestedProviderCredentialEnv names the variable a provider suggests.
//
// The address comes first: a known API host names its provider no
// matter what the endpoint is called. The configured identity comes
// next: an endpoint whose address is local or not yet typed still names
// its provider when its id or its display name starts with one. A full
// display name is never turned into a variable: DeepSeek-Flash suggests
// DEEPSEEK_API_KEY, never DEEPSEEK_FLASH_API_KEY.
func suggestedProviderCredentialEnv(baseURL, id, name string) string {
	if provider := providerFromBaseURL(baseURL); provider != "" {
		if env, ok := providerCredentialEnvs[provider]; ok {
			return env
		}
	}
	if provider := providerFromIdentity(id); provider != "" {
		if env, ok := providerCredentialEnvs[provider]; ok {
			return env
		}
	}
	if provider := providerFromIdentity(name); provider != "" {
		if env, ok := providerCredentialEnvs[provider]; ok {
			return env
		}
	}
	return ""
}

// providerFromBaseURL identifies a recognised provider from its API
// host. A local address identifies no provider: a fixture on 127.0.0.1
// can stand in for any of them.
func providerFromBaseURL(baseURL string) string {
	host := strings.ToLower(strings.TrimSpace(baseURL))
	// The value may be a full URL or a bare host: look for the provider
	// name anywhere in it, so "https://api.deepseek.com/anthropic" and
	// "api.deepseek.com" both match. A loopback address never does.
	if strings.Contains(host, "127.0.0.1") || strings.Contains(host, "localhost") {
		return ""
	}
	switch {
	case strings.Contains(host, "deepseek"):
		return "deepseek"
	case strings.Contains(host, "minimax"):
		return "minimax"
	case strings.Contains(host, "opencode"):
		return "opencode-go"
	default:
		return ""
	}
}

// providerFromIdentity identifies a recognised provider from a
// configured endpoint id or display name. Only a leading provider name
// counts: "DeepSeek-Flash" is a DeepSeek endpoint, and "My DeepSeek
// Proxy" is not claimed.
func providerFromIdentity(text string) string {
	lowered := strings.ToLower(strings.TrimSpace(text))
	for _, provider := range []string{"deepseek", "minimax", "opencode"} {
		if !strings.HasPrefix(lowered, provider) {
			continue
		}
		rest := lowered[len(provider):]
		if rest == "" || rest[0] < 'a' || rest[0] > 'z' {
			if provider == "opencode" {
				return "opencode-go"
			}
			return provider
		}
	}
	return ""
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

// credentialStatusForRow says what the credential row holds.
//
// A typed key is never shown: the row says a new key was typed and
// will be stored in plaintext (mode 0600) on save. A stored key is
// never shown either: the row says one is stored. An environment
// variable is named, never valued.
func (m *Model) credentialStatusForRow(value string) string {
	if m.manage.endpointCredential != "" {
		return "a new key was typed — save stores it in plaintext (mode 0600); an environment variable is preferred"
	}
	if m.manage.endpointClearCredential {
		return "the credential will be removed on save"
	}
	if m.manage.endpointHasLiteral && strings.TrimSpace(value) == "" {
		return "a stored key is set (plaintext, mode 0600) — choosing a variable replaces it; it is never shown"
	}
	return m.credentialStatus(value)
}

// endpointValue reads one value of the endpoint the operator described.
func (m *Model) endpointValue(key string) string {
	return m.manageValue(key)
}

// endpointSpecFromRows is the endpoint the operator described.
//
// Only the fields that were named are filled in: an edit that changes
// the name alone sends the name alone, and everything else the
// endpoint holds stays where it is. A typed key travels in
// spec.Credential and never appears in a row; a removal travels in
// spec.ClearCredential.
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
	if m.manage.endpointCredential != "" {
		spec.Credential = m.manage.endpointCredential
	} else if m.manage.endpointClearCredential {
		spec.ClearCredential = true
	} else if values.credentialEnv != "" && (before == nil || values.credentialEnv != before.Settings.CredentialEnv) {
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
// sends only what changed. A typed key is cleared from memory on save,
// whether the save worked or not: it must not survive longer than it
// must.
func (m *Model) saveEndpointValues(values endpointValues, before *EndpointDetail,
	after func(*Model, string) tea.Cmd) tea.Cmd {
	if m.hooks.SaveEndpoint == nil {
		m.manage.notice = "Endpoint management is not wired up."
		return nil
	}
	spec := m.endpointSpecFromRows(before)
	clearSecret := func() {
		m.manage.endpointCredential = ""
		m.manage.endpointClearCredential = false
		m.manage.endpointHasLiteral = false
		m.scrubManageFields()
	}
	if before == nil {
		// A new endpoint needs its identity in full.
		spec.ID = values.id
		spec.Name = values.name
		spec.Protocol = values.protocol
		spec.BaseURL = values.baseURL
		if spec.WireProtocol == "" {
			spec.WireProtocol = values.wire
		}
		if m.manage.endpointCredential != "" {
			spec.Credential = m.manage.endpointCredential
			spec.CredentialEnv = ""
			spec.ClearCredential = false
		} else if m.manage.endpointClearCredential {
			spec.CredentialEnv = ""
			spec.Credential = ""
			spec.ClearCredential = true
		} else {
			spec.CredentialEnv = values.credentialEnv
		}
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
		clearSecret()
		m.manage.notice = "Cannot save the endpoint: " + err.Error()
		return nil
	}
	clearSecret()
	m.refreshEngine()
	m.syncEffortFromHooks()
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
// credential. A key the operator just typed is used for the test
// without being saved: the test proves the key before it is stored.
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
	if m.manage.endpointCredential != "" {
		spec.Credential = m.manage.endpointCredential
		spec.CredentialEnv = ""
	} else if m.manage.endpointClearCredential {
		spec.CredentialEnv = ""
		spec.Credential = ""
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
