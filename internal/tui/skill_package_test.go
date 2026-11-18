package tui

import (
	"strings"
	"testing"

	"github.com/flowbyte-com/HAL9001/internal/session"
	"github.com/flowbyte-com/HAL9001/internal/skills"
)

// The diagnostics keep package provenance readable, and never claim
// more of the package was used than the entrypoint projection.

// got returns one fact's value, or "" when it is absent.
func got(facts []skillFact, label string) string { return factsValue(facts, label) }

func factsValue(facts []skillFact, label string) string {
	for _, f := range facts {
		if f.Label == label {
			return f.Value
		}
	}
	return ""
}

// A packaged skill's facts name the package, the entrypoint and exactly
// what was loaded, with the digests kept separate.
func TestSkillFactsShowPackageProvenance(t *testing.T) {
	loaded := []skills.Skill{{
		ID: "skill:review-pack-v2.0.0", Name: "review-pack", Version: "2.0.0",
		Body: "entry",
		Package: &skills.PackageRef{
			SkillID:          "skill:review-pack-v2.0.0",
			Entrypoint:       "SKILL.md",
			EntrypointSHA256: "sha256:entrypoint",
			PackageHash:      "sha256:package",
			Files:            []skills.PackageFile{{Path: "checklists/go.md"}},
			Complete:         true,
		},
	}}
	facts := skillFacts(skills.Decision{
		Skills: []string{"review-pack"}, Source: skills.SourceExplicit, Loaded: loaded,
	}, &session.Attribution{Skills: []session.AttributedSkill{{
		Name: "review-pack", Version: "2.0.0", ContentHash: "sha256:attribution",
		PackageID: "skill:review-pack-v2.0.0", Entrypoint: "SKILL.md",
		EntrypointSHA256: "sha256:entrypoint", PackageHash: "sha256:package",
	}}})

	if got := factsValue(facts, "package"); got != "skill:review-pack-v2.0.0" {
		t.Errorf("package = %q", got)
	}
	if got := factsValue(facts, "entrypoint"); got != "SKILL.md" {
		t.Errorf("entrypoint = %q", got)
	}
	loadedText := factsValue(facts, "loaded")
	if !strings.Contains(loadedText, "entrypoint only") {
		t.Errorf("loaded = %q, want entrypoint only", loadedText)
	}
	if strings.Contains(loadedText, "package loaded") {
		t.Errorf("loaded claims the whole package: %q", loadedText)
	}
	if got := factsValue(facts, "entrypoint hash"); got != "sha256:entrypoint" {
		t.Errorf("entrypoint hash = %q", got)
	}
	if got := factsValue(facts, "package hash"); got != "sha256:package" {
		t.Errorf("package hash = %q", got)
	}
	// HAL's attribution hash is its own fact: it covers the body and
	// hard constraints that were supplied, and it is not the entrypoint
	// file's digest nor the package digest.
	if got := factsValue(facts, "hash"); got != "sha256:attribution" {
		t.Errorf("attribution hash = %q", got)
	}
	for _, other := range []string{got(facts, "entrypoint hash"), got(facts, "package hash")} {
		if factsValue(facts, "hash") == other {
			t.Errorf("the attribution hash was conflated with %q", other)
		}
	}
}

// A single-document skill shows no package facts at all.
func TestSkillFactsShowNoPackageForSingleDocument(t *testing.T) {
	loaded := []skills.Skill{{
		ID: "skill:humanize-text-v1.0.1", Name: "humanize-text", Version: "1.0.1",
		Body: "text",
	}}
	facts := skillFacts(skills.Decision{
		Skills: []string{"humanize-text"}, Source: skills.SourceAuto, Loaded: loaded,
	}, nil)
	for _, label := range []string{"package", "entrypoint", "loaded", "package hash", "entrypoint hash"} {
		if got := factsValue(facts, label); got != "" {
			t.Errorf("a single-document skill shows %q = %q", label, got)
		}
	}
}

// A listing that was capped is described as incomplete: the operator
// can tell "I know every file" from "I know some".
func TestSkillFactsMarkIncompleteListing(t *testing.T) {
	loaded := []skills.Skill{{
		ID: "skill:big-v1.0.0", Name: "big", Version: "1.0.0",
		Package: &skills.PackageRef{
			SkillID: "skill:big-v1.0.0", Entrypoint: "SKILL.md", Complete: false,
		},
	}}
	facts := skillFacts(skills.Decision{Skills: []string{"big"}, Loaded: loaded}, nil)
	if got := factsValue(facts, "loaded"); !strings.Contains(got, "entrypoint only") {
		t.Errorf("loaded = %q", got)
	}
	if strings.Contains(factsValue(facts, "loaded"), "supporting files listed") {
		t.Errorf("a capped listing claimed full enumeration: %q", factsValue(facts, "loaded"))
	}
}

// Hostile store metadata cannot forge a row or move the cursor: every
// package value is flattened to one line.
func TestPackageFactsFlattenHostileValues(t *testing.T) {
	loaded := []skills.Skill{{
		Name: "pkg",
		Package: &skills.PackageRef{
			SkillID:     "skill:x\u001b[31mred",
			Entrypoint:  "SKILL.md\nforged row",
			PackageHash: "sha256:a\u0007b",
		},
	}}
	facts := skillFacts(skills.Decision{Skills: []string{"pkg"}, Loaded: loaded}, nil)
	for _, f := range facts {
		if strings.ContainsAny(f.Value, "\n\r") {
			t.Errorf("fact %q carries a line break: %q", f.Label, f.Value)
		}
	}
	if got := factsValue(facts, "entrypoint"); got != "SKILL.md forged row" {
		t.Errorf("entrypoint = %q", got)
	}
}
