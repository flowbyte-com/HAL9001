// Package workspace is HAL9001's one write capability: a single, operator
// approved text file inside a dedicated directory.
//
// The package holds no authority by itself. Prepare turns a model's
// proposal into an immutable record without touching the filesystem;
// only Approve — called from the trusted interface after a deliberate key
// action — performs one operation, and only for the exact path and
// content that were reviewed.
//
// Every filesystem operation is resolved through an os.Root: a handle on
// the workspace directory that names each file relative to itself and
// refuses any path that leaves the tree. The checks in this package
// produce the operator's error messages; the root is what makes them
// true. A directory replaced by a symbolic link between the check and the
// write cannot redirect the write, because the write is resolved by the
// handle and not by a path assembled in user space.
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

// dirPerm is the mode of a directory the executor creates.
const dirPerm = 0o755

// filePerm is the mode of a file the executor creates. A saved file is
// never executable: writing one grants no way to run it.
const filePerm = 0o644

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

// errNoWorkspace reports that the workspace directory does not exist yet.
// It is not a refusal: the directory is created by the first approved
// write, never by a proposal.
var errNoWorkspace = fmt.Errorf("workspace: %w", os.ErrNotExist)

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
	// root is the workspace the proposal was prepared against. Approve
	// refuses a proposal that belongs to another workspace.
	root string
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

	// beforeWrite runs after every check and immediately before the
	// write itself. It exists so a test can move the filesystem inside
	// the exact window where a check-based executor would be vulnerable.
	// Production code never sets it.
	beforeWrite func()
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
	abs = resolvePath(filepath.Clean(abs))
	home, _ := os.UserHomeDir()
	if home != "" {
		home = resolvePath(home)
	}
	s := &Store{root: abs, now: time.Now, homeDir: home}
	return s, nil
}

// resolvePath returns the absolute path with every symbolic link in the
// part that already exists resolved.
//
// The workspace directory usually does not exist when the store is
// opened, so the whole path cannot be resolved at once: the deepest
// existing ancestor is resolved and the remaining names are appended.
// This keeps the root stable — and comparable — on systems where a
// parent directory is itself a link, as /var is on macOS.
func resolvePath(abs string) string {
	rest := ""
	cur := abs
	for {
		resolved, err := filepath.EvalSymlinks(cur)
		if err == nil {
			if rest == "" {
				return resolved
			}
			return filepath.Join(resolved, rest)
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			// Reached the top without finding an existing ancestor.
			return abs
		}
		rest = filepath.Join(filepath.Base(cur), rest)
		cur = parent
	}
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
	// Resolve the link so the comparison is made against the real
	// directory, whichever name it is reached by.
	abs = resolvePath(filepath.Clean(abs))
	s.deny = append(s.deny, abs)
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

// proposePath accepts the three forms the operator and the model may
// naturally use: a workspace-relative path, a path beginning with ~, or
// an absolute path. Tilde and absolute forms must resolve inside the
// workspace; they are converted to the same workspace-relative path the
// review and the executor use. A relative path is never stripped of a
// workspace-name component: HAL/HAL/... can be a legitimate nested
// destination.
func (s *Store) proposePath(raw string) (string, error) {
	if s == nil {
		return "", ErrClosed
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", ErrPath
	}
	if strings.HasPrefix(raw, "~") {
		home := s.homeDir
		if home == "" {
			var err error
			home, err = os.UserHomeDir()
			if err != nil || home == "" {
				return "", ErrClosed
			}
		}
		switch {
		case raw == "~":
			raw = home
		case strings.HasPrefix(raw, "~/") || strings.HasPrefix(raw, `~\`):
			raw = filepath.Join(home, raw[2:])
		default:
			// ~user is not supported; treating it as a relative name
			// would silently create a literal tilde directory.
			return "", ErrPath
		}
	}
	// Treat either slash form as a path separator for the absolute
	// check, so a Windows-style absolute path is classified consistently
	// on every platform. cleanRelPath still rejects drive letters.
	forAbs := strings.ReplaceAll(raw, `\`, "/")
	if filepath.IsAbs(filepath.FromSlash(forAbs)) {
		abs := filepath.Clean(filepath.FromSlash(forAbs))
		if !within(s.root, abs) {
			return "", ErrPath
		}
		rel, err := filepath.Rel(s.root, abs)
		if err != nil || rel == "." || rel == "" {
			return "", ErrPath
		}
		return cleanRelPath(filepath.ToSlash(rel))
	}
	return cleanRelPath(raw)
}

// Prepare validates a proposed write and returns the immutable record the
// operator reviews. It never creates, modifies or removes anything.
func (s *Store) Prepare(op Op) (Proposal, error) {
	if s == nil {
		return Proposal{}, ErrClosed
	}
	rel, err := s.proposePath(op.Path)
	if err != nil {
		return Proposal{}, err
	}
	if err := s.checkProtected(rel); err != nil {
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
	}
	// Inspect the destination, if there is a workspace to inspect. A
	// workspace that does not exist yet cannot hold a file, a link or a
	// replacement.
	root, err := s.rootHandle()
	switch {
	case err == nil:
		defer root.Close()
		if err := checkDestination(root, rel, false); err != nil {
			return Proposal{}, err
		}
		info, err := root.Lstat(rel)
		switch {
		case err == nil:
			p.Overwrite = true
			p.Existing = info.Size()
			p.existing = info
		case errors.Is(err, os.ErrNotExist):
			// Nothing is there: this is a create.
		default:
			return Proposal{}, rootError(err)
		}
	case errors.Is(err, os.ErrNotExist):
		// The workspace does not exist yet.
	default:
		return Proposal{}, err
	}
	return p, nil
}

// Approve performs exactly one approved write. It re-validates everything,
// verifies that the destination is still the one that was reviewed, and
// writes through the root handle, which resolves every name inside the
// workspace.
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
	if p.root == "" || p.root != s.root {
		// The proposal belongs to another workspace, or to none.
		return fail(ErrDestinationChanged)
	}
	rel, err := cleanRelPath(p.Path)
	if err != nil {
		return fail(err)
	}
	if err := s.checkProtected(rel); err != nil {
		return fail(err)
	}
	// The proposal must still describe the same bytes.
	if sha256Hex(p.Content) != p.Digest {
		return fail(ErrDestinationChanged)
	}
	if err := validateContent(p.Content); err != nil {
		return fail(err)
	}
	// The workspace directory is created by the first approved write.
	if err := s.ensureRootDir(); err != nil {
		return fail(err)
	}
	root, err := s.rootHandle()
	if err != nil {
		return fail(err)
	}
	defer root.Close()

	if err := checkDestination(root, rel, false); err != nil {
		return fail(err)
	}
	if err := ensureParents(root, rel); err != nil {
		return fail(err)
	}
	// Walk the path again now that the directories exist: a link
	// substituted for one of them in the meantime is still refused.
	if err := checkDestination(root, rel, false); err != nil {
		return fail(err)
	}
	// Re-check the destination immediately before the write. For a
	// replacement the identity must match the file that was reviewed.
	cur, statErr := root.Lstat(rel)
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
	case !errors.Is(statErr, os.ErrNotExist):
		return fail(rootError(statErr))
	}

	// Every check has passed. The write below is still confined by the
	// root handle, so a directory replaced by a link in this window
	// cannot redirect it.
	if s.beforeWrite != nil {
		s.beforeWrite()
	}

	if p.Overwrite {
		err = replaceFile(root, rel, p.Content)
	} else {
		err = createFile(root, rel, p.Content)
	}
	if err != nil {
		return fail(err)
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

// rootHandle opens the confined handle on the workspace.
//
// The handle is the enforcement mechanism: every later name is resolved
// relative to it, so no operation can leave the workspace even when the
// tree changes while the operation is in progress.
func (s *Store) rootHandle() (*os.Root, error) {
	if s == nil {
		return nil, ErrClosed
	}
	info, err := os.Lstat(s.root)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return nil, errNoWorkspace
	case err != nil:
		return nil, fmt.Errorf("workspace: %w", err)
	case info.Mode()&os.ModeSymlink != 0:
		return nil, ErrSymlink
	case !info.IsDir():
		return nil, ErrNotRegular
	}
	// The root path must still be the plain directory it was when the
	// store was opened. A link substituted for it would redirect every
	// later operation.
	if resolved, err := filepath.EvalSymlinks(s.root); err != nil || resolved != s.root {
		return nil, ErrSymlink
	}
	root, err := os.OpenRoot(s.root)
	if err != nil {
		return nil, fmt.Errorf("workspace: %w", err)
	}
	// The handle must refer to the directory that was just checked. The
	// window between the check and the open is closed by comparing the
	// two: a link substituted for the root in between would otherwise be
	// followed by the open itself.
	want, err := os.Lstat(s.root)
	if err != nil {
		root.Close()
		return nil, fmt.Errorf("workspace: %w", err)
	}
	got, err := root.Stat(".")
	if err != nil {
		root.Close()
		return nil, fmt.Errorf("workspace: %w", err)
	}
	if want.Mode()&os.ModeSymlink != 0 {
		root.Close()
		return nil, ErrSymlink
	}
	if !os.SameFile(want, got) {
		root.Close()
		return nil, ErrDestinationChanged
	}
	return root, nil
}

// ensureRootDir creates the workspace directory itself, once, before the
// first approved write. It refuses a link or a file in its place.
func (s *Store) ensureRootDir() error {
	info, err := os.Lstat(s.root)
	switch {
	case err == nil:
		if info.Mode()&os.ModeSymlink != 0 {
			return ErrSymlink
		}
		if !info.IsDir() {
			return ErrNotRegular
		}
		return nil
	case !errors.Is(err, os.ErrNotExist):
		return fmt.Errorf("workspace: %w", err)
	}
	if err := os.Mkdir(s.root, dirPerm); err != nil {
		if errors.Is(err, os.ErrExist) {
			// Something appeared between the check and the creation:
			// look at it again rather than assume.
			info, lerr := os.Lstat(s.root)
			if lerr == nil && info.IsDir() && info.Mode()&os.ModeSymlink == 0 {
				return nil
			}
		}
		return fmt.Errorf("workspace: %w", err)
	}
	return nil
}

// checkProtected refuses a destination inside a protected application
// directory.
//
// The comparison is made on logical paths and touches no file, so it
// cannot be raced: the root handle keeps the operation inside the
// workspace whatever the directory tree looks like.
func (s *Store) checkProtected(rel string) error {
	if s == nil || len(s.deny) == 0 {
		return nil
	}
	full := filepath.Join(s.root, filepath.FromSlash(rel))
	for _, deny := range s.deny {
		if within(deny, full) {
			return ErrProtected
		}
	}
	return nil
}

// checkDestination walks the destination inside the root and refuses a
// symbolic link anywhere on the way, a non-directory in the middle, and a
// final component that is not of the expected kind.
//
// The walk uses the root handle, so every name is resolved inside the
// workspace: the check cannot be pointed somewhere else.
func checkDestination(root *os.Root, rel string, dir bool) error {
	if rel == "" || rel == "." {
		if dir {
			return nil
		}
		return ErrPath
	}
	parts := strings.Split(rel, "/")
	cur := ""
	for i, part := range parts {
		if cur == "" {
			cur = part
		} else {
			cur += "/" + part
		}
		info, err := root.Lstat(cur)
		if errors.Is(err, os.ErrNotExist) {
			// The rest does not exist yet; the components checked so
			// far are real directories.
			return nil
		}
		if err != nil {
			return rootError(err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return ErrSymlink
		}
		if i < len(parts)-1 || dir {
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

// ensureParents creates the directories the destination needs, inside the
// root. The handle creates them relative to itself, so a link swapped in
// for one of them cannot move the creation outside the workspace.
func ensureParents(root *os.Root, rel string) error {
	dir := path.Dir(rel)
	if dir == "." || dir == "" {
		return nil
	}
	if err := root.MkdirAll(dir, dirPerm); err != nil {
		return rootError(err)
	}
	return nil
}

// createFile writes a new file without ever replacing an existing one:
// O_EXCL makes the create atomic, and a late appearance of the
// destination is reported instead of overwritten.
func createFile(root *os.Root, rel string, content []byte) error {
	f, err := root.OpenFile(rel, os.O_CREATE|os.O_EXCL|os.O_WRONLY, filePerm)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return ErrDestinationChanged
		}
		return rootError(err)
	}
	if _, err := f.Write(content); err != nil {
		f.Close()
		_ = root.Remove(rel)
		return fmt.Errorf("workspace: %w", err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		_ = root.Remove(rel)
		return fmt.Errorf("workspace: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = root.Remove(rel)
		return fmt.Errorf("workspace: %w", err)
	}
	return nil
}

// replaceFile writes a temporary file beside the destination and renames
// it into place. The rename replaces the directory entry rather than
// following it, and the handle keeps both names inside the workspace, so
// a symbolic link swapped in after the checks cannot redirect the write.
func replaceFile(root *os.Root, rel string, content []byte) error {
	tmp, err := tempRel(path.Dir(rel))
	if err != nil {
		return err
	}
	f, err := root.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, filePerm)
	if err != nil {
		return rootError(err)
	}
	cleanup := func() {
		f.Close()
		_ = root.Remove(tmp)
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
		_ = root.Remove(tmp)
		return fmt.Errorf("workspace: %w", err)
	}
	if err := root.Rename(tmp, rel); err != nil {
		_ = root.Remove(tmp)
		return rootError(err)
	}
	return nil
}

// tempRel returns an unused temporary name beside a workspace file. The
// name is relative to the root, so the file is created inside it.
func tempRel(dir string) (string, error) {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", fmt.Errorf("workspace: %w", err)
	}
	name := ".hal-write-" + hex.EncodeToString(buf[:]) + ".tmp"
	if dir == "." || dir == "" {
		return name, nil
	}
	return dir + "/" + name, nil
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
	dirName := "."
	prefix := ""
	if rel != "" && rel != "." {
		clean, err := cleanRelPath(rel)
		if err != nil {
			return nil, err
		}
		if err := s.checkProtected(clean); err != nil {
			return nil, err
		}
		dirName = clean
		prefix = clean
	}
	root, err := s.rootHandle()
	if err != nil {
		return nil, err
	}
	defer root.Close()
	if err := checkDestination(root, dirName, true); err != nil {
		return nil, err
	}
	dir, err := root.Open(dirName)
	if err != nil {
		return nil, rootError(err)
	}
	defer dir.Close()
	items, err := dir.ReadDir(-1)
	if err != nil {
		return nil, rootError(err)
	}
	out := make([]Entry, 0, len(items))
	for _, item := range items {
		info, err := item.Info()
		if err != nil {
			continue
		}
		if info.Mode()&os.ModeSymlink != 0 {
			// A link is reported by name, never followed.
			info, err = root.Lstat(path.Join(prefix, item.Name()))
			if err != nil {
				continue
			}
		}
		child := item.Name()
		if prefix != "" {
			child = path.Join(prefix, item.Name())
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
	clean, err := cleanRelPath(rel)
	if err != nil {
		return nil, err
	}
	if err := s.checkProtected(clean); err != nil {
		return nil, err
	}
	root, err := s.rootHandle()
	if err != nil {
		return nil, err
	}
	defer root.Close()
	if err := checkDestination(root, clean, false); err != nil {
		return nil, err
	}
	info, err := root.Lstat(clean)
	if err != nil {
		return nil, rootError(err)
	}
	if !info.Mode().IsRegular() {
		return nil, ErrNotRegular
	}
	if info.Size() > MaxBytes {
		return nil, ErrTooLarge
	}
	data, err := root.ReadFile(clean)
	if err != nil {
		return nil, rootError(err)
	}
	if err := validateContent(data); err != nil {
		return nil, err
	}
	return data, nil
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

// rootError classifies an error reported by the root handle.
//
// The standard library does not export the sentinel it uses for a path
// that leaves the workspace, so the wording is matched instead. It is the
// only error text the root produces for that refusal, and it has said so
// since the root was introduced.
func rootError(err error) error {
	if err == nil {
		return nil
	}
	if isEscape(err) {
		return ErrSymlink
	}
	return fmt.Errorf("workspace: %w", err)
}

// isEscape reports whether the root refused a path that left the
// workspace.
func isEscape(err error) bool {
	return err != nil && strings.Contains(err.Error(), "escapes from parent")
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
