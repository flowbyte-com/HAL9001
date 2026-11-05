package tui

import (
	"strings"
	"testing"

	"github.com/flowbyte-com/HAL9001/internal/engine"
	"github.com/flowbyte-com/HAL9001/internal/permission"
)

func TestFirecrawlToolLinesUseTheCompactForm(t *testing.T) {
	search := engine.ToolActivity{
		Name: "firecrawl_search", Status: "ok", Summary: "5 results", Mode: permission.Guarded,
	}
	if got, want := toolActivityLine(search, 80),
		"⚙ firecrawl_search · completed · 5 results · 444"; got != want {
		t.Errorf("search line = %q, want %q", got, want)
	}
	scrape := engine.ToolActivity{
		Name: "firecrawl_scrape", Status: "ok", Bytes: 12 * 1024,
		Target: "https://example.com/article", Mode: permission.Guarded,
	}
	got := toolActivityLine(scrape, 80)
	for _, want := range []string{"⚙ firecrawl_scrape", "completed", "12.0 KiB", "444"} {
		if !strings.Contains(got, want) {
			t.Errorf("scrape line %q is missing %q", got, want)
		}
	}
	rate := engine.ToolActivity{
		Name: "firecrawl_search", Status: "failed", Summary: "rate limited", Mode: permission.Guarded,
	}
	if got, want := toolActivityLine(rate, 80),
		"⚙ firecrawl_search · failed · rate limited · 444"; got != want {
		t.Errorf("rate-limit line = %q, want %q", got, want)
	}
	timeout := engine.ToolActivity{
		Name: "firecrawl_scrape", Status: "timeout", Summary: "timed out", Mode: permission.Guarded,
	}
	if got, want := toolActivityLine(timeout, 80),
		"⚙ firecrawl_scrape · timed out · 444"; got != want {
		t.Errorf("timeout line = %q, want %q", got, want)
	}
}

func TestFirecrawlIsDocumentedInHelp(t *testing.T) {
	m := New(nil)
	m.width = 120
	plain := stripANSI(strings.Join(m.helpRows(), "\n"))
	if !strings.Contains(plain, "Firecrawl web research") {
		t.Errorf("help is missing the Firecrawl capability: %s", plain)
	}
}

func TestDiagnosticsReportFirecrawlStatus(t *testing.T) {
	m := NewWithHooks(nil, Hooks{
		WebResearch: func() string {
			return "firecrawl · ready · 5 results/search · 2 scrapes/turn · 64 KiB/turn"
		},
	})
	m.width = 120
	plain := stripANSI(strings.Join(m.diagRows(), "\n"))
	if !strings.Contains(plain, "web research") ||
		!strings.Contains(plain, "firecrawl · ready") {
		t.Errorf("diagnostics do not report Firecrawl:\n%s", plain)
	}
}
