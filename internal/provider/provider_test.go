package provider

import (
	"context"
	"testing"
)

func TestConfigValidation(t *testing.T) {
	good := Config{
		Provider:  "openai-compatible",
		Endpoint:  "https://api.example.com/v1",
		APIKeyEnv: "HAL9001_API_KEY",
		Models:    []Model{{ID: "hal-flash", MaxTokens: 1024}},
	}
	if err := good.Validate(); err != nil {
		t.Errorf("valid config rejected: %v", err)
	}
	for name, mutate := range map[string]func(*Config){
		"missing provider": func(c *Config) { c.Provider = "" },
		"missing endpoint": func(c *Config) { c.Endpoint = "" },
		"missing key env":  func(c *Config) { c.APIKeyEnv = "" },
		"no models":        func(c *Config) { c.Models = nil },
		"empty model id":   func(c *Config) { c.Models[0].ID = "" },
		"negative max":     func(c *Config) { c.Models[0].MaxTokens = -1 },
	} {
		cfg := good
		cfg.Models = append([]Model(nil), good.Models...)
		mutate(&cfg)
		if err := cfg.Validate(); err == nil {
			t.Errorf("%s: expected a validation error", name)
		}
	}
}

func TestMockProvider(t *testing.T) {
	mock := &MockProvider{Replies: []Response{{Content: "hello"}}}
	if mock.Name() != "mock" {
		t.Errorf("mock name = %q", mock.Name())
	}
	resp, err := mock.Chat(context.Background(), []Message{{Role: "user", Content: "hi"}}, Model{ID: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Content != "hello" {
		t.Errorf("reply = %q, want %q", resp.Content, "hello")
	}
	if mock.Calls != 1 {
		t.Errorf("calls = %d, want 1", mock.Calls)
	}
}
