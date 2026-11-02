package agent

import (
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/cmd/picoclaw/internal/cliui"
)

// The interactive CLI must use the persona branding: HAL sessions show
// "You >" / "HAL >" and no lobster emoji; other sessions keep the
// original PicoClaw labels.
func TestFormatResponseBrandLabels(t *testing.T) {
	previous := cliui.CurrentBrand()
	t.Cleanup(func() { cliui.SetBrand(previous) })

	cliui.SetBrand(cliui.Hal9001Brand())
	got := formatResponse("hello there")
	if got != "HAL > hello there" {
		t.Errorf("HAL response label = %q, want %q", got, "HAL > hello there")
	}
	if strings.Contains(got, "🦞") {
		t.Error("HAL response label must not contain the lobster emoji")
	}

	cliui.SetBrand(cliui.PicoClawBrand())
	got = formatResponse("hello there")
	if !strings.Contains(got, "🦞") {
		t.Errorf("PicoClaw response label must keep the logo, got %q", got)
	}
}

func TestBrandLabelsForPersonas(t *testing.T) {
	previous := cliui.CurrentBrand()
	t.Cleanup(func() { cliui.SetBrand(previous) })

	cliui.SetBrand(cliui.BrandForPersona("hal9001"))
	if got := cliui.CurrentBrand().UserLabel; got != "You > " {
		t.Errorf("HAL user label = %q, want %q", got, "You > ")
	}
	cliui.SetBrand(cliui.BrandForPersona(""))
	if got := cliui.CurrentBrand().UserLabel; !strings.Contains(got, "You") {
		t.Errorf("PicoClaw user label = %q, want the original label", got)
	}
}
