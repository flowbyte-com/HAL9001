package bench

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/sipeed/picoclaw/pkg/providers"
)

// mockProvider is a deterministic, tool-capable provider used only to validate
// the harness. It answers from the exact messages the agent sends; it has no
// access to fixture files or other trials.
type mockProvider struct {
	spec TrialSpec
	// t1Stage and t6Stage sequence the deterministic "write script/test then
	// rerun" turns. The provider is recreated per trial and called sequentially.
	t1Stage int
	t6Stage int
	// multiT1Stage/multiT2Stage sequence the two-failure scenario.
	multiT1Stage int
	multiT2Stage int
	// codingStage sequences each 005A coding-task turn. The key is
	// taskID|turnID, never the full prompt, because task-state decoration
	// changes the rendered user message between iterations.
	codingStage map[string]int
}

func NewMockProvider(spec TrialSpec) providers.LLMProvider {
	return &mockProvider{spec: spec, codingStage: map[string]int{}}
}

func (m *mockProvider) GetDefaultModel() string {
	if strings.TrimSpace(m.spec.ModelName) != "" {
		return m.spec.ModelName
	}
	return "mock-003f"
}

func (m *mockProvider) Chat(
	ctx context.Context,
	messages []providers.Message,
	tools []providers.ToolDefinition,
	model string,
	options map[string]any,
) (*providers.LLMResponse, error) {
	lastUser := lastUserContent(messages)
	joined := joinMessageContent(messages)

	// The legacy context manager asks the same provider for a summary. Answer
	// neutrally so the compacted history cannot carry the failure diagnostic.
	if strings.HasPrefix(strings.TrimSpace(lastUser), "Provide a concise summary") ||
		strings.Contains(lastUser, "Merge these two conversation summaries") {
		if m.spec.Experiment == "D" {
			return m.respond(faithfulMockSummary(messages), messages), nil
		}
		if m.spec.Scenario == "task_state_summary_source" {
			return m.respond(TaskStateSummarySource, messages), nil
		}
		return m.respond("The user worked on parser test changes; earlier conversation details were compacted.", messages), nil
	}

	if m.spec.Experiment == "D" {
		return m.codingTaskResponse(messages, lastUser), nil
	}

	if m.spec.Scenario == "task_state_script" {
		return m.taskStateScriptResponse(messages, lastUser, joined), nil
	}
	if m.spec.Scenario == "task_state_multi_failure" {
		return m.taskStateMultiFailureResponse(messages, lastUser, joined), nil
	}
	if m.spec.Scenario == "task_state_summary_source" {
		return m.taskStateSummarySourceResponse(messages, lastUser, joined), nil
	}

	if m.spec.Scenario == "abstention" {
		return m.respond("I don't know; no matching vault phrase is recorded.", messages), nil
	}
	if m.spec.Scenario == "bash_probe" || m.spec.Scenario == "bash_mpm_probe" {
		last := lastMessage(messages)
		if last != nil && last.Role == "tool" {
			if strings.Contains(last.Content, "coding-canary-7f3") ||
				strings.Contains(last.Content, ".sqlite") || strings.Contains(last.Content, "mpm") {
				return m.respond("The hidden file is visible.", messages), nil
			}
			return m.respond("I don't know; the shell could not read it.", messages), nil
		}
		target := m.spec.FixtureProbe
		command := "cat " + target
		if m.spec.Scenario == "bash_mpm_probe" {
			target = m.spec.MPMProbe
			command = "find " + target + " -maxdepth 2 -type f"
		}
		return m.toolCall(m.toolName("bash"), map[string]any{"command": command}, messages), nil
	}
	if m.spec.Scenario == "fixture_probe" {
		last := lastMessage(messages)
		if last != nil && last.Role == "tool" {
			if m.spec.ExpectedAnswer != "" && strings.Contains(last.Content, m.spec.ExpectedAnswer) {
				return m.respond("The canary token is "+m.spec.ExpectedAnswer+".", messages), nil
			}
			return m.respond("I don't know; that information is not available in the current context.", messages), nil
		}
		if m.spec.FixtureProbe != "" {
			return m.toolCall(m.toolName("read"), map[string]any{"path": m.spec.FixtureProbe}, messages), nil
		}
	}
	if m.spec.Scenario == "coding_write" || m.spec.Scenario == "coding_edit" || m.spec.Scenario == "coding_read" {
		return m.codingResponse(messages, lastUser), nil
	}

	// The deterministic model can see only the prompt it is given. A fact that
	// is absent from every message must produce abstention, not a lucky answer.
	if strings.Contains(joined, m.spec.ExpectedAnswer) {
		return m.respond("The atlas project vault access phrase is "+m.spec.ExpectedAnswer+".", messages), nil
	}
	return m.respond("I don't know; that information is not available in the current context.", messages), nil
}

// taskStateBlock returns the rendered task-state block from a request, if any.
// The prompt builder wraps each part as
// "Retrieved context (... source: task.state):\n<content>" and appends the
// question after a separator, so the block ends at "\n\nQuestion: ".
func taskStateBlock(messages []providers.Message) string {
	const marker = "source: task.state):"
	for _, message := range messages {
		idx := strings.Index(message.Content, marker)
		if idx < 0 {
			continue
		}
		rest := message.Content[idx+len(marker):]
		if end := strings.Index(rest, "\n\nQuestion: "); end >= 0 {
			rest = rest[:end]
		}
		return strings.TrimSpace(rest)
	}
	return ""
}

func (m *mockProvider) taskStateScriptResponse(
	messages []providers.Message,
	lastUser, joined string,
) *providers.LLMResponse {
	block := taskStateBlock(messages)

	switch {
	case strings.Contains(lastUser, "Run the failing parser test"):
		switch m.t1Stage {
		case 0:
			m.t1Stage = 1
			return m.toolCall(m.toolName("write"), map[string]any{
				"path": "parser_check.sh", "content": TaskStateCheckScript, "overwrite": true,
			}, messages)
		case 1:
			m.t1Stage = 2
			return m.toolCall(m.toolName("bash"), map[string]any{"command": TaskStateTestCommand}, messages)
		default:
			return m.respond("The failing test reported: parse.go:12: undefined: parsePort.", messages)
		}
	case strings.Contains(lastUser, "Correction: use listenPort"):
		return m.respond("Understood: use listenPort and keep the JSON tag unchanged.", messages)
	case strings.Contains(lastUser, "Continue with the parser investigation"):
		return m.respond("Proposed diagnosis: the parser reads the deprecated port field.\nNext: inspect parse.go.", messages)
	case strings.Contains(lastUser, "Continue after compaction"):
		return m.respond("Compaction complete.", messages)
	case strings.Contains(lastUser, "What did the failing test report"):
		if strings.Contains(block, TaskStateDiagnostic) || strings.Contains(joined, TaskStateDiagnostic) {
			return m.respond("The failing test reported: parse.go:12: undefined: parsePort.", messages)
		}
		return m.respond("I don't know; that detail is no longer in the conversation.", messages)
	case strings.Contains(lastUser, "Run the parser test again"):
		switch m.t6Stage {
		case 0:
			m.t6Stage = 1
			return m.toolCall(m.toolName("write"), map[string]any{
				"path": "fixed.flag", "content": "fixed\n", "overwrite": true,
			}, messages)
		case 1:
			m.t6Stage = 2
			return m.toolCall(m.toolName("bash"), map[string]any{"command": TaskStateTestCommand}, messages)
		default:
			return m.respond("The parser test now passes.", messages)
		}
	case strings.Contains(lastUser, "Is the parser test still failing"):
		if block != "" {
			if strings.Contains(block, "Failure") && strings.Contains(block, TaskStateDiagnostic) {
				return m.respond("Yes, it is still failing.", messages)
			}
			return m.respond("No, the test now passes.", messages)
		}
		if strings.Contains(joined, "PASS") {
			return m.respond("No, the test now passes.", messages)
		}
		return m.respond("I don't know whether it is still failing.", messages)
	}
	return m.respond("Done.", messages)
}

func (m *mockProvider) taskStateMultiFailureResponse(
	messages []providers.Message,
	lastUser, joined string,
) *providers.LLMResponse {
	switch {
	case strings.Contains(lastUser, "Run both checks and report the failures"):
		switch m.multiT1Stage {
		case 0:
			m.multiT1Stage = 1
			return m.toolCall(m.toolName("write"), map[string]any{
				"path": "parser_check.sh", "content": TaskStateCheckScript, "overwrite": true,
			}, messages)
		case 1:
			m.multiT1Stage = 2
			return m.toolCall(m.toolName("write"), map[string]any{
				"path": "lint_check.sh", "content": TaskStateLintScript, "overwrite": true,
			}, messages)
		case 2:
			m.multiT1Stage = 3
			return m.toolCall(m.toolName("bash"), map[string]any{"command": TaskStateTestCommand}, messages)
		case 3:
			m.multiT1Stage = 4
			return m.toolCall(m.toolName("bash"), map[string]any{"command": TaskStateLintCommand}, messages)
		default:
			return m.respond("Both checks failed; the tool output has the diagnostics.", messages)
		}
	case strings.Contains(lastUser, "Run only the parser test again"):
		switch m.multiT2Stage {
		case 0:
			m.multiT2Stage = 1
			return m.toolCall(m.toolName("write"), map[string]any{
				"path": "fixed.flag", "content": "fixed\n", "overwrite": true,
			}, messages)
		case 1:
			m.multiT2Stage = 2
			return m.toolCall(m.toolName("bash"), map[string]any{"command": TaskStateTestCommand}, messages)
		default:
			return m.respond("The parser test now passes.", messages)
		}
	case strings.Contains(lastUser, "Which failures remain"):
		block := taskStateBlock(messages)
		if block != "" {
			parser := strings.Contains(block, TaskStateDiagnostic)
			lint := strings.Contains(block, TaskStateLintDiagnostic)
			switch {
			case lint && !parser:
				return m.respond("The lint check still fails: lint.go:4: unused import.", messages)
			case lint && parser:
				return m.respond("Both failures remain.", messages)
			case parser:
				return m.respond("The parser failure remains.", messages)
			default:
				return m.respond("No recorded failure remains.", messages)
			}
		}
		if strings.Contains(joined, TaskStateLintDiagnostic) {
			return m.respond("The lint check still fails: lint.go:4: unused import.", messages)
		}
		return m.respond("I don't know which failures remain; that detail is no longer in the conversation.", messages)
	case strings.Contains(lastUser, "Objective: Add a metrics endpoint"):
		return m.respond("Starting the metrics endpoint task.", messages)
	}
	return m.respond("Done.", messages)
}

func (m *mockProvider) taskStateSummarySourceResponse(
	messages []providers.Message,
	lastUser, joined string,
) *providers.LLMResponse {
	switch {
	case strings.Contains(lastUser, "Run the failing parser test"):
		switch m.t1Stage {
		case 0:
			m.t1Stage = 1
			return m.toolCall(m.toolName("write"), map[string]any{
				"path": "parser_check.sh", "content": TaskStateCheckScript, "overwrite": true,
			}, messages)
		case 1:
			m.t1Stage = 2
			return m.toolCall(m.toolName("bash"), map[string]any{"command": TaskStateTestCommand}, messages)
		default:
			return m.respond("The failing test reported: parse.go:12: undefined: parsePort.", messages)
		}
	case strings.Contains(lastUser, "Continue after compaction"):
		return m.respond("Compaction complete.", messages)
	case strings.Contains(lastUser, "What did the failing test report"):
		if strings.Contains(joined, TaskStateDiagnostic) {
			return m.respond("The failing test reported: parse.go:12: undefined: parsePort.", messages)
		}
		return m.respond("I don't know; that detail is no longer in the conversation.", messages)
	}
	return m.respond("Done.", messages)
}

func (m *mockProvider) codingResponse(messages []providers.Message, lastUser string) *providers.LLMResponse {
	last := lastMessage(messages)
	if last != nil && last.Role == "tool" {
		if strings.Contains(last.Content, m.spec.ExpectedAnswer) {
			return m.respond("The canary token is "+m.spec.ExpectedAnswer+".", messages)
		}
		if m.spec.Scenario == "coding_read" {
			return m.respond("I could not read the requested token.", messages)
		}
		return m.respond("Done.", messages)
	}
	if last != nil && last.Role == "assistant" && len(last.ToolCalls) > 0 {
		return m.respond("Done.", messages)
	}

	switch m.spec.Scenario {
	case "coding_write":
		content := m.spec.CodingExpectedContent
		if content == "" {
			content = "coding-canary-7f3\n"
		}
		return m.toolCall(m.toolName("write"), map[string]any{
			"path": m.spec.CodingExpectedFile, "content": content, "overwrite": true,
		}, messages)
	case "coding_edit":
		return m.toolCall(m.toolName("edit"), map[string]any{
			"path": m.spec.CodingExpectedFile, "old_text": "BROKEN", "new_text": strings.TrimSpace(m.spec.ExpectedAnswer),
		}, messages)
	case "coding_read":
		return m.toolCall(m.toolName("read"), map[string]any{"path": m.spec.CodingExpectedFile}, messages)
	}
	return m.respond("Done.", messages)
}

func (m *mockProvider) toolName(profileName string) string {
	if m.spec.ToolProfile != "standard" {
		return profileName
	}
	if profileName == "bash" {
		return "exec"
	}
	return profileName + "_file"
}

func (m *mockProvider) toolCall(name string, args map[string]any, messages []providers.Message) *providers.LLMResponse {
	if m.spec.VerboseMock {
		messages = verbosePadMessages(messages)
	}
	raw, _ := json.Marshal(args)
	return &providers.LLMResponse{
		FinishReason: "tool_calls",
		ToolCalls: []providers.ToolCall{{
			ID:   "mock-call-" + sanitize(name),
			Type: "function",
			Function: &providers.FunctionCall{
				Name:      name,
				Arguments: string(raw),
			},
		}},
		Usage: estimateUsage(messages, ""),
	}
}

func (m *mockProvider) respond(content string, messages []providers.Message) *providers.LLMResponse {
	if m.spec.VerboseMock {
		content = verbosePadCompletion(content)
	}
	return &providers.LLMResponse{
		Content:      content,
		FinishReason: "stop",
		Usage:        estimateUsage(messages, content),
	}
}

// verbosePadCompletion simulates a verbose live model: every assistant
// message carries roughly 1000 completion tokens, which is what makes
// prompts grow fast enough to trigger production threshold compaction.
// Deterministic filler; never used for capability claims.
func verbosePadCompletion(content string) string {
	const target = 4000
	if len(content) >= target {
		return content
	}
	var b strings.Builder
	b.WriteString(content)
	for b.Len() < target {
		b.WriteString("\nVerbose agent note: re-examining the repository state, tool outputs and remaining failures before deciding the next command. ")
	}
	return b.String()
}

func estimateUsage(messages []providers.Message, completion string) *providers.UsageInfo {
	promptChars := 0
	for _, msg := range messages {
		promptChars += len(msg.Content)
	}
	prompt := (promptChars + 3) / 4
	completionTokens := (len(completion) + 3) / 4
	return &providers.UsageInfo{
		PromptTokens:     prompt,
		CompletionTokens: completionTokens,
		TotalTokens:      prompt + completionTokens,
	}
}

func lastUserContent(messages []providers.Message) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "user" {
			return messages[i].Content
		}
	}
	return ""
}

func lastMessage(messages []providers.Message) *providers.Message {
	if len(messages) == 0 {
		return nil
	}
	return &messages[len(messages)-1]
}

func joinMessageContent(messages []providers.Message) string {
	var b strings.Builder
	for _, msg := range messages {
		b.WriteString(msg.Content)
		b.WriteByte('\n')
	}
	return b.String()
}

var _ = fmt.Sprintf

// ---------------------------------------------------------------------------
// 005A deterministic coding-task script.
//
// The mock is deliberately not a model: it replays a fixed, realistic tool
// trajectory so the harness, scoring, isolation and measurement paths can be
// validated. It must never be used to claim improved coding intelligence.

type codingMockAction struct {
	Tool string
	Args map[string]any
	Say  string
}

func mockRead(path string) codingMockAction {
	return codingMockAction{Tool: "read", Args: map[string]any{"path": path}}
}

func mockWrite(path, content string) codingMockAction {
	return codingMockAction{Tool: "write", Args: map[string]any{
		"path": path, "content": content, "overwrite": true,
	}}
}

func mockEdit(path, oldText, newText string) codingMockAction {
	return codingMockAction{Tool: "edit", Args: map[string]any{
		"path": path, "old_text": oldText, "new_text": newText,
	}}
}

func mockBash(command string) codingMockAction {
	return codingMockAction{Tool: "bash", Args: map[string]any{"command": command}}
}

func mockSay(text string) codingMockAction {
	return codingMockAction{Say: text}
}

func (m *mockProvider) codingTaskResponse(messages []providers.Message, lastUser string) *providers.LLMResponse {
	turn := codingMockTurn(m.spec.CodingTask, lastUser)
	key := m.spec.CodingTask + "|" + turn
	stage := m.codingStage[key]
	actions := codingMockActions(m.spec.CodingTask, turn)
	if m.spec.VerboseMock {
		actions = verboseCodingMockActions(m.spec.CodingTask, turn, actions, m.spec.VerboseMockIters)
	}
	if stage >= len(actions) {
		return m.respond("Done.", messages)
	}
	m.codingStage[key] = stage + 1
	action := actions[stage]
	if action.Say != "" {
		return m.respond(action.Say, messages)
	}
	return m.toolCall(action.Tool, action.Args, messages)
}

func codingMockTurn(task, lastUser string) string {
	switch task {
	case "requirement_change":
		if strings.Contains(lastUser, "Finance has changed the tie rule") {
			return "t2"
		}
		return "t1"
	case "compaction_ledger":
		if strings.Contains(lastUser, "Continue and finish the ledger fix") {
			return "t3"
		}
		if strings.Contains(lastUser, "Run the checks again") {
			return "t2"
		}
		return "t1"
	case "restart_parser":
		if strings.Contains(lastUser, "We restarted the process") {
			return "t2"
		}
		return "t1"
	case "tiered_migration":
		if strings.Contains(lastUser, "Finish the tier work") {
			return "t3"
		}
		if strings.Contains(lastUser, "Run the checks again") {
			return "t2"
		}
		return "t1"
	default:
		return "t1"
	}
}

func codingMockActions(task, turn string) []codingMockAction {
	switch task {
	case "multi_file_cart":
		return []codingMockAction{
			mockRead("discount.go"),
			mockRead("money.go"),
			mockWrite("discount.go", `package cart

import "strings"

// DiscountCents returns the discount in cents for a promotion code.
func DiscountCents(subtotalCents int, code string) int {
	switch strings.ToUpper(strings.TrimSpace(code)) {
	case "SAVE15":
		return subtotalCents * 15 / 100
	case "SAVE10":
		return subtotalCents * 10 / 100
	default:
		return 0
	}
}
`),
			mockWrite("money.go", `package cart

import "fmt"

// FormatCents renders a cent amount as a dollar string.
func FormatCents(cents int) string {
	return fmt.Sprintf("$%d.%02d", cents/100, cents%100)
}
`),
			mockBash("sh check.sh"),
			mockSay("All cart checks pass now."),
		}
	case "misleading_failure":
		return []codingMockAction{
			mockBash("sh check.sh"),
			mockRead("legacy_golden.txt"),
			mockBash("sh diagnose.sh"),
			mockRead("README.md"),
			mockRead("rate.go"),
			mockEdit("rate.go", "if weightGrams >= 1000 {", "if weightGrams > 1000 {"),
			mockBash("sh check.sh"),
			mockSay("The current README contract is implemented; all checks pass."),
		}
	case "requirement_change":
		if turn == "t2" {
			return []codingMockAction{
				mockRead("invoice.go"),
				mockWrite("invoice.go", `package billing

import "math"

// RoundCents rounds a currency amount to the nearest cent. Exact ties round to
// the nearest even cent.
func RoundCents(amount float64) int {
	floor := math.Floor(amount)
	diff := amount - floor
	switch {
	case diff > 0.5:
		return int(floor) + 1
	case diff < 0.5:
		return int(floor)
	case math.Mod(floor, 2) == 0:
		return int(floor)
	default:
		return int(floor) + 1
	}
}
`),
				mockBash("sh check.sh"),
				mockSay("Ties now round to even and non-tie behavior is unchanged."),
			}
		}
		return []codingMockAction{
			mockRead("invoice.go"),
			mockWrite("invoice.go", `package billing

import "math"

// RoundCents rounds a currency amount to the nearest cent.
func RoundCents(amount float64) int {
	return int(math.Floor(amount + 0.5))
}
`),
			mockBash("sh check.sh"),
			mockSay("Non-tie rounding passes; exact ties currently round up."),
		}
	case "compaction_ledger":
		if turn == "t3" {
			return []codingMockAction{
				mockRead("validate.go"),
				mockWrite("validate.go", `package ledger

import "errors"

// Validate checks whether an entry may be posted.
func Validate(entry Entry) error {
	if entry.Cents < -1000000 {
		return errors.New("amount too large")
	}
	return nil
}
`),
				mockBash("sh check.sh"),
				mockSay("The ledger fix is complete."),
			}
		}
		if turn == "t2" {
			return []codingMockAction{
				mockBash("sh check.sh"),
				mockSay("Only TestValidateAllowsEmptyDescription still fails."),
			}
		}
		return []codingMockAction{
			mockBash("sh check.sh"),
			mockRead("balance.go"),
			mockRead("validate.go"),
			mockWrite("balance.go", `package ledger

// Entry is one ledger line. Positive cents are credits, negative are debits.
type Entry struct {
	Description string
	Cents       int
}

// Balance returns the net balance in cents.
func Balance(entries []Entry) int {
	total := 0
	for _, entry := range entries {
		total += entry.Cents
	}
	return total
}
`),
			mockBash("sh check.sh"),
			mockSay("Balance now includes debits; the validation test still fails."),
		}
	case "restart_parser":
		if turn == "t2" {
			return []codingMockAction{
				mockRead("parse.go"),
				mockWrite("parse.go", `package parser

import "strings"

// ParseList splits a comma-separated list.
func ParseList(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
`),
				mockBash("sh check.sh"),
				mockSay("Both parser failures are fixed."),
			}
		}
		return []codingMockAction{
			mockBash("sh check.sh"),
			mockRead("token.go"),
			mockRead("parse.go"),
			mockWrite("token.go", `package parser

import (
	"strconv"
	"strings"
)

// ParseInt parses a decimal integer.
func ParseInt(s string) (int, error) {
	return strconv.Atoi(strings.TrimSpace(s))
}
`),
			mockBash("sh check.sh"),
			mockSay("ParseInt is fixed; ParseList still fails."),
		}
	case "tiered_migration":
		if turn == "t3" {
			return []codingMockAction{
				mockRead("round.go"),
				mockWrite("regression_test.go", `package tiers

import "testing"

// TestG4Regression pins the original mis-mapping: G4 (edge 1000) used to
// migrate to 800 (the next tier up) instead of 650.
func TestG4Regression(t *testing.T) {
	if got := Migrate("G4"); got == 800 {
		t.Fatalf("regression: Migrate(G4) = 800, want the fixed mapping")
	}
	if got := Migrate("G4"); got != 650 {
		t.Fatalf("Migrate(G4) = %d, want 650", got)
	}
}
`),
				mockBash("sh check.sh"),
				mockRead("round.go"),
				mockWrite("round.go", `package tiers

import "math"

// RoundEdge rounds a currency amount to the nearest cent. Exact halves
// round to the nearest even cent (banker's rounding).
func RoundEdge(amount float64) int {
	return int(math.RoundToEven(amount))
}
`),
				mockBash("sh check.sh"),
				mockSay("The rounding helper now matches the dry-run mapping."),
			}
		}
		if turn == "t2" {
			return []codingMockAction{
				mockBash("sh check.sh"),
				mockSay("Only TestRoundEdgeTies still fails."),
			}
		}
		return []codingMockAction{
			mockBash("sh check.sh"),
			mockRead("legacy.go"),
			mockRead("tiers.go"),
			mockBash("go test -run TestDryRunMapping -v ./..."),
			mockWrite("legacy.go", `package tiers

// LegacyTier is one row of the retired legacy table. Codes are old-style
// ("G1".."G7"); edges match the current table one to one.
type LegacyTier struct {
	Code      string
	EdgeCents int
}

var legacyTable = []LegacyTier{
	{Code: "G1", EdgeCents: 100},
	{Code: "G2", EdgeCents: 250},
	{Code: "G3", EdgeCents: 500},
	{Code: "G4", EdgeCents: 1000},
	{Code: "G5", EdgeCents: 2500},
	{Code: "G6", EdgeCents: 5000},
	{Code: "G7", EdgeCents: 10000},
}

// Migrate maps a legacy tier code to its current rate in cents. An amount
// sitting exactly on a tier edge belongs to that tier.
func Migrate(code string) int {
	for _, tier := range legacyTable {
		if code != tier.Code {
			continue
		}
		return RateFor(tier.EdgeCents)
	}
	return -1
}
`),
			mockBash("sh check.sh"),
			mockSay("G4 now maps to its own tier; the rounding helper still fails as expected."),
		}
	}
	return nil
}

// faithfulMockSummary is an extractive summary used only by deterministic
// mock trials. It preserves user requirements and tool evidence instead of
// returning an artificially impoverished summary that would disadvantage the
// control condition. Live trials use the production summarizer.
func faithfulMockSummary(messages []providers.Message) string {
	var b strings.Builder
	b.WriteString("Deterministic benchmark summary of the conversation so far.\n")
	for _, message := range messages {
		content := strings.TrimSpace(message.Content)
		if content == "" {
			continue
		}
		switch message.Role {
		case "system":
			continue
		case "user":
			if strings.HasPrefix(content, "Provide a concise summary") ||
				strings.Contains(content, "Merge these two conversation summaries") {
				continue
			}
			b.WriteString("User: " + mockSummaryLine(content, 300) + "\n")
		case "assistant":
			b.WriteString("Assistant: " + mockSummaryLine(content, 240) + "\n")
		case "tool":
			b.WriteString("Tool result: " + mockSummaryLine(content, 500) + "\n")
		}
		if b.Len() > 6000 {
			break
		}
	}
	return strings.TrimSpace(b.String())
}

func mockSummaryLine(value string, limit int) string {
	value = strings.Join(strings.Fields(value), " ")
	if len(value) <= limit {
		return value
	}
	return value[:limit] + "..."
}

// ---------------------------------------------------------------------------
// 005B-R verbose-agent simulation.
//
// The aborted live pilot showed a trajectory the deterministic script never
// produces: ~20 tool iterations per turn with ~1000-token completions,
// repeated failing checks, and threshold compaction firing on nearly every
// request. verboseCodingMockActions replays that *shape* deterministically —
// same action kinds, an order of magnitude more of them — so compaction
// churn, runaway guards and crash recovery can be exercised offline. It
// changes no fixture, scoring rule or condition behavior, and the final
// repository outcome still converges (the fix is applied early, exactly
// like the live trial that fixed balance.go and then looped).

func verboseCodingMockActions(task, turn string, base []codingMockAction, iters int) []codingMockAction {
	if task != "compaction_ledger" && task != "tiered_migration" {
		return base
	}
	// iters <= 0 selects the live shape: 20 tool actions per turn with no
	// converging response, so the agent-side loop exhausts its iteration
	// budget exactly like the aborted live trial. Positive iters converges
	// with the base final response after that many tools, which exercises
	// automatic-then-explicit compaction sequencing offline.
	live := iters <= 0
	if live {
		iters = 20
	}
	filler := []codingMockAction{
		mockBash("sh check.sh"),
		mockRead("balance.go"),
		mockBash("sh check.sh"),
		mockRead("validate.go"),
		mockBash("sh check.sh"),
		mockRead("ledger_test.go"),
	}
	if task == "tiered_migration" {
		filler = []codingMockAction{
			mockBash("sh check.sh"),
			mockRead("legacy.go"),
			mockBash("go test -run TestDryRunMapping -v ./..."),
			mockRead("tiers.go"),
			mockBash("sh check.sh"),
			mockRead("round.go"),
		}
	}
	tools := func(actions []codingMockAction) []codingMockAction {
		var out []codingMockAction
		for _, action := range actions {
			if action.Say == "" {
				out = append(out, action)
			}
		}
		return out
	}
	finalSay := func(actions []codingMockAction) *codingMockAction {
		for _, action := range actions {
			if action.Say != "" {
				action := action
				return &action
			}
		}
		return nil
	}
	var out []codingMockAction
	switch turn {
	case "t1":
		// t1 applies the balance fix first (as the live agent did).
		out = append(out, tools(base)...)
		for len(out) < iters {
			out = append(out, filler...)
		}
		out = out[:iters]
		if live {
			return out
		}
		if final := finalSay(base); final != nil {
			out = append(out, *final)
		}
		return out
	case "t2":
		// t2 only ever re-ran checks and reads.
		for len(out) < iters {
			out = append(out, filler...)
		}
		out = out[:iters]
		if live {
			return out
		}
		if final := finalSay(base); final != nil {
			out = append(out, *final)
		}
		return out
	default:
		return base
	}
}

// verbosePadMessages grows the visible history the way a verbose model's
// long tool outputs would, so threshold compaction fires as it did live.
// Deterministic; test-only shape simulation.
func verbosePadMessages(messages []providers.Message) []providers.Message {
	out := make([]providers.Message, len(messages))
	copy(out, messages)
	if len(out) == 0 {
		return out
	}
	out[len(out)-1].Content += verbosePadCompletion("")
	return out
}
