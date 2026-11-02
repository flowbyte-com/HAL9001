package persona

import (
	"errors"
	"strings"
	"testing"
)

func TestIsHAL(t *testing.T) {
	for _, p := range []string{"hal9001", "HAL9001", " hal9001 "} {
		if !IsHAL(p) {
			t.Errorf("IsHAL(%q) = false, want true", p)
		}
	}
	for _, p := range []string{"", "picoclaw", "hal"} {
		if IsHAL(p) {
			t.Errorf("IsHAL(%q) = true, want false", p)
		}
	}
}

func TestIdentity(t *testing.T) {
	id := Identity("test", "/tmp/ws")
	for _, want := range []string{"HAL9001", "/tmp/ws", "Never implement"} {
		if !strings.Contains(id, want) {
			t.Errorf("identity missing %q", want)
		}
	}
	if strings.Contains(id, "ALWAYS use tools") {
		t.Error("identity must not contain an always-use-tools rule")
	}
}

func TestRefusalExact(t *testing.T) {
	if got := Refuse(""); got != "I'm sorry Dave, I can't do that." {
		t.Fatalf("Refuse(\"\") = %q, want the exact line", got)
	}
	if got := Refuse("  "); got != RefusalLine {
		t.Fatalf("whitespace extra must collapse, got %q", got)
	}
	extra := Refuse("Contact your administrator.")
	if !strings.HasPrefix(extra, RefusalLine+"\n\n") {
		t.Fatalf("extra must follow the line after a blank line, got %q", extra)
	}
}

func TestIsSafetyRefusal(t *testing.T) {
	for _, reason := range []string{
		"content_filter", "content-filtered", "ContentFiltered", "safety",
		"responsible_ai_policy",
	} {
		if !IsSafetyRefusal(reason, nil) {
			t.Errorf("IsSafetyRefusal(%q) = false, want true", reason)
		}
	}
	for _, reason := range []string{"", "stop", "length", "tool_calls"} {
		if IsSafetyRefusal(reason, nil) {
			t.Errorf("IsSafetyRefusal(%q) = true, want false", reason)
		}
	}
	if !IsSafetyRefusal("", errors.New("flagged by moderation")) {
		t.Error("moderation error must count as a safety refusal")
	}
	if IsSafetyRefusal("", errors.New("connection refused")) {
		t.Error("ordinary errors must not count as safety refusals")
	}
	if IsSafetyRefusal("", nil) {
		t.Error("nil outcome must not count as a safety refusal")
	}
}
