package tools

import (
	"context"
	"strings"

	"github.com/sipeed/picoclaw/pkg/media"
)

// CodingProfileToolNames is the provider-visible contract of the minimal
// coding profile. Order matters only for diagnostics.
var CodingProfileToolNames = []string{"read", "write", "edit", "bash"}

// displayNameSetter is implemented by file tools that render their own name in
// guidance and error messages. The coding adapter renames the shared tool
// instance so those messages match the visible contract.
type displayNameSetter interface {
	SetDisplayName(name string)
}

// alternativeToolsSetter is implemented by the write tool so its guidance can
// name only the alternatives that actually exist.
type alternativeToolsSetter interface {
	SetAlternativeTools(names []string)
}

// codingTool adapts an existing tool under a coding-profile name. Execution and
// parameters are delegated unchanged; only the name and model-facing
// description are part of the coding contract.
type codingTool struct {
	name        string
	description string
	inner       Tool
}

func (t *codingTool) Name() string { return t.name }

func (t *codingTool) Description() string { return t.description }

func (t *codingTool) Parameters() map[string]any { return t.inner.Parameters() }

func (t *codingTool) Execute(ctx context.Context, args map[string]any) *ToolResult {
	return t.inner.Execute(ctx, args)
}

func (t *codingTool) SetMediaStore(store media.MediaStore) {
	if aware, ok := t.inner.(mediaStoreAware); ok {
		aware.SetMediaStore(store)
	}
}

func setDisplayName(inner Tool, name string) {
	if setter, ok := inner.(displayNameSetter); ok {
		setter.SetDisplayName(name)
	}
}

// NewCodingReadTool wraps the configured file reader as `read`.
func NewCodingReadTool(inner Tool) Tool {
	setDisplayName(inner, "read")
	description := "Read a file from the workspace. Use offset and length to read part of a large file."
	if hasToolParameter(inner, "start_line") {
		description = "Read a text file from the workspace. Output includes line numbers. " +
			"Use start_line and max_lines to page through a large file."
	}
	return &codingTool{name: "read", description: description, inner: inner}
}

// NewCodingWriteTool wraps the file writer as `write`. editAvailable controls
// whether the guidance may point at the `edit` tool.
func NewCodingWriteTool(inner Tool, editAvailable bool) Tool {
	setDisplayName(inner, "write")
	if setter, ok := inner.(alternativeToolsSetter); ok {
		if editAvailable {
			setter.SetAlternativeTools([]string{"edit"})
		} else {
			setter.SetAlternativeTools(nil)
		}
	}
	description := "Write content to a file. By default an existing file is not replaced; " +
		"set overwrite=true to replace the whole file."
	if editAvailable {
		description += " Use edit for targeted changes that must preserve the rest of the file."
	}
	return &codingTool{name: "write", description: description, inner: inner}
}

// NewCodingEditTool wraps the exact-text editor as `edit`.
func NewCodingEditTool(inner Tool) Tool {
	setDisplayName(inner, "edit")
	return &codingTool{
		name: "edit",
		description: "Edit a file by replacing one exact, unique occurrence of old_text with new_text. " +
			"Read the file first if you need to confirm its current content.",
		inner: inner,
	}
}

// NewCodingBashTool wraps the command tool as a foreground `bash` interface.
// It forwards only command, cwd and timeout to the existing execution path, so
// isolation, deny patterns, workspace checks, output limits and timeouts remain
// in force. Background sessions, PTY control, polling and stdin actions are not
// part of the coding contract.
func NewCodingBashTool(inner Tool) Tool {
	return &codingBashTool{inner: inner}
}

type codingBashTool struct {
	inner Tool
}

func (t *codingBashTool) Name() string { return "bash" }

func (t *codingBashTool) Description() string {
	return "Run a shell command and return its output. Commands run with DeepClaw's configured " +
		"execution isolation, safety guards, timeout and output limits. Use read, write and edit " +
		"for file operations. Chain dependent commands with &&; use the cwd parameter instead of cd. " +
		"A simpler interface does not make arbitrary commands safe."
}

func (t *codingBashTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"command": map[string]any{
				"type":        "string",
				"description": "Shell command to execute.",
			},
			"cwd": map[string]any{
				"type":        "string",
				"description": "Optional working directory, relative to the workspace or an allowed path.",
			},
			"timeout": map[string]any{
				"type":        "integer",
				"description": "Optional timeout in seconds; 0 disables the timeout.",
			},
		},
		"required": []string{"command"},
	}
}

func (t *codingBashTool) Execute(ctx context.Context, args map[string]any) *ToolResult {
	command, _ := args["command"].(string)
	if strings.TrimSpace(command) == "" {
		return ErrorResult("command is required")
	}
	forwarded := map[string]any{
		"action":  "run",
		"command": command,
	}
	if cwd, ok := args["cwd"].(string); ok && strings.TrimSpace(cwd) != "" {
		forwarded["cwd"] = cwd
	}
	if timeout, ok := args["timeout"]; ok {
		// JSON arguments decode numbers as float64, but programmatic callers
		// may pass an integer. Normalize both to the execution tool's contract.
		switch value := timeout.(type) {
		case int:
			forwarded["timeout"] = float64(value)
		case int64:
			forwarded["timeout"] = float64(value)
		default:
			forwarded["timeout"] = timeout
		}
	}
	return t.inner.Execute(ctx, forwarded)
}

func (t *codingBashTool) SetMediaStore(store media.MediaStore) {
	if aware, ok := t.inner.(mediaStoreAware); ok {
		aware.SetMediaStore(store)
	}
}

func hasToolParameter(tool Tool, name string) bool {
	params := tool.Parameters()
	properties, ok := params["properties"].(map[string]any)
	if !ok {
		return false
	}
	_, ok = properties[name]
	return ok
}
