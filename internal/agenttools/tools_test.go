package agenttools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/flowbyte-com/HAL9001/internal/permission"
	"github.com/flowbyte-com/HAL9001/internal/provider"
	"github.com/flowbyte-com/HAL9001/internal/workspace"
)

func enabled(t *testing.T) (*Executor, *permission.Controller) {
	t.Helper()
	c := permission.New()
	c.SetSession("test")
	if err := c.Enable("test"); err != nil {
		t.Fatal(err)
	}
	return New(c, nil), c
}

func guarded(t *testing.T) *Executor {
	t.Helper()
	c := permission.New()
	c.SetSession("test")
	return New(c, nil)
}

func call(name string, args map[string]any) provider.ToolCall {
	return provider.ToolCall{ID: "call-1", Name: name, Arguments: args}
}

func TestGuardedDeniesEveryPrivilegedTool(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "note.txt")
	marker := filepath.Join(dir, "ran")
	ex := guarded(t)
	cases := []provider.ToolCall{
		call("read", map[string]any{"path": path}),
		call("write", map[string]any{"path": path, "content": "x"}),
		call("edit", map[string]any{"path": path, "old_text": "a", "new_text": "b"}),
		call("bash", map[string]any{"command": "touch " + marker}),
	}
	for _, c := range cases {
		res := ex.Execute(context.Background(), c)
		if res.Status != "denied" {
			t.Errorf("%s under 444: status = %q", c.Name, res.Status)
		}
		if !strings.Contains(res.Content, "not enabled") {
			t.Errorf("%s under 444: %s", c.Name, res.Content)
		}
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("444 wrote the file")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Error("444 ran the command")
	}
}

func TestReadWriteEditAndBashOutsideWorkspace(t *testing.T) {
	ex, _ := enabled(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "note.txt")

	res := ex.Execute(context.Background(), call("write", map[string]any{
		"path": path, "content": "alpha\nbeta\ngamma\n",
	}))
	if res.Status != "ok" {
		t.Fatalf("write = %s: %s", res.Status, res.Content)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "alpha\nbeta\ngamma\n" {
		t.Fatalf("written file = %q, %v", data, err)
	}

	res = ex.Execute(context.Background(), call("read", map[string]any{
		"path": path, "offset": 2, "limit": 1,
	}))
	if res.Status != "ok" {
		t.Fatalf("read = %s: %s", res.Status, res.Content)
	}
	var got struct {
		Content   string `json:"content"`
		StartLine int    `json:"start_line"`
		EndLine   int    `json:"end_line"`
	}
	if err := json.Unmarshal([]byte(res.Content), &got); err != nil {
		t.Fatal(err)
	}
	if got.Content != "2: beta\n" || got.StartLine != 2 || got.EndLine != 2 {
		t.Errorf("read result = %#v", got)
	}

	res = ex.Execute(context.Background(), call("edit", map[string]any{
		"path": path, "old_text": "beta", "new_text": "BETA",
	}))
	if res.Status != "ok" {
		t.Fatalf("edit = %s: %s", res.Status, res.Content)
	}
	data, _ = os.ReadFile(path)
	if !strings.Contains(string(data), "BETA") || strings.Contains(string(data), "beta") {
		t.Errorf("edited file = %q", data)
	}

	res = ex.Execute(context.Background(), call("bash", map[string]any{
		"command": "printf 'out'; printf 'err' >&2; exit 7",
		"workdir": dir,
	}))
	if res.Status != "failed" || res.ExitCode == nil || *res.ExitCode != 7 {
		t.Fatalf("bash = %#v", res)
	}
	if !strings.Contains(res.Content, `"stdout":"out"`) ||
		!strings.Contains(res.Content, `"stderr":"err"`) {
		t.Errorf("bash output = %s", res.Content)
	}
}

func TestReadBoundsAndBinaryRefusal(t *testing.T) {
	ex, _ := enabled(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "lines.txt")
	var b strings.Builder
	for i := 1; i <= 100; i++ {
		b.WriteString("line ")
		b.WriteString(strconv.Itoa(i))
		b.WriteByte('\n')
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	res := ex.Execute(context.Background(), call("read", map[string]any{
		"path": path, "limit": 3,
	}))
	var got struct {
		Truncated bool `json:"truncated"`
	}
	_ = json.Unmarshal([]byte(res.Content), &got)
	if !got.Truncated {
		t.Errorf("a clipped read did not report truncation: %s", res.Content)
	}

	bin := filepath.Join(dir, "bin")
	if err := os.WriteFile(bin, []byte{1, 0, 2}, 0o644); err != nil {
		t.Fatal(err)
	}
	res = ex.Execute(context.Background(), call("read", map[string]any{"path": bin}))
	if res.Status != "failed" || !strings.Contains(res.Content, "binary") {
		t.Errorf("binary read = %s %s", res.Status, res.Content)
	}
}

func TestEditRequiresAnUnambiguousMatch(t *testing.T) {
	ex, _ := enabled(t)
	path := filepath.Join(t.TempDir(), "note.txt")
	_ = os.WriteFile(path, []byte("same same\n"), 0o644)

	res := ex.Execute(context.Background(), call("edit", map[string]any{
		"path": path, "old_text": "same", "new_text": "new",
	}))
	if res.Status != "failed" || !strings.Contains(res.Content, "occurs 2 times") {
		t.Fatalf("ambiguous edit = %s: %s", res.Status, res.Content)
	}
	res = ex.Execute(context.Background(), call("edit", map[string]any{
		"path": path, "old_text": "same", "new_text": "new", "replace_all": true,
	}))
	if res.Status != "ok" {
		t.Fatalf("replace_all = %s: %s", res.Status, res.Content)
	}
	data, _ := os.ReadFile(path)
	if string(data) != "new new\n" {
		t.Errorf("file = %q", data)
	}
}

func TestBashBoundsLargeOutput(t *testing.T) {
	ex, _ := enabled(t)
	res := ex.Execute(context.Background(), call("bash", map[string]any{
		"command": "head -c 100000 /dev/zero | tr '\\0' x",
	}))
	if res.Status != "ok" || !res.Truncated {
		t.Fatalf("status = %s truncated = %v", res.Status, res.Truncated)
	}
	if !strings.Contains(res.Content, "bytes omitted") {
		t.Errorf("large output was not bounded: %d bytes", len(res.Content))
	}
}

func TestBashTimeoutAndProcessGroupCleanup(t *testing.T) {
	ex, _ := enabled(t)
	dir := t.TempDir()
	pidfile := filepath.Join(dir, "child.pid")
	command := "sleep 30 & echo $! > " + pidfile + "; wait"
	start := time.Now()
	res := ex.Execute(context.Background(), call("bash", map[string]any{
		"command": command, "timeout_seconds": 1,
	}))
	if res.Status != "timeout" {
		t.Fatalf("status = %s: %s", res.Status, res.Content)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("timeout took %s", elapsed)
	}
	raw, err := os.ReadFile(pidfile)
	if err != nil {
		t.Fatalf("child pid was not written: %v", err)
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(raw)))
	if pid > 0 {
		for i := 0; i < 20; i++ {
			if err := syscall.Kill(pid, 0); err != nil {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		if err := syscall.Kill(pid, 0); err == nil {
			_ = syscall.Kill(pid, syscall.SIGKILL)
			t.Fatalf("child %d survived the command timeout", pid)
		}
	}
}

func TestRevoking666CancelsRunningBash(t *testing.T) {
	ex, c := enabled(t)
	done := make(chan Result, 1)
	go func() {
		done <- ex.Execute(context.Background(), call("bash", map[string]any{
			"command": "sleep 20", "timeout_seconds": 30,
		}))
	}()
	time.Sleep(200 * time.Millisecond)
	if c.Active() == 0 {
		t.Fatal("the command did not register as active")
	}
	c.Disable()
	select {
	case res := <-done:
		if res.Status != "cancelled" {
			t.Fatalf("status = %s: %s", res.Status, res.Content)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("revoking 666 did not stop the command")
	}
	if c.Active() != 0 {
		t.Fatalf("active operations after cancellation = %d", c.Active())
	}
}

func TestMalformedArgumentsNeverExecute(t *testing.T) {
	ex, _ := enabled(t)
	res := ex.Execute(context.Background(), provider.ToolCall{
		Name: "bash", Arguments: map[string]any{"_invalid": "{"},
	})
	if res.Status != "failed" || !strings.Contains(res.Content, "not valid JSON") {
		t.Fatalf("malformed = %s: %s", res.Status, res.Content)
	}
	res = ex.Execute(context.Background(), call("write", map[string]any{"path": 3, "content": true}))
	if res.Status != "failed" {
		t.Fatalf("typed args = %s: %s", res.Status, res.Content)
	}
}

func TestToolAuditDoesNotStoreContentsOrCommands(t *testing.T) {
	dir := t.TempDir()
	audit, err := workspace.OpenAudit(filepath.Join(dir, "audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	c := permission.New()
	c.SetSession("session-1")
	_ = c.Enable("session-1")
	ex := New(c, audit).WithSession(func() string { return "session-1" })
	path := filepath.Join(dir, "secret.txt")
	res := ex.Execute(context.Background(), call("write", map[string]any{
		"path": path, "content": "TOP-SECRET-CONTENT",
	}))
	if res.Status != "ok" {
		t.Fatal(res.Content)
	}
	res = ex.Execute(context.Background(), call("bash", map[string]any{
		"command": "echo TOP-SECRET-COMMAND",
	}))
	if res.Status != "ok" {
		t.Fatal(res.Content)
	}
	records, err := audit.Records()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 {
		t.Fatalf("records = %d", len(records))
	}
	raw, _ := os.ReadFile(audit.Path())
	text := string(raw)
	for _, secret := range []string{"TOP-SECRET-CONTENT", "TOP-SECRET-COMMAND"} {
		if strings.Contains(text, secret) {
			t.Errorf("audit stored %q", secret)
		}
	}
	if records[0].Tool != "write" || records[0].Kind != "tool" || records[0].Mode != "666" {
		t.Errorf("write record = %#v", records[0])
	}
	if records[1].Tool != "bash" || records[1].CommandHash == "" {
		t.Errorf("bash record = %#v", records[1])
	}
}
