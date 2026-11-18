package skills

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Package-aware skills.
//
// Two shapes must both work: a single-document skill (the shape every
// store emits today) and a packaged skill whose read response carries
// package identity alongside the entrypoint projection. Everything here
// runs against a fake MPM binary and synthetic payloads: the real store
// is never read, and the real MPM database is never opened.

// fakeMPM writes an executable that prints one fixed stdout payload,
// whatever arguments it is given. It stands in for the `mpm call`
// boundary.
//
// The payload is compacted to one line first: MPM prints a single JSON
// envelope on stdout, and HAL reads the last JSON line, exactly as the
// agent protocol does.
func fakeMPM(t *testing.T, payload string) string {
	t.Helper()
	var probe any
	if err := json.Unmarshal([]byte(payload), &probe); err != nil {
		t.Fatalf("the fixture payload is not JSON: %v", err)
	}
	compact, err := json.Marshal(probe)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "mpm")
	script := "#!/bin/sh\ncat <<'ENVELOPE'\n" + string(compact) + "\nENVELOPE\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin
}

// clientFor builds a client bound to a fake MPM that always answers
// with payload.
func clientFor(t *testing.T, payload string) *Client {
	t.Helper()
	return &Client{Bin: fakeMPM(t, payload)}
}

// singleDocumentEnvelope is the answer MPM gives for an ordinary skill:
// no package block at all.
const singleDocumentEnvelope = `{"success":true,"id":"skill:humanize-text-v1.0.1",
 "name":"humanize-text","version":"1.0.1","when_to_use":"Rewrite prose.",
 "domain":"writing","constraints":["Keep every fact."],"body":"# Humanize\n\nSoften the tone."}`

// packagedEnvelope is a read response that carries package identity,
// using the vocabulary of MPM's staged package schema.
const packagedEnvelope = `{"success":true,"id":"skill:review-pack-v2.0.0",
 "name":"review-pack","version":"2.0.0","when_to_use":"Reviewing a diff.",
 "domain":"code","constraints":["Never approve untested code."],
 "body":"# Review pack\n\nRead the entrypoint, then the checklist.",
 "package":{"skill_id":"skill:review-pack-v2.0.0","entrypoint":"SKILL.md",
   "entrypoint_sha256":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
   "package_hash":"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
   "files":[{"path":"checklists/go.md","sha256":"sha256:cccc","size_bytes":2048,"role":"reference"},
            {"path":"scripts/lint.sh","sha256":"sha256:dddd","size_bytes":512,"role":"script"}]}}`

// A single-document skill keeps working exactly as before: no package
// identity is invented for it.
func TestReadSingleDocumentSkillHasNoPackage(t *testing.T) {
	c := clientFor(t, singleDocumentEnvelope)
	s, err := c.Read(context.Background(), "humanize-text")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if s.Package != nil {
		t.Errorf("a single-document skill was given package identity: %+v", s.Package)
	}
	if s.Body == "" || len(s.Constraints) == 0 {
		t.Errorf("the skill lost its content: %+v", s)
	}
	if !strings.HasPrefix(s.ContentHash(), "sha256:") {
		t.Errorf("content hash = %q", s.ContentHash())
	}
}

// A packaged skill keeps its identity and its entrypoint, and its
// projection hash stays a hash of the projection only.
func TestReadPackagedSkillKeepsIdentityAndEntrypoint(t *testing.T) {
	c := clientFor(t, packagedEnvelope)
	s, err := c.Read(context.Background(), "review-pack")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if s.Package == nil {
		t.Fatal("the package identity was dropped")
	}
	if s.Package.SkillID != "skill:review-pack-v2.0.0" {
		t.Errorf("package skill id = %q", s.Package.SkillID)
	}
	if s.Package.Entrypoint != "SKILL.md" {
		t.Errorf("entrypoint = %q", s.Package.Entrypoint)
	}
	if len(s.Package.Files) != 2 {
		t.Fatalf("supporting files = %d, want 2", len(s.Package.Files))
	}
	if s.Package.Files[1].Role != "script" || s.Package.Files[1].Size != 512 {
		t.Errorf("file metadata lost: %+v", s.Package.Files[1])
	}
	if !s.Package.Complete {
		t.Error("a fully enumerated listing was marked incomplete")
	}
	// The three digests mean different things and must stay distinct.
	if s.Package.EntrypointSHA256 == s.Package.PackageHash {
		t.Error("entrypoint and package digests were conflated")
	}
	if s.ContentHash() == s.Package.EntrypointSHA256 {
		t.Error("the projection hash was reported as the entrypoint digest")
	}
	if s.ContentHash() == s.Package.PackageHash {
		t.Error("the projection hash was reported as the package digest")
	}
}

// A package block that names nothing is not package identity: absence
// must not be mistaken for a package.
func TestEmptyPackageBlockIsAbsent(t *testing.T) {
	payload := strings.Replace(singleDocumentEnvelope, `"body":"# Humanize\n\nSoften the tone."`,
		`"body":"# Humanize\n\nSoften the tone.","package":{}`, 1)
	c := clientFor(t, payload)
	s, err := c.Read(context.Background(), "humanize-text")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if s.Package != nil {
		t.Errorf("an empty package block became identity: %+v", s.Package)
	}
}

// A store that does not enumerate supporting files is reported as
// incomplete rather than as an empty package.
func TestPackageWithoutFileListingIsIncomplete(t *testing.T) {
	payload := `{"success":true,"id":"skill:p-v1.0.0","name":"p","version":"1.0.0",
	  "body":"entry","package":{"skill_id":"skill:p-v1.0.0","entrypoint":"SKILL.md"}}`
	c := clientFor(t, payload)
	s, err := c.Read(context.Background(), "p")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if s.Package == nil {
		t.Fatal("the package was dropped")
	}
	if s.Package.Complete {
		t.Error("an unenumerated package was reported complete")
	}
	if len(s.Package.Files) != 0 {
		t.Errorf("files were invented: %+v", s.Package.Files)
	}
}

// A long file listing is capped, and the cap is visible rather than
// silent.
func TestPackageFileListingIsCapped(t *testing.T) {
	var files []string
	for i := 0; i < MaxPackageFilesParsed+5; i++ {
		files = append(files, fmt.Sprintf(`{"path":"f%03d.md","sha256":"sha256:x","size_bytes":1}`, i))
	}
	payload := `{"success":true,"id":"skill:big-v1.0.0","name":"big","version":"1.0.0","body":"e",
	  "package":{"skill_id":"skill:big-v1.0.0","entrypoint":"SKILL.md","files":[` +
		strings.Join(files, ",") + `]}}`
	c := clientFor(t, payload)
	s, err := c.Read(context.Background(), "big")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(s.Package.Files) != MaxPackageFilesParsed {
		t.Errorf("listed %d files, want the cap %d", len(s.Package.Files), MaxPackageFilesParsed)
	}
	if s.Package.Complete {
		t.Error("a capped listing was reported complete")
	}
}

// A package read is not available from the production client: the P8
// operations do not exist yet, and HAL says so instead of guessing.
func TestClientDoesNotImplementPackageSource(t *testing.T) {
	var c any = &Client{}
	if _, ok := c.(PackageSource); ok {
		t.Error("the client claims package support before P8 exists")
	}
}

// The read path still fails cleanly when the store answers with an
// error, and no package identity leaks out of a failed read.
func TestPackagedReadFailureIsClean(t *testing.T) {
	c := clientFor(t, `{"success":false,"error":"skill not found"}`)
	if _, err := c.Read(context.Background(), "missing"); err == nil {
		t.Fatal("a failed read returned no error")
	}
}

// One supporting file is assembled from bounded chunks.
func TestReadBoundedAssemblesChunks(t *testing.T) {
	content := []byte(strings.Repeat("abc", 1000))
	src := &chunkSource{content: content, chunk: 64}
	got, err := ReadBounded(context.Background(), src, "skill:p-v1.0.0", "docs/a.md", 0)
	if err != nil {
		t.Fatalf("ReadBounded: %v", err)
	}
	if string(got) != string(content) {
		t.Errorf("assembled %d bytes, want %d", len(got), len(content))
	}
	if src.reads < 2 {
		t.Errorf("the file was read in %d call(s); the read was not chunked", src.reads)
	}
}

// A file over the budget is refused, never silently truncated.
func TestReadBoundedRefusesOversize(t *testing.T) {
	src := &chunkSource{content: make([]byte, MaxPackageReadTotal+10), chunk: 1024}
	if _, err := ReadBounded(context.Background(), src, "s", "big.md", 0); err == nil {
		t.Fatal("an oversize package file was assembled")
	}
	// A caller may narrow the budget further.
	src = &chunkSource{content: make([]byte, 4096), chunk: 512}
	if _, err := ReadBounded(context.Background(), src, "s", "mid.md", 1024); err == nil {
		t.Fatal("the caller's narrower budget was ignored")
	}
}

// Without a package source, the read is explicitly unsupported.
func TestReadBoundedWithoutSourceIsUnsupported(t *testing.T) {
	_, err := ReadBounded(context.Background(), nil, "s", "a.md", 0)
	if !errors.Is(err, ErrPackageUnsupported) {
		t.Fatalf("error = %v, want ErrPackageUnsupported", err)
	}
}

// Digest verification accepts a matching package and rejects every way
// it can disagree.
func TestVerifyPackageDigest(t *testing.T) {
	got := []PackageFile{
		{Path: "SKILL.md", SHA256: "a"},
		{Path: "docs/b.md", SHA256: "b"},
	}
	want := PackageDigest{Files: map[string]string{"SKILL.md": "a", "docs/b.md": "b"}}
	if err := VerifyPackageDigest(got, want); err != nil {
		t.Errorf("a matching package was refused: %v", err)
	}
	tampered := []PackageFile{{Path: "SKILL.md", SHA256: "z"}, {Path: "docs/b.md", SHA256: "b"}}
	if err := VerifyPackageDigest(tampered, want); err == nil {
		t.Error("a tampered file was accepted")
	}
	extra := append(append([]PackageFile(nil), got...), PackageFile{Path: "run.sh", SHA256: "c"})
	if err := VerifyPackageDigest(extra, want); err == nil {
		t.Error("an unexpected file was accepted")
	}
	if err := VerifyPackageDigest(got[:1], want); err == nil {
		t.Error("a missing file was accepted")
	}
	if err := VerifyPackageDigest(got, PackageDigest{}); err == nil {
		t.Error("verification against nothing was accepted")
	}
}

// chunkSource is a package source double that serves one file in
// bounded chunks and counts the reads.
type chunkSource struct {
	content []byte
	chunk   int
	reads   int
}

func (c *chunkSource) ListFiles(context.Context, string) ([]PackageFile, error) {
	return []PackageFile{{Path: "a.md", Size: int64(len(c.content))}}, nil
}

func (c *chunkSource) ReadChunk(_ context.Context, _, _ string, offset, limit int64) ([]byte, int64, error) {
	c.reads++
	total := int64(len(c.content))
	if offset >= total {
		return nil, total, nil
	}
	end := offset + limit
	if limit <= 0 || end > total {
		end = total
	}
	if c.chunk > 0 && int64(c.chunk) < end-offset {
		end = offset + int64(c.chunk)
	}
	return c.content[offset:end], total, nil
}

func (c *chunkSource) Verify(context.Context, string, PackageDigest) error { return nil }

// The fake binary must be the thing answering: this guards the test
// helper itself.
func TestFakeMPMIsUsed(t *testing.T) {
	c := clientFor(t, `{"success":true,"id":"x","name":"x","version":"1","body":"b"}`)
	if c.Bin == "" {
		t.Fatal("the fake binary was not wired")
	}
	if _, err := c.Read(context.Background(), "x"); err != nil {
		t.Fatalf("fake MPM read: %v", err)
	}
	var probe map[string]any
	if err := json.Unmarshal([]byte(`{"ok":true}`), &probe); err != nil {
		t.Fatal(err)
	}
}
