package bench

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// 005A fixtures are embedded so a trial subprocess never needs a path to the
// repository testdata. The agent under test sees only its workspace; hidden
// tests are materialised into a separate scoring copy after the agent stops.
//
//go:embed testdata/005a/tasks
var codingFixtureFS embed.FS

const codingFixtureRoot = "testdata/005a/tasks"

var (
	codingTaskIDPattern = regexp.MustCompile(`^[a-z0-9_]+$`)
	topLevelTestPattern = regexp.MustCompile(`(?m)^func\s+(Test[A-Za-z0-9_]+)\s*\(`)
)

// CodingTask is one self-contained Go repository assignment.
type CodingTask struct {
	ID                    string
	Title                 string
	DesiredBehavior       string
	Interruption          string // compaction|restart|""
	NoCompaction          bool
	ContextWindow         int
	MaxToolIterations     int
	MaxTokens             int
	SummarizeTokenPercent int
	Turns                 []TurnSpec
	Criteria              []CodingCriterion
	Constraints           CodingConstraints
	EvidenceReuseTests    []string

	Files       map[string][]byte
	Modes       map[string]os.FileMode
	HiddenFiles map[string][]byte
	PublicTests []string
	HiddenTests []string
	FixtureSHA  string
}

// CodingFixtureState records the reproducible starting point of a fixture.
type CodingFixtureState struct {
	TaskID     string `json:"task_id"`
	Commit     string `json:"starting_commit"`
	ContentSHA string `json:"fixture_sha256"`
	Path       string `json:"path"`
}

type codingTaskManifest struct {
	ID                    string            `json:"id"`
	Title                 string            `json:"title"`
	DesiredBehavior       string            `json:"desired_behavior"`
	Interruption          string            `json:"interruption"`
	NoCompaction          bool              `json:"no_compaction"`
	ContextWindow         int               `json:"context_window"`
	MaxToolIterations     int               `json:"max_tool_iterations"`
	MaxTokens             int               `json:"max_tokens"`
	SummarizeTokenPercent int               `json:"summarize_token_percent"`
	Turns                 []TurnSpec        `json:"turns"`
	Criteria              []CodingCriterion `json:"criteria"`
	Constraints           CodingConstraints `json:"constraints"`
	EvidenceReuseTests    []string          `json:"evidence_reuse_tests"`
}

// CodingTaskIDs lists every embedded 005A assignment, sorted for deterministic
// run order.
func CodingTaskIDs() ([]string, error) {
	entries, err := fs.ReadDir(codingFixtureFS, codingFixtureRoot)
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, entry := range entries {
		if entry.IsDir() {
			ids = append(ids, entry.Name())
		}
	}
	sort.Strings(ids)
	if len(ids) == 0 {
		return nil, fmt.Errorf("no coding fixtures embedded")
	}
	return ids, nil
}

// LoadCodingTask reads and validates one embedded assignment.
func LoadCodingTask(id string) (CodingTask, error) {
	if !codingTaskIDPattern.MatchString(id) {
		return CodingTask{}, fmt.Errorf("invalid coding task id %q", id)
	}
	base := codingFixtureRoot + "/" + id
	data, err := fs.ReadFile(codingFixtureFS, base+"/task.json")
	if err != nil {
		return CodingTask{}, fmt.Errorf("read task manifest %s: %w", id, err)
	}
	var manifest codingTaskManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return CodingTask{}, fmt.Errorf("parse task manifest %s: %w", id, err)
	}
	if manifest.ID != id {
		return CodingTask{}, fmt.Errorf("task %s manifest id is %q", id, manifest.ID)
	}
	task := CodingTask{
		ID:                    manifest.ID,
		Title:                 manifest.Title,
		DesiredBehavior:       manifest.DesiredBehavior,
		Interruption:          strings.TrimSpace(manifest.Interruption),
		NoCompaction:          manifest.NoCompaction,
		ContextWindow:         manifest.ContextWindow,
		MaxToolIterations:     manifest.MaxToolIterations,
		MaxTokens:             manifest.MaxTokens,
		SummarizeTokenPercent: manifest.SummarizeTokenPercent,
		Turns:                 append([]TurnSpec(nil), manifest.Turns...),
		Criteria:              append([]CodingCriterion(nil), manifest.Criteria...),
		Constraints:           manifest.Constraints,
		EvidenceReuseTests:    append([]string(nil), manifest.EvidenceReuseTests...),
	}
	if task.ContextWindow <= 0 {
		task.ContextWindow = 32768
	}
	if task.MaxToolIterations <= 0 {
		task.MaxToolIterations = 16
	}
	if task.MaxTokens <= 0 {
		task.MaxTokens = 1024
	}
	if task.SummarizeTokenPercent <= 0 {
		task.SummarizeTokenPercent = 75
	}
	if len(task.Turns) == 0 {
		return CodingTask{}, fmt.Errorf("task %s has no turns", id)
	}
	if task.Interruption != "" && task.Interruption != "compaction" && task.Interruption != "restart" {
		return CodingTask{}, fmt.Errorf("task %s has unknown interruption %q", id, task.Interruption)
	}

	files, modes, err := readEmbeddedTree(codingFixtureFS, base+"/repo")
	if err != nil {
		return CodingTask{}, err
	}
	hidden, _, err := readEmbeddedTree(codingFixtureFS, base+"/hidden")
	if err != nil {
		return CodingTask{}, err
	}
	task.Files = files
	task.Modes = modes
	task.HiddenFiles = hidden
	task.FixtureSHA = codingFixtureSHA(files, modes)
	task.PublicTests = testNamesIn(files, modes)
	task.HiddenTests = testNamesIn(hidden, nil)
	if err := validateCodingTask(task); err != nil {
		return CodingTask{}, fmt.Errorf("task %s: %w", id, err)
	}
	return task, nil
}

func readEmbeddedTree(fsys fs.FS, root string) (map[string][]byte, map[string]os.FileMode, error) {
	out := map[string][]byte{}
	modes := map[string]os.FileMode{}
	err := fs.WalkDir(fsys, root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		data, err := fs.ReadFile(fsys, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		// Fixture modules are stored as *.txt so the Go tool does not treat
		// their directory as a nested module and silently drop it from embed.
		switch rel {
		case "go.mod.txt":
			rel = "go.mod"
		case "go.sum.txt":
			rel = "go.sum"
		}
		mode := os.FileMode(0o644)
		if strings.HasSuffix(rel, ".sh") {
			mode = 0o755
		}
		out[rel] = data
		modes[rel] = mode
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	if len(out) == 0 {
		return nil, nil, fmt.Errorf("embedded tree %s is empty", root)
	}
	return out, modes, nil
}

func testNamesIn(files map[string][]byte, modes map[string]os.FileMode) []string {
	seen := map[string]bool{}
	var names []string
	paths := make([]string, 0, len(files))
	for path := range files {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		if !strings.HasSuffix(path, "_test.go") {
			continue
		}
		for _, match := range topLevelTestPattern.FindAllSubmatch(files[path], -1) {
			name := string(match[1])
			if !seen[name] {
				seen[name] = true
				names = append(names, name)
			}
		}
	}
	sort.Strings(names)
	return names
}

func validateCodingTask(task CodingTask) error {
	if len(task.Criteria) == 0 {
		return fmt.Errorf("no criteria")
	}
	total := 0
	hidden := map[string]bool{}
	for _, name := range task.HiddenTests {
		hidden[name] = true
	}
	covered := map[string]bool{}
	public := map[string]bool{}
	for _, name := range task.PublicTests {
		public[name] = true
	}
	for _, criterion := range task.Criteria {
		total += criterion.Points
		switch criterion.Kind {
		case "build", "public_tests":
		case "test":
			if !hidden[criterion.Test] {
				return fmt.Errorf("criterion %s references unknown hidden test %q", criterion.ID, criterion.Test)
			}
			covered[criterion.Test] = true
		default:
			return fmt.Errorf("criterion %s has unknown kind %q", criterion.ID, criterion.Kind)
		}
	}
	if total <= 0 {
		return fmt.Errorf("criteria total no points")
	}
	for _, name := range task.HiddenTests {
		if !covered[name] {
			return fmt.Errorf("hidden test %s has no criterion", name)
		}
	}
	for _, name := range task.EvidenceReuseTests {
		if !hidden[name] && !public[name] {
			return fmt.Errorf("evidence reuse test %q is unknown", name)
		}
	}
	for _, path := range task.Constraints.ProtectedFiles {
		if _, ok := task.Files[path]; !ok {
			return fmt.Errorf("protected file %q is not in the starting repository", path)
		}
	}
	if task.Interruption == "compaction" && task.NoCompaction {
		return fmt.Errorf("compaction task marked no_compaction")
	}
	return nil
}

// CodingScenarioSpec exposes a task as a bench scenario. D uses one scenario
// per assignment and both conditions run the same turns and budget.
func CodingScenarioSpec(task CodingTask) ScenarioSpec {
	return ScenarioSpec{
		Name:                  task.ID,
		Question:              task.Turns[0].Prompt,
		Turns:                 append([]TurnSpec(nil), task.Turns...),
		CodingTask:            task.ID,
		ContextWindow:         task.ContextWindow,
		MaxToolIterations:     task.MaxToolIterations,
		MaxTokens:             task.MaxTokens,
		SummarizeTokenPercent: task.SummarizeTokenPercent,
	}
}

// MaterializeCodingTask writes a pristine repository, creates the known
// starting commit and verifies the content hash. Calling it twice with the same
// task produces the same commit and SHA.
func MaterializeCodingTask(task CodingTask, dir string) (CodingFixtureState, error) {
	if err := os.RemoveAll(dir); err != nil {
		return CodingFixtureState{}, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return CodingFixtureState{}, err
	}
	for path, data := range task.Files {
		full := filepath.Join(dir, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return CodingFixtureState{}, err
		}
		mode := task.Modes[path]
		if mode == 0 {
			mode = 0o644
		}
		if err := os.WriteFile(full, data, mode); err != nil {
			return CodingFixtureState{}, err
		}
		if err := os.Chmod(full, mode); err != nil {
			return CodingFixtureState{}, err
		}
	}
	commit, err := commitCodingFixture(dir)
	if err != nil {
		return CodingFixtureState{}, err
	}
	sha, err := CodingWorkspaceSHA(dir)
	if err != nil {
		return CodingFixtureState{}, err
	}
	if sha != task.FixtureSHA {
		return CodingFixtureState{}, fmt.Errorf("fixture %s content hash %s, want %s", task.ID, sha, task.FixtureSHA)
	}
	return CodingFixtureState{TaskID: task.ID, Commit: commit, ContentSHA: sha, Path: dir}, nil
}

func commitCodingFixture(dir string) (string, error) {
	env := append(os.Environ(),
		"GIT_AUTHOR_NAME=DeepClaw Bench",
		"GIT_AUTHOR_EMAIL=bench@example.invalid",
		"GIT_COMMITTER_NAME=DeepClaw Bench",
		"GIT_COMMITTER_EMAIL=bench@example.invalid",
		"GIT_AUTHOR_DATE=2020-01-01T00:00:00Z",
		"GIT_COMMITTER_DATE=2020-01-01T00:00:00Z",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
	)
	run := func(args ...string) (string, error) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		if err != nil {
			return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
		}
		return strings.TrimSpace(string(out)), nil
	}
	if _, err := run("init", "-q", "-b", "main"); err != nil {
		if _, retryErr := run("init", "-q"); retryErr != nil {
			return "", retryErr
		}
	}
	if _, err := run("add", "-A"); err != nil {
		return "", err
	}
	if _, err := run("-c", "user.name=DeepClaw Bench", "-c", "user.email=bench@example.invalid",
		"commit", "-q", "-m", "fixture: starting point"); err != nil {
		return "", err
	}
	return run("rev-parse", "HEAD")
}

// CodingWorkspaceSHA hashes the agent-visible repository content. Runtime
// state (.git, sessions, task-state sidecars) is excluded so a reset can be
// compared with the fixture definition.
func CodingWorkspaceSHA(dir string) (string, error) {
	files := map[string][]byte{}
	modes := map[string]os.FileMode{}
	err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if entry.IsDir() {
			if rel == ".git" || rel == "sessions" || rel == ".deepclaw" || rel == "memory" {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		files[rel] = data
		info, err := entry.Info()
		if err != nil {
			return err
		}
		mode := os.FileMode(0o644)
		if info.Mode()&0o111 != 0 {
			mode = 0o755
		}
		modes[rel] = mode
		return nil
	})
	if err != nil {
		return "", err
	}
	return codingFixtureSHA(files, modes), nil
}

func codingFixtureSHA(files map[string][]byte, modes map[string]os.FileMode) string {
	paths := make([]string, 0, len(files))
	for path := range files {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	h := sha256.New()
	for _, path := range paths {
		mode := os.FileMode(0o644)
		if modes != nil && modes[path] != 0 {
			mode = modes[path]
		}
		data := files[path]
		fmt.Fprintf(h, "%s\x00%o\x00%d\x00", path, mode.Perm(), len(data))
		h.Write(data)
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// PrepareCodingTrial materialises the pristine repository for a D trial.
func PrepareCodingTrial(spec *TrialSpec) error {
	task, err := LoadCodingTask(spec.CodingTask)
	if err != nil {
		return err
	}
	state, err := MaterializeCodingTask(task, spec.AgentWorkspace)
	if err != nil {
		return err
	}
	spec.StartingCommit = state.Commit
	spec.FixtureSHA = state.ContentSHA
	return nil
}

// CodingFixtureVerification is the offline qualification report.
type CodingFixtureVerification struct {
	GeneratedAt string                          `json:"generated_at"`
	Tasks       []CodingFixtureVerificationTask `json:"tasks"`
	Valid       bool                            `json:"valid"`
}

type CodingFixtureVerificationTask struct {
	TaskID         string  `json:"task_id"`
	Title          string  `json:"title"`
	StartingCommit string  `json:"starting_commit"`
	FixtureSHA     string  `json:"fixture_sha256"`
	Deterministic  bool    `json:"deterministic_commit"`
	BuildOK        bool    `json:"build_ok"`
	PublicFailed   int     `json:"public_tests_failed"`
	HiddenFailed   int     `json:"hidden_tests_failed"`
	HiddenPassed   int     `json:"hidden_tests_passed"`
	Partial        float64 `json:"pristine_partial"`
	Valid          bool    `json:"valid"`
	Error          string  `json:"error,omitempty"`
}

// VerifyCodingFixtures materialises every task twice and scores the pristine
// repository. A valid fixture must produce a deterministic commit, build, and
// fail at least one hidden acceptance test before any work is done.
func VerifyCodingFixtures(outDir string) (*CodingFixtureVerification, error) {
	ids, err := CodingTaskIDs()
	if err != nil {
		return nil, err
	}
	if outDir == "" {
		outDir = filepath.Join(repoRoot(), ".lab", "bench", "005a", "fixtures")
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return nil, err
	}
	report := &CodingFixtureVerification{GeneratedAt: nowRFC3339(), Valid: true}
	for _, id := range ids {
		entry := CodingFixtureVerificationTask{TaskID: id}
		task, loadErr := LoadCodingTask(id)
		if loadErr != nil {
			entry.Error = loadErr.Error()
			entry.Valid = false
			report.Valid = false
			report.Tasks = append(report.Tasks, entry)
			continue
		}
		entry.Title = task.Title
		first, firstErr := MaterializeCodingTask(task, filepath.Join(outDir, id, "first"))
		second, secondErr := MaterializeCodingTask(task, filepath.Join(outDir, id, "second"))
		if firstErr != nil || secondErr != nil {
			entry.Error = strings.TrimSpace(strings.Join(nonEmpty(firstErr, secondErr), "; "))
			entry.Valid = false
			report.Valid = false
			report.Tasks = append(report.Tasks, entry)
			continue
		}
		entry.StartingCommit = first.Commit
		entry.FixtureSHA = first.ContentSHA
		entry.Deterministic = first.Commit == second.Commit && first.ContentSHA == second.ContentSHA
		outcome, scoreErr := ScoreCodingTask(task, first.Path, filepath.Join(outDir, id, "scoring"))
		if scoreErr != nil {
			entry.Error = scoreErr.Error()
			entry.Valid = false
			report.Valid = false
			report.Tasks = append(report.Tasks, entry)
			continue
		}
		entry.BuildOK = outcome.BuildOK
		entry.PublicFailed = outcome.PublicTestsFailed
		entry.HiddenFailed = outcome.HiddenTestsFailed
		entry.HiddenPassed = outcome.HiddenTestsPassed
		entry.Partial = outcome.Partial
		entry.Valid = entry.Deterministic && outcome.BuildOK && outcome.HiddenTestsFailed > 0
		if !entry.Valid {
			report.Valid = false
		}
		report.Tasks = append(report.Tasks, entry)
	}
	if err := writeJSON(filepath.Join(outDir, "verification.json"), report); err != nil {
		return report, err
	}
	return report, nil
}

func nonEmpty(errs ...error) []string {
	var out []string
	for _, err := range errs {
		if err != nil {
			out = append(out, err.Error())
		}
	}
	return out
}

// ResetCodingFixture writes a pristine fixture at outDir for a human or an
// external runner. It does not run the agent.
func ResetCodingFixture(taskID, outDir string) (CodingFixtureState, error) {
	task, err := LoadCodingTask(taskID)
	if err != nil {
		return CodingFixtureState{}, err
	}
	return MaterializeCodingTask(task, outDir)
}

// presentHiddenTests reports hidden test files that somehow exist in the
// agent workspace. The harness never writes them there; this is a leak check.
func presentHiddenTests(workspace string, task CodingTask) []string {
	var present []string
	for name := range task.HiddenFiles {
		if _, err := os.Stat(filepath.Join(workspace, filepath.FromSlash(name))); err == nil {
			present = append(present, name)
		}
	}
	sort.Strings(present)
	return present
}

func gitHead(dir string) (string, error) {
	cmd := exec.Command("git", "-C", dir, "rev-parse", "HEAD")
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
