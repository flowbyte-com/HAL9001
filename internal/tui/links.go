package tui

import (
	"net/url"
	"regexp"
	"sort"
	"strings"

	lipgloss "charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	gmtext "github.com/yuin/goldmark/text"
)

// linkMarker is a zero-width separator placed around a Markdown link label
// before Glamour renders. Glamour then lays the label out at its natural
// width, without the duplicate visible destination it normally adds. After
// rendering, the marker pair is replaced with one OSC 8 hyperlink.
const linkMarker = "\u2063"

// bareURLPattern matches the visible part of a bare http(s) URL in a
// streaming row. Parentheses are excluded because they are usually prose
// punctuation; the URL is validated again before a hyperlink is emitted.
var bareURLPattern = regexp.MustCompile(`https?://[^\s<>"'` + "`" + `]+`)

// pendingMarkdownLink matches a complete labeled link while streaming. A
// partial link remains raw until its closing parenthesis arrives.
var pendingMarkdownLink = regexp.MustCompile(`\[([^\]\n]+)\]\((https?://[^)\s]+)\)`)

// prepareMarkdownLinks rewrites labeled Markdown links to marker-delimited
// labels and returns the safe destinations in source order. Glamour then
// lays out only the label, which keeps table widths, paragraph wrapping and
// long-link rows honest. Bare URLs are left for Glamour's autolink support.
//
// The source is parsed with Goldmark, so links inside fenced or inline code
// are never rewritten. Unsafe destinations (non-HTTP(S), credentials,
// control characters) keep their readable label but lose the destination
// and become non-clickable.
func prepareMarkdownLinks(source string) (string, []string) {
	if !strings.Contains(source, "[") {
		return source, nil
	}
	md := goldmark.New(goldmark.WithExtensions(extension.GFM))
	doc := md.Parser().Parse(gmtext.NewReader([]byte(source)))
	type edit struct {
		start, end int
		label      string
		href       string
	}
	var edits []edit
	ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		link, ok := n.(*ast.Link)
		if !ok {
			return ast.WalkContinue, nil
		}
		start, end, label, ok := linkSourceRange([]byte(source), link)
		if !ok {
			return ast.WalkContinue, nil
		}
		href, safe := safeLinkURL(string(link.Destination))
		if !safe {
			href = ""
		}
		edits = append(edits, edit{start: start, end: end, label: label, href: href})
		return ast.WalkContinue, nil
	})
	if len(edits) == 0 {
		return source, nil
	}
	sort.Slice(edits, func(i, j int) bool { return edits[i].start < edits[j].start })
	// Apply from the end so the earlier offsets remain valid.
	out := []byte(source)
	var links []string
	for i := len(edits) - 1; i >= 0; i-- {
		e := edits[i]
		if e.start < 0 || e.end > len(out) || e.start >= e.end {
			continue
		}
		label := e.label
		if e.href == "" {
			out = append(out[:e.start], append([]byte(label), out[e.end:]...)...)
			continue
		}
		replacement := linkMarker + label + linkMarker
		out = append(out[:e.start], append([]byte(replacement), out[e.end:]...)...)
	}
	for _, e := range edits {
		if e.href != "" {
			links = append(links, e.href)
		}
	}
	return string(out), links
}

// linkSourceRange finds the source span of a parsed link and its label. It
// derives the span from the child text segments, then scans the small amount
// of Markdown punctuation around them.
func linkSourceRange(src []byte, link *ast.Link) (start, end int, label string, ok bool) {
	min, max := -1, -1
	ast.Walk(link, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		t, ok := n.(*ast.Text)
		if !ok {
			return ast.WalkContinue, nil
		}
		if min < 0 || t.Segment.Start < min {
			min = t.Segment.Start
		}
		if t.Segment.Stop > max {
			max = t.Segment.Stop
		}
		return ast.WalkContinue, nil
	})
	if min < 0 || max <= min || max > len(src) {
		return 0, 0, "", false
	}
	open := findOpeningBracket(src, min)
	if open < 0 || open >= min {
		return 0, 0, "", false
	}
	close := findClosingBracket(src, max)
	if close < 0 || close >= len(src) {
		return 0, 0, "", false
	}
	end = close + 1
	if end < len(src) && src[end] == '(' {
		if p := findClosingParen(src, end); p > end {
			end = p + 1
		}
	} else if end < len(src) && src[end] == '[' {
		if r := findClosingBracket(src, end+1); r >= 0 {
			end = r + 1
		}
	}
	if end > len(src) || open+1 > close {
		return 0, 0, "", false
	}
	return open, end, string(src[open+1 : close]), true
}

func findOpeningBracket(src []byte, before int) int {
	for i := before - 1; i >= 0; i-- {
		if src[i] != '[' {
			continue
		}
		if i > 0 && src[i-1] == '\\' {
			continue
		}
		return i
	}
	return -1
}

func findClosingBracket(src []byte, from int) int {
	for i := from; i < len(src); i++ {
		if src[i] != ']' {
			continue
		}
		if i > 0 && src[i-1] == '\\' {
			continue
		}
		return i
	}
	return -1
}

func findClosingParen(src []byte, open int) int {
	depth := 0
	var quote byte
	for i := open; i < len(src); i++ {
		c := src[i]
		if c == '\\' {
			i++
			continue
		}
		if quote != 0 {
			if c == quote {
				quote = 0
			}
			continue
		}
		if c == '"' || c == '\'' {
			quote = c
			continue
		}
		switch c {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

// safeLinkURL normalizes one Markdown destination and reports whether it is
// safe to emit as an OSC 8 hyperlink. Only http and https are clickable.
func safeLinkURL(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.ContainsAny(raw, "\x00\x07\x1b\r\n") {
		return "", false
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil {
		return "", false
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
	default:
		return "", false
	}
	normalized := u.String()
	if strings.ContainsAny(normalized, "\x00\x07\x1b\r\n") {
		return "", false
	}
	return normalized, true
}

// applyPreparedLinks replaces the marker pairs from prepareMarkdownLinks
// with OSC 8 hyperlinks and the theme's understated link style. A marker
// imbalance fails closed: markers are removed and no partial link is emitted.
func applyPreparedLinks(rendered string, links []string, style lipgloss.Style) string {
	if len(links) == 0 {
		return strings.ReplaceAll(rendered, linkMarker, "")
	}
	var b strings.Builder
	rest := rendered
	for _, href := range links {
		i := strings.Index(rest, linkMarker)
		if i < 0 {
			return strings.ReplaceAll(rendered, linkMarker, "")
		}
		j := strings.Index(rest[i+len(linkMarker):], linkMarker)
		if j < 0 {
			return strings.ReplaceAll(rendered, linkMarker, "")
		}
		j += i + len(linkMarker)
		b.WriteString(rest[:i])
		// The label already carries Glamour's normal text styling. Strip
		// it before applying the link style: nesting a Lip Gloss style
		// around pre-styled fragments can corrupt the SGR boundaries.
		label := ansi.Strip(rest[i+len(linkMarker) : j])
		parts := strings.Split(label, "\n")
		for k, part := range parts {
			parts[k] = ansi.SetHyperlink(href) + style.Render(part) + ansi.ResetHyperlink()
		}
		b.WriteString(strings.Join(parts, "\n"))
		rest = rest[j+len(linkMarker):]
	}
	if strings.Contains(rest, linkMarker) {
		return strings.ReplaceAll(rendered, linkMarker, "")
	}
	b.WriteString(rest)
	return b.String()
}

// scrubUnsafeHyperlinks revalidates every OSC 8 span left by Glamour or the
// prepared-link pass. Non-HTTP(S) destinations (for example mailto: from an
// autolink) lose their clickable sequence but keep their visible text.
func scrubUnsafeHyperlinks(rendered string) string {
	if !strings.Contains(rendered, "\x1b]8;") {
		return rendered
	}
	var b strings.Builder
	rest := rendered
	activeSafe := false
	for {
		i := strings.Index(rest, "\x1b]8;")
		if i < 0 {
			b.WriteString(rest)
			break
		}
		b.WriteString(rest[:i])
		rest = rest[i:]
		end := strings.IndexByte(rest, '\x07')
		if end < 0 {
			// An unterminated OSC sequence is never shown as a link.
			break
		}
		seq := rest[:end+1]
		rest = rest[end+1:]
		body := strings.TrimPrefix(seq, "\x1b]8;")
		body = strings.TrimSuffix(body, "\x07")
		params, uri, found := strings.Cut(body, ";")
		if !found {
			continue
		}
		if uri == "" {
			if activeSafe {
				b.WriteString(ansi.ResetHyperlink())
			}
			activeSafe = false
			continue
		}
		href, safe := safeLinkURL(uri)
		if !safe {
			if activeSafe {
				b.WriteString(ansi.ResetHyperlink())
			}
			activeSafe = false
			continue
		}
		if activeSafe {
			b.WriteString(ansi.ResetHyperlink())
		}
		b.WriteString(ansi.SetHyperlink(href, params))
		activeSafe = true
	}
	return b.String()
}

// sanitizeMarkdown removes terminal control characters before any Markdown
// is rendered or shown while streaming. Ordinary newlines and tabs remain;
// all other C0/C1 controls, including ESC and BEL, do not.
func sanitizeMarkdown(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	s = strings.ReplaceAll(s, linkMarker, "")
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\n' || r == '\t':
			return r
		case r < 0x20, r == 0x7f, r >= 0x80 && r <= 0x9f:
			return -1
		default:
			return r
		}
	}, s)
}

// linkifyPendingText adds OSC 8 hyperlinks to bare URLs and complete labeled
// links in a streaming row. It is intentionally conservative: fenced rows are
// skipped by the caller, inline code spans are left alone, and only validated
// http(s) URLs become clickable. An incomplete link stays raw until it closes.
func linkifyPendingText(text string, style lipgloss.Style) string {
	parts := strings.Split(text, "`")
	for i := 0; i < len(parts); i += 2 {
		parts[i] = linkifyPendingSegment(parts[i], style)
	}
	return strings.Join(parts, "`")
}

func linkifyPendingSegment(text string, style lipgloss.Style) string {
	matches := pendingMarkdownLink.FindAllStringSubmatchIndex(text, -1)
	if len(matches) == 0 {
		return linkifyBareURLs(text, style)
	}
	var b strings.Builder
	last := 0
	for _, m := range matches {
		if m[0] < last {
			continue
		}
		b.WriteString(linkifyBareURLs(text[last:m[0]], style))
		label := text[m[2]:m[3]]
		href, ok := safeLinkURL(text[m[4]:m[5]])
		if ok {
			b.WriteString(ansi.SetHyperlink(href) + style.Render(ansi.Strip(label)) + ansi.ResetHyperlink())
		} else {
			b.WriteString(label)
		}
		last = m[1]
	}
	b.WriteString(linkifyBareURLs(text[last:], style))
	return b.String()
}

func linkifyBareURLs(text string, style lipgloss.Style) string {
	matches := bareURLPattern.FindAllStringIndex(text, -1)
	if len(matches) == 0 {
		return text
	}
	var b strings.Builder
	last := 0
	for _, m := range matches {
		if m[0] < last {
			continue
		}
		raw := text[m[0]:m[1]]
		raw = strings.TrimRight(raw, ".,;:!?")
		raw = strings.TrimRight(raw, ")]}")
		href, ok := safeLinkURL(raw)
		if !ok {
			continue
		}
		b.WriteString(text[last:m[0]])
		b.WriteString(ansi.SetHyperlink(href) + style.Render(raw) + ansi.ResetHyperlink())
		last = m[0] + len(raw)
	}
	if last == 0 {
		return text
	}
	b.WriteString(text[last:])
	return b.String()
}

// isFenceLine reports whether a streaming row opens or closes a fenced code
// block. URLs inside the fence are never linkified.
func isFenceLine(line string) bool {
	trimmed := strings.TrimSpace(line)
	return strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~")
}
