package tui

import (
	"errors"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"

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
//
// The overlay has three zones. The header names the operation, the
// middle holds the proposed contents, and the footer carries the two
// decisions. Only the middle scrolls: an operator reading the first page
// of a long document must still see where the file is going and how to
// decide, so the header and the decisions never move.

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
		// The chat frame is recomputed too, so the conversation behind
		// the overlay is right when the overlay closes.
		m.sizePanes()
		m.clampPanel()
		return m, nil
	case tea.MouseWheelMsg:
		if msg.Button == tea.MouseWheelUp {
			m.scrollPanel(-3)
		} else {
			m.scrollPanel(3)
		}
		return m, nil
	case tea.MouseClickMsg:
		// The pointer never decides a write. Only a chord does, so a
		// click is ignored on purpose.
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
			m.scrollPanel(-m.approvalPage())
		case "pgdown", "pgdn":
			m.scrollPanel(m.approvalPage())
		case "home":
			m.scrollPanel(-1 << 20)
		case "end":
			m.scrollPanel(1 << 20)
		}
		return m, nil
	}
	return m, nil
}

// approvalBlock is one fixed piece of the approval overlay.
//
// priority says how readily the block may be dropped when the terminal
// is too short to hold the whole overlay: zero is never dropped. The
// preview is the one part that scrolls, and it is not a block.
type approvalBlock struct {
	rows     []string
	priority int
	// preview marks the place where the scrollable document goes.
	preview bool
	// meta marks the row that reports the bytes and the reading
	// position. Its text depends on the height of the preview, so it
	// is filled in once that height is known.
	meta bool
}

// approvalLayout renders the review and reports the height of the
// scrollable preview.
//
// The overlay is fixed at the top and the bottom and scrollable in the
// middle. When the terminal is too short, the decorative rows go first:
// the destination and the decisions are the last things to go.
func (m Model) approvalLayout() ([]string, int) {
	blocks, preview := m.approvalGeometry()
	for i := range blocks {
		if blocks[i].meta {
			blocks[i].rows = []string{m.approvalMeta(preview)}
		}
	}
	rows := make([]string, 0, m.height)
	for _, b := range blocks {
		if b.preview {
			rows = append(rows, m.panelRows(m.approvalPreviewRows(), preview)...)
			continue
		}
		rows = append(rows, b.rows...)
	}
	return rows, preview
}

// approvalGeometry resolves the overlay's blocks and the height of the
// scrollable preview.
//
// When the terminal is too short to hold the whole overlay, the most
// decorative row goes first. The destination and the decisions are
// never dropped: they are the review.
func (m Model) approvalGeometry() ([]approvalBlock, int) {
	blocks := m.approvalBlocks()
	preview := 0
	for {
		fixed := 0
		for _, b := range blocks {
			if b.preview {
				continue
			}
			fixed += len(b.rows)
		}
		preview = m.height - fixed
		if preview >= 1 {
			break
		}
		idx := -1
		for i, b := range blocks {
			if b.preview || b.priority <= 0 {
				continue
			}
			if idx < 0 || b.priority > blocks[idx].priority {
				idx = i
			}
		}
		if idx < 0 {
			break
		}
		blocks = append(blocks[:idx], blocks[idx+1:]...)
	}
	if preview < 0 {
		preview = 0
	}
	return blocks, preview
}

// approvalBlocks returns the fixed parts of the approval overlay, in the
// order they are drawn.
func (m Model) approvalBlocks() []approvalBlock {
	p := m.write
	blocks := make([]approvalBlock, 0, 12)
	add := func(priority int, rows ...string) {
		blocks = append(blocks, approvalBlock{rows: rows, priority: priority})
	}
	// Which conversation is asking.
	add(90, m.headerRow())
	add(100, m.rule())
	// What the review is about.
	add(70, m.approvalTitle())
	// The three facts the operator needs before deciding.
	if m.height < 8 {
		// A terminal this short gets the three facts on one row.
		add(0, m.approvalCompactFacts())
	} else {
		long, short := m.approvalOperation(p)
		add(0, m.approvalField("destination", m.displayWritePath(p), ""))
		add(0, m.approvalField("operation", long, short))
		add(0, m.approvalField("size", humanSize(int64(p.Size())), ""))
	}
	add(100, m.rule())
	// The document: the only part that scrolls.
	blocks = append(blocks, approvalBlock{preview: true})
	add(100, m.rule())
	// Which bytes, and where the reader is in the document.
	blocks = append(blocks, approvalBlock{rows: []string{""}, priority: 80, meta: true})
	// The permission state.
	add(0, m.statusBar())
	// The decisions are the last row, so a short terminal never cuts
	// them off.
	add(0, m.approvalControls()...)
	return blocks
}

// approvalPreviewRows renders the proposed contents, wrapped to the
// terminal. This is the document the operator inspects, and the only
// part of the overlay that scrolls.
func (m Model) approvalPreviewRows() []string {
	width := m.width - 6
	if width < 10 {
		width = 10
	}
	rows := make([]string, 0, 32)
	for _, line := range strings.Split(string(m.write.Content), "\n") {
		for _, wrapped := range wrapRunes([]rune(line), width) {
			rows = append(rows, "  │ "+strings.TrimRight(string(wrapped), " "))
		}
	}
	return rows
}

// approvalPreviewHeight is how many rows the scrollable preview gets in
// the current terminal.
func (m Model) approvalPreviewHeight() int {
	_, preview := m.approvalGeometry()
	return preview
}

// approvalPage is how far a page key moves the preview.
func (m Model) approvalPage() int {
	n := m.approvalPreviewHeight()
	if n < 1 {
		n = 1
	}
	return n
}

// clampPanel keeps the scroll offset inside the document after a
// resize: the same offset can point past the end of a document that now
// wraps differently.
func (m *Model) clampPanel() {
	if m.view != viewApprove {
		return
	}
	total := len(m.approvalPreviewRows())
	n := m.approvalPreviewHeight()
	if m.panelOffset > total-n {
		m.panelOffset = total - n
	}
	if m.panelOffset < 0 {
		m.panelOffset = 0
	}
}

// approvalTitle names the review and says that nothing has happened yet.
//
// A narrow terminal takes the shorter form: the statement about the
// write is the important one.
func (m Model) approvalTitle() string {
	full := "666 — one write needs your approval · nothing has been written yet"
	short := "666 — one write needs your approval"
	tiny := "666 — one write"
	for _, t := range []string{full, short, tiny} {
		if lipgloss.Width(t)+2 <= m.width {
			if m.color {
				return "  " + m.theme.styles().active.Render(t)
			}
			return "  " + t
		}
	}
	return "  " + tiny
}

// approvalField renders one labelled fact of the operation.
//
// A narrow terminal gets the short form of the value rather than a
// value cut in half, and a path that is still too long keeps both of its
// ends: the directory it lives in and the name of the file.
func (m Model) approvalField(label, value, short string) string {
	st := m.theme.styles()
	const labelWidth = 12
	room := m.width - 2 - labelWidth
	if room < 4 {
		room = 4
	}
	if short != "" && lipgloss.Width(value) > room {
		value = short
	}
	if lipgloss.Width(value) > room {
		value = truncateMiddle(value, room)
	}
	row := "  " + padTo(st.active.Render(label), labelWidth) + value
	return m.fitWidth(row, m.width)
}

// approvalOperation names the operation: a new file or a replacement.
func (m Model) approvalOperation(p workspace.Proposal) (string, string) {
	if !p.Overwrite {
		return "create — the file does not exist yet", "create"
	}
	return fmt.Sprintf("OVERWRITE — replaces the existing file (%s)", humanSize(p.Existing)),
		fmt.Sprintf("OVERWRITE (%s)", humanSize(p.Existing))
}

// approvalCompactFacts puts the three facts of the operation on one
// row, for a terminal that is too short for three.
//
// The path keeps both of its ends, so the file is still named.
func (m Model) approvalCompactFacts() string {
	p := m.write
	long, short := m.approvalOperation(p)
	operation := long
	if lipgloss.Width(operation) > 24 {
		operation = short
	}
	size := humanSize(int64(p.Size()))
	path := m.displayWritePath(p)
	row := "  " + path + " · " + operation + " · " + size
	if lipgloss.Width(row) > m.width {
		room := m.width - 2 - lipgloss.Width(operation) - lipgloss.Width(size) - 6
		if room < 8 {
			room = 8
		}
		row = "  " + truncateMiddle(path, room) + " · " + operation + " · " + size
	}
	return m.fitWidth(row, m.width)
}

// approvalMeta names the exact bytes and the reader's place in the
// document.
//
// The row is informative: it can be dropped on a short terminal without
// taking anything away from the decision.
func (m Model) approvalMeta(visible int) string {
	parts := []string{"sha256:" + shortDigest(m.write.Digest)}
	total := len(m.approvalPreviewRows())
	if total > 0 {
		first := m.panelOffset + 1
		if first > total {
			first = total
		}
		if visible >= total {
			parts = append(parts, fmt.Sprintf("all %d lines", total))
		} else {
			parts = append(parts, fmt.Sprintf("lines %d–%d of %d", first, first+visible-1, total))
		}
	}
	if m.write.Turn > 0 {
		parts = append(parts, fmt.Sprintf("turn %d", m.write.Turn))
	}
	return m.joinFitting(parts)
}

// joinFitting joins the parts with a separator, dropping the last ones
// until the row fits the room it has.
func (m Model) joinFitting(parts []string) string {
	for len(parts) > 1 {
		row := "  " + strings.Join(parts, " · ")
		if lipgloss.Width(row) <= m.width {
			return row
		}
		parts = parts[:len(parts)-1]
	}
	return m.fitWidth("  "+strings.Join(parts, " · "), m.width)
}

// approvalControls renders the decisions and the keys that make them.
//
// The row never scrolls away: it is the last row of the overlay, so it
// is there at the beginning of a document, in the middle of it and at
// its end. A narrow terminal splits the row in two rather than cutting
// a key off.
func (m Model) approvalControls() []string {
	width := m.width
	if width < 8 {
		width = 8
	}
	full := m.approvalControlRow(true)
	if lipgloss.Width(full) <= width {
		return []string{full}
	}
	// Two rows keep every fact: the decisions above, the keys that are
	// not decisions below.
	row1 := m.approvalControlRow(false)
	row2 := m.approvalRow("↑/↓", " Scroll", "Esc", " Reject")
	if lipgloss.Width(row1) <= width && lipgloss.Width(row2) <= width {
		return []string{row1, row2}
	}
	// Nothing fits: the two decisions alone still say what can be done.
	short1 := m.approvalRow(keyName(keyApproveWrite), " Approve")
	short2 := m.approvalRow(keyName(keyRejectWrite), " Reject")
	if lipgloss.Width(short1) <= width && lipgloss.Width(short2) <= width {
		return []string{short1, short2}
	}
	return []string{m.fitWidth("  "+keyName(keyApproveWrite), width)}
}

// approvalControlRow renders the decisions in one row.
func (m Model) approvalControlRow(withScroll bool) string {
	if withScroll {
		return m.approvalRow(keyName(keyApproveWrite), " Approve",
			keyName(keyRejectWrite), " Reject", "↑/↓", " Scroll", "Esc", " Reject")
	}
	return m.approvalRow(keyName(keyApproveWrite), " Approve",
		keyName(keyRejectWrite), " Reject")
}

// approvalRow renders key/label pairs in the theme: the key in the
// accent colour, the label in the muted one.
func (m Model) approvalRow(pairs ...string) string {
	parts := make([]string, 0, len(pairs)/2)
	for i := 0; i+1 < len(pairs); i += 2 {
		if pairs[i] == "" {
			continue
		}
		if m.color {
			st := m.theme.styles()
			parts = append(parts, st.active.Render(pairs[i])+st.footer.Render(pairs[i+1]))
			continue
		}
		parts = append(parts, pairs[i]+pairs[i+1])
	}
	return "  " + strings.Join(parts, " · ")
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
