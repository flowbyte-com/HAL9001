package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"

	"github.com/flowbyte-com/HAL9001/internal/engine"
	"github.com/flowbyte-com/HAL9001/internal/permission"
	"github.com/flowbyte-com/HAL9001/internal/session"
)

func toolIconActivity(name, action, status string) engine.ToolActivity {
	return engine.ToolActivity{
		Name: name, Action: action, Status: status,
		Mode: permission.Guarded,
	}
}

// TestToolEmojiVocabulary checks the exact action vocabulary. MPM keeps
// its knowledge identity for every action; the other rows use the
// structured tool identity, never command text.
func TestToolEmojiVocabulary(t *testing.T) {
	cases := []struct {
		name   string
		action string
		status string
		want   string
	}{
		{"web_search", "web_search", "ok", "🔎"},
		{"firecrawl_search", "firecrawl_search", "ok", "🔎"},
		{"read", "read", "ok", "📄"},
		{"firecrawl_scrape", "firecrawl_scrape", "ok", "📄"},
		{"mpm_memory", "query", "ok", "💡"},
		{"mpm_skills", "list", "ok", "💡"},
		{"mpm_decisions", "record", "ok", "💡"},
		{"mpm_lessons", "propose", "ok", "💡"},
		{"mpm_theories", "propose", "ok", "💡"},
		{"mpm_handoff", "write", "ok", "💡"},
		{"write", "write", "ok", "📝"},
		{"write_file", "write", "ok", "📝"},
		{"edit", "edit", "ok", "✏️"},
		{"bash", "bash", "ok", "💻"},
		{"mystery", "mystery", "ok", "⚙"},
		// Structured categories reserved for future tools. They are not
		// registered tools today; the classifier is exercised so the
		// vocabulary cannot silently drift.
		{"package", "package", "ok", "📦"},
		{"delete", "delete", "ok", "🔪"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := toolEmojiFor(toolIconActivity(tc.name, tc.action, tc.status))
			if got != tc.want {
				t.Errorf("toolEmojiFor(%q/%q) = %q, want %q", tc.name, tc.action, got, tc.want)
			}
		})
	}
}

// TestToolEmojiPriority covers failure over destructive over ordinary,
// plus the rule that shell text is never parsed as a classification.
func TestToolEmojiPriority(t *testing.T) {
	failedBash := toolIconActivity("bash", "bash", "failed")
	if got := toolEmojiFor(failedBash); got != "🚨" {
		t.Errorf("failed bash icon = %q, want 🚨", got)
	}
	if got := toolEmojiFor(toolIconActivity("web_search", "web_search", "timeout")); got != "🚨" {
		t.Errorf("timed-out search icon = %q, want 🚨", got)
	}
	if got := toolEmojiFor(toolIconActivity("delete", "delete", "failed")); got != "🚨" {
		t.Errorf("failed destructive icon = %q, want 🚨", got)
	}
	if got := toolEmojiFor(toolIconActivity("delete", "delete", "ok")); got != "🔪" {
		t.Errorf("destructive icon = %q, want 🔪", got)
	}
	if got := toolEmojiFor(toolIconActivity("unknown", "unknown", "failed")); got != "🚨" {
		t.Errorf("failed unknown icon = %q, want 🚨", got)
	}

	// A destructive-looking string is not a destructive operation. HAL
	// does not receive the command text in ToolActivity, and even when a
	// summary contains it, the shell icon stays 💻.
	bash := toolIconActivity("bash", "bash", "ok")
	bash.Target = "/tmp/cache"
	bash.Summary = "rm -rf /tmp/cache"
	if got := toolEmojiFor(bash); got != "💻" {
		t.Errorf("bash with destructive-looking text = %q, want 💻", got)
	}

	// Cancellation and refusal are distinct outcomes, not failures.
	for _, status := range []string{"cancelled", "denied"} {
		if got := toolEmojiFor(toolIconActivity("bash", "bash", status)); got != "💻" {
			t.Errorf("%s bash icon = %q, want 💻", status, got)
		}
	}
}

// TestExpectedFailurePresentation checks that 📍 requires a declared,
// matched expectation. An after-the-fact explanation in Summary is not
// an expectation.
func TestExpectedFailurePresentation(t *testing.T) {
	expectedRead := toolIconActivity("read", "read", "failed")
	expectedRead.Expected = "missing"
	expectedRead.ExpectedMet = true
	expectedRead.Mode = permission.Unrestricted
	if got := toolEmojiFor(expectedRead); got != "📍" {
		t.Errorf("expected missing read icon = %q, want 📍", got)
	}
	line := toolActivityLineMode(expectedRead, 80, toolIconsEmoji)
	for _, want := range []string{"📍 read", "failed", "expected missing", "666"} {
		if !strings.Contains(line, want) {
			t.Errorf("expected read line is missing %q: %q", want, line)
		}
	}
	if strings.Contains(line, "🚨") {
		t.Errorf("expected read kept the unexpected-failure icon: %q", line)
	}

	expectedBash := toolIconActivity("bash", "bash", "failed")
	expectedBash.Expected = "exit:17"
	expectedBash.ExpectedMet = true
	expectedBash.ExitCode = intPointer(17)
	expectedBash.Mode = permission.Unrestricted
	if got := toolEmojiFor(expectedBash); got != "📍" {
		t.Errorf("expected exit icon = %q, want 📍", got)
	}
	bashLine := toolActivityLineMode(expectedBash, 80, toolIconsEmoji)
	if !strings.Contains(bashLine, "📍 bash") ||
		!strings.Contains(bashLine, "expected exit 17") ||
		strings.Count(bashLine, "exit 17") != 1 {
		t.Errorf("expected bash line = %q", bashLine)
	}

	// A failure with a mismatched expectation is unexpected.
	unexpected := toolIconActivity("bash", "bash", "failed")
	unexpected.Expected = "exit:17"
	unexpected.ExpectedMet = false
	unexpected.ExitCode = intPointer(1)
	if got := toolEmojiFor(unexpected); got != "🚨" {
		t.Errorf("mismatched exit icon = %q, want 🚨", got)
	}
	unexpectedLine := toolActivityLineMode(unexpected, 80, toolIconsEmoji)
	for _, want := range []string{"🚨 bash", "exit 1", "expected exit 17 not met"} {
		if !strings.Contains(unexpectedLine, want) {
			t.Errorf("unexpected line is missing %q: %q", want, unexpectedLine)
		}
	}

	// A declared failure expectation that succeeds keeps the ordinary
	// action icon and records the mismatch.
	surprise := toolIconActivity("bash", "bash", "ok")
	surprise.Expected = "exit:17"
	surprise.ExpectedMet = false
	surprise.ExitCode = intPointer(0)
	surprise.Mode = permission.Unrestricted
	if got := toolEmojiFor(surprise); got != "💻" {
		t.Errorf("unexpected success icon = %q, want 💻", got)
	}
	surpriseLine := toolActivityLineMode(surprise, 80, toolIconsEmoji)
	for _, want := range []string{"💻 bash", "completed", "exit 0", "expected exit 17 not met"} {
		if !strings.Contains(surpriseLine, want) {
			t.Errorf("unexpected success line is missing %q: %q", want, surpriseLine)
		}
	}

	// A model's explanation after the fact is not an expectation.
	explained := toolIconActivity("bash", "bash", "failed")
	explained.Summary = "this was expected to exit 17"
	if got := toolEmojiFor(explained); got != "🚨" {
		t.Errorf("explanation-only failure icon = %q, want 🚨", got)
	}
	if line := toolActivityLineMode(explained, 80, toolIconsEmoji); strings.Contains(line, "expected") {
		t.Errorf("explanation-only failure gained an expectation: %q", line)
	}

	// Failure still wins over destructive when the expectation matched.
	expectedDelete := toolIconActivity("delete", "delete", "failed")
	expectedDelete.Expected = "exit:17"
	expectedDelete.ExpectedMet = true
	expectedDelete.ExitCode = intPointer(17)
	if got := toolEmojiFor(expectedDelete); got != "📍" {
		t.Errorf("expected destructive failure icon = %q, want 📍", got)
	}

	// Classic keeps the gear while retaining the new diagnostic.
	classic := toolActivityLineMode(expectedBash, 80, toolIconsClassic)
	if !strings.HasPrefix(classic, "⚙ bash") ||
		!strings.Contains(classic, "expected exit 17") {
		t.Errorf("classic expected line = %q", classic)
	}
}

// TestExpectedFailureHistoricalResume checks that the verified ledger
// metadata survives a resume and still distinguishes expected from
// unexpected failures.
func TestExpectedFailureHistoricalResume(t *testing.T) {
	lines := []SessionLine{{
		Role: "assistant", Text: "ran", TurnID: "expected",
		Tools: []session.ToolExecution{
			{
				TurnID: "expected", CallID: "read-1", Tool: "read", Action: "read",
				Mode: "666", Status: session.ToolFailure,
				Expected: "missing", ExpectedMet: true, Summary: "failed",
			},
			{
				TurnID: "expected", CallID: "bash-1", Tool: "bash", Action: "bash",
				Mode: "666", Status: session.ToolFailure, ExitCode: intPointer(17),
				Expected: "exit:17", ExpectedMet: true, Summary: "exit 17",
			},
			{
				TurnID: "expected", CallID: "bash-2", Tool: "bash", Action: "bash",
				Mode: "666", Status: session.ToolFailure, ExitCode: intPointer(1),
				Expected: "exit:17", ExpectedMet: false, Summary: "exit 1",
			},
			{
				TurnID: "expected", CallID: "bash-3", Tool: "bash", Action: "bash",
				Mode: "666", Status: session.ToolFailure, ExitCode: intPointer(9),
				Summary: "exit 9",
			},
		},
	}}
	m := transcriptModel(t, 100, 30)
	m.applyToolIcons(toolIconsEmoji)
	m.SetHistory(lines)
	plain := stripANSI(strings.Join(m.layoutRows(), "\n"))
	for _, want := range []string{
		"📍 read", "expected missing",
		"📍 bash", "expected exit 17",
		"🚨 bash", "expected exit 17 not met",
		"🚨 bash · failed · exit 9",
	} {
		if !strings.Contains(plain, want) {
			t.Errorf("resumed expected rows are missing %q:\n%s", want, plain)
		}
	}
	if strings.Contains(plain, "📍 read · failed · expected missing · 666") == false {
		t.Errorf("expected read row lost a detail:\n%s", plain)
	}

	diag := stripANSI(strings.Join(m.diagRows(), "\n"))
	if !strings.Contains(diag, "📍 read") || !strings.Contains(diag, "expected missing") {
		t.Errorf("/diag does not show the expected outcome:\n%s", diag)
	}
	for _, line := range m.Lines() {
		if strings.Contains(line, "📍") || strings.Contains(line, "expected") {
			t.Errorf("expectation metadata entered stored text: %q", line)
		}
	}

	m.applyToolIcons(toolIconsClassic)
	classic := stripANSI(strings.Join(m.layoutRows(), "\n"))
	if !strings.Contains(classic, "⚙ bash") ||
		!strings.Contains(classic, "expected exit 17") ||
		strings.Contains(classic, "📍") {
		t.Errorf("classic mode did not preserve the original icon: \n%s", classic)
	}
}

// TestToolIconGutterAndWidths checks the fixed two-cell emoji plus one
// separator, the variation selector in ✏️, and that every line stays
// inside the display width.
func TestToolIconGutterAndWidths(t *testing.T) {
	expected := toolIconActivity("bash", "bash", "failed")
	expected.Expected = "exit:17"
	expected.ExpectedMet = true
	expected.ExitCode = intPointer(17)
	activities := []engine.ToolActivity{
		toolIconActivity("web_search", "web_search", "ok"),
		toolIconActivity("firecrawl_scrape", "firecrawl_scrape", "ok"),
		toolIconActivity("mpm_memory", "query", "ok"),
		toolIconActivity("write", "write", "ok"),
		toolIconActivity("edit", "edit", "ok"),
		toolIconActivity("package", "package", "ok"),
		toolIconActivity("bash", "bash", "ok"),
		toolIconActivity("delete", "delete", "ok"),
		toolIconActivity("mystery", "mystery", "ok"),
		expected,
	}
	for _, a := range activities {
		prefix, gutter := toolIconPrefix(a, toolIconsEmoji)
		if gutter != emojiToolGutter {
			t.Errorf("%s gutter = %d, want %d", a.Name, gutter, emojiToolGutter)
		}
		if got := lipgloss.Width(prefix); got != emojiToolGutter {
			t.Errorf("%s prefix %q is %d cells, want %d", a.Name, prefix, got, emojiToolGutter)
		}
		line := toolActivityLineMode(a, 80, toolIconsEmoji)
		if !strings.HasPrefix(line, prefix) {
			t.Errorf("%s line %q does not start with %q", a.Name, line, prefix)
		}
		if got := displayColumn(line, strings.TrimPrefix(line, prefix)); got != emojiToolGutter {
			t.Errorf("%s content starts at cell %d, want %d: %q", a.Name, got, emojiToolGutter, line)
		}
	}

	// ✏️ must keep its variation selector: it is one two-cell grapheme,
	// not a one-cell pencil followed by a stray selector.
	edit := toolActivityLineMode(toolIconActivity("edit", "edit", "ok"), 80, toolIconsEmoji)
	if !strings.Contains(edit, "✏\uFE0F") {
		t.Errorf("edit line lost the variation selector: %q", edit)
	}
	if got := lipgloss.Width("✏\uFE0F"); got != 2 {
		t.Errorf("✏️ width = %d, want 2", got)
	}
	if got := truncateToolCells("✏\uFE0Fabcdef", 3); got != "✏\uFE0F…" {
		t.Errorf("truncated variation selector = %q, want %q", got, "✏\uFE0F…")
	}

	for _, width := range []int{8, 12, 20, 40, 80, 170} {
		for _, a := range activities {
			line := toolActivityLineMode(a, width, toolIconsEmoji)
			if got := lipgloss.Width(line); got > width {
				t.Errorf("%s at width %d is %d cells: %q", a.Name, width, got, line)
			}
			if strings.Contains(line, "\n") {
				t.Errorf("%s at width %d wrapped: %q", a.Name, width, line)
			}
			prefix, _ := toolIconPrefix(a, toolIconsEmoji)
			if !strings.HasPrefix(line, prefix) {
				t.Errorf("%s at width %d cut the icon: %q", a.Name, width, line)
			}
		}
	}

	// A long target and a long tool name truncate without wrapping, and
	// without splitting the variation selector if one lands at the cut.
	long := toolIconActivity("read", "read", "ok")
	long.Target = strings.Repeat("segment/", 10) + "✏\uFE0F.md"
	for _, width := range []int{20, 40} {
		line := toolActivityLineMode(long, width, toolIconsEmoji)
		if got := lipgloss.Width(line); got > width {
			t.Errorf("long target at width %d is %d cells: %q", width, got, line)
		}
		if strings.Contains(line, "\n") {
			t.Errorf("long target at width %d wrapped: %q", width, line)
		}
		if !strings.HasPrefix(line, "📄 ") {
			t.Errorf("long target at width %d lost the icon: %q", width, line)
		}
		if at := strings.Index(line, "\uFE0F"); at >= 0 {
			if at == 0 || !strings.Contains(line[:at], "✏") {
				t.Errorf("variation selector was separated from its base: %q", line)
			}
		}
	}

	// Classic remains exactly the old two-cell gear gutter.
	classicPrefix, gutter := toolIconPrefix(toolIconActivity("bash", "bash", "ok"), toolIconsClassic)
	if classicPrefix != "⚙ " || gutter != 2 {
		t.Errorf("classic prefix = %q/%d, want %q/2", classicPrefix, gutter, "⚙ ")
	}
}

// TestNewStartsWithEmojiIcons checks the startup default directly.
func TestNewStartsWithEmojiIcons(t *testing.T) {
	m := New(nil)
	if m.toolIcons != toolIconsEmoji {
		t.Fatalf("New() mode = %v, want emoji", m.toolIcons)
	}
	if got := m.toolIconsLabel(); got != "emoji" {
		t.Errorf("New() label = %q, want emoji", got)
	}
}

// TestToolIconToggle covers the new default and both directions of the
// session-local switch. It also checks that new and live rows follow the
// selected mode.
func TestToolIconToggle(t *testing.T) {
	m := transcriptModel(t, 100, 30)
	m.SetHistory(historicalLines(true))

	if m.toolIcons != toolIconsEmoji {
		t.Fatalf("default mode = %v, want emoji", m.toolIcons)
	}
	emoji := stripANSI(strings.Join(allEntryRows(m), "\n"))
	for _, want := range []string{"📝 write", "🚨 bash"} {
		if !strings.Contains(emoji, want) {
			t.Fatalf("default is not emoji (%q missing):\n%s", want, emoji)
		}
	}
	if strings.Contains(emoji, "⚙ write") || strings.Contains(emoji, "⚙ bash") {
		t.Errorf("default still used classic rows:\n%s", emoji)
	}
	m = typeText(m, "/tool-icons")
	m = enterCurrent(m)
	if !strings.Contains(m.Notice(), "Tool icons: emoji") {
		t.Errorf("default status notice = %q", m.Notice())
	}

	// The opt-out restores the original gear for existing and new rows.
	m = typeText(m, "/tool-icons classic")
	m = enterCurrent(m)
	if m.toolIcons != toolIconsClassic {
		t.Fatalf("mode after /tool-icons classic = %v", m.toolIcons)
	}
	classic := stripANSI(strings.Join(allEntryRows(m), "\n"))
	if !strings.Contains(classic, "⚙ write") || !strings.Contains(classic, "⚙ bash") {
		t.Errorf("classic was not restored:\n%s", classic)
	}
	if strings.Contains(classic, "📝 write") || strings.Contains(classic, "🚨 bash") {
		t.Errorf("emoji survived the switch to classic:\n%s", classic)
	}
	i := m.appendEntry("assistant", "classic turn")
	m.entries[i].tools = []engine.ToolActivity{toolIconActivity("read", "read", "ok")}
	m.renderEntry(i)
	m.renderConversation()
	if got := stripANSI(strings.Join(m.entries[i].rows, "\n")); !strings.Contains(got, "⚙ read") {
		t.Errorf("new tool row did not use classic mode: %q", got)
	}

	live := permissionModel()
	live.applyToolIcons(toolIconsClassic)
	updated, _ := live.Update(streamToolMsg{id: 0,
		activity: toolIconActivity("web_search", "web_search", "ok")})
	live = updated.(Model)
	if !strings.Contains(live.Notice(), "⚙ web_search") {
		t.Errorf("live tool notice did not use classic mode: %q", live.Notice())
	}

	// No argument reports classic; switching back uses emoji again.
	m = typeText(m, "/tool-icons")
	m = enterCurrent(m)
	if !strings.Contains(m.Notice(), "Tool icons: classic") {
		t.Errorf("classic status notice = %q", m.Notice())
	}
	m = typeText(m, "/tool-icons emoji")
	m = enterCurrent(m)
	if m.toolIcons != toolIconsEmoji {
		t.Fatalf("mode after /tool-icons emoji = %v", m.toolIcons)
	}
	back := stripANSI(strings.Join(allEntryRows(m), "\n"))
	if !strings.Contains(back, "📝 write") || !strings.Contains(back, "🚨 bash") {
		t.Errorf("emoji was not restored:\n%s", back)
	}
	if strings.Contains(back, "⚙ write") || strings.Contains(back, "⚙ bash") {
		t.Errorf("classic survived the switch to emoji:\n%s", back)
	}

	i = m.appendEntry("assistant", "emoji turn")
	m.entries[i].tools = []engine.ToolActivity{toolIconActivity("read", "read", "ok")}
	m.renderEntry(i)
	m.renderConversation()
	if got := stripANSI(strings.Join(m.entries[i].rows, "\n")); !strings.Contains(got, "📄 read") {
		t.Errorf("new tool row did not use emoji mode: %q", got)
	}

	live = permissionModel()
	live.applyToolIcons(toolIconsEmoji)
	updated, _ = live.Update(streamToolMsg{id: 0,
		activity: toolIconActivity("web_search", "web_search", "ok")})
	live = updated.(Model)
	if !strings.Contains(live.Notice(), "🔎 web_search") {
		t.Errorf("live tool notice did not use emoji mode: %q", live.Notice())
	}

	// Unknown arguments leave the current appearance untouched.
	m = typeText(m, "/tool-icons loud")
	m = enterCurrent(m)
	if m.toolIcons != toolIconsEmoji || !strings.Contains(m.Notice(), "classic|emoji") {
		t.Errorf("invalid argument changed mode/notice: %v %q", m.toolIcons, m.Notice())
	}
}

// TestToolIconsCommandIsDiscoverable covers help, suggestions and Tab
// completion without a second registry.
func TestToolIconsCommandIsDiscoverable(t *testing.T) {
	m := testModel()
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = updated.(Model)
	m = typeText(m, "/tool")
	if !m.suggestOpen {
		t.Fatal("/tool did not open suggestions")
	}
	found := false
	for _, c := range m.suggestItems {
		if c.name == "/tool-icons" {
			found = true
		}
	}
	if !found {
		t.Fatalf("/tool suggestions are missing /tool-icons: %v", m.suggestItems)
	}
	m = pressKey(m, tea.KeyTab, 0, "")
	if got := m.Composer().Value(); got != "/tool-icons " {
		t.Errorf("Tab completed %q, want %q", got, "/tool-icons ")
	}

	m = testModel()
	m.width = 120
	help := stripANSI(strings.Join(m.helpRows(), "\n"))
	for _, want := range []string{"/tool-icons", "Switch tool icons"} {
		if !strings.Contains(help, want) {
			t.Errorf("help is missing %q:\n%s", want, help)
		}
	}
}

// TestToolIconsHistoricalResume checks that evidence loaded from disk
// uses the current presentation and remains outside the stored text.
func TestToolIconsHistoricalResume(t *testing.T) {
	m := transcriptModel(t, 100, 30)
	m.applyToolIcons(toolIconsEmoji)
	m.SetHistory(historicalLines(true))

	rows := stripANSI(strings.Join(m.layoutRows(), "\n"))
	for _, want := range []string{"📝 write", "🚨 bash", "exit 17", "666"} {
		if !strings.Contains(rows, want) {
			t.Errorf("resumed emoji rows are missing %q:\n%s", want, rows)
		}
	}
	if !strings.Contains(rows, "🔴") {
		t.Errorf("the assistant eye disappeared:\n%s", rows)
	}

	// Icons are presentation only: the stored conversation text and the
	// lines used by selection/copy never contain them.
	for _, line := range m.Lines() {
		for _, icon := range []string{"📝", "🚨", "⚙"} {
			if strings.Contains(line, icon) {
				t.Errorf("stored conversation line contains %q: %q", icon, line)
			}
		}
	}

	diag := stripANSI(strings.Join(m.diagRows(), "\n"))
	for _, want := range []string{"📝 write", "🚨 bash", "last tools"} {
		if !strings.Contains(diag, want) {
			t.Errorf("/diag is missing %q:\n%s", want, diag)
		}
	}
}

// TestToolIconsSelectionAndThemes checks that decoration stays out of a
// mouse selection and that every theme keeps the icon text and the eye.
func TestToolIconsSelectionAndThemes(t *testing.T) {
	for _, theme := range Themes() {
		m := transcriptModel(t, 100, 30)
		m.SetColor(true)
		m.SetTheme(theme)
		m.SetMouse(true)
		m.applyToolIcons(toolIconsEmoji)
		m.SetHistory(historicalLines(true))
		m.renderConversation()

		plain := stripANSI(strings.Join(m.layoutRows(), "\n"))
		for _, want := range []string{"📝 write", "🚨 bash", "🔴"} {
			if !strings.Contains(plain, want) {
				t.Errorf("%s: missing %q:\n%s", theme.Name, want, plain)
			}
		}
		for _, row := range m.layoutRows() {
			if got := lipgloss.Width(row); got > 100 {
				t.Errorf("%s: row is %d cells: %q", theme.Name, got, stripANSI(row))
			}
		}
		if got := strings.Count(plain, "\n🔴"); got != 1 {
			t.Errorf("%s: assistant eyes = %d, want 1", theme.Name, got)
		}

		// Selection is decoration-aware: the icon and tool text are not
		// part of the answer selection.
		last := m.conv.rows[len(m.conv.rows)-1]
		selected := m.conv.extract(cellPos{row: 0, cell: 0},
			cellPos{row: len(m.conv.rows) - 1, cell: last.width})
		for _, unwanted := range []string{"📝", "🚨", "⚙"} {
			if strings.Contains(selected, unwanted) {
				t.Errorf("%s: selection copied %q: %q", theme.Name, unwanted, selected)
			}
		}
	}
}

// TestToolIconsDoNotTouchPermissionOrEvidence checks the toggle is pure
// presentation: no mode change, no evidence rewrite, no stored glyphs.
func TestToolIconsDoNotTouchPermissionOrEvidence(t *testing.T) {
	m := permissionModel()
	m.lastTools = []engine.ToolActivity{toolIconActivity("bash", "bash", "ok")}
	beforeMode := m.PermissionMode()
	beforeTools := append([]engine.ToolActivity(nil), m.lastTools...)

	m = typeText(m, "/tool-icons emoji")
	m = enterCurrent(m)
	if m.PermissionMode() != beforeMode || beforeMode != permission.Guarded {
		t.Errorf("permission mode changed: %v -> %v", beforeMode, m.PermissionMode())
	}
	if len(m.lastTools) != len(beforeTools) || m.lastTools[0] != beforeTools[0] {
		t.Errorf("tool evidence changed: %#v -> %#v", beforeTools, m.lastTools)
	}
	for _, line := range m.Lines() {
		if strings.Contains(line, "💻") {
			t.Errorf("icon entered the stored conversation: %q", line)
		}
	}
}

// TestToolIconsAt80x9 checks the narrow frame: a tool line is clipped to
// the content width without wrapping or breaking the icon, and every
// frame row still fits the terminal.
func TestToolIconsAt80x9(t *testing.T) {
	m := transcriptModel(t, 80, 9)
	m.SetHistory([]SessionLine{{
		Role: "assistant", Text: "done", TurnID: "t1",
		Tools: []session.ToolExecution{{
			TurnID: "t1", CallID: "c1", Tool: "edit", Action: "edit",
			Mode: "666", Status: session.ToolSuccess,
		}},
	}})
	m.applyToolIcons(toolIconsEmoji)

	toolRows := 0
	for _, row := range allEntryRows(m) {
		plain := stripANSI(row)
		if strings.Contains(plain, "✏\uFE0F edit") {
			toolRows++
			if got := lipgloss.Width(row); got > m.contentWidth() {
				t.Errorf("tool row is %d cells, content width is %d: %q", got, m.contentWidth(), plain)
			}
		}
	}
	if toolRows == 0 {
		t.Fatalf("no emoji tool row rendered:\n%s", strings.Join(allEntryRows(m), "\n"))
	}
	for _, row := range m.layoutRows() {
		if got := lipgloss.Width(row); got > 80 {
			t.Errorf("80x9 row is %d cells: %q", got, stripANSI(row))
		}
	}

	// Consecutive tool rows at a normal height keep their icon column
	// and have no blank row between them.
	m = transcriptModel(t, 80, 24)
	m.SetHistory([]SessionLine{{
		Role: "assistant", Text: "ran", TurnID: "t1",
		Tools: []session.ToolExecution{
			{TurnID: "t1", CallID: "c1", Tool: "write", Action: "write", Mode: "666", Status: session.ToolSuccess},
			{TurnID: "t1", CallID: "c2", Tool: "edit", Action: "edit", Mode: "666", Status: session.ToolSuccess},
			{TurnID: "t1", CallID: "c3", Tool: "read", Action: "read", Mode: "666", Status: session.ToolSuccess},
			{TurnID: "t1", CallID: "c4", Tool: "bash", Action: "bash", Mode: "666", Status: session.ToolFailure, ExitCode: intPointer(17)},
		},
	}})
	m.applyToolIcons(toolIconsEmoji)
	var toolLines []string
	for _, row := range allEntryRows(m) {
		plain := stripANSI(row)
		for _, icon := range []string{"📝", "✏️", "📄", "🚨"} {
			if strings.Contains(plain, icon+" ") {
				toolLines = append(toolLines, plain)
				if got := displayColumn(plain, strings.TrimPrefix(plain, m.transcriptPrefix())); got != m.contentColumn() {
					t.Errorf("tool row does not start at the content column: %q", plain)
				}
			}
		}
	}
	if len(toolLines) != 4 {
		t.Fatalf("tool rows = %d, want 4:\n%s", len(toolLines), strings.Join(toolLines, "\n"))
	}
	for _, line := range toolLines {
		after := strings.TrimPrefix(line, m.transcriptPrefix())
		// Every emoji is two cells plus one separator, so the name after
		// the icon starts at column 3 inside the line. The first ASCII
		// space is the separator after the two-cell glyph.
		space := strings.IndexByte(after, ' ')
		if space < 0 {
			t.Errorf("tool row has no icon separator: %q", line)
			continue
		}
		name := after[space+1:]
		if got := displayColumn(after, name); got != emojiToolGutter {
			t.Errorf("name %q starts at %d, want %d: %q", name, got, emojiToolGutter, line)
		}
	}
}
