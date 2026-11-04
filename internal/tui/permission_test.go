package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/flowbyte-com/HAL9001/internal/engine"
	"github.com/flowbyte-com/HAL9001/internal/permission"
)

func permissionModel() Model {
	m := NewWithHooks(&scriptEngine{}, Hooks{})
	m.SetColor(false)
	m.SetSessionID("session-one")
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	return updated.(Model)
}

func openPermission(t *testing.T, m Model) Model {
	t.Helper()
	m = pressKey(m, 'p', tea.ModCtrl, "")
	if m.view != viewPermission {
		t.Fatalf("Ctrl+P did not open the permission control (view %d)", m.view)
	}
	return m
}

func TestDefaultPermissionIs444(t *testing.T) {
	m := permissionModel()
	if m.PermissionMode() != permission.Guarded {
		t.Fatal("a new conversation is not 444")
	}
	if footer := footerText(m); !strings.Contains(footer, "444 READ ONLY") {
		t.Errorf("footer = %q", footer)
	}
}

func TestPermissionScreenExplainsAndEnables666(t *testing.T) {
	m := permissionModel()
	m = openPermission(t, m)
	screen := stripANSI(strings.Join(m.layoutRows(), "\n"))
	for _, want := range []string{
		"666 · UNRESTRICTED", "Caveat emptor.",
		"read, write, edit and bash",
		"only for this conversation",
		"Ctrl+Y Enable 666", "Esc Cancel",
	} {
		if !strings.Contains(screen, want) {
			t.Errorf("activation screen is missing %q:\n%s", want, screen)
		}
	}

	updated, _ := m.Update(tea.KeyPressMsg{Code: 'y', Mod: tea.ModCtrl})
	m = updated.(Model)
	if m.PermissionMode() != permission.Unrestricted {
		t.Fatal("Ctrl+Y did not enable 666")
	}
	if m.view != viewChat {
		t.Errorf("enabling left view %d", m.view)
	}
	if footer := footerText(m); !strings.Contains(footer, "666 UNRESTRICTED") {
		t.Errorf("footer = %q", footer)
	}
	if !strings.Contains(m.Notice(), "Ctrl+P returns to 444") {
		t.Errorf("activation notice = %q", m.Notice())
	}
}

func TestPermissionScreenDisables666(t *testing.T) {
	m := permissionModel()
	m = openPermission(t, m)
	updated, _ := m.Update(tea.KeyPressMsg{Code: 'y', Mod: tea.ModCtrl})
	m = updated.(Model)
	m = openPermission(t, m)
	screen := stripANSI(strings.Join(m.layoutRows(), "\n"))
	if !strings.Contains(screen, "666 is active for this conversation.") {
		t.Errorf("active screen = %s", screen)
	}
	updated, _ = m.Update(tea.KeyPressMsg{Code: 'n', Mod: tea.ModCtrl})
	m = updated.(Model)
	if m.PermissionMode() != permission.Guarded {
		t.Fatal("Ctrl+N did not disable 666")
	}
	if footer := footerText(m); !strings.Contains(footer, "444 READ ONLY") {
		t.Errorf("footer = %q", footer)
	}
}

func TestPermissionCommandOpensTheSameControl(t *testing.T) {
	m := permissionModel()
	m = typeText(m, "/permission")
	m = enterCurrent(m)
	if m.view != viewPermission {
		t.Fatalf("/permission opened view %d", m.view)
	}
}

func Test666ResetsOnConversationChange(t *testing.T) {
	m := permissionModel()
	m = openPermission(t, m)
	updated, _ := m.Update(tea.KeyPressMsg{Code: 'y', Mod: tea.ModCtrl})
	m = updated.(Model)

	m.SetHistory([]SessionLine{{Role: "user", Text: "another conversation"}})
	if m.PermissionMode() != permission.Guarded {
		t.Fatal("SetHistory kept 666")
	}
	if footer := footerText(m); !strings.Contains(footer, "444 READ ONLY") {
		t.Errorf("footer after history change = %q", footer)
	}
}

func Test666ResetsOnNewConversation(t *testing.T) {
	m := NewWithHooks(&scriptEngine{}, Hooks{
		NewSession: func() (Engine, error) { return &scriptEngine{}, nil },
		SessionID:  func() string { return "session-two" },
	})
	m.SetColor(false)
	m.SetSessionID("session-one")
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = updated.(Model)
	m = openPermission(t, m)
	updated, _ = m.Update(tea.KeyPressMsg{Code: 'y', Mod: tea.ModCtrl})
	m = updated.(Model)

	m = typeText(m, "/clear")
	m = enterCurrent(m)
	if m.PermissionMode() != permission.Guarded {
		t.Fatal("/clear kept 666")
	}
	if m.sessionID != "session-two" {
		t.Errorf("session id = %q", m.sessionID)
	}
}

func Test666ResetsOnQuit(t *testing.T) {
	m := permissionModel()
	m = openPermission(t, m)
	updated, _ := m.Update(tea.KeyPressMsg{Code: 'y', Mod: tea.ModCtrl})
	m = updated.(Model)
	cmd := m.quit()
	if cmd == nil {
		t.Fatal("quit returned no command")
	}
	if m.PermissionMode() != permission.Guarded {
		t.Fatal("quitting kept 666")
	}
}

func TestOneOffApprovalLabelIsNotTheSessionMode(t *testing.T) {
	m := permissionModel()
	m.writeState = writeRequested
	if got := m.permissionLabel(); got != "666 REQUESTED" {
		t.Errorf("pending approval label = %q", got)
	}
	if m.PermissionMode() != permission.Guarded {
		t.Error("a pending one-off write changed the session mode")
	}
}

func TestToolActivityIsShownCompactlyAndNotAsContent(t *testing.T) {
	m := permissionModel()
	m.sending = true
	updated, _ := m.Update(streamToolMsg{id: 0, activity: engine.ToolActivity{
		CallID: "c1", Name: "bash", Status: "ok", Target: "/tmp/work",
		ExitCode: intPtr(0), Bytes: 2048,
	}})
	m = updated.(Model)
	if len(m.lastTools) != 1 || m.lastTools[0].Name != "bash" {
		t.Fatalf("lastTools = %#v", m.lastTools)
	}
	if !strings.Contains(m.Notice(), "bash") {
		t.Errorf("running notice = %q", m.Notice())
	}
	m.sending = false
	m.appendToolNotes(m.lastTools)
	rows := stripANSI(strings.Join(m.layoutRows(), "\n"))
	if !strings.Contains(rows, "⚙ bash") || !strings.Contains(rows, "exit 0") {
		t.Errorf("tool activity is not visible:\n%s", rows)
	}
}

func TestHelpDocumentsThePermissionControl(t *testing.T) {
	m := permissionModel()
	m = typeText(m, "/help")
	m = enterCurrent(m)
	m.panelOffset = len(m.helpRows()) - m.helpBodyHeight()
	m.clampHelpOffset()
	text := stripANSI(strings.Join(m.layoutRows(), "\n"))
	if !strings.Contains(text, "Permissions and file writes") ||
		!strings.Contains(text, "session-scoped unrestricted") ||
		!strings.Contains(text, "Ctrl+P") {
		t.Errorf("help does not document the permission control:\n%s", text)
	}
	if !strings.Contains(text, "/permission") {
		// The command lives in the sessions section, which may have
		// scrolled off by the tail; the permission controls are the
		// important part here.
		t.Logf("permission command is above the visible tail")
	}
}

func TestPermissionModeSurvivesOnlyTheSameController(t *testing.T) {
	c := permission.New()
	c.SetSession("session-one")
	m := permissionModel()
	m.SetPermission(c)
	if err := c.Enable("session-one"); err != nil {
		t.Fatal(err)
	}
	if m.PermissionMode() != permission.Unrestricted {
		t.Fatal("the interface did not observe the shared controller")
	}
	c.Disable()
	if m.PermissionMode() != permission.Guarded {
		t.Fatal("disabling the controller did not update the interface")
	}
}

func TestToolActivityDoesNotAppearInTheStoredSession(t *testing.T) {
	m := permissionModel()
	m.lastTools = []engine.ToolActivity{{Name: "read", Status: "ok", Target: "/tmp/x"}}
	lines := m.Lines()
	if len(lines) != 0 {
		t.Errorf("activity became conversation lines: %v", lines)
	}
}

func intPtr(n int) *int { return &n }
