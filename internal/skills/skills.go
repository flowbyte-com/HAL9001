// Package skills discovers and loads MPM-managed skills for HAL9001.
//
// HAL reaches the skill store through MPM's own command line: the
// canonical, documented way for an external agent to read a skill. The
// only actions HAL ever sends are "list" and "read". It never saves,
// deletes, promotes or edits a skill, and it keeps no second copy of the
// store — MPM stays the single source of truth.
//
// A skill shapes how HAL writes. It never widens what HAL may do: the
// skill's text is loaded into one request, it is never appended to the
// persona, and it can never grant a capability the application did not
// already have.
package skills

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
)

// ErrUnavailable reports that MPM could not be reached. It is not a
// failure of the conversation: HAL simply behaves like ordinary HAL.
var ErrUnavailable = errors.New("skills: the skill store is not available")

// DefaultTimeout bounds one call to MPM. The calls are local and cheap;
// the bound is there so a wedged process cannot stall a turn.
const DefaultTimeout = 10 * time.Second

// Entry is one skill in the catalogue.
//
// The catalogue is the lightweight projection MPM returns for an
// inventory query: it carries the activation guidance but not the
// instructions themselves.
type Entry struct {
	// ID is the store's own identifier for the skill, such as
	// "skill:humanize-text-v1.0.1". It is stable for the lifetime of
	// that version.
	ID string
	// Name is the skill's name. A skill is always read by name, so the
	// name is the identity that survives an update.
	Name      string
	Version   string
	WhenToUse string
	Domain    string
	Global    bool
}

// Skill is one loaded skill: the instructions and the hard constraints
// that come with them.
type Skill struct {
	// ID is the store's identifier for the exact version that was read.
	ID string
	// Name is the skill's name.
	Name string
	// Version is the version that was read.
	Version string
	// Hash is the content hash of the instructions that were supplied.
	// It is what makes an attribution historical: a later update of the
	// skill in the store cannot change what an earlier turn used.
	Hash        string
	Domain      string
	WhenToUse   string
	Body        string
	Constraints []string
	// Package describes the multi-file package this skill was projected
	// from, when the store says so. It is nil for single-document
	// skills, and nil whenever the store reports no package identity:
	// absence means "unknown", never "not packaged".
	Package *PackageRef
}

// PackageRef is the package identity MPM reports for a skill, if any.
//
// Every field here is store-supplied metadata, carried for attribution
// and display only. It never authorises anything, never triggers
// further reads on its own, and never claims more than the entrypoint
// projection was loaded: supporting files stay unread until an
// explicit, bounded package read asks for them.
type PackageRef struct {
	// SkillID is the store's skill identifier the package belongs to.
	SkillID string
	// Entrypoint is the package path of the loaded projection, such as
	// "SKILL.md".
	Entrypoint string
	// EntrypointSHA256 is the store-reported digest of the entrypoint
	// file, when the store reports one.
	EntrypointSHA256 string
	// PackageHash is the store-reported digest of the package as a
	// whole, when the store reports one.
	PackageHash string
	// Files lists the supporting files the store reports, without their
	// bytes. It is capped at parse time; a longer listing is truncated
	// and marked incomplete rather than read eagerly.
	Files []PackageFile
	// Complete reports whether Files lists every supporting file. False
	// means the listing was capped or the store did not enumerate them:
	// either way, only the entrypoint was loaded.
	Complete bool
}

// PackageFile is one supporting file named by the store: identity and
// size only, never content. Content arrives only through an explicit,
// bounded chunk read.
type PackageFile struct {
	// Path is the store-canonical, slash-separated package path.
	Path string
	// SHA256 is the store-reported digest of the file, when reported.
	SHA256 string
	// Size is the store-reported byte size. Negative means unknown.
	Size int64
	// Role is the store-reported role (for example "docs", "script",
	// "reference"), when reported. It is descriptive only: a role never
	// grants execution, and HAL never executes package files.
	Role string
}

// ContentHash is the hash of the exact instructions of a skill.
//
// It covers the body and the hard constraints, which are the whole of
// what the store supplied. The hash is taken over the bytes that went
// into the request, so an attribution recorded against it stays true
// after the skill is updated in the store.
func ContentHash(body string, constraints []string) string {
	h := sha256.New()
	h.Write([]byte(body))
	for _, c := range constraints {
		h.Write([]byte{0})
		h.Write([]byte(c))
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

// Hash is the content hash of this skill's instructions.
func (s Skill) ContentHash() string {
	if s.Hash != "" {
		return s.Hash
	}
	return ContentHash(s.Body, s.Constraints)
}

// Catalogue is the set of skills MPM offers.
type Catalogue struct {
	Entries []Entry
	// Err reports why the catalogue is empty, when it is.
	Err error
	// Loaded reports whether the catalogue was fetched at least once.
	Loaded bool
}

// Names lists the catalogue's skill names.
func (c Catalogue) Names() []string {
	out := make([]string, 0, len(c.Entries))
	for _, e := range c.Entries {
		out = append(out, e.Name)
	}
	return out
}

// Has reports whether the catalogue holds a skill with this name.
func (c Catalogue) Has(name string) bool {
	for _, e := range c.Entries {
		if strings.EqualFold(e.Name, name) {
			return true
		}
	}
	return false
}

// Store is the read side of a skill store.
//
// The production implementation is the MPM command line; a test can
// supply its own. The interface is read-only by construction: there is no
// way to save, delete or edit a skill through it.
type Store interface {
	// List returns the skills the store offers, newest version of each.
	List(ctx context.Context) ([]Entry, error)
	// Read returns one skill's instructions.
	Read(ctx context.Context, name string) (Skill, error)
}

// Client reads skills from MPM's command line.
type Client struct {
	// Bin is the mpm executable. Empty means it is resolved.
	Bin string
	// Timeout bounds one call. Zero means DefaultTimeout.
	Timeout time.Duration
	// Env is the environment for the call. Empty means the current one.
	Env []string
}

// listEnvelope mirrors the answer to a "list" action.
//
// The catalogue entries use MPM's SkillSummary, which carries no JSON
// tags: their keys are the Go field names, so they are capitalised.
type listEnvelope struct {
	Success bool           `json:"success"`
	Error   string         `json:"error"`
	Count   int            `json:"count"`
	Scope   string         `json:"scope"`
	Skills  []skillSummary `json:"skills"`
}

type skillSummary struct {
	ID        string `json:"ID"`
	Name      string `json:"Name"`
	Version   string `json:"Version"`
	WhenToUse string `json:"WhenToUse"`
	IsGlobal  bool   `json:"IsGlobal"`
	Weight    int    `json:"Weight"`
}

// readEnvelope mirrors the answer to a "read" action.
type readEnvelope struct {
	Success     bool             `json:"success"`
	Error       string           `json:"error"`
	ID          string           `json:"id"`
	Name        string           `json:"name"`
	Version     string           `json:"version"`
	WhenToUse   string           `json:"when_to_use"`
	Domain      string           `json:"domain"`
	Constraints []string         `json:"constraints"`
	Body        string           `json:"body"`
	IsGlobal    bool             `json:"is_global"`
	Package     *packageEnvelope `json:"package"`
}

// packageEnvelope mirrors the optional package block of a read
// response.
//
// MPM's read-only package operations do not exist yet (tracked
// upstream as P8), so no production server emits this block today. It
// is parsed only so a packaged skill is recognised when a response
// does carry package identity -- including fixture doubles that speak
// the same vocabulary as MPM's own staged package schema (skill_id
// keying, entrypoint plus sha256, package_hash, per-file
// path/sha256/size_bytes/role). Field names here are HAL-side
// provisional placeholders, not the P8 contract; aligning with P8 is
// a separate, clearly identified integration step.
//
// Every field is optional and every value is untrusted display
// metadata. An absent block changes nothing: the skill is treated
// exactly as a single-document skill. A present block never triggers
// further reads, never executes anything, and never claims the full
// package was loaded -- only the entrypoint projection in Body.
type packageEnvelope struct {
	SkillID          string                `json:"skill_id"`
	Entrypoint       string                `json:"entrypoint"`
	EntrypointSHA256 string                `json:"entrypoint_sha256"`
	PackageHash      string                `json:"package_hash"`
	Files            []packageFileEnvelope `json:"files"`
}

// packageFileEnvelope is one supporting file named by a read response:
// identity and size only, never content.
type packageFileEnvelope struct {
	Path      string      `json:"path"`
	SHA256    string      `json:"sha256"`
	SizeBytes json.Number `json:"size_bytes"`
	Role      string      `json:"role"`
}

// List returns the skills MPM offers, newest version of each name.
func (c *Client) List(ctx context.Context) ([]Entry, error) {
	raw, err := c.call(ctx, `{"action":"list","params":{"scope":"all"}}`)
	if err != nil {
		return nil, err
	}
	var env listEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("skills: unreadable catalogue: %w", err)
	}
	if !env.Success {
		return nil, fmt.Errorf("skills: catalogue failed: %s", env.Error)
	}
	out := make([]Entry, 0, len(env.Skills))
	for _, s := range env.Skills {
		name := s.Name
		if name == "" {
			continue
		}
		out = append(out, Entry{
			ID:        s.ID,
			Name:      name,
			Version:   s.Version,
			WhenToUse: s.WhenToUse,
			Global:    s.IsGlobal,
		})
	}
	return out, nil
}

// Read fetches one skill's instructions.
//
// The skill is read by name and without a version, so MPM resolves the
// latest one: a skill can be updated in MPM and HAL picks it up without
// being rebuilt.
func (c *Client) Read(ctx context.Context, name string) (Skill, error) {
	payload, err := json.Marshal(map[string]any{
		"action": "read",
		"params": map[string]any{"name": name},
	})
	if err != nil {
		return Skill{}, fmt.Errorf("skills: %w", err)
	}
	raw, err := c.call(ctx, string(payload))
	if err != nil {
		return Skill{}, err
	}
	var env readEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return Skill{}, fmt.Errorf("skills: unreadable skill %q: %w", name, err)
	}
	if !env.Success {
		return Skill{}, fmt.Errorf("skills: %q could not be read: %s", name, env.Error)
	}
	return Skill{
		Package:     packageRefFromEnvelope(env.ID, env.Package),
		ID:          env.ID,
		Name:        env.Name,
		Version:     env.Version,
		Hash:        ContentHash(env.Body, env.Constraints),
		Domain:      env.Domain,
		WhenToUse:   env.WhenToUse,
		Body:        env.Body,
		Constraints: env.Constraints,
	}, nil
}

// call runs one mpm invocation and returns the JSON envelope it printed.
func (c *Client) call(ctx context.Context, payload string) ([]byte, error) {
	bin, err := c.binary()
	if err != nil {
		return nil, err
	}
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, bin, "call", "mpm_skills", "--payload", payload)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if len(c.Env) > 0 {
		cmd.Env = c.Env
	}
	if err := cmd.Run(); err != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail != "" {
			return nil, fmt.Errorf("%w: %s: %s", ErrUnavailable, bin, firstLine(detail))
		}
		return nil, fmt.Errorf("%w: %s: %w", ErrUnavailable, bin, err)
	}
	return lastJSONObject(stdout.Bytes())
}

// binary resolves the mpm executable.
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

// lastJSONObject returns the last JSON object printed on stdout.
//
// MPM writes one envelope on stdout, but it may print progress lines
// before it. The agent protocol reads the last JSON line, and so does
// this.
func lastJSONObject(out []byte) ([]byte, error) {
	lines := bytes.Split(out, []byte("\n"))
	for i := len(lines) - 1; i >= 0; i-- {
		line := bytes.TrimSpace(lines[i])
		if len(line) < 2 || line[0] != '{' || line[len(line)-1] != '}' {
			continue
		}
		var probe map[string]any
		if err := json.Unmarshal(line, &probe); err != nil {
			continue
		}
		return line, nil
	}
	return nil, fmt.Errorf("%w: no JSON envelope in the output", ErrUnavailable)
}

// firstLine keeps an error message to one line.
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// MaxPackageFilesParsed caps how many supporting files HAL records
// from one read response. Anything beyond the cap is dropped and the
// PackageRef is marked incomplete rather than read eagerly: listing
// names is cheap, loading bytes is not.
const MaxPackageFilesParsed = 128

// packageRefFromEnvelope builds the package identity of a read
// response, or nil when the response carries no package identity. A
// present-but-empty block is treated as absent: without at least a
// skill id, an entrypoint path, or a package hash, there is nothing to
// recognise.
func packageRefFromEnvelope(skillID string, env *packageEnvelope) *PackageRef {
	if env == nil {
		return nil
	}
	if strings.TrimSpace(env.SkillID) == "" &&
		strings.TrimSpace(env.Entrypoint) == "" &&
		strings.TrimSpace(env.PackageHash) == "" {
		return nil
	}
	id := strings.TrimSpace(env.SkillID)
	if id == "" {
		id = skillID
	}
	ref := &PackageRef{
		SkillID:          id,
		Entrypoint:       strings.TrimSpace(env.Entrypoint),
		EntrypointSHA256: strings.TrimSpace(env.EntrypointSHA256),
		PackageHash:      strings.TrimSpace(env.PackageHash),
		Complete:         true,
	}
	for _, f := range env.Files {
		path := strings.TrimSpace(f.Path)
		if path == "" {
			continue
		}
		if len(ref.Files) >= MaxPackageFilesParsed {
			ref.Complete = false
			break
		}
		size := int64(-1)
		if n, err := f.SizeBytes.Int64(); err == nil {
			size = n
		}
		ref.Files = append(ref.Files, PackageFile{
			Path:   path,
			SHA256: strings.TrimSpace(f.SHA256),
			Size:   size,
			Role:   strings.TrimSpace(f.Role),
		})
	}
	if env.Files == nil {
		// The store did not enumerate supporting files: either way,
		// only the entrypoint projection was loaded.
		ref.Complete = false
	}
	return ref
}

// Selector owns the catalogue, the routing decision and the loading of a
// selected skill's instructions.
//
// It is shared by the engine, which routes each turn, and the interface,
// which lists the skills and lets the operator override the choice. Its
// state is guarded because the engine routes on its own goroutine while
// the interface reads the last decision.
type Selector struct {
	store Store

	mu       sync.Mutex
	entries  []Entry
	loaded   bool
	loadErr  error
	forced   []string
	sticky   string
	last     Decision
	lastUsed time.Time
}

// NewSelector builds a selector over a store. A nil store makes the
// selector inert: no skill is ever selected.
func NewSelector(store Store) *Selector {
	return &Selector{store: store}
}

// Refresh reloads the catalogue from MPM.
//
// A failure is remembered rather than raised: the interface shows that
// the store is unavailable and the conversation continues.
func (s *Selector) Refresh(ctx context.Context) error {
	if s == nil || s.store == nil {
		return ErrUnavailable
	}
	entries, err := s.store.List(ctx)
	s.mu.Lock()
	defer s.mu.Unlock()
	if err != nil {
		s.loadErr = err
		return err
	}
	s.entries = entries
	s.loaded = true
	s.loadErr = nil
	return nil
}

// Catalogue returns the cached catalogue.
func (s *Selector) Catalogue() Catalogue {
	if s == nil {
		return Catalogue{Err: ErrUnavailable}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return Catalogue{Entries: append([]Entry(nil), s.entries...), Err: s.loadErr, Loaded: s.loaded}
}

// Forced returns the operator's explicit selection.
func (s *Selector) Forced() []string {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.forced...)
}

// SetForced pins the skill for the writing task ahead.
//
// It is the operator's override: the router stops choosing, and the named
// skill is used until the override is cleared. An empty name clears it.
func (s *Selector) SetForced(names []string) error {
	if s == nil {
		return ErrUnavailable
	}
	clean := make([]string, 0, len(names))
	for _, n := range names {
		n = strings.TrimSpace(n)
		if n == "" {
			continue
		}
		if !s.Catalogue().Has(n) {
			return fmt.Errorf("skills: no skill named %q", n)
		}
		clean = append(clean, n)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.forced = clean
	if len(clean) > 0 {
		s.sticky = clean[0]
	}
	return nil
}

// ClearForced restores automatic routing.
func (s *Selector) ClearForced() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.forced = nil
	s.sticky = ""
}

// LastDecision returns the routing decision of the latest turn.
func (s *Selector) LastDecision() Decision {
	if s == nil {
		return Decision{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.last
}

// Load reads the instructions of the selected skills.
//
// Only the selected skills are read, and only for the request that needs
// them. A skill that cannot be read is skipped: the turn continues
// without it rather than failing.
func (s *Selector) Load(ctx context.Context, names []string) ([]Skill, error) {
	if s == nil || s.store == nil || len(names) == 0 {
		return nil, nil
	}
	out := make([]Skill, 0, len(names))
	var firstErr error
	for _, name := range names {
		skill, err := s.store.Read(ctx, name)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if skill.Body == "" {
			continue
		}
		out = append(out, skill)
	}
	if len(out) == 0 && firstErr != nil {
		return nil, firstErr
	}
	return out, firstErr
}
