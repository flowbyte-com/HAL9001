//go:build linux

package isolation

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAppendLinuxProtectedMountRules_MasksDirectoriesAndBindsFiles(t *testing.T) {
	resetProtectedForTest(t)
	root := t.TempDir()
	protectedDir := filepath.Join(root, "mpm-workspace")
	if err := os.MkdirAll(protectedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	protectedFile := filepath.Join(root, "mpm")
	if err := os.WriteFile(protectedFile, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := ProtectPaths(protectedDir, protectedFile); err != nil {
		t.Fatalf("ProtectPaths: %v", err)
	}

	plan := []MountRule{{Source: root, Target: root, Mode: "rw"}}
	plan = appendLinuxProtectedMountRules(plan)

	maskIndex := -1
	for i, rule := range plan {
		if rule.Target == protectedDir {
			if rule.Mode != "mask" {
				t.Fatalf("directory mount mode = %q, want mask", rule.Mode)
			}
			maskIndex = i
		}
	}
	if maskIndex < 0 {
		t.Fatalf("protected directory was not masked: %#v", plan)
	}
	if maskIndex != len(plan)-2 {
		t.Fatalf("protected mask must be appended after the rw root: %#v", plan)
	}
	assertMountRule(t, plan, protectedFile, "ro")
}

func TestAppendLinuxProtectedMountRules_SkipsSymlinkDestination(t *testing.T) {
	resetProtectedForTest(t)
	root := t.TempDir()
	target := filepath.Join(root, "real-store")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "linked-store")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	if err := ProtectPaths(link); err != nil {
		t.Fatalf("ProtectPaths: %v", err)
	}

	plan := appendLinuxProtectedMountRules(nil)
	assertNoMountRule(t, plan, link)
	assertMountRule(t, plan, target, "mask")
}

func TestBuildLinuxBwrapArgs_UsesTmpfsAndPrivatePIDNamespace(t *testing.T) {
	root := t.TempDir()
	protectedDir := filepath.Join(root, "store")
	if err := os.MkdirAll(protectedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	plan := []MountRule{
		{Source: root, Target: root, Mode: "rw"},
		{Target: protectedDir, Mode: "mask"},
	}
	args, err := buildLinuxBwrapArgs("true", "true", []string{"true"}, root, plan, nil)
	if err != nil {
		t.Fatalf("buildLinuxBwrapArgs: %v", err)
	}
	if !contains(args, "--unshare-pid") {
		t.Fatalf("bwrap args missing --unshare-pid: %v", args)
	}
	hasTmpfs := false
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "--tmpfs" && args[i+1] == protectedDir {
			hasTmpfs = true
		}
	}
	if !hasTmpfs {
		t.Fatalf("bwrap args missing --tmpfs mask for %q: %v", protectedDir, args)
	}
}
