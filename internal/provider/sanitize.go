package provider

import (
	"strings"
	"unicode/utf8"
)

// Discovery hygiene.
//
// What an endpoint says about its models is untrusted data. A hostile
// or broken endpoint can return an identifier or a display name that
// carries terminal escape sequences, that pretends to be a table row or
// that is long enough to break the panel it is shown in.
//
// Two different rules apply to the two different fields:
//
//   - an IDENTIFIER is either valid or refused. It is never repaired,
//     because a repaired identifier is not the identifier the endpoint
//     wrote, and the endpoint is the authority on its own names. A
//     refused identifier is not listed at all.
//
//   - a LABEL is display-only, so it is sanitised rather than refused:
//     the complete escape sequences are removed, the remaining control
//     characters are dropped, a line break becomes a space, and the
//     result is bounded. What is left is inert text.

// Bounds on untrusted discovery metadata.
const (
	// MaxModelIDLength bounds a discovered model identifier, in runes.
	// A model identifier is a short name; anything longer is not one.
	MaxModelIDLength = 256
	// MaxModelLabelLength bounds a discovered display label, in runes.
	MaxModelLabelLength = 120
)

// SafeModelID reports whether a discovered identifier may be used as it
// is, and returns it unchanged when it may.
//
// An identifier travels into a provider request and into the terminal,
// so one that carries a control character, that has surrounding
// whitespace, that is empty or that is absurdly long is not a model
// identifier. It is refused, never trimmed into shape: a rewritten
// identifier would be sent to the provider under a name the endpoint
// does not know.
func SafeModelID(id string) (string, bool) {
	if id == "" || !utf8.ValidString(id) {
		return "", false
	}
	if id != strings.TrimSpace(id) {
		// Surrounding whitespace is not part of a name, and an
		// identifier that carries it is malformed rather than unusual.
		return "", false
	}
	if utf8.RuneCountInString(id) > MaxModelIDLength {
		return "", false
	}
	for _, r := range id {
		if isControlRune(r) {
			return "", false
		}
	}
	return id, true
}

// isControlRune reports whether a rune is a terminal control character.
//
// The C0 range, DEL and the C1 range are all control characters. The
// escape that starts an ANSI or OSC sequence is among them, so a rune
// that survives this test can never begin a terminal sequence.
func isControlRune(r rune) bool {
	switch {
	case r < 0x20:
		return true
	case r == 0x7f:
		return true
	case r >= 0x80 && r <= 0x9f:
		return true
	}
	return false
}

// SafeDisplayText returns text that is safe to render: one bounded line
// without terminal control characters.
//
// A complete escape sequence is removed rather than merely broken: an
// ANSI sequence or an OSC string that reaches the terminal can rewrite
// the screen, retitle the window or forge a row of the panel it is
// shown in. Removing the whole sequence leaves the surrounding text
// intact and inert.
//
// Line breaks become spaces, so a label cannot forge a row. The result
// is bounded in runes, because a bound in bytes would cut a multi-byte
// character in half.
func SafeDisplayText(s string, max int) string {
	if max <= 0 {
		max = MaxModelLabelLength
	}
	var b strings.Builder
	b.Grow(len(s))
	runes := []rune(s)
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		switch {
		case r == 0x1b:
			// An escape sequence: skip it whole.
			i = skipEscapeSequence(runes, i)
		case r == '\n' || r == '\r' || r == '\v' || r == '\f':
			// A line break becomes a space, so a label cannot forge a
			// row of the panel it is shown in. A CRLF pair is one
			// break, not two.
			if r == '\r' && i+1 < len(runes) && runes[i+1] == '\n' {
				i++
			}
			b.WriteRune(' ')
		case isControlRune(r):
			// Dropped.
		default:
			b.WriteRune(r)
		}
	}
	out := strings.TrimSpace(b.String())
	cleaned := []rune(out)
	if len(cleaned) > max {
		cleaned = cleaned[:max]
	}
	return strings.TrimSpace(string(cleaned))
}

// skipEscapeSequence returns the index of the last rune of the escape
// sequence that starts at position i.
//
// The sequences that matter are the ones a terminal acts on:
//
//   - CSI: ESC '[' parameters intermediates, a final byte in @-~;
//   - OSC: ESC ']' text, ended by BEL or by ESC '\';
//   - the string sequences: ESC 'P', ESC 'X', ESC '^' and ESC '_',
//     ended by ESC '\';
//   - any other ESC, which takes the single rune that follows it.
//
// An unterminated sequence runs to the end of the text: a half-written
// escape is still an escape.
func skipEscapeSequence(runes []rune, i int) int {
	if i+1 >= len(runes) {
		return i
	}
	switch runes[i+1] {
	case '[':
		j := i + 2
		for j < len(runes) {
			r := runes[j]
			if r >= '@' && r <= '~' {
				return j
			}
			j++
		}
		return len(runes) - 1
	case ']', 'P', 'X', '^', '_':
		j := i + 2
		for j < len(runes) {
			if runes[j] == 0x07 {
				return j
			}
			if runes[j] == 0x1b && j+1 < len(runes) && runes[j+1] == '\\' {
				return j + 1
			}
			j++
		}
		return len(runes) - 1
	default:
		return i + 1
	}
}
