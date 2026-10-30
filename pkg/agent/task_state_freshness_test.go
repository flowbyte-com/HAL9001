package agent

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/session"
)

// scriptedCaptureProvider replays scripted responses and keeps a copy of every
// message slice it is asked to send. Copies are shallow (Content is an
// immutable string), which is enough to detect stale or duplicated prompt
// parts across iterations.
type scriptedCaptureProvider struct {
	mu        sync.Mutex
	calls     int
	responses []providers.LLMResponse
	errAt     map[int]error
	snapshots [][]providers.Message
}

func (p *scriptedCaptureProvider) Chat(
	_ context.Context,
	messages []providers.Message,
	_ []providers.ToolDefinition,
	_ string,
	_ map[string]any,
) (*providers.LLMResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	index := p.calls
	p.calls++
	p.snapshots = append(p.snapshots, append([]providers.Message(nil), messages...))
	if err := p.errAt[index]; err != nil {
		return nil, err
	}
	if index >= len(p.responses) {
		return &providers.LLMResponse{Content: "done", FinishReason: "stop"}, nil
	}
	response := p.responses[index]
	return &response, nil
}

func (p *scriptedCaptureProvider) GetDefaultModel() string { return "scripted-capture-model" }

func (p *scriptedCaptureProvider) snapshot(index int) []providers.Message {
	p.mu.Lock()
	defer p.mu.Unlock()
	if index < 0 || index >= len(p.snapshots) {
		return nil
	}
	return append([]providers.Message(nil), p.snapshots[index]...)
}

func newFreshnessLoop(
	t *testing.T,
	provider providers.LLMProvider,
	workspace, home, project string,
) (*AgentLoop, *AgentInstance) {
	t.Helper()
	t.Setenv(TaskStateEnabledEnv, "1")
	t.Setenv(TaskStateBudgetEnv, "500")
	t.Setenv(TaskStateResidencyEnv, TaskStateResidencyBoundary)
	t.Setenv("PICOCLAW_HOME", home)
	t.Setenv("DEEPCLAW_MPM_PROJECT", project)
	t.Setenv("DEEPCLAW_MPM_ENABLED", "")
	t.Setenv("DEEPCLAW_ACTIVE_MEMORY", "")

	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = workspace
	cfg.Agents.Defaults.ModelName = "test-model"
	cfg.Agents.Defaults.MaxTokens = 4096
	cfg.Agents.Defaults.MaxToolIterations = 8
	loop := NewAgentLoop(cfg, bus.NewMessageBus(), provider)
	t.Cleanup(loop.Close)
	instance := loop.GetRegistry().GetDefaultAgent()
	if instance == nil || instance.ActiveMemory == nil || !instance.TaskStateEnabled {
		t.Fatal("task state was not enabled")
	}
	return loop, instance
}

func capturedPrompt(messages []providers.Message) string {
	var b strings.Builder
	for _, message := range messages {
		b.WriteString(message.Content)
		b.WriteString("\n")
	}
	return b.String()
}

func taskStatePartCount(messages []providers.Message) int {
	count := 0
	for _, message := range messages {
		count += strings.Count(message.Content, "source: task.state")
	}
	return count
}

func seedTaskStateFailure(
	t *testing.T,
	instance *AgentInstance,
	sessionKey, command, output string,
) {
	t.Helper()
	if instance == nil || instance.ActiveMemory == nil {
		t.Fatal("task state store is not enabled")
	}
	_, err := instance.ActiveMemory.UpdateTaskState(sessionKey, func(state *TaskState) {
		state.ApplyUserMessage("Objective: Fix the parser port regression.", "main-turn-0")
		state.RecordTool("exec", map[string]any{"command": command}, output, true,
			"call-seed", "main-turn-0", "")
	})
	if err != nil {
		t.Fatalf("seed task state: %v", err)
	}
}

func execToolCall(id, command string) providers.ToolCall {
	return providers.ToolCall{
		ID:        id,
		Type:      "function",
		Name:      "exec",
		Arguments: map[string]any{"action": "run", "command": command},
	}
}

func hasSupersededFailure(state *TaskState) bool {
	if state == nil {
		return false
	}
	for _, entry := range state.Entries {
		if entry.Kind == TaskEntryKindFailure && entry.Superseded {
			return true
		}
	}
	return false
}

// TestTaskStatePromptRefreshesAfterToolResult is the 004C stale-state
// regression: the first request sees the seeded failure as active; the tool
// succeeds; the next request must see no unresolved failure even though the
// user turn is still in progress.
func TestTaskStatePromptRefreshesAfterToolResult(t *testing.T) {
	workspace := t.TempDir()
	home := t.TempDir()
	sessionKey := session.BuildOpaqueSessionKey("deepclaw-task-freshness:single")
	provider := &scriptedCaptureProvider{responses: []providers.LLMResponse{
		{ToolCalls: []providers.ToolCall{execToolCall("call-rerun", "true")}, FinishReason: "tool_calls"},
		{Content: "The parser test now passes.", FinishReason: "stop"},
	}}
	loop, instance := newFreshnessLoop(t, provider, workspace, home, "atlas")
	seedTaskStateFailure(t, instance, sessionKey,
		"true", "--- FAIL: TestParsePort\nparse.go:12: undefined: parsePort\nFAIL")

	taskStateTurn(t, loop, sessionKey, "Run the parser test again.")

	first := capturedPrompt(provider.snapshot(0))
	if !strings.Contains(first, "Failure") || !strings.Contains(first, "undefined: parsePort") {
		t.Fatalf("first request did not carry the seeded failure:\n%s", first)
	}
	second := capturedPrompt(provider.snapshot(1))
	if strings.Contains(second, "Failure") || strings.Contains(second, "undefined: parsePort") {
		t.Fatalf("second request still presented the resolved failure as active:\n%s", second)
	}
	if got := taskStatePartCount(provider.snapshot(1)); got != 1 {
		t.Fatalf("second request has %d task-state parts, want 1:\n%s", got, second)
	}

	state, err := instance.ActiveMemory.TaskState(sessionKey)
	if err != nil || state == nil {
		t.Fatalf("load task state: %v", err)
	}
	var failure, success *TaskStateEntry
	for i := range state.Entries {
		entry := &state.Entries[i]
		if strings.Contains(entry.Content, "undefined: parsePort") {
			failure = entry
		}
		if entry.Kind == TaskEntryKindObservation && strings.Contains(entry.Content, "succeeded") {
			success = entry
		}
	}
	if failure == nil || !failure.Superseded {
		t.Fatalf("resolved failure was not retained as superseded history: %+v", failure)
	}
	if success == nil || success.Superseded {
		t.Fatalf("successful rerun was not stored as the active observation: %+v", success)
	}
}

func TestTaskStatePromptRefreshesMultipleToolResults(t *testing.T) {
	workspace := t.TempDir()
	home := t.TempDir()
	sessionKey := session.BuildOpaqueSessionKey("deepclaw-task-freshness:multiple")
	provider := &scriptedCaptureProvider{responses: []providers.LLMResponse{
		{ToolCalls: []providers.ToolCall{
			execToolCall("call-a", "true"),
			execToolCall("call-b", "true"),
		}, FinishReason: "tool_calls"},
		{Content: "Both commands succeeded.", FinishReason: "stop"},
	}}
	loop, instance := newFreshnessLoop(t, provider, workspace, home, "atlas")
	seedTaskStateFailure(t, instance, sessionKey, "true", "--- FAIL: TestParsePort undefined: parsePort")

	taskStateTurn(t, loop, sessionKey, "Run the checks again.")

	second := capturedPrompt(provider.snapshot(1))
	if strings.Contains(second, "Failure") || strings.Contains(second, "undefined: parsePort") {
		t.Fatalf("second request after two tool results still carried the failure:\n%s", second)
	}
	state, err := instance.ActiveMemory.TaskState(sessionKey)
	if err != nil || state == nil {
		t.Fatalf("load task state: %v", err)
	}
	if !hasSupersededFailure(state) {
		t.Fatalf("failure history was not retained as superseded: %+v", state.Entries)
	}
}

func TestTaskStateUnrelatedSuccessDoesNotClearFailure(t *testing.T) {
	workspace := t.TempDir()
	home := t.TempDir()
	sessionKey := session.BuildOpaqueSessionKey("deepclaw-task-freshness:unrelated")
	provider := &scriptedCaptureProvider{responses: []providers.LLMResponse{
		{ToolCalls: []providers.ToolCall{execToolCall("call-unrelated", "echo unrelated")}, FinishReason: "tool_calls"},
		{Content: "The unrelated command succeeded.", FinishReason: "stop"},
	}}
	loop, instance := newFreshnessLoop(t, provider, workspace, home, "atlas")
	seedTaskStateFailure(t, instance, sessionKey,
		"true", "--- FAIL: TestParsePort\nparse.go:12: undefined: parsePort")

	taskStateTurn(t, loop, sessionKey, "Run a different command.")

	second := capturedPrompt(provider.snapshot(1))
	if !strings.Contains(second, "undefined: parsePort") {
		t.Fatalf("unrelated success cleared the original failure:\n%s", second)
	}
	state, err := instance.ActiveMemory.TaskState(sessionKey)
	if err != nil || state == nil {
		t.Fatalf("load task state: %v", err)
	}
	activeFailures := 0
	for _, entry := range state.Entries {
		if entry.Kind == TaskEntryKindFailure && !entry.Superseded {
			activeFailures++
		}
	}
	if activeFailures != 1 {
		t.Fatalf("active failures = %d, want 1: %+v", activeFailures, state.Entries)
	}
}

func TestTaskStateTwoFailuresOnlyMatchingSuccessSupersedes(t *testing.T) {
	workspace := t.TempDir()
	home := t.TempDir()
	sessionKey := session.BuildOpaqueSessionKey("deepclaw-task-freshness:two-failures")
	provider := &scriptedCaptureProvider{responses: []providers.LLMResponse{
		{ToolCalls: []providers.ToolCall{execToolCall("call-parser", "true")}, FinishReason: "tool_calls"},
		{Content: "The parser test passes.", FinishReason: "stop"},
	}}
	loop, instance := newFreshnessLoop(t, provider, workspace, home, "atlas")
	seedTaskStateFailure(t, instance, sessionKey,
		"true", "--- FAIL: TestParsePort\nparse.go:12: undefined: parsePort")
	seedTaskStateFailure(t, instance, sessionKey,
		"echo lint", "--- FAIL: TestLint\nlint.go:4: unused import")

	taskStateTurn(t, loop, sessionKey, "Rerun only the parser test.")

	second := capturedPrompt(provider.snapshot(1))
	if strings.Contains(second, "undefined: parsePort") {
		t.Fatalf("resolved parser failure remained resident:\n%s", second)
	}
	if !strings.Contains(second, "unused import") {
		t.Fatalf("unrelated lint failure was lost:\n%s", second)
	}
	state, err := instance.ActiveMemory.TaskState(sessionKey)
	if err != nil || state == nil {
		t.Fatalf("load task state: %v", err)
	}
	failures := 0
	for _, entry := range state.Entries {
		if entry.Kind == TaskEntryKindFailure && !entry.Superseded {
			failures++
		}
	}
	if failures != 1 {
		t.Fatalf("active failures = %d, want 1: %+v", failures, state.Entries)
	}
}

func TestTaskStatePromptRefreshSurvivesContextRetry(t *testing.T) {
	workspace := t.TempDir()
	home := t.TempDir()
	sessionKey := session.BuildOpaqueSessionKey("deepclaw-task-freshness:retry")
	provider := &scriptedCaptureProvider{
		responses: []providers.LLMResponse{
			{ToolCalls: []providers.ToolCall{execToolCall("call-rerun", "true")}, FinishReason: "tool_calls"},
			{},
			{Content: "Recovered after compaction.", FinishReason: "stop"},
		},
		errAt: map[int]error{1: errors.New("context_length_exceeded")},
	}
	loop, instance := newFreshnessLoop(t, provider, workspace, home, "atlas")
	loop.GetConfig().Agents.Defaults.MaxLLMRetries = 2
	instance.ContextWindow = 20000
	instance.MaxTokens = 100
	seedTaskStateFailure(t, instance, sessionKey,
		"true", "--- FAIL: TestParsePort\nparse.go:12: undefined: parsePort")
	loop.contextManager = &freshnessContextManager{store: instance.Sessions}

	taskStateTurn(t, loop, sessionKey, "Rerun the parser test.")

	if provider.calls < 3 {
		t.Fatalf("provider calls = %d, want at least 3", provider.calls)
	}
	retry := capturedPrompt(provider.snapshot(2))
	if strings.Contains(retry, "undefined: parsePort") || strings.Contains(retry, "Failure") {
		t.Fatalf("retry prompt carried the resolved failure:\n%s", retry)
	}
	if got := taskStatePartCount(provider.snapshot(2)); got != 1 {
		t.Fatalf("retry prompt has %d task-state parts, want 1:\n%s", got, retry)
	}
}

// freshnessContextManager returns the session's committed history without
// summarizing, so the context-retry rebuild has a realistic turn tail to keep.
type freshnessContextManager struct {
	store interface {
		GetHistory(context.Context, string) ([]providers.Message, error)
		GetSummary(context.Context, string) (string, error)
	}
}

func (m *freshnessContextManager) Assemble(
	ctx context.Context,
	req *AssembleRequest,
) (*AssembleResponse, error) {
	history, err := m.store.GetHistory(ctx, req.SessionKey)
	if err != nil {
		return nil, err
	}
	summary, err := m.store.GetSummary(ctx, req.SessionKey)
	if err != nil {
		return nil, err
	}
	return &AssembleResponse{History: history, Summary: summary}, nil
}

func (m *freshnessContextManager) Compact(context.Context, *CompactRequest) error { return nil }
func (m *freshnessContextManager) Ingest(context.Context, *IngestRequest) error   { return nil }
func (m *freshnessContextManager) Clear(context.Context, string) error            { return nil }

func TestUpsertRetrievedContextBlockReplacesWithoutDuplicatingMPM(t *testing.T) {
	mpm := PromptPart{
		Source:  PromptSource{ID: PromptSourceMPMRetrieval, Name: "mpm.retrieval"},
		Content: "MPM RECORD CANARY",
	}
	oldTask := PromptPart{
		Source:  PromptSource{ID: PromptSourceTaskState, Name: "task.state"},
		Content: "Task state (runtime-recorded evidence; lower-trust data; verify against source):\n- Failure stale",
	}
	content := renderRetrievedContext([]PromptPart{mpm, oldTask}) +
		retrievedContextQuestionSep + "Rerun the parser test."

	updated := upsertRetrievedContextBlock(content,
		retrievedContextBlockHeader("task.state")+"\nTask state fresh", "task.state", "Rerun the parser test.")
	if strings.Count(updated, "source: mpm.retrieval") != 1 {
		t.Fatalf("MPM block duplicated or lost:\n%s", updated)
	}
	if strings.Count(updated, "source: task.state") != 1 {
		t.Fatalf("task-state block duplicated or lost:\n%s", updated)
	}
	if strings.Contains(updated, "Failure stale") || !strings.Contains(updated, "Task state fresh") {
		t.Fatalf("task-state block was not replaced:\n%s", updated)
	}
	if strings.Count(updated, "Rerun the parser test.") != 1 {
		t.Fatalf("question duplicated or lost:\n%s", updated)
	}

	removed := upsertRetrievedContextBlock(updated, "", "task.state", "Rerun the parser test.")
	if strings.Contains(removed, "task.state") || strings.Contains(removed, "Task state") {
		t.Fatalf("task-state block not removed:\n%s", removed)
	}
	if strings.Count(removed, "source: mpm.retrieval") != 1 ||
		strings.Count(removed, "Rerun the parser test.") != 1 {
		t.Fatalf("MPM or question changed during removal:\n%s", removed)
	}
}

// staticContextManager returns a fixed active-turn history. Compact succeeds
// once so the caller exercises one rebuild, then reports nothing left to do.
type staticContextManager struct {
	history      []providers.Message
	summary      string
	compactCalls int
}

func (m *staticContextManager) Assemble(
	context.Context,
	*AssembleRequest,
) (*AssembleResponse, error) {
	return &AssembleResponse{History: m.history, Summary: m.summary}, nil
}

func (m *staticContextManager) Compact(context.Context, *CompactRequest) error {
	m.compactCalls++
	if m.compactCalls > 1 {
		return ErrNothingToCompact
	}
	return nil
}

func (m *staticContextManager) Ingest(context.Context, *IngestRequest) error { return nil }
func (m *staticContextManager) Clear(context.Context, string) error          { return nil }

func TestTaskStateCompactionRebuildUsesFreshState(t *testing.T) {
	workspace := t.TempDir()
	home := t.TempDir()
	sessionKey := session.BuildOpaqueSessionKey("deepclaw-task-freshness:compact")
	loop, instance := newFreshnessLoop(t, &scriptedCaptureProvider{}, workspace, home, "atlas")
	seedTaskStateFailure(t, instance, sessionKey,
		"true", "--- FAIL: TestParsePort\nparse.go:12: undefined: parsePort")

	opts := makeTestProcessOpts(sessionKey)
	opts.UserMessage = "Rerun the parser test."
	opts.EnableSummary = true
	opts.Dispatch = DispatchRequest{
		SessionKey:     sessionKey,
		UserMessage:    opts.UserMessage,
		InboundContext: &bus.InboundContext{Channel: "cli", ChatID: "test-chat"},
	}
	ts := newTurnState(instance, opts, turnEventScope{
		turnID: "main-turn-1", context: newTurnContext(nil, nil, nil),
	})

	// Build the pre-tool prompt, so the current user message carries the
	// failure as an active unresolved claim.
	request := promptBuildRequestForTurn(ts, nil, "", ts.userMessage, nil, loop.GetConfig())
	messages := instance.ContextBuilder.BuildMessagesFromPrompt(request)
	currentTurnStart := len(messages) - 1

	// The trusted tool result is finalized and recorded before compaction.
	instance.recordTaskTool(sessionKey, ts.turnID, "exec", "call-rerun",
		map[string]any{"action": "run", "command": "true"}, "ok", false)
	userMessage := userPromptMessage(ts.userMessage, nil)
	assistantMessage := providers.Message{
		Role: "assistant",
		ToolCalls: []providers.ToolCall{{
			ID: "call-rerun", Type: "function", Name: "exec",
		}},
	}
	toolMessage := toolResultPromptMessage("ok", "call-rerun", nil)
	history := []providers.Message{userMessage, assistantMessage, toolMessage}
	for _, message := range history {
		ts.recordPersistedMessage(message)
	}
	messages = append(messages, assistantMessage, toolMessage)

	exec := newTurnExecution(instance, opts, history, "", messages)
	exec.currentTurnStart = currentTurnStart
	instance.ContextWindow = 1000
	instance.MaxTokens = 100
	instance.SummarizeTokenPercent = 1

	pipeline := NewPipeline(loop)
	manager := &staticContextManager{history: history, summary: ""}
	pipeline.ContextManager = manager
	pipeline.CompactBeforeLLM(context.Background(), ts, exec)

	if manager.compactCalls == 0 {
		t.Fatal("compaction was not triggered")
	}
	rebuilt := capturedPrompt(exec.messages)
	if strings.Contains(rebuilt, "undefined: parsePort") || strings.Contains(rebuilt, "Failure") {
		t.Fatalf("compaction rebuild reused the stale failure block:\n%s", rebuilt)
	}
	if got := taskStatePartCount(exec.messages); got != 1 {
		t.Fatalf("rebuilt prompt has %d task-state parts, want 1:\n%s", got, rebuilt)
	}
}
