package knowledge

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flowbyte-com/HAL9001/internal/permission"
	"github.com/flowbyte-com/HAL9001/internal/provider"
	"github.com/flowbyte-com/HAL9001/internal/workspace"
)

// fakeMPM is a small executable that behaves like `mpm call`. It records the
// exact tool and payload and returns deterministic envelopes; no real MPM
// store is touched.
func fakeMPM(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	logPath := filepath.Join(dir, "calls.log")
	bin := filepath.Join(dir, "mpm")
	script := `#!/bin/sh
tool="$2"
payload="$4"
printf '%s\t%s\n' "$tool" "$payload" >> "` + logPath + `"
case "$tool:$payload" in
  mpm_memory:*'"action":"save"'*)
    printf '%s\n' '{"success":true,"id":"mem_123","fact":"stored"}'
    ;;
  mpm_memory:*'"action":"query"'*)
    printf '%s\n' 'progress line'
    printf '%s\n' '{"success":false,"error":"query failed"}'
    ;;
  mpm_memory:*'"action":"show"'*)
    printf '%s\n' '{"success":true,"memory":{"id":"mem_123"}}'
    ;;
  mpm_work:*'"action":"create"'*)
    printf '%s\n' '{"success":true,"id":"work_1","title":"t"}'
    ;;
  mpm_handoff:*'"action":"write"'*)
    printf '%s\n' '{"success":true,"id":"handoff_1"}'
    ;;
  mpm_skills:*'"action":"list"'*)
    printf '%s\n' '{"success":true,"skills":[]}'
    ;;
  *)
    printf '%s\n' '{"success":false,"error":"unexpected fake call"}'
    ;;
esac
`
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, logPath
}

func registry(t *testing.T) (*Registry, string) {
	t.Helper()
	bin, log := fakeMPM(t)
	r := NewRegistry(&Client{Bin: bin, Timeout: 5 * time.Second})
	return r, log
}

func TestToolSurfaceIsTypedAndHidesDestructiveActions(t *testing.T) {
	r, _ := registry(t)
	tools := r.Tools()
	if len(tools) != 8 {
		t.Fatalf("tools = %d", len(tools))
	}
	for _, tool := range tools {
		if tool.Name == "" || tool.Description == "" || tool.Parameters == "" {
			t.Errorf("incomplete tool: %#v", tool)
		}
	}
	all := strings.Join(func() []string {
		var out []string
		for _, tool := range tools {
			out = append(out, tool.Parameters)
		}
		return out
	}(), "\n")
	for _, forbidden := range []string{`"delete"`, `"shred"`, `"invalidate"`, `"supersede"`} {
		if strings.Contains(all, forbidden) {
			t.Errorf("destructive action %s is advertised", forbidden)
		}
	}
}

func TestMemorySaveIsOnlySuccessfulWhenMPMConfirms(t *testing.T) {
	r, log := registry(t)
	res := r.Execute(context.Background(), provider.ToolCall{
		Name: "mpm_memory",
		Arguments: map[string]any{
			"action": "save",
			"params": map[string]any{"fact": "HAL uses MPM", "tags": []string{"hal"}},
		},
	})
	if res.Status != "ok" || !strings.Contains(res.Content, `"mem_123"`) {
		t.Fatalf("save = %s: %s", res.Status, res.Content)
	}
	raw, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "mpm_memory") ||
		!strings.Contains(string(raw), `"fact":"HAL uses MPM"`) {
		t.Errorf("payload = %s", raw)
	}

	res = r.Execute(context.Background(), provider.ToolCall{
		Name: "mpm_memory",
		Arguments: map[string]any{
			"action": "query", "params": map[string]any{"query": "missing"},
		},
	})
	if res.Status != "failed" || strings.Contains(res.Content, "confirmed") {
		t.Fatalf("query = %s: %s", res.Status, res.Content)
	}
	if !strings.Contains(res.Content, "query failed") {
		t.Errorf("MPM error was lost: %s", res.Content)
	}
}

func TestAllowlistAndValidationPreventProcessSpawn(t *testing.T) {
	r, log := registry(t)
	cases := []provider.ToolCall{
		{Name: "mpm_memory", Arguments: map[string]any{"action": "delete", "params": map[string]any{"memory_id": "x"}}},
		{Name: "mpm_lessons", Arguments: map[string]any{"action": "propose", "params": map[string]any{}}},
		{Name: "mpm_memory", Arguments: map[string]any{"action": "save"}},
		{Name: "mpm_memory", Arguments: map[string]any{"action": "save", "params": map[string]any{"fact": "x"}, "operator": true}},
		{Name: "not_a_tool", Arguments: map[string]any{"action": "query"}},
	}
	for _, call := range cases {
		res := r.Execute(context.Background(), call)
		if res.Status == "ok" {
			t.Errorf("%#v was accepted", call)
		}
	}
	if raw, err := os.ReadFile(log); err == nil && len(raw) > 0 {
		t.Errorf("a rejected call reached MPM: %s", raw)
	}
}

func TestHugeMPMResultIsShortenedExplicitly(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "mpm")
	script := `#!/bin/sh
printf '{"success":true,"data":"'
head -c 100000 /dev/zero | tr '\0' x
printf '"}'
`
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	r := NewRegistry(&Client{Bin: bin, Timeout: 5 * time.Second})
	res := r.Execute(context.Background(), provider.ToolCall{
		Name: "mpm_memory",
		Arguments: map[string]any{
			"action": "show", "params": map[string]any{"memory_id": "mem_1"},
		},
	})
	if res.Status != "ok" || !res.Truncated {
		t.Fatalf("result = %s truncated=%v", res.Status, res.Truncated)
	}
	if !strings.Contains(res.Content, `"truncated":true`) ||
		!strings.Contains(res.Content, `"result_preview"`) {
		t.Errorf("shortening is not explicit: %s", res.Content)
	}
	var probe map[string]any
	if err := json.Unmarshal([]byte(res.Content), &probe); err != nil {
		t.Errorf("shortened result is not valid JSON: %v", err)
	}
}

func TestKnowledgeAuditRecordsNoPayload(t *testing.T) {
	dir := t.TempDir()
	audit, err := workspace.OpenAudit(filepath.Join(dir, "audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	bin, _ := fakeMPM(t)
	r := NewRegistry(&Client{Bin: bin, Timeout: 5 * time.Second}).
		WithAudit(audit, func() string { return "sess" }).
		WithMode(func() permission.Mode { return permission.Guarded })
	res := r.Execute(context.Background(), provider.ToolCall{
		Name: "mpm_memory",
		Arguments: map[string]any{
			"action": "save", "params": map[string]any{"fact": "SENSITIVE-FACT"},
		},
	})
	if res.Status != "ok" {
		t.Fatal(res.Content)
	}
	records, err := audit.Records()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].Tool != "mpm_memory" ||
		records[0].Action != "save" || records[0].Mode != "444" {
		t.Fatalf("records = %#v", records)
	}
	raw, _ := os.ReadFile(audit.Path())
	if strings.Contains(string(raw), "SENSITIVE-FACT") {
		t.Error("the audit trail copied the knowledge payload")
	}
}

func TestClientHandlesProgressLinesAndMissingStores(t *testing.T) {
	bin, _ := fakeMPM(t)
	c := &Client{Bin: bin, Timeout: 5 * time.Second}
	env, err := c.Call(context.Background(), "mpm_skills", "list", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !env.Success {
		t.Fatalf("envelope = %#v", env)
	}
	c = &Client{Bin: filepath.Join(t.TempDir(), "missing"), Timeout: time.Second}
	if _, err := c.Call(context.Background(), "mpm_skills", "list", nil); err == nil {
		t.Fatal("a missing MPM did not fail")
	}
}
