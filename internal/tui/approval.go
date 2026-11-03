package tui

import (
	"errors"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/flowbyte-com/HAL9001/internal/workspace"
)

// The approval overlay is the only place a write can be authorised.
//
// It shows one immutable operation — the destination, whether it creates
// or replaces a file, the size, the digest and the full contents — and
// waits for a deliberate key. Nothing else can satisfy it: conversation
// text, a model answer, a paste or a tool call all leave the request
// pending until the operator decides. Esc, a session change and quitting
// all reject it.

// writeState is the permission state shown while a request is alive.
type writeState int

const (
	// writeIdle is HAL's normal state: 444, read-only.
	writeIdle writeState = iota
	// writeRequested means one operation is waiting for the operator.
	writeRequested
	// writeWriting means that operation is being performed right now.
	writeWriting
)

// writeResultMsg carries the outcome of the one approved operation.
type writeResultMsg struct {
	result workspace.Result
	err    error
}

// SetWorkspace attaches the trusted workspace store. Without one, a
// proposal can be reviewed but never approved: approval cannot write
// anything, and HAL stays 444.
func (m *Model) SetWorkspace(s *workspace.Store) { m.workspace = s }

// WriteState reports the permission state, for tests.
func (m Model) WriteState() writeState { return m.writeState }

// PendingWrite returns the proposal awaiting a decision, for tests.
func (m Model) PendingWrite() (workspace.Proposal, bool) { return m.write, m.writeOpen }

// requestWrite opens the approval view for one prepared proposal.
//
// Only one request can be pending. A second proposal is refused on the
// spot and recorded as superseded, so the operator always reviews the
// exact operation in front of them.
func (m *Model) requestWrite(p workspace.Proposal) {
	if m.writeOpen || m.writeState != writeIdle {
		m.recordRejection(p, "another approval was pending")
		m.notice = "A write approval is already pending — decide that one first."
		return
	}
	m.write = p
	m.writeOpen = true
	m.writeState = writeRequested
	m.panelOffset = 0
	m.view = viewApprove
	m.notice = ""
}

// approveWrite performs the reviewed operation exactly once.
//
// The write runs as a command so the interface can show 666 WRITING
// before it starts; the result comes back as a message and only then does
// the interface report a file. The request is closed before the write
// starts, so a second key press cannot repeat it — and the store itself
// refuses a replayed proposal.
func (m Model) approveWrite() (tea.Model, tea.Cmd) {
	p := m.write
	m.writeOpen = false
	m.view = viewChat
	if m.workspace == nil {
		m.writeState = writeIdle
		m.notice = "Workspace unavailable — nothing was saved."
		return m, nil
	}
	m.writeState = writeWriting
	m.notice = "666 WRITING · " + m.workspace.DisplayPath(p)
	ws := m.workspace
	return m, func() tea.Msg {
		res, err := ws.Approve(p)
		return writeResultMsg{result: res, err: err}
	}
}

// rejectPendingWrite declines the request in front of the operator.
func (m Model) rejectPendingWrite(reason string) (tea.Model, tea.Cmd) {
	p := m.write
	m.writeOpen = false
	m.write = workspace.Proposal{}
	m.writeState = writeIdle
	m.view = viewChat
	m.recordRejection(p, reason)
	m.notice = "Write rejected — nothing was saved."
	m.appendNote("assistant", "Write rejected — nothing was saved.")
	return m, nil
}

// cancelWrite drops a pending request without a decision: the session
// changed, the interface is quitting, or the request was superseded.
func (m *Model) cancelWrite(reason string) {
	if !m.writeOpen {
		// Nothing is pending. A write that is already running cannot be
		// undone; its result returns the interface to 444.
		return
	}
	if m.write.ID != "" {
		m.recordRejection(m.write, reason)
	}
	m.writeOpen = false
	m.write = workspace.Proposal{}
	m.writeState = writeIdle
	if m.view == viewApprove {
		m.view = viewChat
	}
}

// recordRejection writes the rejection to the audit trail. Bookkeeping is
// best effort: a failure must never make a rejection look like an
// approval.
func (m *Model) recordRejection(p workspace.Proposal, reason string) {
	if m.workspace == nil || p.ID == "" {
		return
	}
	_ = m.workspace.Reject(p, reason)
}

// updateWrite handles the outcome of the one approved operation.
func (m Model) updateWrite(msg writeResultMsg) (tea.Model, tea.Cmd) {
	m.writeState = writeIdle
	m.write = workspace.Proposal{}
	if msg.err != nil {
		reason := writeFailureReason(msg.err)
		m.notice = "Write failed — nothing was saved."
		m.appendNote("assistant", "I couldn't save the file: "+reason+". Nothing was written.")
		return m, nil
	}
	path := msg.result.Path
	if m.workspace != nil {
		path = m.workspace.DisplayPath(workspace.Proposal{Path: msg.result.Path})
	}
	verb := "Saved"
	if msg.result.Overwrite {
		verb = "Replaced"
	}
	m.notice = verb + " " + path + " · 444"
	m.appendNote("assistant", verb+" "+path+" ("+humanSize(msg.result.Bytes)+"). Back to 444 — nothing else was written.")
	return m, nil
}

// updateApprove handles the approval view. Only a deliberate key decides:
// everything else scrolls the review or is ignored.
func (m Model) updateApprove(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.ready = true
		return m, nil
	case tea.MouseWheelMsg:
		if msg.Button == tea.MouseWheelUp {
			m.scrollPanel(-3)
		} else {
			m.scrollPanel(3)
		}
		return m, nil
	case tea.KeyPressMsg:
		key := msg.String()
		switch key {
		case keyQuit, keyQuit2:
			return m, m.quit()
		case keyApproveWrite:
			return m.approveWrite()
		case keyRejectWrite, "esc":
			reason := "rejected by operator"
			if key == "esc" {
				reason = "dismissed with Esc"
			}
			return m.rejectPendingWrite(reason)
		case "up", "k":
			m.scrollPanel(-1)
		case "down", "j":
			m.scrollPanel(1)
		case "pgup":
			m.scrollPanel(-contentHeight(m.height))
		case "pgdown", "pgdn":
			m.scrollPanel(contentHeight(m.height))
		case "home":
			m.scrollPanel(-1 << 20)
		case "end":
			m.scrollPanel(1 << 20)
		}
		return m, nil
	}
	return m, nil
}

// approvalRows renders the review: destination, operation, size, digest,
// the full contents, and the two decisions.
func (m Model) approvalRows() []string {
	st := m.theme.styles()
	p := m.write
	rows := []string{
		"666 — one write needs your approval (Esc rejects)",
		"",
	}
	row := func(label, value string) {
		rows = append(rows, "  "+padTo(st.active.Render(label), 14)+value)
	}
	row("destination", m.displayWritePath(p))
	if p.Overwrite {
		row("operation", fmt.Sprintf("OVERWRITE — replaces the existing file (%s)", humanSize(p.Existing)))
	} else {
		row("operation", "create — the file does not exist yet")
	}
	row("size", humanSize(int64(p.Size())))
	row("digest", "sha256:"+shortDigest(p.Digest))
	row("turn", fmt.Sprintf("%s · turn %d", p.Session, p.Turn))
	rows = append(rows, "")
	rows = append(rows, "  "+st.footer.Render("proposed contents — nothing has been written"))
	width := m.width - 6
	if width < 10 {
		width = 10
	}
	for _, line := range strings.Split(string(p.Content), "\n") {
		for _, wrapped := range wrapRunes([]rune(line), width) {
			rows = append(rows, "  │ "+strings.TrimRight(string(wrapped), " "))
		}
	}
	rows = append(rows, "")
	approve, reject := keyName(keyApproveWrite), keyName(keyRejectWrite)
	if p.Overwrite {
		rows = append(rows, "  "+st.active.Render(approve)+" approve overwrite   "+st.active.Render(reject)+" reject   ↑/↓ scroll   Esc reject")
	} else {
		rows = append(rows, "  "+st.active.Render(approve)+" approve   "+st.active.Render(reject)+" reject   ↑/↓ scroll   Esc reject")
	}
	return rows
}

// displayWritePath renders a proposal's destination the way the operator
// configured it (~/HAL/note.md).
func (m Model) displayWritePath(p workspace.Proposal) string {
	if m.workspace != nil {
		return m.workspace.DisplayPath(p)
	}
	return p.Path
}

// permissionLabel is the capability state HAL is in right now.
func (m Model) permissionLabel() string {
	switch m.writeState {
	case writeRequested:
		return "666 REQUESTED"
	case writeWriting:
		return "666 WRITING"
	}
	return "444 READ ONLY"
}

// writeFailureReason explains an execution failure in one short line,
// without the path or the contents.
func writeFailureReason(err error) string {
	switch {
	case errors.Is(err, workspace.ErrDestinationChanged):
		return "the file changed after you reviewed it"
	case errors.Is(err, workspace.ErrSymlink):
		return "the destination goes through a symbolic link"
	case errors.Is(err, workspace.ErrNotRegular):
		return "the destination is not a regular file"
	case errors.Is(err, workspace.ErrProtected):
		return "the destination is a protected directory"
	case errors.Is(err, workspace.ErrClosed):
		return "the workspace is not available"
	default:
		return "the filesystem refused the write"
	}
}

// humanSize renders a byte count compactly.
func humanSize(n int64) string {
	switch {
	case n <= 0:
		return "empty"
	case n < 1024:
		return fmt.Sprintf("%d bytes", n)
	case n < 1024*1024:
		return fmt.Sprintf("%.1f KiB", float64(n)/1024)
	default:
		return fmt.Sprintf("%.1f MiB", float64(n)/(1024*1024))
	}
}

// shortDigest keeps the review readable while still naming the exact
// bytes: the first sixteen hex characters of the SHA-256.
func shortDigest(digest string) string {
	if len(digest) <= 16 {
		return digest
	}
	return digest[:16] + "…"
}
