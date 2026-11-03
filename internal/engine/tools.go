package engine

import (
	"errors"
	"fmt"
	"strings"

	"github.com/flowbyte-com/HAL9001/internal/capability"
	"github.com/flowbyte-com/HAL9001/internal/provider"
	"github.com/flowbyte-com/HAL9001/internal/workspace"
)

// The one operation the model may propose. Offering it grants nothing:
// the model can only ask, and the interface decides whether the operation
// ever happens.
//
// The schema asks for the complete contents so that what the operator
// reviews is exactly what is written. There is deliberately no overwrite
// flag: whether the destination exists is a fact of the filesystem, not a
// decision the model gets to make.
var writeTool = provider.Tool{
	Name: workspace.ToolName,
	Description: "Propose saving one UTF-8 text file inside the operator's HAL workspace. " +
		"This only prepares a proposal: the operator reviews the exact path and contents and " +
		"approves or rejects it in the interface. Nothing is written before that approval.",
	Parameters: `{
		"type": "object",
		"properties": {
			"path": {
				"type": "string",
				"description": "Destination relative to the workspace, for example notes/acc-fuel.md. No leading slash, no drive letter and no .. components."
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

// offeredTools is what the engine offers on a turn: the single write
// proposal, and only when a workspace is configured.
func (e *Engine) offeredTools() []provider.Tool {
	if e.workspace == nil || e.toolUnsupported {
		return nil
	}
	return []provider.Tool{writeTool}
}

// handleToolCalls reviews every call a model made and returns the text to
// add to the reply plus, at most, one prepared write.
//
// Only the write proposal is ever prepared. Every other name stays
// outside 444 and is refused exactly as before: nothing the model asks
// for can execute, and a call it repeats is refused again rather than
// prompting the operator twice.
func (e *Engine) handleToolCalls(calls []provider.ToolCall, session string, turn int) (string, *workspace.Proposal) {
	var (
		notes    []string
		proposal *workspace.Proposal
	)
	for _, call := range calls {
		if call.Name != workspace.ToolName {
			// The existing 444 boundary, unchanged: an unknown name is
			// refused, and an allowed name still has no executor here.
			if err := capability.Check(call.Name); err != nil {
				notes = append(notes, fmt.Sprintf("Requested operation %q refused: %s", call.Name, capability.Refusal()))
			} else {
				notes = append(notes, fmt.Sprintf("Operation %q is allowed but not yet implemented in this foundation.", call.Name))
			}
			continue
		}
		if e.workspace == nil {
			notes = append(notes, "I can't save files here: no workspace is configured.")
			continue
		}
		if proposal != nil {
			notes = append(notes, "Only one write can be approved per message; the second proposal was ignored.")
			continue
		}
		p, err := e.prepareWrite(call, session, turn)
		if err != nil {
			notes = append(notes, writeRefusal(err))
			continue
		}
		prepared := p
		proposal = &prepared
		notes = append(notes, "Proposed write: "+e.workspace.DisplayPath(p)+
			" ("+humanBytes(int64(p.Size()))+") — awaiting your approval.")
	}
	return strings.Join(notes, "\n"), proposal
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
