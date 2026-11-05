package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"
)

func renderLinkMarkdown(md string, width int) string {
	m := New(nil)
	return m.renderMarkdown(md, width)
}

func hasHyperlink(rendered, href string) bool {
	return strings.Contains(rendered, ";"+href+"\x07")
}

func plainLines(text string) []string {
	return strings.Split(stripANSI(text), "\n")
}

func TestMarkdownLinkUsesLabelAndOSC8(t *testing.T) {
	out := renderLinkMarkdown("Read [Article title](https://example.com/article) now.", 80)
	if !hasHyperlink(out, "https://example.com/article") {
		t.Fatalf("link is not clickable: %q", out)
	}
	if strings.Contains(stripANSI(out), "https://example.com/article") {
		t.Errorf("the destination is still visible: %q", stripANSI(out))
	}
	if !strings.Contains(stripANSI(out), "Article title") {
		t.Errorf("the label is missing: %q", stripANSI(out))
	}
}

func TestBareURLsAndPunctuationAreClickable(t *testing.T) {
	out := renderLinkMarkdown("See https://example.com/a, then http://example.com/b. (https://example.com/c)", 100)
	for _, href := range []string{
		"https://example.com/a", "http://example.com/b", "https://example.com/c",
	} {
		if !hasHyperlink(out, href) {
			t.Errorf("bare URL %q is not clickable", href)
		}
	}
	plain := stripANSI(out)
	if !strings.Contains(plain, "https://example.com/a,") ||
		!strings.Contains(plain, "http://example.com/b.") {
		t.Errorf("punctuation was swallowed: %q", plain)
	}
	if hasHyperlink(out, "https://example.com/a,") {
		t.Error("trailing punctuation entered the hyperlink destination")
	}
}

func TestUnicodeLabelAndLongWrappedLinkStayInsideWidth(t *testing.T) {
	label := "日本語の長いリンク " + strings.Repeat("label ", 12)
	href := "https://example.com/" + strings.Repeat("long-path-", 12)
	out := renderLinkMarkdown("Start ["+label+"]("+href+") end.", 40)
	if !hasHyperlink(out, href) {
		t.Fatalf("long link is not clickable: %q", out)
	}
	plain := stripANSI(out)
	if !strings.Contains(plain, "日本語の長いリンク") {
		t.Errorf("unicode label is missing: %q", plain)
	}
	if strings.Contains(plain, href) {
		t.Errorf("long destination is still visible: %q", plain)
	}
	for _, line := range plainLines(out) {
		if got := lipgloss.Width(line); got > 40 {
			t.Errorf("row is %d cells at width 40: %q", got, line)
		}
	}
}

func TestLinksInListsTablesAndReferenceDefinitions(t *testing.T) {
	md := "" +
		"1. [First item](https://example.com/1)\n" +
		"2. [Second item](https://example.com/2)\n\n" +
		"| Site | Link |\n|---|---|\n| Docs | [Documentation](https://example.com/docs) |\n\n" +
		"A [reference][ref] too.\n\n[ref]: https://example.com/ref\n"
	out := renderLinkMarkdown(md, 80)
	for _, href := range []string{
		"https://example.com/1", "https://example.com/2",
		"https://example.com/docs", "https://example.com/ref",
	} {
		if !hasHyperlink(out, href) {
			t.Errorf("list/table/reference link %q is not clickable", href)
		}
	}
	plain := stripANSI(out)
	for _, label := range []string{"First item", "Documentation", "reference"} {
		if !strings.Contains(plain, label) {
			t.Errorf("label %q is missing: %q", label, plain)
		}
	}
	for _, href := range []string{"https://example.com/1", "https://example.com/docs"} {
		if strings.Contains(plain, href) {
			t.Errorf("destination %q is visible", href)
		}
	}
	for _, line := range plainLines(out) {
		if got := lipgloss.Width(line); got > 80 {
			t.Errorf("row is %d cells at width 80: %q", got, line)
		}
	}
}

func TestCodeBlocksAndInlineCodeRemainPlain(t *testing.T) {
	md := "```text\n[link](https://example.com/code)\nhttps://example.com/code\n```\n\n`https://example.com/inline`\n"
	out := renderLinkMarkdown(md, 80)
	if strings.Contains(out, "\x1b]8;") {
		t.Errorf("code content received a hyperlink: %q", out)
	}
	plain := stripANSI(out)
	if !strings.Contains(plain, "https://example.com/code") ||
		!strings.Contains(plain, "https://example.com/inline") {
		t.Errorf("code content is not readable: %q", plain)
	}
}

func TestLinksFollowEveryThemeAccent(t *testing.T) {
	for _, theme := range Themes() {
		m := New(nil)
		m.SetTheme(theme)
		out := m.renderMarkdown("[Article](https://example.com/article)", 80)
		if !hasHyperlink(out, "https://example.com/article") {
			t.Errorf("%s: link is not clickable", theme.Name)
		}
		if r, g, b, ok := parseHex(theme.Accent); ok {
			want := fmt.Sprintf("38;2;%d;%d;%d", r, g, b)
			if !strings.Contains(out, want) {
				t.Errorf("%s: link does not use the accent colour %s", theme.Name, theme.Accent)
			}
		}
	}
}

func TestUnsafeLinksAreNotClickable(t *testing.T) {
	for _, md := range []string{
		"[click](javascript:alert(1))",
		"[click](file:///etc/passwd)",
		"[click](data:text/html,hello)",
		"[click](mailto:user@example.com)",
		"[click](https://user:pass@example.com/x)",
	} {
		out := renderLinkMarkdown(md, 80)
		if strings.Contains(out, "\x1b]8;") {
			t.Errorf("unsafe link is clickable: %q -> %q", md, out)
		}
		if !strings.Contains(stripANSI(out), "click") {
			t.Errorf("unsafe link lost its label: %q -> %q", md, stripANSI(out))
		}
	}
}

func TestControlSequencesCannotBeInjectedThroughLinks(t *testing.T) {
	md := "[safe\x1b]8;;https://evil.example\x07](https://example.com/good)\n\n" +
		"[other](https://example.com/\x1b]8;;https://evil.example\x07)"
	out := renderLinkMarkdown(md, 80)
	if !hasHyperlink(out, "https://example.com/good") {
		t.Fatal("the safe link was lost")
	}
	if strings.Contains(out, "\x1b]8;;https://evil.example\x07") {
		t.Errorf("an injected destination became a standalone hyperlink: %q", out)
	}
	if strings.Contains(stripANSI(out), "\x1b") || strings.Contains(stripANSI(out), "\x07") {
		t.Errorf("control characters survived into plain text: %q", stripANSI(out))
	}
}

func TestStreamingBareURLIsClickableButFencesAreNot(t *testing.T) {
	m := New(nil)
	rows := m.pendingRows("See https://example.com/live now")
	joined := strings.Join(rows, "\n")
	if !hasHyperlink(joined, "https://example.com/live") {
		t.Errorf("streaming URL is not clickable: %q", joined)
	}
	if !strings.Contains(stripANSI(joined), "https://example.com/live") {
		t.Errorf("streaming URL is not readable: %q", stripANSI(joined))
	}

	markdown := m.pendingRows("Read [Article title](https://example.com/live-link) now")
	markdownText := strings.Join(markdown, "\n")
	if !hasHyperlink(markdownText, "https://example.com/live-link") ||
		!strings.Contains(stripANSI(markdownText), "Article title") ||
		strings.Contains(stripANSI(markdownText), "https://example.com/live-link") {
		t.Errorf("streaming Markdown link is not label-only: %q", stripANSI(markdownText))
	}

	fence := m.pendingRows("```\nhttps://example.com/code\n```")
	if strings.Contains(strings.Join(fence, "\n"), "\x1b]8;") {
		t.Errorf("fenced streaming URL is clickable: %q", fence)
	}
	inline := m.pendingRows("`https://example.com/inline`")
	if strings.Contains(strings.Join(inline, "\n"), "\x1b]8;") {
		t.Errorf("inline-code streaming URL is clickable: %q", inline)
	}
}

func TestHistoryResumeKeepsClickableLabel(t *testing.T) {
	m := transcriptModel(t, 80, 24)
	m.SetHistory([]SessionLine{
		{Role: "assistant", Text: "Read [Article title](https://example.com/article) now.", TurnID: "t1"},
	})
	out := m.viewport.GetContent()
	if !hasHyperlink(out, "https://example.com/article") {
		t.Errorf("resumed link is not clickable: %q", out)
	}
	plain := stripANSI(out)
	if !strings.Contains(plain, "Article title") || strings.Contains(plain, "https://example.com/article") {
		t.Errorf("resumed link is not label-only: %q", plain)
	}
}

func TestMonochromeViewDropsHyperlinkSequences(t *testing.T) {
	m := transcriptModel(t, 80, 24)
	m.SetColor(false)
	m.SetHistory([]SessionLine{
		{Role: "assistant", Text: "Read [Article title](https://example.com/article).", TurnID: "t1"},
	})
	view := m.View().Content
	if strings.Contains(view, "\x1b]8;") {
		t.Errorf("monochrome view contains OSC 8: %q", view)
	}
	if !strings.Contains(view, "Article title") {
		t.Errorf("monochrome view lost the label: %q", view)
	}
}

func TestCopyReturnsCleanText(t *testing.T) {
	m := testModel()
	var copied string
	m.hooks.Copy = func(s string) error {
		copied = s
		return nil
	}
	m.lastResponse = "Read [Article title](https://example.com/article).\n\n\x1b]8;;https://evil.example\x07"
	cmd := m.copyCmd(false)
	if cmd == nil {
		t.Fatal("copy command missing")
	}
	_ = cmd()
	if strings.Contains(copied, "\x1b") || strings.Contains(copied, "\x07") {
		t.Errorf("copy contains terminal controls: %q", copied)
	}
	if !strings.Contains(copied, "Article title") {
		t.Errorf("copy lost the visible text: %q", copied)
	}
}

func TestSelectionCopiesLinkLabelWithoutDestination(t *testing.T) {
	m := testModel()
	m.SetMouse(true)
	var copied string
	m.hooks.Copy = func(s string) error {
		copied = s
		return nil
	}
	m.appendEntry("assistant", "[Article title](https://example.com/article)")
	m.renderConversation()
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(Model)
	row, ok := m.screenRowOf(0)
	if !ok {
		t.Fatal("the link row is not on screen")
	}
	m = pressPointer(m, entryPrefixCells, row)
	m = dragPointer(m, entryPrefixCells+len("Article title"), row)
	m = releasePointer(m, entryPrefixCells+len("Article title"), row)
	if !strings.Contains(copied, "Article title") {
		t.Errorf("selection lost the label: %q", copied)
	}
	if strings.Contains(copied, "https://example.com/article") || strings.Contains(copied, "\x1b") {
		t.Errorf("selection contains the destination or controls: %q", copied)
	}
}

func TestSafeLinkURL(t *testing.T) {
	for _, raw := range []string{
		"https://example.com/a", "http://example.com/b",
	} {
		if _, ok := safeLinkURL(raw); !ok {
			t.Errorf("safe URL %q refused", raw)
		}
	}
	for _, raw := range []string{
		"javascript:alert(1)", "data:text/html,x", "file:///etc/passwd",
		"mailto:user@example.com", "https://user:pass@example.com/",
		"https://example.com/\x1b]8;;x",
	} {
		if _, ok := safeLinkURL(raw); ok {
			t.Errorf("unsafe URL %q accepted", raw)
		}
	}
}
