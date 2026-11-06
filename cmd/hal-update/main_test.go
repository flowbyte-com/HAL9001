package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestVersionCommand(t *testing.T) {
	var out, errOut bytes.Buffer
	if err := run([]string{"--version"}, &out, &errOut); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	if !strings.Contains(text, "HAL9001 updater") || !strings.Contains(text, "commit:") {
		t.Fatalf("output = %q", text)
	}
}

func TestStatusCommandIsReadOnly(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "root")
	bin := filepath.Join(base, "bin")
	t.Setenv("HAL9001_INSTALL_ROOT", root)
	t.Setenv("HAL9001_LOCAL_BIN", bin)
	var out, errOut bytes.Buffer
	if err := run([]string{"--status"}, &out, &errOut); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "not installed") {
		t.Fatalf("output = %q", out.String())
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("status created the root: %v", err)
	}
}

func TestOneActionAtATime(t *testing.T) {
	var out, errOut bytes.Buffer
	err := run([]string{"--status", "--check"}, &out, &errOut)
	if err == nil || !strings.Contains(err.Error(), "choose one") {
		t.Fatalf("error = %v", err)
	}
}

func TestBootstrapOnlyFlags(t *testing.T) {
	var out, errOut bytes.Buffer
	if err := run([]string{"--from", "."}, &out, &errOut); err == nil {
		t.Fatal("--from without --bootstrap must fail")
	}
	if err := run([]string{"--replace-existing"}, &out, &errOut); err == nil {
		t.Fatal("--replace-existing without --bootstrap must fail")
	}
}

func TestHelpAndBadFlag(t *testing.T) {
	var out, errOut bytes.Buffer
	if err := run([]string{"--help"}, &out, &errOut); err != nil {
		t.Fatalf("--help = %v", err)
	}
	if !strings.Contains(errOut.String(), "hal-update") {
		t.Fatalf("help output = %q", errOut.String())
	}
	errOut.Reset()
	if err := run([]string{"--no-such-flag"}, &out, &errOut); err == nil {
		t.Fatal("a bad flag must fail")
	}
}

func TestUnexpectedArgument(t *testing.T) {
	var out, errOut bytes.Buffer
	if err := run([]string{"--status", "extra"}, &out, &errOut); err == nil {
		t.Fatal("an unexpected positional argument must fail")
	}
}
