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
	Name      string
	Version   string
	WhenToUse string
	Domain    string
	Global    bool
}

// Skill is one loaded skill: the instructions and the hard constraints
// that come with them.
type Skill struct {
	Name        string
	Version     string
	Domain      string
	WhenToUse   string
	Body        string
	Constraints []string
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
	Success     bool     `json:"success"`
	Error       string   `json:"error"`
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Version     string   `json:"version"`
	WhenToUse   string   `json:"when_to_use"`
	Domain      string   `json:"domain"`
	Constraints []string `json:"constraints"`
	Body        string   `json:"body"`
	IsGlobal    bool     `json:"is_global"`
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
		Name:        env.Name,
		Version:     env.Version,
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
