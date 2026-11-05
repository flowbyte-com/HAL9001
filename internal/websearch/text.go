package websearch

import (
	"strings"
	"unicode/utf8"
)

// collapseSpace normalizes HTML and provider text into one readable line.
func collapseSpace(s string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(s)), " ")
}

// clipRunes bounds one metadata field without cutting a UTF-8 sequence.
func clipRunes(s string, max int) string {
	s = strings.TrimSpace(s)
	if max <= 0 || s == "" {
		return s
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return strings.TrimRight(string(r[:max]), " \t\r\n") + "…"
}

// truncateUTF8 bounds a body by bytes and leaves room for an ellipsis.
func truncateUTF8(s string, max int) (string, bool) {
	if max <= 0 {
		return "", len(s) > 0
	}
	if len(s) <= max {
		return s, false
	}
	cut := max - len("…")
	if cut < 1 {
		cut = 1
	}
	if cut > len(s) {
		cut = len(s)
	}
	for cut > 0 && !utf8.ValidString(s[:cut]) {
		cut--
	}
	trimmed := strings.TrimRight(s[:cut], " \t\r\n")
	if trimmed == "" {
		trimmed = s[:cut]
	}
	return trimmed + "…", true
}

// redact prevents an upstream error body from reproducing a credential.
func redact(s, key string) string {
	key = strings.TrimSpace(key)
	if key == "" {
		return s
	}
	return strings.ReplaceAll(s, key, "[redacted]")
}
