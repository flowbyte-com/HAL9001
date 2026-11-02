package provider

import (
	"context"
	"os"
	"strings"
	"testing"
)

// TestLiveSmoke hits a real OpenAI-compatible endpoint. It runs only when
// explicitly enabled with credentials in the environment — never in CI by
// default, and it never logs the key.
//
//	HAL9001_LIVE_SMOKE=1 HAL9001_ENDPOINT=... HAL9001_MODEL=... HAL9001_API_KEY=... go test -run TestLiveSmoke .
func TestLiveSmoke(t *testing.T) {
	if os.Getenv("HAL9001_LIVE_SMOKE") != "1" {
		t.Skip("live smoke test disabled (set HAL9001_LIVE_SMOKE=1 with credentials to enable)")
	}
	endpoint := strings.TrimSpace(os.Getenv("HAL9001_ENDPOINT"))
	model := strings.TrimSpace(os.Getenv("HAL9001_MODEL"))
	key := strings.TrimSpace(os.Getenv("HAL9001_API_KEY"))
	if endpoint == "" || model == "" || key == "" {
		t.Skip("live smoke test needs HAL9001_ENDPOINT, HAL9001_MODEL and HAL9001_API_KEY")
	}
	client := &OpenAICompatible{Endpoint: endpoint, APIKey: key}
	resp, err := client.Chat(context.Background(),
		[]Message{{Role: "user", Content: "Reply with exactly: smoke ok"}},
		Model{ID: model, MaxTokens: 32})
	if err != nil {
		t.Fatalf("live chat failed: %v", err)
	}
	if strings.TrimSpace(resp.Content) == "" {
		t.Fatal("live chat returned empty content")
	}
	t.Logf("live reply (%d chars)", len(resp.Content))
}
