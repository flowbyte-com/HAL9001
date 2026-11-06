package agenttools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flowbyte-com/HAL9001/internal/provider"
	"github.com/flowbyte-com/HAL9001/internal/workspace"
)

func TestReadExpectMissing(t *testing.T) {
	ex, _ := enabled(t)
	dir := t.TempDir()
	missing := filepath.Join(dir, "missing.txt")

	res := ex.Execute(context.Background(), call("read", map[string]any{
		"path":           missing,
		"expect_missing": true,
	}))
	if res.Status != "failed" || !res.NotFound {
		t.Fatalf("missing read = %s notFound=%v: %s", res.Status, res.NotFound, res.Content)
	}
	if res.Expected != "missing" || !res.ExpectedMet {
		t.Errorf("expectation = %q/%v, want missing/true", res.Expected, res.ExpectedMet)
	}
	if !strings.Contains(res.Content, "no such file") {
		t.Errorf("actual failure detail was lost: %s", res.Content)
	}

	unexpected := ex.Execute(context.Background(), call("read", map[string]any{
		"path": missing,
	}))
	if unexpected.Status != "failed" || unexpected.Expected != "" || unexpected.ExpectedMet {
		t.Errorf("undeclared missing read = %#v", unexpected)
	}

	existing := filepath.Join(dir, "existing.txt")
	if err := os.WriteFile(existing, []byte("here"), 0o644); err != nil {
		t.Fatal(err)
	}
	surprise := ex.Execute(context.Background(), call("read", map[string]any{
		"path":           existing,
		"expect_missing": true,
	}))
	if surprise.Status != "ok" || surprise.Expected != "missing" || surprise.ExpectedMet {
		t.Errorf("missing expectation on an existing file = %#v", surprise)
	}
	if !strings.Contains(surprise.Content, `"content":"1: here`) {
		t.Errorf("the successful read lost its content: %s", surprise.Content)
	}

	none := ex.Execute(context.Background(), call("read", map[string]any{
		"path": existing, "expect_missing": false,
	}))
	if none.Status != "ok" || none.Expected != "" || none.ExpectedMet {
		t.Errorf("false expectation was recorded: %#v", none)
	}
}

func TestBashExpectExitCode(t *testing.T) {
	ex, _ := enabled(t)
	cases := []struct {
		name       string
		args       map[string]any
		status     string
		exit       int
		wantCode   string
		wantMet    bool
		detailWant string
	}{
		{
			name:     "matching failure",
			args:     map[string]any{"command": "exit 17", "expect_exit_code": 17},
			status:   "failed",
			exit:     17,
			wantCode: "exit:17",
			wantMet:  true,
		},
		{
			name:     "unexpected exit",
			args:     map[string]any{"command": "exit 1", "expect_exit_code": 17},
			status:   "failed",
			exit:     1,
			wantCode: "exit:17",
			wantMet:  false,
		},
		{
			name:     "expected failure succeeded",
			args:     map[string]any{"command": "exit 0", "expect_exit_code": 17},
			status:   "ok",
			exit:     0,
			wantCode: "exit:17",
			wantMet:  false,
		},
		{
			name:     "matching success is not an expected failure",
			args:     map[string]any{"command": "exit 0", "expect_exit_code": 0},
			status:   "ok",
			exit:     0,
			wantCode: "exit:0",
			wantMet:  true,
		},
		{
			name:     "undeclared failure",
			args:     map[string]any{"command": "exit 17"},
			status:   "failed",
			exit:     17,
			wantCode: "",
			wantMet:  false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := ex.Execute(context.Background(), call("bash", tc.args))
			if res.Status != tc.status {
				t.Errorf("status = %q, want %q: %s", res.Status, tc.status, res.Content)
			}
			if res.ExitCode == nil || *res.ExitCode != tc.exit {
				t.Errorf("exit = %v, want %d", res.ExitCode, tc.exit)
			}
			if res.Expected != tc.wantCode || res.ExpectedMet != tc.wantMet {
				t.Errorf("expectation = %q/%v, want %q/%v",
					res.Expected, res.ExpectedMet, tc.wantCode, tc.wantMet)
			}
		})
	}
}

func TestInvalidExpectationsNeverExecute(t *testing.T) {
	ex, _ := enabled(t)
	marker := filepath.Join(t.TempDir(), "ran")
	cases := []struct {
		name string
		call provider.ToolCall
	}{
		{
			name: "expect_missing on bash",
			call: call("bash", map[string]any{
				"command": "touch " + marker, "expect_missing": true,
			}),
		},
		{
			name: "expect_exit_code on read",
			call: call("read", map[string]any{
				"path": marker, "expect_exit_code": 17,
			}),
		},
		{
			name: "both expectations",
			call: call("read", map[string]any{
				"path": marker, "expect_missing": true, "expect_exit_code": 17,
			}),
		},
		{
			name: "non-boolean missing expectation",
			call: call("read", map[string]any{
				"path": marker, "expect_missing": "yes",
			}),
		},
		{
			name: "non-integer exit expectation",
			call: call("bash", map[string]any{
				"command": "touch " + marker, "expect_exit_code": "17",
			}),
		},
		{
			name: "out-of-range exit expectation",
			call: call("bash", map[string]any{
				"command": "touch " + marker, "expect_exit_code": 256,
			}),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := ex.Execute(context.Background(), tc.call)
			if res.Status != "failed" || res.Action != "invalid" {
				t.Fatalf("result = %s/%s: %s", res.Status, res.Action, res.Content)
			}
			if res.Expected != "" || res.ExpectedMet {
				t.Errorf("invalid call recorded an expectation: %#v", res)
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatalf("the invalid call executed a side effect: %v", err)
			}
		})
	}
}

func TestExpectedMetadataReachesTheAudit(t *testing.T) {
	dir := t.TempDir()
	audit, err := workspace.OpenAudit(filepath.Join(dir, "audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	ex, _ := enabled(t)
	ex.audit = audit
	ex.WithSession(func() string { return "expected" })
	res := ex.Execute(context.Background(), call("bash", map[string]any{
		"command": "exit 17", "expect_exit_code": 17,
	}))
	if res.Expected != "exit:17" || !res.ExpectedMet {
		t.Fatalf("result = %#v", res)
	}
	records, err := audit.Records()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].Expected != "exit:17" || !records[0].ExpectedMet {
		t.Fatalf("audit record = %#v", records)
	}
	if records[0].Reason != "failed" {
		t.Errorf("actual failure reason was lost: %#v", records[0])
	}
}
