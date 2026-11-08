package engine

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/flowbyte-com/HAL9001/internal/agenttools"
	"github.com/flowbyte-com/HAL9001/internal/permission"
	"github.com/flowbyte-com/HAL9001/internal/provider"
	"github.com/flowbyte-com/HAL9001/internal/workspace"
)

// The one operation the model may propose in 444. Offering it grants
// nothing: the model can only ask, and the interface decides whether the
// operation ever happens.
//
// The schema asks for the complete contents so that what the operator
// reviews is exactly what is written. There is deliberately no overwrite
// flag: whether the destination exists is a fact of the filesystem, not a
// decision the model gets to make.
var writeTool = provider.Tool{
	Name: workspace.ToolName,
	Description: "Propose saving one UTF-8 text file inside the operator's HAL workspace. " +
		"The workspace root is already included; name the file inside it. " +
		"This only prepares a proposal: the operator reviews the exact path and contents and " +
		"approves or rejects it in the interface. Nothing is written before that approval.",
	Parameters: `{
		"type": "object",
		"properties": {
			"path": {
				"type": "string",
				"description": "Destination inside the workspace. Prefer the path exactly as the operator named it: ~/HAL/notes/acc-fuel.md, /home/you/HAL/notes/acc-fuel.md and notes/acc-fuel.md all resolve to the same file. A relative path starts at the workspace root. Do not prepend the workspace folder to a relative path unless the operator explicitly requested a nested HAL/HAL/... directory. It must not contain .. components or leave the workspace."
			},
			"content": {
				"type": "string",
				"description": "The complete file contents, exactly as they should be saved."
			}
		},
		"required": ["path", "content"],
		"additionalProperties": false
	}`,
}

// offeredTools is what the engine offers on a turn.
//
// In 444 it is the guarded write proposal, the read-only Firecrawl research
// tools and MPM's typed knowledge tools. In 666 the guarded proposal is
// replaced by the real read/write/edit/bash set; Firecrawl and MPM tools
// remain available because they cannot edit OS files.
func (e *Engine) offeredTools() []provider.Tool {
	if e.toolUnsupported {
		return nil
	}
	var out []provider.Tool
	if e.permissionMode() == permission.Unrestricted && e.execution != nil {
		out = append(out, agenttools.Tools()...)
	} else if e.workspace != nil {
		out = append(out, writeTool)
	}
	if e.web != nil {
		out = append(out, e.web.Tools()...)
	}
	if e.knowledge != nil {
		out = append(out, e.knowledge.Tools()...)
	}
	if e.mcp != nil {
		out = append(out, e.mcp.Tools()...)
	}
	return out
}

// prepareWrite turns one call into a reviewed proposal. It reads the
// arguments strictly and hands the bytes to the workspace, which validates
// the destination and copies the content.
func (e *Engine) prepareWrite(call provider.ToolCall, session string, turn int) (workspace.Proposal, error) {
	if raw, ok := call.Arguments["_invalid"]; ok {
		_ = raw
		return workspace.Proposal{}, fmt.Errorf("%w: arguments are not valid JSON", workspace.ErrPath)
	}
	path, ok := stringArg(call.Arguments, "path")
	if !ok {
		return workspace.Proposal{}, fmt.Errorf("%w: no destination path was given", workspace.ErrPath)
	}
	content, ok := stringArg(call.Arguments, "content")
	if !ok {
		return workspace.Proposal{}, fmt.Errorf("%w: no file contents were given", workspace.ErrNotText)
	}
	return e.workspace.Prepare(workspace.Op{
		Path:    path,
		Content: []byte(content),
		Session: session,
		Turn:    turn,
	})
}

// stringArg reads one string argument without coercion: a number or an
// object is a malformed proposal, not something to reinterpret.
func stringArg(args map[string]any, name string) (string, bool) {
	if args == nil {
		return "", false
	}
	v, ok := args[name]
	if !ok {
		return "", false
	}
	s, ok := v.(string)
	return s, ok
}

// writeRefusal explains a refused proposal in one conversational line. It
// names the problem, never the workspace internals or the content.
func writeRefusal(err error) string {
	switch {
	case errors.Is(err, workspace.ErrSymlink):
		return "I can't save that: the destination goes through a symbolic link."
	case errors.Is(err, workspace.ErrNotRegular):
		return "I can't save that: the destination is not a regular file."
	case errors.Is(err, workspace.ErrTooLarge):
		return "I can't save that: the file would be larger than 1 MiB."
	case errors.Is(err, workspace.ErrNotText):
		return "I can't save that: the content is not text."
	case errors.Is(err, workspace.ErrProtected):
		return "I can't save that: the destination is a protected directory."
	case errors.Is(err, workspace.ErrPath):
		return "I can't save that: the destination must be a plain file name inside my workspace."
	default:
		return "I can't save that: the workspace refused the operation."
	}
}

// humanBytes renders a compact size for the review line.
func humanBytes(n int64) string {
	switch {
	case n <= 0:
		return "empty"
	case n < 1024:
		return fmt.Sprintf("%d bytes", n)
	case n < 1024*1024:
		return fmt.Sprintf("%.1f KiB", float64(n)/1024)
	default:
		return fmt.Sprintf("%.1f MiB", float64(n)/(1024*1024))
	}
}

// quoteJSON renders one string as a JSON value.
func quoteJSON(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// errorJSON is the compact failure shape returned to the model.
func errorJSON(tool, message string) string {
	b, _ := json.Marshal(map[string]any{
		"ok": false, "tool": tool, "error": message,
	})
	return string(b)
}

// isToolRejection reports a provider that will not accept function
// definitions at all. It lets the conversation continue without the write
// capability instead of failing every turn.
func isToolRejection(err error) bool {
	msg := strings.ToLower(err.Error())
	if !strings.Contains(msg, "tool") && !strings.Contains(msg, "function") {
		return false
	}
	for _, hint := range []string{"400", "unsupported", "not support", "does not support", "invalid", "unknown"} {
		if strings.Contains(msg, hint) {
			return true
		}
	}
	return false
}
