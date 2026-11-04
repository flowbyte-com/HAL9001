package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"

	"github.com/flowbyte-com/HAL9001/internal/usage"
	"github.com/flowbyte-com/HAL9001/internal/workspace"
)

// The row below the composer is the contextual hint: it offers the keys
// that act on the draft while there is one, the undo hint while a
// cleared draft can be restored, and the ordinary keys otherwise.
//
// The row is reserved, so only its text changes: the composer and the
// footer never move when the hint does. The hint is about the composer
// alone, so it never changes with the permission state.

// footerText is the footer row of the rendered layout, without styling.
// The notice row sits below it when the frame keeps one.
func footerText(m Model) string {
	rows := m.layoutRows()
	i := len(rows) - 1
	if m.frame.notice {
		i--
	}
	if i < 0 || i >= len(rows) {
		return ""
	}
	return stripANSI(rows[i])
}

// hintText is the reserved hint row below the composer, without styling.
func hintText(m Model) string {
	rows := m.layoutRows()
	_, _, hint, _, _ := frameParts(rows)
	if hint < 0 || hint >= len(rows) {
		return ""
	}
	return stripANSI(rows[hint])
}

// draftHint is the exact hint the interface promises for a draft.
var draftHint = "Enter send · Ctrl+J newline · Ctrl+U clear · /help"

// emptyHint is the exact hint the interface promises for an empty
// composer.
var emptyHint = "Enter send · Ctrl+J newline · /help"

// clearedHint is the exact hint the interface promises for a draft that
// was cleared and can be restored.
var clearedHint = "Draft cleared · Ctrl+Z restore"

// TestTheHintFollowsTheDraft checks every state of the draft against
// the hint it must show.
func TestTheHintFollowsTheDraft(t *testing.T) {
	m := testModel()
	m.SetModelName("MiniMax-M3.1-Flash")
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = updated.(Model)

	if got := hintText(m); got != emptyHint {
		t.Errorf("empty composer hint = %q, want %q", got, emptyHint)
	}
	if strings.Contains(hintText(m), "Ctrl+U") {
		t.Error("an empty composer must not offer Ctrl+U")
	}

	m = typeText(m, "hello")
	if got := hintText(m); got != draftHint {
		t.Errorf("typed hint = %q, want %q", got, draftHint)
	}
	// The model stays in the footer, not in the hint.
	if !strings.Contains(footerText(m), "MiniMax-M3.1-Flash") {
		t.Errorf("typed footer lost the model: %q", footerText(m))
	}

	m = pressKey(m, 'u', tea.ModCtrl, "")
	if got := hintText(m); got != clearedHint {
		t.Errorf("cleared hint = %q, want %q", got, clearedHint)
	}
	if strings.Contains(hintText(m), "Ctrl+U") {
		t.Errorf("a cleared draft must not offer Ctrl+U: %q", hintText(m))
	}

	m = pressKey(m, 'z', tea.ModCtrl, "")
	if got := hintText(m); got != draftHint {
		t.Errorf("restored hint = %q, want %q", got, draftHint)
	}

	m = enterCurrent(m)
	if got := hintText(m); got != emptyHint {
		t.Errorf("submitted hint = %q, want %q", got, emptyHint)
	}
}

// TestTheHintFollowsAPaste checks that a paste and a deletion update the
// row without a keypress loop.
func TestTheHintFollowsAPaste(t *testing.T) {
	m := testModel()
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = updated.(Model)
	m = pasteText(m, numberedLines(3))
	if got := hintText(m); got != draftHint {
		t.Errorf("after a paste: %q", got)
	}
	// Delete every character again.
	for i := 0; i < len(numberedLines(3)); i++ {
		m = pressKey(m, tea.KeyBackspace, 0, "")
	}
	if got := hintText(m); got != emptyHint {
		t.Errorf("after deleting the draft: %q", got)
	}
}

// TestTheHintUsesTheBoundKeys checks that the hint is built from the
// keys the interface actually handles, so a rebind cannot leave a stale
// hint behind.
func TestTheHintUsesTheBoundKeys(t *testing.T) {
	if keyClearDraft != "ctrl+u" || keyRestoreDraft != "ctrl+z" {
		t.Fatalf("the draft keys changed: %q %q", keyClearDraft, keyRestoreDraft)
	}
	if got, want := keyName(keyClearDraft), "Ctrl+U"; got != want {
		t.Errorf("keyName(%q) = %q, want %q", keyClearDraft, got, want)
	}
	if got, want := keyName(keyRestoreDraft), "Ctrl+Z"; got != want {
		t.Errorf("keyName(%q) = %q, want %q", keyRestoreDraft, got, want)
	}
	m := testModel()
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = updated.(Model)
	m = typeText(m, "x")
	if got := hintText(m); !strings.Contains(got, keyName(keyClearDraft)+" clear") {
		t.Errorf("the hint does not use the bound clear key: %q", got)
	}
	m = pressKey(m, 'u', tea.ModCtrl, "")
	if got := hintText(m); !strings.Contains(got, keyName(keyRestoreDraft)+" restore") {
		t.Errorf("the hint does not use the bound restore key: %q", got)
	}
}

// TestTheHintNeverChangesTheFrame checks that the contextual row adds
// no height and moves nothing.
func TestTheHintNeverChangesTheFrame(t *testing.T) {
	m := testModel()
	m.SetModelName("MiniMax-M3.1-Flash")
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(Model)
	height, viewport := len(m.layoutRows()), m.frame.viewport
	offset := m.viewport.YOffset()

	m = typeText(m, "a longer draft line that changes the hint")
	if got := len(m.layoutRows()); got != height {
		t.Errorf("the layout grew to %d rows, want %d", got, height)
	}
	if m.frame.viewport != viewport {
		t.Errorf("the conversation shrank to %d rows, want %d", m.frame.viewport, viewport)
	}
	if m.viewport.YOffset() != offset {
		t.Errorf("the viewport moved: %d -> %d", offset, m.viewport.YOffset())
	}
}

// TestTheHintAtEveryWidth checks that the hint stays legible: the row
// never exceeds the terminal, and Ctrl+U is visible while a draft exists
// as long as the terminal can hold it.
func TestTheHintAtEveryWidth(t *testing.T) {
	for _, width := range []int{120, 100, 80, 58, 40, 30, 20, 14, 12, 8} {
		m := testModel()
		m.SetModelName("MiniMax-M3.1-Flash")
		m.SetEffort("medium", true)
		updated, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: 24})
		m = updated.(Model)
		m = typeText(m, "hello")
		got := hintText(m)
		if w := lipgloss.Width(got); w > width {
			t.Errorf("width %d: the hint is %d cells wide: %q", width, w, got)
		}
		if strings.TrimSpace(got) == "" {
			t.Errorf("width %d: the hint is empty", width)
		}
		if width >= len(draftHint) && got != draftHint {
			t.Errorf("width %d: hint = %q, want %q", width, got, draftHint)
		}
	}
}

// TestTheHintOnEveryTheme checks that the row is styled by the palette
// and still says the same thing.
func TestTheHintOnEveryTheme(t *testing.T) {
	for _, theme := range Themes() {
		m := testModel()
		m.SetColor(true)
		m.SetTheme(theme)
		m.SetModelName("MiniMax-M3.1-Flash")
		updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
		m = updated.(Model)
		m = typeText(m, "hello")
		row := ""
		for _, r := range m.layoutRows() {
			if strings.Contains(stripANSI(r), "Ctrl+U") {
				row = r
			}
		}
		if row == "" {
			t.Fatalf("%s: no hint row", theme.Name)
		}
		if !strings.Contains(row, "\x1b[") {
			t.Errorf("%s: the hint is not styled", theme.Name)
		}
		if !strings.Contains(stripANSI(row), draftHint) {
			t.Errorf("%s: hint = %q", theme.Name, stripANSI(row))
		}
		// The row must fit the terminal in display cells.
		if w := lipgloss.Width(stripANSI(row)); w > 100 {
			t.Errorf("%s: the hint is %d cells wide", theme.Name, w)
		}
	}
}

// TestTheFooterShowsTheThreeParts checks the footer: the permission on
// the left, the session's usage in the middle and the model on the
// right.
func TestTheFooterShowsTheThreeParts(t *testing.T) {
	m := testModel()
	m.SetModelName("MiniMax-M3.1-Flash")
	m.SetEffort("medium", true)
	m.SetUsage(usage.Session{Input: 1200, Output: 340, Turns: 2})
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = updated.(Model)
	footer := footerText(m)
	if !strings.HasPrefix(footer, "444 READ ONLY") {
		t.Errorf("footer = %q, want the permission on the left", footer)
	}
	if !strings.Contains(footer, "MiniMax-M3.1-Flash · medium") {
		t.Errorf("footer = %q, want the model and the effort on the right", footer)
	}
	if !strings.Contains(footer, "Tokens:") {
		t.Errorf("footer = %q, want the session usage", footer)
	}
	// The three parts do not overlap.
	if lipgloss.Width(footer) > 100 {
		t.Errorf("the footer is %d cells wide", lipgloss.Width(footer))
	}
}

// TestTheUsageIsOnlyShownWhereItIsKnown checks that the middle of the
// footer is empty when the session has no accounting, rather than
// showing invented zeros.
func TestTheUsageIsOnlyShownWhereItIsKnown(t *testing.T) {
	m := testModel()
	m.SetModelName("MiniMax-M3.1-Flash")
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = updated.(Model)
	if strings.Contains(footerText(m), "Tokens:") {
		t.Errorf("footer = %q, want no usage", footerText(m))
	}
	m.SetUsage(usage.Session{Input: 10, Output: 5, Turns: 1})
	if !strings.Contains(footerText(m), "Tokens:") {
		t.Errorf("footer = %q, want the usage", footerText(m))
	}
}

// TestThePermissionStaysVisibleWhileTyping checks that 444 does not
// disappear when the composer holds a draft: the hints moved to their
// own row, so the permission line is always there.
func TestThePermissionStaysVisibleWhileTyping(t *testing.T) {
	m := testModel()
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(Model)
	if !strings.Contains(footerText(m), "444 READ ONLY") {
		t.Errorf("footer = %q", footerText(m))
	}
	m = typeText(m, "a draft is being written")
	if !strings.Contains(footerText(m), "444 READ ONLY") {
		t.Errorf("the permission vanished while typing: %q", footerText(m))
	}
	if !strings.Contains(hintText(m), "Ctrl+U clear") {
		t.Errorf("the hint row lost the draft keys: %q", hintText(m))
	}
}

// TestTheHintDoesNotDependOnThePermission checks that the keyboard hints
// are the same whatever the permission state is.
func TestTheHintDoesNotDependOnThePermission(t *testing.T) {
	m := testModel()
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(Model)
	m = typeText(m, "hello")
	before := hintText(m)
	m.writeState = writeWriting
	m.write = workspace.Proposal{ID: "x", Path: "notes.md", Content: []byte("x"), Digest: "d"}
	if got := hintText(m); got != before {
		t.Errorf("the hint changed with the permission state: %q vs %q", got, before)
	}
	m.writeState = writeRequested
	if got := hintText(m); got != before {
		t.Errorf("the hint changed with the permission state: %q vs %q", got, before)
	}
}

// TestTheFooterShowsTheWriteStateProminently checks that a write that
// needs the operator is named in the footer.
func TestTheFooterShowsTheWriteStateProminently(t *testing.T) {
	m := testModel()
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(Model)
	m.writeState = writeRequested
	if !strings.Contains(footerText(m), "666 REQUESTED") {
		t.Errorf("footer = %q, want the write request", footerText(m))
	}
	m.writeState = writeWriting
	if !strings.Contains(footerText(m), "666 WRITING") {
		t.Errorf("footer = %q, want the write in progress", footerText(m))
	}
}

// TestTheHeaderShortensTheTitle checks that a long session title is
// shortened rather than wrapped.
func TestTheHeaderShortensTheTitle(t *testing.T) {
	for _, width := range []int{120, 80, 40, 24, 16} {
		m := testModel()
		m.SetTitle("a very long conversation title that will not fit in a narrow terminal")
		updated, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: 24})
		m = updated.(Model)
		header := stripANSI(m.headerRow())
		if lipgloss.Width(header) > width {
			t.Errorf("width %d: the header is %d cells wide: %q", width, lipgloss.Width(header), header)
		}
		if !strings.Contains(header, "HAL9001") {
			t.Errorf("width %d: the identity is missing: %q", width, header)
		}
	}
}
