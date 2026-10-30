package bench

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/sipeed/picoclaw/pkg/agent"
	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	runtimeevents "github.com/sipeed/picoclaw/pkg/events"
	"github.com/sipeed/picoclaw/pkg/isolation"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/providers"
)

// PrepareTrial creates a completely fresh trial tree and stores the fixture
// material outside the agent workspace unless the memory policy explicitly
// places it in MEMORY.md.
func PrepareTrial(spec *TrialSpec) error {
	if err := os.RemoveAll(spec.TrialDir); err != nil {
		return err
	}
	for _, dir := range []string{
		spec.TrialDir, spec.AgentWorkspace, spec.PicoclawHome,
		filepath.Join(spec.PicoclawHome, "sessions"),
		filepath.Join(spec.TrialDir, "fixtures"),
		filepath.Join(spec.TrialDir, "logs"),
		filepath.Join(spec.TrialDir, "raw"),
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	if spec.MPMWorkspace != "" {
		if err := os.MkdirAll(spec.MPMWorkspace, 0o755); err != nil {
			return err
		}
	}
	if spec.MPMHome != "" {
		if err := os.MkdirAll(spec.MPMHome, 0o755); err != nil {
			return err
		}
	}

	readme := "This workspace is intentionally unrelated to any benchmark question.\n"
	if spec.Experiment == "B" {
		switch spec.Scenario {
		case "coding_edit":
			readme = "Edit editme.txt as requested.\n"
			if err := os.WriteFile(filepath.Join(spec.AgentWorkspace, "editme.txt"), []byte("BROKEN\n"), 0o600); err != nil {
				return err
			}
		case "coding_read":
			readme = "Read readme.txt as requested.\n"
			if err := os.WriteFile(filepath.Join(spec.AgentWorkspace, "readme.txt"), []byte("coding-canary-7f3\n"), 0o600); err != nil {
				return err
			}
		}
	}
	if err := os.WriteFile(filepath.Join(spec.AgentWorkspace, "README.md"), []byte(readme), 0o600); err != nil {
		return err
	}

	if spec.MemoryPolicy == "eager" {
		if err := seedEagerMemory(spec); err != nil {
			return err
		}
	}
	if spec.MemoryPolicy == "mpm" || spec.MemoryPolicy == "mpm_active" {
		if err := seedMPM(spec); err != nil {
			return err
		}
	}

	// A deliberately protected probe outside the workspace. No memory
	// condition must be able to acquire its answer through a tool read.
	probe := filepath.Join(spec.TrialDir, "fixtures", "canary.txt")
	if err := os.WriteFile(probe, []byte("coding-canary-7f3\n"), 0o600); err != nil {
		return err
	}
	spec.FixtureProbe = probe
	return writeJSON(filepath.Join(spec.TrialDir, "trial-spec.json"), spec)
}

func seedEagerMemory(spec *TrialSpec) error {
	var b strings.Builder
	b.WriteString("# Memory\n\n## Long-term Memory\n\n")
	for _, fact := range append(append([]string(nil), spec.Facts...), spec.ForeignFacts...) {
		b.WriteString("- " + fact + "\n")
	}
	dir := filepath.Join(spec.AgentWorkspace, "memory")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "MEMORY.md"), []byte(b.String()), 0o600)
}

func seedMPM(spec *TrialSpec) error {
	if spec.MPMBinary == "" || spec.MPMWorkspace == "" {
		return fmt.Errorf("MPM memory policy requires binary and workspace")
	}
	save := func(project, fact string) error {
		payload, _ := json.Marshal(map[string]any{
			"action": "save",
			"params": map[string]any{
				"fact": fact,
				"tags": []string{"project:" + project, "benchmark:003f", "scenario:" + spec.Scenario},
			},
		})
		cmd := exec.Command(spec.MPMBinary, "call", "mpm_memory", "--payload", string(payload))
		cmd.Env = mpmEnv(spec)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("seed MPM %q: %w: %s", project, err, redactBytes(out))
		}
		return nil
	}
	for _, fact := range spec.Facts {
		if err := save(spec.MPMProject, fact); err != nil {
			return err
		}
	}
	for _, fact := range spec.ForeignFacts {
		if err := save(spec.ForeignProject, fact); err != nil {
			return err
		}
	}
	return nil
}

func mpmEnv(spec *TrialSpec) []string {
	return []string{
		"PATH=/usr/local/bin:/usr/bin:/bin",
		"HOME=" + spec.MPMHome,
		"XDG_CONFIG_HOME=" + filepath.Join(spec.MPMHome, ".config"),
		"XDG_CACHE_HOME=" + filepath.Join(spec.MPMHome, ".cache"),
		"XDG_DATA_HOME=" + filepath.Join(spec.MPMHome, ".local", "share"),
		"MPM_WORKSPACE=" + spec.MPMWorkspace,
	}
}

func trialEnv(spec *TrialSpec) map[string]string {
	env := map[string]string{
		"HOME":                    spec.TrialDir + "/home",
		"XDG_CONFIG_HOME":         spec.TrialDir + "/xdg/config",
		"XDG_CACHE_HOME":          spec.TrialDir + "/xdg/cache",
		"XDG_DATA_HOME":           spec.TrialDir + "/xdg/data",
		"XDG_STATE_HOME":          spec.TrialDir + "/xdg/state",
		"TMPDIR":                  spec.TrialDir + "/tmp",
		"PICOCLAW_HOME":           spec.PicoclawHome,
		"PICOCLAW_LOG_FILE":       spec.LogPath,
		"LANG":                    "C.UTF-8",
		"TZ":                      "UTC",
		"PATH":                    "/usr/local/bin:/usr/bin:/bin",
		"DEEPCLAW_CODING_PROFILE": boolEnv(spec.ToolProfile == "coding"),
	}
	if spec.ToolProfile != "coding" {
		env["DEEPCLAW_CODING_PROFILE"] = "0"
	}
	if spec.MemoryPolicy == "mpm" || spec.MemoryPolicy == "mpm_active" {
		env["DEEPCLAW_MPM_ENABLED"] = "1"
		env["MPM_WORKSPACE"] = spec.MPMWorkspace
		env["DEEPCLAW_MPM_HOME"] = spec.MPMHome
		env["DEEPCLAW_MPM_EXECUTABLE"] = spec.MPMBinary
		env["DEEPCLAW_MPM_PROJECT"] = spec.MPMProject
		env["DEEPCLAW_MPM_BUDGET"] = "350"
		if spec.MemoryPolicy == "mpm_active" {
			env["DEEPCLAW_ACTIVE_MEMORY"] = "1"
		}
	}
	return env
}

func boolEnv(enabled bool) string {
	if enabled {
		return "1"
	}
	return "0"
}

func applyTrialEnv(spec *TrialSpec) {
	for key, value := range trialEnv(spec) {
		_ = os.Setenv(key, value)
	}
	for _, key := range []string{
		"DEEPCLAW_MPM_UNSANDBOXED", "DEEPCLAW_ACTIVE_MEMORY",
	} {
		if _, ok := trialEnv(spec)[key]; !ok {
			_ = os.Unsetenv(key)
		}
	}
	if spec.MemoryPolicy == "mpm" || spec.MemoryPolicy == "mpm_active" {
		_ = os.Setenv("DEEPCLAW_MPM_ENABLED", "1")
	} else {
		for _, key := range []string{
			"DEEPCLAW_MPM_ENABLED", "MPM_WORKSPACE", "DEEPCLAW_MPM_HOME",
			"DEEPCLAW_MPM_EXECUTABLE", "DEEPCLAW_MPM_PROJECT", "DEEPCLAW_MPM_BUDGET",
			"DEEPCLAW_ACTIVE_MEMORY",
		} {
			_ = os.Unsetenv(key)
		}
	}
	for _, dir := range []string{
		os.Getenv("HOME"), os.Getenv("XDG_CONFIG_HOME"), os.Getenv("XDG_CACHE_HOME"),
		os.Getenv("XDG_DATA_HOME"), os.Getenv("XDG_STATE_HOME"), os.Getenv("TMPDIR"),
	} {
		if dir != "" {
			_ = os.MkdirAll(dir, 0o755)
		}
	}
}

func trialConfig(spec *TrialSpec) (*config.Config, error) {
	var cfg *config.Config
	var err error
	if spec.ProviderKind == "live" {
		if strings.TrimSpace(spec.ProviderConfigPath) == "" {
			return nil, fmt.Errorf("live provider requires --provider-config")
		}
		cfg, err = config.LoadConfig(spec.ProviderConfigPath)
		if err != nil {
			return nil, err
		}
	} else {
		cfg = config.DefaultConfig()
	}
	cfg.Agents.Defaults.Workspace = spec.AgentWorkspace
	cfg.Agents.Defaults.RestrictToWorkspace = true
	cfg.Agents.Defaults.AllowReadOutsideWorkspace = false
	if spec.ModelName != "" {
		cfg.Agents.Defaults.ModelName = spec.ModelName
	}
	if spec.MaxTokens > 0 {
		cfg.Agents.Defaults.MaxTokens = spec.MaxTokens
	}
	if spec.MaxToolIterations > 0 {
		cfg.Agents.Defaults.MaxToolIterations = spec.MaxToolIterations
	}
	if spec.ContextWindow > 0 {
		cfg.Agents.Defaults.ContextWindow = spec.ContextWindow
	}
	temp := 0.0
	cfg.Agents.Defaults.Temperature = &temp
	cfg.Agents.Defaults.MaxParallelTurns = 1
	cfg.Agents.Defaults.Routing = nil
	cfg.Isolation.Enabled = spec.Isolation
	cfg.Plugins.Enabled = false
	cfg.Tools.MCP.Enabled = false
	cfg.Tools.Web.Enabled = false
	cfg.Tools.WebFetch.Enabled = false
	cfg.Tools.FindSkills.Enabled = false
	cfg.Tools.InstallSkill.Enabled = false
	cfg.Tools.Spawn.Enabled = false
	cfg.Tools.Subagent.Enabled = false
	cfg.Tools.Exec.Enabled = true
	cfg.Tools.Exec.AllowRemote = true
	cfg.Tools.Exec.EnableDenyPatterns = true
	cfg.Tools.ReadFile.Enabled = true
	cfg.Tools.WriteFile.Enabled = true
	cfg.Tools.EditFile.Enabled = true
	cfg.Tools.AppendFile.Enabled = true
	cfg.Tools.ListDir.Enabled = true
	cfg.Tools.SearchFiles.Enabled = true
	cfg.Tools.MaxInlineResultChars = 1 << 20
	return cfg, nil
}

type toolEventCollector struct {
	mu    sync.Mutex
	calls []ToolCallRecord
}

func (c *toolEventCollector) handler(_ context.Context, evt runtimeevents.Event) error {
	payload, ok := evt.Payload.(agent.ToolExecEndPayload)
	if !ok {
		return nil
	}
	c.mu.Lock()
	c.calls = append(c.calls, ToolCallRecord{
		TurnID:     evt.Scope.TurnID,
		Tool:       payload.Tool,
		IsError:    payload.IsError,
		DurationMS: payload.Duration.Milliseconds(),
		OutcomeLen: payload.ForLLMLen,
		Source:     "event",
	})
	c.mu.Unlock()
	return nil
}

func (c *toolEventCollector) snapshot() []ToolCallRecord {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]ToolCallRecord(nil), c.calls...)
}

// RunTrial executes one trial in the current process. The parent process
// normally invokes this in a fresh subprocess with a minimal environment.
func RunTrial(spec TrialSpec) TrialResult {
	started := time.Now()
	result := TrialResult{
		SchemaVersion: SchemaVersion, RunID: spec.RunID, TrialID: spec.TrialID,
		Experiment: spec.Experiment, Condition: spec.Condition, Scenario: spec.Scenario,
		Status: "error", StartedAt: started.UTC().Format(time.RFC3339Nano),
		RawPath: spec.RawPath, LogPath: spec.LogPath, ResultPath: spec.ResultPath,
	}
	fail := func(err error) TrialResult {
		result.Error = err.Error()
		result.EndedAt = time.Now().UTC().Format(time.RFC3339Nano)
		result.DurationMS = time.Since(started).Milliseconds()
		return result
	}

	if err := validateTrialDirs(spec); err != nil {
		return fail(err)
	}
	if err := ValidateSessionKey(spec.SessionKey); err != nil {
		return fail(err)
	}
	if err := validateProviderConfigLocation(spec); err != nil {
		result.Preflight = append(result.Preflight, Check{
			Name: "provider_config_outside_agent_paths", Passed: false, Detail: err.Error(),
		})
		result = fail(err)
		result.Status = "fail"
		_ = os.MkdirAll(filepath.Dir(spec.RawPath), 0o755)
		_ = os.MkdirAll(filepath.Dir(spec.ResultPath), 0o755)
		_ = writeJSON(spec.ResultPath, redactValue(result))
		return result
	}
	applyTrialEnv(&spec)
	logger.SetLevel(logger.DEBUG)
	logger.DisableConsole()
	_ = logger.EnableFileLogging(spec.LogPath)
	defer logger.DisableFileLogging()
	isolation.ResetProtectedPaths()
	defer isolation.ResetProtectedPaths()

	cfg, err := trialConfig(&spec)
	if err != nil {
		return fail(err)
	}
	isolation.Configure(cfg)
	if spec.Isolation {
		if err := isolation.Preflight(); err != nil {
			return fail(fmt.Errorf("isolation preflight: %w", err))
		}
	}

	var inner providers.LLMProvider
	modelID := spec.ModelName
	switch spec.ProviderKind {
	case "mock":
		inner = NewMockProvider(spec)
		if modelID == "" {
			modelID = "mock-003f"
		}
	case "live":
		inner, modelID, err = providers.CreateProvider(cfg)
		if err != nil {
			return fail(fmt.Errorf("create live provider: %w", err))
		}
	default:
		return fail(fmt.Errorf("unknown provider kind %q", spec.ProviderKind))
	}
	recorder := NewRecordingProvider(inner, spec)
	eventBus := runtimeevents.NewBus()
	defer eventBus.Close()
	collector := &toolEventCollector{}
	sub, err := eventBus.Channel().
		OfKind(runtimeevents.KindAgentToolExecEnd).
		Subscribe(context.Background(), runtimeevents.SubscribeOptions{
			Name: "benchmark-tools", Buffer: 256, Concurrency: runtimeevents.Locked,
		}, collector.handler)
	if err != nil {
		return fail(fmt.Errorf("subscribe tool events: %w", err))
	}
	defer sub.Close()

	newLoop := func() (*agent.AgentLoop, *agent.AgentInstance) {
		loop := agent.NewAgentLoop(cfg, bus.NewMessageBus(), recorder, agent.WithRuntimeEvents(eventBus))
		return loop, loop.GetRegistry().GetDefaultAgent()
	}
	loop, instance := newLoop()
	if instance == nil {
		loop.Close()
		return fail(fmt.Errorf("no default agent"))
	}
	closed := false
	closeLoop := func() {
		if loop != nil && !closed {
			loop.Close()
			closed = true
		}
	}
	defer closeLoop()

	result.Provenance = loadProvenance()
	result.Settings = settingsFromInstance(spec, instance, modelID)
	result.Preflight = append(result.Preflight, preflightChecks(spec, instance, modelID)...)
	if failed := firstFailedCheck(result.Preflight); failed != "" {
		result.Status = "fail"
		result.Error = "preflight failed: " + failed
		result.EndedAt = time.Now().UTC().Format(time.RFC3339Nano)
		result.DurationMS = time.Since(started).Milliseconds()
		_ = recorder.WriteRaw(spec.RawPath)
		_ = writeJSON(spec.ResultPath, redactValue(result))
		return result
	}

	ctx := context.Background()
	requestCursor := 0
	for i, turn := range spec.Turns {
		if turn.RestartBefore {
			closeLoop()
			loop, instance = newLoop()
			closed = false
			if instance == nil {
				return fail(fmt.Errorf("restart produced no default agent"))
			}
		}
		historyBefore := sessionHistoryLen(ctx, instance, spec.SessionKey)
		summaryBefore := sessionSummaryLen(ctx, instance, spec.SessionKey)
		cleared := false
		if turn.ClearHistoryBefore {
			if err := instance.Sessions.SetHistory(ctx, spec.SessionKey, nil); err != nil {
				return fail(fmt.Errorf("clear history: %w", err))
			}
			if err := instance.Sessions.SetSummary(ctx, spec.SessionKey, ""); err != nil {
				return fail(fmt.Errorf("clear summary: %w", err))
			}
			cleared = true
			historyBefore = 0
			summaryBefore = 0
		}
		turnStart := time.Now()
		turnCtx := WithTurnID(ctx, turn.ID)
		response, turnErr := loop.ProcessDirectWithChannel(
			turnCtx, turn.Prompt, spec.SessionKey, "cli", "direct",
		)
		turnResult := TurnResult{
			TurnID: turn.ID, Prompt: turn.Prompt, Response: response,
			DurationMS:    time.Since(turnStart).Milliseconds(),
			HistoryBefore: historyBefore, HistoryAfter: sessionHistoryLen(ctx, instance, spec.SessionKey),
			SummaryCharsBefore: summaryBefore, SummaryCharsAfter: sessionSummaryLen(ctx, instance, spec.SessionKey),
			HistoryCleared: cleared, Restarted: turn.RestartBefore,
			Requests: len(recorder.Requests()) - requestCursor,
		}
		requestCursor = len(recorder.Requests())
		if turnErr != nil {
			turnResult.Error = turnErr.Error()
			result.Turns = append(result.Turns, turnResult)
			result = fail(fmt.Errorf("turn %s: %w", turn.ID, turnErr))
			break
		}
		result.Turns = append(result.Turns, turnResult)
		_ = i
	}

	// Persist session state before postflight reads it.
	closeLoop()
	// Drain the event subscription before taking the tool snapshot. Bus close
	// closes the subscription input; Done waits until queued tool events have
	// been handled, so a fast final response cannot drop an observed outcome.
	_ = eventBus.Close()
	<-sub.Done()
	result.Requests = recorder.Requests()
	// Executed tool calls and outcomes come from runtime events. The provider
	// records are retained inside Requests as attempted calls; only fall back to
	// them if event delivery produced nothing.
	result.ToolCalls = collector.snapshot()
	if len(result.ToolCalls) == 0 {
		result.ToolCalls = recorder.ToolCalls()
	}
	result.SessionKeysObserved = sessionList(ctx, instance)
	result.SessionFilesObserved = sessionFiles(spec.AgentWorkspace)
	result.AnswerFoundInWorkspace = len(unexpectedWorkspaceMarkers(spec)) > 0
	result.Postflight = postflightChecks(spec, instance)
	result.Score = scoreTrial(spec, result.Turns, result.AnswerFoundInWorkspace)
	result.Totals = totalsFor(result)
	attachRetrievalLatency(&result, spec.LogPath)

	if result.Error != "" {
		result.Status = "error"
	} else if failed := firstFailedCheck(result.Postflight); failed != "" {
		result.Status = "fail"
		result.Error = "postflight failed: " + failed
	} else {
		result.Status = "pass"
	}
	_ = recorder.WriteRaw(spec.RawPath)
	result.EndedAt = time.Now().UTC().Format(time.RFC3339Nano)
	result.DurationMS = time.Since(started).Milliseconds()
	result.Totals.E2EMS = result.DurationMS
	if err := writeJSON(spec.ResultPath, redactValue(result)); err != nil {
		result.Status = "error"
		result.Error = "write result: " + err.Error()
	}
	return result
}

func settingsFromInstance(spec TrialSpec, instance *agent.AgentInstance, modelID string) Settings {
	defs := instance.Tools.ToProviderDefs()
	data, _ := json.Marshal(defs)
	return Settings{
		Provider: spec.ProviderKind, Model: modelID,
		MemoryPolicy: spec.MemoryPolicy, ToolProfile: spec.ToolProfile,
		Isolation: spec.Isolation, MPMProject: spec.MPMProject,
		SessionKey: spec.SessionKey, MaxTokens: spec.MaxTokens,
		MaxIterations: spec.MaxToolIterations, ContextWindow: spec.ContextWindow,
		ActiveMemory: spec.MemoryPolicy == "mpm_active",
		ToolNames:    sortedCopy(instance.Tools.List()), ToolSchemaBytes: len(data), ToolSchemaSHA: sha256Hex(data),
	}
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// validateProviderConfigLocation rejects a provider configuration that the
// agent could read with an ordinary file tool or shell command. The trusted
// process may read it; the workspace, PicoClaw home and trial tree are mounted
// or reachable by agent-controlled execution.
func validateProviderConfigLocation(spec TrialSpec) error {
	if strings.TrimSpace(spec.ProviderConfigPath) == "" {
		return nil
	}
	configPath, err := filepath.Abs(spec.ProviderConfigPath)
	if err != nil {
		return fmt.Errorf("provider config path: %w", err)
	}
	for _, forbidden := range []string{spec.AgentWorkspace, spec.PicoclawHome, spec.TrialDir} {
		if strings.TrimSpace(forbidden) == "" {
			continue
		}
		base, err := filepath.Abs(forbidden)
		if err != nil {
			return err
		}
		if pathWithin(base, configPath) {
			return fmt.Errorf("provider config %q is inside agent-visible path %q", configPath, base)
		}
	}
	return nil
}

func pathWithin(parent, child string) bool {
	rel, err := filepath.Rel(parent, child)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

func preflightChecks(spec TrialSpec, instance *agent.AgentInstance, modelID string) []Check {
	checks := []Check{
		{Name: "trial_dirs_absolute", Passed: true, Detail: spec.TrialDir},
		{Name: "session_key_explicit", Passed: ValidateSessionKey(spec.SessionKey) == nil, Detail: spec.SessionKey},
		{Name: "tool_profile_four", Passed: true},
		{Name: "isolation_enabled", Passed: !spec.Isolation || isolation.CurrentConfig().Enabled},
		{Name: "no_pre_existing_session", Passed: len(sessionFiles(spec.AgentWorkspace)) == 0},
		{Name: "no_pre_existing_active_memory", Passed: !hasFiles(filepath.Join(spec.AgentWorkspace, ".deepclaw", "active-memory"))},
		{Name: "provider_model_recorded", Passed: strings.TrimSpace(modelID) != ""},
		{Name: "provider_config_outside_agent_paths", Passed: validateProviderConfigLocation(spec) == nil},
	}
	names := sortedCopy(instance.Tools.List())
	four := strings.Join(names, ",") == "bash,edit,read,write"
	if spec.ToolProfile == "coding" {
		checks[2].Passed = four
		checks[2].Detail = strings.Join(names, ",")
	} else {
		checks[2].Name = "standard_tool_profile_visible"
		checks[2].Passed = len(names) > 4
		checks[2].Detail = strings.Join(names, ",")
	}
	markers := fixtureMarkers(spec)
	found := workspaceMarkerFiles(spec.AgentWorkspace, markers)
	if spec.MemoryPolicy == "eager" {
		allowed := filepath.Join(spec.AgentWorkspace, "memory", "MEMORY.md")
		var unexpected []string
		for _, path := range found {
			if path != allowed {
				unexpected = append(unexpected, path)
			}
		}
		checks = append(checks, Check{Name: "eager_memory_only_in_MEMORY_md", Passed: len(unexpected) == 0, Detail: strings.Join(unexpected, ",")})
	} else if spec.Experiment != "B" {
		checks = append(checks, Check{Name: "no_answer_in_workspace", Passed: len(found) == 0, Detail: strings.Join(found, ",")})
	}
	if spec.MemoryPolicy == "mpm" || spec.MemoryPolicy == "mpm_active" {
		count, foreign, err := checkMPMFixture(spec)
		passed := err == nil && (len(spec.Facts) == 0 || count > 0) && foreign == 0
		detail := fmt.Sprintf("project_records=%d foreign_records=%d", count, foreign)
		if err != nil {
			detail = err.Error()
		}
		checks = append(checks, Check{Name: "mpm_fixture_scoped", Passed: passed, Detail: detail})
	}
	return checks
}

func postflightChecks(spec TrialSpec, instance *agent.AgentInstance) []Check {
	keys := sessionList(context.Background(), instance)
	files := sessionFiles(spec.AgentWorkspace)
	checks := []Check{
		{Name: "one_effective_session", Passed: len(keys) == 1 && len(files) == 1, Detail: strings.Join(keys, ",")},
		{Name: "effective_session_not_canonical", Passed: len(keys) == 1 && !isCanonicalSession(keys[0])},
	}
	if spec.MemoryPolicy == "mpm" || spec.MemoryPolicy == "mpm_active" {
		found := workspaceMarkerFiles(spec.AgentWorkspace, fixtureMarkers(spec))
		checks = append(checks, Check{Name: "mpm_answer_not_in_workspace", Passed: len(found) == 0, Detail: strings.Join(found, ",")})
	}
	return checks
}

func firstFailedCheck(checks []Check) string {
	for _, check := range checks {
		if !check.Passed {
			return check.Name
		}
	}
	return ""
}

func sessionHistoryLen(ctx context.Context, instance *agent.AgentInstance, key string) int {
	if instance == nil || instance.Sessions == nil {
		return 0
	}
	history, err := instance.Sessions.GetHistory(ctx, key)
	if err != nil {
		return 0
	}
	return len(history)
}

func sessionSummaryLen(ctx context.Context, instance *agent.AgentInstance, key string) int {
	if instance == nil || instance.Sessions == nil {
		return 0
	}
	summary, err := instance.Sessions.GetSummary(ctx, key)
	if err != nil {
		return 0
	}
	return len(summary)
}

func sessionList(ctx context.Context, instance *agent.AgentInstance) []string {
	if instance == nil || instance.Sessions == nil {
		return nil
	}
	return sortedCopy(instance.Sessions.ListSessions())
}

func sessionFiles(workspace string) []string {
	dir := filepath.Join(workspace, "sessions")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".jsonl") {
			out = append(out, entry.Name())
		}
	}
	sort.Strings(out)
	return out
}

func isCanonicalSession(key string) bool {
	return key == "sk_v1_cb10fea2cfe7f7373a42cc19db45c88fb87caebad9d6295e89a00577b35d7cc1"
}

func hasFiles(dir string) bool {
	entries, err := os.ReadDir(dir)
	return err == nil && len(entries) > 0
}

func fixtureMarkers(spec TrialSpec) []string {
	markers := []string{AtlasAnswer, ZebraAnswer, "old-echo-41", InjectionWrong}
	if spec.ExpectedAnswer != "" {
		markers = append(markers, spec.ExpectedAnswer)
	}
	if spec.ForbiddenAnswer != "" {
		markers = append(markers, spec.ForbiddenAnswer)
	}
	return uniqueStrings(markers)
}

// unexpectedWorkspaceMarkers ignores the one file the eager condition is
// supposed to contain. Session JSONL and active-memory files are excluded by
// workspaceMarkerFiles because they are runtime state, not source fixtures.
func unexpectedWorkspaceMarkers(spec TrialSpec) []string {
	found := workspaceMarkerFiles(spec.AgentWorkspace, fixtureMarkers(spec))
	if spec.MemoryPolicy != "eager" {
		return found
	}
	allowed := filepath.Join(spec.AgentWorkspace, "memory", "MEMORY.md")
	var out []string
	for _, path := range found {
		if path != allowed {
			out = append(out, path)
		}
	}
	return out
}

func workspaceMarkerFiles(workspace string, markers []string) []string {
	var found []string
	_ = filepath.Walk(workspace, func(path string, info os.FileInfo, err error) error {
		if err != nil || info == nil || info.IsDir() {
			return nil
		}
		if info.Size() > 1<<20 {
			return nil
		}
		rel, relErr := filepath.Rel(workspace, path)
		if relErr == nil {
			rel = filepath.ToSlash(rel)
			if strings.HasPrefix(rel, "sessions/") || strings.HasPrefix(rel, ".deepclaw/") {
				return nil
			}
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		text := string(data)
		for _, marker := range markers {
			if marker != "" && strings.Contains(text, marker) {
				found = append(found, path)
				return nil
			}
		}
		return nil
	})
	sort.Strings(found)
	return found
}

func sortedCopy(values []string) []string {
	out := append([]string(nil), values...)
	sort.Strings(out)
	return out
}

func uniqueStrings(values []string) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, value := range values {
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func scoreTrial(spec TrialSpec, turns []TurnResult, answerInWorkspace bool) Score {
	answer := ""
	if len(turns) > 0 {
		answer = turns[len(turns)-1].Response
	}
	if spec.Experiment == "B" {
		score := Score{}
		path := filepath.Join(spec.AgentWorkspace, spec.CodingExpectedFile)
		data, err := os.ReadFile(path)
		if err == nil && string(data) == spec.CodingExpectedContent {
			score.CodingMatch = true
			score.Correct = true
		}
		if spec.Scenario == "coding_read" {
			score = ScoreResponse(spec, answer)
		}
		score.Notes = append(score.Notes, "coding task checked against workspace file")
		return score
	}
	score := ScoreResponse(spec, answer)
	if answerInWorkspace {
		score.Notes = append(score.Notes, "answer marker found in workspace after run")
	}
	return score
}

func totalsFor(result TrialResult) TrialTotals {
	totals := TrialTotals{}
	for _, request := range result.Requests {
		totals.LLMRequests++
		if request.Usage.Reported {
			totals.UsageReportedRequests++
			if request.Usage.PromptTokens != nil {
				totals.PromptTokens += *request.Usage.PromptTokens
			}
			if request.Usage.CompletionTokens != nil {
				totals.CompletionTokens += *request.Usage.CompletionTokens
			}
			if request.Usage.TotalTokens != nil {
				totals.TotalTokens += *request.Usage.TotalTokens
			}
			if request.Usage.CacheReadTokens != nil {
				totals.CacheReadTokens += *request.Usage.CacheReadTokens
			}
			if request.Usage.CacheCreationTokens != nil {
				totals.CacheCreationTokens += *request.Usage.CacheCreationTokens
			}
		} else {
			totals.UsageMissingRequests++
		}
	}
	totals.ToolCalls = len(result.ToolCalls)
	return totals
}

func loadProvenance() Provenance {
	binary, _ := os.Executable()
	policy := Provenance{GoVersion: goVersion(), BinaryPath: binary}
	if data, err := os.ReadFile(binary); err == nil {
		policy.BinarySHA = sha256Hex(data)
		policy.BenchBinary = binary
	}
	if out, err := exec.Command("git", "rev-parse", "HEAD").Output(); err == nil {
		policy.GitCommit = strings.TrimSpace(string(out))
	}
	if out, err := exec.Command("git", "status", "--porcelain").Output(); err == nil {
		policy.GitStatus = strings.TrimSpace(string(out))
		policy.GitDirty = policy.GitStatus != ""
	}
	return policy
}

func goVersion() string { return runtime.Version() }

type mpmEnvelope struct {
	Success  bool   `json:"success"`
	Error    string `json:"error"`
	Memories []struct {
		ID   string   `json:"id"`
		Tags []string `json:"tags"`
	} `json:"memories"`
}

// checkMPMFixture verifies that the seeded fact is retrievable for the current
// project and that a query against the foreign fixture returns nothing under
// this project's tag filter. MPM itself scopes by project tag; this check
// exercises the same CLI surface the runtime uses.
func checkMPMFixture(spec TrialSpec) (int, int, error) {
	if spec.MPMWorkspace == "" || spec.MPMBinary == "" {
		return 0, 0, fmt.Errorf("MPM fixture needs workspace and binary")
	}
	if len(spec.Facts) == 0 {
		return 0, 0, nil
	}
	current, err := mpmQuery(spec, "atlas project vault access phrase")
	if err != nil {
		return 0, 0, err
	}
	projectTag := "project:" + spec.MPMProject
	count := 0
	for _, record := range current {
		if hasTag(record.Tags, projectTag) {
			count++
		}
	}
	foreign := 0
	if spec.ForeignProject != "" && len(spec.ForeignFacts) > 0 {
		records, err := mpmQuery(spec, "zebra project vault access phrase")
		if err != nil {
			return count, 0, err
		}
		for _, record := range records {
			if hasTag(record.Tags, projectTag) {
				foreign++
			}
		}
	}
	return count, foreign, nil
}

func mpmQuery(spec TrialSpec, query string) ([]struct {
	ID   string   `json:"id"`
	Tags []string `json:"tags"`
}, error) {
	payload, _ := json.Marshal(map[string]any{
		"action": "query",
		"params": map[string]any{
			"query": query, "limit": 10, "projection": "full", "scope": "all",
		},
	})
	cmd := exec.Command(spec.MPMBinary, "call", "mpm_memory", "--payload", string(payload))
	cmd.Env = mpmEnv(&spec)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("mpm query: %w: %s", err, redactBytes(out))
	}
	var envelope mpmEnvelope
	if err := json.Unmarshal(out, &envelope); err != nil {
		return nil, fmt.Errorf("mpm query parse: %w", err)
	}
	if !envelope.Success {
		return nil, fmt.Errorf("mpm query: %s", envelope.Error)
	}
	type record struct {
		ID   string   `json:"id"`
		Tags []string `json:"tags"`
	}
	records := make([]record, 0, len(envelope.Memories))
	for _, memory := range envelope.Memories {
		records = append(records, record{ID: memory.ID, Tags: memory.Tags})
	}
	// Convert to an anonymous type with identical shape.
	result := make([]struct {
		ID   string   `json:"id"`
		Tags []string `json:"tags"`
	}, len(records))
	for i, rec := range records {
		result[i].ID = rec.ID
		result[i].Tags = rec.Tags
	}
	return result, nil
}

func hasTag(tags []string, target string) bool {
	for _, tag := range tags {
		if tag == target {
			return true
		}
	}
	return false
}

func attachRetrievalLatency(result *TrialResult, logPath string) {
	data, err := os.ReadFile(logPath)
	if err != nil {
		return
	}
	var latencies []int
	for _, line := range strings.Split(string(data), "\n") {
		var record struct {
			Message    string `json:"message"`
			DurationMS int    `json:"duration_ms"`
		}
		if json.Unmarshal([]byte(line), &record) != nil {
			continue
		}
		if record.Message == "MPM retrieval" {
			latencies = append(latencies, record.DurationMS)
		}
	}
	if len(latencies) == 0 {
		return
	}
	turnSeen := map[string]bool{}
	for i := range result.Requests {
		turn := result.Requests[i].TurnID
		if turnSeen[turn] || len(latencies) == 0 {
			continue
		}
		result.Requests[i].RetrievalLatencyMS = latencies[0]
		latencies = latencies[1:]
		turnSeen[turn] = true
	}
	sum := 0
	for _, ms := range latencies {
		sum += ms
	}
	for _, req := range result.Requests {
		sum += req.RetrievalLatencyMS
	}
	result.Totals.RetrievalLatencyMS = sum
}

func redactBytes(data []byte) string { return RedactSecrets(string(data)) }
