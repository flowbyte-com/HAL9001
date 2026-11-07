package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

// The composer shows a five-row window on the draft, and the window is
// only an abbreviation: the textarea holds every line, and a paste is
// never truncated. These tests drive the real textarea through the same
// Update path the interface uses, so they cover the bracket-paste
// insertion, the wrap, the height and the window together.

// pasteText delivers a bracketed paste to the model.
func pasteText(m Model, text string) Model {
	updated, _ := m.Update(tea.PasteMsg{Content: text})
	return updated.(Model)
}

// numberedLines is a draft of n numbered lines.
func numberedLines(n int) string {
	lines := make([]string, 0, n)
	for i := 1; i <= n; i++ {
		lines = append(lines, fmt.Sprintf("line %03d of the draft", i))
	}
	return strings.Join(lines, "\n")
}

// windowText is the visible part of the composer, without styling.
func windowText(m Model) []string {
	rows := m.composerWindow(m.ComposerRows())
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		out = append(out, strings.TrimRight(stripANSI(row), " "))
	}
	return out
}

// composerText is the draft inside the composer, without styling.
func composerText(m Model) string {
	return strings.TrimRight(stripANSI(strings.Join(windowText(m), "\n")), " ")
}

// TestPasteKeepsEveryLine checks that a paste of any size reaches the
// buffer whole. The height growing is not enough: the text has to be
// there.
func TestPasteKeepsEveryLine(t *testing.T) {
	for _, n := range []int{2, 5, 20, 100, 150} {
		m := testModel()
		want := numberedLines(n)
		m = pasteText(m, want)
		if got := m.Composer().Value(); got != want {
			t.Errorf("%d lines: the buffer holds %d bytes, want %d", n, len(got), len(want))
		}
		if got := m.draftRows(); got != n {
			t.Errorf("%d lines: draft rows = %d", n, got)
		}
	}
}

// TestLongPasteIsVisible is the regression for the screenshot: a paste
// taller than the textarea's own limit used to leave the composer blank.
// Every window row must carry draft text, and the last row must be the
// last line.
func TestLongPasteIsVisible(t *testing.T) {
	for _, n := range []int{100, 104, 150, 300} {
		m := testModel()
		want := numberedLines(n)
		m = pasteText(m, want)
		if h := m.ComposerRows(); h != maxComposerHeight {
			t.Fatalf("%d lines: composer rows = %d, want %d", n, h, maxComposerHeight)
		}
		rows := windowText(m)
		if len(rows) != maxComposerHeight {
			t.Fatalf("%d lines: window = %d rows, want %d", n, len(rows), maxComposerHeight)
		}
		for i, row := range rows {
			if strings.TrimSpace(row) == "" {
				t.Errorf("%d lines: window row %d is blank: %q", n, i, rows)
			}
		}
		lastDraftLine := fmt.Sprintf("line %03d of the draft", n)
		if rows[len(rows)-1] != lastDraftLine {
			t.Errorf("%d lines: last visible row = %q, want %q", n, rows[len(rows)-1], lastDraftLine)
		}
	}
}

// TestComposerHeightFollowsTheDraft checks the one-to-five row rule.
func TestComposerHeightFollowsTheDraft(t *testing.T) {
	m := testModel()
	if got := m.ComposerRows(); got != 1 {
		t.Errorf("empty composer rows = %d, want 1", got)
	}
	m = pasteText(m, "alpha\nbravo")
	if got := m.ComposerRows(); got != 2 {
		t.Errorf("two lines: composer rows = %d, want 2", got)
	}
	m = pasteText(m, "\ncharlie\ndelta\necho")
	if got := m.ComposerRows(); got != maxComposerHeight {
		t.Errorf("five lines: composer rows = %d, want %d", got, maxComposerHeight)
	}
	m = pasteText(m, "\nfoxtrot")
	if got := m.ComposerRows(); got != maxComposerHeight {
		t.Errorf("six lines: composer rows = %d, want %d", got, maxComposerHeight)
	}
	// Deleting contracts the composer again.
	for i := 0; i < 6; i++ {
		m = pressKey(m, tea.KeyBackspace, 0, "")
	}
	// Deleting pulls the cursor back; empty lines go too.
	m = pressKey(m, 'u', tea.ModCtrl, "")
	if got := m.ComposerRows(); got != 1 {
		t.Errorf("after clearing: composer rows = %d, want 1", got)
	}
}

// TestPasteUnicodeAndMarkdown checks that a paste with Markdown and
// wide characters is kept and shown.
func TestPasteUnicodeAndMarkdown(t *testing.T) {
	body := "## Heading\n\n- ✓ héllo wörld 日本語\n- **bold** and `code`\n"
	m := testModel()
	m = pasteText(m, body)
	if got := m.Composer().Value(); got != body {
		t.Errorf("buffer = %q", got)
	}
	shown := composerText(m)
	if !strings.Contains(shown, "héllo wörld 日本語") {
		t.Errorf("the composer does not show the wide text: %q", shown)
	}
	if !strings.Contains(shown, "**bold**") {
		t.Errorf("the composer does not show the Markdown: %q", shown)
	}
}

// TestPasteLongWrappedLine checks that a pasted line longer than the
// composer is wrapped, and that the window shows its last rows with the
// cursor on the final one.
func TestPasteLongWrappedLine(t *testing.T) {
	body := strings.TrimRight(strings.Repeat("word ", 120), " ")
	m := testModel()
	m = pasteText(m, body)
	if got := m.Composer().Value(); got != body {
		t.Errorf("buffer = %d bytes, want %d", len(got), len(body))
	}
	rows := m.draftRows()
	if rows <= maxComposerHeight {
		t.Fatalf("the long line did not wrap past the window: %d row(s)", rows)
	}
	if h := m.ComposerRows(); h != maxComposerHeight {
		t.Errorf("composer rows = %d, want %d", h, maxComposerHeight)
	}
	cur := m.cursorDraftRow()
	if cur < m.draftOffset || cur >= m.draftOffset+m.ComposerRows() {
		t.Errorf("cursor row %d is outside the window %d..%d", cur, m.draftOffset, m.draftOffset+m.ComposerRows())
	}
	if !strings.Contains(composerText(m), "word word") {
		t.Errorf("the composer does not show the wrapped line: %q", composerText(m))
	}
}

// TestPasteTrailingNewlines checks that the exact pasted text, including
// its trailing line breaks, is kept.
func TestPasteTrailingNewlines(t *testing.T) {
	body := "alpha\nbravo\n\n"
	m := testModel()
	m = pasteText(m, body)
	if got := m.Composer().Value(); got != body {
		t.Errorf("buffer = %q, want %q", got, body)
	}
	if got := m.draftRows(); got != 4 {
		t.Errorf("draft rows = %d, want 4", got)
	}
	if got := m.ComposerRows(); got != 4 {
		t.Errorf("composer rows = %d, want 4", got)
	}
	rows := windowText(m)
	if rows[0] != "alpha" || rows[1] != "bravo" {
		t.Errorf("window = %q", rows)
	}
	if got := m.cursorDraftRow(); got != 3 {
		t.Errorf("cursor row = %d, want 3 (the empty line after the last break)", got)
	}
}

// TestComposerWindowShowsTheScrolledDraft checks that the window can be
// scrolled through a long draft and shows the rows at the offset.
func TestComposerWindowShowsTheScrolledDraft(t *testing.T) {
	m := testModel()
	m = pasteText(m, numberedLines(20))
	// The window starts at the cursor, which is at the end.
	if got := windowText(m); len(got) != 5 || !strings.Contains(got[4], "line 020") {
		t.Fatalf("window at the cursor = %q", got)
	}
	// Scrolling up shows earlier rows without moving the cursor.
	m.draftOffset = 0
	rows := windowText(m)
	if !strings.Contains(rows[0], "line 001") {
		t.Errorf("the window does not start at the first row: %q", rows)
	}
	if !strings.Contains(rows[4], "line 005") {
		t.Errorf("the window does not end at the fifth row: %q", rows)
	}
	// The cursor stays at the end even though the window is elsewhere.
	if got := m.cursorDraftRow(); got != 19 {
		t.Errorf("the cursor moved: row %d, want 19", got)
	}
}

// TestComposerScrollIsIndependentOfTheConversation checks the two
// viewports do not move each other.
func TestComposerScrollIsIndependentOfTheConversation(t *testing.T) {
	m := testModel()
	m.SetMouse(true)
	for i := 0; i < 30; i++ {
		m.appendEntry("assistant", fmt.Sprintf("answer %02d", i))
	}
	m.renderConversation()
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(Model)
	m.viewport.GotoTop()
	m.renderConversation()
	conversation := m.viewport.YOffset()
	m = pasteText(m, numberedLines(20))

	// The wheel over the composer moves the draft window only.
	updated, _ = m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelUp, X: 10, Y: m.composerTop() + 1})
	m = updated.(Model)
	if m.viewport.YOffset() != conversation {
		t.Errorf("the composer wheel moved the conversation: %d -> %d", conversation, m.viewport.YOffset())
	}
	draftOffset := m.draftOffset

	// The wheel over the conversation moves the conversation only.
	updated, _ = m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelDown, X: 10, Y: m.viewportTop() + 1})
	m = updated.(Model)
	if m.draftOffset != draftOffset {
		t.Errorf("the conversation wheel moved the draft: %d -> %d", draftOffset, m.draftOffset)
	}
}

// TestPasteSubmitSendsTheWholeDraft checks that Enter sends the complete
// original draft, not the five visible rows.
func TestPasteSubmitSendsTheWholeDraft(t *testing.T) {
	m := testModel()
	body := numberedLines(120)
	m = pasteText(m, body)
	m = enterCurrent(m)
	entries := m.Entries()
	if len(entries) < 1 {
		t.Fatal("nothing was submitted")
	}
	if got := entries[0].text; got != body {
		t.Errorf("submitted %d bytes, want %d", len(got), len(body))
	}
	if got := m.Lines()[0]; got != "You > "+body {
		t.Error("the conversation line lost part of the prompt")
	}
}

// TestClearAndRestoreLongPaste checks Ctrl+U and Ctrl+Z on a draft that
// is taller than the composer.
func TestClearAndRestoreLongPaste(t *testing.T) {
	m := testModel()
	body := numberedLines(150)
	m = pasteText(m, body)

	m = pressKey(m, 'u', tea.ModCtrl, "")
	if got := m.Composer().Value(); got != "" {
		t.Errorf("Ctrl+U left %d bytes", len(got))
	}
	if got := m.ComposerRows(); got != 1 {
		t.Errorf("Ctrl+U left the composer at %d rows, want 1", got)
	}
	if want := "Draft cleared · Ctrl+Z to restore"; m.Notice() != want {
		t.Errorf("notice = %q, want %q", m.Notice(), want)
	}

	m = pressKey(m, 'z', tea.ModCtrl, "")
	if got := m.Composer().Value(); got != body {
		t.Errorf("Ctrl+Z restored %d bytes, want %d", len(got), len(body))
	}
	if got := m.ComposerRows(); got != maxComposerHeight {
		t.Errorf("Ctrl+Z restored the composer at %d rows, want %d", got, maxComposerHeight)
	}
	if want := "Draft restored"; m.Notice() != want {
		t.Errorf("notice = %q, want %q", m.Notice(), want)
	}
	rows := windowText(m)
	if !strings.Contains(rows[len(rows)-1], "line 150") {
		t.Errorf("the restored draft is not visible: %q", rows)
	}
	cur := m.cursorDraftRow()
	if cur < m.draftOffset || cur >= m.draftOffset+m.ComposerRows() {
		t.Errorf("the cursor is outside the window: row %d, window %d..%d", cur, m.draftOffset, m.draftOffset+m.ComposerRows())
	}
}

// TestLongDraftEditingKeepsContent checks that editing a draft larger
// than the window neither loses nor duplicates lines.
func TestLongDraftEditingKeepsContent(t *testing.T) {
	m := testModel()
	body := numberedLines(120)
	m = pasteText(m, body)
	// Type in the middle of the draft, then remove the character again.
	for i := 0; i < 60; i++ {
		m.composer.CursorUp()
	}
	m.composer.SetCursorColumn(4)
	m = typeText(m, "X")
	if got := m.Composer().Value(); !strings.Contains(got, "X") {
		t.Fatal("the typed character was lost")
	}
	m = pressKey(m, tea.KeyBackspace, 0, "")
	if got := m.Composer().Value(); got != body {
		t.Errorf("editing changed the draft: %d bytes, want %d", len(got), len(body))
	}
	// The window followed the cursor to the edited line.
	cur := m.cursorDraftRow()
	if cur < m.draftOffset || cur >= m.draftOffset+m.ComposerRows() {
		t.Errorf("the cursor is outside the window after editing: row %d, window %d..%d", cur, m.draftOffset, m.draftOffset+m.ComposerRows())
	}
}

// TestNoticeReplacementAndExpiry checks that a notice replaces the older
// one and that only the newest notice can expire.
func TestNoticeReplacementAndExpiry(t *testing.T) {
	m := testModel()
	m = pasteText(m, "draft")
	m = pressKey(m, 'u', tea.ModCtrl, "")
	first := m.noticeSeq
	if want := "Draft cleared · Ctrl+Z to restore"; m.Notice() != want {
		t.Fatalf("notice = %q, want %q", m.Notice(), want)
	}
	m = pressKey(m, 'z', tea.ModCtrl, "")
	if want := "Draft restored"; m.Notice() != want {
		t.Errorf("the second notice did not replace the first: %q", m.Notice())
	}
	// The expiry of the notice that was replaced is ignored.
	updated, _ := m.Update(noticeExpireMsg{seq: first})
	m = updated.(Model)
	if m.Notice() != "Draft restored" {
		t.Errorf("a stale expiry cleared the notice: %q", m.Notice())
	}
	// The expiry of the current notice clears it.
	updated, _ = m.Update(noticeExpireMsg{seq: m.noticeSeq})
	m = updated.(Model)
	if m.Notice() != "" {
		t.Errorf("the notice did not expire: %q", m.Notice())
	}
}

// TestNoticeExpiryIsScheduled checks that a notice is put on a timer,
// and that a turn in flight does not expire its own status line.
func TestNoticeExpiryIsScheduled(t *testing.T) {
	old := noticeLifetime
	noticeLifetime = time.Millisecond
	defer func() { noticeLifetime = old }()

	m := testModel()
	m = pasteText(m, "draft")
	updated, cmd := m.Update(tea.KeyPressMsg{Code: 'u', Mod: tea.ModCtrl})
	m = updated.(Model)
	if cmd == nil {
		t.Fatal("no expiry was scheduled")
	}
	msg := cmd()
	if _, ok := msg.(noticeExpireMsg); !ok {
		t.Fatalf("the scheduled message is %T, want noticeExpireMsg", msg)
	}
	seq := msg.(noticeExpireMsg).seq
	if seq != m.noticeSeq {
		t.Errorf("the expiry names notice %d, current is %d", seq, m.noticeSeq)
	}
}

// TestComposerWindowIgnoresAClampedView checks the safety net: a
// textarea whose view is shorter than the draft (a widget that clamps its
// own height, the way the old 99-row limit did) cannot make the composer
// blank — the draft's own rows are shown instead.
func TestComposerWindowIgnoresAClampedView(t *testing.T) {
	m := testModel()
	m = pasteText(m, numberedLines(120))
	m.composer.SetHeight(1)
	rows := windowText(m)
	if blankRows(rows) {
		t.Fatalf("blank window: %q", rows)
	}
	if !strings.Contains(rows[len(rows)-1], "line 120") {
		t.Errorf("the window does not show the end of the draft: %q", rows)
	}
}

// TestComposerWindowIgnoresAScrolledTextarea covers the other stale
// widget state: the textarea has scrolled its own view to the caret even
// though HAL's window is at the top of a draft that fits. Trusting the
// scrolled view would hide the first line; the draft's own rows win.
func TestComposerWindowIgnoresAScrolledTextarea(t *testing.T) {
	const first = "First line: testing HAL's multiline composer."
	const second = "Second line: both lines should appear immediately."
	m := testModel()
	m = pasteText(m, first+"\n"+second)
	// A frame has rendered: the textarea's viewport now holds the draft.
	_ = m.View()
	m.composer.SetHeight(1)
	if got := m.Composer().ScrollYOffset(); got == 0 {
		t.Fatalf("the test did not scroll the textarea")
	}
	m.frame.composer = 2
	m.draftOffset = 0
	rows := strings.Join(windowText(m), "\n")
	if !strings.Contains(rows, first) || !strings.Contains(rows, second) {
		t.Errorf("a scrolled textarea hid a line that fits: %q", rows)
	}
}

// TestComposerCaretFollowsAResize checks that the window stays around the
// caret and keeps showing text when the terminal changes size.
func TestComposerCaretFollowsAResize(t *testing.T) {
	m := testModel()
	m = pasteText(m, numberedLines(120))
	for _, size := range [][2]int{{60, 20}, {170, 38}, {40, 12}, {120, 40}} {
		updated, _ := m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		m = updated.(Model)
		cur := m.cursorDraftRow()
		if cur < m.draftOffset || cur >= m.draftOffset+m.ComposerRows() {
			t.Errorf("%dx%d: caret row %d is outside the window %d..%d",
				size[0], size[1], cur, m.draftOffset, m.draftOffset+m.ComposerRows())
		}
		if blankRows(windowText(m)) {
			t.Errorf("%dx%d: blank window", size[0], size[1])
		}
	}
}

// TestDiagReportsTheComposerWithoutItsText checks that the diagnostics
// panel says where the composer's window is, and never prints the draft.
func TestDiagReportsTheComposerWithoutItsText(t *testing.T) {
	m := testModel()
	m = pasteText(m, "secret draft text\nsecond line")
	panel := strings.Join(m.diagRows(), "\n")
	if strings.Contains(panel, "secret") {
		t.Errorf("the diagnostics panel printed the draft:\n%s", panel)
	}
	for _, want := range []string{"draft", "composer", "textarea"} {
		if !strings.Contains(panel, want) {
			t.Errorf("the diagnostics panel has no %q row:\n%s", want, panel)
		}
	}
}

// TestPasteIsFullyVisibleImmediately reproduces the field report without
// sending any follow-up key: the frame right after the paste already has
// the right height, visible rows and caret position.
func TestPasteIsFullyVisibleImmediately(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{name: "two lines", body: "alpha\nbravo"},
		{name: "three lines", body: "alpha\nbravo\ncharlie"},
		{name: "five lines", body: numberedLines(5)},
		{name: "long wrapped link and newline",
			body: "intro\n[Example](https://example.com/a/very/long/path/that/wraps/over/the/width) and more\n"},
		{name: "six lines", body: numberedLines(6)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := testModel()
			updated, _ := m.Update(tea.WindowSizeMsg{Width: 60, Height: 30})
			m = updated.(Model)
			m = pasteText(m, tc.body)
			shown := composerRulesAt(t, m, 60)
			lines := m.draftLines()
			expected := lines
			if len(expected) > maxComposerHeight {
				// A longer draft intentionally keeps the caret's window,
				// not every earlier row.
				expected = expected[len(expected)-maxComposerHeight:]
			}
			for _, line := range expected {
				want := strings.TrimRight(line, " ")
				if !containsRow(shown, want) {
					t.Errorf("line %q is not visible immediately: %q", want, shown)
				}
			}
			cur := m.cursorDraftRow()
			if cur < m.draftOffset || cur >= m.draftOffset+m.ComposerRows() {
				t.Errorf("caret row %d is outside window %d..%d",
					cur, m.draftOffset, m.draftOffset+m.ComposerRows())
			}
			if got := m.ComposerRows(); got != min(m.draftRows(), maxComposerHeight) {
				t.Errorf("composer rows = %d, want %d", got, min(m.draftRows(), maxComposerHeight))
			}
			if cur >= len(m.draftLines()) {
				t.Errorf("caret row %d is beyond %d draft rows", cur, len(m.draftLines()))
			}
		})
	}
}

// TestPasteIsFullyVisibleOnAShortTerminal is the field report at the
// size that exposes it: the frame has to trade rows, and it used to give
// up the composer's text rows before the optional chrome. A two-line
// paste then showed only its caret row; Up scrolled the hidden line into
// view. The first frame after the paste must already show both.
func TestPasteIsFullyVisibleOnAShortTerminal(t *testing.T) {
	const first = "First line: testing HAL's multiline composer."
	const second = "Second line: both lines should appear immediately."
	cases := []struct {
		name string
		body string
		rows int
	}{
		{name: "eight rows", body: first + "\n" + second, rows: 8},
		{name: "nine rows", body: first + "\n" + second, rows: 9},
		{name: "nine rows trailing newline", body: first + "\n" + second + "\n", rows: 9},
		{name: "ten rows trailing newline", body: first + "\n" + second + "\n", rows: 10},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := testModel()
			updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: tc.rows})
			m = updated.(Model)
			m = pasteText(m, tc.body)
			if got := len(m.layoutRows()); got != tc.rows {
				t.Fatalf("the frame has %d rows, want %d", got, tc.rows)
			}
			shown := windowText(m)
			text := strings.Join(shown, "\n")
			if !strings.Contains(text, first) {
				t.Errorf("the first line is not visible immediately: %q", shown)
			}
			if !strings.Contains(text, second) {
				t.Errorf("the second line is not visible immediately: %q", shown)
			}
			if m.draftOffset != 0 {
				t.Errorf("a draft that fits starts at window offset %d, want 0", m.draftOffset)
			}
			if got := m.Composer().ScrollYOffset(); got != 0 {
				t.Errorf("the textarea scrolled its own view to %d, want 0", got)
			}
			if got := m.Composer().Height(); got < m.draftRows() {
				t.Errorf("the textarea is %d rows tall for %d draft rows", got, m.draftRows())
			}
			cur := m.cursorDraftRow()
			if cur < m.draftOffset || cur >= m.draftOffset+m.ComposerRows() {
				t.Errorf("caret row %d is outside window %d..%d",
					cur, m.draftOffset, m.draftOffset+m.ComposerRows())
			}
			if got, want := m.ComposerRows(), min(m.draftRows(), maxComposerHeight); got != want {
				t.Errorf("composer rows = %d, want %d", got, want)
			}

			// Up must not be what makes the first line appear. It may move
			// the caret, but the frame still shows both lines afterwards.
			before := strings.Join(windowText(m), "\n")
			up := pressKey(m, tea.KeyUp, 0, "")
			after := strings.Join(windowText(up), "\n")
			if !strings.Contains(after, first) || !strings.Contains(after, second) {
				t.Errorf("after Up the frame lost a line: before=%q after=%q", before, after)
			}
		})
	}
}

// TestPasteStartEndSequenceShowsBothLines follows the full message
// sequence a real terminal produces: the bracketed-paste start, the
// content, and the end marker. The end marker passes through the generic
// key path, so it must not restore a taller frame or scroll the draft.
func TestPasteStartEndSequenceShowsBothLines(t *testing.T) {
	const first = "First line: testing HAL's multiline composer."
	const second = "Second line: both lines should appear immediately."
	m := testModel()
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 9})
	m = updated.(Model)
	for _, msg := range []tea.Msg{
		tea.PasteStartMsg{},
		tea.PasteMsg{Content: first + "\n" + second},
		tea.PasteEndMsg{},
	} {
		updated, _ = m.Update(msg)
		m = updated.(Model)
	}
	text := strings.Join(windowText(m), "\n")
	if !strings.Contains(text, first) || !strings.Contains(text, second) {
		t.Errorf("the start/paste/end sequence hid a line: %q", text)
	}
	if m.draftOffset != 0 {
		t.Errorf("window offset = %d, want 0", m.draftOffset)
	}
	if got := m.Composer().ScrollYOffset(); got != 0 {
		t.Errorf("textarea scroll = %d, want 0", got)
	}
	if got := len(m.layoutRows()); got != 9 {
		t.Errorf("the frame has %d rows, want 9", got)
	}
}

// TestShortTerminalKeepsComposerText checks the frame budget directly:
// optional chrome is dropped before the draft's text rows. At nine rows
// a two-line draft keeps both rows and its rules; a five-line draft
// keeps all five rows and gives up the rules instead.
func TestShortTerminalKeepsComposerText(t *testing.T) {
	two := computeChatFrame(9, 2, 0, false)
	if two.composer != 2 {
		t.Errorf("two-line composer = %d rows, want 2", two.composer)
	}
	if two.divider {
		t.Errorf("the decorative divider survived before the composer text")
	}
	if !two.notice || !two.hint {
		t.Errorf("the functional notice and hint rows should survive: %+v", two)
	}
	if !two.rules {
		t.Errorf("the composer rules should survive for a two-line draft at nine rows")
	}
	if got := two.chrome() + two.composer + two.viewport; got != 9 {
		t.Errorf("two-line frame uses %d rows, want 9", got)
	}

	five := computeChatFrame(9, 5, 0, false)
	if five.composer != 5 {
		t.Errorf("five-line composer = %d rows, want 5", five.composer)
	}
	if got := five.chrome() + five.composer + five.viewport; got != 9 {
		t.Errorf("five-line frame uses %d rows, want 9", got)
	}

	normal := computeChatFrame(24, 5, 8, false)
	if normal.composer != 5 || normal.suggest != 8 || !normal.notice ||
		!normal.hint || !normal.divider || !normal.rules {
		t.Errorf("a full-size frame lost chrome: %+v", normal)
	}
}

func containsRow(rows []string, want string) bool {
	for _, row := range rows {
		if row == want {
			return true
		}
	}
	return false
}

func TestPasteIntoExistingDraftIsVisibleImmediately(t *testing.T) {
	m := testModel()
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 60, Height: 30})
	m = updated.(Model)
	m = typeText(m, "start ")
	m = pasteText(m, "one\ntwo\nthree")
	shown := composerRulesAt(t, m, 60)
	text := strings.Join(shown, "\n")
	for _, want := range []string{"start one", "two", "three"} {
		if !strings.Contains(text, want) {
			t.Errorf("pasted text %q is not visible immediately: %q", want, shown)
		}
	}
	cur := m.cursorDraftRow()
	if cur < m.draftOffset || cur >= m.draftOffset+m.ComposerRows() {
		t.Errorf("caret row %d is outside window %d..%d", cur, m.draftOffset, m.draftOffset+m.ComposerRows())
	}
}
