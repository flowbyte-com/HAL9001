package fstools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/isolation"
)

func TestWriteToolsRefuseProtectedDeepClawPaths(t *testing.T) {
	isolation.ResetProtectedPaths()
	t.Cleanup(isolation.ResetProtectedPaths)

	workspace := t.TempDir()
	protectedDir := filepath.Join(workspace, "mpm-workspace")
	if err := os.MkdirAll(protectedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	database := filepath.Join(protectedDir, "memories.db")
	if err := os.WriteFile(database, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(workspace, "mpm")
	if err := os.WriteFile(executable, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := isolation.ProtectPaths(protectedDir, executable); err != nil {
		t.Fatalf("ProtectPaths: %v", err)
	}

	write := NewWriteFileTool(workspace, false)
	result := write.Execute(context.Background(), map[string]any{
		"path":    database,
		"content": "overwritten",
	})
	if !result.IsError || !strings.Contains(result.ForLLM, "DeepClaw MPM isolation") {
		t.Fatalf("write_file did not refuse the protected database: %+v", result)
	}

	result = write.Execute(context.Background(), map[string]any{
		"path":    filepath.Join(protectedDir, "new-file"),
		"content": "created",
	})
	if !result.IsError {
		t.Fatal("write_file created a file inside a protected directory")
	}

	result = write.Execute(context.Background(), map[string]any{
		"path":    executable,
		"content": "tampered",
	})
	if !result.IsError {
		t.Fatal("write_file replaced the protected MPM executable")
	}

	edit := NewEditFileTool(workspace, false)
	result = edit.Execute(context.Background(), map[string]any{
		"path":     database,
		"old_text": "original",
		"new_text": "tampered",
	})
	if !result.IsError {
		t.Fatalf("edit_file did not refuse the protected database: %+v", result)
	}

	appendTool := NewAppendFileTool(workspace, false)
	result = appendTool.Execute(context.Background(), map[string]any{
		"path":    database,
		"content": "appended",
	})
	if !result.IsError {
		t.Fatalf("append_file did not refuse the protected database: %+v", result)
	}

	data, err := os.ReadFile(database)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "original" {
		t.Fatalf("protected database was modified: %q", data)
	}

	// An ordinary workspace file must remain writable.
	normal := filepath.Join(workspace, "note.txt")
	result = write.Execute(context.Background(), map[string]any{
		"path":    normal,
		"content": "ordinary",
	})
	if result.IsError {
		t.Fatalf("ordinary workspace write was refused: %+v", result)
	}
}
