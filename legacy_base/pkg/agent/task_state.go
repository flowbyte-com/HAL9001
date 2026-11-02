package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	// TaskStateEnabledEnv enables the evidence-backed short-term task state.
	TaskStateEnabledEnv = "DEEPCLAW_TASK_STATE"
	// TaskStateBudgetEnv sets the rendered task-state budget in estimated tokens.
	TaskStateBudgetEnv = "DEEPCLAW_TASK_STATE_BUDGET"
	// TaskStateResidencyEnv selects how much of the stored state is rendered.
	TaskStateResidencyEnv = "DEEPCLAW_TASK_STATE_RESIDENCY"

	// TaskStateResidencyAlways renders every active entry whose evidence is not
	// visible: the Milestone 004A behavior.
	TaskStateResidencyAlways = "always"
	// TaskStateResidencyBoundary renders only entries needed to preserve
	// continuity after evidence leaves the visible window. It also ignores
	// whitespace differences when checking visibility and drops routine
	// filesystem successes and compaction bookkeeping.
	TaskStateResidencyBoundary = "boundary"

	defaultTaskStateBudget = 500
	minTaskStateBudget     = 100
	maxTaskStateBudget     = 2000
	maxTaskStateEntries    = 64
	maxTaskStateContent    = 240
	maxTaskStateExcerpt    = 320
)

const (
	TaskEntryKindObjective   = "objective"
	TaskEntryKindConstraint  = "constraint"
	TaskEntryKindCorrection  = "correction"
	TaskEntryKindObservation = "observation"
	TaskEntryKindDecision    = "decision"
	TaskEntryKindDiagnosis   = "diagnosis"
	TaskEntryKindFailure     = "failure"
	TaskEntryKindBlocker     = "blocker"
	TaskEntryKindNext        = "next_action"
	TaskEntryKindCompaction  = "compaction"
)

const (
	TaskOriginUser    = "user"
	TaskOriginTool    = "tool"
	TaskOriginRuntime = "runtime"
	TaskOriginModel   = "model"
)

const (
	TaskVerificationUserConfirmed = "user_confirmed"
	TaskVerificationToolReported  = "tool_reported"
	TaskVerificationRuntime       = "runtime_verified"
	TaskVerificationProposed      = "proposed"
	TaskVerificationUnresolved    = "unresolved"
)

const (
	TaskObjectiveActive    = "active"
	TaskObjectiveCompleted = "completed"
)

// TaskStateEvidence identifies where a task-state entry came from. OutputSHA
// and OutputLen preserve a verifiable reference without copying the whole tool
// output into the prompt-bound state.
type TaskStateEvidence struct {
	TurnID     string `json:"turn_id,omitempty"`
	ToolName   string `json:"tool_name,omitempty"`
	ToolCallID string `json:"tool_call_id,omitempty"`
	Command    string `json:"command,omitempty"`
	Path       string `json:"path,omitempty"`
	ExitCode   *int   `json:"exit_code,omitempty"`
	OutputLen  int    `json:"output_len,omitempty"`
	OutputSHA  string `json:"output_sha256,omitempty"`
	Quote      string `json:"quote,omitempty"`
}

// TaskStateEntry is one bounded, attributed task-state item.
type TaskStateEntry struct {
	ID           string            `json:"id"`
	Kind         string            `json:"kind"`
	Key          string            `json:"key"`
	Content      string            `json:"content"`
	Origin       string            `json:"origin"`
	Verification string            `json:"verification"`
	ProjectID    string            `json:"project_id,omitempty"`
	SessionKey   string            `json:"session_key"`
	Evidence     TaskStateEvidence `json:"evidence,omitempty"`
	CreatedAt    time.Time         `json:"created_at"`
	UpdatedAt    time.Time         `json:"updated_at"`
	Superseded   bool              `json:"superseded,omitempty"`
	SupersededBy string            `json:"superseded_by,omitempty"`
	Supersedes   []string          `json:"supersedes,omitempty"`
}

// TaskState is the short-term, evidence-backed task view for one session. It
// is stored inside the existing active-memory sidecar, not in MPM.
type TaskState struct {
	SessionKey         string            `json:"session_key"`
	ProjectID          string            `json:"project_id,omitempty"`
	Objective          string            `json:"objective,omitempty"`
	ObjectiveStatus    string            `json:"objective_status,omitempty"`
	ObjectiveOrigin    string            `json:"objective_origin,omitempty"`
	ObjectiveVerify    string            `json:"objective_verification,omitempty"`
	ObjectiveEvidence  TaskStateEvidence `json:"objective_evidence,omitempty"`
	ObjectiveCreatedAt time.Time         `json:"objective_created_at,omitempty"`
	Entries            []TaskStateEntry  `json:"entries,omitempty"`
	NextEntry          int               `json:"next_entry,omitempty"`
	// ObservedSummaryLen deduplicates the runtime compaction marker across
	// turns while the same summary remains visible.
	ObservedSummaryLen int       `json:"observed_summary_len,omitempty"`
	UpdatedAt          time.Time `json:"updated_at,omitempty"`
}

// TaskStateEnabled reports whether the feature is requested.
func TaskStateEnabled() bool {
	return strings.TrimSpace(getenv(TaskStateEnabledEnv)) == "1"
}

// TaskStateResidencyMode returns the configured prompt-residency policy.
// Boundary-aware residency is the default; "always" restores Milestone 004A.
func TaskStateResidencyMode() string {
	switch strings.ToLower(strings.TrimSpace(getenv(TaskStateResidencyEnv))) {
	case TaskStateResidencyAlways:
		return TaskStateResidencyAlways
	case TaskStateResidencyBoundary, "":
		return TaskStateResidencyBoundary
	default:
		return TaskStateResidencyBoundary
	}
}

// TaskStateBudget returns the enforced estimated-token budget for the rendered
// task-state block. Invalid values fall back to the default.
func TaskStateBudget() int {
	raw := strings.TrimSpace(getenv(TaskStateBudgetEnv))
	if raw == "" {
		return defaultTaskStateBudget
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return defaultTaskStateBudget
	}
	if value < minTaskStateBudget {
		return minTaskStateBudget
	}
	if value > maxTaskStateBudget {
		return maxTaskStateBudget
	}
	return value
}

// getenv is a seam so tests can exercise the feature gate and budget parser
// without mutating the process environment.
var getenv = func(key string) string {
	return os.Getenv(key)
}

// NewTaskState creates an empty state bound to one session and project.
func NewTaskState(projectID, sessionKey string) *TaskState {
	return &TaskState{
		SessionKey:      strings.TrimSpace(sessionKey),
		ProjectID:       strings.TrimSpace(projectID),
		ObjectiveStatus: TaskObjectiveActive,
		UpdatedAt:       time.Now().UTC(),
	}
}

// ValidFor reports whether the state belongs to the requested session/project.
func (s *TaskState) ValidFor(projectID, sessionKey string) bool {
	if s == nil {
		return false
	}
	if strings.TrimSpace(s.SessionKey) != strings.TrimSpace(sessionKey) {
		return false
	}
	if strings.TrimSpace(projectID) != "" && strings.TrimSpace(s.ProjectID) != strings.TrimSpace(projectID) {
		return false
	}
	return true
}

// ApplyUserMessage extracts only deterministic, explicitly marked user state.
// It intentionally does not turn ordinary questions into objectives.
func (s *TaskState) ApplyUserMessage(message, turnID string) {
	if s == nil {
		return
	}
	message = strings.TrimSpace(message)
	if message == "" {
		return
	}
	now := time.Now().UTC()
	if s.ObjectiveStatus == "" {
		s.ObjectiveStatus = TaskObjectiveActive
	}

	lines := strings.Split(message, "\n")
	for _, raw := range lines {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		lower := strings.ToLower(line)
		switch {
		case hasMarker(lower, "objective:"):
			value := markerValue(line)
			if value != "" {
				s.setObjective(value, turnID, value, now)
			}
		case hasMarker(lower, "constraint:"), hasMarker(lower, "requirement:"), hasMarker(lower, "must:"):
			value := markerValue(line)
			if value != "" {
				s.addEntry(TaskEntryKindConstraint, "constraint", value, TaskOriginUser,
					TaskVerificationUserConfirmed, TaskStateEvidence{TurnID: turnID, Quote: value}, now)
			}
		case hasMarker(lower, "correction:"), strings.HasPrefix(lower, "actually,"):
			value := markerValue(line)
			if strings.HasPrefix(lower, "actually,") {
				value = strings.TrimSpace(line[len("actually,"):])
			}
			if value != "" {
				s.addEntry(TaskEntryKindCorrection, "constraint", value, TaskOriginUser,
					TaskVerificationUserConfirmed, TaskStateEvidence{TurnID: turnID, Quote: value}, now)
			}
		case hasMarker(lower, "decision:"):
			value := markerValue(line)
			if value != "" {
				s.addEntry(TaskEntryKindDecision, "decision", value, TaskOriginUser,
					TaskVerificationUserConfirmed, TaskStateEvidence{TurnID: turnID, Quote: value}, now)
			}
		case hasMarker(lower, "next:"), hasMarker(lower, "next step:"):
			value := markerValue(line)
			if value != "" {
				s.addEntry(TaskEntryKindNext, "next_action", value, TaskOriginUser,
					TaskVerificationUserConfirmed, TaskStateEvidence{TurnID: turnID, Quote: value}, now)
			}
		case hasMarker(lower, "blocker:"), hasMarker(lower, "failure:"):
			value := markerValue(line)
			if value != "" {
				s.addEntry(TaskEntryKindBlocker, "blocker", value, TaskOriginUser,
					TaskVerificationUserConfirmed, TaskStateEvidence{TurnID: turnID, Quote: value}, now)
			}
		case hasMarker(lower, "done:"), hasMarker(lower, "objective complete:"):
			s.CompleteObjective(turnID)
		}
	}

	if strings.TrimSpace(s.Objective) == "" {
		// A question is not an objective. Only a plain directive (no question
		// anywhere in the message) becomes an implicit objective, so ordinary
		// follow-up questions are never duplicated as task state.
		first := strings.TrimSpace(lines[0])
		if first != "" && !strings.HasSuffix(first, "?") && !strings.Contains(message, "?") &&
			!isTaskMarker(first) && looksLikeDirective(first) {
			s.setObjective(truncateTaskText(first, maxTaskStateContent), turnID, first, now)
		}
	}
	if s.ObjectiveStatus == "" {
		s.ObjectiveStatus = TaskObjectiveActive
	}
	s.UpdatedAt = now
}

// ObserveSummary records that compaction/restart has hidden earlier messages.
// It stores the boundary and length, never the summary text.
func (s *TaskState) ObserveSummary(summary, turnID string) {
	if s == nil || strings.TrimSpace(summary) == "" {
		return
	}
	if s.ObservedSummaryLen == len(summary) {
		return
	}
	s.ObservedSummaryLen = len(summary)
	content := fmt.Sprintf("Context summary present after compaction or restart (%d chars); earlier messages may be omitted.", len(summary))
	s.addEntry(TaskEntryKindCompaction, "compaction", content, TaskOriginRuntime,
		TaskVerificationRuntime, TaskStateEvidence{TurnID: turnID}, time.Now().UTC())
}

// RecordCompaction records an explicit compaction event from the context
// manager.
func (s *TaskState) RecordCompaction(reason, turnID string) {
	if s == nil {
		return
	}
	content := "Context compaction event observed"
	if strings.TrimSpace(reason) != "" {
		content += " (" + truncateTaskText(reason, 80) + ")"
	}
	content += "."
	s.addEntry(TaskEntryKindCompaction, "compaction", content, TaskOriginRuntime,
		TaskVerificationRuntime, TaskStateEvidence{TurnID: turnID}, time.Now().UTC())
}

var exitCodePattern = regexp.MustCompile(`\[Command exited with code (-?\d+)\]`)

// RecordTool records a tool-reported observation. A tool result proves only what
// the tool reported; it is not a verified interpretation of why it happened.
func (s *TaskState) RecordTool(toolName string, args map[string]any, content string, isError bool, toolCallID, turnID, stateDir string) {
	if s == nil {
		return
	}
	toolName = strings.TrimSpace(toolName)
	if !taskStateToolRelevant(toolName) {
		return
	}
	command := strings.TrimSpace(taskStringArg(args, "command"))
	path := strings.TrimSpace(taskStringArg(args, "path"))
	if stateDir != "" {
		needle := strings.TrimSpace(command + " " + path)
		if strings.Contains(needle, stateDir) {
			return
		}
	}
	key := "tool:" + toolName
	label := toolName
	if command != "" {
		key += ":cmd:" + normalizeTaskKey(command)
		label = "bash"
	} else if path != "" {
		key += ":path:" + normalizeTaskKey(path)
	}
	full := content
	excerpt := taskExcerpt(content, maxTaskStateExcerpt)
	if excerpt == "" {
		excerpt = command
	}

	var exitCode *int
	if toolName == "bash" || toolName == "exec" {
		if match := exitCodePattern.FindStringSubmatch(content); len(match) == 2 {
			if value, err := strconv.Atoi(match[1]); err == nil {
				exitCode = &value
			}
		}
		if exitCode == nil && !isError {
			zero := 0
			exitCode = &zero
		}
	}

	status := "succeeded"
	kind := TaskEntryKindObservation
	if isError || (exitCode != nil && *exitCode != 0) {
		status = "failed"
		kind = TaskEntryKindFailure
	}
	text := fmt.Sprintf("%s %s", label, status)
	if command != "" {
		text += ": " + truncateTaskText(command, 160)
	} else if path != "" {
		text += ": " + truncateTaskText(path, 160)
	}
	if exitCode != nil {
		text += fmt.Sprintf(" (exit %d)", *exitCode)
	}
	if excerpt != "" {
		text += ". Reported: " + excerpt
	}
	s.addEntry(kind, key, text, TaskOriginTool, TaskVerificationToolReported, TaskStateEvidence{
		TurnID: turnID, ToolName: toolName, ToolCallID: toolCallID,
		Command: truncateTaskText(command, 200), Path: truncateTaskText(path, 200),
		ExitCode: exitCode, OutputLen: len(full), OutputSHA: taskHash(full),
		Quote: taskExcerpt(excerpt, 160),
	}, time.Now().UTC())
}

// RecordAssistant captures only explicitly proposed model state. These entries
// are always marked proposed and never promoted to verified findings.
func (s *TaskState) RecordAssistant(message, turnID string) {
	if s == nil {
		return
	}
	now := time.Now().UTC()
	for _, raw := range strings.Split(message, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "- ") || strings.HasPrefix(line, "Task state") {
			continue
		}
		lower := strings.ToLower(line)
		switch {
		case hasMarker(lower, "proposed diagnosis:"):
			value := markerValue(line)
			if value != "" {
				s.addEntry(TaskEntryKindDiagnosis, "diagnosis", value, TaskOriginModel,
					TaskVerificationProposed, TaskStateEvidence{TurnID: turnID, Quote: value}, now)
			}
		case hasMarker(lower, "proposed decision:"):
			value := markerValue(line)
			if value != "" {
				s.addEntry(TaskEntryKindDecision, "decision", value, TaskOriginModel,
					TaskVerificationProposed, TaskStateEvidence{TurnID: turnID, Quote: value}, now)
			}
		case hasMarker(lower, "next:"), hasMarker(lower, "next step:"):
			value := markerValue(line)
			if value != "" {
				s.addEntry(TaskEntryKindNext, "next_action", value, TaskOriginModel,
					TaskVerificationProposed, TaskStateEvidence{TurnID: turnID, Quote: value}, now)
			}
		case hasMarker(lower, "blocker:"):
			value := markerValue(line)
			if value != "" {
				s.addEntry(TaskEntryKindBlocker, "blocker", value, TaskOriginModel,
					TaskVerificationProposed, TaskStateEvidence{TurnID: turnID, Quote: value}, now)
			}
		}
	}
}

// CompleteObjective marks the task complete and supersedes its task details so
// they are not carried into an unrelated task.
func (s *TaskState) CompleteObjective(turnID string) {
	if s == nil {
		return
	}
	s.ObjectiveStatus = TaskObjectiveCompleted
	now := time.Now().UTC()
	for i := range s.Entries {
		if !s.Entries[i].Superseded {
			s.Entries[i].Superseded = true
			s.Entries[i].SupersededBy = "objective-complete"
			s.Entries[i].UpdatedAt = now
		}
	}
	s.ObjectiveEvidence.TurnID = turnID
	s.UpdatedAt = now
}

func (s *TaskState) setObjective(value, turnID, quote string, now time.Time) {
	value = truncateTaskText(value, maxTaskStateContent)
	if value == "" {
		return
	}
	if strings.TrimSpace(s.Objective) != "" && !strings.EqualFold(strings.TrimSpace(s.Objective), value) {
		s.startNewTask(value, turnID, quote, now)
		return
	}
	s.Objective = value
	if s.ObjectiveStatus == "" {
		s.ObjectiveStatus = TaskObjectiveActive
	}
	s.ObjectiveOrigin = TaskOriginUser
	s.ObjectiveVerify = TaskVerificationUserConfirmed
	s.ObjectiveEvidence = TaskStateEvidence{TurnID: turnID, Quote: quote}
	if s.ObjectiveCreatedAt.IsZero() {
		s.ObjectiveCreatedAt = now
	}
	s.UpdatedAt = now
}

func (s *TaskState) startNewTask(value, turnID, quote string, now time.Time) {
	s.Objective = value
	s.ObjectiveStatus = TaskObjectiveActive
	s.ObjectiveOrigin = TaskOriginUser
	s.ObjectiveVerify = TaskVerificationUserConfirmed
	s.ObjectiveEvidence = TaskStateEvidence{TurnID: turnID, Quote: quote}
	s.ObjectiveCreatedAt = now
	s.Entries = nil
	s.UpdatedAt = now
}

func (s *TaskState) addEntry(kind, key, content, origin, verification string, evidence TaskStateEvidence, now time.Time) {
	content = truncateTaskText(content, maxTaskStateContent)
	if content == "" {
		return
	}
	key = strings.TrimSpace(key)
	superseded := make([]string, 0, 2)
	if key != "" {
		for i := range s.Entries {
			entry := &s.Entries[i]
			if entry.Superseded || entry.Key != key {
				continue
			}
			// A model proposal never overrides a user-confirmed instruction,
			// decision or next action. It is recorded alongside it.
			if origin == TaskOriginModel && entry.Verification == TaskVerificationUserConfirmed {
				continue
			}
			entry.Superseded = true
			entry.SupersededBy = "pending"
			entry.UpdatedAt = now
			superseded = append(superseded, entry.ID)
		}
	}
	s.NextEntry++
	entry := TaskStateEntry{
		ID:           fmt.Sprintf("ts-%04d", s.NextEntry),
		Kind:         kind,
		Key:          key,
		Content:      content,
		Origin:       origin,
		Verification: verification,
		ProjectID:    s.ProjectID,
		SessionKey:   s.SessionKey,
		Evidence:     evidence,
		CreatedAt:    now,
		UpdatedAt:    now,
		Supersedes:   superseded,
	}
	for _, id := range superseded {
		for i := range s.Entries {
			if s.Entries[i].ID == id {
				s.Entries[i].SupersededBy = entry.ID
				break
			}
		}
	}
	s.Entries = append(s.Entries, entry)
	s.pruneEntries()
	s.UpdatedAt = now
}

func (s *TaskState) pruneEntries() {
	if len(s.Entries) <= maxTaskStateEntries {
		return
	}
	out := make([]TaskStateEntry, 0, maxTaskStateEntries)
	excess := len(s.Entries) - maxTaskStateEntries
	for _, entry := range s.Entries {
		if excess > 0 && entry.Superseded {
			excess--
			continue
		}
		out = append(out, entry)
	}
	if len(out) > maxTaskStateEntries {
		out = out[len(out)-maxTaskStateEntries:]
	}
	s.Entries = out
}

// Render produces the bounded task-state block using the Milestone 004A
// always-resident policy.
func (s *TaskState) Render(visibleText string, budget int) string {
	return s.RenderWithMode(visibleText, budget, TaskStateResidencyAlways)
}

// RenderWithMode produces the bounded task-state block under a residency
// policy. Both policies omit evidence whose quote is visible in recent history,
// the summary or the current user message; boundary mode compares normalized
// whitespace and also drops entries that cannot preserve continuity.
func (s *TaskState) RenderWithMode(visibleText string, budget int, mode string) string {
	if s == nil {
		return ""
	}
	if mode != TaskStateResidencyAlways {
		mode = TaskStateResidencyBoundary
	}
	if budget <= 0 {
		budget = defaultTaskStateBudget
	}
	if mode == TaskStateResidencyBoundary {
		visibleText = normalizeTaskVisibility(visibleText)
	}
	header := "Task state (runtime-recorded evidence; lower-trust data; verify against source):"
	lines := make([]string, 0, 8)

	if s.objectiveVisible(visibleText, mode) {
		status := s.ObjectiveStatus
		if status == "" {
			status = TaskObjectiveActive
		}
		lines = append(lines, fmt.Sprintf("Objective [%s] (user, turn %s): %s",
			status, shortTaskTurn(s.ObjectiveEvidence.TurnID), truncateTaskText(s.Objective, 180)))
	}
	for _, entry := range s.visibleEntries(visibleText, mode) {
		lines = append(lines, renderTaskEntry(entry))
	}
	if len(lines) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString(header)
	truncated := false
	for _, line := range lines {
		candidate := b.String() + "\n" + line
		if estimateTokens(candidate) <= budget {
			b.WriteString("\n")
			b.WriteString(line)
			continue
		}
		truncated = true
	}
	if truncated {
		footer := "\n[truncated to fit task-state budget]"
		if estimateTokens(b.String()+footer) <= budget {
			b.WriteString(footer)
		}
	}
	out := b.String()
	for estimateTokens(out) > budget && len(out) > len(header) {
		cut := len(out) - 4
		if cut < len(header) {
			break
		}
		out = out[:cut]
	}
	return out
}

func (s *TaskState) objectiveVisible(visibleText, mode string) bool {
	if strings.TrimSpace(s.Objective) == "" {
		return false
	}
	if mode == TaskStateResidencyBoundary && s.ObjectiveStatus == TaskObjectiveCompleted {
		return false
	}
	quote := strings.TrimSpace(s.ObjectiveEvidence.Quote)
	if evidenceVisible(visibleText, quote, mode) {
		return false
	}
	return true
}

func (s *TaskState) visibleEntries(visibleText, mode string) []TaskStateEntry {
	entries := make([]TaskStateEntry, 0, len(s.Entries))
	for _, entry := range s.Entries {
		if entry.Superseded {
			continue
		}
		if s.ProjectID != "" && strings.TrimSpace(entry.ProjectID) != s.ProjectID {
			continue
		}
		if mode == TaskStateResidencyBoundary && !boundaryResidencyAllowed(entry) {
			continue
		}
		quote := strings.TrimSpace(entry.Evidence.Quote)
		if evidenceVisible(visibleText, quote, mode) {
			continue
		}
		entries = append(entries, entry)
	}
	sort.SliceStable(entries, func(i, j int) bool {
		pi, pj := taskEntryPriority(entries[i].Kind), taskEntryPriority(entries[j].Kind)
		if pi != pj {
			return pi < pj
		}
		return entries[i].CreatedAt.Before(entries[j].CreatedAt)
	})
	return entries
}

// normalizeTaskVisibility collapses whitespace so a stored, normalized quote
// can be matched against raw history that still contains newlines, tabs or
// repeated spaces.
func normalizeTaskVisibility(value string) string {
	return strings.Join(strings.Fields(value), " ")
}

func evidenceVisible(visibleText, quote, mode string) bool {
	quote = strings.TrimSpace(quote)
	if quote == "" {
		return false
	}
	if mode == TaskStateResidencyBoundary {
		return strings.Contains(normalizeTaskVisibility(visibleText), normalizeTaskVisibility(quote))
	}
	return strings.Contains(visibleText, quote)
}

// boundaryResidencyAllowed keeps stored evidence but decides whether it is
// worth occupying prompt space. Compaction bookkeeping is already implied by
// the summary in the system prompt. A successful read/write/edit is routine
// workflow, not a finding; failures remain visible as their own entries.
func boundaryResidencyAllowed(entry TaskStateEntry) bool {
	switch entry.Kind {
	case TaskEntryKindCompaction:
		return false
	case TaskEntryKindObservation:
		return taskCommandTool(entry.Evidence.ToolName)
	default:
		return true
	}
}

func taskCommandTool(toolName string) bool {
	switch strings.ToLower(strings.TrimSpace(toolName)) {
	case "bash", "exec":
		return true
	default:
		return false
	}
}

func renderTaskEntry(entry TaskStateEntry) string {
	origin := entry.Origin
	source := origin
	if entry.Evidence.ToolName != "" {
		source = entry.Evidence.ToolName
	}
	turn := shortTaskTurn(entry.Evidence.TurnID)
	label := entry.Verification
	switch entry.Verification {
	case TaskVerificationProposed:
		label = "proposed, unverified"
	case TaskVerificationToolReported:
		label = "tool_reported"
	}
	line := fmt.Sprintf("- %s [%s] (%s, turn %s): %s",
		taskKindLabel(entry.Kind), label, source, turn, entry.Content)
	if entry.Evidence.ExitCode != nil {
		line += fmt.Sprintf(" [exit %d]", *entry.Evidence.ExitCode)
	}
	return line
}

func taskKindLabel(kind string) string {
	switch kind {
	case TaskEntryKindConstraint:
		return "Constraint"
	case TaskEntryKindCorrection:
		return "Correction"
	case TaskEntryKindObservation:
		return "Observation"
	case TaskEntryKindDecision:
		return "Decision"
	case TaskEntryKindDiagnosis:
		return "Diagnosis"
	case TaskEntryKindFailure:
		return "Failure"
	case TaskEntryKindBlocker:
		return "Blocker"
	case TaskEntryKindNext:
		return "Next"
	case TaskEntryKindCompaction:
		return "Compaction"
	default:
		return "Task"
	}
}

func taskEntryPriority(kind string) int {
	switch kind {
	case TaskEntryKindConstraint:
		return 0
	case TaskEntryKindCorrection:
		return 1
	case TaskEntryKindFailure, TaskEntryKindBlocker:
		return 2
	case TaskEntryKindObservation:
		return 3
	case TaskEntryKindDecision:
		return 4
	case TaskEntryKindDiagnosis:
		return 5
	case TaskEntryKindNext:
		return 6
	case TaskEntryKindCompaction:
		return 7
	default:
		return 8
	}
}

func taskStateToolRelevant(toolName string) bool {
	switch strings.ToLower(strings.TrimSpace(toolName)) {
	case "bash", "exec", "read", "read_file", "write", "write_file", "edit", "edit_file",
		"append_file", "search_files", "list_dir":
		return true
	default:
		return false
	}
}

func taskStringArg(args map[string]any, key string) string {
	if args == nil {
		return ""
	}
	value, _ := args[key].(string)
	return value
}

func taskExcerpt(value string, limit int) string {
	value = strings.Join(strings.Fields(value), " ")
	if limit <= 0 {
		limit = maxTaskStateExcerpt
	}
	if len(value) <= limit {
		return value
	}
	if limit <= 3 {
		return value[:limit]
	}
	return strings.TrimSpace(value[:limit-3]) + "..."
}

func truncateTaskText(value string, limit int) string {
	value = strings.Join(strings.Fields(value), " ")
	if limit > 0 && len(value) > limit {
		if limit <= 3 {
			return value[:limit]
		}
		return strings.TrimSpace(value[:limit-3]) + "..."
	}
	return value
}

func normalizeTaskKey(value string) string {
	return truncateTaskText(strings.ToLower(strings.Join(strings.Fields(value), " ")), 200)
}

func taskHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func shortTaskTurn(turnID string) string {
	if strings.TrimSpace(turnID) == "" {
		return "unknown"
	}
	return truncateTaskText(turnID, 24)
}

func hasMarker(lowerLine, marker string) bool {
	return strings.HasPrefix(strings.TrimSpace(lowerLine), strings.ToLower(marker))
}

func markerValue(line string) string {
	if idx := strings.Index(line, ":"); idx >= 0 {
		return truncateTaskText(strings.TrimSpace(line[idx+1:]), maxTaskStateContent)
	}
	return ""
}

var directiveVerbs = map[string]bool{
	"add": true, "build": true, "change": true, "continue": true, "create": true,
	"debug": true, "delete": true, "document": true, "ensure": true, "extend": true,
	"fix": true, "implement": true, "improve": true, "investigate": true, "keep": true,
	"make": true, "migrate": true, "move": true, "remove": true, "replace": true,
	"refactor": true, "report": true, "review": true, "run": true, "simplify": true,
	"test": true, "update": true, "use": true, "validate": true, "verify": true,
	"write": true,
}

func looksLikeDirective(line string) bool {
	fields := strings.Fields(strings.ToLower(strings.TrimSpace(line)))
	if len(fields) == 0 {
		return false
	}
	return directiveVerbs[fields[0]]
}

func isTaskMarker(line string) bool {
	lower := strings.ToLower(strings.TrimSpace(line))
	for _, marker := range []string{
		"objective:", "constraint:", "requirement:", "must:", "correction:",
		"decision:", "next:", "next step:", "blocker:", "failure:",
		"done:", "objective complete:", "actually,",
	} {
		if hasMarker(lower, marker) {
			return true
		}
	}
	return false
}
