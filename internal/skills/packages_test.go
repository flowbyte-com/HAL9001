package skills

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// The bounded read must fail closed.
//
// A package file is only handed back when the source proved it was read
// to the end. A source that stops early, reports no size, or changes its
// mind mid-read is an error: partial content never looks complete.

// stopEarlySource stops before the size it advertised.
type stopEarlySource struct {
	content []byte
	total   int64
	stopAt  int64
	reads   int
}

func (s *stopEarlySource) ListFiles(context.Context, string) ([]PackageFile, error) {
	return []PackageFile{{Path: "a.md", Size: s.total}}, nil
}

func (s *stopEarlySource) ReadChunk(_ context.Context, _, _ string, offset, limit int64) ([]byte, int64, error) {
	s.reads++
	if offset >= s.stopAt {
		return nil, s.total, nil // premature EOF: total is not reached
	}
	end := offset + limit
	if end > s.stopAt {
		end = s.stopAt
	}
	return s.content[offset:end], s.total, nil
}

func (s *stopEarlySource) Verify(context.Context, string, PackageDigest) error { return nil }

// A source that advertises more bytes than it delivers must not yield a
// successful partial read.
func TestReadBoundedRefusesPrematureEOF(t *testing.T) {
	content := []byte(strings.Repeat("x", 100))
	src := &stopEarlySource{content: content, total: 1000, stopAt: 100}
	got, err := ReadBounded(context.Background(), src, "s", "a.md", 0)
	if err == nil {
		t.Fatalf("a premature EOF returned success with %d bytes", len(got))
	}
	if got != nil {
		t.Errorf("partial bytes were returned alongside the error: %d", len(got))
	}
	if !strings.Contains(err.Error(), "ended at 100 of 1000") {
		t.Errorf("error = %v, want the truncation named", err)
	}
}

// changingSizeSource reports a different total on the second call.
type changingSizeSource struct{ calls int }

func (s *changingSizeSource) ListFiles(context.Context, string) ([]PackageFile, error) {
	return nil, nil
}

func (s *changingSizeSource) ReadChunk(_ context.Context, _, _ string, offset, limit int64) ([]byte, int64, error) {
	s.calls++
	if s.calls == 1 {
		return make([]byte, 10), 20, nil
	}
	return make([]byte, 10), 30, nil
}

func (s *changingSizeSource) Verify(context.Context, string, PackageDigest) error { return nil }

func TestReadBoundedRefusesInconsistentSize(t *testing.T) {
	if _, err := ReadBounded(context.Background(), &changingSizeSource{}, "s", "a.md", 0); err == nil {
		t.Fatal("a source that changed its reported size was accepted")
	}
}

// unknownSizeSource reports no size at all.
type unknownSizeSource struct{}

func (unknownSizeSource) ListFiles(context.Context, string) ([]PackageFile, error) {
	return nil, nil
}

func (unknownSizeSource) ReadChunk(context.Context, string, string, int64, int64) ([]byte, int64, error) {
	return []byte("abc"), -1, nil
}

func (unknownSizeSource) Verify(context.Context, string, PackageDigest) error { return nil }

func TestReadBoundedRefusesUnknownSize(t *testing.T) {
	_, err := ReadBounded(context.Background(), unknownSizeSource{}, "s", "a.md", 0)
	if err == nil {
		t.Fatal("a source that reported no size was trusted")
	}
	// It must refuse because no size was reported, not because the
	// budget eventually ran out.
	if !strings.Contains(err.Error(), "reported no size") {
		t.Errorf("error = %v, want the missing size named", err)
	}
}

// stalledSource makes no progress: it always answers with no bytes while
// claiming the file is longer.
type stalledSource struct{ reads int }

func (s *stalledSource) ListFiles(context.Context, string) ([]PackageFile, error) {
	return nil, nil
}

func (s *stalledSource) ReadChunk(context.Context, string, string, int64, int64) ([]byte, int64, error) {
	s.reads++
	return nil, 100, nil
}

func (s *stalledSource) Verify(context.Context, string, PackageDigest) error { return nil }

func TestReadBoundedStalledSourceFailsClosed(t *testing.T) {
	src := &stalledSource{}
	got, err := ReadBounded(context.Background(), src, "s", "a.md", 0)
	if err == nil {
		t.Fatalf("a stalled source returned success with %d bytes", len(got))
	}
	// It must terminate, not loop: a handful of reads at most.
	if src.reads > 2 {
		t.Errorf("the read made %d calls before giving up", src.reads)
	}
}

// A source that returns more than the chunk it was asked for is
// inconsistent.
type greedySource struct{}

func (greedySource) ListFiles(context.Context, string) ([]PackageFile, error) {
	return nil, nil
}

func (greedySource) ReadChunk(context.Context, string, string, int64, int64) ([]byte, int64, error) {
	return make([]byte, PackageChunkDefault+1), PackageChunkDefault + 1, nil
}

func (greedySource) Verify(context.Context, string, PackageDigest) error { return nil }

func TestReadBoundedRefusesOversizedChunk(t *testing.T) {
	if _, err := ReadBounded(context.Background(), greedySource{}, "s", "a.md", 0); err == nil {
		t.Fatal("a chunk larger than requested was accepted")
	}
}

// An invalid budget is refused, not silently adjusted.
func TestReadBoundedRejectsInvalidBudget(t *testing.T) {
	src := &chunkSource{content: []byte("abc"), chunk: 8}
	for _, budget := range []int64{-1, MaxPackageReadTotal + 1} {
		if _, err := ReadBounded(context.Background(), src, "s", "a.md", budget); err == nil {
			t.Errorf("budget %d was accepted", budget)
		}
	}
	// Zero is the documented "use the maximum" value, not a clamp.
	if _, err := ReadBounded(context.Background(), src, "s", "a.md", 0); err != nil {
		t.Errorf("the documented zero budget was refused: %v", err)
	}
}

// A cancelled context stops the read and reports the cancellation.
func TestReadBoundedPropagatesCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ReadBounded(ctx, &chunkSource{content: []byte("abc"), chunk: 8}, "s", "a.md", 0); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}

// A source failure reaches the caller unchanged.
func TestReadBoundedPropagatesSourceError(t *testing.T) {
	boom := errors.New("source exploded")
	src := &failingSource{err: boom}
	if _, err := ReadBounded(context.Background(), src, "s", "a.md", 0); !errors.Is(err, boom) {
		t.Fatalf("error = %v, want the source error", err)
	}
}

type failingSource struct{ err error }

func (f *failingSource) ListFiles(context.Context, string) ([]PackageFile, error) {
	return nil, f.err
}

func (f *failingSource) ReadChunk(context.Context, string, string, int64, int64) ([]byte, int64, error) {
	return nil, 0, f.err
}

func (f *failingSource) Verify(context.Context, string, PackageDigest) error { return f.err }

// A complete read returns exactly the proven content, and an empty file
// is complete rather than truncated.
func TestReadBoundedReturnsProvenContentExactly(t *testing.T) {
	content := []byte("0123456789abcdef")
	got, err := ReadBounded(context.Background(), &chunkSource{content: content, chunk: 5}, "s", "a.md", 0)
	if err != nil {
		t.Fatalf("ReadBounded: %v", err)
	}
	if string(got) != string(content) {
		t.Errorf("content = %q, want %q", got, content)
	}
	empty, err := ReadBounded(context.Background(), &chunkSource{content: nil, chunk: 8}, "s", "empty.md", 0)
	if err != nil {
		t.Fatalf("an empty file was refused: %v", err)
	}
	if len(empty) != 0 {
		t.Errorf("an empty file returned %d bytes", len(empty))
	}
}
