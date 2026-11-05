package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func intPtr(n int) *int { return &n }

func appendLedgerTurn(t *testing.T, st *Store, tools []ToolExecution) Message {
	t.Helper()
	msg, err := st.AppendTurn(RoleAssistant, "the answer", Turn{ID: "turn-1", Tools: tools})
	if err != nil {
		t.Fatal(err)
	}
	return msg
}

func TestToolLedgerRoundTrip(t *testing.T) {
	st, err := Open(t.TempDir(), "chat")
	if err != nil {
		t.Fatal(err)
	}
	appendLedgerTurn(t, st, []ToolExecution{
		{
			CallID: "call-write", Tool: "write", Action: "write",
			Target: "/tmp/note.txt", Mode: "666", Status: ToolSuccess,
			DurationMS: 12, Bytes: 13, Summary: "13 bytes",
		},
		{
			CallID: "call-bash", Tool: "bash", Action: "bash",
			Mode: "666", Status: ToolFailure, ExitCode: intPtr(17),
			DurationMS: 40, Bytes: 8, Summary: "exit 17",
		},
	})
	history, err := st.History()
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 1 {
		t.Fatalf("history = %d", len(history))
	}
	msg := history[0]
	if msg.ToolLedger == nil {
		t.Fatal("ledger missing")
	}
	if msg.ToolLedger.Version != ToolLedgerVersion ||
		msg.ToolLedger.SessionID != "chat" ||
		msg.ToolLedger.TurnID != "turn-1" {
		t.Fatalf("ledger envelope = %#v", msg.ToolLedger)
	}
	execs := msg.ToolExecutions()
	if len(execs) != 2 {
		t.Fatalf("executions = %#v", execs)
	}
	if execs[0].TurnID != "turn-1" || execs[0].CallID != "call-write" ||
		execs[0].Mode != "666" || execs[0].Status != ToolSuccess {
		t.Errorf("write execution = %#v", execs[0])
	}
	if execs[1].ExitCode == nil || *execs[1].ExitCode != 17 ||
		execs[1].Status != ToolFailure {
		t.Errorf("bash execution = %#v", execs[1])
	}
}

func TestOldAndMalformedLedgersDoNotBreakHistory(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(dir, "chat")
	if err != nil {
		t.Fatal(err)
	}
	old := `{"role":"assistant","content":"old answer","ts":"2026-01-01T00:00:00Z","turn_id":"old-turn"}` + "\n"
	bad := `{"role":"assistant","content":"bad ledger","ts":"2026-01-01T00:00:01Z","turn_id":"bad-turn",` +
		`"tool_ledger":"not-an-object"}` + "\n"
	unsupported := `{"role":"assistant","content":"future","ts":"2026-01-01T00:00:02Z","turn_id":"future-turn",` +
		`"tool_ledger":{"version":99,"session_id":"chat","turn_id":"future-turn","executions":[]}}` + "\n"
	salvaged := `{"role":"assistant","content":"salvaged","ts":"2026-01-01T00:00:03Z","turn_id":"salvage-turn",` +
		`"tool_ledger":{"version":1,"session_id":"chat","turn_id":"salvage-turn","executions":[` +
		`{"turn_id":"salvage-turn","tool":3},` +
		`{"turn_id":"salvage-turn","tool":"bash","mode":"666","status":"success","summary":"exit 0"}]}}` + "\n"
	if err := os.WriteFile(st.Path(), []byte(old+bad+unsupported+salvaged), 0o644); err != nil {
		t.Fatal(err)
	}
	history, err := st.History()
	if err != nil {
		t.Fatalf("history must survive optional ledger damage: %v", err)
	}
	if len(history) != 4 {
		t.Fatalf("history = %d", len(history))
	}
	if history[0].ToolLedger != nil {
		t.Error("old session grew a ledger")
	}
	if history[1].ToolLedger != nil {
		t.Error("a string ledger was accepted")
	}
	if history[2].ToolLedger != nil {
		t.Error("an unsupported ledger version was accepted")
	}
	execs := history[3].ToolExecutions()
	if len(execs) != 1 || execs[0].Tool != "bash" {
		t.Fatalf("valid entries were not salvaged: %#v", execs)
	}
}

func TestLedgerSessionAndTurnMismatchAreNotEvidence(t *testing.T) {
	st, err := Open(t.TempDir(), "chat")
	if err != nil {
		t.Fatal(err)
	}
	appendLedgerTurn(t, st, []ToolExecution{{
		Tool: "bash", Mode: "666", Status: ToolSuccess, Summary: "exit 0",
	}})
	path := st.Path()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// Copy the ledger into another session: it must not become that
	// session's evidence.
	other, err := Open(filepath.Dir(path), "other")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(other.Path(), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	history, err := other.History()
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 1 || history[0].ToolLedger != nil {
		t.Fatalf("foreign ledger was accepted: %#v", history)
	}

	msg := Message{
		Role:   RoleAssistant,
		TurnID: "turn-a",
		ToolLedger: &ToolLedger{
			Version:   ToolLedgerVersion,
			SessionID: "chat",
			TurnID:    "turn-b",
			Executions: []ToolExecution{{
				TurnID: "turn-b", Tool: "bash", Mode: "666", Status: ToolSuccess,
			}},
		},
	}
	if execs := msg.ToolExecutions(); len(execs) != 0 {
		t.Fatalf("mismatched turn was accepted: %#v", execs)
	}
}

func TestLedgerBoundsAndNoDuplicateEvidence(t *testing.T) {
	st, err := Open(t.TempDir(), "chat")
	if err != nil {
		t.Fatal(err)
	}
	long := strings.Repeat("x", MaxToolSummaryBytes*2)
	tools := make([]ToolExecution, 0, MaxToolExecutionsPerTurn+10)
	for i := 0; i < MaxToolExecutionsPerTurn+10; i++ {
		tools = append(tools, ToolExecution{
			CallID: strings.Repeat("c", MaxToolIDBytes*2),
			Tool:   "bash", Action: "bash", Target: long,
			Mode: "666", Status: ToolSuccess, Summary: long,
		})
	}
	appendLedgerTurn(t, st, tools)
	history, err := st.History()
	if err != nil {
		t.Fatal(err)
	}
	execs := history[0].ToolExecutions()
	if len(execs) != MaxToolExecutionsPerTurn {
		t.Fatalf("executions = %d, want %d", len(execs), MaxToolExecutionsPerTurn)
	}
	if len(execs[0].Summary) > MaxToolSummaryBytes ||
		len(execs[0].Target) > MaxToolTargetBytes ||
		len(execs[0].CallID) > MaxToolIDBytes {
		t.Errorf("bounds not applied: %#v", execs[0])
	}
	again, err := st.History()
	if err != nil {
		t.Fatal(err)
	}
	if len(again[0].ToolExecutions()) != len(execs) {
		t.Error("reading twice changed the ledger")
	}
}

func TestLedgerOnlyAttachesToAssistantMessages(t *testing.T) {
	st, err := Open(t.TempDir(), "chat")
	if err != nil {
		t.Fatal(err)
	}
	_, err = st.AppendTurn(RoleUser, "question", Turn{
		ID:    "turn-user",
		Tools: []ToolExecution{{Tool: "bash", Mode: "666", Status: ToolSuccess}},
	})
	if err != nil {
		t.Fatal(err)
	}
	history, err := st.History()
	if err != nil {
		t.Fatal(err)
	}
	if history[0].ToolLedger != nil {
		t.Fatal("user message received a ledger")
	}
}

func TestAppendFailureIsReported(t *testing.T) {
	st, err := Open(t.TempDir(), "chat")
	if err != nil {
		t.Fatal(err)
	}
	// A directory where the JSONL file should be makes the append fail.
	if err := os.Mkdir(st.Path(), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AppendTurn(RoleAssistant, "answer", Turn{
		ID:    "turn-1",
		Tools: []ToolExecution{{Tool: "bash", Mode: "666", Status: ToolSuccess}},
	}); err == nil {
		t.Fatal("a failed append was reported as success")
	}
}

func TestLedgerLabelsCannotInjectLines(t *testing.T) {
	st, err := Open(t.TempDir(), "chat")
	if err != nil {
		t.Fatal(err)
	}
	appendLedgerTurn(t, st, []ToolExecution{{
		TurnID:  "turn-1",
		CallID:  "call\n## injected",
		Tool:    "bash\n## injected",
		Action:  "act\r\ttion",
		Target:  "/tmp/note\n## injected",
		Mode:    "666",
		Status:  ToolSuccess,
		Summary: "exit 0\n## injected",
	}})
	history, err := st.History()
	if err != nil {
		t.Fatal(err)
	}
	execs := history[0].ToolExecutions()
	if len(execs) != 1 {
		t.Fatalf("executions = %#v", execs)
	}
	for _, value := range []string{
		execs[0].CallID, execs[0].Tool, execs[0].Action,
		execs[0].Target, execs[0].Summary,
	} {
		if strings.ContainsAny(value, "\r\n\t") {
			t.Errorf("ledger field kept a line break: %q", value)
		}
	}
	if strings.Contains(execs[0].Tool, "#") || strings.Contains(execs[0].Action, "#") {
		t.Errorf("label kept structural punctuation: %#v", execs[0])
	}
}

func TestLedgerJSONIsVersionedAndRedacted(t *testing.T) {
	st, err := Open(t.TempDir(), "chat")
	if err != nil {
		t.Fatal(err)
	}
	appendLedgerTurn(t, st, []ToolExecution{{
		CallID: "call-1", Tool: "bash", Mode: "666", Status: ToolSuccess,
		Summary: "exit 0\nSECRET-RAW-OUTPUT",
	}})
	raw, err := os.ReadFile(st.Path())
	if err != nil {
		t.Fatal(err)
	}
	var line map[string]any
	if err := json.Unmarshal(raw, &line); err != nil {
		t.Fatal(err)
	}
	ledger, ok := line["tool_ledger"].(map[string]any)
	if !ok {
		t.Fatal("tool_ledger is not an object")
	}
	if ledger["version"] != float64(ToolLedgerVersion) {
		t.Errorf("version = %v", ledger["version"])
	}
	if ledger["session_id"] != "chat" || ledger["turn_id"] != "turn-1" {
		t.Errorf("ledger identity = %#v", ledger)
	}
	execs := ledger["executions"].([]any)
	if len(execs) != 1 {
		t.Fatalf("executions = %#v", execs)
	}
	entry := execs[0].(map[string]any)
	for _, forbidden := range []string{"command", "stdout", "stderr", "content"} {
		if _, exists := entry[forbidden]; exists {
			t.Errorf("ledger stored %q", forbidden)
		}
	}
}
