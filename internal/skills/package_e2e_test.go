package skills

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// End-to-end: HAL selecting and loading a skill's entrypoint through
// the real MPM command line, and through a fixture that speaks the same
// contract while carrying package identity.
//
// Both runs are isolated. The first uses a disposable MPM_WORKSPACE, so
// the operator's own store is never read or written; the second uses a
// fixture binary, so no MPM build is required.

// mpmAvailable reports whether the real command line can be used.
func mpmAvailable(t *testing.T) bool {
	t.Helper()
	if _, err := exec.LookPath("mpm"); err == nil {
		return true
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return false
	}
	for _, candidate := range []string{
		filepath.Join(home, ".mpm", "bin", "mpm"),
		filepath.Join(home, ".local", "bin", "mpm"),
	} {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return true
		}
	}
	return false
}

// writeSkillFile writes a synthetic single-document skill.
func writeSkillFile(t *testing.T, dir, name string) string {
	t.Helper()
	path := filepath.Join(dir, "SKILL.md")
	body := `---
name: ` + name + `
version: 1.0.0
description: A synthetic skill for the end-to-end demonstration.
when_to_use: "Demonstrating the client. Triggers: \"demo e2e\"."
---
# ` + name + `

Write the demonstration answer.
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestRealMPMWorkspaceSelectsAndLoads demonstrates the full path against
// a real, disposable MPM database: import a skill, discover it, route a
// request to it, and load its entrypoint.
//
// It never touches the operator's store: MPM_WORKSPACE points at a
// temporary directory for the import and for every client call.
func TestRealMPMWorkspaceSelectsAndLoads(t *testing.T) {
	if !mpmAvailable(t) {
		t.Skip("mpm is not installed")
	}
	ws := t.TempDir()
	skillsDir := filepath.Join(ws, "fixture")
	if err := os.MkdirAll(skillsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	file := writeSkillFile(t, skillsDir, "demo-e2e")

	// Import through MPM's own command line, into the disposable
	// workspace only.
	importCmd := exec.Command("mpm", "skill", "add", "--file", file)
	importCmd.Env = append(os.Environ(), "MPM_WORKSPACE="+ws)
	if out, err := importCmd.CombinedOutput(); err != nil {
		t.Skipf("this MPM cannot import into a disposable workspace: %v: %s", err, strings.TrimSpace(string(out)))
	}

	// HAL's client, pinned to the same disposable workspace.
	c := &Client{Env: append(os.Environ(), "MPM_WORKSPACE="+ws)}
	entries, err := c.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	found := false
	for _, e := range entries {
		if e.Name == "demo-e2e" {
			found = true
		}
	}
	if !found {
		t.Fatalf("the imported skill is not in the catalogue: %+v", entries)
	}

	// Route a request to it, then load the entrypoint through the
	// selector: the same path the engine uses for every turn.
	sel := NewSelector(c)
	if err := sel.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	d := sel.Route(`Write a demo e2e answer for me.`)
	if !d.Selected() || d.Skills[0] != "demo-e2e" {
		t.Fatalf("routing did not select the imported skill: %+v", d)
	}
	loaded, err := sel.Load(context.Background(), d.Skills)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(loaded) != 1 || loaded[0].Body == "" {
		t.Fatalf("the entrypoint did not load: %+v", loaded)
	}
	// A single-document skill carries no package identity, even though
	// it came from a real store.
	if loaded[0].Package != nil {
		t.Errorf("a single-document skill was given package identity: %+v", loaded[0].Package)
	}
	if loaded[0].Version != "1.0.0" {
		t.Errorf("version = %q", loaded[0].Version)
	}
}

// TestPackagedEntrypointThroughFixtureMPM demonstrates the packaged
// path end to end: discovery, routing and entrypoint loading through
// the same client and selector, against a fixture that speaks the
// documented `mpm call mpm_skills` contract and reports package
// identity.
//
// The fixture exists because no released MPM accepts a multi-file
// package yet (P8, issue #253). When P8 lands, this test should be
// re-pointed at a real import; the HAL-side path it exercises does not
// change.
func TestPackagedEntrypointThroughFixtureMPM(t *testing.T) {
	// One binary answering both list and read: the fixture inspects the
	// action in the payload, exactly as MPM does.
	dir := t.TempDir()
	bin := filepath.Join(dir, "mpm")
	script := `#!/bin/sh
case "$*" in
*'"action":"list"'*|*'"action": "list"'*)
  printf '%s\n' '{"success":true,"count":1,"scope":"all","skills":[{"ID":"skill:review-pack-v2.0.0","Name":"review-pack","Version":"2.0.0","WhenToUse":"Reviewing a diff. Triggers: \"review\", \"diff\".","IsGlobal":false,"Weight":5}]}'
  ;;
*'"action":"read"'*|*'"action": "read"'*)
  printf '%s\n' '{"success":true,"id":"skill:review-pack-v2.0.0","name":"review-pack","version":"2.0.0","when_to_use":"Reviewing a diff.","domain":"code","constraints":["Never approve untested code."],"body":"# Review pack\n\nRead the entrypoint, then the checklist.","package":{"skill_id":"skill:review-pack-v2.0.0","entrypoint":"SKILL.md","entrypoint_sha256":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","package_hash":"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","files":[{"path":"checklists/go.md","sha256":"sha256:cccc","size_bytes":2048,"role":"reference"},{"path":"scripts/lint.sh","sha256":"sha256:dddd","size_bytes":512,"role":"script"}]}}'
  ;;
*)
  printf '%s\n' '{"success":false,"error":"unknown action"}'
  ;;
esac
`
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	c := &Client{Bin: bin}
	sel := NewSelector(c)
	if err := sel.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if !sel.Catalogue().Has("review-pack") {
		t.Fatalf("the packaged skill is not discoverable: %+v", sel.Catalogue().Entries)
	}
	d := sel.Route(`Write a review of this diff.`)
	if !d.Selected() || d.Skills[0] != "review-pack" {
		t.Fatalf("routing did not select the packaged skill: %+v", d)
	}
	loaded, err := sel.Load(context.Background(), d.Skills)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(loaded) != 1 {
		t.Fatalf("loaded %d skills", len(loaded))
	}
	s := loaded[0]
	if s.Body == "" {
		t.Fatal("the entrypoint body did not load")
	}
	if s.Package == nil {
		t.Fatal("the package identity was lost")
	}
	if s.Package.Entrypoint != "SKILL.md" || len(s.Package.Files) != 2 {
		t.Errorf("package identity = %+v", s.Package)
	}
	// The listing names supporting files; their bytes were never read.
	if strings.Contains(s.Body, "checklists/go.md") || strings.Contains(s.Body, "#!/bin/sh") {
		t.Errorf("supporting-file content leaked into the entrypoint body")
	}
	// The derived projection and the reported digests stay distinct.
	if s.ContentHash() == s.Package.EntrypointSHA256 || s.ContentHash() == s.Package.PackageHash {
		t.Error("the projection hash was conflated with a reported digest")
	}
}
