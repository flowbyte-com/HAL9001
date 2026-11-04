// Package agenttools implements HAL9001's 666 tools: read, write, edit and
// bash. They are ordinary operating-system operations performed with the
// permissions of the user running HAL. They are never offered in 444.
//
// A tool is only an implementation of a model's request. It has no authority
// of its own: Execute asks the permission gate again at the execution
// boundary, so disabling 666 stops every pending and running tool even if the
// model was already offered the tool. A revoked operation is cancelled; a
// bash command is terminated together with its process group.
package agenttools

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/flowbyte-com/HAL9001/internal/permission"
	"github.com/flowbyte-com/HAL9001/internal/provider"
	"github.com/flowbyte-com/HAL9001/internal/workspace"
)

const (
	// MaxReadBytes bounds one read result returned to the model.
	MaxReadBytes = 64 * 1024
	// MaxReadLines bounds one read by line count even when each line is short.
	MaxReadLines = 400
	// MaxWriteBytes bounds one write. Larger documents should be produced in
	// pieces through edit.
	MaxWriteBytes = 1 * 1024 * 1024
	// MaxEditBytes bounds the file an edit may load.
	MaxEditBytes = 4 * 1024 * 1024
	// MaxOutputBytes bounds stdout and stderr independently.
	MaxOutputBytes = 32 * 1024
	// DefaultTimeout bounds one bash command.
	DefaultTimeout = 120 * time.Second
	// MaxTimeout is the longest timeout a model may request.
	MaxTimeout = 600 * time.Second
)

// Tools returns the complete 666 tool set in stable order.
func Tools() []provider.Tool {
	return []provider.Tool{
		{
			Name: "read",
			Description: "Read a UTF-8 text file from the operating system, optionally a bounded line range. " +
				"Use this in 666 when the operator asks you to inspect a file or when a task cannot be answered without it. " +
				"It runs with the OS user's permissions and is not confined to the HAL workspace.",
			Parameters: `{
				"type": "object",
				"properties": {
					"path": {"type": "string", "description": "Absolute path, path with ~, or a path relative to HAL's working directory."},
					"offset": {"type": "integer", "minimum": 1, "description": "First line to return, 1-based. Defaults to 1."},
					"limit": {"type": "integer", "minimum": 1, "maximum": 400, "description": "Maximum lines to return. Defaults to 400."}
				},
				"required": ["path"],
				"additionalProperties": false
			}`,
		},
		{
			Name: "write",
			Description: "Create or replace one UTF-8 text file on the operating system. " +
				"Use this only when the operator explicitly asks you to save a file. It runs with the OS user's permissions and is not confined to the HAL workspace.",
			Parameters: `{
				"type": "object",
				"properties": {
					"path": {"type": "string", "description": "Absolute path, path with ~, or a path relative to HAL's working directory."},
					"content": {"type": "string", "description": "The complete file contents to write."}
				},
				"required": ["path", "content"],
				"additionalProperties": false
			}`,
		},
		{
			Name: "edit",
			Description: "Apply a targeted text replacement to one file. " +
				"By default the old text must occur exactly once, so an ambiguous edit is refused rather than guessed. Set replace_all only when every occurrence should change.",
			Parameters: `{
				"type": "object",
				"properties": {
					"path": {"type": "string", "description": "Absolute path, path with ~, or a path relative to HAL's working directory."},
					"old_text": {"type": "string", "description": "The exact text to replace."},
					"new_text": {"type": "string", "description": "The replacement text."},
					"replace_all": {"type": "boolean", "description": "Replace every occurrence instead of requiring exactly one."}
				},
				"required": ["path", "old_text", "new_text"],
				"additionalProperties": false
			}`,
		},
		{
			Name: "bash",
			Description: "Run one shell command with bash and return stdout, stderr and the exit code. " +
				"Use this only when the operator asks you to run or verify something. Commands run with the OS user's permissions; their output is bounded and their process group is terminated when the command finishes or is cancelled.",
			Parameters: `{
				"type": "object",
				"properties": {
					"command": {"type": "string", "description": "The command line passed to bash -c."},
					"workdir": {"type": "string", "description": "Working directory. Defaults to HAL's current directory."},
					"timeout_seconds": {"type": "integer", "minimum": 1, "maximum": 600, "description": "Wall-clock limit. Defaults to 120 seconds."}
				},
				"required": ["command"],
				"additionalProperties": false
			}`,
		},
	}
}

// Executor performs the 666 tools. A nil gate denies everything: there is no
// implicit authority when the application forgot to wire one.
type Executor struct {
	gate    permission.Gate
	audit   *workspace.Audit
	session func() string
	now     func() time.Time
}

// New builds an executor over the permission gate. Audit may be nil; a
// failure to write the trail must never make an operation look successful.
func New(gate permission.Gate, audit *workspace.Audit) *Executor {
	return &Executor{gate: gate, audit: audit, now: time.Now}
}

// WithSession sets the conversation identifier recorded in the audit trail.
func (e *Executor) WithSession(session func() string) *Executor {
	e.session = session
	return e
}

// Result is the outcome of one tool call, including the metadata the
// interface needs to display it without reproducing its content.
type Result struct {
	Content   string
	Tool      string
	Action    string
	Target    string
	Status    string
	Summary   string
	Duration  time.Duration
	Bytes     int
	Truncated bool
	ExitCode  *int
	// Mode is the permission mode captured at the execution boundary.
	// A later revocation must not relabel a cancelled operation as 444.
	Mode permission.Mode
}

// Execute runs one structured call. Arguments are validated before any
// side effect, and the permission gate is checked immediately before the
// operation begins.
func (e *Executor) Execute(ctx context.Context, call provider.ToolCall) Result {
	start := e.now()
	mode := permission.Guarded
	if e.gate != nil {
		mode = e.gate.Mode()
	}
	if raw, ok := call.Arguments["_invalid"]; ok {
		_ = raw
		return e.finish(call, Result{
			Tool: call.Name, Status: "failed", Action: "invalid", Mode: mode,
			Content: errorJSON(call.Name, "arguments are not valid JSON"),
		}, start)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var res Result
	switch call.Name {
	case "read":
		res = e.read(ctx, call)
	case "write":
		res = e.write(ctx, call)
	case "edit":
		res = e.edit(ctx, call)
	case "bash":
		res = e.bash(ctx, call)
	default:
		res = Result{Tool: call.Name, Status: "denied", Action: "unknown",
			Content: errorJSON(call.Name, "unknown tool")}
		res.Summary = call.Name + " denied · unknown tool"
	}
	res.Mode = mode
	return e.finish(call, res, start)
}

// denied turns a gate refusal into a tool result the model can explain
// honestly. The model is never told that retrying will help.
func (e *Executor) denied(name string, err error) Result {
	return Result{
		Tool: name, Action: "permission", Status: "denied",
		Content: errorJSON(name, "666 is not enabled for this conversation; the operation was not performed"),
		Summary: name + " denied · 444",
	}
}

// begin checks the gate and, on success, returns the operation context and
// the cleanup that deregisters it.
func (e *Executor) begin(ctx context.Context, name string) (context.Context, func(), error) {
	if e.gate == nil {
		return ctx, func() {}, permission.ErrDisabled
	}
	return e.gate.Begin(ctx, name)
}

// finish fills the common metadata, records the audit line, and returns the
// result. Audit failures are deliberately not surfaced as tool failures:
// bookkeeping never changes what happened.
func (e *Executor) finish(call provider.ToolCall, res Result, start time.Time) Result {
	if res.Tool == "" {
		res.Tool = call.Name
	}
	if res.Action == "" {
		res.Action = call.Name
	}
	res.Duration = e.now().Sub(start)
	if res.Status == "" {
		res.Status = "ok"
	}
	if res.Summary == "" {
		res.Summary = res.Tool + " " + res.Status
	}
	mode := "444"
	if res.Mode == permission.Unrestricted {
		mode = "666"
	}
	rec := workspace.ToolRecord{
		Time:       start,
		Mode:       mode,
		Tool:       res.Tool,
		Action:     res.Action,
		Target:     res.Target,
		Outcome:    res.Status,
		ExitCode:   res.ExitCode,
		Bytes:      res.Bytes,
		DurationMS: res.Duration.Milliseconds(),
		Truncated:  res.Truncated,
	}
	if res.Status != "ok" {
		rec.Reason = res.Status
	}
	if e.session != nil {
		rec.Session = e.session()
	}
	if res.Tool == "bash" {
		if command, ok := stringArg(call.Arguments, "command"); ok {
			sum := sha256.Sum256([]byte(command))
			rec.CommandHash = "sha256:" + hex.EncodeToString(sum[:8])
		}
	}
	if e.audit != nil {
		_ = e.audit.AppendTool(rec)
	}
	return res
}

func (e *Executor) read(ctx context.Context, call provider.ToolCall) Result {
	path, ok := stringArg(call.Arguments, "path")
	if !ok || strings.TrimSpace(path) == "" {
		return Result{Action: "read", Status: "failed",
			Content: errorJSON("read", "path is required")}
	}
	offset, err := intArg(call.Arguments, "offset", 1)
	if err != nil || offset < 1 {
		return Result{Action: "read", Target: path, Status: "failed",
			Content: errorJSON("read", "offset must be a positive integer")}
	}
	limit, err := intArg(call.Arguments, "limit", MaxReadLines)
	if err != nil || limit < 1 {
		return Result{Action: "read", Target: path, Status: "failed",
			Content: errorJSON("read", "limit must be a positive integer")}
	}
	if limit > MaxReadLines {
		limit = MaxReadLines
	}
	abs, err := resolvePath(path)
	if err != nil {
		return Result{Action: "read", Target: path, Status: "failed",
			Content: errorJSON("read", err.Error())}
	}
	opCtx, done, err := e.begin(ctx, "read")
	if err != nil {
		return e.denied("read", err)
	}
	defer done()
	if err := opCtx.Err(); err != nil {
		return Result{Action: "read", Target: abs, Status: "cancelled",
			Content: errorJSON("read", "cancelled before the read")}
	}
	// The final permission check sits immediately before the filesystem
	// call, not at the point where the tool was offered.
	if e.gate == nil || e.gate.Mode() != permission.Unrestricted {
		return e.denied("read", permission.ErrDisabled)
	}

	f, err := os.Open(abs)
	if err != nil {
		return Result{Action: "read", Target: abs, Status: "failed",
			Content: errorJSON("read", err.Error())}
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return Result{Action: "read", Target: abs, Status: "failed",
			Content: errorJSON("read", err.Error())}
	}
	if info.IsDir() {
		return Result{Action: "read", Target: abs, Status: "failed",
			Content: errorJSON("read", "path is a directory")}
	}
	if !info.Mode().IsRegular() {
		return Result{Action: "read", Target: abs, Status: "failed",
			Content: errorJSON("read", "path is not a regular file")}
	}
	head := make([]byte, 8192)
	n, _ := f.Read(head)
	if strings.IndexByte(string(head[:n]), 0) >= 0 {
		return Result{Action: "read", Target: abs, Status: "failed",
			Content: errorJSON("read", "file appears to be binary")}
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return Result{Action: "read", Target: abs, Status: "failed",
			Content: errorJSON("read", err.Error())}
	}

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	var body strings.Builder
	lineNo, lines, bytesUsed := 0, 0, 0
	truncated := false
	for scanner.Scan() {
		lineNo++
		if lineNo < offset {
			continue
		}
		text := scanner.Text()
		if lines >= limit || bytesUsed+len(text)+1 > MaxReadBytes {
			truncated = true
			break
		}
		body.WriteString(strconv.Itoa(lineNo))
		body.WriteString(": ")
		body.WriteString(text)
		body.WriteByte('\n')
		lines++
		bytesUsed += len(text) + 1 + len(strconv.Itoa(lineNo)) + 2
	}
	if err := scanner.Err(); err != nil {
		return Result{Action: "read", Target: abs, Status: "failed",
			Content: errorJSON("read", err.Error())}
	}
	end := offset + lines - 1
	if lines == 0 {
		end = offset - 1
	}
	type readResult struct {
		OK        bool   `json:"ok"`
		Tool      string `json:"tool"`
		Path      string `json:"path"`
		StartLine int    `json:"start_line"`
		EndLine   int    `json:"end_line"`
		Content   string `json:"content"`
		Truncated bool   `json:"truncated"`
		Note      string `json:"note,omitempty"`
	}
	out := readResult{OK: true, Tool: "read", Path: abs, StartLine: offset,
		EndLine: end, Content: body.String(), Truncated: truncated}
	if lines == 0 {
		out.Note = "the requested start line is past the end of the file"
	}
	raw, _ := marshalJSON(out)
	return Result{
		Content: string(raw), Action: "read", Target: abs, Status: "ok",
		Summary: fmt.Sprintf("read %s · %d line(s)", abs, lines),
		Bytes:   len(body.String()), Truncated: truncated,
	}
}

func (e *Executor) write(ctx context.Context, call provider.ToolCall) Result {
	path, ok := stringArg(call.Arguments, "path")
	content, okContent := stringArg(call.Arguments, "content")
	if !ok || strings.TrimSpace(path) == "" || !okContent {
		return Result{Action: "write", Target: path, Status: "failed",
			Content: errorJSON("write", "path and content are required")}
	}
	if len(content) > MaxWriteBytes {
		return Result{Action: "write", Target: path, Status: "failed",
			Content: errorJSON("write", fmt.Sprintf("content is larger than %d bytes", MaxWriteBytes))}
	}
	abs, err := resolvePath(path)
	if err != nil {
		return Result{Action: "write", Target: path, Status: "failed",
			Content: errorJSON("write", err.Error())}
	}
	opCtx, done, err := e.begin(ctx, "write")
	if err != nil {
		return e.denied("write", err)
	}
	defer done()
	existed := false
	if info, err := os.Stat(abs); err == nil {
		if info.IsDir() {
			return Result{Action: "write", Target: abs, Status: "failed",
				Content: errorJSON("write", "path is a directory")}
		}
		if !info.Mode().IsRegular() {
			return Result{Action: "write", Target: abs, Status: "failed",
				Content: errorJSON("write", "path is not a regular file")}
		}
		existed = true
	} else if !errors.Is(err, os.ErrNotExist) {
		return Result{Action: "write", Target: abs, Status: "failed",
			Content: errorJSON("write", err.Error())}
	}
	parent := filepath.Dir(abs)
	if info, err := os.Stat(parent); err != nil || !info.IsDir() {
		return Result{Action: "write", Target: abs, Status: "failed",
			Content: errorJSON("write", "the parent directory does not exist")}
	}
	if err := opCtx.Err(); err != nil {
		return Result{Action: "write", Target: abs, Status: "cancelled",
			Content: errorJSON("write", "cancelled before the write")}
	}
	// The final permission check sits immediately before the filesystem
	// call, not at the point where the tool was offered.
	if e.gate == nil || e.gate.Mode() != permission.Unrestricted {
		return e.denied("write", permission.ErrDisabled)
	}
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		return Result{Action: "write", Target: abs, Status: "failed",
			Content: errorJSON("write", err.Error())}
	}
	return Result{
		Content: writeJSON("write", abs, len(content), existed, ""),
		Action:  "write", Target: abs, Status: "ok",
		Summary: fmt.Sprintf("wrote %s · %d bytes", abs, len(content)),
		Bytes:   len(content),
	}
}

func (e *Executor) edit(ctx context.Context, call provider.ToolCall) Result {
	path, okPath := stringArg(call.Arguments, "path")
	oldText, okOld := stringArg(call.Arguments, "old_text")
	newText, okNew := stringArg(call.Arguments, "new_text")
	replaceAll, _ := boolArg(call.Arguments, "replace_all")
	if !okPath || !okOld || !okNew || strings.TrimSpace(path) == "" {
		return Result{Action: "edit", Target: path, Status: "failed",
			Content: errorJSON("edit", "path, old_text and new_text are required")}
	}
	if oldText == "" {
		return Result{Action: "edit", Target: path, Status: "failed",
			Content: errorJSON("edit", "old_text must not be empty")}
	}
	abs, err := resolvePath(path)
	if err != nil {
		return Result{Action: "edit", Target: path, Status: "failed",
			Content: errorJSON("edit", err.Error())}
	}
	opCtx, done, err := e.begin(ctx, "edit")
	if err != nil {
		return e.denied("edit", err)
	}
	defer done()
	data, err := os.ReadFile(abs)
	if err != nil {
		return Result{Action: "edit", Target: abs, Status: "failed",
			Content: errorJSON("edit", err.Error())}
	}
	if len(data) > MaxEditBytes {
		return Result{Action: "edit", Target: abs, Status: "failed",
			Content: errorJSON("edit", fmt.Sprintf("file is larger than %d bytes", MaxEditBytes))}
	}
	count := strings.Count(string(data), oldText)
	if count == 0 {
		return Result{Action: "edit", Target: abs, Status: "failed",
			Content: errorJSON("edit", "old_text was not found")}
	}
	if !replaceAll && count != 1 {
		return Result{Action: "edit", Target: abs, Status: "failed",
			Content: errorJSON("edit", fmt.Sprintf("old_text occurs %d times; provide more context or set replace_all", count))}
	}
	if !replaceAll {
		count = 1
	}
	updated := strings.ReplaceAll(string(data), oldText, newText)
	if len(updated) > MaxEditBytes {
		return Result{Action: "edit", Target: abs, Status: "failed",
			Content: errorJSON("edit", fmt.Sprintf("edited file would be larger than %d bytes", MaxEditBytes))}
	}
	info, err := os.Stat(abs)
	if err != nil {
		return Result{Action: "edit", Target: abs, Status: "failed",
			Content: errorJSON("edit", err.Error())}
	}
	if err := opCtx.Err(); err != nil {
		return Result{Action: "edit", Target: abs, Status: "cancelled",
			Content: errorJSON("edit", "cancelled before the edit")}
	}
	if e.gate == nil || e.gate.Mode() != permission.Unrestricted {
		return e.denied("edit", permission.ErrDisabled)
	}
	if err := os.WriteFile(abs, []byte(updated), info.Mode().Perm()); err != nil {
		return Result{Action: "edit", Target: abs, Status: "failed",
			Content: errorJSON("edit", err.Error())}
	}
	return Result{
		Content: editJSON("edit", abs, count, len(updated), ""),
		Action:  "edit", Target: abs, Status: "ok",
		Summary: fmt.Sprintf("edited %s · %d replacement(s)", abs, count),
		Bytes:   len(updated),
	}
}

func (e *Executor) bash(ctx context.Context, call provider.ToolCall) Result {
	command, ok := stringArg(call.Arguments, "command")
	if !ok || strings.TrimSpace(command) == "" {
		return Result{Action: "bash", Status: "failed",
			Content: errorJSON("bash", "command is required")}
	}
	if len(command) > 128*1024 {
		return Result{Action: "bash", Status: "failed",
			Content: errorJSON("bash", "command is too large")}
	}
	timeout := DefaultTimeout
	seconds, err := intArg(call.Arguments, "timeout_seconds", int(DefaultTimeout/time.Second))
	if err != nil {
		return Result{Action: "bash", Status: "failed",
			Content: errorJSON("bash", "timeout_seconds must be an integer")}
	}
	timeout = time.Duration(seconds) * time.Second
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	if timeout > MaxTimeout {
		timeout = MaxTimeout
	}
	workdir := ""
	if raw, ok := stringArg(call.Arguments, "workdir"); ok && strings.TrimSpace(raw) != "" {
		abs, err := resolvePath(raw)
		if err != nil {
			return Result{Action: "bash", Status: "failed",
				Content: errorJSON("bash", err.Error())}
		}
		info, err := os.Stat(abs)
		if err != nil || !info.IsDir() {
			return Result{Action: "bash", Target: abs, Status: "failed",
				Content: errorJSON("bash", "workdir is not a directory")}
		}
		workdir = abs
	}
	opCtx, done, err := e.begin(ctx, "bash")
	if err != nil {
		return e.denied("bash", err)
	}
	defer done()
	runCtx, cancel := context.WithTimeout(opCtx, timeout)
	defer cancel()
	if runCtx.Err() != nil {
		return Result{Action: "bash", Target: workdir, Status: "cancelled",
			Content: errorJSON("bash", "cancelled before the command started")}
	}
	if e.gate == nil || e.gate.Mode() != permission.Unrestricted {
		return e.denied("bash", permission.ErrDisabled)
	}

	cmd := exec.Command("bash", "-c", command)
	if workdir != "" {
		cmd.Dir = workdir
	} else if cwd, err := os.Getwd(); err == nil {
		cmd.Dir = cwd
	}
	cmd.Env = os.Environ()
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// A background child can inherit stdout/stderr and keep the copy
	// goroutines alive after the shell itself exits. Bound that wait so a
	// normal command cannot hang on its own background work.
	cmd.WaitDelay = 2 * time.Second
	stdout := newBoundedBuffer(MaxOutputBytes)
	stderr := newBoundedBuffer(MaxOutputBytes)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	start := e.now()
	if err := cmd.Start(); err != nil {
		return Result{Action: "bash", Target: workdir, Status: "failed",
			Content: errorJSON("bash", err.Error()), Duration: e.now().Sub(start)}
	}
	pid := cmd.Process.Pid
	doneWait := make(chan error, 1)
	go func() { doneWait <- cmd.Wait() }()

	var waitErr error
	cancelled, timedOut := false, false
	select {
	case waitErr = <-doneWait:
	case <-runCtx.Done():
		switch {
		case errors.Is(runCtx.Err(), context.DeadlineExceeded):
			timedOut = true
		default:
			cancelled = true
		}
		terminateGroup(pid, 2*time.Second)
		waitErr = <-doneWait
	}
	// A shell can leave background work behind even when it exits
	// normally. The command owns that work; clean it up before returning.
	terminateLingeringGroup(pid)

	exitCode := exitCodeOf(waitErr)
	status := "ok"
	if timedOut {
		status = "timeout"
	} else if cancelled {
		status = "cancelled"
	} else if exitCode == nil || *exitCode != 0 {
		status = "failed"
	}
	succeeded := !cancelled && !timedOut
	raw, _ := marshalJSON(struct {
		OK        bool   `json:"ok"`
		Tool      string `json:"tool"`
		Command   string `json:"command"`
		Workdir   string `json:"workdir,omitempty"`
		ExitCode  *int   `json:"exit_code,omitempty"`
		Stdout    string `json:"stdout"`
		Stderr    string `json:"stderr"`
		TimedOut  bool   `json:"timed_out"`
		Cancelled bool   `json:"cancelled"`
		Truncated bool   `json:"truncated"`
		Duration  int64  `json:"duration_ms"`
	}{
		OK: succeeded, Tool: "bash", Command: command, Workdir: workdir,
		ExitCode: exitCode, Stdout: stdout.String(), Stderr: stderr.String(),
		TimedOut: timedOut, Cancelled: cancelled,
		Truncated: stdout.Truncated() || stderr.Truncated(),
		Duration:  e.now().Sub(start).Milliseconds(),
	})
	summary := "bash " + workdir + " · " + status
	if exitCode != nil {
		summary = fmt.Sprintf("bash · exit %d · %s", *exitCode, e.now().Sub(start).Round(time.Millisecond))
	}
	return Result{
		Content: string(raw), Action: "bash", Target: workdir,
		Status: status, Summary: summary,
		Bytes:     len(stdout.String()) + len(stderr.String()),
		Truncated: stdout.Truncated() || stderr.Truncated(),
		ExitCode:  exitCode,
	}
}

func exitCodeOf(err error) *int {
	if err == nil {
		code := 0
		return &code
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		code := exitErr.ExitCode()
		return &code
	}
	return nil
}

// resolvePath expands ~ and makes a path absolute. It deliberately does not
// confine the result: 666 operates at the OS user's boundary.
func resolvePath(p string) (string, error) {
	p = strings.TrimSpace(p)
	if p == "" {
		return "", errors.New("path is empty")
	}
	if strings.ContainsRune(p, 0) {
		return "", errors.New("path contains a NUL byte")
	}
	if len(p) > 4096 {
		return "", errors.New("path is too long")
	}
	switch {
	case p == "~":
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			return "", errors.New("cannot resolve the home directory")
		}
		p = home
	case strings.HasPrefix(p, "~/"):
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			return "", errors.New("cannot resolve the home directory")
		}
		p = filepath.Join(home, strings.TrimPrefix(p, "~/"))
	}
	if !filepath.IsAbs(p) {
		abs, err := filepath.Abs(p)
		if err != nil {
			return "", err
		}
		p = abs
	}
	return filepath.Clean(p), nil
}

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

func intArg(args map[string]any, name string, def int) (int, error) {
	if args == nil {
		return def, nil
	}
	v, ok := args[name]
	if !ok {
		return def, nil
	}
	switch n := v.(type) {
	case float64:
		if n != float64(int(n)) {
			return 0, errors.New("not an integer")
		}
		return int(n), nil
	case int:
		return n, nil
	case json.Number:
		i, err := n.Int64()
		if err != nil {
			return 0, err
		}
		return int(i), nil
	default:
		return 0, errors.New("not an integer")
	}
}

func boolArg(args map[string]any, name string) (bool, bool) {
	if args == nil {
		return false, false
	}
	v, ok := args[name]
	if !ok {
		return false, false
	}
	b, ok := v.(bool)
	return b, ok
}

func errorJSON(tool, message string) string {
	raw, _ := marshalJSON(struct {
		OK    bool   `json:"ok"`
		Tool  string `json:"tool"`
		Error string `json:"error"`
	}{false, tool, message})
	return string(raw)
}

func writeJSON(tool, path string, bytes int, existed bool, note string) string {
	raw, _ := marshalJSON(struct {
		OK      bool   `json:"ok"`
		Tool    string `json:"tool"`
		Path    string `json:"path"`
		Bytes   int    `json:"bytes"`
		Created bool   `json:"created"`
		Note    string `json:"note,omitempty"`
	}{true, tool, path, bytes, !existed, note})
	return string(raw)
}

func editJSON(tool, path string, replacements, bytes int, note string) string {
	raw, _ := marshalJSON(struct {
		OK           bool   `json:"ok"`
		Tool         string `json:"tool"`
		Path         string `json:"path"`
		Replacements int    `json:"replacements"`
		Bytes        int    `json:"bytes"`
		Note         string `json:"note,omitempty"`
	}{true, tool, path, replacements, bytes, note})
	return string(raw)
}

func marshalJSON(v any) ([]byte, error) {
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return []byte(strings.TrimRight(b.String(), "\n")), nil
}

// boundedBuffer keeps the head and tail of a stream up to a fixed size. It
// can never grow with the command's output.
type boundedBuffer struct {
	mu        sync.Mutex
	head      []byte
	tail      []byte
	headLimit int
	tailLimit int
	total     int64
}

func newBoundedBuffer(limit int) *boundedBuffer {
	if limit < 2 {
		limit = 2
	}
	return &boundedBuffer{headLimit: limit / 2, tailLimit: limit - limit/2}
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(p)
	b.total += int64(n)
	if len(b.head) < b.headLimit {
		take := b.headLimit - len(b.head)
		if take > len(p) {
			take = len(p)
		}
		b.head = append(b.head, p[:take]...)
		p = p[take:]
	}
	if len(p) > 0 {
		if len(p) > b.tailLimit {
			p = p[len(p)-b.tailLimit:]
		}
		b.tail = append(b.tail, p...)
		if len(b.tail) > b.tailLimit {
			b.tail = append([]byte(nil), b.tail[len(b.tail)-b.tailLimit:]...)
		}
	}
	return n, nil
}

func (b *boundedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.total <= int64(len(b.head)+len(b.tail)) {
		return string(append(append([]byte(nil), b.head...), b.tail...))
	}
	omitted := b.total - int64(len(b.head)+len(b.tail))
	return string(b.head) + fmt.Sprintf("\n… [%d bytes omitted] …\n", omitted) + string(b.tail)
}

func (b *boundedBuffer) Truncated() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.total > int64(len(b.head)+len(b.tail))
}

// terminateGroup sends SIGTERM to the command's process group, waits up to
// the grace period, then sends SIGKILL. It is safe when the group is gone.
func terminateGroup(pid int, grace time.Duration) {
	if pid <= 0 {
		return
	}
	_ = syscall.Kill(-pid, syscall.SIGTERM)
	deadline := time.Now().Add(grace)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(-pid, 0); err != nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	_ = syscall.Kill(-pid, syscall.SIGKILL)
}

// terminateLingeringGroup removes background children after a normal shell
// exit. It is deliberately short: the command has already returned.
func terminateLingeringGroup(pid int) {
	if pid <= 0 {
		return
	}
	if err := syscall.Kill(-pid, 0); err != nil {
		return
	}
	_ = syscall.Kill(-pid, syscall.SIGTERM)
	deadline := time.Now().Add(300 * time.Millisecond)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(-pid, 0); err != nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	_ = syscall.Kill(-pid, syscall.SIGKILL)
}
