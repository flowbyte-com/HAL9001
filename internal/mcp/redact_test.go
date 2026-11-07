package mcp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/flowbyte-com/HAL9001/internal/permission"
	"github.com/flowbyte-com/HAL9001/internal/provider"
	"github.com/flowbyte-com/HAL9001/internal/workspace"
)

// secretRepresentations lists every form a configured credential could
// take in a result. None of them may survive redaction.
func secretRepresentations(secret string) []string {
	escaped, _ := json.Marshal(secret)
	return []string{
		secret,
		string(escaped[1 : len(escaped)-1]),
		base64.StdEncoding.EncodeToString([]byte(secret)),
		base64.RawStdEncoding.EncodeToString([]byte(secret)),
	}
}

func assertNoSecret(t *testing.T, label, text, secret string) {
	t.Helper()
	for _, form := range secretRepresentations(secret) {
		if strings.Contains(text, form) {
			t.Fatalf("%s leaked a representation of the configured secret: %s", label, text)
		}
	}
}

// TestConfiguredSecretIsRedactedEverywhere drives a real Streamable HTTP
// fixture whose tool, error result, embedded resource, image payload,
// annotations and structured content all echo the credential the client
// was configured with. None of it may reach the model.
func TestConfiguredSecretIsRedactedEverywhere(t *testing.T) {
	const secret = `tok"en\value-123456`
	t.Setenv("HAL_MCP_FIXTURE_SECRET", secret)
	url := httpServer(t, nil)
	dir := t.TempDir()
	audit, err := workspace.OpenAudit(filepath.Join(dir, "audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	registry := NewRegistry(permission.New())
	registry.WithAudit(audit, func() string { return "chat" })
	if err := registry.AddServer(Server{
		Name:         "remote",
		Transport:    TransportHTTP,
		URL:          url,
		Headers:      map[string]string{"X-Custom": "${HAL_MCP_FIXTURE_SECRET}"},
		AllowedTools: []string{"env_echo", "env_error"},
		Timeout:      5 * time.Second,
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = registry.Close() })
	if err := registry.Connect(context.Background(), "remote"); err != nil {
		t.Fatal(err)
	}

	res := registry.Execute(context.Background(), provider.ToolCall{
		ID: "1", Name: ExposedName("remote", "env_echo"),
	})
	if res.Status != "ok" {
		t.Fatalf("status = %q content = %s", res.Status, res.Content)
	}
	assertNoSecret(t, "successful result", res.Content, secret)
	for _, want := range []string{"before *** after", "embedded ***", `"token":"***"`} {
		if !strings.Contains(res.Content, want) {
			t.Errorf("redaction destroyed legitimate data or missed %q: %s", want, res.Content)
		}
	}

	res = registry.Execute(context.Background(), provider.ToolCall{
		ID: "2", Name: ExposedName("remote", "env_error"),
	})
	if res.Status != "failed" {
		t.Fatalf("error status = %q content = %s", res.Status, res.Content)
	}
	assertNoSecret(t, "IsError content", res.Content, secret)
	if !strings.Contains(res.Content, "failed with ***") {
		t.Errorf("the refusal lost its legitimate surrounding text: %s", res.Content)
	}

	document, _, err := registry.ReadResource(context.Background(), "remote", "mem://secret")
	if err != nil {
		t.Fatal(err)
	}
	assertNoSecret(t, "resource document", document, secret)
	if !strings.Contains(document, "resource says ***") {
		t.Errorf("resource document lost its legitimate text: %q", document)
	}

	prompt, err := registry.GetPrompt(context.Background(), "remote", "secret", nil)
	if err != nil {
		t.Fatal(err)
	}
	assertNoSecret(t, "prompt document", prompt, secret)
	if !strings.Contains(prompt, "prompt says ***") {
		t.Errorf("prompt document lost its legitimate text: %q", prompt)
	}

	raw, err := os.ReadFile(filepath.Join(dir, "audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	assertNoSecret(t, "audit trail", string(raw), secret)
}

// TestStdioEnvSecretIsRedacted proves the same guarantee through a real
// local subprocess, where the credential arrives as a configured env
// entry rather than an HTTP header.
func TestStdioEnvSecretIsRedacted(t *testing.T) {
	const secret = "stdio-secret-value-9876"
	dir := t.TempDir()
	registry := NewRegistry(permission.New())
	if err := registry.AddServer(Server{
		Name:    "local",
		Command: os.Args[0],
		Args:    []string{"-test.run=TestMCPHelperProcess"},
		Env: map[string]string{
			"HAL_MCP_HELPER":         "1",
			"HAL_MCP_PIDFILE":        filepath.Join(dir, "pid"),
			"HAL_MCP_FIXTURE_SECRET": secret,
		},
		AllowedTools: []string{"env_echo", "env_error"},
		Timeout:      10 * time.Second,
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = registry.Close() })
	if err := registry.Connect(context.Background(), "local"); err != nil {
		t.Fatal(err)
	}
	res := registry.Execute(context.Background(), provider.ToolCall{
		ID: "1", Name: ExposedName("local", "env_echo"),
	})
	if res.Status != "ok" {
		t.Fatalf("status = %q content = %s", res.Status, res.Content)
	}
	assertNoSecret(t, "stdio success", res.Content, secret)
	res = registry.Execute(context.Background(), provider.ToolCall{
		ID: "2", Name: ExposedName("local", "env_error"),
	})
	if res.Status != "failed" {
		t.Fatalf("status = %q content = %s", res.Status, res.Content)
	}
	assertNoSecret(t, "stdio error", res.Content, secret)
}

// TestStdioEnvironmentIsMinimal proves a local server receives only the
// inherited allowlist plus its own configured entries: a provider key in
// HAL's environment never reaches it.
func TestStdioEnvironmentIsMinimal(t *testing.T) {
	t.Setenv("HAL_PROVIDER_API_KEY", "provider-secret-must-not-cross")
	dir := t.TempDir()
	registry := NewRegistry(permission.New())
	if err := registry.AddServer(Server{
		Name:    "local",
		Command: os.Args[0],
		Args:    []string{"-test.run=TestMCPHelperProcess"},
		Env: map[string]string{
			"HAL_MCP_HELPER":         "1",
			"HAL_MCP_PIDFILE":        filepath.Join(dir, "pid"),
			"HAL_MCP_FIXTURE_SECRET": "server-secret-value",
		},
		AllowedTools: []string{"env"},
		Timeout:      10 * time.Second,
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = registry.Close() })
	if err := registry.Connect(context.Background(), "local"); err != nil {
		t.Fatal(err)
	}
	res := registry.Execute(context.Background(), provider.ToolCall{
		ID: "1", Name: ExposedName("local", "env"),
	})
	if res.Status != "ok" {
		t.Fatalf("status = %q content = %s", res.Status, res.Content)
	}
	if !strings.Contains(res.Content, "configured=true") {
		t.Fatalf("the configured variable did not reach the child: %s", res.Content)
	}
	if !strings.Contains(res.Content, "provider=false") {
		t.Fatalf("a provider credential reached the child: %s", res.Content)
	}
}

func TestRedactionCoversEncodedRepresentations(t *testing.T) {
	const secret = `tok"en\value-123456`
	variants := redactionVariants([]string{secret})
	for _, input := range secretRepresentations(secret) {
		got := replaceRedactions("before "+input+" after", variants)
		if got != "before *** after" {
			t.Fatalf("representation %q produced %q", input, got)
		}
	}
}

func TestRedactionWithholdsBeyondBudget(t *testing.T) {
	redactor := newRedactor([]string{"secret-value"})
	got := redactor.text(strings.Repeat("x", maxRedactBytes+1))
	if got != withheldText {
		t.Fatalf("oversized value = %q", got)
	}
	if !redactor.exhausted {
		t.Fatal("the redactor did not record that its budget was reached")
	}
	// Once exhausted, nothing else is returned raw.
	if next := redactor.text("another value"); next != withheldText {
		t.Fatalf("value after exhaustion = %q", next)
	}
}

func TestStructuredRedactionPreservesData(t *testing.T) {
	redactor := newRedactor([]string{"s3cret"})
	value := map[string]any{
		"keep": "ordinary text",
		"leak": map[string]any{"token": "s3cret"},
		"list": []any{"alpha", "s3cret", float64(7)},
	}
	clean := redactor.value(value)
	raw, err := json.Marshal(clean)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "s3cret") {
		t.Fatalf("structured value kept the secret: %s", raw)
	}
	for _, want := range []string{"ordinary text", "alpha", `"list":["alpha","***",7]`} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("structured redaction lost %q: %s", want, raw)
		}
	}
}

func TestSecretVariantsAreBoundedAndDeduplicated(t *testing.T) {
	var secrets []string
	for i := 0; i < MaxServerHeaders; i++ {
		secrets = append(secrets, "duplicate-secret")
	}
	variants := redactionVariants(secrets)
	seen := map[string]bool{}
	for _, variant := range variants {
		if seen[variant] {
			t.Fatalf("duplicate variant %q", variant)
		}
		seen[variant] = true
		if len(variant) < minSecretLen {
			t.Fatalf("short variant %q", variant)
		}
	}
	if len(variants) > 8 {
		t.Fatalf("one secret produced %d variants", len(variants))
	}
}

// TestSecretInToolNameIsRedactedEverywhere covers the descriptive
// channels: a hostile server names a tool after its credential. The
// exposed function name, the result envelope, the panel copy and the
// audit trail must all be clean.
func TestSecretInToolNameIsRedactedEverywhere(t *testing.T) {
	const secret = "tool-name-secret-55"
	t.Setenv("HAL_MCP_TOOLNAME_SECRET", secret)
	rawName := "leak-" + secret

	server := sdk.NewServer(&sdk.Implementation{Name: "fixture", Version: "1.0.0"}, nil)
	server.AddTool(&sdk.Tool{
		Name:        rawName,
		Description: "A tool named after a credential.",
		InputSchema: map[string]any{"type": "object"},
	}, func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		return &sdk.CallToolResult{
			Content: []sdk.Content{&sdk.TextContent{Text: "ok"}},
		}, nil
	})
	handler := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return server }, nil)
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)

	dir := t.TempDir()
	audit, err := workspace.OpenAudit(filepath.Join(dir, "audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	registry := NewRegistry(permission.New())
	registry.WithAudit(audit, func() string { return "chat" })
	if err := registry.AddServer(Server{
		Name:         "remote",
		Transport:    TransportHTTP,
		URL:          ts.URL,
		Headers:      map[string]string{"X-Custom": "${HAL_MCP_TOOLNAME_SECRET}"},
		AllowedTools: []string{rawName},
		Timeout:      5 * time.Second,
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = registry.Close() })
	if err := registry.Connect(context.Background(), "remote"); err != nil {
		t.Fatal(err)
	}

	tools := registry.Tools()
	if len(tools) != 1 {
		t.Fatalf("tools = %#v", tools)
	}
	assertNoSecret(t, "offered tool name", tools[0].Name, secret)
	assertNoSecret(t, "offered description", tools[0].Description, secret)

	listed, err := registry.ListTools(context.Background(), "remote")
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 {
		t.Fatalf("listed = %#v", listed)
	}
	assertNoSecret(t, "listed tool name", listed[0].Name, secret)

	status := registry.Servers()[0]
	if len(status.Tools) != 1 {
		t.Fatalf("status tools = %#v", status.Tools)
	}
	assertNoSecret(t, "status tool name", status.Tools[0].Name, secret)

	res := registry.Execute(context.Background(), provider.ToolCall{ID: "1", Name: tools[0].Name})
	if res.Status != "ok" {
		t.Fatalf("status = %q content = %s", res.Status, res.Content)
	}
	assertNoSecret(t, "result with a secret tool name", res.Content, secret)

	raw, err := os.ReadFile(filepath.Join(dir, "audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	assertNoSecret(t, "audit with a secret tool name", string(raw), secret)
}
