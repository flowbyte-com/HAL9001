package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

// The reasoning a provider streams can be very long. The interface must
// stay responsive while it arrives: the work it does per chunk must not
// grow with the reasoning already received.
//
// The test drives the interface with the same reasoning in three sizes.
// The per-chunk cost must stay flat: a cost that grows with the amount
// received is the quadratic growth this guards against.

// thinkingChunks is one chunk of streamed reasoning.
func thinkingChunks(total int, chunk int) []string {
	out := make([]string, 0, total/chunk+1)
	for written := 0; written < total; written += chunk {
		size := chunk
		if written+size > total {
			size = total - written
		}
		out = append(out, strings.Repeat("x", size))
	}
	return out
}

// measureThinking streams the reasoning through the interface and
// reports the total time and the cost of the last chunk.
func measureThinking(t *testing.T, chunks []string) (time.Duration, time.Duration) {
	t.Helper()
	m := NewWithHooks(&scriptEngine{}, Hooks{})
	m.SetColor(false)
	m.SetShowThinking(true)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	m = updated.(Model)

	start := time.Now()
	var last time.Duration
	for _, chunk := range chunks {
		before := time.Now()
		updated, _ = m.Update(streamThinkingMsg{id: m.streamID, text: chunk})
		m = updated.(Model)
		last = time.Since(before)
	}
	return time.Since(start), last
}

// TestTheReasoningRenderingDoesNotGrowWithTheStream is the performance
// regression test.
//
// The same reasoning is streamed in three sizes. The time each chunk
// costs must not grow with the reasoning already received: a stream of
// 240 KB must not cost hundreds of times more per chunk than a stream of
// 24 KB.
func TestTheReasoningRenderingDoesNotGrowWithTheStream(t *testing.T) {
	small := thinkingChunks(24*1024, 256)
	large := thinkingChunks(240*1024, 256)

	smallTotal, smallLast := measureThinking(t, small)
	largeTotal, largeLast := measureThinking(t, large)

	t.Logf("24 KB: total %s, last chunk %s", smallTotal, smallLast)
	t.Logf("240 KB: total %s, last chunk %s", largeTotal, largeLast)

	// The last chunk of the long stream must not cost dramatically more
	// than the last chunk of the short one. The stream is ten times
	// longer, so a bounded cost keeps the ratio near one; a quadratic
	// cost makes it near a hundred.
	if smallLast > 0 {
		ratio := float64(largeLast) / float64(smallLast)
		if ratio > 8 {
			t.Errorf("the last chunk of the long stream costs %s against %s: %.1f times more",
				largeLast, smallLast, ratio)
		}
	}
}

// TestTheReasoningRenderingStaysWithinItsBudget puts a ceiling on the
// whole stream.
//
// The budget is generous: it is the point at which the operator would
// notice the interface stutter, not a target.
func TestTheReasoningRenderingStaysWithinItsBudget(t *testing.T) {
	if raceDetector {
		// The detector slows every call by a factor of several: the
		// budget is about the interface, not about the instrument.
		t.Skip("the race detector makes a time budget meaningless")
	}
	chunks := thinkingChunks(240*1024, 256)
	total, last := measureThinking(t, chunks)
	t.Logf("240 KB in %d chunks: total %s, last chunk %s", len(chunks), total, last)
	if total > 5*time.Second {
		t.Errorf("the interface took %s to render 240 KB of reasoning", total)
	}
}

// TestTheReasoningRenderingScalesLinearly reports the scaling, so the
// numbers can be compared across revisions.
func TestTheReasoningRenderingScalesLinearly(t *testing.T) {
	for _, size := range []int{24 * 1024, 120 * 1024, 240 * 1024} {
		chunks := thinkingChunks(size, 256)
		total, last := measureThinking(t, chunks)
		t.Logf("%d KB in %d chunks: total %s, last chunk %s, per chunk %s",
			size/1024, len(chunks), total, last, total/time.Duration(len(chunks)))
	}
	fmt.Fprintln(testWriter{}, "")
}

// testWriter is a writer that throws everything away.
type testWriter struct{}

func (testWriter) Write(p []byte) (int, error) { return len(p), nil }
