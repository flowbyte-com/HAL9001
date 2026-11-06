package install

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// ManifestSchema identifies the manifest contract.
const ManifestSchema = "hal-release/1"

var hashPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// Binary records one installed executable.
type Binary struct {
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

// Manifest is the release's identity. It is readable without Git and
// without executing the release, so status and rollback validation do
// not need a source checkout.
type Manifest struct {
	Schema      string   `json:"schema"`
	Product     string   `json:"product"`
	Layout      string   `json:"layout"`
	Revision    string   `json:"revision"`
	Commit      string   `json:"commit"`
	ShortCommit string   `json:"short_commit"`
	Version     string   `json:"version"`
	BuildEpoch  int64    `json:"build_epoch"`
	BuiltAt     string   `json:"built_at"`
	Source      string   `json:"source"`
	Binaries    []Binary `json:"binaries"`
}

// BuildMeta is the immutable source identity a release is built from.
type BuildMeta struct {
	Commit  string
	Version string
	Epoch   int64
	Source  string
}

// NewManifest hashes the two binaries and returns the manifest for a
// complete release directory.
func NewManifest(releaseDir string, meta BuildMeta) (Manifest, error) {
	if !ValidRevision(meta.Commit) {
		return Manifest{}, fmt.Errorf("build commit %q is not a full commit SHA", meta.Commit)
	}
	if strings.TrimSpace(meta.Version) == "" {
		return Manifest{}, fmt.Errorf("build version is empty")
	}
	m := Manifest{
		Schema:      ManifestSchema,
		Product:     Product,
		Layout:      LayoutVersion,
		Revision:    meta.Commit,
		Commit:      meta.Commit,
		ShortCommit: meta.Commit[:8],
		Version:     meta.Version,
		BuildEpoch:  meta.Epoch,
		Source:      meta.Source,
	}
	if meta.Epoch > 0 {
		m.BuiltAt = time.Unix(meta.Epoch, 0).UTC().Format(time.RFC3339)
	} else {
		m.BuiltAt = "unknown"
	}
	for _, name := range []string{HalName, UpdaterName} {
		path := filepath.Join(releaseDir, "bin", name)
		hash, size, err := HashFile(path)
		if err != nil {
			return Manifest{}, err
		}
		m.Binaries = append(m.Binaries, Binary{Name: name, SHA256: hash, Size: size})
	}
	return m, nil
}

// HashFile returns the SHA-256 and size of a regular file.
func HashFile(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// WriteManifest writes manifest.json atomically inside a candidate.
func WriteManifest(releaseDir string, m Manifest) error {
	if m.Schema == "" {
		m.Schema = ManifestSchema
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp, err := os.CreateTemp(releaseDir, ".manifest-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, filepath.Join(releaseDir, "manifest.json"))
}

// ReadManifest parses manifest.json with unknown fields rejected. A
// future manifest is a refusal, not a silent downgrade.
func ReadManifest(releaseDir string) (Manifest, error) {
	var m Manifest
	path := filepath.Join(releaseDir, "manifest.json")
	f, err := os.Open(path)
	if err != nil {
		return m, fmt.Errorf("release has no readable manifest.json: %w", err)
	}
	defer f.Close()
	dec := json.NewDecoder(f)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		return m, fmt.Errorf("release manifest is not valid JSON: %w", err)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return m, fmt.Errorf("release manifest has trailing data")
	}
	return m, nil
}

// ValidateFields checks the manifest against the release contract. It
// does not touch the filesystem beyond the expected revision.
func (m Manifest) ValidateFields(expectedRev string) error {
	if m.Schema != ManifestSchema {
		return fmt.Errorf("manifest schema %q is not %q", m.Schema, ManifestSchema)
	}
	if m.Product != Product {
		return fmt.Errorf("manifest product %q is not %q", m.Product, Product)
	}
	if m.Layout != LayoutVersion {
		return fmt.Errorf("manifest layout %q is not %q", m.Layout, LayoutVersion)
	}
	if !ValidRevision(m.Commit) {
		return fmt.Errorf("manifest commit %q is not a full commit SHA", m.Commit)
	}
	if m.Revision != m.Commit {
		return fmt.Errorf("manifest revision %q does not match commit %q", m.Revision, m.Commit)
	}
	if expectedRev != "" && m.Revision != expectedRev {
		return fmt.Errorf("manifest revision %q does not match release %q", m.Revision, expectedRev)
	}
	if m.ShortCommit != m.Commit[:8] {
		return fmt.Errorf("manifest short commit %q does not match %q", m.ShortCommit, m.Commit[:8])
	}
	if strings.TrimSpace(m.Version) == "" {
		return fmt.Errorf("manifest has no version")
	}
	if strings.TrimSpace(m.Source) == "" {
		return fmt.Errorf("manifest has no source")
	}
	if m.BuildEpoch < 0 {
		return fmt.Errorf("manifest build epoch is negative")
	}
	if m.BuiltAt != "unknown" {
		if _, err := time.Parse(time.RFC3339, m.BuiltAt); err != nil {
			return fmt.Errorf("manifest built_at %q is not RFC3339", m.BuiltAt)
		}
	}
	if len(m.Binaries) != 2 {
		return fmt.Errorf("manifest lists %d binaries; exactly %d are required", len(m.Binaries), 2)
	}
	seen := map[string]bool{}
	for _, b := range m.Binaries {
		if b.Name != HalName && b.Name != UpdaterName {
			return fmt.Errorf("manifest names unexpected binary %q", b.Name)
		}
		if seen[b.Name] {
			return fmt.Errorf("manifest names binary %q twice", b.Name)
		}
		seen[b.Name] = true
		if !hashPattern.MatchString(b.SHA256) {
			return fmt.Errorf("manifest hash for %s is not a SHA-256", b.Name)
		}
		if b.Size <= 0 {
			return fmt.Errorf("manifest size for %s is not positive", b.Name)
		}
	}
	return nil
}

// ValidateRelease proves a release directory before it is activated.
//
// The checks are deliberately fail-closed: the directory is a real
// directory, the manifest is strict, the binary names and hashes match,
// no state or Git metadata is present, and both binaries report the
// commit the manifest names. That last check catches a release built
// from two different revisions.
func ValidateRelease(releaseDir, expectedRev string) (Manifest, error) {
	var m Manifest
	fi, err := os.Lstat(releaseDir)
	if err != nil {
		return m, fmt.Errorf("release directory does not exist: %w", err)
	}
	if !fi.IsDir() || fi.Mode()&os.ModeSymlink != 0 {
		return m, fmt.Errorf("release %s is not a real directory", releaseDir)
	}
	manifestPath := filepath.Join(releaseDir, "manifest.json")
	if fi, err := os.Lstat(manifestPath); err != nil || !fi.Mode().IsRegular() || fi.Mode()&os.ModeSymlink != 0 {
		return m, fmt.Errorf("release manifest.json is not a regular file")
	}
	m, err = ReadManifest(releaseDir)
	if err != nil {
		return m, err
	}
	if err := m.ValidateFields(expectedRev); err != nil {
		return m, err
	}
	if err := validateReleaseTree(releaseDir); err != nil {
		return m, err
	}
	for _, b := range m.Binaries {
		path := filepath.Join(releaseDir, "bin", b.Name)
		info, err := os.Lstat(path)
		if err != nil {
			return m, fmt.Errorf("release is missing bin/%s: %w", b.Name, err)
		}
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return m, fmt.Errorf("release bin/%s is not a regular file", b.Name)
		}
		if info.Mode().Perm()&0o111 == 0 {
			return m, fmt.Errorf("release bin/%s is not executable", b.Name)
		}
		if info.Size() != b.Size {
			return m, fmt.Errorf("release bin/%s size changed (manifest %d, file %d)", b.Name, b.Size, info.Size())
		}
		hash, _, err := HashFile(path)
		if err != nil {
			return m, fmt.Errorf("cannot hash release bin/%s: %w", b.Name, err)
		}
		if hash != b.SHA256 {
			return m, fmt.Errorf("release bin/%s does not match its manifest hash", b.Name)
		}
		reported, err := reportedCommit(path)
		if err != nil {
			return m, fmt.Errorf("release bin/%s cannot report its version: %w", b.Name, err)
		}
		if reported != m.Commit {
			return m, fmt.Errorf("release bin/%s reports commit %q but the manifest says %q (mixed build)", b.Name, reported, m.Commit)
		}
	}
	return m, nil
}

func validateReleaseTree(releaseDir string) error {
	entries, err := os.ReadDir(releaseDir)
	if err != nil {
		return fmt.Errorf("cannot read release directory: %w", err)
	}
	for _, entry := range entries {
		if entry.Name() != "bin" && entry.Name() != "manifest.json" {
			return fmt.Errorf("release carries unexpected entry %q", entry.Name())
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("release entry %q is a symlink", entry.Name())
		}
	}
	binEntries, err := os.ReadDir(filepath.Join(releaseDir, "bin"))
	if err != nil {
		return fmt.Errorf("release has no readable bin directory: %w", err)
	}
	var names []string
	for _, entry := range binEntries {
		name := entry.Name()
		if name != HalName && name != UpdaterName {
			return fmt.Errorf("release bin directory carries unexpected entry %q", name)
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("release bin/%s is a symlink", name)
		}
		names = append(names, name)
	}
	sort.Strings(names)
	want := []string{HalName, UpdaterName}
	sort.Strings(want)
	if strings.Join(names, ",") != strings.Join(want, ",") {
		return fmt.Errorf("release bin directory holds %v; want %v", names, want)
	}
	gitFound := false
	err = filepath.Walk(releaseDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.Name() == ".git" {
			gitFound = true
			return filepath.SkipAll
		}
		return nil
	})
	if err != nil {
		return err
	}
	if gitFound {
		return fmt.Errorf("release carries Git metadata")
	}
	return nil
}

func reportedCommit(binary string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "--version")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "commit:") {
			commit := strings.TrimSpace(strings.TrimPrefix(line, "commit:"))
			if !ValidRevision(commit) {
				return "", fmt.Errorf("reported commit %q is not a full commit SHA", commit)
			}
			return commit, nil
		}
	}
	return "", fmt.Errorf("output has no commit line")
}
