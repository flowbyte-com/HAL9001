package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// MiniMax-M3.1-Flash receives reasoning_effort. The parameter is only
// ever sent for models whose API documents it.
func TestReasoningEffortSentForMiniMax(t *testing.T) {
	for _, level := range EffortLevels {
		var captured capturedRequest
		srv := streamServer(t, []string{contentLine("ok")}, &captured)
		p := &OpenAICompatible{Endpoint: srv.URL, APIKey: "k"}
		if _, err := p.ChatStream(context.Background(),
			[]Message{{Role: "user", Content: "x"}},
			Model{ID: "MiniMax-M3.1-Flash", Effort: level},
			func(Delta) error { return nil }); err != nil {
			t.Fatal(err)
		}
		if got := captured.body["reasoning_effort"]; got != level {
			t.Errorf("level %q serialized as %v", level, got)
		}
		srv.Close()
	}
}

// The request body carries no effort parameter for a model outside the
// capable family, even when a level is configured.
func TestReasoningEffortNotSentToOtherProviders(t *testing.T) {
	for _, id := range []string{"gpt-4o", "claude-3", "llama-3.1-70b", ""} {
		var captured capturedRequest
		srv := streamServer(t, []string{contentLine("ok")}, &captured)
		p := &OpenAICompatible{Endpoint: srv.URL, APIKey: "k"}
		if _, err := p.ChatStream(context.Background(),
			[]Message{{Role: "user", Content: "x"}},
			Model{ID: id, Effort: "medium"},
			func(Delta) error { return nil }); err != nil {
			t.Fatal(err)
		}
		if _, ok := captured.body["reasoning_effort"]; ok {
			t.Errorf("model %q must not receive reasoning_effort: %v", id, captured.body)
		}
		srv.Close()
	}
}

// An unrecognised level is dropped rather than forwarded: the API only
// documents five values.
func TestInvalidEffortIsNotSent(t *testing.T) {
	var captured capturedRequest
	srv := streamServer(t, []string{contentLine("ok")}, &captured)
	defer srv.Close()
	p := &OpenAICompatible{Endpoint: srv.URL, APIKey: "k"}
	if _, err := p.ChatStream(context.Background(),
		[]Message{{Role: "user", Content: "x"}},
		Model{ID: "MiniMax-M3.1-Flash", Effort: "maximum-ish"},
		func(Delta) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if _, ok := captured.body["reasoning_effort"]; ok {
		t.Errorf("invalid level forwarded: %v", captured.body["reasoning_effort"])
	}
}

func TestEffortCapability(t *testing.T) {
	cases := map[string]bool{
		"MiniMax-M3.1-Flash": true,
		"minimax-m2":         true,
		"MiniMax-Text-01":    false,
		"gpt-4o":             false,
		"":                   false,
	}
	for id, want := range cases {
		if got := EffortCapable(id); got != want {
			t.Errorf("EffortCapable(%q) = %v, want %v", id, got, want)
		}
	}
	if got := DefaultEffortFor("MiniMax-M3.1-Flash"); got != "medium" {
		t.Errorf("default effort = %q, want medium", got)
	}
	if got := DefaultEffortFor("gpt-4o"); got != "" {
		t.Errorf("default effort for an unsupported model = %q, want empty", got)
	}
	for _, level := range []string{"low", "medium", "high", "xhigh", "max", " MAX "} {
		if !ValidEffort(level) {
			t.Errorf("ValidEffort(%q) = false", level)
		}
	}
	for _, level := range []string{"", "minimal", "none", "off", "maxx"} {
		if ValidEffort(level) {
			t.Errorf("ValidEffort(%q) = true", level)
		}
	}
	if got := NormalizeEffort(" HIGH "); got != "high" {
		t.Errorf("NormalizeEffort = %q", got)
	}
	if got := NormalizeEffort("turbo"); got != "" {
		t.Errorf("NormalizeEffort(turbo) = %q", got)
	}
}

// The non-streaming path sends the same parameter.
func TestReasoningEffortSentWhenNotStreaming(t *testing.T) {
	var captured capturedRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		captured.body = body
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`)
	}))
	defer srv.Close()
	p := &OpenAICompatible{Endpoint: srv.URL, APIKey: "k"}
	if _, err := p.Chat(context.Background(),
		[]Message{{Role: "user", Content: "x"}},
		Model{ID: "MiniMax-M3.1-Flash", Effort: "xhigh"}); err != nil {
		t.Fatal(err)
	}
	if got := captured.body["reasoning_effort"]; got != "xhigh" {
		t.Errorf("non-streaming reasoning_effort = %v", got)
	}
	if _, ok := captured.body["thinking"]; ok {
		t.Error("the adapter must not invent a thinking parameter")
	}
}
