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

	if spec.Experiment == "D" {
		if err := PrepareCodingTrial(spec); err != nil {
			return err
		}
		probe := filepath.Join(spec.TrialDir, "fixtures", "canary.txt")
		if err := os.WriteFile(probe, []byte("coding-canary-7f3\n"), 0o600); err != nil {
			return err
		}
		spec.FixtureProbe = probe
		return writeJSON(filepath.Join(spec.TrialDir, "trial-spec.json"), spec)
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
		"PATH":                    trialPath(*spec),
		"DEEPCLAW_CODING_PROFILE": boolEnv(spec.ToolProfile == "coding"),
	}
	if spec.ToolProfile != "coding" {
		env["DEEPCLAW_CODING_PROFILE"] = "0"
	}
	if spec.Experiment == "D" {
		env["DEEPCLAW_TASK_STATE"] = boolEnv(spec.TaskState)
		env["DEEPCLAW_MPM_ENABLED"] = "0"
		env["DEEPCLAW_ACTIVE_MEMORY"] = "0"
		env["GOPROXY"] = "off"
		env["GOFLAGS"] = "-mod=readonly"
		env["GOTOOLCHAIN"] = "local"
		env["CGO_ENABLED"] = "0"
		env["GO111MODULE"] = "on"
		if spec.SummarizeTokenPercent > 0 {
			env["PICOCLAW_AGENTS_DEFAULTS_SUMMARIZE_TOKEN_PERCENT"] = fmt.Sprintf("%d", spec.SummarizeTokenPercent)
		}
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
	if spec.TaskState {
		env["DEEPCLAW_TASK_STATE"] = "1"
		env["DEEPCLAW_TASK_STATE_BUDGET"] = fmt.Sprintf("%d", spec.TaskStateBudget)
		if spec.TaskStateResidency != "" {
			env["DEEPCLAW_TASK_STATE_RESIDENCY"] = spec.TaskStateResidency
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
		"DEEPCLAW_TASK_STATE", "DEEPCLAW_TASK_STATE_BUDGET",
		"DEEPCLAW_TASK_STATE_RESIDENCY",
	} {
		if _, ok := trialEnv(spec)[key]; !ok {
			_ = os.Unsetenv(key)
		}
	}
	if spec.MemoryPolicy == "mpm" || spec.MemoryPolicy == "mpm_active" {
		_ = os.Setenv("DEEPCLAW_MPM_ENABLED", "1")
	} else if spec.Experiment != "D" {
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
	if spec.SummarizeTokenPercent > 0 {
		cfg.Agents.Defaults.SummarizeTokenPercent = spec.SummarizeTokenPercent
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
	if spec.Experiment == "D" && cfg.Tools.Exec.TimeoutSeconds < 300 {
		cfg.Tools.Exec.TimeoutSeconds = 300
	}
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
		PairID: spec.PairID, Repetition: spec.Repetition, SequenceIndex: spec.SequenceIndex,
		StartingCommit: spec.StartingCommit, ProcessStarts: 1,
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

	// 005D: the trusted tool-trace collector is created before the
	// persist-on-fail reassignment so failure paths can snapshot it.
	tracer := newToolTraceCollector(ToolTracePath(spec))
	tracer.reseed()

	// 005B-R: from here on, every failure path persists partial evidence.
	// The original fail() only filled fields; a compact/exhaustion/transport
	// failure then lost all in-memory requests, tool calls and totals because
	// neither the raw trace nor result.json was written. The ledger (written
	// synchronously per request) already survived crashes; this makes the
	// result file survive them too.
	fail = func(err error) TrialResult {
		result.Error = err.Error()
		result.EndedAt = time.Now().UTC().Format(time.RFC3339Nano)
		result.DurationMS = time.Since(started).Milliseconds()
		result.Requests = recorder.Requests()
		result.ToolCalls = collector.snapshot()
		if len(result.ToolCalls) == 0 {
			result.ToolCalls = recorder.ToolCalls()
		}
		if stopped, _ := recorder.BudgetStop(); stopped {
			result.TokenBudgetStopped = true
		}
		if recovery, recErr := ReconcileUsage(result, UsageLedgerPath(spec)); recErr == nil {
			result.UsageRecovery = &recovery
		}
		traceSummary := tracer.snapshot()
		result.ToolTrace = &traceSummary
		result.Totals = totalsFor(result)
		_ = recorder.WriteRaw(spec.RawPath)
		_ = writeJSON(spec.ResultPath, redactValue(result))
		return result
	}

	// 005B-R: count automatic threshold compactions per bench turn via the
	// session-summarize events. Production compaction code is frozen; the
	// bench only observes, to detect runaway compaction churn.
	var summarizeMu sync.Mutex
	summarizeCount := 0
	summarizeSub, summarizeErr := eventBus.Channel().
		OfKind(runtimeevents.KindAgentSessionSummarize).
		Subscribe(context.Background(), runtimeevents.SubscribeOptions{
			Name: "benchmark-summaries", Buffer: 256, Concurrency: runtimeevents.Locked,
		}, func(_ context.Context, _ runtimeevents.Event) error {
			summarizeMu.Lock()
			summarizeCount++
			summarizeMu.Unlock()
			return nil
		})
	if summarizeErr != nil {
		return fail(fmt.Errorf("subscribe summarize events: %w", summarizeErr))
	}
	defer summarizeSub.Close()

	// 005D: subscribe the trusted tool-execution events. Start carries
	// final validated tool names and arguments, End carries duration and
	// error status, Skipped carries hook denials. Dispatch is strictly
	// sequential with locked delivery, so order-joins are exact. Nothing
	// here touches prompts or tool schemas.
	traceTurn := ""
	traceRequestOf := func() string {
		requests := recorder.Requests()
		for i := len(requests) - 1; i >= 0; i-- {
			if requests[i].TurnID == traceTurn && requests[i].RequestID != "" {
				return requests[i].RequestID
			}
		}
		return ""
	}
	startSub, startErr := eventBus.Channel().
		OfKind(runtimeevents.KindAgentToolExecStart).
		Subscribe(context.Background(), runtimeevents.SubscribeOptions{
			Name: "benchmark-tool-start", Buffer: 1024, Concurrency: runtimeevents.Locked,
		}, func(_ context.Context, evt runtimeevents.Event) error {
			payload, ok := evt.Payload.(agent.ToolExecStartPayload)
			if !ok {
				return nil
			}
			tracer.recordStart(evt.Scope.TurnID, payload.Tool, payload.Arguments)
			return nil
		})
	if startErr != nil {
		return fail(fmt.Errorf("subscribe tool start events: %w", startErr))
	}
	defer startSub.Close()
	endSub, endErr := eventBus.Channel().
		OfKind(runtimeevents.KindAgentToolExecEnd).
		Subscribe(context.Background(), runtimeevents.SubscribeOptions{
			Name: "benchmark-tool-end", Buffer: 1024, Concurrency: runtimeevents.Locked,
		}, func(_ context.Context, evt runtimeevents.Event) error {
			payload, ok := evt.Payload.(agent.ToolExecEndPayload)
			if !ok {
				return nil
			}
			tracer.recordEnd(evt.Scope.TurnID, payload.Tool, payload.Duration, payload.IsError, payload.Async, payload.ForLLMLen)
			return nil
		})
	if endErr != nil {
		return fail(fmt.Errorf("subscribe tool end events: %w", endErr))
	}
	defer endSub.Close()
	skipSub, skipErr := eventBus.Channel().
		OfKind(runtimeevents.KindAgentToolExecSkipped).
		Subscribe(context.Background(), runtimeevents.SubscribeOptions{
			Name: "benchmark-tool-skip", Buffer: 256, Concurrency: runtimeevents.Locked,
		}, func(_ context.Context, evt runtimeevents.Event) error {
			payload, ok := evt.Payload.(agent.ToolExecSkippedPayload)
			if !ok {
				return nil
			}
			tracer.recordSkipped(evt.Scope.TurnID, payload.Tool, payload.Reason)
			return nil
		})
	if skipErr != nil {
		return fail(fmt.Errorf("subscribe tool skip events: %w", skipErr))
	}
	defer skipSub.Close()
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
	turnStartIndex, turnEndIndex := spec.TurnStart, spec.TurnEnd
	if turnStartIndex < 0 {
		turnStartIndex = 0
	}
	if turnEndIndex <= 0 || turnEndIndex > len(spec.Turns) {
		turnEndIndex = len(spec.Turns)
	}
	if turnStartIndex > turnEndIndex {
		turnStartIndex = turnEndIndex
	}
	// 005B-R: runaway-trajectory accounting, symmetric across conditions.
	exhaustions, totalSummaries, totalRequests, totalToolCalls := 0, 0, 0, 0
	for i, turn := range spec.Turns[turnStartIndex:turnEndIndex] {
		traceTurn = turn.ID
		tracer.setContext(turn.ID, traceRequestOf)
		filesBeforeTurn := workspaceFileStatus(spec.AgentWorkspace)
		restarted := turn.RestartBefore
		if turn.RestartBefore {
			// A resumed segment already runs in a new OS process. Do not close
			// and rebuild the loop again just because the first turn carries
			// the original restart marker.
			if !(spec.Resume && i == 0) {
				closeLoop()
				loop, instance = newLoop()
				closed = false
				if instance == nil {
					return fail(fmt.Errorf("restart produced no default agent"))
				}
			}
		}
		historyBefore := sessionHistoryLen(ctx, instance, spec.SessionKey)
		summaryBefore := sessionSummaryLen(ctx, instance, spec.SessionKey)
		compacted := false
		compactionAlreadyLost := false
		summarizeMu.Lock()
		turnSummarizeStart := summarizeCount
		summarizeMu.Unlock()
		if turn.CompactBefore {
			beforeCompact := sessionSummaryLen(ctx, instance, spec.SessionKey)
			if _, err := loop.ProcessDirectWithChannel(ctx, "/compact", spec.SessionKey, "cli", "direct"); err != nil {
				return fail(fmt.Errorf("turn %s compact: %w", turn.ID, err))
			}
			afterCompact := sessionSummaryLen(ctx, instance, spec.SessionKey)
			historyAfterCompact := sessionHistoryLen(ctx, instance, spec.SessionKey)
			compacted = afterCompact > beforeCompact
			if !compacted {
				// 005B-R: a no-op explicit compaction is accepted only when
				// history loss was already achieved — ordinarily by
				// automatic threshold compaction earlier in the trial. The
				// trial starts with an empty summary (preflight rejects
				// pre-existing sessions), so a non-empty summary proves
				// some compaction already ran; the remaining history must
				// then be within twice the normal protected tail
				// (production keeps ~4 messages plus the current turn).
				// Anything larger means compaction is seriously behind and
				// the no-op is genuine: fail as before.
				if explicitCompactHistoryLost(afterCompact, historyAfterCompact) {
					compactionAlreadyLost = true
				} else {
					return fail(fmt.Errorf("turn %s compact produced no summary (summary_chars=%d history=%d)",
						turn.ID, afterCompact, historyAfterCompact))
				}
			}
			// Compaction requests belong to the run total, not to the turn's
			// own request count.
			requestCursor = len(recorder.Requests())
			historyBefore = sessionHistoryLen(ctx, instance, spec.SessionKey)
			summaryBefore = sessionSummaryLen(ctx, instance, spec.SessionKey)
		}
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
		toolsBefore := len(collector.snapshot())
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
			HistoryCleared: cleared, Restarted: restarted, Compacted: compacted,
			CompactionAlreadyLost: compactionAlreadyLost,
			Requests:              len(recorder.Requests()) - requestCursor,
		}
		turnRequests := recorder.Requests()
		if requestCursor < len(turnRequests) {
			for _, request := range turnRequests[requestCursor:] {
				turnResult.TaskStatePromptChars += request.TaskStateChars
				turnResult.TaskStatePromptTokens += request.TaskStateTokens
				turnResult.TaskStateRendered += request.TaskStateRenderedCount
				if request.TaskStateText != "" {
					turnResult.TaskStatePromptText += request.TaskStateText + "\n"
				}
			}
		}
		if snapshot := collector.snapshot(); len(snapshot) >= toolsBefore {
			turnResult.ToolCalls = append([]ToolCallRecord(nil), snapshot[toolsBefore:]...)
		}
		if spec.TaskState {
			turnResult.TaskState = taskStateSnapshot(instance, spec.SessionKey, spec.TaskStateBudget)
		}
		requestCursor = len(recorder.Requests())
		if turnErr != nil {
			turnResult.Error = turnErr.Error()
			result.Turns = append(result.Turns, turnResult)
			result = fail(fmt.Errorf("turn %s: %w", turn.ID, turnErr))
			break
		}
		// 005B-R: no-progress analysis on the completed turn. A task limit
		// stops the remaining turns but keeps the trial harness-valid: the
		// repository is scored as-is. Identical rules in both conditions.
		summarizeMu.Lock()
		turnSummaries := summarizeCount - turnSummarizeStart
		summarizeMu.Unlock()
		totalSummaries += turnSummaries
		turnResult.AutoCompactions = turnSummaries
		turnResult.FilesChanged = changedWorkspaceFiles(filesBeforeTurn, workspaceFileStatus(spec.AgentWorkspace))
		totalRequests += turnResult.Requests
		totalToolCalls += len(turnResult.ToolCalls)
		var runawayRequests []RequestRecord
		allRequests := recorder.Requests()
		runawayStart := len(allRequests) - turnResult.Requests
		if runawayStart < 0 {
			runawayStart = 0
		}
		runawayRequests = append([]RequestRecord(nil), allRequests[runawayStart:]...)
		if maxIterationExhausted(turnResult.Response) {
			exhaustions++
		}
		taskLimitReason := turnRunawayReason(runawayRequests, turnSummaries)
		switch {
		case taskLimitReason != "":
		case totalSummaries > maxSummariesPerTrial:
			taskLimitReason = "task execution limit: too many automatic compactions in one trial"
		case exhaustions >= maxIterationExhaustionsPerTrial:
			taskLimitReason = "task execution limit: tool-iteration budget exhausted without a final response more than once"
		case totalRequests > maxRequestsPerTrial:
			taskLimitReason = "task execution limit: too many LLM requests in one trial"
		case totalToolCalls > maxToolCallsPerTrial:
			taskLimitReason = "task execution limit: too many tool calls in one trial"
		}
		result.Turns = append(result.Turns, turnResult)
		if taskLimitReason != "" {
			result.TaskLimitReached = true
			result.TaskLimitReason = fmt.Sprintf("turn %s: %s", turn.ID, taskLimitReason)
			break
		}
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
	// 005B: record whether the token-budget guard (or a metering failure)
	// blocked a request. The human-readable reason is already on the failed
	// turn and in result.Error.
	if stopped, _ := recorder.BudgetStop(); stopped {
		result.TokenBudgetStopped = true
	}
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
	result.ResidentCacheRecords = residentCacheRecords(instance, spec.SessionKey)
	if spec.TaskState {
		result.TaskStateFinal = taskStateSnapshot(instance, spec.SessionKey, spec.TaskStateBudget)
	}
	if spec.Experiment == "D" && !spec.SkipScoring {
		result.Score = Score{Notes: []string{"independently scored from the resulting repository; not a prompt diagnostic"}}
		if result.TaskLimitReached {
			result.Score.Notes = append(result.Score.Notes, "task execution limit reached: "+result.TaskLimitReason)
		}
		outcome, scoreErr := scoreCodingResult(spec, result, started)
		if scoreErr != nil {
			result.Error = "coding scoring: " + scoreErr.Error()
		} else {
			result.Coding = outcome
			result.Score.FullSuccess = outcome.FullSuccess
			result.Score.Partial = outcome.Partial
			result.Score.PointsEarned = outcome.PointsEarned
			result.Score.PointsTotal = outcome.PointsTotal
			result.Score.Correct = outcome.FullSuccess
			if len(outcome.ConstraintViolations) > 0 {
				result.Score.Notes = append(result.Score.Notes,
					"constraint violations: "+strings.Join(outcome.ConstraintViolations, ", "))
			}
		}
	} else if spec.Experiment != "D" {
		result.Score = scoreTrial(spec, result.Turns, result.AnswerFoundInWorkspace)
	}
	if spec.Experiment == "C" {
		result.TurnScores = scoreTaskStateTrial(spec, result)
		result.Score = Score{Notes: []string{"deterministic task-state mechanism test; not a live-model reasoning result"}}
		result.Score.Correct = true
		for _, turnScore := range result.TurnScores {
			if !turnScore.Passed {
				result.Score.Correct = false
			}
		}
	}
	result.Postflight = postflightChecks(spec, instance)
	result.Postflight = append(result.Postflight, codingPostflightChecks(spec, result)...)
	result.Totals = totalsFor(result)
	// 005B-R: reconcile the durable ledger on the normal path too, so the
	// recorded recovery always reflects reservations for unknown outcomes.
	if recovery, recErr := ReconcileUsage(result, UsageLedgerPath(spec)); recErr == nil {
		result.UsageRecovery = &recovery
	}
	traceFinal := tracer.snapshot()
	result.ToolTrace = &traceFinal
	attachRetrievalLatency(&result, spec.LogPath)
	result.Totals.MPMRetrievalCalls = countLogMessage(spec.LogPath, "MPM retrieval")

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
		ActiveMemory:       spec.MemoryPolicy == "mpm_active" || spec.TaskState,
		TaskState:          spec.TaskState,
		TaskStateBudget:    spec.TaskStateBudget,
		TaskStateResidency: spec.TaskStateResidency,
		ToolNames:          sortedCopy(instance.Tools.List()),
		ToolSchemaBytes:    len(data), ToolSchemaSHA: sha256Hex(data),
		CodingTask:       spec.CodingTask,
		CodingFixtureSHA: spec.FixtureSHA,
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
	sessionFilesNow := sessionFiles(spec.AgentWorkspace)
	sessionOK := len(sessionFilesNow) == 0
	if spec.Resume {
		sessionOK = len(sessionFilesNow) > 0
	}
	activeMemoryDir := filepath.Join(spec.AgentWorkspace, ".deepclaw", "active-memory")
	activeMemoryOK := !hasFiles(activeMemoryDir)
	checks := []Check{
		{Name: "trial_dirs_absolute", Passed: true, Detail: spec.TrialDir},
		{Name: "session_key_explicit", Passed: ValidateSessionKey(spec.SessionKey) == nil, Detail: spec.SessionKey},
		{Name: "tool_profile_four", Passed: true},
		{Name: "isolation_enabled", Passed: !spec.Isolation || isolation.CurrentConfig().Enabled},
		{Name: "no_pre_existing_session", Passed: sessionOK, Detail: fmt.Sprintf("resume=%t sessions=%d", spec.Resume, len(sessionFilesNow))},
		{Name: "no_pre_existing_active_memory", Passed: activeMemoryOK, Detail: fmt.Sprintf("resume=%t", spec.Resume)},
		{Name: "provider_model_recorded", Passed: strings.TrimSpace(modelID) != ""},
		{Name: "provider_config_outside_agent_paths", Passed: validateProviderConfigLocation(spec) == nil},
	}
	if spec.TaskState {
		dir := ""
		if instance.ActiveMemory != nil {
			dir = instance.ActiveMemory.Dir()
		}
		protected := false
		for _, path := range isolation.ProtectedPaths() {
			if path == dir && dir != "" {
				protected = true
			}
		}
		sidecarOK := !hasFiles(dir)
		if spec.Resume {
			sidecarOK = hasFiles(dir)
		}
		checks = append(checks,
			Check{Name: "task_state_outside_workspace", Passed: dir != "" && !pathWithin(spec.AgentWorkspace, dir), Detail: dir},
			Check{Name: "task_state_protected", Passed: protected, Detail: dir},
			Check{Name: "task_state_no_pre_existing_sidecar", Passed: sidecarOK, Detail: dir},
		)
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
	if spec.Experiment == "D" {
		task, taskErr := LoadCodingTask(spec.CodingTask)
		taskOK := taskErr == nil
		detail := spec.CodingTask
		if taskErr != nil {
			detail = taskErr.Error()
		}
		checks = append(checks,
			Check{Name: "coding_task_loaded", Passed: taskOK, Detail: detail},
			Check{Name: "coding_starting_commit_recorded", Passed: spec.StartingCommit != "", Detail: spec.StartingCommit},
			Check{Name: "coding_paired_design", Passed: spec.PairID != "", Detail: spec.PairID},
			Check{Name: "coding_task_state_matches_condition", Passed: instance.TaskStateEnabled == spec.TaskState,
				Detail: fmt.Sprintf("task_state=%t condition=%s", instance.TaskStateEnabled, spec.Condition)},
			Check{Name: "coding_mpm_disabled", Passed: os.Getenv("DEEPCLAW_MPM_ENABLED") == "0" && spec.MPMWorkspace == "",
				Detail: os.Getenv("DEEPCLAW_MPM_ENABLED")},
			Check{Name: "coding_resident_cache_disabled",
				Passed: os.Getenv("DEEPCLAW_ACTIVE_MEMORY") == "0" && residentCacheRecords(instance, spec.SessionKey) == 0,
				Detail: os.Getenv("DEEPCLAW_ACTIVE_MEMORY")},
		)
		if taskOK {
			if !spec.Resume {
				sha, shaErr := CodingWorkspaceSHA(spec.AgentWorkspace)
				checks = append(checks, Check{Name: "coding_fixture_clean", Passed: shaErr == nil && sha == spec.FixtureSHA,
					Detail: spec.FixtureSHA})
			} else {
				// The resumed process must find the earlier session on disk. The
				// session store lives in PICOCLAW_HOME, not the workspace, so the
				// check reads its state rather than shelling out to git.
				history := sessionHistoryLen(context.Background(), instance, spec.SessionKey)
				sidecarOK := true
				if spec.TaskState {
					sidecarOK = instance.ActiveMemory != nil && hasFiles(instance.ActiveMemory.Dir())
				}
				checks = append(checks, Check{
					Name:   "coding_resume_state_present",
					Passed: history > 0 && sidecarOK,
					Detail: fmt.Sprintf("history=%d task_state=%t", history, sidecarOK),
				})
			}
			checks = append(checks,
				Check{Name: "coding_hidden_tests_absent", Passed: len(presentHiddenTests(spec.AgentWorkspace, task)) == 0,
					Detail: strings.Join(presentHiddenTests(spec.AgentWorkspace, task), ",")},
			)
			if head, err := gitHead(spec.AgentWorkspace); err == nil {
				checks = append(checks, Check{Name: "coding_known_starting_commit", Passed: head == spec.StartingCommit, Detail: head})
			} else {
				checks = append(checks, Check{Name: "coding_known_starting_commit", Passed: false, Detail: err.Error()})
			}
		}
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
	if spec.TaskState {
		checks = append(checks, Check{
			Name:   "task_state_not_in_workspace",
			Passed: !hasFiles(filepath.Join(spec.AgentWorkspace, ".deepclaw", "active-memory")),
			Detail: filepath.Join(spec.AgentWorkspace, ".deepclaw", "active-memory"),
		})
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

// scoreCodingResult is the only bridge between an executed trial and the
// condition-independent repository scorer. It passes no condition, task-state
// setting or response text into ScoreCodingTask.
func scoreCodingResult(spec TrialSpec, result TrialResult, started time.Time) (*CodingOutcome, error) {
	task, err := LoadCodingTask(spec.CodingTask)
	if err != nil {
		return nil, err
	}
	workDir := filepath.Join(spec.TrialDir, "scoring")
	outcome, err := ScoreCodingTask(task, spec.AgentWorkspace, workDir)
	if err != nil {
		return nil, err
	}
	outcome.StartingCommit = spec.StartingCommit
	outcome.Metrics = ComputeCodingMetrics(result, task)
	outcome.DurationMS = time.Since(started).Milliseconds()
	if outcome.FullSuccess {
		outcome.TimeToFullSuccessMS = outcome.DurationMS
	}
	if err := writeJSON(filepath.Join(workDir, "report.json"), outcome); err != nil {
		return nil, err
	}
	return &outcome, nil
}

func codingPostflightChecks(spec TrialSpec, result TrialResult) []Check {
	if spec.Experiment != "D" || spec.SkipScoring {
		return nil
	}
	task, taskErr := LoadCodingTask(spec.CodingTask)
	if taskErr != nil {
		return []Check{{Name: "coding_task_reload", Passed: false, Detail: taskErr.Error()}}
	}
	checks := []Check{
		{Name: "coding_scoring_completed", Passed: result.Coding != nil},
		{Name: "coding_hidden_tests_absent", Passed: len(presentHiddenTests(spec.AgentWorkspace, task)) == 0,
			Detail: strings.Join(presentHiddenTests(spec.AgentWorkspace, task), ",")},
		{Name: "coding_mpm_retrieval_zero", Passed: countLogMessage(spec.LogPath, "MPM retrieval") == 0},
		{Name: "coding_resident_cache_empty", Passed: result.ResidentCacheRecords == 0,
			Detail: fmt.Sprintf("records=%d", result.ResidentCacheRecords)},
		{Name: "coding_state_sidecar_outside_workspace",
			Passed: !hasFiles(filepath.Join(spec.AgentWorkspace, ".deepclaw", "active-memory"))},
		{Name: "coding_scoring_inside_trial_dir",
			Passed: result.Coding == nil || pathWithin(spec.TrialDir, result.Coding.ScoringDir)},
	}
	switch task.Interruption {
	case "compaction":
		compacted := false
		reachedInterruption := false
		for _, turn := range result.Turns {
			// 005B-R: an accepted no-op (history loss already achieved by
			// automatic compaction) exercises the same property as a fresh
			// explicit compaction.
			if (turn.Compacted || turn.CompactionAlreadyLost) && turn.SummaryCharsAfter > 0 {
				compacted = true
			}
		}
		for _, spec := range task.Turns {
			if spec.CompactBefore {
				for _, turn := range result.Turns {
					if turn.TurnID == spec.ID {
						reachedInterruption = true
					}
				}
			}
		}
		if result.TaskLimitReached && !reachedInterruption {
			// 005B-R: the task limit stopped the trial before the
			// interruption turn. The property is untested, not violated:
			// the limit itself is the recorded outcome.
			checks = append(checks, Check{Name: "coding_compaction_exercised", Passed: true,
				Detail: "interruption turn not reached; task limit stopped the trial first"})
		} else {
			checks = append(checks, Check{Name: "coding_compaction_exercised", Passed: compacted})
		}
	case "restart":
		restarted := false
		reachedRestart := false
		for _, turn := range result.Turns {
			if turn.Restarted {
				restarted = true
			}
		}
		for _, spec := range task.Turns {
			if spec.RestartBefore {
				for _, turn := range result.Turns {
					if turn.TurnID == spec.ID {
						reachedRestart = true
					}
				}
			}
		}
		if result.TaskLimitReached && !reachedRestart {
			checks = append(checks, Check{Name: "coding_restart_exercised", Passed: true,
				Detail: "restart turn not reached; task limit stopped the trial first"})
		} else {
			checks = append(checks, Check{Name: "coding_restart_exercised", Passed: restarted})
		}
	}
	return checks
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

// workspaceFileStatus snapshots `git status --porcelain` for a workspace.
// It returns nil for non-git workspaces; callers treat that as unknown.
func workspaceFileStatus(workspace string) map[string]string {
	cmd := exec.Command("git", "-C", workspace, "status", "--porcelain=v1")
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	status := map[string]string{}
	for _, line := range strings.Split(string(out), "\n") {
		if len(line) < 4 {
			continue
		}
		path := strings.TrimSpace(line[3:])
		if index := strings.Index(path, " -> "); index >= 0 {
			path = path[index+4:]
		}
		status[path] = strings.TrimSpace(line[:2])
		if path == "sessions/" || strings.HasPrefix(path, "sessions/") {
			// Session JSONL churns every turn; it is runtime state, not a
			// repository change attributable to a coding-tool attempt.
			delete(status, path)
		}
	}
	return status
}

// changedWorkspaceFiles returns paths whose git status differs between two
// snapshots (appeared, disappeared, or changed state), sorted.
func changedWorkspaceFiles(before, after map[string]string) []string {
	if before == nil || after == nil {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for path, status := range after {
		if before[path] != status && !seen[path] {
			seen[path] = true
			out = append(out, path)
		}
	}
	for path := range before {
		if _, ok := after[path]; !ok && !seen[path] {
			seen[path] = true
			out = append(out, path)
		}
	}
	return out
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
	for _, turn := range result.Turns {
		totals.TaskStatePromptChars += turn.TaskStatePromptChars
		totals.TaskStatePromptTokens += turn.TaskStatePromptTokens
	}
	totals.CodingPointsEarned = result.Score.PointsEarned
	totals.CodingPointsTotal = result.Score.PointsTotal
	if result.TaskStateFinal != nil && result.TaskStateFinal.Available {
		totals.TaskStateFinalEntries = result.TaskStateFinal.Entries
		totals.TaskStateFinalActiveEntries = result.TaskStateFinal.ActiveEntries
		totals.TaskStateRenderedChars = result.TaskStateFinal.RenderedChars
		totals.TaskStateRenderedTokens = result.TaskStateFinal.RenderedTokens
		totals.TaskStateStorageBytes = int(result.TaskStateFinal.StorageBytes)
	}
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
