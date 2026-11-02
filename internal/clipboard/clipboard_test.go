package clipboard

import (
	"bytes"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
)

func TestCopyWritesOSC52(t *testing.T) {
	var buf bytes.Buffer
	c := OSC52{W: &buf}
	if err := c.Copy("# Hello\n\nSome *markdown*"); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.HasPrefix(out, "\x1b]52;c;") || !strings.HasSuffix(out, "\x07") {
		t.Errorf("not an OSC 52 sequence: %q", out)
	}
	payload := strings.TrimSuffix(strings.TrimPrefix(out, "\x1b]52;c;"), "\x07")
	raw, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "# Hello\n\nSome *markdown*" {
		t.Errorf("payload altered in transit: %q", raw)
	}
}

func TestCopyRejectsEmptyAndOversize(t *testing.T) {
	var buf bytes.Buffer
	c := OSC52{W: &buf}
	if err := c.Copy("   "); err == nil {
		t.Error("empty copy must fail")
	}
	if err := c.Copy(strings.Repeat("x", MaxBytes+1)); err == nil {
		t.Error("oversize copy must fail instead of truncating silently")
	}
}

func TestCopyWithoutWriterIsUnavailable(t *testing.T) {
	var c OSC52
	if err := c.Copy("hi"); !errors.Is(err, ErrUnavailable) {
		t.Errorf("expected ErrUnavailable, got %v", err)
	}
}
