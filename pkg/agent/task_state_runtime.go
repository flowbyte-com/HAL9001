package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"strings"

	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/providers"
)

// TaskStateProjectID resolves the project identity used to scope task state.
// An explicit DeepClaw project wins. Without one, a workspace digest keeps
// separate workspaces from sharing sidecars under the same PicoClaw home.
func TaskStateProjectID(explicit, workspace string) string {
	explicit = strings.TrimSpace(explicit)
	if explicit != "" {
		return explicit
	}
	workspace = strings.TrimSpace(workspace)
	if workspace == "" {
		return ""
	}
	if abs, err := filepath.Abs(workspace); err == nil {
		workspace = abs
	}
	sum := sha256.Sum256([]byte("deepclaw-task-project-v1\x00" + filepath.Clean(workspace)))
	return "ws-" + hex.EncodeToString(sum[:8])
}

func (a *AgentInstance) taskStateActive() bool {
	return a != nil && a.TaskStateEnabled && a.ActiveMemory != nil
}

// captureUserTaskState records only deterministic, explicitly marked user
// instructions. Ordinary questions are never copied into the objective.
func (a *AgentInstance) captureUserTaskState(sessionKey, message, turnID string) {
	if !a.taskStateActive() {
		return
	}
	if _, err := a.ActiveMemory.UpdateTaskState(sessionKey, func(state *TaskState) {
		state.ApplyUserMessage(message, turnID)
	}); err != nil {
		logger.WarnCF("agent", "Task state user capture failed", map[string]any{
			"session_key": sessionKey,
			"error":       err.Error(),
		})
	}
}

// observeTaskSummary records the first observed compaction/restart boundary
// (or a changed summary) without storing the summary text itself.
func (a *AgentInstance) observeTaskSummary(sessionKey, summary, turnID string) {
	if !a.taskStateActive() || strings.TrimSpace(summary) == "" {
		return
	}
	if _, err := a.ActiveMemory.UpdateTaskState(sessionKey, func(state *TaskState) {
		state.ObserveSummary(summary, turnID)
	}); err != nil {
		logger.WarnCF("agent", "Task state summary observation failed", map[string]any{
			"session_key": sessionKey,
			"error":       err.Error(),
		})
	}
}

// recordTaskCompaction records an explicit runtime compaction event. The
// reason is a short runtime label, never conversation content.
func (a *AgentInstance) recordTaskCompaction(sessionKey, reason, turnID string) {
	if !a.taskStateActive() {
		return
	}
	if _, err := a.ActiveMemory.UpdateTaskState(sessionKey, func(state *TaskState) {
		state.RecordCompaction(reason, turnID)
	}); err != nil {
		logger.WarnCF("agent", "Task state compaction capture failed", map[string]any{
			"session_key": sessionKey,
			"error":       err.Error(),
		})
	}
}

// recordTaskTool records what a tool reported. The entry is always
// tool_reported evidence, never a verified interpretation of the result.
func (a *AgentInstance) recordTaskTool(
	sessionKey, turnID, toolName, toolCallID string,
	args map[string]any,
	content string,
	isError bool,
) {
	if !a.taskStateActive() {
		return
	}
	stateDir := a.ActiveMemory.Dir()
	if _, err := a.ActiveMemory.UpdateTaskState(sessionKey, func(state *TaskState) {
		state.RecordTool(toolName, args, content, isError, toolCallID, turnID, stateDir)
	}); err != nil {
		logger.WarnCF("agent", "Task state tool capture failed", map[string]any{
			"session_key": sessionKey,
			"tool":        toolName,
			"error":       err.Error(),
		})
	}
}

// captureAssistantTaskState records only explicitly labelled model proposals.
// They remain proposed and can never be promoted to verified facts.
func (a *AgentInstance) captureAssistantTaskState(sessionKey, content, turnID string) {
	if !a.taskStateActive() || strings.TrimSpace(content) == "" {
		return
	}
	if _, err := a.ActiveMemory.UpdateTaskState(sessionKey, func(state *TaskState) {
		state.RecordAssistant(content, turnID)
	}); err != nil {
		logger.WarnCF("agent", "Task state assistant capture failed", map[string]any{
			"session_key": sessionKey,
			"error":       err.Error(),
		})
	}
}

// taskStatePromptPart renders the state as its own lower-trust prompt part.
// Entries still visible in history or authored this turn are omitted.
func (a *AgentInstance) taskStatePromptPart(
	sessionKey string,
	history []providers.Message,
	summary, currentMessage string,
) (PromptPart, bool) {
	if !a.taskStateActive() || strings.TrimSpace(sessionKey) == "" {
		return PromptPart{}, false
	}
	state, err := a.ActiveMemory.TaskState(sessionKey)
	if err != nil || state == nil {
		return PromptPart{}, false
	}
	visible := taskHistoryText(history, summary)
	if strings.TrimSpace(currentMessage) != "" {
		visible += "\n" + currentMessage
	}
	text := strings.TrimSpace(state.RenderWithMode(visible, a.TaskStateBudget, a.TaskStateResidency))
	if text == "" {
		return PromptPart{}, false
	}
	return PromptPart{
		ID:      "context.task.state",
		Layer:   PromptLayerContext,
		Slot:    PromptSlotMemory,
		Source:  PromptSource{ID: PromptSourceTaskState, Name: "task.state"},
		Title:   "task state",
		Content: text,
		Stable:  false,
		Cache:   PromptCacheNone,
	}, true
}

func taskHistoryText(history []providers.Message, summary string) string {
	var b strings.Builder
	b.WriteString(summary)
	for _, message := range history {
		b.WriteString("\n")
		b.WriteString(message.Content)
	}
	return b.String()
}
