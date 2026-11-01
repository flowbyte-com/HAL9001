package bench

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// ScoreCodingTask evaluates the repository produced by a trial. It never sees
// the condition, the prompt or the agent's final message: only the workspace,
// the embedded hidden tests and the recorded trace. The same function is used
// for control and treatment and for the pristine-fixture qualification.
func ScoreCodingTask(task CodingTask, workspace, workDir string) (CodingOutcome, error) {
	outcome := CodingOutcome{
		TaskID:           task.ID,
		FixtureSHA:       task.FixtureSHA,
		NoCompactionTask: task.NoCompaction,
		ScoringDir:       workDir,
	}
	if err := os.RemoveAll(workDir); err != nil {
		return outcome, err
	}
	repoDir := filepath.Join(workDir, "repo")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		return outcome, err
	}
	copyReport, err := copyCodingWorkspace(workspace, repoDir)
	if err != nil {
		return outcome, fmt.Errorf("copy workspace: %w", err)
	}
	outcome.FinalContentSHA, err = CodingWorkspaceSHA(repoDir)
	if err != nil {
		return outcome, fmt.Errorf("hash workspace: %w", err)
	}

	violations := checkCodingConstraints(task, workspace, copyReport)
	outcome.ConstraintViolations = violations

	// Hidden tests are materialised only in this scoring copy, after the agent
	// process has stopped. They are never present in the agent workspace.
	for name, data := range task.HiddenFiles {
		full := filepath.Join(repoDir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return outcome, err
		}
		if err := os.WriteFile(full, data, 0o644); err != nil {
			return outcome, err
		}
	}

	env := codingGoEnv(workDir)
	buildOutput, buildOK, buildErr := runCodingGo(repoDir, env, 120*time.Second, "build", "./...")
	outcome.BuildOK = buildOK
	testOutput, testRun, testErr := runCodingTests(repoDir, env, 240*time.Second)

	for _, name := range task.PublicTests {
		if testRun.passed[name] {
			outcome.PublicTestsPassed++
		} else {
			outcome.PublicTestsFailed++
		}
	}
	for _, name := range task.HiddenTests {
		if testRun.passed[name] {
			outcome.HiddenTestsPassed++
		} else {
			outcome.HiddenTestsFailed++
		}
	}

	outcome.Checks, outcome.PointsEarned, outcome.PointsTotal = evaluateCodingCriteria(
		task, outcome.BuildOK, outcome.PublicTestsPassed, outcome.PublicTestsFailed, testRun,
	)
	outcome.Partial = 0
	if outcome.PointsTotal > 0 {
		outcome.Partial = float64(outcome.PointsEarned) / float64(outcome.PointsTotal)
	}
	outcome.FullSuccess = len(violations) == 0
	for _, check := range outcome.Checks {
		if check.Mandatory && !check.Passed {
			outcome.FullSuccess = false
		}
	}
	outcome.EvidenceReuse = codingEvidenceReuse(task, testRun)
	for _, violation := range violations {
		if strings.HasPrefix(violation, "wrong_fix:") {
			outcome.WrongFixApplied = true
		}
	}
	switch task.Interruption {
	case "compaction":
		outcome.RecoveredAfterCompaction = outcome.FullSuccess
	case "restart":
		outcome.RecoveredAfterRestart = outcome.FullSuccess
	}

	detail := strings.TrimSpace(strings.Join([]string{
		truncateTail(string(buildOutput), 1200),
		truncateTail(testOutput, 4000),
		errorText(buildErr),
		errorText(testErr),
	}, "\n"))
	outcome.TestOutputTail = RedactSecrets(detail)
	if outcome.TestOutputTail == "" {
		outcome.TestOutputTail = "no test output"
	}

	_ = os.WriteFile(filepath.Join(workDir, "test-output.txt"), []byte(outcome.TestOutputTail), 0o600)
	if err := writeJSON(filepath.Join(workDir, "report.json"), outcome); err != nil {
		return outcome, err
	}
	return outcome, nil
}

func errorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func truncateTail(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[len(value)-limit:]
}

type codingCopyReport struct {
	Symlinks []string
	Skipped  []string
}

func copyCodingWorkspace(src, dst string) (codingCopyReport, error) {
	report := codingCopyReport{}
	err := filepath.WalkDir(src, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if rel == "." {
			return nil
		}
		if entry.IsDir() {
			switch rel {
			case ".git", "sessions", ".deepclaw", "memory":
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			report.Symlinks = append(report.Symlinks, rel)
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Size() > 4<<20 {
			report.Skipped = append(report.Skipped, rel)
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		mode := os.FileMode(0o644)
		if info.Mode()&0o111 != 0 {
			mode = 0o755
		}
		if err := os.WriteFile(target, data, mode); err != nil {
			return err
		}
		return os.Chmod(target, mode)
	})
	sort.Strings(report.Symlinks)
	sort.Strings(report.Skipped)
	return report, err
}

func checkCodingConstraints(task CodingTask, workspace string, copyReport codingCopyReport) []string {
	var violations []string
	for _, path := range copyReport.Symlinks {
		violations = append(violations, "symlink:"+path)
	}
	for _, path := range copyReport.Skipped {
		violations = append(violations, "oversized_file:"+path)
	}
	for _, path := range task.Constraints.ProtectedFiles {
		want, ok := task.Files[path]
		if !ok {
			continue
		}
		got, err := os.ReadFile(filepath.Join(workspace, filepath.FromSlash(path)))
		if err != nil {
			violations = append(violations, "missing_protected_file:"+path)
			continue
		}
		if sha256.Sum256(got) != sha256.Sum256(want) {
			violations = append(violations, "modified_protected_file:"+path)
		}
	}
	for _, path := range task.Constraints.ForbiddenFiles {
		full := filepath.Join(workspace, filepath.FromSlash(path))
		if _, err := os.Stat(full); err == nil {
			violations = append(violations, "forbidden_file:"+path)
		}
	}
	if task.Constraints.ForbidNestedGoMod {
		_ = filepath.WalkDir(workspace, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil || entry == nil || entry.IsDir() {
				return nil
			}
			rel, relErr := filepath.Rel(workspace, path)
			if relErr != nil {
				return nil
			}
			rel = filepath.ToSlash(rel)
			if rel != "go.mod" && filepath.Base(rel) == "go.mod" {
				violations = append(violations, "nested_go_mod:"+rel)
			}
			return nil
		})
	}
	for _, pattern := range task.Constraints.WrongFixPatterns {
		re, err := regexp.Compile(pattern)
		if err != nil {
			violations = append(violations, "invalid_wrong_fix_pattern:"+pattern)
			continue
		}
		matched := false
		_ = filepath.WalkDir(workspace, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil || entry == nil || entry.IsDir() || matched {
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return nil
			}
			if re.Match(data) {
				matched = true
			}
			return nil
		})
		if matched {
			violations = append(violations, "wrong_fix:"+pattern)
		}
	}
	for name := range task.HiddenFiles {
		if _, err := os.Stat(filepath.Join(workspace, filepath.FromSlash(name))); err == nil {
			violations = append(violations, "hidden_test_present:"+name)
		}
	}
	sort.Strings(violations)
	return uniqueStrings(violations)
}

func codingGoEnv(workDir string) []string {
	dirs := map[string]string{
		"HOME":       filepath.Join(workDir, "home"),
		"TMPDIR":     filepath.Join(workDir, "tmp"),
		"GOCACHE":    filepath.Join(workDir, "gocache"),
		"GOMODCACHE": filepath.Join(workDir, "gomod"),
		"GOPATH":     filepath.Join(workDir, "gopath"),
		"GOTMPDIR":   filepath.Join(workDir, "gotmp"),
	}
	for _, dir := range dirs {
		_ = os.MkdirAll(dir, 0o755)
	}
	env := []string{
		"HOME=" + dirs["HOME"],
		"TMPDIR=" + dirs["TMPDIR"],
		"PATH=/usr/local/go/bin:/usr/local/bin:/usr/bin:/bin",
		"LANG=C.UTF-8",
		"LC_ALL=C.UTF-8",
		"TZ=UTC",
		"GO111MODULE=on",
		"GOFLAGS=-mod=readonly",
		"GOPROXY=off",
		"GOSUMDB=off",
		"GOTOOLCHAIN=local",
		"CGO_ENABLED=0",
		"GOCACHE=" + dirs["GOCACHE"],
		"GOMODCACHE=" + dirs["GOMODCACHE"],
		"GOPATH=" + dirs["GOPATH"],
		"GOTMPDIR=" + dirs["GOTMPDIR"],
	}
	sort.Strings(env)
	return env
}

func runCodingGo(dir string, env []string, timeout time.Duration, args ...string) ([]byte, bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", args...)
	cmd.Dir = dir
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		return out, false, fmt.Errorf("go %s timed out", strings.Join(args, " "))
	}
	return out, err == nil, err
}

type goTestEvent struct {
	Action  string `json:"Action"`
	Package string `json:"Package"`
	Test    string `json:"Test"`
	Output  string `json:"Output"`
}

type goTestRun struct {
	passed  map[string]bool
	failed  map[string]bool
	skipped map[string]bool
}

func runCodingTests(dir string, env []string, timeout time.Duration) (string, goTestRun, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "test", "-json", "-count=1", "./...")
	cmd.Dir = dir
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	run := goTestRun{passed: map[string]bool{}, failed: map[string]bool{}, skipped: map[string]bool{}}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var event goTestEvent
		if json.Unmarshal([]byte(line), &event) != nil {
			continue
		}
		if event.Test == "" || strings.Contains(event.Test, "/") {
			continue
		}
		switch event.Action {
		case "pass":
			run.passed[event.Test] = true
		case "fail":
			run.failed[event.Test] = true
		case "skip":
			run.skipped[event.Test] = true
		}
	}
	if ctx.Err() == context.DeadlineExceeded {
		return string(out), run, fmt.Errorf("go test timed out")
	}
	return string(out), run, err
}

func evaluateCodingCriteria(
	task CodingTask,
	buildOK bool,
	publicPassed, publicFailed int,
	tests goTestRun,
) ([]CodingCheck, int, int) {
	checks := make([]CodingCheck, 0, len(task.Criteria))
	earned := 0
	total := 0
	for _, criterion := range task.Criteria {
		check := CodingCheck{
			Name:      criterion.ID,
			Kind:      criterion.Kind,
			Mandatory: criterion.Mandatory,
			Points:    criterion.Points,
		}
		switch criterion.Kind {
		case "build":
			check.Passed = buildOK
			check.Detail = "go build ./..."
		case "public_tests":
			check.Passed = publicFailed == 0 && publicPassed > 0
			check.Detail = fmt.Sprintf("public tests passed=%d failed=%d", publicPassed, publicFailed)
		case "test":
			check.Passed = tests.passed[criterion.Test]
			switch {
			case check.Passed:
				check.Detail = "hidden test passed"
			case tests.failed[criterion.Test]:
				check.Detail = "hidden test failed"
			case tests.skipped[criterion.Test]:
				check.Detail = "hidden test was skipped"
			default:
				check.Detail = "hidden test was not reported (build or compile failure)"
			}
		default:
			check.Detail = "unknown criterion kind"
		}
		if check.Passed {
			check.Earned = criterion.Points
		}
		earned += check.Earned
		total += criterion.Points
		checks = append(checks, check)
	}
	return checks, earned, total
}

func codingEvidenceReuse(task CodingTask, tests goTestRun) bool {
	if len(task.EvidenceReuseTests) == 0 {
		return false
	}
	for _, name := range task.EvidenceReuseTests {
		if !tests.passed[name] {
			return false
		}
	}
	return true
}

// codingRequestToolCalls returns the attempted tool calls in execution order.
func codingRequestToolCalls(result TrialResult) []ToolCallRecord {
	var calls []ToolCallRecord
	for _, request := range result.Requests {
		calls = append(calls, request.ToolCalls...)
	}
	return calls
}

func normalizeCodingTool(name string) string {
	switch name {
	case "read_file":
		return "read"
	case "write_file":
		return "write"
	case "edit_file":
		return "edit"
	case "exec":
		return "bash"
	default:
		return name
	}
}

// ComputeCodingMetrics derives local effort observations from the recorded
// provider and runtime events. Provider-reported usage stays in Totals; these
// values are never mixed with it.
func ComputeCodingMetrics(result TrialResult, task CodingTask) CodingMetrics {
	metrics := CodingMetrics{}
	readCounts := map[string]int{}
	bashCounts := map[string]int{}
	postCompactionTurns := map[string]bool{}
	postRestartTurns := map[string]bool{}
	compactionIndex := -1
	restartIndex := -1
	for i, turn := range task.Turns {
		if turn.CompactBefore && compactionIndex < 0 {
			compactionIndex = i
		}
		if turn.RestartBefore && restartIndex < 0 {
			restartIndex = i
		}
	}
	markPost := func(from int, target map[string]bool) {
		if from < 0 {
			return
		}
		for _, turn := range task.Turns[from:] {
			target[turn.ID] = true
		}
	}
	markPost(compactionIndex, postCompactionTurns)
	markPost(restartIndex, postRestartTurns)

	readRepeats := func(calls []ToolCallRecord, turnFilter func(string) bool) int {
		counts := map[string]int{}
		for _, call := range calls {
			if normalizeCodingTool(call.Tool) != "read" {
				continue
			}
			if turnFilter != nil && !turnFilter(call.TurnID) {
				continue
			}
			path, _ := call.Arguments["path"].(string)
			if strings.TrimSpace(path) == "" {
				path = "?"
			}
			counts[path]++
		}
		repeats := 0
		for _, count := range counts {
			if count > 1 {
				repeats += count - 1
			}
		}
		return repeats
	}

	calls := codingRequestToolCalls(result)
	for _, call := range calls {
		metrics.TotalToolCalls++
		switch normalizeCodingTool(call.Tool) {
		case "read":
			metrics.Reads++
			path, _ := call.Arguments["path"].(string)
			if strings.TrimSpace(path) == "" {
				path = "?"
			}
			readCounts[path]++
		case "write":
			metrics.Writes++
		case "edit":
			metrics.Edits++
		case "bash":
			metrics.BashCalls++
			command, _ := call.Arguments["command"].(string)
			if strings.TrimSpace(command) == "" {
				command = "?"
			}
			bashCounts[command]++
			if postCompactionTurns[call.TurnID] {
				metrics.PostCompactionToolCalls++
			}
			if postRestartTurns[call.TurnID] {
				metrics.PostRestartToolCalls++
			}
		}
	}
	for path, count := range readCounts {
		if count > 1 {
			metrics.RepeatedReads += count - 1
			metrics.RepeatedReadPaths = append(metrics.RepeatedReadPaths, path)
		}
	}
	for command, count := range bashCounts {
		if count > 1 {
			metrics.RepeatedBashCommands += count - 1
			metrics.RepeatedBashCommandList = append(metrics.RepeatedBashCommandList, command)
		}
	}
	sort.Strings(metrics.RepeatedReadPaths)
	sort.Strings(metrics.RepeatedBashCommandList)

	for _, call := range result.ToolCalls {
		if call.IsError {
			metrics.FailedToolCalls++
			if normalizeCodingTool(call.Tool) == "bash" {
				metrics.FailedBashCalls++
			}
		}
	}
	if metrics.FailedBashCalls > 0 {
		metrics.RepeatedFailedInvestigations = metrics.RepeatedBashCommands
	}
	metrics.PostCompactionRepeatedReads = readRepeats(calls, func(turn string) bool { return postCompactionTurns[turn] })
	metrics.PostRestartRepeatedReads = readRepeats(calls, func(turn string) bool { return postRestartTurns[turn] })

	diagnosis := regexp.MustCompile(`(?i)\b(diagnosis|root cause|the cause is|caused by)\b`)
	for _, turn := range result.Turns {
		if diagnosis.MatchString(turn.Response) {
			metrics.DiagnosisStatements++
		}
	}
	for _, request := range result.Requests {
		if postCompactionTurns[request.TurnID] {
			metrics.PostCompactionRequests++
		}
		if postRestartTurns[request.TurnID] {
			metrics.PostRestartRequests++
		}
	}
	return metrics
}

func countLogMessage(path, message string) int {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	count := 0
	for _, line := range strings.Split(string(data), "\n") {
		var record struct {
			Message string `json:"message"`
		}
		if json.Unmarshal([]byte(line), &record) == nil && record.Message == message {
			count++
		}
	}
	return count
}

// summarizeCodingPairs builds the pre-registered paired comparison from the
// recorded trials. Only complete control/treatment pairs are summarized; raw
// trials (including errored ones) remain in summary.Trials.
func summarizeCodingPairs(trials []TrialResult) ([]CodingPair, *CodingSummary) {
	type members struct {
		control   int
		treatment int
	}
	byPair := map[string]*members{}
	order := []string{}
	for i := range trials {
		trial := &trials[i]
		if trial.PairID == "" {
			continue
		}
		entry := byPair[trial.PairID]
		if entry == nil {
			entry = &members{control: -1, treatment: -1}
			byPair[trial.PairID] = entry
			order = append(order, trial.PairID)
		}
		switch trial.Condition {
		case CodingConditionControl:
			entry.control = i
		case CodingConditionTreatment:
			entry.treatment = i
		}
	}
	sort.Strings(order)

	pairs := make([]CodingPair, 0, len(order))
	summary := &CodingSummary{}
	for _, pairID := range order {
		entry := byPair[pairID]
		if entry == nil || entry.control < 0 || entry.treatment < 0 {
			continue
		}
		control := &trials[entry.control]
		treatment := &trials[entry.treatment]
		pair := CodingPair{
			PairID:             pairID,
			Task:               control.Scenario,
			Repetition:         control.Repetition,
			ControlTrial:       control.TrialID,
			TreatmentTrial:     treatment.TrialID,
			ControlRequests:    control.Totals.LLMRequests,
			TreatmentRequests:  treatment.Totals.LLMRequests,
			ControlToolCalls:   control.Totals.ToolCalls,
			TreatmentToolCalls: treatment.Totals.ToolCalls,
		}
		if control.Coding != nil {
			pair.ControlFullSuccess = control.Coding.FullSuccess
			pair.ControlPartial = control.Coding.Partial
			pair.ControlPoints = control.Coding.PointsEarned
			pair.ControlRepeatedReads = control.Coding.Metrics.RepeatedReads
			pair.ControlRepeatedFailed = control.Coding.Metrics.RepeatedFailedInvestigations
			pair.ControlTaskStateTokens = control.Totals.TaskStatePromptTokens
		}
		if treatment.Coding != nil {
			pair.TreatmentFullSuccess = treatment.Coding.FullSuccess
			pair.TreatmentPartial = treatment.Coding.Partial
			pair.TreatmentPoints = treatment.Coding.PointsEarned
			pair.TreatmentRepeatedReads = treatment.Coding.Metrics.RepeatedReads
			pair.TreatmentRepeatedFailed = treatment.Coding.Metrics.RepeatedFailedInvestigations
			pair.TreatmentTaskStateTokens = treatment.Totals.TaskStatePromptTokens
		}
		pair.ControlDurationMS = control.DurationMS
		pair.TreatmentDurationMS = treatment.DurationMS
		pairs = append(pairs, pair)

		summary.Pairs++
		if pair.ControlFullSuccess {
			summary.ControlFullSuccess++
		}
		if pair.TreatmentFullSuccess {
			summary.TreatmentFullSuccess++
		}
		switch {
		case pair.TreatmentFullSuccess && !pair.ControlFullSuccess:
			summary.TreatmentWins++
		case pair.ControlFullSuccess && !pair.TreatmentFullSuccess:
			summary.ControlWins++
		default:
			summary.Ties++
		}
		summary.MeanPartialDelta += pair.TreatmentPartial - pair.ControlPartial
		summary.MeanPointsDelta += float64(pair.TreatmentPoints - pair.ControlPoints)
		summary.MeanDurationDeltaMS += float64(pair.TreatmentDurationMS - pair.ControlDurationMS)
		summary.MeanRequestDelta += float64(pair.TreatmentRequests - pair.ControlRequests)
		summary.MeanToolCallDelta += float64(pair.TreatmentToolCalls - pair.ControlToolCalls)
		summary.MeanRepeatedReadDelta += float64(pair.TreatmentRepeatedReads - pair.ControlRepeatedReads)
	}
	if summary.Pairs > 0 {
		divisor := float64(summary.Pairs)
		summary.MeanPartialDelta /= divisor
		summary.MeanPointsDelta /= divisor
		summary.MeanDurationDeltaMS /= divisor
		summary.MeanRequestDelta /= divisor
		summary.MeanToolCallDelta /= divisor
		summary.MeanRepeatedReadDelta /= divisor
	}
	return pairs, summary
}
