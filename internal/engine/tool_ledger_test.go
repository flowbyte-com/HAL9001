package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flowbyte-com/HAL9001/internal/agenttools"
	"github.com/flowbyte-com/HAL9001/internal/permission"
	"github.com/flowbyte-com/HAL9001/internal/provider"
	"github.com/flowbyte-com/HAL9001/internal/session"
)

func enabledLedgerEngine(t *testing.T, dir string, p provider.Provider) (*Engine, *permission.Controller) {
	t.Helper()
	st, err := session.Open(dir, "chat")
	if err != nil {
		t.Fatal(err)
	}
	perm := permission.New()
	perm.SetSession("chat")
	if err := perm.Enable("chat"); err != nil {
		t.Fatal(err)
	}
	return New(st, p, provider.Model{ID: "m"}).
		WithSystemPrompt("You are HAL9001.").
		WithPermission(perm).
		WithExecution(agenttools.New(perm, nil)), perm
}

func resumeLedgerEngine(t *testing.T, dir string, perm *permission.Controller) (*Engine, *stepProvider, []provider.Message) {
	t.Helper()
	st, err := session.Open(dir, "chat")
	if err != nil {
		t.Fatal(err)
	}
	spy := &stepProvider{responses: []provider.Response{{Content: "Acknowledged."}}}
	eng := New(st, spy, provider.Model{ID: "m"}).
		WithSystemPrompt("You are HAL9001.").
		WithPermission(perm).
		WithExecution(agenttools.New(perm, nil))
	if _, err := eng.RespondStreamEvents(context.Background(), "what happened?", nil); err != nil {
		t.Fatal(err)
	}
	if len(spy.requests) == 0 {
		t.Fatal("no resumed request captured")
	}
	return eng, spy, spy.requests[0]
}

func TestResumeBashFailureKeepsExitCodeAndStatus(t *testing.T) {
	dir := t.TempDir()
	p := &stepProvider{responses: []provider.Response{
		{Content: "Running it.", ToolCalls: []provider.ToolCall{{
			ID: "bash-fail", Name: "bash",
			Arguments: map[string]any{"command": "exit 17"},
		}}},
		{Content: "The command failed."},
	}}
	eng, perm := enabledLedgerEngine(t, dir, p)
	if _, err := eng.RespondStreamEvents(context.Background(), "run the failing command", nil); err != nil {
		t.Fatal(err)
	}
	st, _ := session.Open(dir, "chat")
	history, _ := st.History()
	execs := history[1].ToolExecutions()
	if len(execs) != 1 || execs[0].Status != session.ToolFailure ||
		execs[0].ExitCode == nil || *execs[0].ExitCode != 17 ||
		execs[0].Mode != "666" {
		t.Fatalf("failure evidence = %#v", execs)
	}

	perm.Disable()
	_, _, request := resumeLedgerEngine(t, dir, perm)
	system := request[0].Content
	if !strings.Contains(system, "444 READ ONLY") ||
		!strings.Contains(system, "Verified historical tool evidence") ||
		!strings.Contains(system, "bash") ||
		!strings.Contains(system, "failure") ||
		!strings.Contains(system, "exit 17") ||
		!strings.Contains(system, "mode 666") {
		t.Fatalf("failure evidence missing from resume:\n%s", system)
	}
}

func TestResumeCancelledToolKeepsStatus(t *testing.T) {
	dir := t.TempDir()
	p := &stepProvider{responses: []provider.Response{
		{Content: "Starting a long command.", ToolCalls: []provider.ToolCall{{
			ID: "bash-cancel", Name: "bash",
			Arguments: map[string]any{"command": "sleep 10", "timeout_seconds": 30},
		}}},
		{Content: "The command was cancelled."},
	}}
	eng, perm := enabledLedgerEngine(t, dir, p)
	done := make(chan error, 1)
	go func() {
		_, err := eng.RespondStreamEvents(context.Background(), "run the long command", nil)
		done <- err
	}()
	deadline := time.Now().Add(3 * time.Second)
	for perm.Active() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if perm.Active() == 0 {
		t.Fatal("the command never registered as active")
	}
	perm.Disable()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the turn did not finish after cancellation")
	}

	st, _ := session.Open(dir, "chat")
	history, _ := st.History()
	execs := history[1].ToolExecutions()
	if len(execs) != 1 || execs[0].Status != session.ToolCancelled ||
		execs[0].Mode != "666" || execs[0].Tool != "bash" {
		t.Fatalf("cancelled evidence = %#v", execs)
	}
	_, _, request := resumeLedgerEngine(t, dir, perm)
	system := request[0].Content
	if !strings.Contains(system, "cancelled") || !strings.Contains(system, "mode 666") {
		t.Fatalf("cancelled evidence missing from resume:\n%s", system)
	}
	if !strings.Contains(system, "444 READ ONLY") {
		t.Fatalf("resume did not return to 444:\n%s", system)
	}
}

func TestMultiToolTurnKeepsCallAssociations(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "multi.txt")
	p := &stepProvider{responses: []provider.Response{
		{Content: "Doing both.", ToolCalls: []provider.ToolCall{
			{ID: "call-write", Name: "write",
				Arguments: map[string]any{"path": path, "content": "multi\n"}},
			{ID: "call-bash", Name: "bash",
				Arguments: map[string]any{"command": "printf MULTI"}},
		}},
		{Content: "Both completed."},
	}}
	eng, perm := enabledLedgerEngine(t, dir, p)
	if _, err := eng.RespondStreamEvents(context.Background(), "do both", nil); err != nil {
		t.Fatal(err)
	}
	st, _ := session.Open(dir, "chat")
	history, _ := st.History()
	execs := history[1].ToolExecutions()
	if len(execs) != 2 {
		t.Fatalf("executions = %#v", execs)
	}
	if execs[0].TurnID != history[1].TurnID || execs[1].TurnID != history[1].TurnID {
		t.Fatalf("turn association lost: %#v", execs)
	}
	if execs[0].CallID != "call-write" || execs[0].Tool != "write" ||
		execs[1].CallID != "call-bash" || execs[1].Tool != "bash" {
		t.Fatalf("call association lost: %#v", execs)
	}
	if string(mustRead(t, path)) != "multi\n" {
		t.Fatal("the write did not happen")
	}

	perm.Disable()
	_, _, request := resumeLedgerEngine(t, dir, perm)
	system := request[0].Content
	for _, want := range []string{"turn " + history[1].TurnID, "call call-write", "call call-bash", "write", "bash"} {
		if !strings.Contains(system, want) {
			t.Errorf("multi-tool evidence missing %q:\n%s", want, system)
		}
	}
}

func TestRefusedToolIsRecordedAsRefused(t *testing.T) {
	dir := t.TempDir()
	perm := permission.New()
	perm.SetSession("chat")
	p := &stepProvider{responses: []provider.Response{{
		Content: "I will run it.",
		ToolCalls: []provider.ToolCall{{
			ID: "bash-refused", Name: "bash",
			Arguments: map[string]any{"command": "touch forbidden"},
		}},
	}}}
	st, _ := session.Open(dir, "chat")
	eng := New(st, p, provider.Model{ID: "m"}).
		WithSystemPrompt("You are HAL9001.").
		WithPermission(perm).
		WithExecution(agenttools.New(perm, nil))
	if _, err := eng.RespondStreamEvents(context.Background(), "run it", nil); err != nil {
		t.Fatal(err)
	}
	history, _ := st.History()
	execs := history[1].ToolExecutions()
	if len(execs) != 1 || execs[0].Status != session.ToolRefused ||
		execs[0].Mode != "444" || execs[0].Tool != "bash" {
		t.Fatalf("refusal evidence = %#v", execs)
	}
	_, _, request := resumeLedgerEngine(t, dir, perm)
	system := request[0].Content
	if !strings.Contains(system, "refused") || !strings.Contains(system, "mode 444") {
		t.Fatalf("refusal evidence missing:\n%s", system)
	}
}

func TestHistoricalEvidenceIsNotReplayed(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "replay-marker")
	p := &stepProvider{responses: []provider.Response{
		{Content: "Touching the marker.", ToolCalls: []provider.ToolCall{{
			ID: "bash-touch", Name: "bash",
			Arguments: map[string]any{"command": "touch " + marker},
		}}},
		{Content: "The marker was created."},
	}}
	eng, perm := enabledLedgerEngine(t, dir, p)
	if _, err := eng.RespondStreamEvents(context.Background(), "create the marker", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("the original execution did not happen: %v", err)
	}
	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}
	perm.Disable()
	_, _, request := resumeLedgerEngine(t, dir, perm)
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("resume replayed the historical command: %v", err)
	}
	if !strings.Contains(request[0].Content, "bash") {
		t.Fatal("the historical evidence is missing from the resumed request")
	}
	for _, message := range request {
		if message.Role == "tool" || len(message.ToolCalls) > 0 {
			t.Fatalf("resume manufactured a provider call: %#v", message)
		}
	}
}

func TestOlderSessionWithoutLedgerResumesUncertain(t *testing.T) {
	dir := t.TempDir()
	st, err := session.Open(dir, "chat")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AppendTurn(session.RoleUser, "hello", session.Turn{ID: "old-turn"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AppendTurn(session.RoleAssistant, "I ran it successfully earlier.", session.Turn{ID: "old-turn"}); err != nil {
		t.Fatal(err)
	}
	perm := permission.New()
	perm.SetSession("chat")
	_, _, request := resumeLedgerEngine(t, dir, perm)
	system := request[0].Content
	if strings.Contains(system, "Verified historical tool evidence") {
		t.Fatalf("an older session grew evidence:\n%s", system)
	}
	if !strings.Contains(system, "do not call it fabricated") {
		t.Fatalf("the uncertainty instruction is missing:\n%s", system)
	}
	if !strings.Contains(system, "444 READ ONLY") {
		t.Fatalf("resume did not start in 444:\n%s", system)
	}
}

func TestHistoricalModeDoesNotEnableTools(t *testing.T) {
	dir := t.TempDir()
	p := &stepProvider{responses: []provider.Response{
		{Content: "Touching once.", ToolCalls: []provider.ToolCall{{
			ID: "bash-once", Name: "bash", Arguments: map[string]any{"command": "true"},
		}}},
		{Content: "Done."},
	}}
	eng, perm := enabledLedgerEngine(t, dir, p)
	if _, err := eng.RespondStreamEvents(context.Background(), "run once", nil); err != nil {
		t.Fatal(err)
	}
	perm.Disable()
	resumed, _, request := resumeLedgerEngine(t, dir, perm)
	if perm.Mode() != permission.Guarded {
		t.Fatalf("resumed mode = %s", perm.Mode())
	}
	for _, name := range []string{"read", "write", "edit", "bash"} {
		if hasTool(resumed.offeredTools(), name) {
			t.Errorf("historical evidence restored %q", name)
		}
	}
	if !strings.Contains(request[0].Content, "444 READ ONLY") {
		t.Fatal("the current permission is not 444")
	}
}

func TestFreshControllerOnResumeStarts444(t *testing.T) {
	dir := t.TempDir()
	p := &stepProvider{responses: []provider.Response{
		{Content: "Touching once.", ToolCalls: []provider.ToolCall{{
			ID: "bash-once", Name: "bash", Arguments: map[string]any{"command": "true"},
		}}},
		{Content: "Done."},
	}}
	eng, perm := enabledLedgerEngine(t, dir, p)
	if _, err := eng.RespondStreamEvents(context.Background(), "run once", nil); err != nil {
		t.Fatal(err)
	}
	perm.Disable()

	// A fresh process has a fresh controller: historical 666 evidence must
	// not make it anything but 444.
	fresh := permission.New()
	fresh.SetSession("chat")
	resumed, _, request := resumeLedgerEngine(t, dir, fresh)
	if fresh.Mode() != permission.Guarded {
		t.Fatalf("fresh resume mode = %s", fresh.Mode())
	}
	if !strings.Contains(request[0].Content, "444 READ ONLY") {
		t.Fatal("fresh resume is not 444")
	}
	for _, name := range []string{"read", "write", "edit", "bash"} {
		if hasTool(resumed.offeredTools(), name) {
			t.Errorf("fresh resume offered %q from history", name)
		}
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
