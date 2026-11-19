package tui

import (
	"testing"

	"github.com/flowbyte-com/HAL9001/internal/provider"
)

// The management screen offers the levels the model actually takes.
//
// A level HAL cannot place on the wire is a setting that can never
// apply: offering it would store a promise the provider never keeps.
// The runtime already refuses those levels, and the screen has to
// agree with it.
func TestTheManagementScreenOffersOnlyTheLevelsThatApply(t *testing.T) {
	cases := []struct {
		model string
		want  []string
	}{
		{"MiniMax-M3.1-Flash", []string{"low", "medium", "high", "xhigh", "max"}},
		{"deepseek-flash", []string{"low", "high", "max"}},
		{"claude-sonnet-4-5", []string{"low", "medium", "high", "xhigh", "max"}},
		// A model HAL documents no level for: nothing is offered.
		{"gpt-4o", nil},
		{"flash", nil},
		{"", nil},
	}
	for _, c := range cases {
		got := effortLevelsForModel(c.model)
		if len(got) != len(c.want) {
			t.Errorf("effortLevelsForModel(%q) = %v, want %v", c.model, got, c.want)
			continue
		}
		for i := range c.want {
			if got[i] != c.want[i] {
				t.Errorf("effortLevelsForModel(%q) = %v, want %v", c.model, got, c.want)
				break
			}
		}
	}
}

// The screen shows the level the provider will get.
//
// A stored level that the provider never sees is shown as the level it
// does get, so the editor and the footer agree with the wire.
func TestTheScreenShowsTheEffectiveLevel(t *testing.T) {
	cases := []struct {
		stored string
		model  string
		want   string
	}{
		{"medium", "deepseek-flash", "high"},
		{"xhigh", "deepseek-flash", "high"},
		{"low", "deepseek-flash", "low"},
		{"medium", "MiniMax-M3.1-Flash", "medium"},
		{"", "MiniMax-M3.1-Flash", "the model's default (medium)"},
		{"", "gpt-4o", "the model's default"},
	}
	for _, c := range cases {
		if got := effortDisplay(c.stored, c.model); got != c.want {
			t.Errorf("effortDisplay(%q, %q) = %q, want %q", c.stored, c.model, got, c.want)
		}
	}
}

// The levels a family documents are the ones the picker offers, and the
// provider's own table is the only source of them.
func TestTheDocumentedLevelsAreTheOfferedOnes(t *testing.T) {
	for _, model := range []string{"MiniMax-M3.1-Flash", "deepseek-flash", "claude-sonnet-4-5"} {
		offered := effortLevelsForModel(model)
		documented, ok := provider.SupportedEffortLevels(model)
		if !ok {
			t.Errorf("SupportedEffortLevels(%q) reported no levels", model)
			continue
		}
		if len(offered) != len(documented) {
			t.Errorf("the screen offers %v for %q, the table documents %v", offered, model, documented)
		}
	}
}
