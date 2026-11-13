package tui

import (
	"strings"
	"testing"
)

// A DeepSeek endpoint suggests DEEPSEEK_API_KEY no matter what it is
// called. The full display name is never turned into a variable.
func TestDeepSeekEndpointsSuggestDeepSeekKey(t *testing.T) {
	for _, tc := range []struct {
		name    string
		baseURL string
		id      string
		display string
	}{
		{"anthropic base", "https://api.deepseek.com/anthropic", "deepseek", "DeepSeek"},
		{"flash name", "https://api.deepseek.com/anthropic", "ds-flash", "DeepSeek-Flash"},
		{"reasoner name", "https://api.deepseek.com/anthropic", "ds-r", "DeepSeek-Reasoner"},
		{"openai base", "https://api.deepseek.com", "deepseek", "DeepSeek"},
		{"local fixture with deepseek id", "http://127.0.0.1:8290/v1", "deepseek", "DeepSeek"},
		{"local fixture with flash name", "http://127.0.0.1:8290/v1", "ds", "DeepSeek-Flash"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := suggestedProviderCredentialEnv(tc.baseURL, tc.id, tc.display)
			if got != "DEEPSEEK_API_KEY" {
				t.Errorf("suggestion = %q, want DEEPSEEK_API_KEY", got)
			}
			if strings.Contains(got, "FLASH") {
				t.Errorf("the full name leaked into the suggestion: %q", got)
			}
		})
	}
}

func TestMiniMaxAndOpenCodeGoSuggestions(t *testing.T) {
	if got := suggestedProviderCredentialEnv("https://api.minimax.io/v1", "mm", "MiniMax"); got != "MINIMAX_API_KEY" {
		t.Errorf("minimax = %q", got)
	}
	if got := suggestedProviderCredentialEnv("https://api.minimaxi.com/anthropic", "mm", "MiniMax"); got != "MINIMAX_API_KEY" {
		t.Errorf("minimaxi = %q", got)
	}
	if got := suggestedProviderCredentialEnv("https://opencode.ai/zen/go/v1", "go", "OpenCode Go"); got != "OPENCODE_GO_API_KEY" {
		t.Errorf("opencode-go = %q", got)
	}
	if got := suggestedProviderCredentialEnv("http://127.0.0.1:1/v1", "minimax", "MiniMax"); got != "MINIMAX_API_KEY" {
		t.Errorf("minimax by id = %q", got)
	}
}

func TestUnknownProviderHasNoProviderSuggestion(t *testing.T) {
	if got := suggestedProviderCredentialEnv("https://proxy.example.com/v1", "my-proxy", "My Proxy"); got != "" {
		t.Errorf("an unknown provider must have no provider suggestion, got %q", got)
	}
	// A name that merely contains a provider in the middle is not claimed.
	if got := providerFromIdentity("My DeepSeek Proxy"); got != "" {
		t.Errorf("a name with deepseek in the middle must not identify a provider, got %q", got)
	}
}

func TestCredentialCandidatesPreferProviderOverFullName(t *testing.T) {
	m := testModel()
	// Simulate an endpoint screen for DeepSeek-Flash on the real host.
	m.manage.actions = m.endpointRows(endpointValues{
		id: "ds-flash", name: "DeepSeek-Flash", baseURL: "https://api.deepseek.com/anthropic",
	})
	items := m.credentialCandidates("https://api.deepseek.com/anthropic", "ds-flash", "DeepSeek-Flash")
	found := false
	for _, item := range items {
		if item.value == "DEEPSEEK_API_KEY" {
			found = true
		}
		if item.value == "DEEPSEEK_FLASH_API_KEY" {
			t.Errorf("the full name was turned into a variable")
		}
	}
	if !found {
		t.Errorf("DEEPSEEK_API_KEY was not suggested: %#v", items)
	}
}
