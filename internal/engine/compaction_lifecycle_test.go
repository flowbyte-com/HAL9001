package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flowbyte-com/HAL9001/internal/agenttools"
	"github.com/flowbyte-com/HAL9001/internal/permission"
	"github.com/flowbyte-com/HAL9001/internal/provider"
	"github.com/flowbyte-com/HAL9001/internal/session"
)

// The lifecycle and the safety of a compaction.
//
// A compaction is an operation like any other: it can be stopped, it
// stores nothing until it is whole, and it does not disturb the work
// that is already in flight. These tests cover the four promises that
// matter to the operator: a stopped compaction changes nothing, an
// interrupted exchange stays resumable, a continuation never replays a
// consequential tool, and the permission mode is untouched.

func TestTheCompactionLeavesAnInterruptedExchangeResumable(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "file.txt")
	if err := os.WriteFile(path, []byte("data\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	st, err := session.Open(t.TempDir(), "chat")
	if err != nil {
		t.Fatal(err)
	}
	// A conversation with something worth compacting.
	compactConversation(t, st, 4, 40)

	perm := permission.New()
	perm.SetSession("chat")
	if err := perm.Enable("chat"); err != nil {
		t.Fatal(err)
	}
	p := &scriptedProvider{steps: []provider.Response{
		{
			Content: "I'll look.",
			ToolCalls: []provider.ToolCall{{
				ID: "call_read", Name: "read",
				Arguments: map[string]any{"path": path},
			}},
		},
	}}
	eng := New(st, p, provider.Model{ID: "m", Budget: compactBudget(4000)}).
		WithPermission(perm).
		WithExecution(agenttools.New(perm, nil))
	ctx, cancel := context.WithCancel(context.Background())
	p.before = func(step int) {
		if step == 2 {
			// The read has run; the exchange is interrupted now.
			cancel()
		}
	}
	ran := 0
	if _, err := eng.RespondStreamEvents(ctx, "read the file", func(ev Event) {
		if ev.Tool != nil && ev.Tool.Status == "ok" {
			ran++
		}
	}); err == nil {
		t.Fatal("the exchange must be interrupted")
	}
	if ran != 1 {
		t.Fatalf("the read ran %d time(s), want once", ran)
	}
	if !eng.Lifecycle().Resumable {
		t.Fatal("the interrupted exchange must be continuable")
	}

	// The compaction happens. It must not disturb the safe point.
	p.before = nil
	p.steps = []provider.Response{{Content: "## Objective\nread the file"}}
	if _, err := eng.Compact(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if !eng.Lifecycle().Resumable {
		t.Fatal("the compaction must leave the interrupted exchange continuable")
	}

	// The continuation proposes the very same read again.
	p.steps = []provider.Response{
		{
			Content: "Reading it again.",
			ToolCalls: []provider.ToolCall{{
				ID: "call_read_2", Name: "read",
				Arguments: map[string]any{"path": path},
			}},
		},
		{Content: "the file says data"},
	}
	ran = 0
	if _, err := eng.Resume(context.Background(), func(ev Event) {
		if ev.Tool != nil && ev.Tool.Status == "ok" {
			ran++
		}
	}); err != nil {
		t.Fatalf("resume: %v", err)
	}
	if ran != 0 {
		t.Fatalf("the read ran %d more time(s); it must not run again", ran)
	}
	// The model is told that the operation is already done.
	found := false
	for _, request := range p.requests {
		for _, message := range request {
			if message.Role == "tool" && strings.Contains(message.Content, "already_done") {
				found = true
			}
		}
	}
	if !found {
		t.Error("the model was not told that the operation had already run")
	}
}

func TestTheCompactionDoesNotChangeThePermissionMode(t *testing.T) {
	st, err := session.Open(t.TempDir(), "chat")
	if err != nil {
		t.Fatal(err)
	}
	compactConversation(t, st, 5, 40)
	perm := permission.New()
	perm.SetSession("chat")
	if err := perm.Enable("chat"); err != nil {
		t.Fatal(err)
	}
	spy := &compactSpy{replies: []string{"## Objective\nship the parser"}}
	eng := compactEngine(t, st, spy, compactBudget(4000)).WithPermission(perm)
	if _, err := eng.Compact(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if perm.Mode() != permission.Unrestricted {
		t.Errorf("the permission mode = %v, want it unchanged", perm.Mode())
	}
}

func TestTheCompactionNeverRunsAnOperation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "file.txt")
	if err := os.WriteFile(path, []byte("data\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	st, err := session.Open(t.TempDir(), "chat")
	if err != nil {
		t.Fatal(err)
	}
	compactConversation(t, st, 5, 40)
	perm := permission.New()
	perm.SetSession("chat")
	if err := perm.Enable("chat"); err != nil {
		t.Fatal(err)
	}
	spy := &compactSpy{replies: []string{"## Objective\nship the parser"}}
	eng := compactEngine(t, st, spy, compactBudget(4000)).
		WithPermission(perm).
		WithExecution(agenttools.New(perm, nil))
	if _, err := eng.Compact(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	// The compaction reads the conversation and nothing else: no
	// operation ran, and the file is untouched.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "data\n" {
		t.Errorf("the file changed: %q", data)
	}
	for _, request := range spy.requests {
		for _, m := range request {
			if m.Role == "tool" || m.Role == "assistant" {
				t.Errorf("a compaction request must not carry a tool exchange: %+v", m)
			}
		}
	}
}

func TestTheCompactionLeavesTheQueuedMessagesAlone(t *testing.T) {
	// The queue lives in the interface, and a compaction is not a turn:
	// it sends no operator message, so it cannot reorder what is
	// waiting. The engine's part of that promise is that it stores no
	// message of its own.
	st, err := session.Open(t.TempDir(), "chat")
	if err != nil {
		t.Fatal(err)
	}
	compactConversation(t, st, 5, 40)
	before, err := st.History()
	if err != nil {
		t.Fatal(err)
	}
	spy := &compactSpy{replies: []string{"## Objective\nship the parser"}}
	eng := compactEngine(t, st, spy, compactBudget(4000))
	if _, err := eng.Compact(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	after, err := st.History()
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Fatalf("the compaction stored %d message(s) of its own", len(after)-len(before))
	}
}

func TestTheCompactionSurvivesARestart(t *testing.T) {
	dir := t.TempDir()
	st, err := session.Open(dir, "chat")
	if err != nil {
		t.Fatal(err)
	}
	compactConversation(t, st, 5, 40)
	spy := &compactSpy{replies: []string{"## Objective\nship the parser"}}
	eng := compactEngine(t, st, spy, compactBudget(4000))
	if _, err := eng.Compact(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	// A new process reads the same conversation.
	again, err := session.Open(dir, "chat")
	if err != nil {
		t.Fatal(err)
	}
	record := again.ReadCompaction()
	if record == nil {
		t.Fatal("the compaction did not survive the restart")
	}
	if !strings.Contains(record.Summary, "ship the parser") {
		t.Errorf("the summary = %q", record.Summary)
	}
	// The request of the new process carries the summary.
	spy2 := &compactSpy{replies: []string{"an answer"}}
	eng2 := compactEngine(t, again, spy2, compactBudget(4000))
	if _, err := eng2.Respond(context.Background(), "next?"); err != nil {
		t.Fatal(err)
	}
	text := requestText(spy2.last(t))
	if !strings.Contains(text, "ship the parser") {
		t.Errorf("the request must carry the summary:\n%s", text)
	}
	if strings.Contains(text, "question 0:") {
		t.Errorf("the request must not carry the covered exchanges:\n%s", text)
	}
}

func TestAnInterruptedCompactionLeavesNoRecordBehind(t *testing.T) {
	dir := t.TempDir()
	st, err := session.Open(dir, "chat")
	if err != nil {
		t.Fatal(err)
	}
	// A conversation that needs more than one request, so the failure
	// lands in the middle of the compaction.
	compactConversation(t, st, 8, 4000)
	// The provider fails after the first stage: the compaction is
	// incomplete and must leave nothing behind.
	spy := &compactSpy{replies: []string{"a summary"}}
	failing := &failingCompactSpy{compactSpy: spy}
	eng := compactEngine(t, st, failing, compactBudget(4000))
	if _, err := eng.Compact(context.Background(), nil); err == nil {
		t.Fatal("the compaction must fail")
	}
	if record := st.ReadCompaction(); record != nil {
		t.Error("an incomplete compaction must not be committed")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), "compaction") {
			t.Errorf("an incomplete compaction left %q behind", e.Name())
		}
	}
}

// failingCompactSpy answers the first request and fails the rest.
type failingCompactSpy struct {
	*compactSpy
}

func (p *failingCompactSpy) Chat(ctx context.Context, messages []provider.Message, model provider.Model) (provider.Response, error) {
	if len(p.requests) > 0 {
		return provider.Response{}, context.DeadlineExceeded
	}
	return p.compactSpy.Chat(ctx, messages, model)
}
