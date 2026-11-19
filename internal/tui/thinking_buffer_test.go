package tui

import (
	"strings"
	"testing"

	"github.com/rivo/uniseg"
)

// The layout of the reasoning block.
//
// The block is wrapped as the reasoning arrives, so the layout must be
// the layout a whole-text wrap produces: the same text, wrapped the
// same way, whether it arrived in one piece or in a thousand.

// referenceWrap is the whole-text wrap the block has to agree with.
//
// It is written out here on purpose: the block wraps as the reasoning
// arrives, and this is the layout it has to arrive at.
func referenceWrap(text string, width int) []string {
	if width < 1 {
		width = 1
	}
	var out []string
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimRight(line, " \t")
		if line == "" {
			out = append(out, "")
			continue
		}
		out = append(out, referenceWrapLine(line, width)...)
	}
	return out
}

// referenceWrapLine wraps one line of plain text within width display
// cells. Words are kept whole where they fit and broken by grapheme
// cluster where they do not.
func referenceWrapLine(line string, width int) []string {
	var out []string
	current := ""
	currentWidth := 0
	for _, word := range strings.Fields(line) {
		wordWidth := uniseg.StringWidth(word)
		if current == "" {
			if wordWidth <= width {
				current, currentWidth = word, wordWidth
				continue
			}
			parts := splitHelpWord(word, width)
			out = append(out, parts[:len(parts)-1]...)
			current, currentWidth = parts[len(parts)-1], uniseg.StringWidth(parts[len(parts)-1])
			continue
		}
		if currentWidth+1+wordWidth <= width {
			current += " " + word
			currentWidth += 1 + wordWidth
			continue
		}
		out = append(out, current)
		if wordWidth <= width {
			current, currentWidth = word, wordWidth
			continue
		}
		parts := splitHelpWord(word, width)
		out = append(out, parts[:len(parts)-1]...)
		current, currentWidth = parts[len(parts)-1], uniseg.StringWidth(parts[len(parts)-1])
	}
	if current != "" {
		out = append(out, current)
	}
	if len(out) == 0 {
		return []string{""}
	}
	return out
}

// wrapBuffer is the buffer under test, with no styling.
func wrapBuffer(width int) *thinkingBuffer {
	return newThinkingBuffer(width, func(line string) string { return line }, "plain")
}

// The block wraps a whole text exactly as the whole-text wrap does.
func TestTheBlockWrapsLikeAWholeTextWrap(t *testing.T) {
	texts := []string{
		"the model weighed two answers and then it weighed them again",
		"a",
		"a b c d e f g h i j k l m n o p q r s t u v w x y z",
		"one\ntwo\nthree",
		"one\n\ntwo",
		"trailing space ",
		"   leading space",
		strings.Repeat("x", 300),
		strings.Repeat("word ", 40),
		"short\n" + strings.Repeat("y", 250) + "\nshort again",
		"mixed  spaces   between   words",
		"tab\tseparated\twords",
	}
	for _, width := range []int{10, 37, 96} {
		for _, text := range texts {
			buf := wrapBuffer(width)
			buf.Append(text)
			buf.Flush()
			want := referenceWrap(text, width)
			got := buf.Rows()
			if len(want) != len(got) {
				t.Fatalf("width %d, text %q: %d rows, want %d\ngot:  %q\nwant: %q",
					width, text, len(got), len(want), got, want)
			}
			for i := range want {
				if want[i] != got[i] {
					t.Errorf("width %d, text %q, row %d = %q, want %q",
						width, text, i, got[i], want[i])
				}
			}
		}
	}
}

// The layout does not depend on how the reasoning arrived: the same text
// in pieces is wrapped the same way.
func TestTheLayoutDoesNotDependOnTheChunks(t *testing.T) {
	text := strings.Repeat("word ", 60) + "\n" + strings.Repeat("x", 200) + "\ntail"
	for _, size := range []int{1, 3, 17, 64, 1000} {
		buf := wrapBuffer(96)
		for i := 0; i < len(text); i += size {
			end := i + size
			if end > len(text) {
				end = len(text)
			}
			buf.Append(text[i:end])
		}
		buf.Flush()
		want := referenceWrap(text, 96)
		got := buf.Rows()
		if len(want) != len(got) {
			t.Fatalf("chunks of %d: %d rows, want %d", size, len(got), len(want))
		}
		for i := range want {
			if want[i] != got[i] {
				t.Errorf("chunks of %d, row %d = %q, want %q", size, i, got[i], want[i])
			}
		}
	}
}

// A window change re-wraps the block for the new width.
func TestTheBlockIsRewrappedForANewWidth(t *testing.T) {
	text := strings.Repeat("word ", 40)
	buf := wrapBuffer(96)
	buf.Append(text)
	buf.Flush()
	buf.SetWidth(40)
	want := referenceWrap(text, 40)
	got := buf.Rows()
	if len(want) != len(got) {
		t.Fatalf("%d rows, want %d", len(got), len(want))
	}
	for i := range want {
		if want[i] != got[i] {
			t.Errorf("row %d = %q, want %q", i, got[i], want[i])
		}
	}
}

// The window bounds what is shown: the oldest rows go first and the
// operator is told that the beginning is not shown.
func TestTheWindowDropsTheOldestRows(t *testing.T) {
	buf := wrapBuffer(96)
	for i := 0; i < maxThinkingRows*2; i++ {
		buf.Append("a row of reasoning that is long enough to be a row of its own\n")
	}
	buf.Flush()
	rows := buf.Rows()
	if len(rows) > maxThinkingRows+1 {
		t.Errorf("the block holds %d rows, want at most %d", len(rows), maxThinkingRows+1)
	}
	if !strings.Contains(rows[0], "not shown") {
		t.Errorf("the first row does not say that the beginning is not shown: %q", rows[0])
	}
}

// The reasoning is kept as the provider sent it.
func TestTheBufferKeepsTheReasoning(t *testing.T) {
	buf := wrapBuffer(96)
	buf.Append("the model weighed it")
	if buf.Text() != "the model weighed it" {
		t.Errorf("the text = %q", buf.Text())
	}
	if buf.Empty() {
		t.Error("the buffer reports no reasoning")
	}
	buf.Flush()
	if buf.Empty() {
		t.Error("the buffer reports no reasoning after the flush")
	}
}

// Reasoning that is only whitespace shows nothing.
func TestWhitespaceIsNoReasoning(t *testing.T) {
	buf := wrapBuffer(96)
	buf.Append("   \n\t ")
	if !buf.Empty() {
		t.Error("whitespace was taken for reasoning")
	}
}
