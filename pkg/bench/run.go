package bench

import (
	"bytes"
	"encoding/json"
	"fmt"
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
	if value != "A" && value != "B" && value != "C" {
		return "", fmt.Errorf("experiment must be A, B or C, got %q", experiment)
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
	manifest := RunManifest{
		SchemaVersion: SchemaVersion, RunID: opts.RunID, Experiment: experiment,
		StartedAt:    started.UTC().Format(time.RFC3339Nano),
		ProviderKind: opts.ProviderKind, ProviderConfig: opts.ProviderConfigPath,
		ModelRequested: opts.ModelName, MPMBinary: opts.MPMBinary,
		TrialsPerCell: opts.Trials, Conditions: conditions, Scenarios: scenarios,
		Isolation: true, Provenance: provenance,
		Notes: []string{
			"all conditions run in separate subprocesses with fresh HOME, XDG, workspace, session store and MPM state",
			"subprocess isolation is enabled for every condition; MPM conditions additionally mask the MPM store",
			"LLM request counts come from RecordingProvider.Chat calls, never from log lines",
		},
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

	for _, condition := range conditions {
		for _, scenarioName := range scenarios {
			scenario, err := Scenario(scenarioName)
			if err != nil {
				return nil, err
			}
			for index := 1; index <= opts.Trials; index++ {
				trialID := NewTrialID(opts.RunID, condition, scenarioName, index)
				spec, err := buildTrialSpec(opts, runDir, condition, scenario, trialID, index)
				if err != nil {
					return nil, fmt.Errorf("prepare %s: %w", trialID, err)
				}
				if err := PrepareTrial(&spec); err != nil {
					return nil, fmt.Errorf("prepare fixture %s: %w", trialID, err)
				}
				result, err := runTrialSubprocess(runDir, spec, runner, opts.ProviderKind)
				if err != nil {
					result = TrialResult{
						SchemaVersion: SchemaVersion, RunID: opts.RunID, TrialID: trialID,
						Experiment: experiment, Condition: condition, Scenario: scenarioName,
						Status: "error", Error: err.Error(),
						StartedAt: nowRFC3339(), EndedAt: nowRFC3339(),
						RawPath: spec.RawPath, LogPath: spec.LogPath,
					}
					_ = writeJSON(spec.ResultPath, redactValue(result))
				}
				redactFile(spec.LogPath)
				redactFile(filepath.Join(spec.TrialDir, "process.log"))
				summary.Trials = append(summary.Trials, result)
			}
		}
	}

	summary.EndedAt = nowRFC3339()
	summary.Aggregates = aggregateTrials(summary.Trials)
	summary.Validation = validationSummary(summary.Trials)
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

func buildTrialSpec(
	opts RunOptions,
	runDir, condition string,
	scenario ScenarioSpec,
	trialID string,
	index int,
) (TrialSpec, error) {
	trialDir := filepath.Join(runDir, "trials", trialID)
	spec := TrialSpec{
		SchemaVersion: SchemaVersion, RunID: opts.RunID, TrialID: trialID,
		Experiment: opts.Experiment, Condition: condition, Scenario: scenario.Name,
		ProviderKind: opts.ProviderKind, ProviderConfigPath: opts.ProviderConfigPath,
		ModelName: opts.ModelName, MPMBinary: opts.MPMBinary,
		SessionKey: NewSessionKey(opts.RunID, trialID),
		TrialDir:   trialDir, AgentWorkspace: filepath.Join(trialDir, "workspace"),
		PicoclawHome:    filepath.Join(trialDir, "picoclaw-home"),
		LogPath:         filepath.Join(trialDir, "logs", "trial.log"),
		RawPath:         filepath.Join(trialDir, "raw", "requests.jsonl"),
		ResultPath:      filepath.Join(trialDir, "result.json"),
		MemoryPolicy:    ConditionMemoryPolicy(condition),
		ToolProfile:     ConditionToolProfile(condition),
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
		TaskState:             ConditionTaskState(condition),
		TaskStateBudget:       500,
		TaskStateResidency:    ConditionTaskStateResidency(condition),
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
	_ = index
	return spec, nil
}

func trialToolIterations(experiment string) int {
	if strings.EqualFold(experiment, "C") {
		return 8
	}
	return 3
}

func runTrialSubprocess(runDir string, spec TrialSpec, runner []string, providerKind string) (TrialResult, error) {
	var result TrialResult
	if len(runner) == 0 {
		return result, fmt.Errorf("empty trial runner")
	}
	// The child reads the spec from DEEPCLAW_BENCH_TRIAL_SPEC, so the same
	// invocation works for the CLI and for the test-binary helper process.
	args := append([]string(nil), runner[1:]...)
	cmd := exec.Command(runner[0], args...)
	cmd.Dir = repoRoot()
	cmd.Env = trialProcessEnv(spec, runDir)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	processLog := filepath.Join(spec.TrialDir, "process.log")
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
		"PATH":                      "/usr/local/bin:/usr/bin:/bin",
		"DEEPCLAW_BENCH_ROOT":       runDir,
		"DEEPCLAW_CODING_PROFILE":   boolEnv(spec.ToolProfile == "coding"),
		"DEEPCLAW_BENCH_TRIAL_SPEC": filepath.Join(spec.TrialDir, "trial-spec.json"),
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

func firstLines(text string, n int) string {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.Join(lines, " | ")
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
		var prompt, completion, total, e2e, retrieval, taskEntries, taskActive, taskTokens, taskPromptTokens []float64
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
