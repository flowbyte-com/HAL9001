package engine

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flowbyte-com/HAL9001/internal/agenttools"
	"github.com/flowbyte-com/HAL9001/internal/permission"
	"github.com/flowbyte-com/HAL9001/internal/provider"
	"github.com/flowbyte-com/HAL9001/internal/session"
	"github.com/flowbyte-com/HAL9001/internal/workspace"
)

// TestResumeKeepsToolEvidenceDistinguishable covers the full permission
// lifecycle: 444, an operator-enabled 666 turn with a successful write and
// bash call, back to 444, then a resumed provider request.
//
// The key is the write path and the bash exit status. The assistant's
// final visible text deliberately does not contain the path or the live
// output marker: it says only that the note was written and the check
// completed. The write path and the bash execution record must survive in
// the ledger, and the resumed context must carry them as a trusted system
// note rather than as assistant prose or fabricated provider calls.
//
// Run with:
//
//	go test -run TestResumeKeepsToolEvidenceDistinguishable ./internal/engine
func TestResumeKeepsToolEvidenceDistinguishable(t *testing.T) {
	const (
		bashMarker = "RESUME-TOOL-EVIDENCE-9c1f"
		writeName  = "resume-write-9c1f.txt"
		writeBody  = "durable note\n"
	)
	dir := t.TempDir()
	note := filepath.Join(dir, writeName)

	// 444 is the starting point: no privileged tool is offered.
	st, err := session.Open(dir, "chat")
	if err != nil {
		t.Fatal(err)
	}
	perm := permission.New()
	perm.SetSession("chat")
	start := New(st, &stepProvider{}, provider.Model{ID: "m"}).
		WithPermission(perm).
		WithExecution(agenttools.New(perm, nil))
	for _, name := range []string{"read", "write", "edit", "bash"} {
		if hasTool(start.offeredTools(), name) {
			t.Fatalf("444 offered %q", name)
		}
	}

	// The operator enables 666. Both tools succeed in this one session.
	if err := perm.Enable("chat"); err != nil {
		t.Fatal(err)
	}
	audit, err := workspace.OpenAudit(filepath.Join(dir, "audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	p := &stepProvider{responses: []provider.Response{
		{
			Content: "I will write the note.",
			ToolCalls: []provider.ToolCall{{
				ID: "write-1", Name: "write",
				Arguments: map[string]any{"path": note, "content": writeBody},
			}},
		},
		{Content: "The note was written."},
		{
			Content: "I will run the check.",
			ToolCalls: []provider.ToolCall{{
				ID: "bash-1", Name: "bash",
				Arguments: map[string]any{"command": "printf " + bashMarker},
			}},
		},
		{Content: "The check completed."},
	}}
	eng := New(st, p, provider.Model{ID: "m"}).
		WithSystemPrompt("You are HAL9001.").
		WithPermission(perm).
		WithExecution(agenttools.New(perm, audit).WithSession(func() string { return "chat" }))
	if _, err := eng.RespondStreamEvents(context.Background(), "write the note", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.RespondStreamEvents(context.Background(), "run the check", nil); err != nil {
		t.Fatal(err)
	}

	body, err := os.ReadFile(note)
	if err != nil || string(body) != writeBody {
		t.Fatalf("written file = %q, %v", body, err)
	}

	// The live turn received both tool results. This is the evidence that
	// must survive the resume.
	live := false
	for _, request := range p.requests {
		for _, message := range request {
			if message.Role != "tool" {
				continue
			}
			if strings.Contains(message.Content, bashMarker) ||
				strings.Contains(message.Content, note) {
				live = true
			}
		}
	}
	if !live {
		t.Fatal("the live 666 turn did not receive a tool result")
	}

	history, err := st.History()
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 4 {
		t.Fatalf("history = %d messages, want 4: %#v", len(history), history)
	}
	// The assistant's visible text is not allowed to be the carrier of the
	// tool evidence.
	if strings.Contains(history[1].Content, bashMarker) ||
		strings.Contains(history[1].Content, note) ||
		strings.Contains(history[3].Content, bashMarker) ||
		strings.Contains(history[3].Content, note) {
		t.Fatal("assistant prose contains the tool marker or path; the test cannot distinguish evidence from a claim")
	}
	// The turn identity is durable for the user and assistant messages.
	if history[0].TurnID == "" || history[0].TurnID != history[1].TurnID {
		t.Fatalf("write turn ids = %q / %q", history[0].TurnID, history[1].TurnID)
	}
	if history[2].TurnID == "" || history[2].TurnID != history[3].TurnID {
		t.Fatalf("bash turn ids = %q / %q", history[2].TurnID, history[3].TurnID)
	}

	// The audit proves the executions happened, but it carries no turn id.
	records, err := audit.Records()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 || records[0].Tool != "write" ||
		records[1].Tool != "bash" || records[1].ExitCode == nil ||
		*records[1].ExitCode != 0 {
		t.Fatalf("audit records = %#v", records)
	}
	auditJSON, _ := json.Marshal(records[0])
	t.Logf("durable assistant message = %q", history[1].Content)
	t.Logf("durable bash answer = %q", history[3].Content)
	t.Logf("audit write record = %s", auditJSON)

	// The assistant's own answers now carry the verified ledger.
	writeEvidence := history[1].ToolExecutions()
	bashEvidence := history[3].ToolExecutions()
	if len(writeEvidence) != 1 || writeEvidence[0].Tool != "write" ||
		writeEvidence[0].TurnID != history[1].TurnID ||
		writeEvidence[0].Mode != "666" || writeEvidence[0].Status != session.ToolSuccess {
		t.Fatalf("write ledger = %#v", writeEvidence)
	}
	if len(bashEvidence) != 1 || bashEvidence[0].Tool != "bash" ||
		bashEvidence[0].TurnID != history[3].TurnID ||
		bashEvidence[0].Mode != "666" || bashEvidence[0].Status != session.ToolSuccess ||
		bashEvidence[0].ExitCode == nil || *bashEvidence[0].ExitCode != 0 {
		t.Fatalf("bash ledger = %#v", bashEvidence)
	}

	// Return to 444 and resume as a second process would.
	perm.Disable()
	st2, err := session.Open(dir, "chat")
	if err != nil {
		t.Fatal(err)
	}
	spy := &stepProvider{responses: []provider.Response{{
		Content: "Earlier you wrote a note and ran a check.",
	}}}
	resume := New(st2, spy, provider.Model{ID: "m"}).
		WithSystemPrompt("You are HAL9001.").
		WithPermission(perm).
		WithExecution(agenttools.New(perm, nil))
	if _, err := resume.RespondStreamEvents(context.Background(), "what did you do earlier?", nil); err != nil {
		t.Fatal(err)
	}
	if len(spy.requests) == 0 {
		t.Fatal("no resumed request captured")
	}
	request := spy.requests[0]
	system := request[0].Content
	if request[0].Role != "system" || !strings.Contains(system, "444 READ ONLY") {
		t.Fatalf("resumed system instruction is not 444:\n%s", system)
	}
	if !strings.Contains(system, "Verified historical tool evidence") ||
		!strings.Contains(system, "not current tool calls") ||
		!strings.Contains(system, history[1].TurnID) ||
		!strings.Contains(system, history[3].TurnID) ||
		!strings.Contains(system, note) ||
		!strings.Contains(system, "bash") ||
		!strings.Contains(system, "mode 666") ||
		!strings.Contains(system, "success") ||
		!strings.Contains(system, "exit 0") {
		t.Fatalf("resumed system note lacks verified evidence:\n%s", system)
	}
	if !strings.Contains(system, "do not call it fabricated") {
		t.Errorf("the missing-evidence instruction is absent:\n%s", system)
	}

	// Evidence is a system note, not fabricated provider tool calls.
	for _, message := range request {
		if message.Role == "tool" {
			t.Errorf("resume invented a provider tool message: %#v", message)
		}
		if len(message.ToolCalls) > 0 {
			t.Errorf("resume invented provider tool calls: %#v", message.ToolCalls)
		}
	}
	// Historical 666 evidence cannot restore 666 or any privileged tool.
	if perm.Mode() != permission.Guarded {
		t.Fatalf("resume permission = %s", perm.Mode())
	}
	if tools := resume.offeredTools(); hasTool(tools, "read") || hasTool(tools, "write") ||
		hasTool(tools, "edit") || hasTool(tools, "bash") {
		t.Fatalf("historical evidence enabled privileged tools: %v", toolNames(tools))
	}
	t.Logf("resumed verified evidence = %s", system)
}
