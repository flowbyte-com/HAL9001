// Command hal9001 launches HAL9001's native terminal interface: a
// conversational thinking partner with permanent 444 read-only authority.
// On first run it asks for three values (provider endpoint, model,
// API key) and stores them in HAL9001's own user configuration directory
// with restrictive permissions. It never reads PicoClaw, OpenCode or MPM
// credentials.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/flowbyte-com/HAL9001/internal/agenttools"
	"github.com/flowbyte-com/HAL9001/internal/attachments"
	"github.com/flowbyte-com/HAL9001/internal/buildinfo"
	"github.com/flowbyte-com/HAL9001/internal/clipboard"
	"github.com/flowbyte-com/HAL9001/internal/config"
	"github.com/flowbyte-com/HAL9001/internal/engine"
	"github.com/flowbyte-com/HAL9001/internal/firecrawl"
	"github.com/flowbyte-com/HAL9001/internal/knowledge"
	"github.com/flowbyte-com/HAL9001/internal/mcp"
	"github.com/flowbyte-com/HAL9001/internal/permission"
	"github.com/flowbyte-com/HAL9001/internal/persona"
	"github.com/flowbyte-com/HAL9001/internal/provider"
	"github.com/flowbyte-com/HAL9001/internal/session"
	"github.com/flowbyte-com/HAL9001/internal/skills"
	"github.com/flowbyte-com/HAL9001/internal/tui"
	"github.com/flowbyte-com/HAL9001/internal/usage"
	"github.com/flowbyte-com/HAL9001/internal/websearch"
	"github.com/flowbyte-com/HAL9001/internal/workspace"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "hal9001:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	// Version reporting must work even when the configuration file is
	// missing or damaged, so it is handled before any configuration is
	// read.
	if wantsVersion(args) {
		fmt.Print(buildinfo.Report("HAL9001"))
		return nil
	}
	// The configuration is migrated before it is read, so that the file
	// the operator already has becomes a provider-scoped one. The
	// migration is idempotent, atomic and keeps a backup of the
	// original; a file that is already current is left alone.
	migration, err := config.Migrate(config.ConfigPath())
	if err != nil {
		fmt.Fprintln(os.Stderr, "hal9001: the configuration could not be migrated:", err)
	}
	cfg, err := config.LoadErr(args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil // usage already printed
		}
		return err
	}
	if cfg.ShowVersion {
		fmt.Print(buildinfo.Report("HAL9001"))
		return nil
	}
	if cfg.NoColor {
		os.Setenv("NO_COLOR", "1")
	}
	if cfg.List {
		return listSessions(cfg.StoreDir)
	}
	// The terminal's mouse modes are restored on every exit path: a
	// clean quit, a cancellation, and an error on the way out.
	if cfg.Mouse {
		defer restoreMouseModes()
	}
	stored, err := config.LoadFile(config.ConfigPath())
	if err != nil {
		return err
	}
	app := &app{
		cfg:       cfg,
		stored:    stored,
		model:     cfg.Model,
		migration: migration,
	}
	// Token accounting is best effort: a failure must never stop the
	// conversation from starting.
	if store, err := usage.Open(filepath.Join(config.ConfigDir(), "usage.json")); err == nil {
		app.usage = store
	}
	// The one directory a proposed write may reach. Opening it writes
	// nothing: the store only resolves the path, and the directory is
	// created when an approved write needs it.
	ws, err := workspace.Open(cfg.WorkspaceDir)
	if err != nil {
		return err
	}
	// The audit trail is trusted bookkeeping under HAL's own data
	// directory; a failure to open it leaves the workspace working
	// without a trail rather than blocking the conversation.
	var audit *workspace.Audit
	if opened, err := workspace.OpenAudit(filepath.Join(config.ConfigDir(), "audit.jsonl")); err == nil {
		audit = opened
		ws.WithAudit(audit)
	}
	// Defence in depth: even a misconfigured workspace can never reach
	// HAL's own configuration and session data.
	ws.Protect(config.ConfigDir())
	app.workspace = ws
	// The upload store. A file the operator attaches is copied into
	// ~/HAL/uploads, and the copy is what the conversation discusses.
	// The store is created once and shared: the interface copies files
	// into it on the operator's command, and the engine reads only the
	// copies the operator already made.
	store, err := attachments.Open(filepath.Join(ws.Root(), "uploads"))
	if err != nil {
		return err
	}
	app.attachments = store
	// The MPM skill store. HAL reads skills from it and never writes to
	// it: the catalogue is fetched once at startup and refreshed on
	// demand. A missing store is not a reason to refuse to start.
	app.skills = skills.NewSelector(&skills.Client{})
	// The permission controller is the only object that can enter 666.
	// The engine receives it as a read-only Gate and the interface holds
	// the controller; the tool executor and MPM registry record activity
	// without being able to change the mode.
	app.permission = permission.New()
	app.tools = agenttools.New(app.permission, audit).
		WithSession(func() string { return app.session })
	app.knowledge = knowledge.NewRegistry(&knowledge.Client{}).
		WithAudit(audit, func() string { return app.session }).
		WithMode(app.permission.Mode)
	// MCP servers are operator-controlled: the configuration file is the
	// only source of servers, and HAL never creates, edits or enables one
	// from a model response. A configured server is still only reachable
	// after an explicit /mcp connect, so a bad or compromised server does
	// not run merely because HAL started.
	app.mcp = mcp.NewRegistry(app.permission).
		WithAudit(audit, func() string { return app.session })
	for _, record := range cfg.MCPServers {
		server := mcpServer(record)
		if err := app.mcp.AddServer(server); err != nil {
			return err
		}
	}
	// Live web research is read-only and available in 444. Firecrawl is
	// primary, Exa is the API fallback and DuckDuckGo Lite is keyless.
	// Keys come only from the environment and never enter the session or
	// audit.
	webSettings := websearch.SettingsFromEnv()
	firecrawlSettings := firecrawl.SettingsFromEnv()
	// The unified limit is the contract; keep the Firecrawl client from
	// applying a second, phase-1 cap to the same query.
	firecrawlSettings.SearchLimit = webSettings.SearchLimit
	firecrawlClient := firecrawl.New(firecrawlSettings)
	app.web = websearch.NewRegistry(
		websearch.NewFallback(
			websearch.NewFirecrawlProvider(firecrawlClient),
			websearch.NewExaProvider(websearch.ExaSettingsFromEnv()),
			websearch.NewDuckDuckGoProvider(websearch.DuckDuckGoSettingsFromEnv()),
		),
		websearch.NewFirecrawlScraper(firecrawlClient),
		webSettings,
	).
		WithAudit(audit, func() string { return app.session }).
		WithMode(app.permission.Mode)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := app.skills.Refresh(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "hal9001: skills unavailable:", err)
	}
	return app.run()
}

// wantsVersion recognizes the version flag before configuration is
// loaded. The flag package would otherwise read ~/.hal9001/config.json
// first, so a damaged config could make `hal --version` fail.
func wantsVersion(args []string) bool {
	for _, arg := range args {
		if arg == "--version" || arg == "-version" {
			return true
		}
	}
	return false
}

func listSessions(storeDir string) error {
	names, err := config.ListSessions(storeDir)
	if err != nil {
		return err
	}
	if len(names) == 0 {
		fmt.Println("No stored conversations yet.")
		return nil
	}
	for _, name := range names {
		fmt.Println(name)
	}
	return nil
}

// restoreMouseModes turns the terminal's mouse reporting off.
//
// Bubble Tea already resets the modes when the program closes. Writing
// the reset again is cheap insurance for the paths where the program
// dies before the renderer gets there, and it leaves the terminal
// exactly as HAL found it.
func restoreMouseModes() {
	f, err := os.OpenFile("/dev/tty", os.O_WRONLY, 0)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.WriteString("\x1b[?1002l\x1b[?1003l\x1b[?1006l")
}

type app struct {
	cfg    config.Config
	stored config.FileConfig
	// migration is what the startup migration did, if anything. It never
	// contains a credential.
	migration config.MigrationReport
	// providerErr is why the active endpoint could not be used, if that
	// is the case. It never contains a credential.
	providerErr  error
	store        *session.Store
	eng          *engine.Engine
	model        string
	session      string
	sessionTitle string
	sessionSeq   int
	usage        *usage.Store
	// workspace is the single write capability shared by the engine
	// (which prepares proposals) and the interface (which approves them).
	workspace *workspace.Store
	// attachments is the upload store. It copies the files the operator
	// chooses into ~/HAL/uploads. Only the interface can start an
	// ingestion, and no model-facing tool reaches it.
	attachments *attachments.Store
	// skills is the MPM skill store, shared by the engine (which routes
	// each turn) and the interface (which lists and pins skills).
	skills *skills.Selector
	// permission is the trusted session-scoped 444/666 control, shared
	// with the interface. The engine only sees its Gate.
	permission *permission.Controller
	// tools performs the 666 read/write/edit/bash set.
	tools *agenttools.Executor
	// knowledge exposes MPM's own typed knowledge tools in both modes.
	knowledge *knowledge.Registry
	// web exposes Firecrawl's read-only research tools in 444 and 666.
	web *websearch.Registry
	// mcp is the native Model Context Protocol client. It contributes
	// the tools of the configured servers, but it is not a permission
	// authority: every call is checked at its execution boundary.
	mcp *mcp.Registry
	// contexts holds the context metadata an endpoint reported for a
	// model, keyed by the model identifier. It is the input to the
	// context-budget resolution, and it keeps the provenance of every
	// number it holds.
	//
	// The background lookup writes it and the resolution reads it, so
	// it is guarded by contextMu.
	contexts map[string]provider.ContextMetadata
	// contextMu guards the recorded context metadata.
	contextMu sync.Mutex
}

// mcpServer converts one stored configuration record into a client
// server definition. Validation happens in AddServer, so a bad record is
// rejected once, with a message that names the server.
func mcpServer(record config.MCPServerRecord) mcp.Server {
	return mcp.Server{
		Name:         record.Name,
		Transport:    mcp.Transport(record.Transport),
		Command:      record.Command,
		Args:         record.Args,
		Env:          record.Env,
		CWD:          record.CWD,
		URL:          record.URL,
		Headers:      record.Headers,
		TokenEnv:     record.TokenEnv,
		Timeout:      time.Duration(record.TimeoutSeconds) * time.Second,
		AllowedTools: record.AllowedTools,
	}
}

// defaultSessionName is the conversation used when nothing has been
// recorded yet. It matches the name older installations already use.
const defaultSessionName = "cli"

// resolveSession picks the conversation to open. Precedence: an explicit
// --session, then the session recorded as active, then the most recently
// modified stored conversation (for installations that predate the
// metadata), then an empty default. It never falls back to the earliest
// session when a newer one is known.
func (a *app) resolveSession() string {
	if explicit := strings.TrimSpace(a.cfg.Session); explicit != "" {
		return explicit
	}
	if stored := strings.TrimSpace(a.stored.ActiveSession); stored != "" && session.ValidName(stored) {
		return stored
	}
	if newest := a.newestSession(); newest != "" {
		return newest
	}
	return defaultSessionName
}

// newestSession returns the most recently modified stored conversation.
func (a *app) newestSession() string {
	names, err := config.ListSessions(a.cfg.StoreDir)
	if err != nil || len(names) == 0 {
		return ""
	}
	best := ""
	var bestTime time.Time
	for _, name := range names {
		if !session.ValidName(name) {
			continue
		}
		info, err := os.Stat(filepath.Join(a.cfg.StoreDir, name+".jsonl"))
		if err != nil {
			continue
		}
		if best == "" || info.ModTime().After(bestTime) {
			best, bestTime = name, info.ModTime()
		}
	}
	return best
}

// persistActiveSession records the conversation for the next launch. It
// is written before the in-memory switch, so a failure leaves the app and
// the interface consistent.
func (a *app) persistActiveSession(name string) error {
	if a.stored.ActiveSession == name {
		return nil
	}
	previous := a.stored.ActiveSession
	a.stored.ActiveSession = name
	if err := config.SaveFile(config.ConfigPath(), a.stored); err != nil {
		a.stored.ActiveSession = previous
		return fmt.Errorf("cannot record the active session: %w", err)
	}
	return nil
}

// openSession resolves and opens the conversation for this launch, then
// records it as the active session for the next one.
func (a *app) openSession() error {
	name := a.resolveSession()
	store, err := session.Open(a.cfg.StoreDir, name)
	if err != nil {
		return err
	}
	a.store = store
	a.session = name
	a.sessionTitle = a.displayTitle(name)
	if a.permission != nil {
		a.permission.SetSession(name)
	}
	// Best effort on startup: a read-only configuration must not stop the
	// conversation from starting.
	_ = a.persistActiveSession(name)
	a.rebuildEngine()
	return nil
}

func (a *app) run() error {
	if err := a.openSession(); err != nil {
		return err
	}
	// A connected server is a child process or an HTTP session. Closing
	// the registry on every exit path means HAL never leaves a local
	// server running behind it.
	if a.mcp != nil {
		defer a.mcp.Close()
	}

	hooks := tui.Hooks{
		SaveOnboard: a.saveOnboard,
		TestOnboard: a.testOnboard,
		HasConfig:   a.hasConfig,
		ListModels:  a.listModels,
		SwitchModel: a.switchModel,
		RefreshEngine: func() tui.Engine {
			// A typed nil pointer must never reach the interface:
			// it would look like an engine and panic on the first
			// request.
			if a.eng == nil {
				return nil
			}
			return a.eng
		},
		Endpoints:      a.endpointInfos,
		SaveEndpoint:   a.saveEndpoint,
		DeleteEndpoint: a.deleteEndpoint,
		AddModel:       a.addModel,
		EditModel:      a.editModel,
		DeleteModel:    a.deleteModel,
		ModelDetails:   a.modelDetails,
		Discover:       a.discover,
		CurrentOnboard: a.currentOnboard,
		NewSession:     a.newSession,
		Theme:          a.currentTheme,
		SaveTheme:      a.saveTheme,
		ListSessions:   a.listSessionNames,
		SwitchSession:  a.switchSession,
		Copy:           a.copyText,
		Effort:         a.currentEffort,
		SetEffort:      a.setEffort,
		Usage:          a.currentUsage,
		RecordTurn:     a.recordTurn,
		RecordFailed:   a.recordFailed,
		RenameSession:  a.renameSession,
		SessionTitle:   func() string { return a.sessionTitle },
		SessionID:      func() string { return a.session },
		WebResearch:    a.webStatus,
		ContextBudget:  a.contextBudgetDescription,
	}
	m := tui.NewWithHooks(a.eng, hooks)
	m.SetModelName(a.activeModelID())
	m.SetEffort(a.currentEffort())
	m.SetUsage(a.currentUsage())
	m.SetMouse(a.cfg.Mouse)
	m.SetWorkspace(a.workspace)
	m.SetSkills(a.skills)
	m.SetAttachments(a.attachments)
	if a.mcp != nil {
		m.SetMCP(a.mcp)
	}
	m.SetPermission(a.permission)
	m.SetTheme(tui.ThemeByName(a.stored.Theme))
	// The header shows the human-readable title; the immutable session
	// identity stays in the diagnostics.
	m.SetTitle(a.displayTitle(a.session))
	m.SetSessionID(a.session)
	if history, err := a.store.History(); err == nil {
		m.SetHistory(sessionLines(history))
	}
	if !a.hasConfig() {
		m.EnterOnboarding()
	}
	a.setMigrationNotice(m)
	if a.providerErr != nil {
		m.SetNotice("The configured endpoint cannot be used: " + a.providerErr.Error())
	}
	return tui.RunWithModel(m)
}

// hasConfig reports whether a provider is configured.
//
// A local endpoint needs no credential, so the absence of a key does not
// mean that the installation is unconfigured.
func (a *app) hasConfig() bool {
	if a.cfg.Endpoint == "" || a.cfg.Model == "" {
		return false
	}
	if a.cfg.APIKey != "" {
		return true
	}
	if endpoint, ok := a.stored.ActiveEndpoint(); ok {
		return endpoint.Protocol == config.ProtocolLocal
	}
	return false
}

// seconds converts a configured second count to a duration, leaving zero
// as "use the built-in default".
func seconds(n int) time.Duration {
	if n <= 0 {
		return 0
	}
	return time.Duration(n) * time.Second
}

// effortFor resolves the effort level for a model: the operator's stored
// choice when present, otherwise the built-in default for capable models
// and nothing for everything else.
func (a *app) effortFor(id string) string {
	if !provider.EffortCapable(id) {
		return ""
	}
	// The effort belongs to the model, and a model belongs to one
	// endpoint: the record of the active endpoint is the one that counts.
	if endpoint, ok := a.stored.ActiveEndpoint(); ok {
		if record, ok := endpoint.Model(id); ok {
			if stored := provider.NormalizeEffort(record.Effort); stored != "" {
				return stored
			}
			return provider.DefaultEffortFor(id)
		}
	}
	if stored := provider.NormalizeEffort(a.stored.EffortFor(id)); stored != "" {
		return stored
	}
	return provider.DefaultEffortFor(id)
}

// currentEffort reports the active level and whether the active model
// supports effort control at all.
func (a *app) currentEffort() (string, bool) {
	id := a.activeModelID()
	if !provider.EffortCapable(id) {
		return "", false
	}
	return a.effortFor(id), true
}

// setEffort persists a reasoning level for the active model and rebuilds
// the engine, so the next request carries it.
func (a *app) setEffort(level string) (tui.Engine, error) {
	id := a.activeModelID()
	if !provider.EffortCapable(id) {
		return nil, fmt.Errorf("model %q does not support reasoning effort", id)
	}
	normalized := provider.NormalizeEffort(level)
	if normalized == "" {
		return nil, fmt.Errorf("unknown effort level %q", level)
	}
	// The change is prepared on a copy and only adopted once it is
	// written, so a failed write leaves the running configuration and
	// the file in step.
	next := a.stored.Clone()
	endpointID := next.ActiveEndpointID()
	if endpointID != "" {
		if _, ok := next.EndpointByID(endpointID); ok {
			if err := next.UpdateModel(endpointID, id, func(record *config.ModelRecord) error {
				record.Effort = normalized
				return nil
			}); err != nil {
				return nil, err
			}
			if err := a.commitConfig(next); err != nil {
				return nil, err
			}
			a.rebuildEngine()
			return a.eng, nil
		}
	}
	next.SetEffort(id, normalized)
	if err := a.commitConfig(next); err != nil {
		return nil, err
	}
	a.rebuildEngine()
	return a.eng, nil
}

func (a *app) rebuildEngine() {
	// The provider and the model are resolved from one target, so a
	// request can never carry the endpoint of one configuration and the
	// credential of another.
	target, err := a.resolveTarget(a.stored)
	var p provider.Provider
	if err == nil {
		p, err = a.providerForTarget(target)
	}
	if err != nil {
		// The configuration cannot be used. The last working runtime
		// stays, so a broken endpoint never takes the conversation
		// down and never sends a request to the wrong place.
		a.providerErr = err
		if a.eng != nil {
			return
		}
	} else {
		a.providerErr = nil
	}
	// A typed nil pointer must never reach the provider interface: the
	// engine's "no provider" check would pass and the first request would
	// dereference a nil client.
	// The budget is resolved again for every request: an endpoint may
	// report a model's context window after the engine was built, and
	// the operator's own limit never changes. The model identifier, the
	// operator's limit and the room the answer may use are read here,
	// on the interface's own thread. The request path only reads the
	// guarded metadata the background lookup recorded.
	budgetID := target.ModelID
	budgetLimit := target.ContextWindow
	budgetOutput := target.outputTokens()
	a.eng = engine.New(a.store, p, modelFromTarget(target)).
		WithSystemPrompt(persona.Identity("native-foundation", a.cfg.StoreDir)).
		WithWorkspace(a.workspace).
		WithSkills(a.skills).
		WithPermission(a.permission).
		WithExecution(a.tools).
		WithWebResearch(a.web).
		WithKnowledge(a.knowledge).
		WithAttachments(a.attachments).
		// The budget is resolved per request: the endpoint may report a
		// model's context window after the engine was built, and the
		// operator's own limit never changes.
		WithBudgetResolver(func() provider.ContextBudget {
			return a.currentBudget(budgetID, budgetLimit, budgetOutput)
		})
	// The MCP registry is wired in on its own, and only when there is
	// one. Folding it into the chain above would pass a typed nil
	// interface into the engine, and the engine would then call through
	// it at startup.
	if a.mcp != nil {
		a.eng.WithMCP(a.mcp)
	}
}

// webStatus is the content-free Firecrawl line shown in /diag.
func (a *app) webStatus() string {
	if a.web == nil {
		return "web research · unavailable"
	}
	return a.web.StatusLine()
}

// saveOnboard stores the first-run values.
//
// The values belong to an endpoint, not to the whole installation: the
// active endpoint is completed, and a new one is created when there is
// none yet. The model is added to that endpoint, and only then does it
// become the active one.
//
// The protocol the operator configured is kept. Onboarding adds the
// endpoint, the model and the key; it does not decide which protocol
// the endpoint speaks. Forcing "openai-compatible" here is what made an
// Anthropic-compatible endpoint impossible to configure: the choice was
// overwritten before it could be made.
func (a *app) saveOnboard(d tui.OnboardData) (tui.Engine, error) {
	next := a.stored.Clone()
	endpointID := next.ActiveEndpointID()
	if endpointID == "" {
		endpointID = "default"
	}
	if _, ok := next.EndpointByID(endpointID); !ok {
		rec := config.EndpointRecord{
			ID:         endpointID,
			Name:       endpointID,
			Protocol:   a.onboardProtocol(),
			BaseURL:    d.Endpoint,
			Credential: d.Key,
			Discovery:  config.DiscoveryRecord{Enabled: true},
		}
		if err := next.AddEndpoint(rec); err != nil {
			return nil, err
		}
	} else if err := next.UpdateEndpoint(endpointID, func(e *config.EndpointRecord) error {
		if a.cfg.Endpoint == "" {
			e.BaseURL = d.Endpoint
		}
		if a.cfg.APIKey == "" {
			e.Credential = d.Key
			e.CredentialEnv = ""
		}
		return nil
	}); err != nil {
		return nil, err
	}
	if _, ok := next.ModelFor(endpointID, d.Model); !ok {
		if err := next.AddModelTo(endpointID, config.ModelRecord{
			ID:          d.Model,
			ContextMode: config.ContextAuto,
			Source:      config.SourceManual,
		}); err != nil {
			return nil, err
		}
	}
	next.SetActive(endpointID, d.Model)
	if err := a.commitTarget(next); err != nil {
		return nil, err
	}
	a.model = d.Model
	// The context the endpoint reports for the new model is looked up
	// in the background, so the interface never waits for a metadata
	// lookup. The budget the engine enforces is resolved again on the
	// next request, so a late answer is still used.
	a.refreshContextInBackground(d.Model)
	return a.eng, nil
}

// onboardProtocol returns the protocol a new endpoint gets.
//
// The protocol the operator chose is kept. Onboarding adds the endpoint,
// the model and the key; it does not decide which protocol the endpoint
// speaks. Forcing "openai-compatible" here is what made an
// Anthropic-compatible endpoint impossible to configure: the choice was
// overwritten before it could be made.
func (a *app) onboardProtocol() config.Protocol {
	// An explicit operator choice wins.
	if a.cfg.FlagGiven("provider") {
		return protocolFromName(a.cfg.Provider)
	}
	if name := strings.TrimSpace(os.Getenv(config.EnvProvider)); name != "" {
		return protocolFromName(name)
	}
	// An endpoint that already exists keeps its protocol.
	if endpoint, ok := a.stored.ActiveEndpoint(); ok {
		return endpoint.Protocol
	}
	return protocolFromName(a.cfg.Provider)
}

// protocolFromName maps a configured protocol name onto the taxonomy.
func protocolFromName(name string) config.Protocol {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case string(config.ProtocolAnthropic), "anthropic":
		return config.ProtocolAnthropic
	case string(config.ProtocolLocal), "ollama", "llamacpp":
		return config.ProtocolLocal
	case string(config.ProtocolCustom):
		return config.ProtocolCustom
	default:
		return config.ProtocolOpenAI
	}
}

// testOnboard checks that the endpoint answers and reports how many
// models it lists.
//
// The endpoint is spoken to with the protocol the operator configured,
// so an Anthropic-compatible endpoint is tested the way it will be
// used. An endpoint that offers no listing is reported as such: the
// operator can still add a model identifier by hand.
func (a *app) testOnboard(d tui.OnboardData) (string, error) {
	wire, err := wireProtocolFor(string(a.onboardProtocol()))
	if err != nil {
		return "", err
	}
	if err != nil {
		return "", err
	}
	client, err := provider.NewForProtocol(wire, d.Endpoint, d.Key, provider.Options{
		DefaultMaxTokens: a.cfg.MaxTokens,
		IdleTimeout:      seconds(a.cfg.StreamIdleSeconds),
		TurnTimeout:      seconds(a.cfg.TurnTimeoutSeconds),
	})
	if err != nil {
		return "", err
	}
	// The catalogue is preferred when the endpoint offers one: it
	// carries the display names the endpoint reported, and a display
	// name is untrusted text that has to be sanitised before it
	// reaches the interface.
	if catalog, ok := client.(provider.ModelCatalog); ok {
		models, err := provider.Discover(context.Background(), catalog, provider.DiscoveryOptions{Limit: 5})
		if err == nil && len(models) > 0 {
			return describeDiscoveredModels(models), nil
		}
		if err != nil && !errors.Is(err, provider.ErrNoModelListing) {
			return "", err
		}
	}
	lister, ok := client.(provider.ModelLister)
	if !ok {
		return "", fmt.Errorf("the %s protocol cannot list models", wire)
	}
	ids, err := lister.Models(context.Background())
	if err != nil {
		if errors.Is(err, provider.ErrNoModelListing) {
			return "reachable, but it does not list its models; add the model id by hand", nil
		}
		return "", err
	}
	switch len(ids) {
	case 0:
		return "reachable, no models listed", nil
	case 1:
		return "reachable, 1 model", nil
	default:
		return fmt.Sprintf("reachable, %d models", len(ids)), nil
	}
}

// discoveredNameLimit bounds one model name in the connection summary.
//
// The summary is one line of interface text: a name that is too long
// belongs in the model list, not in a notice.
const discoveredNameLimit = 48

// describeDiscoveredModels names the models an endpoint reported.
//
// The names are display-only text and are sanitised again here, so a
// name that carries an escape sequence or a line break cannot forge a
// row of the interface. The identifiers themselves are never rewritten:
// the exact name the endpoint wrote is the one a request carries.
func describeDiscoveredModels(models []provider.ModelInfo) string {
	names := make([]string, 0, len(models))
	for _, m := range models {
		name := m.Display()
		if name == "" {
			name = m.ID
		}
		names = append(names, provider.SafeDisplayText(name, discoveredNameLimit))
	}
	return fmt.Sprintf("reachable, %d models: %s", len(models), strings.Join(names, ", "))
}

func (a *app) currentOnboard() tui.OnboardData {
	endpoint := a.cfg.Endpoint
	if endpoint == "" {
		endpoint = a.stored.Endpoint
	}
	key := a.cfg.APIKey
	if key == "" {
		key = a.stored.APIKey
	}
	model := a.model
	if model == "" {
		model = a.cfg.Model
	}
	return tui.OnboardData{Endpoint: endpoint, Model: model, Key: key}
}

// setMigrationNotice reports what the startup migration did.
//
// The message names the endpoint and the models that need a review, and
// never a credential.
func (a *app) setMigrationNotice(m tui.Model) {
	if !a.migration.Migrated {
		return
	}
	notice := a.migration.Summary()
	if len(a.migration.Reviews) > 0 {
		notice += fmt.Sprintf(" — %d model(s) need a review: /model list", len(a.migration.Reviews))
	}
	m.SetNotice(notice)
}

func (a *app) listModels() []tui.ModelInfo {
	active := a.model
	if active == "" {
		active = a.cfg.Model
	}
	if active == "" {
		if m, ok := a.stored.ActiveModelRecord(); ok {
			active = m.ID
		}
	}
	activeEndpoint := a.stored.ActiveEndpointID()
	seen := map[string]bool{}
	var out []tui.ModelInfo
	add := func(id, label, endpoint, review string) {
		if id == "" {
			return
		}
		key := endpoint + "/" + id
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, tui.ModelInfo{
			ID:       id,
			Label:    label,
			Endpoint: endpoint,
			Review:   review,
			Active:   id == active && (endpoint == activeEndpoint || activeEndpoint == ""),
		})
	}
	// Every endpoint's models are listed, and every model stays under its
	// own endpoint: the same identifier may exist twice, once per
	// endpoint, and the two are not the same model.
	for _, endpoint := range a.stored.Endpoints {
		for _, m := range endpoint.Models {
			add(m.ID, m.Display(), endpoint.ID, m.Review)
		}
	}
	for _, m := range a.stored.Models {
		add(m.ID, m.Display(), activeEndpoint, "")
	}
	if a.cfg.Model != "" {
		add(a.cfg.Model, a.cfg.Model+" (flags)", activeEndpoint, "")
	}
	if a.model != "" {
		add(a.model, a.model, activeEndpoint, "")
	}
	return out
}

// switchModel makes one model of one endpoint the active one.
//
// The selection is persisted, so it survives a restart, and it is scoped:
// the model is looked up inside the endpoint the operator named.
func (a *app) switchModel(endpointID, id string) (tui.Engine, error) {
	if endpointID == "" {
		endpointID = a.stored.ActiveEndpointID()
	}
	found := false
	for _, m := range a.listModels() {
		if m.ID != id {
			continue
		}
		if m.Endpoint != "" && endpointID != "" && m.Endpoint != endpointID {
			continue
		}
		found = true
		break
	}
	if !found {
		return nil, fmt.Errorf("unknown model %q", id)
	}
	next := a.stored.Clone()
	if endpointID != "" {
		if _, ok := next.EndpointByID(endpointID); !ok {
			return nil, fmt.Errorf("no endpoint with the id %q", endpointID)
		}
		if _, ok := next.ModelFor(endpointID, id); !ok {
			return nil, fmt.Errorf("endpoint %q has no model %q", endpointID, id)
		}
		next.SetActive(endpointID, id)
	} else {
		// A pre-version-2 configuration has one flat model list.
		next.ActiveModel = id
	}
	// The runtime that would use the new selection is built before
	// anything is written. A selection that cannot be used is refused:
	// the last working runtime and the last stored selection stay.
	if err := a.commitTarget(next); err != nil {
		return nil, err
	}
	a.model = id
	// What the endpoint says about this model's context is looked up in
	// the background, so the interface never waits for a metadata
	// lookup. The lookup is bounded and is never retried; an endpoint
	// that says nothing leaves the budget provisional.
	a.refreshContextInBackground(id)
	return a.eng, nil
}

func (a *app) newSession() (tui.Engine, error) {
	// A new session must never reuse an existing one: two clears in the
	// same second are still two separate conversations. The counter
	// covers names not yet written to disk; the stat covers a file left
	// by an earlier run.
	base := "cli-" + time.Now().Format("20060102-150405")
	a.sessionSeq++
	name := base
	if a.sessionSeq > 1 {
		name = fmt.Sprintf("%s-%d", base, a.sessionSeq)
	}
	for i := 2; ; i++ {
		if _, err := os.Stat(filepath.Join(a.cfg.StoreDir, name+".jsonl")); os.IsNotExist(err) {
			break
		}
		name = fmt.Sprintf("%s-%d", base, i)
	}
	store, err := session.Open(a.cfg.StoreDir, name)
	if err != nil {
		return nil, err
	}
	if err := a.persistActiveSession(name); err != nil {
		return nil, err
	}
	// A new session exists from creation, even before its first message:
	// the browser can list it and a restart keeps it selected.
	path := filepath.Join(a.cfg.StoreDir, name+".jsonl")
	if f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644); err != nil {
		return nil, err
	} else {
		_ = f.Close()
	}
	a.store = store
	a.session = name
	if a.permission != nil {
		a.permission.SetSession(name)
	}
	// A fresh conversation gets a date/time display title and a creation
	// timestamp; the ID stays immutable.
	now := time.Now().UTC()
	_ = session.SaveMeta(a.cfg.StoreDir, name, session.Meta{CreatedAt: now})
	a.sessionTitle = session.DefaultTitle(now)
	a.rebuildEngine()
	return a.eng, nil
}

// activeModelID returns the model the next request uses.
//
// The stored selection is the source of truth: a version-2 configuration
// names the endpoint and the model together, and that pair is what the
// request carries.
func (a *app) activeModelID() string {
	if a.stored.Selection != nil && a.stored.ActiveModelID() != "" {
		return a.stored.ActiveModelID()
	}
	if a.model != "" {
		return a.model
	}
	if a.cfg.Model != "" {
		return a.cfg.Model
	}
	if active, ok := a.stored.ActiveModelRecord(); ok {
		return active.ID
	}
	if active, ok := a.stored.Active(); ok {
		return active.ID
	}
	return ""
}

// currentUsage returns the cumulative accounting for the active session.
func (a *app) currentUsage() usage.Session {
	if a.usage == nil {
		return usage.Session{}
	}
	return a.usage.Get(a.session)
}

// usageTurn converts provider statistics into a stored record. Duration
// is wall-clock time, finish and truncation describe how the turn ended.
func usageTurn(stats provider.TurnStats) usage.Turn {
	return usage.Turn{
		Input:     stats.Usage.PromptTokens,
		Output:    stats.Usage.CompletionTokens,
		Reasoning: stats.Usage.ReasoningTokens,
		Cached:    stats.Usage.CachedTokens,
		Duration:  stats.Duration.Milliseconds(),
		Finish:    stats.FinishReason,
		Truncated: stats.OutputLimitHit,
		Reported:  stats.HasUsage,
	}
}

// recordTurn stores one finished answer. A turn without provider-reported
// usage is recorded as incomplete so the counts are never invented.
func (a *app) recordTurn(stats provider.TurnStats) {
	if a.usage == nil || stats.StartedAt.IsZero() {
		return
	}
	_ = a.usage.AddAnswer(a.session, usageTurn(stats))
}

// recordFailed stores a turn that produced no answer. Usage the provider
// did report still counts toward the session totals; a report-less
// failure only increments the failed counter.
func (a *app) recordFailed(stats *provider.TurnStats) {
	if a.usage == nil {
		return
	}
	if stats == nil || stats.StartedAt.IsZero() {
		_ = a.usage.AddFailed(a.session, usage.Turn{})
		return
	}
	_ = a.usage.AddFailed(a.session, usageTurn(*stats))
}

func (a *app) currentTheme() string {
	return a.stored.Theme
}

func (a *app) saveTheme(name string) error {
	a.stored.Theme = name
	return config.SaveFile(config.ConfigPath(), a.stored)
}

// listSessionNames returns stored conversations for the browser: display
// title, creation and activity dates, most recently active first, with
// the active session marked. History files are never modified.
func (a *app) listSessionNames() ([]tui.SessionInfo, error) {
	names, err := config.ListSessions(a.cfg.StoreDir)
	if err != nil {
		return nil, err
	}
	out := make([]tui.SessionInfo, 0, len(names))
	for _, name := range names {
		info := session.Stat(a.cfg.StoreDir, name)
		out = append(out, tui.SessionInfo{
			ID:      info.ID,
			Title:   info.Title,
			Created: info.Created,
			Updated: info.Updated,
			Current: name == a.session,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].Updated.Equal(out[j].Updated) {
			return out[i].Updated.After(out[j].Updated)
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

// displayTitle resolves what the header should show for a session.
func (a *app) displayTitle(id string) string {
	info := session.Stat(a.cfg.StoreDir, id)
	if info.Title != "" {
		return info.Title
	}
	return id
}

// renameSession sets the display title of the active conversation.
func (a *app) renameSession(title string) (string, error) {
	display, err := session.RenameSession(a.cfg.StoreDir, a.session, title)
	if err != nil {
		return "", err
	}
	return display, nil
}

func (a *app) switchSession(name string) (tui.Engine, []tui.SessionLine, error) {
	store, err := session.Open(a.cfg.StoreDir, name)
	if err != nil {
		return nil, nil, err
	}
	history, err := store.History()
	if err != nil {
		return nil, nil, err
	}
	if err := a.persistActiveSession(name); err != nil {
		return nil, nil, err
	}
	// Selecting a conversation is activity: it becomes the most recent
	// one for the next launch's browser order.
	now := time.Now()
	_ = os.Chtimes(filepath.Join(a.cfg.StoreDir, name+".jsonl"), now, now)
	a.store = store
	a.session = name
	a.sessionTitle = a.displayTitle(name)
	if a.permission != nil {
		a.permission.SetSession(name)
	}
	a.rebuildEngine()
	return a.eng, sessionLines(history), nil
}

// sessionLines turns stored messages into the interface's own lines.
//
// The attribution of a turn travels with its answer: it is what the
// stored record says shaped that answer, and it is shown beside the
// reply's metadata. It never becomes part of the text.
func sessionLines(history []session.Message) []tui.SessionLine {
	lines := make([]tui.SessionLine, 0, len(history))
	for _, msg := range history {
		role := "assistant"
		if msg.Role == session.RoleUser {
			role = "user"
		}
		lines = append(lines, tui.SessionLine{
			Role:        role,
			Text:        msg.Content,
			TurnID:      msg.TurnID,
			Tools:       msg.ToolExecutions(),
			Attribution: msg.Attribution,
			Files:       msg.Attachments,
		})
	}
	return lines
}

func (a *app) copyText(text string) error {
	copier, err := clipboard.System()
	if err != nil {
		return err
	}
	return copier.Copy(text)
}
