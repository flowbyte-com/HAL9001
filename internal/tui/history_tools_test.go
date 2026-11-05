package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/flowbyte-com/HAL9001/internal/engine"
	"github.com/flowbyte-com/HAL9001/internal/session"
)

func intPointer(n int) *int { return &n }

func historicalLines(withTools bool) []SessionLine {
	lines := []SessionLine{
		{Role: "user", Text: "please do both", TurnID: "turn-1"},
	}
	if withTools {
		lines = append(lines, SessionLine{
			Role: "assistant", Text: "both completed", TurnID: "turn-1",
			Tools: []session.ToolExecution{
				{
					TurnID: "turn-1", CallID: "call-write", Tool: "write",
					Action: "write", Target: "/tmp/note.txt", Mode: "666",
					Status: session.ToolSuccess, DurationMS: 12, Bytes: 13,
					Summary: "13 bytes",
				},
				{
					TurnID: "turn-1", CallID: "call-bash", Tool: "bash",
					Action: "bash", Mode: "666", Status: session.ToolFailure,
					ExitCode: intPointer(17), DurationMS: 40, Summary: "exit 17",
				},
			},
		})
	} else {
		lines = append(lines, SessionLine{
			Role: "assistant", Text: "both completed", TurnID: "turn-1",
		})
	}
	return lines
}

func TestSessionSwitchRebindsHistoricalTools(t *testing.T) {
	hooks := Hooks{
		ListSessions: func() ([]SessionInfo, error) {
			return []SessionInfo{{ID: "chat", Title: "Chat"}}, nil
		},
		SwitchSession: func(name string) (Engine, []SessionLine, error) {
			return &scriptEngine{}, historicalLines(true), nil
		},
	}
	m := NewWithHooks(&scriptEngine{}, hooks)
	m.SetColor(false)
	m.SetSessionID("old")
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = updated.(Model)
	m.lastTools = []engine.ToolActivity{{Name: "stale", Status: "ok"}}

	m = typeText(m, "/sessions")
	m = enterCurrent(m)
	m = pressKey(m, tea.KeyEnter, 0, "")
	if m.sessionID != "chat" {
		t.Fatalf("session id = %q", m.sessionID)
	}
	if len(m.lastTools) != 2 || m.lastTools[0].Name != "write" ||
		m.lastTools[1].Name != "bash" {
		t.Fatalf("switch did not rebind historical tools: %#v", m.lastTools)
	}
	if len(m.entries) != 2 || len(m.entries[1].tools) != 2 {
		t.Fatalf("switch did not attach historical evidence: %#v", m.entries)
	}
}

func TestSetHistoryRendersVerifiedToolEvidenceOnce(t *testing.T) {
	m := permissionModel()
	m.lastTools = []engine.ToolActivity{{Name: "stale", Status: "ok"}}
	m.SetHistory(historicalLines(true))

	if len(m.entries) != 2 {
		t.Fatalf("entries = %d, want user + assistant: %#v", len(m.entries), m.entries)
	}
	if m.entries[1].note || m.entries[1].role != "assistant" ||
		m.entries[1].turnID != "turn-1" || len(m.entries[1].tools) != 2 {
		t.Errorf("assistant entry = %#v", m.entries[1])
	}
	if len(m.lastTools) != 2 {
		t.Fatalf("lastTools = %#v", m.lastTools)
	}
	if !m.lastTools[0].Historical || m.lastTools[0].Name != "write" ||
		!m.lastTools[1].Historical || m.lastTools[1].Name != "bash" {
		t.Errorf("rebound lastTools = %#v", m.lastTools)
	}

	rowsBefore := append([]string(nil), m.entries[1].rows...)
	// Replaying the same history must replace, not append, the activity.
	m.SetHistory(historicalLines(true))
	if len(m.entries) != 2 || len(m.entries[1].tools) != 2 ||
		len(m.entries[1].rows) != len(rowsBefore) {
		t.Fatalf("history replay duplicated activity: entries=%d rows=%d/%d",
			len(m.entries), len(m.entries[1].rows), len(rowsBefore))
	}

	rows := strings.Join(m.diagRows(), "\n")
	for _, want := range []string{"last tools", "⚙ write", "⚙ bash", "exit 17", "666"} {
		if !strings.Contains(rows, want) {
			t.Errorf("/diag is missing %q:\n%s", want, rows)
		}
	}
}

func TestHistoricalEvidenceStaysBeneathItsAssistantAnswer(t *testing.T) {
	m := permissionModel()
	m.SetHistory(historicalLines(true))
	plain := stripANSI(strings.Join(m.layoutRows(), "\n"))
	answer := strings.Index(plain, "both completed")
	write := strings.Index(plain, "⚙ write")
	bash := strings.Index(plain, "⚙ bash")
	if answer < 0 || write < 0 || bash < 0 || write < answer || bash < answer {
		t.Fatalf("tool evidence is not attached beneath the answer:\n%s", plain)
	}
	if strings.Count(plain, "⚙ write") != 1 ||
		strings.Count(plain, "⚙ bash") != 1 {
		t.Fatalf("tool activity was duplicated:\n%s", plain)
	}
	eye := strings.Index(plain, "◉")
	if eye < 0 || eye > answer {
		t.Fatalf("the assistant eye is missing or displaced:\n%s", plain)
	}
	if strings.Count(plain, "◉") != 1 {
		t.Fatalf("an assistant response must carry exactly one eye:\n%s", plain)
	}
}

func TestSetHistoryDoesNotShowOlderToolsAsLastTools(t *testing.T) {
	m := permissionModel()
	lines := historicalLines(true)
	lines = append(lines,
		SessionLine{Role: "user", Text: "and then?", TurnID: "turn-2"},
		SessionLine{Role: "assistant", Text: "nothing else", TurnID: "turn-2"},
	)
	m.SetHistory(lines)
	if len(m.lastTools) != 0 {
		t.Fatalf("the latest assistant turn used no tools, but lastTools = %#v", m.lastTools)
	}
	plain := stripANSI(strings.Join(m.layoutRows(), "\n"))
	if strings.Count(plain, "⚙ write") != 1 {
		t.Fatalf("older evidence was lost or duplicated:\n%s", plain)
	}
}

func TestSetHistoryWithoutToolsClearsLastTools(t *testing.T) {
	m := permissionModel()
	m.lastTools = []engine.ToolActivity{{Name: "bash", Status: "ok"}}
	m.SetHistory(historicalLines(false))
	if len(m.lastTools) != 0 {
		t.Fatalf("lastTools leaked across history: %#v", m.lastTools)
	}
	if strings.Contains(stripANSI(strings.Join(m.layoutRows(), "\n")), "⚙ ") {
		t.Fatal("a tool note appeared for a turn without evidence")
	}
}
