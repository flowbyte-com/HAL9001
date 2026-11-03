// Package workspace is HAL9001's one write capability: a single, operator
// approved text file inside a dedicated directory.
//
// The package holds no authority by itself. Prepare turns a model's
// proposal into an immutable record without touching the filesystem;
// only Approve — called from the trusted interface after a deliberate key
// action — performs one operation, and only for the exact path and
// content that were reviewed. Every check runs twice: when the proposal is
// prepared and again immediately before the write, so a destination that
// changed in between fails closed instead of being followed.
//
// Nothing here executes anything. A saved file is just a file.
package workspace

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// ToolName is the one operation a model may propose.
const ToolName = "write_file"

// DefaultDirName is the workspace directory created under the operator's
// home directory when nothing else is configured.
const DefaultDirName = "HAL"

// MaxBytes bounds one proposed file. Larger documents are refused before
// approval rather than after it: the operator never approves something the
// executor will reject.
const MaxBytes = 1 << 20

// maxPathDepth bounds how deep inside the workspace a proposal may reach.
const maxPathDepth = 16

// Errors are stable classes: callers show a short reason and the audit
// records the class, never the file contents.
var (
	// ErrPath reports a destination that is not a plain, relative path
	// inside the workspace.
	ErrPath = errors.New("workspace: destination is not a plain relative path")
	// ErrSymlink reports a path component that is a symbolic link.
	ErrSymlink = errors.New("workspace: destination crosses a symbolic link")
	// ErrNotRegular reports a destination that exists and is not a
	// regular file (a directory, device, socket, …).
	ErrNotRegular = errors.New("workspace: destination is not a regular file")
	// ErrTooLarge reports content above MaxBytes.
	ErrTooLarge = errors.New("workspace: content is too large")
	// ErrNotText reports content that is not UTF-8 text.
	ErrNotText = errors.New("workspace: content is not text")
	// ErrDestinationChanged reports that the destination changed between
	// approval and execution, or that a proposal was already consumed.
	ErrDestinationChanged = errors.New("workspace: destination changed since approval")
	// ErrProtected reports a destination inside a protected application
	// directory.
	ErrProtected = errors.New("workspace: destination is a protected directory")
	// ErrClosed reports a store without a usable root.
	ErrClosed = errors.New("workspace: store is not configured")
)

// Proposal is one reviewed operation. It is immutable by contract: the
// content is copied when the proposal is made, and Approve recomputes the
// digest before writing, so a mutation in between refuses the write.
type Proposal struct {
	// ID names this request in the audit trail.
	ID string
	// Path is the destination relative to the workspace, slash-separated.
	Path string
	// Content is the exact proposed file contents.
	Content []byte
	// Digest is the SHA-256 of Content, hex encoded.
	Digest string
	// Overwrite reports that a file exists at the destination.
	Overwrite bool
	// Existing is the size of the file that would be replaced.
	Existing int64
	// Session and Turn bind the proposal to one conversation turn.
	Session string
	Turn    int
	// Created is when the proposal was prepared.
	Created time.Time

	// existing is the identity of the file seen at preparation time.
	// Approve compares it with the destination to detect a swap.
	existing os.FileInfo
	// root and abs are resolved once, at preparation.
	root string
	abs  string
}

// Size returns the number of bytes the file would contain.
func (p Proposal) Size() int { return len(p.Content) }

// Entry is one item inside the workspace, for listing.
type Entry struct {
	// Path is relative to the workspace, slash-separated.
	Path string
	// Dir reports a directory.
	Dir bool
	// Bytes is the file size; zero for directories.
	Bytes int64
}

// List returns the entries directly inside a workspace directory. An empty
// path lists the workspace root.
//
// Listing is a read capability, not an authority: it never leaves the
// workspace, never follows a symbolic link and never recurses into one.
func (s *Store) List(rel string) ([]Entry, error) {
	if s == nil {
		return nil, ErrClosed
	}
	dir, err := s.readPath(rel, true)
	if err != nil {
		return nil, err
	}
	items, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, fmt.Errorf("workspace: %w", err)
	}
	if err != nil {
		return nil, fmt.Errorf("workspace: %w", err)
	}
	out := make([]Entry, 0, len(items))
	for _, item := range items {
		info, err := item.Info()
		if err != nil {
			continue
		}
		if info.Mode()&os.ModeSymlink != 0 {
			// A link is reported by name, never followed.
			info, err = os.Lstat(filepath.Join(dir, item.Name()))
			if err != nil {
				continue
			}
		}
		child := item.Name()
		if rel != "" && rel != "." {
			child = path.Join(rel, item.Name())
		}
		out = append(out, Entry{
			Path:  child,
			Dir:   info.IsDir(),
			Bytes: info.Size(),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

// ReadFile returns one workspace file's contents. It is bounded to the
// same size as a write and refused when it is not text.
func (s *Store) ReadFile(rel string) ([]byte, error) {
	if s == nil {
		return nil, ErrClosed
	}
	abs, err := s.readPath(rel, false)
	if err != nil {
		return nil, err
	}
	info, err := os.Lstat(abs)
	if err != nil {
		return nil, fmt.Errorf("workspace: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, ErrNotRegular
	}
	if info.Size() > MaxBytes {
		return nil, ErrTooLarge
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return nil, fmt.Errorf("workspace: %w", err)
	}
	if err := validateContent(data); err != nil {
		return nil, err
	}
	return data, nil
}

// readPath validates a read destination and proves it is inside the
// workspace and free of symbolic links.
func (s *Store) readPath(rel string, dir bool) (string, error) {
	if rel == "" || rel == "." {
		if !dir {
			return "", ErrPath
		}
		return s.root, nil
	}
	clean, err := cleanRelPath(rel)
	if err != nil {
		return "", err
	}
	abs, err := s.resolve(clean)
	if err != nil {
		return "", err
	}
	if err := s.checkPath(abs, dir); err != nil {
		return "", err
	}
	return abs, nil
}

// Result describes a file that was written.
type Result struct {
	Path      string
	Bytes     int64
	Overwrite bool
}

// Op is a proposed write before it is validated.
type Op struct {
	Path    string
	Content []byte
	Session string
	Turn    int
}

// Store is a workspace root plus its audit trail. A nil *Store is safe to
// use: every method reports ErrClosed instead of writing anywhere.
type Store struct {
	root    string
	deny    []string
	audit   *Audit
	now     func() time.Time
	homeDir string
}

// Open resolves a workspace root. The directory is created only when a
// write is approved, so configuring a workspace never mutates the disk.
//
// A root that is itself a symbolic link is resolved once: the workspace is
// the directory the operator pointed at. Symbolic links *inside* it are
// refused later, per operation.
func Open(dir string) (*Store, error) {
	expanded, err := expandHome(strings.TrimSpace(dir))
	if err != nil {
		return nil, err
	}
	if expanded == "" {
		return nil, ErrClosed
	}
	abs, err := filepath.Abs(expanded)
	if err != nil {
		return nil, fmt.Errorf("workspace: %w", err)
	}
	abs = filepath.Clean(abs)
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		abs = resolved
	}
	home, _ := os.UserHomeDir()
	s := &Store{root: abs, now: time.Now, homeDir: home}
	return s, nil
}

// WithAudit attaches the audit trail. A nil audit is allowed: the store
// still enforces the boundary, it just keeps no record.
func (s *Store) WithAudit(a *Audit) *Store {
	if s != nil {
		s.audit = a
	}
	return s
}

// Protect adds an application directory that proposals may never reach,
// even if the workspace were misconfigured to contain it.
func (s *Store) Protect(dir string) *Store {
	if s == nil {
		return s
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return s
	}
	s.deny = append(s.deny, filepath.Clean(abs))
	return s
}

// Root returns the absolute workspace directory.
func (s *Store) Root() string {
	if s == nil {
		return ""
	}
	return s.root
}

// DisplayPath renders a proposal's destination the way the interface shows
// it: ~/HAL/note.md when the workspace is under the home directory.
func (s *Store) DisplayPath(p Proposal) string {
	return s.displayPath(p.Path)
}

func (s *Store) displayPath(rel string) string {
	if s == nil {
		return rel
	}
	full := filepath.Join(s.root, filepath.FromSlash(rel))
	if s.homeDir != "" {
		if shortcut, err := filepath.Rel(s.homeDir, full); err == nil && !strings.HasPrefix(shortcut, "..") && !filepath.IsAbs(shortcut) {
			return filepath.Join("~", shortcut)
		}
	}
	return full
}

// Prepare validates a proposed write and returns the immutable record the
// operator reviews. It never creates, modifies or removes anything.
func (s *Store) Prepare(op Op) (Proposal, error) {
	if s == nil {
		return Proposal{}, ErrClosed
	}
	rel, err := cleanRelPath(op.Path)
	if err != nil {
		return Proposal{}, err
	}
	abs, err := s.resolve(rel)
	if err != nil {
		return Proposal{}, err
	}
	if err := s.checkDestination(abs); err != nil {
		return Proposal{}, err
	}
	if err := validateContent(op.Content); err != nil {
		return Proposal{}, err
	}
	content := append([]byte(nil), op.Content...)
	digest := sha256Hex(content)

	p := Proposal{
		ID:      newID(),
		Path:    rel,
		Content: content,
		Digest:  digest,
		Session: op.Session,
		Turn:    op.Turn,
		Created: s.now(),
		root:    s.root,
		abs:     abs,
	}
	if info, err := os.Lstat(abs); err == nil {
		p.Overwrite = true
		p.Existing = info.Size()
		p.existing = info
	} else if !os.IsNotExist(err) {
		return Proposal{}, fmt.Errorf("workspace: %w", err)
	}
	return p, nil
}

// Approve performs exactly one approved write. It re-validates everything,
// verifies that the destination is still the one that was reviewed, writes
// a temporary file and moves it into place, and records the outcome.
//
// A proposal can only be executed once: after a create the destination
// exists, and after a replace the file is no longer the one that was
// approved, so a replay fails with ErrDestinationChanged.
func (s *Store) Approve(p Proposal) (Result, error) {
	if s == nil {
		return Result{}, ErrClosed
	}
	res := Result{Path: p.Path, Overwrite: p.Overwrite}
	fail := func(err error) (Result, error) {
		s.record(p, true, outcomeFailed, errClass(err))
		return res, err
	}
	if p.root == "" || p.abs == "" {
		return fail(ErrDestinationChanged)
	}
	rel, err := cleanRelPath(p.Path)
	if err != nil {
		return fail(err)
	}
	abs, err := s.resolve(rel)
	if err != nil {
		return fail(err)
	}
	// The proposal must still describe the same destination and bytes.
	if abs != p.abs {
		return fail(ErrDestinationChanged)
	}
	if sha256Hex(p.Content) != p.Digest {
		return fail(ErrDestinationChanged)
	}
	if err := validateContent(p.Content); err != nil {
		return fail(err)
	}
	if err := s.checkDestination(abs); err != nil {
		return fail(err)
	}
	if err := s.prepareParents(abs); err != nil {
		return fail(err)
	}
	// Re-check the destination immediately before the write. For a
	// replacement the identity must match the file that was reviewed.
	cur, statErr := os.Lstat(abs)
	switch {
	case p.Overwrite:
		if statErr != nil || cur.Mode()&os.ModeSymlink != 0 || !cur.Mode().IsRegular() {
			return fail(ErrDestinationChanged)
		}
		if p.existing == nil || !os.SameFile(p.existing, cur) {
			return fail(ErrDestinationChanged)
		}
	case statErr == nil:
		return fail(ErrDestinationChanged)
	case !os.IsNotExist(statErr):
		return fail(statErr)
	}

	if p.Overwrite {
		if err := replaceFile(abs, p.Content); err != nil {
			return fail(err)
		}
	} else {
		if err := createFile(abs, p.Content); err != nil {
			return fail(err)
		}
	}
	res.Bytes = int64(len(p.Content))
	s.record(p, true, outcomeWritten, "")
	return res, nil
}

// Reject records an operator rejection. Nothing is written, and the
// proposal is not kept.
func (s *Store) Reject(p Proposal, reason string) error {
	if s == nil {
		return ErrClosed
	}
	s.record(p, false, outcomeNotRun, strings.TrimSpace(reason))
	return nil
}

// record appends one audit line. Audit failures are swallowed: bookkeeping
// must never turn a refused write into an approved one, or a written file
// into an error.
func (s *Store) record(p Proposal, approved bool, outcome, reason string) {
	if s == nil || s.audit == nil {
		return
	}
	decision := "rejected"
	if approved {
		decision = "approved"
	}
	_ = s.audit.Append(Record{
		Time:      s.now(),
		Session:   p.Session,
		Operation: "write",
		Path:      p.Path,
		Digest:    p.Digest,
		Bytes:     len(p.Content),
		Overwrite: p.Overwrite,
		Decision:  decision,
		Outcome:   outcome,
		Reason:    reason,
	})
}

// resolve joins a validated relative path onto the root and proves the
// result is still inside it.
func (s *Store) resolve(rel string) (string, error) {
	abs := filepath.Join(s.root, filepath.FromSlash(rel))
	back, err := filepath.Rel(s.root, abs)
	if err != nil || back == ".." || strings.HasPrefix(back, ".."+string(filepath.Separator)) || filepath.IsAbs(back) {
		return "", ErrPath
	}
	for _, deny := range s.deny {
		if within(deny, abs) {
			return "", ErrProtected
		}
	}
	return abs, nil
}

// checkDestination refuses symbolic links anywhere on the way to the
// destination, and a destination that is not a regular file.
func (s *Store) checkDestination(abs string) error {
	return s.checkPath(abs, false)
}

// checkPath is checkDestination with the expected kind of the final
// component: a regular file for a write, a directory for a listing.
func (s *Store) checkPath(abs string, dir bool) error {
	rel, err := filepath.Rel(s.root, abs)
	if err != nil {
		return ErrPath
	}
	parts := strings.Split(rel, string(filepath.Separator))
	cur := s.root
	for i, part := range parts {
		cur = filepath.Join(cur, part)
		info, err := os.Lstat(cur)
		if os.IsNotExist(err) {
			// The rest does not exist yet; the components checked so
			// far are real directories.
			return nil
		}
		if err != nil {
			return fmt.Errorf("workspace: %w", err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return ErrSymlink
		}
		if i < len(parts)-1 {
			if !info.IsDir() {
				return ErrNotRegular
			}
			continue
		}
		if dir {
			if !info.IsDir() {
				return ErrNotRegular
			}
			continue
		}
		if !info.Mode().IsRegular() {
			return ErrNotRegular
		}
	}
	return nil
}

// prepareParents creates the workspace root and the missing parent
// directories one component at a time, refusing symbolic links and
// non-directories on the way.
func (s *Store) prepareParents(abs string) error {
	if err := ensureDir(s.root); err != nil {
		return err
	}
	if err := mkdirChain(s.root, filepath.Dir(abs)); err != nil {
		return err
	}
	// The root may have been created just now; it must be a real
	// directory, not a link that appeared in the meantime.
	info, err := os.Lstat(s.root)
	if err != nil {
		return fmt.Errorf("workspace: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return ErrSymlink
	}
	return nil
}

// ensureDir creates dir when it is missing and refuses a link or a file.
// It creates exactly one level: a missing grandparent is a configuration
// problem, not something a write may invent.
func ensureDir(dir string) error {
	info, err := os.Lstat(dir)
	switch {
	case err == nil:
		if info.Mode()&os.ModeSymlink != 0 {
			return ErrSymlink
		}
		if !info.IsDir() {
			return ErrNotRegular
		}
		return nil
	case !os.IsNotExist(err):
		return fmt.Errorf("workspace: %w", err)
	}
	if err := os.Mkdir(dir, 0o755); err != nil {
		if os.IsExist(err) {
			info, lerr := os.Lstat(dir)
			if lerr == nil && info.IsDir() && info.Mode()&os.ModeSymlink == 0 {
				return nil
			}
		}
		return fmt.Errorf("workspace: %w", err)
	}
	return nil
}

// mkdirChain creates dir below root, one component at a time.
func mkdirChain(root, dir string) error {
	rel, err := filepath.Rel(root, dir)
	if err != nil {
		return ErrPath
	}
	if rel == "." {
		return nil
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return ErrPath
	}
	cur := root
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		cur = filepath.Join(cur, part)
		info, err := os.Lstat(cur)
		switch {
		case err == nil:
			if info.Mode()&os.ModeSymlink != 0 {
				return ErrSymlink
			}
			if !info.IsDir() {
				return ErrNotRegular
			}
			continue
		case !os.IsNotExist(err):
			return fmt.Errorf("workspace: %w", err)
		}
		if err := os.Mkdir(cur, 0o755); err != nil {
			if os.IsExist(err) {
				// Something appeared between the check and the
				// creation: look at it again rather than assume.
				info, lerr := os.Lstat(cur)
				if lerr == nil && info.IsDir() && info.Mode()&os.ModeSymlink == 0 {
					continue
				}
			}
			return fmt.Errorf("workspace: %w", err)
		}
	}
	return nil
}

// createFile writes a new file without ever replacing an existing one:
// O_EXCL makes the create atomic, and a late appearance of the
// destination is reported instead of overwritten.
func createFile(abs string, content []byte) error {
	f, err := os.OpenFile(abs, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		if os.IsExist(err) {
			return ErrDestinationChanged
		}
		return fmt.Errorf("workspace: %w", err)
	}
	if _, err := f.Write(content); err != nil {
		f.Close()
		_ = os.Remove(abs)
		return fmt.Errorf("workspace: %w", err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		_ = os.Remove(abs)
		return fmt.Errorf("workspace: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(abs)
		return fmt.Errorf("workspace: %w", err)
	}
	return nil
}

// replaceFile writes a temporary file beside the destination and renames
// it into place. The rename replaces the directory entry rather than
// following it, so a symbolic link swapped in after the checks cannot
// redirect the write outside the workspace.
func replaceFile(abs string, content []byte) error {
	dir := filepath.Dir(abs)
	tmp, err := tempName(dir)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("workspace: %w", err)
	}
	cleanup := func() {
		f.Close()
		_ = os.Remove(tmp)
	}
	if _, err := f.Write(content); err != nil {
		cleanup()
		return fmt.Errorf("workspace: %w", err)
	}
	if err := f.Sync(); err != nil {
		cleanup()
		return fmt.Errorf("workspace: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("workspace: %w", err)
	}
	if err := os.Rename(tmp, abs); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("workspace: %w", err)
	}
	return nil
}

// tempName returns an unused temporary path in dir.
func tempName(dir string) (string, error) {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", fmt.Errorf("workspace: %w", err)
	}
	return filepath.Join(dir, ".hal-write-"+hex.EncodeToString(buf[:])+".tmp"), nil
}

// cleanRelPath validates and normalises a workspace-relative destination.
func cleanRelPath(rel string) (string, error) {
	rel = strings.TrimSpace(rel)
	if rel == "" || strings.ContainsRune(rel, 0) {
		return "", ErrPath
	}
	// Backslashes are treated as separators everywhere, so a Windows-
	// style proposal cannot smuggle a path on another platform.
	rel = strings.ReplaceAll(rel, "\\", "/")
	if strings.HasPrefix(rel, "/") || filepath.IsAbs(rel) || filepath.VolumeName(rel) != "" {
		return "", ErrPath
	}
	parts := strings.Split(rel, "/")
	if len(parts) == 0 || len(parts) > maxPathDepth {
		return "", ErrPath
	}
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return "", ErrPath
		}
		if err := checkComponent(part); err != nil {
			return "", err
		}
	}
	return path.Join(parts...), nil
}

// reservedNames are the device names Windows refuses as file names.
var reservedNames = map[string]bool{
	"CON": true, "PRN": true, "AUX": true, "NUL": true,
}

// checkComponent refuses a name that would not be a plain file name on
// every supported platform.
func checkComponent(part string) error {
	if len(part) > 200 {
		return ErrPath
	}
	// Characters Windows cannot store, and the alternate-data-stream
	// separator on NTFS. Refusing them everywhere keeps a proposal
	// portable and prevents a name from meaning something else on
	// another platform.
	if strings.ContainsAny(part, `:*?"<>|`) {
		return ErrPath
	}
	if strings.HasSuffix(part, ".") || strings.HasSuffix(part, " ") {
		return ErrPath
	}
	upper := strings.ToUpper(part)
	if i := strings.IndexByte(upper, '.'); i >= 0 {
		upper = upper[:i]
	}
	if reservedNames[upper] {
		return ErrPath
	}
	if len(upper) == 4 && (strings.HasPrefix(upper, "COM") || strings.HasPrefix(upper, "LPT")) &&
		upper[3] >= '1' && upper[3] <= '9' {
		return ErrPath
	}
	return nil
}

// validateContent refuses anything that is not bounded UTF-8 text.
func validateContent(content []byte) error {
	if len(content) > MaxBytes {
		return ErrTooLarge
	}
	if bytes.IndexByte(content, 0) >= 0 || !utf8.Valid(content) {
		return ErrNotText
	}
	return nil
}

// within reports whether target is inside dir (or is dir itself).
func within(dir, target string) bool {
	rel, err := filepath.Rel(dir, target)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel))
}

// expandHome replaces a leading ~ with the operator's home directory.
func expandHome(p string) (string, error) {
	if p == "" || (p[0] != '~') {
		return p, nil
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "", fmt.Errorf("workspace: %w", ErrClosed)
	}
	if p == "~" {
		return home, nil
	}
	if strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`) {
		return filepath.Join(home, p[2:]), nil
	}
	return p, nil
}

// sha256Hex is the digest shown in the review and recorded in the audit.
func sha256Hex(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

// newID names one request. It carries no authority; it only lets the audit
// and the interface refer to the same proposal.
func newID() string {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return fmt.Sprintf("p-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(buf[:])
}

// Audit outcome names. They are stable: the trail is read by people.
const (
	outcomeWritten = "written"
	outcomeFailed  = "failed"
	outcomeNotRun  = "not-run"
)

// errClass turns an error into a short, content-free reason for the audit.
func errClass(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, ErrPath):
		return "path rejected"
	case errors.Is(err, ErrSymlink):
		return "symbolic link"
	case errors.Is(err, ErrNotRegular):
		return "not a regular file"
	case errors.Is(err, ErrTooLarge):
		return "too large"
	case errors.Is(err, ErrNotText):
		return "not text"
	case errors.Is(err, ErrDestinationChanged):
		return "destination changed"
	case errors.Is(err, ErrProtected):
		return "protected directory"
	case errors.Is(err, ErrClosed):
		return "workspace unavailable"
	default:
		return "write failed"
	}
}
