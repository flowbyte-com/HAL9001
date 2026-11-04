// Package knowledge exposes MPM's existing knowledge tools to HAL9001.
//
// MPM remains the sole owner of its store. HAL sends the documented
// `mpm call <tool> --payload ...` request and passes MPM's own parameters
// and identifiers through unchanged. There is no HAL database, no second
// memory table, and no claim of success that MPM did not return.
//
// The allowlist below is deliberately narrower than MPM's full operator
// surface: retrieval and ordinary creation/update are available, but
// irreversible shredding and operator-only lesson admission are not.
package knowledge

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/flowbyte-com/HAL9001/internal/permission"
	"github.com/flowbyte-com/HAL9001/internal/provider"
	"github.com/flowbyte-com/HAL9001/internal/workspace"
)

// DefaultTimeout bounds one MPM call.
const DefaultTimeout = 20 * time.Second

// MaxResponseBytes bounds the raw MPM envelope held in memory.
const MaxResponseBytes = 2 * 1024 * 1024

// MaxPromptBytes is the most MPM output sent back to the model in one tool
// result. The raw result is still what MPM confirmed; only the copy handed
// to the model is shortened, and that fact is explicit.
const MaxPromptBytes = 48 * 1024

// ErrUnavailable reports that the MPM executable could not be reached.
var ErrUnavailable = errors.New("knowledge: MPM is not available")

// Client calls MPM's universal tool boundary.
type Client struct {
	// Bin is the mpm executable. Empty means it is resolved.
	Bin string
	// Timeout bounds one call. Zero means DefaultTimeout.
	Timeout time.Duration
	// Env is the environment for the call. Empty means the current one.
	Env []string
}

// Envelope is one MPM tool answer. Success is true only when MPM said so.
type Envelope struct {
	Success bool
	Error   string
	Raw     []byte
}

// Call sends one action through MPM's documented tool boundary.
func (c *Client) Call(ctx context.Context, tool, action string, params map[string]any) (Envelope, error) {
	bin, err := c.binary()
	if err != nil {
		return Envelope{}, err
	}
	payload, err := json.Marshal(map[string]any{"action": action, "params": params})
	if err != nil {
		return Envelope{}, fmt.Errorf("knowledge: %w", err)
	}
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, bin, "call", tool, "--payload", string(payload))
	var stdout limitedBuffer
	stdout.limit = MaxResponseBytes
	var stderr limitedBuffer
	stderr.limit = 8192
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	cmd.WaitDelay = 2 * time.Second
	if len(c.Env) > 0 {
		cmd.Env = c.Env
	}
	runErr := cmd.Run()
	raw, err := lastJSONObject(stdout.Bytes())
	if err != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail != "" {
			return Envelope{}, fmt.Errorf("%w: %s", ErrUnavailable, firstLine(detail))
		}
		if runErr != nil {
			return Envelope{}, fmt.Errorf("%w: %w", ErrUnavailable, runErr)
		}
		if stdout.Truncated() {
			return Envelope{}, fmt.Errorf("%w: response exceeded %d bytes", ErrUnavailable, MaxResponseBytes)
		}
		return Envelope{}, fmt.Errorf("%w: no JSON envelope in the output", ErrUnavailable)
	}
	var probe struct {
		Success *bool  `json:"success"`
		Error   string `json:"error"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return Envelope{}, fmt.Errorf("%w: unreadable JSON envelope", ErrUnavailable)
	}
	if probe.Success == nil {
		// Never invent success. A response that does not carry MPM's own
		// confirmation is reported as a failure.
		return Envelope{Success: false, Error: "MPM did not confirm success", Raw: raw}, nil
	}
	return Envelope{Success: *probe.Success, Error: probe.Error, Raw: raw}, nil
}

func (c *Client) binary() (string, error) {
	if bin := strings.TrimSpace(c.Bin); bin != "" {
		return bin, nil
	}
	if bin := strings.TrimSpace(os.Getenv("HAL9001_MPM_BIN")); bin != "" {
		return bin, nil
	}
	if bin := strings.TrimSpace(os.Getenv("MPM_BIN")); bin != "" {
		return bin, nil
	}
	if path, err := exec.LookPath("mpm"); err == nil {
		return path, nil
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		for _, candidate := range []string{
			filepath.Join(home, ".mpm", "bin", "mpm"),
			filepath.Join(home, ".mpm", "current", "bin", "mpm"),
			filepath.Join(home, ".local", "bin", "mpm"),
		} {
			if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
				return candidate, nil
			}
		}
	}
	return "", ErrUnavailable
}

// limitedBuffer keeps a bounded prefix and discards the rest.
type limitedBuffer struct {
	mu        sync.Mutex
	limit     int
	buf       []byte
	truncated bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.limit <= 0 {
		return len(p), nil
	}
	room := b.limit - len(b.buf)
	if room <= 0 {
		b.truncated = true
		return len(p), nil
	}
	if len(p) > room {
		b.buf = append(b.buf, p[:room]...)
		b.truncated = true
		return len(p), nil
	}
	b.buf = append(b.buf, p...)
	return len(p), nil
}

func (b *limitedBuffer) Bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]byte(nil), b.buf...)
}

func (b *limitedBuffer) String() string { return string(b.Bytes()) }

func (b *limitedBuffer) Truncated() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.truncated
}

// lastJSONObject returns the last complete JSON object in MPM's output.
func lastJSONObject(out []byte) ([]byte, error) {
	lines := bytes.Split(out, []byte("\n"))
	for i := len(lines) - 1; i >= 0; i-- {
		line := bytes.TrimSpace(lines[i])
		if len(line) < 2 || line[0] != '{' || line[len(line)-1] != '}' {
			continue
		}
		var probe map[string]any
		if err := json.Unmarshal(line, &probe); err == nil {
			return line, nil
		}
	}
	return nil, ErrUnavailable
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// ToolDef is one MPM-backed tool HAL offers. Actions is HAL's allowlist;
// parameters are passed to MPM, whose own validation remains authoritative.
type ToolDef struct {
	Name        string
	Description string
	Schema      string
	Actions     []string
}

var defs = []ToolDef{
	{
		Name: "mpm_memory",
		Description: "Retrieve, inspect or save MPM memories. " +
			"Actions query and show are retrieval; save creates a durable memory in MPM. " +
			"This is MPM knowledge storage, not filesystem access. A save is only reported as successful when MPM confirms it with an id.",
		Actions: []string{"query", "show", "save"},
		Schema: `{
			"type": "object",
			"properties": {
				"action": {"type": "string", "enum": ["query", "show", "save"], "description": "query/show retrieve; save creates."},
				"params": {
					"type": "object",
					"properties": {
						"fact": {"type": "string", "description": "save: the durable fact to remember."},
						"query": {"type": "string", "description": "query: the FTS query."},
						"memory_id": {"type": "string", "description": "show: MPM memory id."},
						"collection": {"type": "string"},
						"tags": {"type": "array", "items": {"type": "string"}},
						"weight": {"type": "number"},
						"limit": {"type": "number"},
						"scope": {"type": "string", "enum": ["all", "local", "shared"]},
						"projection": {"type": "string", "enum": ["summary", "full"]}
					},
					"additionalProperties": true
				}
			},
			"required": ["action"],
			"additionalProperties": false
		}`,
	},
	{
		Name: "mpm_lessons",
		Description: "Search, list or propose lessons in MPM. " +
			"search and list retrieve; propose records a pending proposal, not a trusted lesson. " +
			"Only an operator can promote a proposal, and HAL must say so.",
		Actions: []string{"search", "list", "propose"},
		Schema: `{
			"type": "object",
			"properties": {
				"action": {"type": "string", "enum": ["search", "list", "propose"]},
				"params": {
					"type": "object",
					"properties": {
						"fact": {"type": "string", "description": "propose: the proposed lesson text."},
						"type": {"type": "string", "enum": ["insight", "warning", "practice"]},
						"query": {"type": "string", "description": "search: the FTS query."},
						"tags": {"type": "array", "items": {"type": "string"}},
						"projection": {"type": "string", "enum": ["summary", "full"]}
					},
					"additionalProperties": true
				}
			},
			"required": ["action"],
			"additionalProperties": false
		}`,
	},
	{
		Name: "mpm_theories",
		Description: "Propose, resolve, inspect or search testable hypotheses in MPM. " +
			"A proposal is a pending theory with validation criteria, not a fact. " +
			"resolve changes its status only with the evidence or conclusion supplied.",
		Actions: []string{"propose", "resolve", "show", "list", "query"},
		Schema: `{
			"type": "object",
			"properties": {
				"action": {"type": "string", "enum": ["propose", "resolve", "show", "list", "query"]},
				"params": {
					"type": "object",
					"properties": {
						"hypothesis": {"type": "string"},
						"validation_criteria": {"type": "string"},
						"theoryId": {"type": "string"},
						"id": {"type": "string"},
						"conclusion": {"type": "string"},
						"newStatus": {"type": "string", "enum": ["proven", "disproven"]},
						"query": {"type": "string"},
						"status": {"type": "string"},
						"tags": {"type": "array", "items": {"type": "string"}},
						"limit": {"type": "number"}
					},
					"additionalProperties": true
				}
			},
			"required": ["action"],
			"additionalProperties": false
		}`,
	},
	{
		Name: "mpm_decisions",
		Description: "Record, inspect or search architectural decisions in MPM. " +
			"record stores the choice and its rationale; show, list and query retrieve. " +
			"Do not record a decision the operator has not confirmed.",
		Actions: []string{"record", "show", "list", "query"},
		Schema: `{
			"type": "object",
			"properties": {
				"action": {"type": "string", "enum": ["record", "show", "list", "query"]},
				"params": {
					"type": "object",
					"properties": {
						"choice": {"type": "string"},
						"context": {"type": "string"},
						"rationale": {"type": "string"},
						"outcome": {"type": "string"},
						"id": {"type": "string"},
						"query": {"type": "string"},
						"status": {"type": "string"},
						"tags": {"type": "array", "items": {"type": "string"}},
						"limit": {"type": "number"}
					},
					"additionalProperties": true
				}
			},
			"required": ["action"],
			"additionalProperties": false
		}`,
	},
	{
		Name: "mpm_evidence",
		Description: "Attach a concrete observation to an MPM memory, theory, decision, lesson or work item, or list existing evidence. " +
			"Source groups are MPM's own vocabulary. Evidence is not a substitute for the artifact itself.",
		Actions: []string{"add", "list", "source_groups"},
		Schema: `{
			"type": "object",
			"properties": {
				"action": {"type": "string", "enum": ["add", "list", "source_groups"]},
				"params": {
					"type": "object",
					"properties": {
						"artifact_id": {"type": "string"},
						"artifact_type": {"type": "string", "enum": ["memory", "theory", "decision", "lesson", "work"]},
						"type": {"type": "string", "description": "The observation type, for example outcome, audit or action."},
						"source_group": {"type": "string", "description": "MPM's source-group vocabulary; use source_groups to discover it."},
						"strength": {"type": "number"},
						"independence_factor": {"type": "number"},
						"notes": {"type": "string"},
						"reference_url": {"type": "string"}
					},
					"additionalProperties": true
				}
			},
			"required": ["action"],
			"additionalProperties": false
		}`,
	},
	{
		Name: "mpm_work",
		Description: "Track a commitment that spans sessions in MPM: create, list, show, update, complete, cancel, add a note, or read history. " +
			"Completing a work item is an explicit claim of completion, not a cleanup step.",
		Actions: []string{"create", "list", "show", "update", "complete", "cancel", "history", "note"},
		Schema: `{
			"type": "object",
			"properties": {
				"action": {"type": "string", "enum": ["create", "list", "show", "update", "complete", "cancel", "history", "note"]},
				"params": {
					"type": "object",
					"properties": {
						"title": {"type": "string"},
						"content": {"type": "string"},
						"session_id": {"type": "string"},
						"work_id": {"type": "string"},
						"status": {"type": "string", "enum": ["open", "done", "cancelled", "all"]},
						"visibility": {"type": "string", "enum": ["active", "archived", "all"]},
						"note": {"type": "string"},
						"limit": {"type": "number"}
					},
					"additionalProperties": true
				}
			},
			"required": ["action"],
			"additionalProperties": false
		}`,
	},
	{
		Name: "mpm_handoff",
		Description: "Write, read or list MPM session handoffs. " +
			"write carries a summary plus optional commitments and open questions for the next session; it is not a work log. " +
			"shred is deliberately not available to HAL.",
		Actions: []string{"write", "read", "list"},
		Schema: `{
			"type": "object",
			"properties": {
				"action": {"type": "string", "enum": ["write", "read", "list"]},
				"params": {
					"type": "object",
					"properties": {
						"summary": {"type": "string"},
						"session_id": {"type": "string"},
						"state": {"type": "string", "enum": ["clean", "crashed", "interrupted", "force_end"]},
						"commitments": {"type": "array", "items": {"type": "string"}},
						"open_questions": {"type": "array", "items": {"type": "string"}},
						"limit": {"type": "number"},
						"unread": {"type": "boolean"}
					},
					"additionalProperties": true
				}
			},
			"required": ["action"],
			"additionalProperties": false
		}`,
	},
	{
		Name: "mpm_skills",
		Description: "List, read or save MPM skills. " +
			"list and read discover procedural guidance; save creates a versioned skill from markdown content with frontmatter. " +
			"HAL never deletes or promotes a skill through this tool.",
		Actions: []string{"list", "read", "save"},
		Schema: `{
			"type": "object",
			"properties": {
				"action": {"type": "string", "enum": ["list", "read", "save"]},
				"params": {
					"type": "object",
					"properties": {
						"name": {"type": "string"},
						"version": {"type": "string"},
						"content": {"type": "string", "description": "save: the complete skill markdown, including frontmatter."},
						"author": {"type": "string"},
						"force": {"type": "boolean"},
						"scope": {"type": "string", "enum": ["all", "local", "shared"]}
					},
					"additionalProperties": true
				}
			},
			"required": ["action"],
			"additionalProperties": false
		}`,
	},
}

var defByName = func() map[string]ToolDef {
	out := make(map[string]ToolDef, len(defs))
	for _, d := range defs {
		out[d.Name] = d
	}
	return out
}()

// Result is one knowledge-tool outcome for the engine and the audit trail.
type Result struct {
	Content   string
	Action    string
	Target    string
	Status    string
	Summary   string
	Duration  time.Duration
	Truncated bool
	// Mode is the permission mode captured when the call began.
	Mode permission.Mode
}

// Registry is the set of MPM-backed tools HAL offers.
type Registry struct {
	client  *Client
	audit   *workspace.Audit
	session func() string
	mode    func() permission.Mode
	now     func() time.Time
}

// NewRegistry builds the MPM tool registry over one client.
func NewRegistry(client *Client) *Registry {
	return &Registry{client: client, now: time.Now}
}

// WithAudit attaches the shared trail.
func (r *Registry) WithAudit(a *workspace.Audit, session func() string) *Registry {
	r.audit = a
	r.session = session
	return r
}

// WithMode reports the permission mode for audit. It does not gate access:
// MPM knowledge operations are available in both 444 and 666.
func (r *Registry) WithMode(mode func() permission.Mode) *Registry {
	r.mode = mode
	return r
}

// Tools returns the offered tool definitions.
func (r *Registry) Tools() []provider.Tool {
	out := make([]provider.Tool, 0, len(defs))
	for _, d := range defs {
		out = append(out, provider.Tool{Name: d.Name, Description: d.Description, Parameters: d.Schema})
	}
	return out
}

// Has reports whether HAL offers this MPM tool.
func (r *Registry) Has(name string) bool {
	_, ok := defByName[name]
	return ok
}

// Execute validates HAL's allowlist and passes the request to MPM. MPM
// remains responsible for validating the parameters and confirming the
// result.
func (r *Registry) Execute(ctx context.Context, call provider.ToolCall) Result {
	start := r.now()
	mode := permission.Guarded
	if r.mode != nil {
		mode = r.mode()
	}
	res := Result{Action: "unknown", Target: call.Name, Status: "failed", Mode: mode}
	def, ok := defByName[call.Name]
	if !ok {
		res.Content = toolError(call.Name, "unknown MPM tool")
		res.Summary = call.Name + " denied · unknown MPM tool"
		return r.finish(call, res, start)
	}
	if raw, ok := call.Arguments["_invalid"]; ok {
		_ = raw
		res.Content = toolError(call.Name, "arguments are not valid JSON")
		res.Summary = call.Name + " failed · malformed arguments"
		return r.finish(call, res, start)
	}
	for key := range call.Arguments {
		if key != "action" && key != "params" {
			res.Content = toolError(call.Name, "arguments may contain only action and params")
			res.Summary = call.Name + " failed · malformed arguments"
			return r.finish(call, res, start)
		}
	}
	action, ok := call.Arguments["action"].(string)
	if !ok || strings.TrimSpace(action) == "" {
		res.Content = toolError(call.Name, "action is required")
		res.Summary = call.Name + " failed · missing action"
		return r.finish(call, res, start)
	}
	if !contains(def.Actions, action) {
		res.Content = toolError(call.Name, fmt.Sprintf("action %q is not available to HAL", action))
		res.Summary = call.Name + " denied · unsupported action"
		return r.finish(call, res, start)
	}
	params := map[string]any{}
	if raw, exists := call.Arguments["params"]; exists {
		if m, ok := raw.(map[string]any); ok {
			params = m
		} else if raw != nil {
			res.Content = toolError(call.Name, "params must be an object")
			res.Summary = call.Name + " failed · malformed params"
			return r.finish(call, res, start)
		}
	}
	if err := required(def.Name, action, params); err != nil {
		res.Action = action
		res.Content = toolError(call.Name, err.Error())
		res.Summary = call.Name + " failed · " + err.Error()
		return r.finish(call, res, start)
	}
	encoded, err := json.Marshal(params)
	if err != nil {
		res.Action = action
		res.Content = toolError(call.Name, "params cannot be encoded")
		res.Summary = call.Name + " failed · malformed params"
		return r.finish(call, res, start)
	}
	if len(encoded) > 256*1024 {
		res.Action = action
		res.Content = toolError(call.Name, "params are too large")
		res.Summary = call.Name + " failed · params too large"
		return r.finish(call, res, start)
	}
	env, err := r.client.Call(ctx, def.Name, action, params)
	res.Action = action
	res.Target = def.Name + "/" + action
	if err != nil {
		res.Content = toolError(call.Name, err.Error())
		res.Summary = call.Name + " " + action + " · MPM unavailable"
		return r.finish(call, res, start)
	}
	if !env.Success {
		message := strings.TrimSpace(env.Error)
		if message == "" {
			message = "MPM did not confirm success"
		}
		res.Content = toolError(call.Name, message)
		res.Summary = call.Name + " " + action + " · failed"
		return r.finish(call, res, start)
	}
	res.Status = "ok"
	res.Summary = call.Name + " " + action + " · confirmed"
	if def.Name == "mpm_lessons" && action == "propose" {
		res.Summary = "mpm_lessons propose · pending operator review"
	}
	if len(env.Raw) > MaxPromptBytes {
		res.Truncated = true
		res.Content = truncatedSuccess(call.Name, action, env.Raw)
		res.Summary += " · result shortened"
	} else {
		res.Content = string(env.Raw)
	}
	return r.finish(call, res, start)
}

func (r *Registry) finish(call provider.ToolCall, res Result, start time.Time) Result {
	res.Duration = r.now().Sub(start)
	mode := "444"
	if res.Mode == permission.Unrestricted {
		mode = "666"
	}
	if r.audit != nil {
		rec := workspace.ToolRecord{
			Time: start, Mode: mode, Tool: call.Name, Action: res.Action,
			Target: res.Target, Outcome: res.Status, Bytes: len(res.Content),
			DurationMS: res.Duration.Milliseconds(), Truncated: res.Truncated,
		}
		if res.Status != "ok" {
			rec.Reason = res.Status
		}
		if r.session != nil {
			rec.Session = r.session()
		}
		_ = r.audit.AppendTool(rec)
	}
	return res
}

// required is intentionally small: it catches the obvious omissions before
// spawning a process, while MPM remains authoritative for every field's
// meaning and validation.
func required(tool, action string, p map[string]any) error {
	stringField := func(name string) bool {
		v, ok := p[name].(string)
		return ok && strings.TrimSpace(v) != ""
	}
	switch tool {
	case "mpm_memory":
		switch action {
		case "save":
			if !stringField("fact") {
				return errors.New("fact is required for save")
			}
		case "query":
			if !stringField("query") {
				return errors.New("query is required for query")
			}
		case "show":
			if !stringField("memory_id") {
				return errors.New("memory_id is required for show")
			}
		}
	case "mpm_lessons":
		if action == "propose" && !stringField("fact") {
			return errors.New("fact is required for propose")
		}
		if action == "search" && !stringField("query") {
			return errors.New("query is required for search")
		}
	case "mpm_theories":
		if action == "propose" && !stringField("hypothesis") {
			return errors.New("hypothesis is required for propose")
		}
		if action == "resolve" {
			if !stringField("theoryId") {
				return errors.New("theoryId is required for resolve")
			}
			if !stringField("conclusion") {
				return errors.New("conclusion is required for resolve")
			}
		}
		if action == "show" && !stringField("id") && !stringField("theoryId") {
			return errors.New("id is required for show")
		}
		if action == "query" && !stringField("query") {
			return errors.New("query is required for query")
		}
	case "mpm_decisions":
		if action == "record" && !stringField("choice") {
			return errors.New("choice is required for record")
		}
		if action == "show" && !stringField("id") {
			return errors.New("id is required for show")
		}
		if action == "query" && !stringField("query") {
			return errors.New("query is required for query")
		}
	case "mpm_evidence":
		if action == "add" {
			for _, name := range []string{"artifact_id", "artifact_type", "type", "source_group"} {
				if !stringField(name) {
					return fmt.Errorf("%s is required for add", name)
				}
			}
		}
		if action == "list" {
			for _, name := range []string{"artifact_id", "artifact_type"} {
				if !stringField(name) {
					return fmt.Errorf("%s is required for list", name)
				}
			}
		}
	case "mpm_work":
		if action == "create" && !stringField("title") {
			return errors.New("title is required for create")
		}
		if action != "create" && action != "list" && !stringField("work_id") {
			return fmt.Errorf("work_id is required for %s", action)
		}
	case "mpm_handoff":
		if action == "write" && !stringField("summary") {
			return errors.New("summary is required for write")
		}
	case "mpm_skills":
		if action == "read" && !stringField("name") {
			return errors.New("name is required for read")
		}
		if action == "save" {
			if !stringField("name") {
				return errors.New("name is required for save")
			}
			if !stringField("content") {
				return errors.New("content is required for save")
			}
		}
	}
	return nil
}

func contains(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}

func toolError(tool, message string) string {
	raw, _ := json.Marshal(map[string]any{
		"ok": false, "tool": tool, "error": message,
	})
	return string(raw)
}

func truncatedSuccess(tool, action string, raw []byte) string {
	preview := raw
	if len(preview) > MaxPromptBytes/2 {
		preview = preview[:MaxPromptBytes/2]
	}
	// A cut JSON object is not sent as if it were complete. The preview is
	// a string and the caller is told that MPM's answer was shortened.
	sum := sha256.Sum256(raw)
	out, _ := json.Marshal(map[string]any{
		"ok": true, "tool": tool, "action": action, "truncated": true,
		"full_bytes": len(raw), "sha256": hex.EncodeToString(sum[:]),
		"result_preview": string(preview),
	})
	return string(out)
}
