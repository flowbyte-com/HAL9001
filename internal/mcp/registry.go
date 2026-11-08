package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/flowbyte-com/HAL9001/internal/buildinfo"
	"github.com/flowbyte-com/HAL9001/internal/permission"
	"github.com/flowbyte-com/HAL9001/internal/provider"
	"github.com/flowbyte-com/HAL9001/internal/workspace"
)

// ToolInfo is one tool of one server, as HAL knows it.
type ToolInfo struct {
	// Exposed is the provider-visible name. It is unique across every
	// server.
	Exposed string
	// Server is the configured server the tool belongs to.
	Server string
	// Name is the server-side tool name.
	Name string
	// Description is the server's own description, bounded.
	Description string
	// Schema is the server's input schema as JSON, bounded.
	Schema string
	// AllowedIn444 reports whether the operator's local policy permits
	// this tool in 444. It comes from the configuration, never from a
	// server annotation.
	AllowedIn444 bool
}

// ResourceInfo is one resource of one server.
type ResourceInfo struct {
	Server      string
	URI         string
	Name        string
	Description string
	MIMEType    string
	Size        int64
}

// PromptArgument is one argument of a prompt.
type PromptArgument struct {
	Name        string
	Description string
	Required    bool
}

// PromptInfo is one prompt of one server.
type PromptInfo struct {
	Server      string
	Name        string
	Description string
	Arguments   []PromptArgument
}

// ServerState names the connection state of one server.
type ServerState string

const (
	// StateConfigured means the server is configured but not connected.
	StateConfigured ServerState = "configured"
	// StateConnected means the session is live.
	StateConnected ServerState = "connected"
	// StateError means the last connection attempt failed.
	StateError ServerState = "error"
	// StateDisconnected means the server was connected and is not any more.
	StateDisconnected ServerState = "disconnected"
)

// ServerStatus is the operator-facing state of one configured server.
type ServerStatus struct {
	Name          string
	Transport     string
	State         ServerState
	Protocol      string
	ServerName    string
	ServerVersion string
	Capabilities  []string
	Tools         []ToolInfo
	Resources     []ResourceInfo
	Prompts       []PromptInfo
	AllowedTools  []string
	Error         string
	// URL is the remote endpoint without credentials.
	URL string
}

// Result is one MCP tool outcome for the engine and the audit trail.
type Result struct {
	Content   string
	Action    string
	Target    string
	Status    string
	Summary   string
	Duration  time.Duration
	Bytes     int
	Truncated bool
	// Mode is the permission mode captured when the call began.
	Mode permission.Mode
}

// Registry holds the configured MCP servers and the tools they expose.
//
// The registry is the only authority on which MCP tools exist: a server
// that is not connected contributes nothing, and a tool that is not
// listed by a connected server cannot be called.
type Registry struct {
	mu      sync.Mutex
	gate    permission.Gate
	audit   *workspace.Audit
	session func() string
	now     func() time.Time

	servers map[string]*serverState
	order   []string
	tools   map[string]ToolInfo
	// collisions names exposed tool names that two distinct server tools
	// would share. They are not offered and cannot be executed: a name
	// that does not identify exactly one tool identifies none.
	collisions map[string]bool
}

// serverState is one configured server and its live session.
type serverState struct {
	cfg Server

	// opMu serializes connect and disconnect for this server. The state
	// mutex is never held across a handshake, a listing or a tool call,
	// so the interface can always read the state promptly.
	opMu sync.Mutex

	mu         sync.Mutex
	session    *sdk.ClientSession
	tools      []ToolInfo
	resources  []ResourceInfo
	prompts    []PromptInfo
	protocol   string
	serverName string
	version    string
	caps       []string
	capsKnown  bool
	connected  bool
	err        string
	// secrets are the resolved credentials of this server. They are used
	// only to redact an error message; they are never logged or stored.
	secrets []string
}

// supports reports whether the server advertised a capability. An older
// server that did not supply a capability object is treated as unknown,
// so the client tries the listing rather than assuming it is absent.
// The caller must hold st.mu.
func (st *serverState) supports(name string) bool {
	if !st.capsKnown {
		return true
	}
	for _, capability := range st.caps {
		if capability == name {
			return true
		}
	}
	return false
}

// NewRegistry builds an empty registry.
//
// The gate is the read-only view of HAL's permission controller. The
// registry can observe the mode and ask the gate to start a privileged
// operation, but it can never change the mode itself.
func NewRegistry(gate permission.Gate) *Registry {
	return &Registry{
		gate:       gate,
		now:        time.Now,
		servers:    map[string]*serverState{},
		tools:      map[string]ToolInfo{},
		collisions: map[string]bool{},
	}
}

// WithAudit attaches the shared trail and the session name.
func (r *Registry) WithAudit(a *workspace.Audit, session func() string) *Registry {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.audit = a
	r.session = session
	return r
}

// AddServer registers one configured server.
//
// The configuration is operator-controlled: this is the only way a
// server enters HAL, and it is never reached from a model response.
func (r *Registry) AddServer(s Server) error {
	if err := s.Validate(); err != nil {
		return err
	}
	if r == nil {
		return errors.New("mcp: no registry is attached")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.servers[s.Name]; exists {
		return fmt.Errorf("mcp: server %q is already configured", s.Name)
	}
	if len(r.servers) >= MaxServers {
		return fmt.Errorf("mcp: at most %d MCP servers can be configured", MaxServers)
	}
	r.servers[s.Name] = &serverState{cfg: s}
	r.order = append(r.order, s.Name)
	return nil
}

// Servers returns the state of every configured server, in configuration
// order.
func (r *Registry) Servers() []ServerStatus {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	names := append([]string(nil), r.order...)
	states := make(map[string]*serverState, len(names))
	for _, name := range names {
		states[name] = r.servers[name]
	}
	r.mu.Unlock()

	out := make([]ServerStatus, 0, len(names))
	for _, name := range names {
		st := states[name]
		st.mu.Lock()
		transport := st.cfg.Transport
		if transport == "" {
			transport = TransportStdio
		}
		secrets := append([]string(nil), st.secrets...)
		status := ServerStatus{
			Name:          st.cfg.Name,
			Transport:     string(transport),
			State:         StateConfigured,
			Protocol:      st.protocol,
			ServerName:    st.serverName,
			ServerVersion: st.version,
			Capabilities:  append([]string(nil), st.caps...),
			Tools:         append([]ToolInfo(nil), st.tools...),
			Resources:     append([]ResourceInfo(nil), st.resources...),
			Prompts:       append([]PromptInfo(nil), st.prompts...),
			AllowedTools:  append([]string(nil), st.cfg.AllowedTools...),
			Error:         st.err,
		}
		if st.cfg.Transport == TransportHTTP {
			status.URL = safeURL(st.cfg.URL)
		}
		switch {
		case st.connected:
			status.State = StateConnected
		case st.err != "":
			status.State = StateError
		case st.session == nil && st.protocol != "":
			status.State = StateDisconnected
		}
		st.mu.Unlock()
		// Server-supplied display strings are data. Strip terminal
		// controls and any configured credential before the interface
		// renders them.
		variants := redactionVariants(secrets)
		status.Protocol = replaceRedactions(status.Protocol, variants)
		status.ServerName = replaceRedactions(status.ServerName, variants)
		status.ServerVersion = replaceRedactions(status.ServerVersion, variants)
		status.Error = replaceRedactions(cleanRemoteText(status.Error, MaxDescriptionBytes), variants)
		for i := range status.Tools {
			status.Tools[i].Name = replaceRedactions(status.Tools[i].Name, variants)
			status.Tools[i].Description = replaceRedactions(status.Tools[i].Description, variants)
			status.Tools[i].Schema = redactJSONText(status.Tools[i].Schema, variants)
		}
		for i := range status.Resources {
			status.Resources[i].URI = replaceRedactions(status.Resources[i].URI, variants)
			status.Resources[i].Name = replaceRedactions(status.Resources[i].Name, variants)
			status.Resources[i].Description = replaceRedactions(status.Resources[i].Description, variants)
			status.Resources[i].MIMEType = replaceRedactions(status.Resources[i].MIMEType, variants)
		}
		for i := range status.Prompts {
			status.Prompts[i].Name = replaceRedactions(status.Prompts[i].Name, variants)
			status.Prompts[i].Description = replaceRedactions(status.Prompts[i].Description, variants)
			for j := range status.Prompts[i].Arguments {
				status.Prompts[i].Arguments[j].Name = replaceRedactions(status.Prompts[i].Arguments[j].Name, variants)
				status.Prompts[i].Arguments[j].Description = replaceRedactions(status.Prompts[i].Arguments[j].Description, variants)
			}
		}
		// The displayed allowlist is a copy: the configured one stays
		// authoritative for the 444 permission check.
		for i := range status.AllowedTools {
			status.AllowedTools[i] = replaceRedactions(status.AllowedTools[i], variants)
		}
		out = append(out, status)
	}
	return out
}

// Connect opens a session with one server and discovers what it offers.
//
// A failed handshake tears the transport down, so a local server never
// outlives the attempt to reach it.
func (r *Registry) Connect(ctx context.Context, name string) error {
	if r == nil {
		return fmt.Errorf("%w: %s", ErrUnknownServer, name)
	}
	st, ok := r.state(name)
	if !ok {
		return fmt.Errorf("%w: %s", ErrUnknownServer, name)
	}
	// One lifecycle operation per server. Nothing below holds st.mu
	// across a handshake, a listing or a call, so Servers() and the
	// panel remain responsive while a server is slow.
	st.opMu.Lock()
	defer st.opMu.Unlock()

	st.mu.Lock()
	if st.connected {
		st.mu.Unlock()
		return nil
	}
	// A new attempt starts from a clean slate: nothing discovered by an
	// earlier session is trusted until this handshake succeeds.
	st.tools = nil
	st.resources = nil
	st.prompts = nil
	st.protocol = ""
	st.serverName = ""
	st.version = ""
	st.caps = nil
	st.capsKnown = false
	st.err = ""
	st.secrets = nil
	cfg := st.cfg
	st.mu.Unlock()

	transport, secrets, closeTransport, err := cfg.transport()
	if err != nil {
		secrets = append(secrets, r.configuredSecrets()...)
		message := redact(err.Error(), secrets)
		st.mu.Lock()
		st.err = message
		st.mu.Unlock()
		r.rebuildTools()
		return errors.New(message)
	}
	st.mu.Lock()
	st.secrets = append([]string(nil), secrets...)
	st.mu.Unlock()
	allSecrets := append(append([]string(nil), secrets...), r.configuredSecrets()...)

	connectCtx, cancel := context.WithTimeout(ctx, DefaultConnectTimeout)
	defer cancel()

	client := sdk.NewClient(&sdk.Implementation{
		Name:    "HAL9001",
		Version: buildinfo.Current().Version,
	}, &sdk.ClientOptions{
		// HAL provides no roots, sampling, elicitation or completion
		// handlers, so it advertises no client capability. The SDK's
		// historical default would claim roots support it does not use.
		Capabilities: &sdk.ClientCapabilities{},
	})
	session, err := client.Connect(connectCtx, transport, nil)
	if err != nil {
		closeTransport()
		wrapped := fmt.Errorf("mcp: cannot connect to %q: %s", name, redact(err.Error(), allSecrets))
		st.mu.Lock()
		st.err = wrapped.Error()
		st.mu.Unlock()
		r.rebuildTools()
		return wrapped
	}

	init := session.InitializeResult()
	protocol := ""
	serverName := ""
	version := ""
	var caps []string
	capsKnown := false
	if init != nil {
		capsKnown = true
		protocol = cleanRemoteText(init.ProtocolVersion, 64)
		if init.ServerInfo != nil {
			serverName = cleanRemoteText(init.ServerInfo.Name, MaxRemoteNameBytes)
			version = cleanRemoteText(init.ServerInfo.Version, MaxRemoteNameBytes)
		}
		caps = capabilityNames(init.Capabilities)
	}

	st.mu.Lock()
	st.session = session
	st.connected = true
	st.err = ""
	st.protocol = protocol
	st.serverName = serverName
	st.version = version
	st.caps = caps
	st.capsKnown = capsKnown
	wantTools := st.supports("tools")
	wantResources := st.supports("resources")
	wantPrompts := st.supports("prompts")
	st.mu.Unlock()

	variants := r.secretVariants()
	var tools []ToolInfo
	if wantTools {
		tools, err = discoverTools(connectCtx, session, cfg, variants)
		if err != nil {
			// The session is live but the tool list is not usable: close it
			// rather than offer a half-known server.
			st.mu.Lock()
			if st.session == session {
				st.session = nil
				st.connected = false
			}
			st.mu.Unlock()
			_ = session.Close()
			wrapped := fmt.Errorf("mcp: cannot list the tools of %q: %s", name, redact(err.Error(), allSecrets))
			st.mu.Lock()
			st.err = wrapped.Error()
			st.mu.Unlock()
			r.rebuildTools()
			return wrapped
		}
	}
	var resources []ResourceInfo
	if wantResources {
		resources = discoverResources(connectCtx, session, cfg, variants)
	}
	var prompts []PromptInfo
	if wantPrompts {
		prompts = discoverPrompts(connectCtx, session, cfg, variants)
	}

	st.mu.Lock()
	if st.session == session {
		st.tools = tools
		st.resources = resources
		st.prompts = prompts
	}
	st.mu.Unlock()

	r.rebuildTools()
	return nil
}

// Disconnect closes the session with one server.
func (r *Registry) Disconnect(ctx context.Context, name string) error {
	if r == nil {
		return fmt.Errorf("%w: %s", ErrUnknownServer, name)
	}
	st, ok := r.state(name)
	if !ok {
		return fmt.Errorf("%w: %s", ErrUnknownServer, name)
	}
	st.opMu.Lock()
	defer st.opMu.Unlock()
	st.mu.Lock()
	session := st.session
	st.session = nil
	st.connected = false
	st.tools = nil
	st.resources = nil
	st.prompts = nil
	st.err = ""
	st.mu.Unlock()
	if session != nil {
		_ = session.Close()
	}
	r.rebuildTools()
	return nil
}

// Close disconnects every server. It is called when HAL exits, so no
// local server is left running behind the interface.
func (r *Registry) Close() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	names := append([]string(nil), r.order...)
	r.mu.Unlock()
	var firstErr error
	for _, name := range names {
		if err := r.Disconnect(context.Background(), name); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// Active reports whether any connected server exposes a tool.
func (r *Registry) Active() bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.tools) > 0
}

// Tools returns the MCP tools the model may use right now.
//
// In 444 only the tools the operator's local policy allows are offered.
// In 666 every tool of every connected server is offered. The decision
// is repeated at the execution boundary, so a stale tool list can never
// widen the authority.
func (r *Registry) Tools() []provider.Tool {
	if r == nil {
		return nil
	}
	mode := r.mode()
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]provider.Tool, 0, len(r.tools))
	for _, name := range r.toolNames() {
		if r.collisions[name] {
			continue
		}
		info := r.tools[name]
		if mode != permission.Unrestricted && !info.AllowedIn444 {
			continue
		}
		out = append(out, provider.Tool{
			Name:        info.Exposed,
			Description: exposedDescription(info),
			Parameters:  info.Schema,
		})
	}
	return out
}

// Has reports whether HAL exposes an MCP tool under this name.
func (r *Registry) Has(name string) bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.collisions[name] {
		return false
	}
	_, ok := r.tools[name]
	return ok
}

// Execute runs one MCP tool call.
//
// The permission decision is made here, at the execution boundary, and
// never from the tool list the model saw. In 444 a call runs only if
// the operator's local policy names the tool. In 666 the call is begun
// through the permission gate, so revoking 666 cancels it.
func (r *Registry) Execute(ctx context.Context, call provider.ToolCall) Result {
	if r == nil {
		return Result{
			Content: errorEnvelope("", call.Name, "no MCP registry is attached"),
			Action:  "unknown",
			Target:  call.Name,
			Status:  "denied",
			Summary: call.Name + " denied · no MCP registry",
			Mode:    permission.Guarded,
		}
	}
	start := r.now()
	mode := r.mode()
	res := Result{Action: "unknown", Target: call.Name, Status: "failed", Mode: mode}

	info, ok := r.lookup(call.Name)
	if !ok {
		res.Status = "denied"
		res.Content = errorEnvelope("", call.Name, "unknown MCP tool")
		res.Summary = call.Name + " denied · unknown MCP tool"
		return r.finish(call, res, start)
	}
	variants := r.secretVariants()
	safeToolName := replaceRedactions(info.Name, variants)
	res.Action = "mcp:" + info.Server + "/" + actionName(safeToolName)
	res.Target = info.Server

	if raw, bad := call.Arguments["_invalid"]; bad {
		_ = raw
		res.Content = errorEnvelope(info.Server, safeToolName, "arguments are not valid JSON")
		res.Summary = call.Name + " failed · malformed arguments"
		return r.finish(call, res, start)
	}
	arguments := map[string]any{}
	for key, value := range call.Arguments {
		arguments[key] = value
	}
	encoded, err := json.Marshal(arguments)
	if err != nil {
		res.Content = errorEnvelope(info.Server, safeToolName, "arguments cannot be encoded")
		res.Summary = call.Name + " failed · malformed arguments"
		return r.finish(call, res, start)
	}
	if len(encoded) > MaxArgumentBytes {
		res.Content = errorEnvelope(info.Server, safeToolName, "arguments are too large")
		res.Summary = call.Name + " failed · arguments too large"
		return r.finish(call, res, start)
	}

	opCtx := ctx
	release := func() {}
	if mode == permission.Unrestricted {
		if r.gate == nil {
			res.Status = "denied"
			res.Content = errorEnvelope(info.Server, safeToolName, "no permission gate is attached")
			res.Summary = call.Name + " denied · no permission gate"
			return r.finish(call, res, start)
		}
		var err error
		opCtx, release, err = r.gate.Begin(ctx, "mcp tool "+info.Exposed)
		if err != nil {
			// The snapshot said 666 but the gate has already returned to
			// 444. A tool the local policy allows in 444 may still run;
			// anything else is refused, and the refusal says that the
			// authority was revoked rather than never granted.
			if !info.AllowedIn444 {
				res.Status = "denied"
				res.Content = errorEnvelope(info.Server, safeToolName,
					"666 was revoked or disabled before this MCP tool could run; it is not on the 444 allowlist")
				res.Summary = call.Name + " denied · 666 revoked"
				return r.finish(call, res, start)
			}
			res.Mode = permission.Guarded
			opCtx = ctx
			release = func() {}
		} else {
			defer release()
		}
	} else if !info.AllowedIn444 {
		res.Status = "denied"
		res.Content = errorEnvelope(info.Server, safeToolName,
			"this MCP tool is not on the local allowlist; it needs 666")
		res.Summary = call.Name + " denied · 444"
		return r.finish(call, res, start)
	}

	timeout := r.timeout(info.Server)
	callCtx, cancel := context.WithTimeout(opCtx, timeout)
	defer cancel()

	result, err := r.callTool(callCtx, info, arguments)
	if err != nil {
		switch {
		case errors.Is(err, context.DeadlineExceeded) || errors.Is(callCtx.Err(), context.DeadlineExceeded):
			res.Status = "timeout"
		case errors.Is(err, context.Canceled) || errors.Is(callCtx.Err(), context.Canceled):
			res.Status = "cancelled"
		default:
			res.Status = "failed"
		}
		res.Content = errorEnvelope(info.Server, safeToolName, r.redactedMessage(info.Server, err))
		res.Summary = call.Name + " failed · " + r.redactedMessage(info.Server, err)
		r.markDisconnected(info.Server, err)
		return r.finish(call, res, start)
	}
	if result == nil {
		res.Status = "failed"
		res.Content = errorEnvelope(info.Server, safeToolName, "the server returned no result")
		res.Summary = call.Name + " failed · no result"
		return r.finish(call, res, start)
	}

	// Bound the combined payload, not each half independently: a server
	// that returns both content and structured content must not get twice
	// the budget. Every value is redacted before it is bounded, and the
	// redactor withholds anything it cannot scan within its own budget.
	redactor := newRedactor(r.configuredSecrets())
	structured := ""
	if result.StructuredContent != nil {
		if raw, err := json.Marshal(result.StructuredContent); err == nil {
			clean, complete := redactor.jsonDocument(string(raw))
			if !complete {
				res.Truncated = true
			}
			limit := MaxResultBytes / 2
			text, cut := boundedText(clean, limit)
			structured = text
			res.Truncated = res.Truncated || cut
		}
	}
	contentBudget := MaxResultBytes - len(structured)
	if contentBudget < 1024 {
		contentBudget = 1024
	}
	pieces, complete := redactor.contentPieces(result.Content)
	content, truncated := packContent(pieces, contentBudget)
	res.Truncated = res.Truncated || truncated || !complete
	res.Bytes = len(content) + len(structured)
	if result.IsError {
		res.Status = "failed"
		res.Content = envelope(info.Server, safeToolName, true, content, structured)
		res.Summary = call.Name + " failed · the server reported an error"
		return r.finish(call, res, start)
	}
	res.Status = "ok"
	res.Content = envelope(info.Server, safeToolName, false, content, structured)
	res.Summary = call.Name + " ok"
	return r.finish(call, res, start)
}

// ListTools returns the tools of one server, connected or not.
func (r *Registry) ListTools(ctx context.Context, name string) ([]ToolInfo, error) {
	st, ok := r.state(name)
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrUnknownServer, name)
	}
	st.mu.Lock()
	session := st.session
	connected := st.connected
	cfg := st.cfg
	supports := st.supports("tools")
	st.mu.Unlock()
	if !connected || session == nil {
		return nil, fmt.Errorf("mcp: server %q is not connected", name)
	}
	if !supports {
		return nil, nil
	}
	tools, err := discoverTools(ctx, session, cfg, r.secretVariants())
	if err != nil {
		return nil, r.redactedError(name, err)
	}
	variants := r.secretVariants()
	for i := range tools {
		tools[i].Name = replaceRedactions(tools[i].Name, variants)
		tools[i].Description = replaceRedactions(tools[i].Description, variants)
		tools[i].Schema = redactJSONText(tools[i].Schema, variants)
	}
	return tools, nil
}

// ListResources returns the resources of one connected server.
func (r *Registry) ListResources(ctx context.Context, name string) ([]ResourceInfo, error) {
	st, ok := r.state(name)
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrUnknownServer, name)
	}
	st.mu.Lock()
	session := st.session
	connected := st.connected
	cfg := st.cfg
	supports := st.supports("resources")
	st.mu.Unlock()
	if !connected || session == nil {
		return nil, fmt.Errorf("mcp: server %q is not connected", name)
	}
	if !supports {
		return nil, nil
	}
	return discoverResources(ctx, session, cfg, r.secretVariants()), nil
}

// ReadResource reads one resource and returns its text, bounded.
//
// The text is untrusted data: it is returned to the operator as a
// document, never as an instruction.
func (r *Registry) ReadResource(ctx context.Context, name, uri string) (string, bool, error) {
	st, ok := r.state(name)
	if !ok {
		return "", false, fmt.Errorf("%w: %s", ErrUnknownServer, name)
	}
	st.mu.Lock()
	session := st.session
	connected := st.connected
	supports := st.supports("resources")
	st.mu.Unlock()
	if !connected || session == nil {
		return "", false, fmt.Errorf("mcp: server %q is not connected", name)
	}
	if !supports {
		return "", false, fmt.Errorf("mcp: server %q has no resources capability", name)
	}
	callCtx, cancel := context.WithTimeout(ctx, r.timeout(name))
	defer cancel()
	result, err := session.ReadResource(callCtx, &sdk.ReadResourceParams{URI: uri})
	if err != nil {
		return "", false, r.redactedError(name, err)
	}
	if result == nil {
		return "", false, errors.New("mcp: the server returned no resource")
	}
	redactor := newRedactor(r.configuredSecrets())
	var b strings.Builder
	truncated := false
	remaining := MaxResourceBytes
	for _, contents := range result.Contents {
		if contents == nil {
			continue
		}
		if remaining <= 0 {
			truncated = true
			break
		}
		if contents.Text == "" {
			line, _ := boundedText("[binary resource: "+cleanRemoteText(contents.MIMEType, MaxRemoteNameBytes)+"]", remaining)
			b.WriteString(line)
			b.WriteString("\n")
			remaining -= len(line) + 1
			continue
		}
		safe := redactor.text(contents.Text)
		if redactor.exhausted {
			truncated = true
		}
		text, cut := boundedText(safe, remaining)
		truncated = truncated || cut
		b.WriteString(text)
		b.WriteString("\n")
		remaining -= len(text) + 1
	}
	return b.String(), truncated, nil
}

// ListPrompts returns the prompts of one connected server.
func (r *Registry) ListPrompts(ctx context.Context, name string) ([]PromptInfo, error) {
	st, ok := r.state(name)
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrUnknownServer, name)
	}
	st.mu.Lock()
	session := st.session
	connected := st.connected
	cfg := st.cfg
	supports := st.supports("prompts")
	st.mu.Unlock()
	if !connected || session == nil {
		return nil, fmt.Errorf("mcp: server %q is not connected", name)
	}
	if !supports {
		return nil, nil
	}
	return discoverPrompts(ctx, session, cfg, r.secretVariants()), nil
}

// GetPrompt fetches one prompt and returns its messages as text.
func (r *Registry) GetPrompt(ctx context.Context, name, prompt string, args map[string]string) (string, error) {
	st, ok := r.state(name)
	if !ok {
		return "", fmt.Errorf("%w: %s", ErrUnknownServer, name)
	}
	st.mu.Lock()
	session := st.session
	connected := st.connected
	supports := st.supports("prompts")
	st.mu.Unlock()
	if !connected || session == nil {
		return "", fmt.Errorf("mcp: server %q is not connected", name)
	}
	if !supports {
		return "", fmt.Errorf("mcp: server %q has no prompts capability", name)
	}
	callCtx, cancel := context.WithTimeout(ctx, r.timeout(name))
	defer cancel()
	result, err := session.GetPrompt(callCtx, &sdk.GetPromptParams{Name: prompt, Arguments: args})
	if err != nil {
		return "", r.redactedError(name, err)
	}
	if result == nil {
		return "", errors.New("mcp: the server returned no prompt")
	}
	redactor := newRedactor(r.configuredSecrets())
	var b strings.Builder
	remaining := MaxPromptBytes
	for _, message := range result.Messages {
		if message == nil {
			continue
		}
		if remaining <= 0 || redactor.exhausted {
			break
		}
		safe := redactor.text(contentText(message.Content))
		text, cut := boundedText(safe, remaining)
		b.WriteString(text)
		b.WriteString("\n")
		remaining -= len(text) + 1
		if cut {
			break
		}
	}
	return b.String(), nil
}

// callTool runs one tool call on the server that owns it.
func (r *Registry) callTool(ctx context.Context, info ToolInfo, arguments map[string]any) (*sdk.CallToolResult, error) {
	st, ok := r.state(info.Server)
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrUnknownServer, info.Server)
	}
	st.mu.Lock()
	session := st.session
	connected := st.connected
	st.mu.Unlock()
	if !connected || session == nil {
		return nil, fmt.Errorf("mcp: server %q is not connected", info.Server)
	}
	return session.CallTool(ctx, &sdk.CallToolParams{Name: info.Name, Arguments: arguments})
}

// markDisconnected forgets a server whose connection is gone, so the
// operator sees the truth and the model sees no stale tools.
func (r *Registry) markDisconnected(name string, err error) {
	st, ok := r.state(name)
	if !ok {
		return
	}
	// Collect every configured credential before taking the state lock:
	// the redaction set must not be assembled while holding it.
	secrets := r.configuredSecrets()
	lost := false
	st.mu.Lock()
	if st.connected && isConnectionError(err) {
		st.connected = false
		st.session = nil
		st.tools = nil
		st.resources = nil
		st.prompts = nil
		st.err = "the connection was lost: " + redact(shortReason(err), secrets)
		lost = true
	}
	st.mu.Unlock()
	if lost {
		r.rebuildTools()
	}
}

// isConnectionError reports whether an error means the session is gone.
func isConnectionError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, sdk.ErrConnectionClosed) || errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
		return true
	}
	text := strings.ToLower(err.Error())
	for _, hint := range []string{"connection closed", "connection is closed", "broken pipe", "unexpected eof", "transport closed", "server closed"} {
		if strings.Contains(text, hint) {
			return true
		}
	}
	return false
}

// finish records the outcome in the shared trail.
//
// The record never carries the arguments, the result or a credential:
// only names, sizes, durations and the outcome.
func (r *Registry) finish(call provider.ToolCall, res Result, start time.Time) Result {
	res.Duration = r.now().Sub(start)
	mode := "444"
	if res.Mode == permission.Unrestricted {
		mode = "666"
	}
	r.mu.Lock()
	audit := r.audit
	session := r.session
	r.mu.Unlock()
	if audit != nil {
		rec := workspace.ToolRecord{
			Time: start, Mode: mode, Tool: call.Name, Action: res.Action,
			Target: res.Target, Outcome: res.Status, Bytes: res.Bytes,
			DurationMS: res.Duration.Milliseconds(), Truncated: res.Truncated,
		}
		if res.Status != "ok" {
			rec.Reason = res.Status
		}
		if session != nil {
			rec.Session = session()
		}
		_ = audit.AppendTool(rec)
	}
	return res
}

// actionName is the display- and audit-safe form of a server tool name.
// The real name is used for the protocol call; this form only appears in
// the activity line and the evidence record, where a control character
// from a server must never reach the terminal or a stored line.
func actionName(tool string) string {
	name := sanitizeName(tool)
	if name == "" {
		return "tool"
	}
	return name
}

// configuredSecrets collects the raw credentials of every server. A
// result from one server is redacted against every configured secret, not
// only its own, because a hostile server can echo another server's value
// if it learns it.
func (r *Registry) configuredSecrets() []string {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	states := make([]*serverState, 0, len(r.servers))
	for _, st := range r.servers {
		states = append(states, st)
	}
	r.mu.Unlock()
	seen := map[string]bool{}
	var secrets []string
	for _, st := range states {
		st.mu.Lock()
		for _, secret := range st.secrets {
			if secret != "" && !seen[secret] {
				seen[secret] = true
				secrets = append(secrets, secret)
			}
		}
		st.mu.Unlock()
	}
	return secrets
}

// secretVariants returns every representation of every configured
// credential, ready for direct replacement.
func (r *Registry) secretVariants() []string {
	return redactionVariants(r.configuredSecrets())
}

// redactedError returns an error whose text carries no credential. When
// no credential was present the original error is returned unchanged, so
// callers that inspect an error keep working.
func (r *Registry) redactedError(name string, err error) error {
	if err == nil {
		return nil
	}
	text := r.redactedMessage(name, err)
	if text == "" || text == err.Error() {
		return err
	}
	return errors.New(text)
}

// redactedMessage renders one error without any configured credential.
func (r *Registry) redactedMessage(_ string, err error) string {
	if err == nil {
		return ""
	}
	return replaceRedactions(shortReason(err), r.secretVariants())
}

// state returns one configured server.
func (r *Registry) state(name string) (*serverState, bool) {
	if r == nil {
		return nil, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	st, ok := r.servers[name]
	return st, ok
}

// lookup finds one exposed tool.
func (r *Registry) lookup(name string) (ToolInfo, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.collisions[name] {
		return ToolInfo{}, false
	}
	info, ok := r.tools[name]
	return info, ok
}

// mode is the current permission mode.
func (r *Registry) mode() permission.Mode {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.gate == nil {
		return permission.Guarded
	}
	return r.gate.Mode()
}

// timeout is the call timeout of one server.
func (r *Registry) timeout(name string) time.Duration {
	r.mu.Lock()
	defer r.mu.Unlock()
	st, ok := r.servers[name]
	if !ok {
		return DefaultTimeout
	}
	return st.cfg.timeout()
}

// toolNames returns the exposed tool names in a stable order.
func (r *Registry) toolNames() []string {
	names := make([]string, 0, len(r.tools))
	for name := range r.tools {
		names = append(names, name)
	}
	sortStrings(names)
	return names
}

// rebuildTools recomputes the exposed tool table.
//
// It is the only place that reads a server's tool list, so a server
// that is not connected contributes nothing.
func (r *Registry) rebuildTools() {
	r.mu.Lock()
	names := append([]string(nil), r.order...)
	states := make(map[string]*serverState, len(names))
	for _, name := range names {
		states[name] = r.servers[name]
	}
	r.mu.Unlock()

	tools := map[string]ToolInfo{}
	collisions := map[string]bool{}
	for _, name := range names {
		st := states[name]
		st.mu.Lock()
		var list []ToolInfo
		if st.connected {
			list = append([]ToolInfo(nil), st.tools...)
		}
		st.mu.Unlock()
		for _, info := range list {
			if collisions[info.Exposed] {
				continue
			}
			if existing, ok := tools[info.Exposed]; ok &&
				(existing.Server != info.Server || existing.Name != info.Name) {
				// Two distinct tools want one name. Offering either would
				// let one server answer for the other, so neither is
				// offered. This is a fail-closed decision.
				delete(tools, info.Exposed)
				collisions[info.Exposed] = true
				continue
			}
			tools[info.Exposed] = info
		}
	}
	r.mu.Lock()
	r.tools = tools
	r.collisions = collisions
	r.mu.Unlock()
}

// discoverTools lists every tool of one server, following the cursor.
//
// A hostile server cannot make the client loop forever: pages and cursors
// are bounded, and a repeated cursor ends the listing.
func discoverTools(ctx context.Context, session *sdk.ClientSession, cfg Server, variants []string) ([]ToolInfo, error) {
	var out []ToolInfo
	cursor := ""
	seen := map[string]bool{}
	for page := 0; page < maxListPages; page++ {
		params := &sdk.ListToolsParams{Cursor: cursor}
		result, err := session.ListTools(ctx, params)
		if err != nil {
			return nil, err
		}
		if result == nil {
			return out, nil
		}
		for _, tool := range result.Tools {
			if tool == nil || !validRemoteName(tool.Name, MaxRemoteToolNameBytes) {
				continue
			}
			if len(out) >= MaxToolsPerServer {
				return out, nil
			}
			out = append(out, toolInfo(cfg, tool, variants))
		}
		cursor = result.NextCursor
		if cursor == "" || seen[cursor] {
			return out, nil
		}
		seen[cursor] = true
	}
	return out, nil
}

// discoverResources lists the resources of one server.
func discoverResources(ctx context.Context, session *sdk.ClientSession, cfg Server, variants []string) []ResourceInfo {
	var out []ResourceInfo
	cursor := ""
	seen := map[string]bool{}
	for page := 0; page < maxListPages; page++ {
		result, err := session.ListResources(ctx, &sdk.ListResourcesParams{Cursor: cursor})
		if err != nil || result == nil {
			return out
		}
		for _, resource := range result.Resources {
			if resource == nil || !validRemoteName(resource.URI, MaxResourceURIBytes) ||
				!validRemoteName(resource.Name, MaxRemoteNameBytes) {
				continue
			}
			if len(out) >= MaxResourcesPerServer {
				continue
			}
			out = append(out, ResourceInfo{
				Server:      cfg.Name,
				URI:         replaceRedactions(resource.URI, variants),
				Name:        replaceRedactions(resource.Name, variants),
				Description: redactAndBound(resource.Description, variants, MaxDescriptionBytes),
				MIMEType:    replaceRedactions(resource.MIMEType, variants),
				Size:        resource.Size,
			})
		}
		cursor = result.NextCursor
		if cursor == "" || seen[cursor] {
			return out
		}
		seen[cursor] = true
	}
	return out
}

// discoverPrompts lists the prompts of one server.
func discoverPrompts(ctx context.Context, session *sdk.ClientSession, cfg Server, variants []string) []PromptInfo {
	var out []PromptInfo
	cursor := ""
	seen := map[string]bool{}
	for page := 0; page < maxListPages; page++ {
		result, err := session.ListPrompts(ctx, &sdk.ListPromptsParams{Cursor: cursor})
		if err != nil || result == nil {
			return out
		}
		for _, prompt := range result.Prompts {
			if prompt == nil || !validRemoteName(prompt.Name, MaxRemoteNameBytes) {
				continue
			}
			if len(out) >= MaxPromptsPerServer {
				continue
			}
			info := PromptInfo{
				Server:      cfg.Name,
				Name:        replaceRedactions(prompt.Name, variants),
				Description: redactAndBound(prompt.Description, variants, MaxDescriptionBytes),
			}
			for _, argument := range prompt.Arguments {
				if argument == nil || !validRemoteName(argument.Name, MaxRemoteNameBytes) {
					continue
				}
				info.Arguments = append(info.Arguments, PromptArgument{
					Name:        replaceRedactions(argument.Name, variants),
					Description: redactAndBound(argument.Description, variants, MaxDescriptionBytes),
					Required:    argument.Required,
				})
			}
			out = append(out, info)
		}
		cursor = result.NextCursor
		if cursor == "" || seen[cursor] {
			return out
		}
		seen[cursor] = true
	}
	return out
}

// capabilityNames lists what a server says it can do.
//
// The list is descriptive only: it grants nothing. HAL's own permission
// gate decides what may run.
func capabilityNames(caps *sdk.ServerCapabilities) []string {
	if caps == nil {
		return nil
	}
	var out []string
	if caps.Tools != nil {
		out = append(out, "tools")
	}
	if caps.Resources != nil {
		out = append(out, "resources")
	}
	if caps.Prompts != nil {
		out = append(out, "prompts")
	}
	if caps.Completions != nil {
		out = append(out, "completions")
	}
	if caps.Logging != nil {
		out = append(out, "logging")
	}
	return out
}

// toolInfo describes one server tool.
//
// The exposed name is built from the redacted display name: a hostile
// server must not be able to smuggle a credential into the function name
// the model sees. The protocol still uses the server's exact name.
func toolInfo(cfg Server, tool *sdk.Tool, variants []string) ToolInfo {
	schema := schemaJSON(tool.InputSchema)
	if len(variants) > 0 {
		if safe := replaceRedactions(schema, variants); safe != schema {
			if json.Valid([]byte(safe)) {
				schema = safe
			} else {
				schema = genericSchema
			}
		}
	}
	displayName := replaceRedactions(tool.Name, variants)
	return ToolInfo{
		Exposed:      ExposedName(cfg.Name, displayName),
		Server:       cfg.Name,
		Name:         tool.Name,
		Description:  redactAndBound(tool.Description, variants, MaxDescriptionBytes),
		Schema:       schema,
		AllowedIn444: cfg.allowsIn444(tool.Name),
	}
}

// redactJSONText redacts a compact JSON document while preserving its
// validity. If replacement would corrupt the document it falls back to
// the permissive schema rather than emit broken JSON.
func redactJSONText(raw string, variants []string) string {
	if raw == "" || len(variants) == 0 {
		return raw
	}
	safe := replaceRedactions(raw, variants)
	if safe == raw {
		return raw
	}
	if json.Valid([]byte(safe)) {
		return safe
	}
	return genericSchema
}

// redactAndBound applies redaction before the byte bound, so a secret
// cannot survive as an unbounded suffix. A description larger than the
// redactor's whole budget is withheld rather than scanned partially.
func redactAndBound(text string, variants []string, maxBytes int) string {
	if len(text) > maxRedactBytes {
		return withheldText
	}
	return boundText(replaceRedactions(text, variants), maxBytes)
}

// exposedDescription is the description the model sees.
//
// The server's own words are marked as untrusted, so a tool
// description cannot pose as an instruction from the operator.
func exposedDescription(info ToolInfo) string {
	description := strings.TrimSpace(info.Description)
	if description == "" {
		description = "(the server gave no description)"
	}
	return "[MCP server " + info.Server + " — untrusted description] " + description
}

// boundText cuts a plain text to a byte budget.
func boundText(text string, maxBytes int) string {
	out, _ := boundedText(text, maxBytes)
	return out
}

// contentText renders one content item as text.
func contentText(item sdk.Content) string {
	if item == nil {
		return ""
	}
	if text, ok := item.(*sdk.TextContent); ok {
		return text.Text
	}
	raw, err := json.Marshal(item)
	if err != nil {
		return ""
	}
	return string(raw)
}

// shortReason is a one-line reason for the interface and the model.
func shortReason(err error) string {
	if err == nil {
		return ""
	}
	text := strings.TrimSpace(err.Error())
	if i := strings.IndexByte(text, '\n'); i >= 0 {
		text = text[:i]
	}
	if len(text) > 200 {
		text = text[:200]
	}
	return text
}

// sortStrings sorts a small string slice in place.
func sortStrings(values []string) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}
