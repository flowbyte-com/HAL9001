package provider

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// Discovery hygiene.
//
// What an endpoint says about its models is untrusted data. These tests
// hold HAL to the two rules that follow from that: an identifier is
// either valid or refused — never repaired — and a display label is
// sanitised until it is inert.

func TestSafeModelIDKeepsTheExactIdentifier(t *testing.T) {
	for _, id := range []string{
		"claude-sonnet-4-5",
		"vendor/Model-V2:latest",
		"MiXeD-Case",
		"gpt-4o-2024-08-06",
		"models/ünïcode-1",
	} {
		got, ok := SafeModelID(id)
		if !ok {
			t.Fatalf("SafeModelID(%q) refused a legitimate identifier", id)
		}
		if got != id {
			t.Fatalf("SafeModelID(%q) = %q; the identifier must not be rewritten", id, got)
		}
	}
}

func TestSafeModelIDRefusesUnsafeIdentifiers(t *testing.T) {
	cases := map[string]string{
		"empty":            "",
		"leading space":    " gpt-4o",
		"trailing space":   "gpt-4o ",
		"newline":          "gpt-\n4o",
		"escape":           "gpt-\x1b[31m4o",
		"bell":             "gpt-4o\x07",
		"delete":           "gpt-4o\x7f",
		"c1":               "gpt-4o\x9b",
		"tab":              "gpt\t4o",
		"only whitespace":  "   ",
		"osc in the name":  "gpt-4o]8;;http://evil\x07",
		"oversized":        strings.Repeat("m", MaxModelIDLength+1),
		"invalid encoding": string([]byte{0xff, 0xfe}),
	}
	for name, id := range cases {
		if got, ok := SafeModelID(id); ok {
			t.Fatalf("%s: SafeModelID(%q) = %q; it must be refused", name, id, got)
		}
	}
}

func TestSafeModelIDAcceptsTheLongestAllowedIdentifier(t *testing.T) {
	id := strings.Repeat("m", MaxModelIDLength)
	if got, ok := SafeModelID(id); !ok || got != id {
		t.Fatalf("an identifier of exactly the bound must be accepted: %q %v", got, ok)
	}
}

func TestSafeDisplayTextRemovesWholeEscapeSequences(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"ansi colour", "before\x1b[31mafter", "beforeafter"},
		{"ansi cursor", "a\x1b[2Jb", "ab"},
		{"osc title", "a\x1b]0;window title\x07b", "ab"},
		{"osc hyperlink", "a\x1b]8;;http://example.com\x1b\\link\x1b]8;;\x07b", "alinkb"},
		{"string sequence", "a\x1bPdata\x1b\\b", "ab"},
		{"unterminated escape", "a\x1b[31", "a"},
		// ESC followed by an ordinary character is a two-character
		// escape in the terminal's own grammar, so the character after
		// it belongs to the sequence and is removed with it.
		{"stray escape", "a\x1bb", "a"},
	}
	for _, tc := range cases {
		got := SafeDisplayText(tc.in, MaxModelLabelLength)
		if got != tc.want {
			t.Fatalf("%s: SafeDisplayText(%q) = %q, want %q", tc.name, tc.in, got, tc.want)
		}
		if strings.ContainsRune(got, 0x1b) {
			t.Fatalf("%s: the sanitised label still carries an escape", tc.name)
		}
	}
}

func TestSafeDisplayTextDropsControlCharactersAndBreaks(t *testing.T) {
	got := SafeDisplayText("a\x07b\x00c\x7fd\ne\rf", MaxModelLabelLength)
	want := "abcd e f"
	if got != want {
		t.Fatalf("SafeDisplayText = %q, want %q", got, want)
	}
	if strings.ContainsAny(got, "\n\r\x07\x00") {
		t.Fatalf("the sanitised label still carries a control character: %q", got)
	}
}

func TestSafeDisplayTextBoundsTheLabel(t *testing.T) {
	long := strings.Repeat("x", MaxModelLabelLength+50)
	got := SafeDisplayText(long, MaxModelLabelLength)
	if utf8.RuneCountInString(got) != MaxModelLabelLength {
		t.Fatalf("the label was not bounded: %d runes", utf8.RuneCountInString(got))
	}
	// A bound in bytes would cut a multi-byte character in half.
	multi := strings.Repeat("é", MaxModelLabelLength+10)
	got = SafeDisplayText(multi, MaxModelLabelLength)
	if !utf8.ValidString(got) {
		t.Fatalf("the bound cut a rune in half: %q", got)
	}
	if utf8.RuneCountInString(got) != MaxModelLabelLength {
		t.Fatalf("the multi-byte label was not bounded in runes: %d", utf8.RuneCountInString(got))
	}
}

func TestModelInfosRefusesUnsafeIdentifiersAndSanitisesLabels(t *testing.T) {
	entries := []map[string]any{
		{"id": "good-model"},
		{"id": "bad\x1b[31mmodel"},
		{"id": " spaced "},
		{"id": "good-model"}, // a duplicate
		{"id": "label-hostile", "display_name": "Model\x1b[31mRed\x07\nSecond line"},
		{"id": strings.Repeat("m", MaxModelIDLength+1)},
	}
	models := modelInfos(entries)
	ids := make([]string, 0, len(models))
	for _, m := range models {
		ids = append(ids, m.ID)
		if strings.ContainsRune(m.ID, 0x1b) {
			t.Fatalf("an unsafe identifier reached the listing: %q", m.ID)
		}
		if strings.ContainsRune(m.Label, 0x1b) || strings.ContainsRune(m.Label, '\n') {
			t.Fatalf("an unsanitised label reached the listing: %q", m.Label)
		}
	}
	want := []string{"good-model", "label-hostile"}
	if len(ids) != len(want) {
		t.Fatalf("ids = %#v, want %#v", ids, want)
	}
	for i, id := range want {
		if ids[i] != id {
			t.Fatalf("ids = %#v, want %#v", ids, want)
		}
	}
	for _, m := range models {
		if m.ID == "label-hostile" && m.Label != "ModelRed Second line" {
			t.Fatalf("label = %q", m.Label)
		}
	}
}
