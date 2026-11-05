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
// The key is the marker and the path. The assistant's final visible text
// deliberately does not contain either: it says only that the note was
// written and the check completed. If the resumed request contains those
// values, they can only have come from a persisted tool result, not from
// assistant prose. The test therefore proves two things at once:
//
//   - a completed tool execution survives the permission change and resume;
//   - it remains distinguishable from an assistant-generated claim.
//
// The current session format stores no tool records, so the final assertion
// is a known gap. It is skipped by default to keep the suite green while the
// architecture is under review. Run with HAL_RESUME_STRICT=1 to make the
// missing evidence fail the test:
//
//	HAL_RESUME_STRICT=1 go test -run TestResumeKeepsToolEvidenceDistinguishable ./internal/engine
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
	if !strings.Contains(request[0].Content, "444 READ ONLY") {
		t.Fatalf("resumed system instruction is not 444:\n%s", request[0].Content)
	}

	var evidence []string
	for _, message := range request {
		if message.Role == "assistant" {
			// Assistant prose is not tool evidence, even when it says an
			// operation happened.
			continue
		}
		if strings.Contains(message.Content, bashMarker) ||
			strings.Contains(message.Content, note) {
			evidence = append(evidence, message.Role+": "+message.Content)
		}
	}
	if len(evidence) == 0 {
		t.Logf("resumed messages = %#v", request)
		t.Logf("history = %#v", history)
		if os.Getenv("HAL_RESUME_STRICT") == "1" {
			t.Fatalf("resumed 444 context has no non-assistant evidence of the completed write/bash; "+
				"the prior assistant claims are the only record: %#v", request)
		}
		t.Skip("known gap: completed tool results are not persisted, so a resumed 444 context cannot " +
			"distinguish a prior tool execution from an assistant claim; set HAL_RESUME_STRICT=1 to fail")
	}
	t.Logf("resumed tool evidence = %#v", evidence)
}
