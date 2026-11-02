package cliui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
)

func withBrand(t *testing.T, b Brand) {
	t.Helper()
	previous := CurrentBrand()
	SetBrand(b)
	t.Cleanup(func() { SetBrand(previous) })
}

func TestBrandForPersona(t *testing.T) {
	hal := BrandForPersona("hal9001")
	if hal.Name != "HAL9001" {
		t.Errorf("hal9001 persona must select the HAL brand, got %q", hal.Name)
	}
	if hal.UserLabel != "You > " {
		t.Errorf("HAL user label = %q, want %q", hal.UserLabel, "You > ")
	}
	if hal.AgentLabel != "HAL > " {
		t.Errorf("HAL agent label = %q, want %q", hal.AgentLabel, "HAL > ")
	}
	if strings.Contains(hal.UserLabel+hal.AgentLabel, "🦞") {
		t.Error("HAL labels must not contain the lobster emoji")
	}
	if !hal.Compact {
		t.Error("HAL brand must render a compact heading")
	}

	for _, persona := range []string{"", "picoclaw", "hal", "hal9002", "unknown"} {
		b := BrandForPersona(persona)
		if b.Name != "PicoClaw" {
			t.Errorf("persona %q must keep the PicoClaw brand, got %q", persona, b.Name)
		}
		if !strings.Contains(b.UserLabel, "🦞") {
			t.Errorf("PicoClaw user label must keep the logo, got %q", b.UserLabel)
		}
		if b.Compact {
			t.Errorf("persona %q must keep the ASCII banner", persona)
		}
	}
}

func TestRenderStartupBannerHAL(t *testing.T) {
	withBrand(t, Hal9001Brand())
	colored := RenderStartupBanner(true)
	if !strings.Contains(colored, "HAL9001") {
		t.Errorf("HAL banner must name the brand:\n%q", colored)
	}
	if strings.Contains(colored, "🦞") {
		t.Errorf("HAL banner must not contain the lobster emoji:\n%q", colored)
	}
	if strings.Contains(colored, "█") {
		t.Errorf("HAL banner must not contain the ASCII logo:\n%q", colored)
	}
	if !strings.Contains(colored, colorRed) {
		t.Errorf("HAL banner should use the red accent for the eye:\n%q", colored)
	}
	plain := RenderStartupBanner(false)
	if strings.Contains(plain, "\033[") {
		t.Errorf("no-color banner must not contain ANSI escapes:\n%q", plain)
	}
	if !strings.Contains(plain, "HAL9001") {
		t.Errorf("no-color banner must name the brand:\n%q", plain)
	}
	// Compact: a single heading line, no slogans or decorative borders.
	lines := 0
	for _, line := range strings.Split(strings.TrimSpace(plain), "\n") {
		if strings.TrimSpace(line) != "" {
			lines++
		}
	}
	if lines != 1 {
		t.Errorf("HAL startup output must be one line, got %d:\n%q", lines, plain)
	}
}

func TestRenderStartupBannerPicoClawUnchanged(t *testing.T) {
	withBrand(t, PicoClawBrand())
	colored := RenderStartupBanner(true)
	plain := RenderStartupBanner(false)
	if !strings.Contains(colored, "█") {
		t.Errorf("PicoClaw banner must keep the ASCII logo:\n%q", colored)
	}
	if !strings.Contains(plain, "█") {
		t.Errorf("plain PicoClaw banner must keep the ASCII logo:\n%q", plain)
	}
	if strings.Contains(plain, "\033[") {
		t.Errorf("plain banner must not contain ANSI escapes")
	}
	if strings.Contains(colored, "\033[") == false {
		t.Errorf("colored banner must contain ANSI escapes")
	}
}

func TestDetectBrandFromEnv(t *testing.T) {
	t.Setenv(config.EnvPersona, "hal9001")
	t.Setenv(config.EnvConfig, filepath.Join(t.TempDir(), "missing.json"))
	if b := DetectBrand(); b.Name != "HAL9001" {
		t.Errorf("env persona must select HAL branding, got %q", b.Name)
	}
}

func TestDetectBrandFromEnvOverridesConfigFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	writeConfigWithPersona(t, path, "picoclaw")
	t.Setenv(config.EnvConfig, path)
	t.Setenv(config.EnvPersona, "hal9001")
	if b := DetectBrand(); b.Name != "HAL9001" {
		t.Errorf("env persona must win over the config file, got %q", b.Name)
	}
}

func TestDetectBrandFromConfigFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	writeConfigWithPersona(t, path, "hal9001")
	t.Setenv(config.EnvConfig, path)
	t.Setenv(config.EnvPersona, "")
	if b := DetectBrand(); b.Name != "HAL9001" {
		t.Errorf("config persona must select HAL branding, got %q", b.Name)
	}
}

func TestDetectBrandDefaultWhenUnset(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(config.EnvConfig, filepath.Join(dir, "missing.json"))
	t.Setenv(config.EnvPersona, "")
	if b := DetectBrand(); b.Name != "PicoClaw" {
		t.Errorf("unset persona must keep PicoClaw branding, got %q", b.Name)
	}
}

func TestDetectBrandIgnoresMalformedConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(config.EnvConfig, path)
	t.Setenv(config.EnvPersona, "")
	if b := DetectBrand(); b.Name != "PicoClaw" {
		t.Errorf("malformed config must fall back to PicoClaw branding, got %q", b.Name)
	}
}

func writeConfigWithPersona(t *testing.T, path, persona string) {
	t.Helper()
	content := `{"version":3,"agents":{"defaults":{"persona":"` + persona + `"}}}`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
