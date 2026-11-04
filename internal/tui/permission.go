package tui

import (
	"os"
	"os/user"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/flowbyte-com/HAL9001/internal/permission"
)

// The permission overlay is the only place 666 can be enabled. It is opened
// by a local command or Ctrl+P, and only a deliberate chord in this overlay
// changes the gate. Conversation text, a model answer, a skill, an MPM record
// or a tool result cannot reach it.
//
// The screen is shown once, with the caveat and the scope of the authority.
// Ordinary use does not repeat theatrical warnings; the persistent red 666
// indicator is the reminder.

// enterPermission opens the operator's permission control.
func (m *Model) enterPermission() {
	m.closeSuggestions()
	m.panelOffset = 0
	m.permissionNotice = ""
	m.view = viewPermission
}

// permissionRows renders the activation screen or the active control.
func (m Model) permissionRows() []string {
	st := m.theme.styles()
	rows := []string{st.danger.Render("666 · UNRESTRICTED"), "Caveat emptor.", ""}
	if m.PermissionMode() == permission.Unrestricted {
		rows = append(rows,
			"666 is active for this conversation.",
			"",
			"HAL's read, write, edit and bash tools run as "+osUser()+" with that user's OS permissions.",
			"Disabling 666 immediately cancels active privileged work.",
			"MPM knowledge tools and the one-off guarded write remain available in 444.",
			"",
			"Use 666 only when you have asked HAL to inspect, edit or run something.",
			"",
			m.approvalRow(keyName(keyRejectWrite), " Disable 666", "Esc", " Back"),
		)
	} else {
		rows = append(rows,
			"Enabling 666 lets HAL use read, write, edit and bash as "+osUser()+".",
			"Commands and file operations use that user's OS permissions and are not",
			"confined to the HAL workspace.",
			"",
			"666 lasts only for this conversation. It returns to 444 when you disable it,",
			"start, switch or resume a conversation, clear the conversation, or quit HAL.",
			"Normal conversation, MPM knowledge tools and the one-off guarded write remain",
			"available in 444.",
			"",
			m.approvalRow(keyName(keyApproveWrite), " Enable 666", "Esc", " Cancel"),
		)
	}
	if m.permissionNotice != "" {
		rows = append(rows, "", m.permissionNotice)
	}
	return rows
}

func osUser() string {
	if u, err := user.Current(); err == nil && strings.TrimSpace(u.Username) != "" {
		return u.Username
	}
	if name := strings.TrimSpace(envUser()); name != "" {
		return name
	}
	return "the operating-system user"
}

func envUser() string {
	// Kept in a tiny helper so the overlay has one source for the display
	// name and tests can see the fallback text.
	return strings.TrimSpace(os.Getenv("USER"))
}

// updatePermission handles the activation screen. Only the two deliberate
// chords change the gate; every other key is ignored on purpose.
func (m Model) updatePermission(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.ready = true
		return m, nil
	case tea.KeyPressMsg:
		switch msg.String() {
		case keyQuit, keyQuit2:
			return m, m.quit()
		case "esc":
			m.view = viewChat
			m.permissionNotice = ""
			return m, nil
		case keyApproveWrite:
			if m.PermissionMode() == permission.Unrestricted {
				return m, nil
			}
			return m.enablePermission()
		case keyRejectWrite:
			if m.PermissionMode() != permission.Unrestricted {
				m.view = viewChat
				m.permissionNotice = ""
				return m, nil
			}
			return m.disablePermission()
		}
	}
	return m, nil
}

func (m Model) enablePermission() (tea.Model, tea.Cmd) {
	if m.permission == nil {
		m.permissionNotice = "The permission control is not wired up."
		return m, nil
	}
	if m.sessionID != "" {
		m.permission.SetSession(m.sessionID)
	}
	if err := m.permission.Enable(m.sessionID); err != nil {
		m.permissionNotice = "666 was not enabled: " + err.Error()
		return m, nil
	}
	m.view = viewChat
	m.permissionNotice = ""
	m.notice = "666 UNRESTRICTED · commands run as " + osUser() + " · Ctrl+P returns to 444"
	return m, nil
}

func (m Model) disablePermission() (tea.Model, tea.Cmd) {
	if m.permission != nil {
		m.permission.Disable()
	}
	m.view = viewChat
	m.permissionNotice = ""
	m.notice = "444 READ ONLY · 666 disabled"
	return m, nil
}
