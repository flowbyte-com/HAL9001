package tui

import (
	"strings"
	"testing"

	"github.com/flowbyte-com/HAL9001/internal/engine"
	"github.com/flowbyte-com/HAL9001/internal/permission"
)

func TestWebToolLinesUseTheCompactForm(t *testing.T) {
	search := engine.ToolActivity{
		Name: "web_search", Status: "ok", Summary: "5 results · firecrawl", Mode: permission.Guarded,
	}
	if got, want := toolActivityLine(search, 90),
		"⚙ web_search · completed · 5 results · firecrawl · 444"; got != want {
		t.Errorf("search line = %q, want %q", got, want)
	}
	fallback := engine.ToolActivity{
		Name: "web_search", Status: "ok", Summary: "5 results · exa · fallback", Mode: permission.Guarded,
	}
	if got := toolActivityLine(fallback, 90); !strings.Contains(got, "exa · fallback") {
		t.Errorf("fallback line = %q", got)
	}
	scrape := engine.ToolActivity{
		Name: "firecrawl_scrape", Status: "ok", Bytes: 12 * 1024,
		Target: "https://example.com/article", Mode: permission.Guarded,
	}
	got := toolActivityLine(scrape, 90)
	for _, want := range []string{"⚙ firecrawl_scrape", "completed", "12.0 KiB", "444"} {
		if !strings.Contains(got, want) {
			t.Errorf("scrape line %q is missing %q", got, want)
		}
	}
	rate := engine.ToolActivity{
		Name: "web_search", Status: "failed", Summary: "all providers failed", Mode: permission.Guarded,
	}
	if got, want := toolActivityLine(rate, 90),
		"⚙ web_search · failed · all providers failed · 444"; got != want {
		t.Errorf("failure line = %q, want %q", got, want)
	}
	timeout := engine.ToolActivity{
		Name: "firecrawl_scrape", Status: "timeout", Summary: "timed out", Mode: permission.Guarded,
	}
	if got, want := toolActivityLine(timeout, 90),
		"⚙ firecrawl_scrape · timed out · 444"; got != want {
		t.Errorf("timeout line = %q, want %q", got, want)
	}
	searchTimeout := engine.ToolActivity{
		Name: "web_search", Status: "timeout", Summary: "timed out · firecrawl", Mode: permission.Guarded,
	}
	if got, want := toolActivityLine(searchTimeout, 90),
		"⚙ web_search · timed out · firecrawl · 444"; got != want {
		t.Errorf("search timeout line = %q, want %q", got, want)
	}
}

func TestHistoricalFirecrawlSearchStillRenders(t *testing.T) {
	search := engine.ToolActivity{
		Name: "firecrawl_search", Status: "ok", Summary: "3 results", Mode: permission.Guarded,
	}
	if got := toolActivityLine(search, 90); !strings.Contains(got, "3 results") {
		t.Errorf("historical line = %q", got)
	}
}

func TestWebResearchIsDocumentedInHelp(t *testing.T) {
	m := New(nil)
	m.width = 120
	plain := stripANSI(strings.Join(m.helpRows(), "\n"))
	if !strings.Contains(plain, "MPM knowledge, web research") {
		t.Errorf("help is missing the web-research capability: %s", plain)
	}
}

func TestDiagnosticsReportWebResearchStatus(t *testing.T) {
	m := NewWithHooks(nil, Hooks{
		WebResearch: func() string {
			return "search: firecrawl=ready · exa=unavailable · duckduckgo=ready\nscrape: firecrawl=ready"
		},
	})
	m.width = 120
	plain := stripANSI(strings.Join(m.diagRows(), "\n"))
	for _, want := range []string{"web research", "duckduckgo=ready", "scrape: firecrawl=ready"} {
		if !strings.Contains(plain, want) {
			t.Errorf("diagnostics do not report %q:\n%s", want, plain)
		}
	}
}
