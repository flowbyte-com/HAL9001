package isolation

import (
	"os/exec"
	"runtime"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
)

// BubblewrapEnforced gates the Pi-like lexical-guard bypass: it must be
// true exactly when child processes run under verified OS confinement, and
// false everywhere else (other platforms, disabled or unverifiable
// isolation, default configurations).

func pinDefaultIsolation(t *testing.T) {
	t.Helper()
	ResetProtectedPaths()
	Configure(config.DefaultConfig())
	t.Cleanup(func() {
		ResetProtectedPaths()
		Configure(config.DefaultConfig())
	})
}

func TestBubblewrapEnforcedDefaultsOff(t *testing.T) {
	pinDefaultIsolation(t)
	if BubblewrapEnforced() {
		t.Fatal("enforced with default configuration")
	}
}

func TestBubblewrapEnforcedRequiresEnabled(t *testing.T) {
	pinDefaultIsolation(t)
	cfg := config.DefaultConfig()
	cfg.Isolation.Enabled = false
	Configure(cfg)
	if BubblewrapEnforced() {
		t.Fatal("enforced with isolation disabled")
	}
}

func TestBubblewrapEnforcedRequiresCleanPreflight(t *testing.T) {
	pinDefaultIsolation(t)
	cfg := config.DefaultConfig()
	cfg.Isolation.Enabled = true
	// An invalid expose rule fails preflight on every platform, so the
	// guard must stay on without depending on bubblewrap presence.
	cfg.Isolation.ExposePaths = []config.ExposePath{{Source: "\x00invalid", Target: "/x"}}
	Configure(cfg)
	if err := Preflight(); err == nil {
		t.Skip("platform accepts the invalid expose path; cannot test unclean preflight")
	}
	if BubblewrapEnforced() {
		t.Fatal("enforced with a failing preflight")
	}
}

func TestBubblewrapEnforcedWhenVerified(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("bubblewrap backend is Linux-only")
	}
	if _, err := exec.LookPath("bwrap"); err != nil {
		t.Skip("bubblewrap not available")
	}
	pinDefaultIsolation(t)
	t.Setenv("PICOCLAW_HOME", t.TempDir())
	cfg := config.DefaultConfig()
	cfg.Isolation.Enabled = true
	Configure(cfg)
	if err := Preflight(); err != nil {
		t.Fatalf("preflight: %v", err)
	}
	if !BubblewrapEnforced() {
		t.Fatal("not enforced with enabled, clean, bubblewrap-backed isolation")
	}
	// Resetting the configuration must immediately drop the bypass.
	Configure(config.DefaultConfig())
	if BubblewrapEnforced() {
		t.Fatal("still enforced after reset to defaults")
	}
}
