// Package attachments copies operator-selected files into HAL's upload
// directory and hands out durable references to them.
//
// The boundary is deliberate. Ingestion is an operator action: only the
// operator, through the interface, chooses a file, and only that file is
// copied. The copy lands inside HAL's own upload directory, the source is
// never moved, modified or overwritten, and the model is offered no tool
// that can start an ingestion of its own.
//
// The destination is enforced with a confined root handle, so a symbolic
// link, a traversal or a concurrent substitution cannot move a copy out
// of the upload directory.
package attachments

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"
)

// The limits keep one operator action from becoming an unbounded amount
// of work. They are generous for source code and documents and small
// enough that a mistyped path cannot fill the disk.
const (
	// MaxFileBytes is the largest single file that may be attached.
	MaxFileBytes = 2 << 20 // 2 MiB
	// MaxFilesPerMessage is the largest number of files that may be
	// pending for one message.
	MaxFilesPerMessage = 10
	// MaxPendingBytes is the largest total size of the files pending for
	// one message.
	MaxPendingBytes = 4 << 20 // 4 MiB
	// MaxContentPerFile is the largest amount of one file's text that is
	// placed in a model request.
	MaxContentPerFile = 16 << 10 // 16 KiB
	// MaxContentTotal is the largest amount of attachment text that is
	// placed in one model request.
	MaxContentTotal = 64 << 10 // 64 KiB
)

// The file kinds. They describe what the file is, so the interface and
// the model request can say so plainly.
const (
	KindText     = "text"
	KindMarkdown = "markdown"
	KindCode     = "code"
	KindData     = "data"
)

// The failures an attachment can hit. Each one names a condition the
// operator can act on, and none of them leaves a partial copy behind.
var (
	// ErrMissing reports that the chosen path does not exist.
	ErrMissing = errors.New("no such file")
	// ErrDirectory reports that the chosen path is a directory.
	ErrDirectory = errors.New("path is a directory")
	// ErrNotRegular reports that the chosen path is not a regular file.
	ErrNotRegular = errors.New("not a regular file")
	// ErrTooLarge reports that the file is over the size limit.
	ErrTooLarge = errors.New("file is too large")
	// ErrBinary reports that the file is binary data.
	ErrBinary = errors.New("binary file")
	// ErrEncoding reports that the file's text encoding is not one HAL
	// can decode safely.
	ErrEncoding = errors.New("unsupported text encoding")
	// ErrSymlink reports that the upload directory itself was replaced
	// by a symbolic link.
	ErrSymlink = errors.New("the upload directory is a symbolic link")
	// ErrChanged reports that the chosen file changed while it was
	// being copied.
	ErrChanged = errors.New("the file changed while it was being attached")
	// ErrClosed reports that the store is not usable.
	ErrClosed = errors.New("attachments: the store is closed")
	// ErrUnknown reports that no attachment carries the given identity.
	ErrUnknown = errors.New("unknown attachment")
)

// Meta is the durable reference to one attached file.
//
// It carries metadata only: the file's contents live in the upload
// directory, and the session record keeps this reference rather than a
// second copy of the content.
type Meta struct {
	// ID is the short, opaque identity of the attachment. It is what
	// the operator names when detaching it.
	ID string `json:"id"`
	// Name is the original filename, exactly as the operator selected
	// it. It is metadata: the stored copy may carry a sanitized name.
	Name string `json:"name"`
	// Path is the copy's location relative to the upload directory.
	Path string `json:"path"`
	// Bytes is the size of the copy.
	Bytes int64 `json:"bytes"`
	// SHA256 is the checksum of the copy, so a later read can be shown
	// to be the same file.
	SHA256 string `json:"sha256"`
	// Kind is the file kind: text, markdown, code or data.
	Kind string `json:"kind"`
	// Source is the absolute path the operator selected. It stays
	// local: it is never part of a model request.
	Source string `json:"source,omitempty"`
	// Created is when the copy was made.
	Created time.Time `json:"created"`
}

// Display renders the attachment the way the interface shows it: the
// name and the size.
func (m Meta) Display() string {
	return m.Name + " (" + HumanSize(m.Bytes) + ")"
}

// HumanSize renders a byte count in the compact form the interface uses.
func HumanSize(n int64) string {
	switch {
	case n < 1024:
		return fmt.Sprintf("%d B", n)
	case n < 1024*1024:
		return fmt.Sprintf("%.1f KiB", float64(n)/1024)
	case n < 1024*1024*1024:
		return fmt.Sprintf("%.1f MiB", float64(n)/(1024*1024))
	default:
		return fmt.Sprintf("%.1f GiB", float64(n)/(1024*1024*1024))
	}
}

// Store owns one upload directory.
//
// A store is safe for concurrent use: two attachments made at the same
// time never share a destination, because every attachment gets its own
// directory with a random name.
type Store struct {
	root string
	home string
	now  func() time.Time
	rand io.Reader
}

// Open prepares the upload store for a directory.
//
// The directory is not created here: HAL writes nothing until the
// operator actually attaches a file.
//
// Only the parent of the upload directory is resolved. The upload
// directory itself is kept as a plain name, so a symbolic link placed in
// its place is refused rather than quietly followed somewhere else.
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
		return nil, fmt.Errorf("attachments: %w", err)
	}
	abs = filepath.Clean(abs)
	home, _ := os.UserHomeDir()
	if home != "" {
		home = resolvePath(filepath.Clean(home))
	}
	return &Store{
		root: filepath.Join(resolvePath(filepath.Dir(abs)), filepath.Base(abs)),
		home: home,
		now:  time.Now,
		rand: rand.Reader,
	}, nil
}

// Root reports the upload directory.
func (s *Store) Root() string {
	if s == nil {
		return ""
	}
	return s.root
}

// DisplayPath renders the upload directory the way the interface shows
// it: ~/HAL/uploads when it is under the home directory.
func (s *Store) DisplayPath() string {
	if s == nil {
		return ""
	}
	if s.home != "" && strings.HasPrefix(s.root, s.home+string(filepath.Separator)) {
		return filepath.Join("~", filepath.ToSlash(s.root[len(s.home)+1:]))
	}
	return s.root
}

// Attach copies a file into the upload directory and returns its
// reference.
//
// The source is only read: it is never moved, modified, truncated or
// overwritten. The copy is a faithful byte-for-byte duplicate, written
// into a directory of its own inside the upload directory.
func (s *Store) Attach(path string) (Meta, error) {
	if s == nil {
		return Meta{}, ErrClosed
	}
	src, info, err := s.resolveSource(path)
	if err != nil {
		return Meta{}, err
	}
	if info.Size() > MaxFileBytes {
		return Meta{}, fmt.Errorf("%w: %s is %s and the limit is %s",
			ErrTooLarge, filepath.Base(src), HumanSize(info.Size()), HumanSize(MaxFileBytes))
	}
	data, err := readBounded(src, MaxFileBytes)
	if err != nil {
		return Meta{}, err
	}
	if int64(len(data)) > MaxFileBytes {
		return Meta{}, fmt.Errorf("%w: %s is over the %s limit",
			ErrTooLarge, filepath.Base(src), HumanSize(MaxFileBytes))
	}
	// The file must still be the file that was chosen. A path that was
	// replaced while it was being read would otherwise produce a copy
	// of something else, without anybody noticing.
	after, err := os.Stat(src)
	if err != nil {
		return Meta{}, fmt.Errorf("%w: %s", ErrMissing, src)
	}
	if changed(info, after) {
		return Meta{}, fmt.Errorf("%w: %s", ErrChanged, filepath.Base(src))
	}
	// The file is decoded before it is copied, so an unsupported or
	// binary file is refused instead of being copied and then found
	// unusable.
	if _, err := Decode(data); err != nil {
		return Meta{}, fmt.Errorf("%s: %w", filepath.Base(src), err)
	}
	root, err := s.rootHandle()
	if err != nil {
		return Meta{}, err
	}
	defer root.Close()

	sum := sha256.Sum256(data)
	name := filepath.Base(src)
	meta := Meta{
		Name:    name,
		Bytes:   int64(len(data)),
		SHA256:  hex.EncodeToString(sum[:]),
		Kind:    classify(name),
		Source:  src,
		Created: s.now().UTC(),
	}
	// The copy gets its own directory under a random name. A directory
	// per attachment means two files with the name never collide, and a
	// concurrent attachment never observes a half-written copy.
	var (
		id      string
		created bool
	)
	for attempt := 0; attempt < 8 && !created; attempt++ {
		id, err = s.newID()
		if err != nil {
			return Meta{}, err
		}
		if err := root.Mkdir(id, dirPerm); err != nil {
			if errors.Is(err, os.ErrExist) {
				continue
			}
			return Meta{}, fmt.Errorf("attachments: %w", err)
		}
		created = true
	}
	if !created {
		return Meta{}, fmt.Errorf("attachments: could not find an unused attachment directory")
	}
	meta.ID = id
	meta.Path = filepath.ToSlash(filepath.Join(id, safeName(meta.Name)))
	if err := writeFile(root, meta.Path, data); err != nil {
		_ = root.RemoveAll(id)
		return Meta{}, err
	}
	if err := writeMeta(root, filepath.ToSlash(filepath.Join(id, "attachment.json")), meta); err != nil {
		_ = root.RemoveAll(id)
		return Meta{}, err
	}
	return meta, nil
}

// Size reports the size of a file the operator might attach, without
// copying it.
//
// The interface uses it to refuse a file that would take the pending set
// over its limit, so the refusal happens before a copy is made.
func (s *Store) Size(path string) (int64, error) {
	if s == nil {
		return 0, ErrClosed
	}
	_, info, err := s.resolveSource(path)
	if err != nil {
		return 0, err
	}
	return info.Size(), nil
}

// Load reads back the stored reference of one attachment.
func (s *Store) Load(id string) (Meta, error) {
	if s == nil {
		return Meta{}, ErrClosed
	}
	root, err := s.rootHandle()
	if err != nil {
		return Meta{}, err
	}
	defer root.Close()
	data, err := root.ReadFile(filepath.ToSlash(filepath.Join(id, "attachment.json")))
	if err != nil {
		return Meta{}, fmt.Errorf("%w: %s", ErrUnknown, id)
	}
	var meta Meta
	if err := json.Unmarshal(data, &meta); err != nil {
		return Meta{}, fmt.Errorf("%w: %s", ErrUnknown, id)
	}
	return meta, nil
}

// Content returns the decoded text of one attachment, bounded to max
// bytes. The second result reports whether the text was cut.
//
// The bound is applied to the decoded text, so a file of multi-byte
// characters is never cut in the middle of one.
//
// The copy is checked against the checksum it was stored with: the
// content discussed is the content that was attached, or the read
// fails.
func (s *Store) Content(meta Meta, max int) (string, bool, error) {
	if s == nil {
		return "", false, ErrClosed
	}
	if max <= 0 {
		return "", false, nil
	}
	root, err := s.rootHandle()
	if err != nil {
		return "", false, err
	}
	defer root.Close()
	raw, err := root.ReadFile(meta.Path)
	if err != nil {
		return "", false, fmt.Errorf("%w: %s", ErrUnknown, meta.Name)
	}
	if meta.SHA256 != "" {
		sum := sha256.Sum256(raw)
		if hex.EncodeToString(sum[:]) != meta.SHA256 {
			return "", false, fmt.Errorf("%w: the stored copy of %s no longer matches its checksum",
				ErrUnknown, meta.Name)
		}
	}
	text, err := Decode(raw)
	if err != nil {
		return "", false, err
	}
	return truncateText(text, max)
}

// resolveSource checks the operator's choice and reports the absolute
// path of the file to copy.
//
// A symbolic link is followed: the operator chose the file it points at.
// The copy is a regular file either way, so the link itself is never
// reproduced inside the upload directory.
func (s *Store) resolveSource(path string) (string, os.FileInfo, error) {
	expanded, err := expandHome(strings.TrimSpace(path))
	if err != nil {
		return "", nil, err
	}
	if expanded == "" {
		return "", nil, fmt.Errorf("%w: no path was given", ErrMissing)
	}
	abs, err := filepath.Abs(expanded)
	if err != nil {
		return "", nil, fmt.Errorf("attachments: %w", err)
	}
	abs = filepath.Clean(abs)
	info, err := os.Stat(abs)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return "", nil, fmt.Errorf("%w: %s", ErrMissing, abs)
	case errors.Is(err, os.ErrPermission):
		return "", nil, fmt.Errorf("%w: %s", ErrNotRegular, abs)
	case err != nil:
		return "", nil, fmt.Errorf("attachments: %w", err)
	}
	if info.IsDir() {
		return "", nil, fmt.Errorf("%w: %s", ErrDirectory, abs)
	}
	if !info.Mode().IsRegular() {
		return "", nil, fmt.Errorf("%w: %s", ErrNotRegular, abs)
	}
	return abs, info, nil
}

// changed reports whether the file at a path is still the file that was
// seen before.
//
// Two things count as a change: a different file now stands at the path,
// and the same file grew or shrank. Either one means the copy would not
// be the file the operator chose.
func changed(before, after os.FileInfo) bool {
	if before == nil || after == nil {
		return true
	}
	if !os.SameFile(before, after) {
		return true
	}
	return after.Size() != before.Size()
}

// rootHandle opens the confined handle on the upload directory.
//
// The handle is the enforcement mechanism: every later name is resolved
// relative to it, so no operation can leave the upload directory even
// when the tree changes while the operation is in progress.
func (s *Store) rootHandle() (*os.Root, error) {
	if s == nil {
		return nil, ErrClosed
	}
	if err := s.ensureRoot(); err != nil {
		return nil, err
	}
	info, err := os.Lstat(s.root)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return nil, ErrClosed
	case err != nil:
		return nil, fmt.Errorf("attachments: %w", err)
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
		return nil, fmt.Errorf("attachments: %w", err)
	}
	// The handle must refer to the directory that was just checked. The
	// window between the check and the open is closed by comparing the
	// two.
	want, err := os.Lstat(s.root)
	if err != nil {
		root.Close()
		return nil, fmt.Errorf("attachments: %w", err)
	}
	got, err := root.Stat(".")
	if err != nil {
		root.Close()
		return nil, fmt.Errorf("attachments: %w", err)
	}
	if want.Mode()&os.ModeSymlink != 0 {
		root.Close()
		return nil, ErrSymlink
	}
	if !os.SameFile(want, got) {
		root.Close()
		return nil, fmt.Errorf("attachments: the upload directory changed while it was opened")
	}
	return root, nil
}

// ensureRoot creates the upload directory itself, once, before the first
// attachment. It refuses a link or a file in its place.
func (s *Store) ensureRoot() error {
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
		return fmt.Errorf("attachments: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(s.root), dirPerm); err != nil {
		return fmt.Errorf("attachments: %w", err)
	}
	if err := os.Mkdir(s.root, dirPerm); err != nil {
		if errors.Is(err, os.ErrExist) {
			// Something appeared between the check and the creation:
			// look at it again rather than assume.
			info, lerr := os.Lstat(s.root)
			if lerr == nil && info.IsDir() && info.Mode()&os.ModeSymlink == 0 {
				return nil
			}
			return ErrSymlink
		}
		return fmt.Errorf("attachments: %w", err)
	}
	return nil
}

// newID mints the random name of one attachment directory.
func (s *Store) newID() (string, error) {
	var b [6]byte
	if _, err := io.ReadFull(s.rand, b[:]); err != nil {
		return "", fmt.Errorf("attachments: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

// writeFile writes a new file without ever replacing an existing one.
func writeFile(root *os.Root, rel string, data []byte) error {
	f, err := root.OpenFile(rel, os.O_CREATE|os.O_EXCL|os.O_WRONLY, filePerm)
	if err != nil {
		return fmt.Errorf("attachments: %w", err)
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		_ = root.Remove(rel)
		return fmt.Errorf("attachments: %w", err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		_ = root.Remove(rel)
		return fmt.Errorf("attachments: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = root.Remove(rel)
		return fmt.Errorf("attachments: %w", err)
	}
	return nil
}

// writeMeta stores the reference beside the copy, so the upload
// directory explains itself without the session record.
func writeMeta(root *os.Root, rel string, meta Meta) error {
	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return fmt.Errorf("attachments: %w", err)
	}
	return writeFile(root, rel, append(data, '\n'))
}

// Decode turns a file's bytes into text.
//
// Only encodings HAL can decode without guessing are accepted: UTF-8,
// with or without a byte-order mark, and UTF-16 with a byte-order mark.
// Anything else is refused. Binary data is never silently converted
// into text.
func Decode(data []byte) (string, error) {
	switch {
	case len(data) >= 3 && data[0] == 0xEF && data[1] == 0xBB && data[2] == 0xBF:
		body := data[3:]
		if !utf8.Valid(body) {
			return "", ErrEncoding
		}
		return string(body), nil
	case len(data) >= 2 && data[0] == 0xFF && data[1] == 0xFE:
		body := data[2:]
		if len(body)%2 != 0 {
			return "", ErrEncoding
		}
		return decodeUTF16(body, binary.LittleEndian), nil
	case len(data) >= 2 && data[0] == 0xFE && data[1] == 0xFF:
		body := data[2:]
		if len(body)%2 != 0 {
			return "", ErrEncoding
		}
		return decodeUTF16(body, binary.BigEndian), nil
	}
	if !utf8.Valid(data) {
		if looksBinary(data) {
			return "", ErrBinary
		}
		return "", ErrEncoding
	}
	// Valid UTF-8 can still be binary: a NUL byte is the classic marker,
	// and a control character is perfectly valid UTF-8. The one
	// exception is UTF-16 text without a byte-order mark, which is text
	// HAL cannot decode safely rather than binary data.
	if looksBinary(data) {
		if looksUTF16Text(data) {
			return "", ErrEncoding
		}
		return "", ErrBinary
	}
	return string(data), nil
}

// decodeUTF16 turns UTF-16 code units into text.
func decodeUTF16(data []byte, order binary.ByteOrder) string {
	units := make([]uint16, 0, len(data)/2)
	for i := 0; i+1 < len(data); i += 2 {
		units = append(units, order.Uint16(data[i:i+2]))
	}
	return string(utf16.Decode(units))
}

// looksBinary reports whether the data looks like binary content: a
// NUL byte in the first few kilobytes is the classic marker.
func looksBinary(data []byte) bool {
	limit := len(data)
	if limit > 8000 {
		limit = 8000
	}
	for i := 0; i < limit; i++ {
		if data[i] == 0 {
			return true
		}
	}
	return false
}

// looksUTF16Text reports whether the data has the shape of UTF-16 text
// written without a byte-order mark: characters alternating with NUL
// bytes.
//
// Such a file is text, not binary. HAL cannot decode it safely, so it
// is refused as an unsupported encoding rather than as binary data. The
// shape must be clear — several pairs, and nearly all of them alike —
// so a short binary blob is never mistaken for text.
func looksUTF16Text(data []byte) bool {
	if len(data) < 8 || len(data)%2 != 0 {
		return false
	}
	pairs := len(data) / 2
	if pairs > 64 {
		pairs = 64
	}
	le, be := 0, 0
	for i := 0; i < pairs; i++ {
		lo, hi := data[2*i], data[2*i+1]
		switch {
		case hi == 0 && lo != 0:
			le++
		case lo == 0 && hi != 0:
			be++
		}
	}
	return le*3 >= pairs*2 || be*3 >= pairs*2
}

// truncateText cuts text to a byte budget without splitting a character.
func truncateText(text string, max int) (string, bool, error) {
	if max <= 0 {
		return "", false, nil
	}
	if len(text) <= max {
		return text, false, nil
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut], true, nil
}

// readBounded reads a file, refusing to read more than limit bytes.
func readBounded(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%w: %s", ErrMissing, path)
		}
		if errors.Is(err, os.ErrPermission) {
			return nil, fmt.Errorf("%w: %s cannot be read", ErrNotRegular, path)
		}
		return nil, fmt.Errorf("attachments: %w", err)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, fmt.Errorf("attachments: %w", err)
	}
	return data, nil
}

// safeName turns the original filename into the name of the copy.
//
// The name keeps its letters, digits and marks so a Unicode filename
// stays readable, and everything else becomes an underscore. The result
// is one path component: it can never reach outside the attachment's own
// directory.
func safeName(name string) string {
	base := filepath.Base(strings.ReplaceAll(name, "\\", "/"))
	var b strings.Builder
	for _, r := range base {
		switch {
		case r < 0x20 || r == 0x7f:
			// Control characters are dropped.
		case r == '/' || r == '\\':
			b.WriteRune('_')
		case unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsMark(r):
			b.WriteRune(r)
		case r == '.' || r == '-' || r == '_':
			b.WriteRune(r)
		case r == ' ':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	out := strings.TrimSpace(b.String())
	// A name made only of dots would name a directory or a parent, not a
	// file.
	if strings.Trim(out, ".") == "" {
		return "attachment"
	}
	out = strings.TrimPrefix(out, ".")
	if out == "" {
		return "attachment"
	}
	// The name is bounded so a very long filename cannot make an
	// unwieldy path.
	if len(out) > 96 {
		out = out[:96]
		for len(out) > 0 && !utf8.RuneStart(out[len(out)-1]) {
			out = out[:len(out)-1]
		}
	}
	return out
}

// classify names the kind of a file from its extension.
func classify(name string) string {
	ext := strings.ToLower(filepath.Ext(name))
	switch ext {
	case ".md", ".markdown", ".mdx":
		return KindMarkdown
	case ".json", ".yaml", ".yml", ".toml", ".csv", ".tsv", ".xml", ".ini",
		".cfg", ".conf", ".properties", ".env":
		return KindData
	case ".go", ".py", ".js", ".mjs", ".cjs", ".ts", ".tsx", ".jsx", ".java",
		".kt", ".kts", ".c", ".h", ".cc", ".cpp", ".hpp", ".rs", ".rb", ".php",
		".sh", ".bash", ".zsh", ".fish", ".ps1", ".pl", ".lua", ".r", ".swift",
		".scala", ".hs", ".ex", ".exs", ".erl", ".clj", ".sql", ".vue", ".svelte",
		".css", ".scss", ".sass", ".less", ".html", ".htm", ".vim", ".el", ".m":
		return KindCode
	default:
		return KindText
	}
}

// expandHome turns a leading ~ into the home directory.
func expandHome(p string) (string, error) {
	if p == "" {
		return "", nil
	}
	if p != "~" && !strings.HasPrefix(p, "~/") {
		return p, nil
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "", fmt.Errorf("attachments: the home directory is unknown")
	}
	if p == "~" {
		return home, nil
	}
	return filepath.Join(home, p[2:]), nil
}

// resolvePath returns the absolute path with every symbolic link in the
// part that already exists resolved.
//
// The upload directory usually does not exist when the store is opened,
// so the whole path cannot be resolved at once: the deepest existing
// ancestor is resolved and the remaining names are appended.
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
			return abs
		}
		rest = filepath.Join(filepath.Base(cur), rest)
		cur = parent
	}
}

// The permissions of the upload directory and of the copies inside it.
// The copies are readable by the operator only, like HAL's own data.
const (
	dirPerm  = 0o700
	filePerm = 0o600
)
