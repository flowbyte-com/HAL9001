package engine

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/flowbyte-com/HAL9001/internal/agenttools"
	"github.com/flowbyte-com/HAL9001/internal/permission"
	"github.com/flowbyte-com/HAL9001/internal/provider"
	"github.com/flowbyte-com/HAL9001/internal/session"
)

// TestExpectedFailureMetadataReachesActivityAndHistory checks the two
// halves of the contract: the live activity says the failure was expected
// only when the call declared it, and the durable ledger retains the
// verified expectation for the next session.
func TestExpectedFailureMetadataReachesActivityAndHistory(t *testing.T) {
	st, _ := session.Open(t.TempDir(), "chat")
	perm := permission.New()
	perm.SetSession("chat")
	if err := perm.Enable("chat"); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(t.TempDir(), "missing.txt")
	p := &stepProvider{responses: []provider.Response{
		{
			Content: "checking.",
			ToolCalls: []provider.ToolCall{
				{
					ID: "call_missing", Name: "read",
					Arguments: map[string]any{
						"path": missing, "expect_missing": true,
					},
				},
				{
					ID: "call_expected_exit", Name: "bash",
					Arguments: map[string]any{
						"command": "exit 17", "expect_exit_code": 17,
					},
				},
				{
					ID: "call_unexpected_exit", Name: "bash",
					Arguments: map[string]any{
						"command": "exit 1", "expect_exit_code": 17,
					},
				},
			},
		},
		{Content: "done."},
	}}
	eng := New(st, p, provider.Model{ID: "m"}).
		WithPermission(perm).
		WithExecution(agenttools.New(perm, nil))
	var activities []ToolActivity
	if _, err := eng.RespondStreamEvents(context.Background(), "test expectations", func(ev Event) {
		if ev.Tool != nil {
			activities = append(activities, *ev.Tool)
		}
	}); err != nil {
		t.Fatal(err)
	}
	if len(activities) != 3 {
		t.Fatalf("activities = %#v", activities)
	}
	if activities[0].Expected != "missing" || !activities[0].ExpectedMet {
		t.Errorf("missing read activity = %#v", activities[0])
	}
	if activities[1].Expected != "exit:17" || !activities[1].ExpectedMet ||
		activities[1].ExitCode == nil || *activities[1].ExitCode != 17 {
		t.Errorf("expected bash activity = %#v", activities[1])
	}
	if activities[2].Expected != "exit:17" || activities[2].ExpectedMet {
		t.Errorf("unexpected bash activity = %#v", activities[2])
	}

	history, _ := st.History()
	if len(history) != 2 {
		t.Fatalf("history = %#v", history)
	}
	execs := history[1].ToolExecutions()
	if len(execs) != 3 {
		t.Fatalf("evidence = %#v", execs)
	}
	if execs[0].Expected != "missing" || !execs[0].ExpectedMet ||
		execs[1].Expected != "exit:17" || !execs[1].ExpectedMet ||
		execs[2].Expected != "exit:17" || execs[2].ExpectedMet {
		t.Errorf("evidence expectations = %#v", execs)
	}
	if execs[0].Status != session.ToolFailure ||
		execs[1].Status != session.ToolFailure ||
		execs[2].Status != session.ToolFailure {
		t.Errorf("actual failure status was changed: %#v", execs)
	}
}
