package tui

import (
	"strings"

	lipgloss "charm.land/lipgloss/v2"
	"github.com/rivo/uniseg"

	"github.com/flowbyte-com/HAL9001/internal/engine"
)

// toolIconsMode selects the presentation of the compact tool activity
// line. It is display-only and session-local; the stored tool evidence
// never changes.
type toolIconsMode int

const (
	toolIconsClassic toolIconsMode = iota
	toolIconsEmoji
)

const (
	classicToolIcon = "⚙"
	// emojiToolGutter is the fixed number of cells before the tool name
	// when emoji icons are on: one two-cell emoji plus one separator.
	// The classic gutter is the existing "⚙ " two-cell prefix.
	emojiToolGutter = 3
)

// toolIconsLabel is the operator-facing name of the current mode.
func (m Model) toolIconsLabel() string {
	if m.toolIcons == toolIconsEmoji {
		return "emoji"
	}
	return "classic"
}

// applyToolIcons changes the presentation and re-renders the entries
// that already contain tool evidence, so the switch affects the current
// conversation as well as future tool rows. It never touches the stored
// records or the permission gate.
func (m *Model) applyToolIcons(mode toolIconsMode) {
	if m.toolIcons == mode {
		return
	}
	anchor := m.anchor()
	m.toolIcons = mode
	for i := range m.entries {
		if len(m.entries[i].tools) > 0 {
			m.renderEntry(i)
		}
	}
	m.renderConversation()
	m.restoreAnchor(anchor)
}

// toolEmojiFor classifies one structured activity record. It looks only
// at the tool identity, action and result status. In particular it never
// inspects shell command text: a bash call remains 💻 whatever command
// it runs, because the icon is decoration, not a security classification.
func toolEmojiFor(a engine.ToolActivity) string {
	if toolIconFailed(a.Status) {
		return "🚨"
	}
	name := strings.ToLower(strings.TrimSpace(a.Name))
	action := strings.ToLower(strings.TrimSpace(a.Action))
	if toolIconDestructive(name, action) {
		return "🔪"
	}
	switch {
	case strings.HasPrefix(name, "mpm_"):
		// MPM knowledge and continuity keep one identity whatever the
		// action is: query, save, propose, record, handoff and so on.
		return "💡"
	case toolIconSearch(name, action):
		return "🔎"
	case toolIconRead(name, action):
		return "📄"
	case toolIconWrite(name, action):
		return "📝"
	case toolIconEdit(name, action):
		return "✏️"
	case toolIconPackage(name, action):
		return "📦"
	case toolIconShell(name, action):
		return "💻"
	default:
		return classicToolIcon
	}
}

// toolIconFailed maps the terminal result states that mean the operation
// itself failed. Cancellation and refusal are separate outcomes; their
// action icon remains visible with the status label beside it.
func toolIconFailed(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "failed", "failure", "timeout":
		return true
	default:
		return false
	}
}

// toolIconSearch and the other classifiers read structured names and
// actions only. They deliberately do not inspect targets, arguments or
// shell command text.
func toolIconSearch(name, action string) bool {
	return name == "web_search" || name == "firecrawl_search" || action == "search"
}

func toolIconRead(name, action string) bool {
	return name == "read" || name == "firecrawl_scrape" || action == "read" || action == "scrape"
}

func toolIconWrite(name, action string) bool {
	return name == "write" || name == "write_file" || action == "write"
}

func toolIconEdit(name, action string) bool {
	return name == "edit" || action == "edit"
}

func toolIconPackage(name, action string) bool {
	switch name {
	case "package", "packages":
		return true
	}
	switch action {
	case "package", "install", "uninstall":
		return true
	default:
		return false
	}
}

func toolIconShell(name, action string) bool {
	return name == "bash" || action == "bash"
}

// toolIconDestructive recognizes only an explicit destructive verb in
// the structured tool identity. It is a presentation hint, never an
// authority decision. No current HAL tool reports one of these verbs.
func toolIconDestructive(name, action string) bool {
	switch name {
	case "delete", "remove", "rm", "rmdir", "unlink":
		return true
	}
	switch action {
	case "delete", "remove", "rm", "rmdir", "unlink", "truncate", "destroy", "drop", "purge":
		return true
	default:
		return false
	}
}

// toolIconPrefix returns the leading icon and its fixed display gutter.
// Every emoji is padded to two cells and followed by one separator, so
// the tool name starts in the same column on every row. Classic keeps
// the exact "⚙ " prefix it has always had.
func toolIconPrefix(a engine.ToolActivity, icons toolIconsMode) (string, int) {
	if icons != toolIconsEmoji {
		return classicToolIcon + " ", 2
	}
	icon := toolEmojiFor(a)
	width := lipgloss.Width(icon)
	if width < 1 || width > 2 {
		// A font that cannot supply the emoji, or a future glyph whose
		// width is not the expected two cells, falls back to the plain
		// gear in the same gutter.
		icon = classicToolIcon
		width = lipgloss.Width(icon)
	}
	if width < 1 {
		width = 1
	}
	return icon + strings.Repeat(" ", 2-width) + " ", emojiToolGutter
}

// truncateToolCells shortens plain tool text to at most n display cells.
// It walks grapheme clusters, so a variation selector, combining mark or
// wide emoji is never cut apart.
func truncateToolCells(s string, n int) string {
	if n < 1 {
		return ""
	}
	if lipgloss.Width(s) <= n {
		return s
	}
	if n == 1 {
		return "…"
	}
	var b strings.Builder
	width := 0
	graphemes := uniseg.NewGraphemes(s)
	for graphemes.Next() {
		clusterWidth := graphemes.Width()
		if width+clusterWidth > n-1 {
			break
		}
		b.WriteString(graphemes.Str())
		width += clusterWidth
	}
	return b.String() + "…"
}

// truncateToolMiddle keeps both ends of a target path.
func truncateToolMiddle(s string, n int) string {
	if n < 1 {
		return ""
	}
	if lipgloss.Width(s) <= n {
		return s
	}
	if n == 1 {
		return "…"
	}
	head := (n - 1) / 2
	tail := n - 1 - head
	return headToolCells(s, head) + "…" + tailToolCells(s, tail)
}

func headToolCells(s string, n int) string {
	if n < 1 {
		return ""
	}
	var b strings.Builder
	width := 0
	graphemes := uniseg.NewGraphemes(s)
	for graphemes.Next() {
		clusterWidth := graphemes.Width()
		if width+clusterWidth > n {
			break
		}
		b.WriteString(graphemes.Str())
		width += clusterWidth
	}
	return b.String()
}

func tailToolCells(s string, n int) string {
	if n < 1 {
		return ""
	}
	var clusters []string
	var widths []int
	graphemes := uniseg.NewGraphemes(s)
	for graphemes.Next() {
		clusters = append(clusters, graphemes.Str())
		widths = append(widths, graphemes.Width())
	}
	width := 0
	start := len(clusters)
	for i := len(clusters) - 1; i >= 0; i-- {
		if width+widths[i] > n {
			break
		}
		width += widths[i]
		start = i
	}
	return strings.Join(clusters[start:], "")
}
