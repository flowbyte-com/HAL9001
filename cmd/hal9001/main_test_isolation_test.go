package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// TestMain keeps every test in this package away from the operator's own
// installation.
//
// The configuration file, the home directory and the workspace all point
// into a temporary directory, so no test can read or write the real
// configuration, and the startup migration can never touch it.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "hal9001-cmd-test-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "cannot create the test directory:", err)
		os.Exit(1)
	}
	defer os.RemoveAll(dir)
	os.Setenv("HAL9001_CONFIG", filepath.Join(dir, "config.json"))
	os.Setenv("HAL9001_HOME", filepath.Join(dir, "home"))
	os.Setenv("HAL9001_WORKSPACE", filepath.Join(dir, "workspace"))
	os.Exit(m.Run())
}
