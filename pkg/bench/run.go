package bench

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// RunOptions selects the experiment and the isolation/transport details for a
// run. Empty slices mean "every condition/scenario in the experiment".
type RunOptions struct {
	Experiment         string
	RunID              string
	OutDir             string
	ProviderKind       string // mock|live
	ProviderConfigPath string
	ModelName          string
	MPMBinary          string
	Trials             int
	Conditions         []string
	Scenarios          []string
	TrialRunner        []string // command prefix; defaults to the current executable
	Seed               int64    // 005A paired-order seed; derived from RunID when zero
}

type RunManifest struct {
	SchemaVersion  string     `json:"schema_version"`
	RunID          string     `json:"run_id"`
	Experiment     string     `json:"experiment"`
	StartedAt      string     `json:"started_at"`
	EndedAt        string     `json:"ended_at"`
	ProviderKind   string     `json:"provider_kind"`
	ProviderConfig string     `json:"provider_config,omitempty"`
	ModelRequested string     `json:"model_requested,omitempty"`
	MPMBinary      string     `json:"mpm_binary,omitempty"`
	TrialsPerCell  int        `json:"trials_per_cell"`
	Conditions     []string   `json:"conditions"`
	Scenarios      []string   `json:"scenarios"`
	Isolation      bool       `json:"isolation"`
	Notes          []string   `json:"notes,omitempty"`
	Provenance     Provenance `json:"provenance"`
	Seed           int64      `json:"seed,omitempty"`
	Order          []string   `json:"order,omitempty"`
	PairCount      int        `json:"pair_count,omitempty"`
}

func DefaultOutDir() string { return benchRoot() }

// DefaultMPMBinary returns the repository-local MPM executable when present.
func DefaultMPMBinary() string {
	candidate := filepath.Join(repoRoot(), ".lab", "bin", "mpm")
	if fileExists(candidate) {
		return candidate
	}
	return ""
}

func normalizeExperiment(experiment string) (string, error) {
	value := strings.ToUpper(strings.TrimSpace(experiment))
	if value != "A" && value != "B" && value != "C" && value != "D" {
		return "", fmt.Errorf("experiment must be A, B, C or D, got %q", experiment)
	}
	return value, nil
}

// RunExperiment prepares every trial tree in the parent process, executes each
// trial in a fresh subprocess, then writes per-trial results plus run-level
// aggregates. Fixtures are never created by the subprocess, so the process
// that records requests cannot accidentally materialise answer-bearing files
// while it initialises.
func RunExperiment(opts RunOptions) (*RunSummary, error) {
	experiment, err := normalizeExperiment(opts.Experiment)
	if err != nil {
		return nil, err
	}
	opts.Experiment = experiment
	if opts.Trials <= 0 {
		opts.Trials = 1
	}
	if opts.ProviderKind == "" {
		opts.ProviderKind = "mock"
	}
	if opts.ProviderKind != "mock" && opts.ProviderKind != "live" {
		return nil, fmt.Errorf("provider kind must be mock or live")
	}
	if opts.ProviderKind == "live" && strings.TrimSpace(opts.ProviderConfigPath) == "" {
		return nil, fmt.Errorf("live provider requires a config path")
	}
	if opts.ProviderKind == "live" && opts.ModelName == "" {
		return nil, fmt.Errorf("live provider requires an explicit model name")
	}
	if opts.RunID == "" {
		opts.RunID = newRunID()
	}
	if opts.OutDir == "" {
		opts.OutDir = DefaultOutDir()
	}
	absOut, err := filepath.Abs(opts.OutDir)
	if err != nil {
		return nil, err
	}
	opts.OutDir = absOut
	runDir := filepath.Join(opts.OutDir, opts.RunID)
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		return nil, err
	}
	if opts.MPMBinary == "" {
		opts.MPMBinary = DefaultMPMBinary()
	}
	if opts.MPMBinary != "" {
		if abs, err := filepath.Abs(opts.MPMBinary); err == nil {
			opts.MPMBinary = abs
		}
	}

	conditions := opts.Conditions
	if len(conditions) == 0 {
		conditions = ExperimentConditions(experiment)
	}
	scenarios := opts.Scenarios
	if len(scenarios) == 0 {
		scenarios = ExperimentScenarios(experiment)
	}
	for _, scenario := range scenarios {
		if _, err := Scenario(scenario); err != nil {
			return nil, err
		}
	}
	for _, condition := range conditions {
		valid := false
		for _, expected := range ExperimentConditions(experiment) {
			if condition == expected {
				valid = true
				break
			}
		}
		if !valid {
			return nil, fmt.Errorf("condition %q does not belong to experiment %s", condition, experiment)
		}
	}

	started := time.Now()
	provenance := loadProvenance()
	plan, seed, pairCount, err := buildTrialPlan(opts, conditions, scenarios)
	if err != nil {
		return nil, err
	}
	manifest := RunManifest{
		SchemaVersion: SchemaVersion, RunID: opts.RunID, Experiment: experiment,
		StartedAt:    started.UTC().Format(time.RFC3339Nano),
		ProviderKind: opts.ProviderKind, ProviderConfig: opts.ProviderConfigPath,
		ModelRequested: opts.ModelName, MPMBinary: opts.MPMBinary,
		TrialsPerCell: opts.Trials, Conditions: conditions, Scenarios: scenarios,
		Isolation: true, Provenance: provenance, Seed: seed, PairCount: pairCount,
		Notes: []string{
			"all conditions run in separate subprocesses with fresh HOME, XDG, workspace, session store and MPM state",
			"subprocess isolation is enabled for every condition; MPM conditions additionally mask the MPM store",
			"LLM request counts come from RecordingProvider.Chat calls, never from log lines",
		},
	}
	if experiment == "D" {
		manifest.Notes = append(manifest.Notes,
			"005A: control and treatment use the same binary, four-tool coding profile, history and compaction settings",
			"005A: MPM retrieval and the resident MPM cache are disabled in both conditions",
			"005A: treatment enables the frozen 004C boundary task state; control sets DEEPCLAW_TASK_STATE=0",
			"005A: control/treatment pair order and pair sequence are randomized from a recorded seed",
			"005A: task success is independently scored from the resulting repository by hidden tests, never from the response",
		)
	}
	if opts.ProviderKind == "mock" {
		manifest.Notes = append(manifest.Notes, "mock provider is deterministic and exists only to validate the harness")
	}
	if err := writeJSON(filepath.Join(runDir, "manifest.json"), redactValue(manifest)); err != nil {
		return nil, err
	}

	summary := &RunSummary{
		SchemaVersion: SchemaVersion, RunID: opts.RunID, Experiment: experiment,
		StartedAt: manifest.StartedAt, Provenance: provenance,
	}
	runner := opts.TrialRunner
	if len(runner) == 0 {
		exe, err := os.Executable()
		if err != nil {
			return nil, fmt.Errorf("resolve trial executable: %w", err)
		}
		runner = []string{exe, "trial"}
	}

	for _, item := range plan {
		scenario, err := Scenario(item.Scenario)
		if err != nil {
			return nil, err
		}
		trialID := NewTrialID(opts.RunID, item.Condition, item.Scenario, item.Repetition)
		spec, err := buildTrialSpecFromPlan(opts, runDir, item, scenario, trialID)
		if err != nil {
			return nil, fmt.Errorf("prepare %s: %w", trialID, err)
		}
		if err := PrepareTrial(&spec); err != nil {
			return nil, fmt.Errorf("prepare fixture %s: %w", trialID, err)
		}
		manifest.Order = append(manifest.Order, trialID)
		result := executeTrialSpec(runDir, spec, runner, opts.ProviderKind, item)
		summary.Trials = append(summary.Trials, result)
		for _, path := range append([]string{spec.LogPath}, processLogPaths(spec)...) {
			redactFile(path)
		}
	}

	summary.EndedAt = nowRFC3339()
	summary.Aggregates = aggregateTrials(summary.Trials)
	summary.Validation = validationSummary(summary.Trials)
	if experiment == "D" {
		summary.CodingPairs, summary.Coding = summarizeCodingPairs(summary.Trials)
	}
	manifest.EndedAt = summary.EndedAt
	if err := writeJSON(filepath.Join(runDir, "manifest.json"), redactValue(manifest)); err != nil {
		return nil, err
	}
	if err := writeJSON(filepath.Join(runDir, "summary.json"), redactValue(summary)); err != nil {
		return nil, err
	}
	writeTrialsJSONL(filepath.Join(runDir, "trials.jsonl"), summary.Trials)
	return summary, nil
}

type trialPlanItem struct {
	Condition     string
	Scenario      string
	PairID        string
	Repetition    int
	SequenceIndex int
}

// buildTrialPlan produces the execution order. Experiments A-C keep their
// sequential condition-major order. Experiment D uses a randomized paired
// design: each repetition of each task contributes one control and one
// treatment trial, the two members are ordered randomly, and the pairs
// themselves are shuffled. The seed is recorded so the order is reproducible.
func buildTrialPlan(opts RunOptions, conditions, scenarios []string) ([]trialPlanItem, int64, int, error) {
	if !strings.EqualFold(opts.Experiment, "D") {
		var plan []trialPlanItem
		sequence := 0
		for _, condition := range conditions {
			for _, scenario := range scenarios {
				for index := 1; index <= opts.Trials; index++ {
					plan = append(plan, trialPlanItem{
						Condition: condition, Scenario: scenario,
						Repetition: index, SequenceIndex: sequence,
					})
					sequence++
				}
			}
		}
		return plan, 0, 0, nil
	}

	seed := opts.Seed
	if seed == 0 {
		seed = seedFromRunID(opts.RunID)
	}
	rng := rand.New(rand.NewSource(seed))

	type pair struct {
		scenario   string
		repetition int
		pairID     string
	}
	var pairs []pair
	for _, scenario := range scenarios {
		for repetition := 1; repetition <= opts.Trials; repetition++ {
			pairs = append(pairs, pair{
				scenario:   scenario,
				repetition: repetition,
				pairID:     fmt.Sprintf("%s-r%02d", scenario, repetition),
			})
		}
	}
	rng.Shuffle(len(pairs), func(i, j int) { pairs[i], pairs[j] = pairs[j], pairs[i] })

	var plan []trialPlanItem
	sequence := 0
	pairCount := 0
	for _, p := range pairs {
		members := append([]string(nil), conditions...)
		// Do not mutate the caller's slice when shuffling pair members.
		rng.Shuffle(len(members), func(i, j int) { members[i], members[j] = members[j], members[i] })
		if len(members) >= 2 {
			pairCount++
		}
		for _, condition := range members {
			plan = append(plan, trialPlanItem{
				Condition: condition, Scenario: p.scenario, PairID: p.pairID,
				Repetition: p.repetition, SequenceIndex: sequence,
			})
			sequence++
		}
	}
	return plan, seed, pairCount, nil
}

func seedFromRunID(runID string) int64 {
	sum := sha256.Sum256([]byte("deepclaw-005a-order-v1\x00" + runID))
	return int64(binary.BigEndian.Uint64(sum[:8]))
}

func buildTrialSpecFromPlan(
	opts RunOptions,
	runDir string,
	item trialPlanItem,
	scenario ScenarioSpec,
	trialID string,
) (TrialSpec, error) {
	trialDir := filepath.Join(runDir, "trials", trialID)
	spec := TrialSpec{
		SchemaVersion: SchemaVersion, RunID: opts.RunID, TrialID: trialID,
		Experiment: opts.Experiment, Condition: item.Condition, Scenario: scenario.Name,
		ProviderKind: opts.ProviderKind, ProviderConfigPath: opts.ProviderConfigPath,
		ModelName: opts.ModelName, MPMBinary: opts.MPMBinary,
		SessionKey: NewSessionKey(opts.RunID, trialID),
		TrialDir:   trialDir, AgentWorkspace: filepath.Join(trialDir, "workspace"),
		PicoclawHome:    filepath.Join(trialDir, "picoclaw-home"),
		LogPath:         filepath.Join(trialDir, "logs", "trial.log"),
		RawPath:         filepath.Join(trialDir, "raw", "requests.jsonl"),
		ResultPath:      filepath.Join(trialDir, "result.json"),
		MemoryPolicy:    ConditionMemoryPolicy(item.Condition),
		ToolProfile:     ConditionToolProfile(item.Condition),
		Isolation:       true,
		MPMProject:      "atlas",
		ForeignProject:  "zebra",
		ExpectedAnswer:  scenario.Expected,
		ForbiddenAnswer: scenario.Forbidden,
		Abstain:         scenario.Abstain,
		Facts:           append([]string(nil), scenario.Facts...),
		ForeignFacts:    append([]string(nil), scenario.ForeignFacts...),
		FixtureProbe:    filepath.Join(trialDir, "fixtures", "canary.txt"),
		MaxTokens:       256, MaxToolIterations: trialToolIterations(opts.Experiment), ContextWindow: 32768,
		CodingExpectedFile:    scenario.CodingFile,
		CodingExpectedContent: scenario.CodingContent,
		TaskState:             ConditionTaskState(item.Condition),
		TaskStateBudget:       500,
		TaskStateResidency:    ConditionTaskStateResidency(item.Condition),
		PairID:                item.PairID,
		Repetition:            item.Repetition,
		SequenceIndex:         item.SequenceIndex,
	}
	if strings.EqualFold(opts.Experiment, "D") {
		spec.CodingTask = scenario.CodingTask
		spec.MPMDisabled = true
		spec.ResidentCacheDisabled = true
		spec.ScoreCacheDir = filepath.Join(runDir, "gocache")
		if scenario.MaxTokens > 0 {
			spec.MaxTokens = scenario.MaxTokens
		}
		if scenario.MaxToolIterations > 0 {
			spec.MaxToolIterations = scenario.MaxToolIterations
		}
		if scenario.ContextWindow > 0 {
			spec.ContextWindow = scenario.ContextWindow
		}
		spec.SummarizeTokenPercent = scenario.SummarizeTokenPercent
	}
	if spec.MemoryPolicy == "mpm" || spec.MemoryPolicy == "mpm_active" {
		spec.MPMWorkspace = filepath.Join(trialDir, "mpm", "workspace")
		spec.MPMHome = filepath.Join(trialDir, "mpm", "home")
		spec.MPMProbe = spec.MPMWorkspace
	}
	if scenario.ForeignFacts != nil && spec.ForeignProject == "" {
		return spec, fmt.Errorf("scenario %s has foreign facts without a foreign project", scenario.Name)
	}
	if spec.ProviderKind == "live" && spec.ModelName == "" {
		return spec, fmt.Errorf("live trial requires an explicit model")
	}
	if len(scenario.Turns) > 0 {
		spec.Turns = append([]TurnSpec(nil), scenario.Turns...)
	} else {
		spec.Turns = []TurnSpec{{ID: "t1", Prompt: scenario.Question}}
	}
	if spec.ProviderKind == "mock" && spec.ModelName == "" {
		spec.ModelName = "mock-003f"
	}
	return spec, nil
}

func trialToolIterations(experiment string) int {
	if strings.EqualFold(experiment, "C") {
		return 8
	}
	return 3
}

// buildTrialSpec keeps the 003F test-facing signature. Experiment D callers
// use buildTrialSpecFromPlan so the paired design metadata is preserved.
func buildTrialSpec(
	opts RunOptions,
	runDir, condition string,
	scenario ScenarioSpec,
	trialID string,
	index int,
) (TrialSpec, error) {
	return buildTrialSpecFromPlan(opts, runDir, trialPlanItem{
		Condition: condition, Scenario: scenario.Name, Repetition: index,
	}, scenario, trialID)
}

func runTrialSubprocess(runDir string, spec TrialSpec, runner []string, providerKind string) (TrialResult, error) {
	var result TrialResult
	if len(runner) == 0 {
		return result, fmt.Errorf("empty trial runner")
	}
	// The child reads the spec from DEEPCLAW_BENCH_TRIAL_SPEC, so the same
	// invocation works for the CLI and for the test-binary helper process.
	// Written here, not in PrepareTrial, because a restart segment carries its
	// own turn range and result/raw paths.
	if err := writeJSON(trialSpecPath(spec), spec); err != nil {
		return result, fmt.Errorf("write trial spec: %w", err)
	}
	args := append([]string(nil), runner[1:]...)
	cmd := exec.Command(runner[0], args...)
	cmd.Dir = repoRoot()
	cmd.Env = trialProcessEnv(spec, runDir)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	processLog := processLogPath(spec)
	_ = os.MkdirAll(filepath.Dir(processLog), 0o755)
	_ = os.WriteFile(processLog, append(append([]byte{}, stdout.Bytes()...), stderr.Bytes()...), 0o600)
	_ = redactFileNow(processLog)
	if data, readErr := os.ReadFile(spec.ResultPath); readErr == nil {
		if jsonErr := json.Unmarshal(data, &result); jsonErr == nil {
			return result, err
		}
	}
	if err != nil {
		return result, fmt.Errorf("trial process: %w (stderr: %s)", err, firstLines(stderr.String(), 8))
	}
	return result, fmt.Errorf("trial process produced no readable result at %s", spec.ResultPath)
}

func trialProcessEnv(spec TrialSpec, runDir string) []string {
	base := map[string]string{
		"HOME":                      filepath.Join(spec.TrialDir, "home"),
		"XDG_CONFIG_HOME":           filepath.Join(spec.TrialDir, "xdg", "config"),
		"XDG_CACHE_HOME":            filepath.Join(spec.TrialDir, "xdg", "cache"),
		"XDG_DATA_HOME":             filepath.Join(spec.TrialDir, "xdg", "data"),
		"XDG_STATE_HOME":            filepath.Join(spec.TrialDir, "xdg", "state"),
		"TMPDIR":                    filepath.Join(spec.TrialDir, "tmp"),
		"PICOCLAW_HOME":             spec.PicoclawHome,
		"PICOCLAW_LOG_FILE":         spec.LogPath,
		"LANG":                      "C.UTF-8",
		"LC_ALL":                    "C.UTF-8",
		"TZ":                        "UTC",
		"PATH":                      trialPath(spec),
		"DEEPCLAW_BENCH_ROOT":       runDir,
		"DEEPCLAW_CODING_PROFILE":   boolEnv(spec.ToolProfile == "coding"),
		"DEEPCLAW_BENCH_TRIAL_SPEC": trialSpecPath(spec),
	}
	if spec.Experiment == "D" {
		base["DEEPCLAW_TASK_STATE"] = boolEnv(spec.TaskState)
		base["DEEPCLAW_MPM_ENABLED"] = "0"
		base["DEEPCLAW_ACTIVE_MEMORY"] = "0"
		base["GOPROXY"] = "off"
		base["GOFLAGS"] = "-mod=readonly"
		base["GOTOOLCHAIN"] = "local"
		base["CGO_ENABLED"] = "0"
		base["GO111MODULE"] = "on"
		if spec.SummarizeTokenPercent > 0 {
			base["PICOCLAW_AGENTS_DEFAULTS_SUMMARIZE_TOKEN_PERCENT"] = fmt.Sprintf("%d", spec.SummarizeTokenPercent)
		}
	}
	if spec.MemoryPolicy == "mpm" || spec.MemoryPolicy == "mpm_active" {
		base["DEEPCLAW_MPM_ENABLED"] = "1"
		base["MPM_WORKSPACE"] = spec.MPMWorkspace
		base["DEEPCLAW_MPM_HOME"] = spec.MPMHome
		base["DEEPCLAW_MPM_EXECUTABLE"] = spec.MPMBinary
		base["DEEPCLAW_MPM_PROJECT"] = spec.MPMProject
		base["DEEPCLAW_MPM_BUDGET"] = "350"
		if spec.MemoryPolicy == "mpm_active" {
			base["DEEPCLAW_ACTIVE_MEMORY"] = "1"
		}
	}
	if spec.TaskState {
		base["DEEPCLAW_TASK_STATE"] = "1"
		base["DEEPCLAW_TASK_STATE_BUDGET"] = fmt.Sprintf("%d", spec.TaskStateBudget)
		if spec.TaskStateResidency != "" {
			base["DEEPCLAW_TASK_STATE_RESIDENCY"] = spec.TaskStateResidency
		}
	}
	env := make([]string, 0, len(base))
	for key, value := range base {
		env = append(env, key+"="+value)
	}
	sort.Strings(env)
	return env
}

func trialPath(spec TrialSpec) string {
	if spec.Experiment == "D" {
		return "/usr/local/go/bin:/usr/local/bin:/usr/bin:/bin"
	}
	return "/usr/local/bin:/usr/bin:/bin"
}

func firstLines(text string, n int) string {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.Join(lines, " | ")
}

func processLogPath(spec TrialSpec) string {
	if spec.Segment > 0 {
		return filepath.Join(spec.TrialDir, fmt.Sprintf("process-s%d.log", spec.Segment))
	}
	return filepath.Join(spec.TrialDir, "process.log")
}

func trialSpecPath(spec TrialSpec) string {
	if spec.Segment > 0 {
		return filepath.Join(spec.TrialDir, fmt.Sprintf("trial-spec-s%d.json", spec.Segment))
	}
	return filepath.Join(spec.TrialDir, "trial-spec.json")
}

func processLogPaths(spec TrialSpec) []string {
	paths, _ := filepath.Glob(filepath.Join(spec.TrialDir, "process*.log"))
	return paths
}

type trialSegment struct {
	start  int
	end    int
	resume bool
}

// trialSegments splits a D trial immediately before each RestartBefore turn.
// Each segment runs in a new OS process but shares the trial's workspace,
// session store and task-state sidecar. Non-D trials keep one process so the
// existing 003F behavior is unchanged.
func trialSegments(spec TrialSpec) []trialSegment {
	if spec.Experiment != "D" || len(spec.Turns) == 0 {
		return []trialSegment{{start: 0, end: len(spec.Turns)}}
	}
	var segments []trialSegment
	start := 0
	resume := false
	for i, turn := range spec.Turns {
		if turn.RestartBefore && i > start {
			segments = append(segments, trialSegment{start: start, end: i, resume: resume})
			start = i
			resume = true
		}
	}
	segments = append(segments, trialSegment{start: start, end: len(spec.Turns), resume: resume})
	return segments
}

// executeTrialSpec runs one plan item. A restart trial is executed as multiple
// subprocesses; the merged result is written to the ordinary result path.
func executeTrialSpec(runDir string, spec TrialSpec, runner []string, providerKind string, item trialPlanItem) TrialResult {
	segments := trialSegments(spec)
	if len(segments) <= 1 {
		spec.SegmentCount = 1
		result, err := runTrialSubprocess(runDir, spec, runner, providerKind)
		if err != nil && result.TrialID == "" {
			result = TrialResult{
				SchemaVersion: SchemaVersion, RunID: spec.RunID, TrialID: spec.TrialID,
				Experiment: spec.Experiment, Condition: item.Condition, Scenario: item.Scenario,
				Status: "error", Error: err.Error(),
				StartedAt: nowRFC3339(), EndedAt: nowRFC3339(),
				RawPath: spec.RawPath, LogPath: spec.LogPath, ResultPath: spec.ResultPath,
				PairID: item.PairID, Repetition: item.Repetition, SequenceIndex: item.SequenceIndex,
				StartingCommit: spec.StartingCommit, ProcessStarts: 1,
			}
			_ = writeJSON(spec.ResultPath, redactValue(result))
		}
		if result.ProcessStarts == 0 {
			result.ProcessStarts = 1
		}
		return result
	}

	started := time.Now()
	results := make([]TrialResult, 0, len(segments))
	for i, segment := range segments {
		segmentSpec := spec
		segmentSpec.TurnStart = segment.start
		segmentSpec.TurnEnd = segment.end
		segmentSpec.Resume = segment.resume
		segmentSpec.Segment = i + 1
		segmentSpec.SegmentCount = len(segments)
		segmentSpec.SkipScoring = i < len(segments)-1
		segmentSpec.RequestIDPrefix = fmt.Sprintf("s%d-", i+1)
		segmentSpec.RawPath = filepath.Join(spec.TrialDir, "raw", fmt.Sprintf("requests-s%d.jsonl", i+1))
		segmentSpec.ResultPath = filepath.Join(spec.TrialDir, fmt.Sprintf("result-s%d.json", i+1))
		result, err := runTrialSubprocess(runDir, segmentSpec, runner, providerKind)
		if err != nil && result.TrialID == "" {
			result = TrialResult{
				SchemaVersion: SchemaVersion, RunID: spec.RunID, TrialID: segmentSpec.TrialID,
				Experiment: spec.Experiment, Condition: item.Condition, Scenario: item.Scenario,
				Status: "error", Error: err.Error(),
				StartedAt: nowRFC3339(), EndedAt: nowRFC3339(),
				RawPath: segmentSpec.RawPath, LogPath: segmentSpec.LogPath,
				ResultPath: segmentSpec.ResultPath,
				PairID:     item.PairID, Repetition: item.Repetition, SequenceIndex: item.SequenceIndex,
				StartingCommit: spec.StartingCommit,
			}
			_ = writeJSON(segmentSpec.ResultPath, redactValue(result))
		}
		results = append(results, result)
		if result.Status == "error" {
			break
		}
	}

	merged := mergeTrialSegments(spec, results, time.Since(started))
	_ = mergeTrialRaw(merged.RawPath, results)
	_ = writeJSON(spec.ResultPath, redactValue(merged))
	return merged
}

func mergeTrialSegments(base TrialSpec, results []TrialResult, elapsed time.Duration) TrialResult {
	if len(results) == 0 {
		return TrialResult{
			SchemaVersion: SchemaVersion, RunID: base.RunID, TrialID: base.TrialID,
			Experiment: base.Experiment, Condition: base.Condition, Scenario: base.Scenario,
			Status: "error", Error: "no trial segment produced a result",
			StartedAt: nowRFC3339(), EndedAt: nowRFC3339(),
			RawPath: base.RawPath, LogPath: base.LogPath, ResultPath: base.ResultPath,
			ProcessStarts: 0,
		}
	}
	last := results[len(results)-1]
	merged := last
	merged.SchemaVersion = SchemaVersion
	merged.RunID = base.RunID
	merged.TrialID = base.TrialID
	merged.Experiment = base.Experiment
	merged.Condition = base.Condition
	merged.Scenario = base.Scenario
	merged.RawPath = base.RawPath
	merged.LogPath = base.LogPath
	merged.ResultPath = base.ResultPath
	merged.StartingCommit = base.StartingCommit
	merged.ProcessStarts = len(results)
	merged.StartedAt = results[0].StartedAt
	merged.EndedAt = last.EndedAt
	merged.DurationMS = elapsed.Milliseconds()
	merged.Turns = nil
	merged.Requests = nil
	merged.ToolCalls = nil
	merged.Preflight = mergeChecksByName(results, func(result TrialResult) []Check { return result.Preflight })
	merged.Postflight = mergeChecksByName(results, func(result TrialResult) []Check { return result.Postflight })
	var errs []string
	taskStateStorage := 0
	for _, result := range results {
		merged.Turns = append(merged.Turns, result.Turns...)
		merged.Requests = append(merged.Requests, result.Requests...)
		merged.ToolCalls = append(merged.ToolCalls, result.ToolCalls...)
		// The task-state sidecar is shared by every segment, so the last
		// segment's measurement is the end state; summing would double count.
		if result.Totals.TaskStateStorageBytes > 0 {
			taskStateStorage = result.Totals.TaskStateStorageBytes
		}
		if result.Error != "" {
			errs = append(errs, result.Error)
		}
		if result.Status != "pass" {
			merged.Status = result.Status
		}
	}
	merged.Totals = totalsFor(merged)
	merged.Totals.E2EMS = elapsed.Milliseconds()
	// All segments append to one trial log, so count the shared file once
	// instead of summing per-segment counts of the same log.
	merged.Totals.MPMRetrievalCalls = countLogMessage(base.LogPath, "MPM retrieval")
	if taskStateStorage > 0 {
		merged.Totals.TaskStateStorageBytes = taskStateStorage
	}
	if len(errs) > 0 {
		merged.Error = strings.Join(uniqueStrings(errs), "; ")
		merged.Status = "error"
	}
	if task, err := LoadCodingTask(base.CodingTask); err == nil && task.Interruption == "restart" {
		merged.Postflight = append(merged.Postflight, Check{
			Name: "coding_process_restart", Passed: len(results) >= 2,
			Detail: fmt.Sprintf("segments=%d", len(results)),
		})
		if merged.Status == "pass" && len(results) < 2 {
			merged.Status = "fail"
			merged.Error = "restart task did not run in a new process"
		}
	}
	return merged
}

func mergeChecksByName(results []TrialResult, pick func(TrialResult) []Check) []Check {
	seen := map[string]bool{}
	var out []Check
	for _, result := range results {
		for _, check := range pick(result) {
			if seen[check.Name] {
				continue
			}
			seen[check.Name] = true
			out = append(out, check)
		}
	}
	return out
}

func mergeTrialRaw(dst string, results []TrialResult) error {
	var b strings.Builder
	for _, result := range results {
		data, err := os.ReadFile(result.RawPath)
		if err != nil {
			continue
		}
		b.Write(data)
		if len(data) > 0 && data[len(data)-1] != '\n' {
			b.WriteByte('\n')
		}
	}
	return writeFilePrivate(dst, []byte(RedactSecrets(b.String())))
}

func redactFile(path string) {
	_ = redactFileNow(path)
}

func redactFileNow(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	redacted := RedactBytes(data)
	if bytes.Equal(data, redacted) {
		return nil
	}
	return os.WriteFile(path, redacted, 0o600)
}

func aggregateTrials(trials []TrialResult) []Aggregate {
	type key struct{ condition, scenario string }
	groups := map[key][]TrialResult{}
	for _, trial := range trials {
		k := key{trial.Condition, trial.Scenario}
		groups[k] = append(groups[k], trial)
	}
	keys := make([]key, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].condition != keys[j].condition {
			return keys[i].condition < keys[j].condition
		}
		return keys[i].scenario < keys[j].scenario
	})
	out := make([]Aggregate, 0, len(keys))
	for _, k := range keys {
		group := groups[k]
		agg := Aggregate{Condition: k.condition, Scenario: k.scenario, Trials: len(group)}
		var prompt, completion, total, e2e, retrieval, taskEntries, taskActive, taskTokens, taskPromptTokens, taskStorage []float64
		var partial, points, requests, toolCalls, repeatedReads, repeatedFailed, diagnosis []float64
		for _, trial := range group {
			if trial.Score.Correct {
				agg.Correct++
			}
			if trial.Score.AbstainOK {
				agg.AbstainOK++
			}
			if trial.Score.ForbiddenHit {
				agg.ForbiddenHits++
			}
			agg.LLMRequests += trial.Totals.LLMRequests
			agg.MPMRetrievalCalls += trial.Totals.MPMRetrievalCalls
			requests = append(requests, float64(trial.Totals.LLMRequests))
			toolCalls = append(toolCalls, float64(trial.Totals.ToolCalls))
			if trial.Coding != nil {
				if trial.Coding.FullSuccess {
					agg.FullSuccess++
				}
				if len(trial.Coding.ConstraintViolations) > 0 {
					agg.ConstraintViolations++
				}
				partial = append(partial, trial.Coding.Partial)
				points = append(points, float64(trial.Coding.PointsEarned))
				repeatedReads = append(repeatedReads, float64(trial.Coding.Metrics.RepeatedReads))
				repeatedFailed = append(repeatedFailed, float64(trial.Coding.Metrics.RepeatedFailedInvestigations))
				diagnosis = append(diagnosis, float64(trial.Coding.Metrics.DiagnosisStatements))
			}
			reported := 0
			for _, request := range trial.Requests {
				if request.Usage.Reported {
					reported++
				}
			}
			if reported == 0 {
				agg.UsageMissingTrials++
			} else {
				if trial.Totals.PromptTokens != 0 {
					prompt = append(prompt, float64(trial.Totals.PromptTokens))
				}
				if trial.Totals.CompletionTokens != 0 {
					completion = append(completion, float64(trial.Totals.CompletionTokens))
				}
				if trial.Totals.TotalTokens != 0 {
					total = append(total, float64(trial.Totals.TotalTokens))
				}
			}
			e2e = append(e2e, float64(trial.Totals.E2EMS))
			if trial.Totals.RetrievalLatencyMS > 0 {
				retrieval = append(retrieval, float64(trial.Totals.RetrievalLatencyMS))
			}
			if trial.TaskStateFinal != nil && trial.TaskStateFinal.Available {
				taskEntries = append(taskEntries, float64(trial.Totals.TaskStateFinalEntries))
				taskActive = append(taskActive, float64(trial.Totals.TaskStateFinalActiveEntries))
				taskTokens = append(taskTokens, float64(trial.Totals.TaskStateRenderedTokens))
				if trial.Totals.TaskStateRenderedTokens > agg.MaxTaskStateTokens {
					agg.MaxTaskStateTokens = trial.Totals.TaskStateRenderedTokens
				}
				taskPromptTokens = append(taskPromptTokens, float64(trial.Totals.TaskStatePromptTokens))
				if trial.Totals.TaskStatePromptTokens > agg.MaxTaskStatePromptTokens {
					agg.MaxTaskStatePromptTokens = trial.Totals.TaskStatePromptTokens
				}
				taskStorage = append(taskStorage, float64(trial.Totals.TaskStateStorageBytes))
			}
		}
		agg.MeanPromptTokens = meanPtr(prompt)
		agg.MeanCompletionTokens = meanPtr(completion)
		agg.MeanTotalTokens = meanPtr(total)
		agg.MeanE2EMS = meanPtr(e2e)
		agg.MeanRetrievalMS = meanPtr(retrieval)
		agg.MeanTaskStateEntries = meanPtr(taskEntries)
		agg.MeanTaskStateActiveEntries = meanPtr(taskActive)
		agg.MeanTaskStateTokens = meanPtr(taskTokens)
		agg.MeanTaskStatePromptTokens = meanPtr(taskPromptTokens)
		agg.MeanTaskStateStorageBytes = meanPtr(taskStorage)
		agg.MeanPartial = meanPtr(partial)
		agg.MeanPointsEarned = meanPtr(points)
		agg.MeanRequests = meanPtr(requests)
		agg.MeanToolCalls = meanPtr(toolCalls)
		agg.MeanRepeatedReads = meanPtr(repeatedReads)
		agg.MeanRepeatedFailed = meanPtr(repeatedFailed)
		agg.MeanDiagnosis = meanPtr(diagnosis)
		out = append(out, agg)
	}
	return out
}

func meanPtr(values []float64) *float64 {
	if len(values) == 0 {
		return nil
	}
	sum := 0.0
	for _, value := range values {
		sum += value
	}
	mean := sum / float64(len(values))
	return &mean
}

func validationSummary(trials []TrialResult) ValidationSummary {
	summary := ValidationSummary{Total: len(trials)}
	for _, trial := range trials {
		switch trial.Status {
		case "pass":
			summary.Passed++
		case "fail":
			summary.Failed++
		default:
			summary.Errors++
		}
	}
	return summary
}

func writeTrialsJSONL(path string, trials []TrialResult) {
	var b strings.Builder
	for _, trial := range trials {
		data, err := json.Marshal(redactValue(trial))
		if err != nil {
			continue
		}
		b.Write(data)
		b.WriteByte('\n')
	}
	_ = os.WriteFile(path, []byte(b.String()), 0o600)
}

// LoadTrialSpec reads a serialized trial specification.
func LoadTrialSpec(path string) (*TrialSpec, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var spec TrialSpec
	if err := json.Unmarshal(data, &spec); err != nil {
		return nil, err
	}
	if spec.SchemaVersion != SchemaVersion {
		return nil, fmt.Errorf("trial spec schema %q is not %q", spec.SchemaVersion, SchemaVersion)
	}
	return &spec, nil
}

// RunTrialFile is the subprocess entry point.
func RunTrialFile(path string) (TrialResult, error) {
	spec, err := LoadTrialSpec(path)
	if err != nil {
		return TrialResult{}, err
	}
	result := RunTrial(*spec)
	if result.Status == "pass" {
		return result, nil
	}
	if result.Error == "" {
		result.Error = "trial did not pass"
	}
	return result, fmt.Errorf("%s", result.Error)
}
