package websearch

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flowbyte-com/HAL9001/internal/firecrawl"
	"github.com/flowbyte-com/HAL9001/internal/permission"
	"github.com/flowbyte-com/HAL9001/internal/provider"
	"github.com/flowbyte-com/HAL9001/internal/workspace"
)

type fakeSearcher struct {
	available bool
	describe  string
	result    SearchResult
	err       error
	calls     int
}

func (s *fakeSearcher) Search(context.Context, string, int) (SearchResult, error) {
	s.calls++
	if s.err != nil {
		return SearchResult{}, s.err
	}
	return s.result, nil
}
func (s *fakeSearcher) Available() bool { return s.available }
func (s *fakeSearcher) Describe() string {
	if s.describe == "" {
		return "firecrawl=ready"
	}
	return s.describe
}

type fakeScraper struct {
	available bool
	reason    string
	page      firecrawl.ScrapeResult
	validate  string
	valErr    error
	scrapeErr error
	calls     int
}

func (s *fakeScraper) Available() bool           { return s.available }
func (s *fakeScraper) UnavailableReason() string { return s.reason }
func (s *fakeScraper) ValidatePageURL(_ context.Context, raw string) (string, error) {
	if s.valErr != nil {
		return "", s.valErr
	}
	if s.validate != "" {
		return s.validate, nil
	}
	return raw, nil
}
func (s *fakeScraper) Scrape(context.Context, string) (firecrawl.ScrapeResult, error) {
	s.calls++
	if s.scrapeErr != nil {
		return firecrawl.ScrapeResult{}, s.scrapeErr
	}
	return s.page, nil
}

func registryFor(searcher Searcher, scraper Scraper, mutate func(*Settings)) *Registry {
	s := Settings{SearchLimit: 5, MaxSearchesPerTurn: 3, MaxScrapesPerTurn: 2}
	if mutate != nil {
		mutate(&s)
	}
	return NewRegistry(searcher, scraper, s)
}

func TestRegistryOffersUsableToolsOnly(t *testing.T) {
	searcher := &fakeSearcher{available: true}
	scraper := &fakeScraper{available: false, reason: "FIRECRAWL_API_KEY is not set"}
	r := registryFor(searcher, scraper, nil)
	tools := r.Tools()
	if len(tools) != 1 || tools[0].Name != toolWebSearch {
		t.Fatalf("tools = %#v", tools)
	}
	if !r.Has(toolWebSearch) || !r.Has(toolScrape) {
		t.Error("Has does not own both names")
	}
	if !strings.Contains(r.StatusLine(), "firecrawl=ready") ||
		!strings.Contains(r.StatusLine(), "unavailable") {
		t.Errorf("status = %q", r.StatusLine())
	}

	scraper.available = true
	if len(r.Tools()) != 2 {
		t.Fatalf("tools after scrape became available = %#v", r.Tools())
	}
}

func TestRegistryWebSearchNormalizesProviderAttribution(t *testing.T) {
	now := time.Now().UTC()
	searcher := &fakeSearcher{available: true, result: SearchResult{
		Query: "q", Provider: ProviderExa,
		Results:     []Hit{{Position: 1, Title: "A", URL: "https://example.com/a", Excerpt: "excerpt"}},
		RetrievedAt: now, CostUSD: 0.007,
		Attempts: []Attempt{
			{Provider: ProviderFirecrawl, Status: "failed", Code: CodeRateLimited},
			{Provider: ProviderExa, Status: "ok"},
		},
	}}
	r := registryFor(searcher, &fakeScraper{}, nil)
	res := r.Execute(context.Background(), provider.ToolCall{
		Name: toolWebSearch, Arguments: map[string]any{"query": "q"},
	}, "turn-1")
	if res.Status != "ok" || res.Provider != ProviderExa ||
		res.Summary != "1 result · exa · fallback" {
		t.Fatalf("result = %#v", res)
	}
	var out struct {
		OK        bool      `json:"ok"`
		Tool      string    `json:"tool"`
		Provider  string    `json:"provider"`
		Count     int       `json:"result_count"`
		Cost      float64   `json:"cost_usd"`
		Attempts  []Attempt `json:"attempts"`
		Untrusted bool      `json:"untrusted"`
		Results   []Hit     `json:"results"`
	}
	if err := json.Unmarshal([]byte(res.Content), &out); err != nil {
		t.Fatal(err)
	}
	if !out.OK || out.Tool != toolWebSearch || out.Provider != ProviderExa ||
		out.Count != 1 || out.Cost != 0.007 || !out.Untrusted ||
		len(out.Attempts) != 2 || len(out.Results) != 1 {
		t.Fatalf("output = %#v", out)
	}
}

func TestRegistryEmptyResultIsNotAFallback(t *testing.T) {
	searcher := &fakeSearcher{available: true, result: SearchResult{
		Query: "q", Provider: ProviderFirecrawl, RetrievedAt: time.Now(),
	}}
	r := registryFor(searcher, &fakeScraper{}, nil)
	res := r.Execute(context.Background(), provider.ToolCall{
		Name: toolWebSearch, Arguments: map[string]any{"query": "q"},
	}, "turn-1")
	if res.Status != "ok" || res.Summary != "0 results · firecrawl" {
		t.Fatalf("result = %#v", res)
	}
	if searcher.calls != 1 {
		t.Errorf("searcher calls = %d", searcher.calls)
	}
}

func TestRegistryTurnQuotasAndReset(t *testing.T) {
	searcher := &fakeSearcher{available: true, result: SearchResult{
		Provider: ProviderFirecrawl, RetrievedAt: time.Now(),
	}}
	r := registryFor(searcher, &fakeScraper{}, func(s *Settings) {
		s.MaxSearchesPerTurn = 1
	})
	call := provider.ToolCall{Name: toolWebSearch, Arguments: map[string]any{"query": "q"}}
	if res := r.Execute(context.Background(), call, "turn-1"); res.Status != "ok" {
		t.Fatalf("first = %#v", res)
	}
	second := r.Execute(context.Background(), call, "turn-1")
	if second.Code != CodeQuota || second.Summary != "limit reached" {
		t.Fatalf("second = %#v", second)
	}
	if searcher.calls != 1 {
		t.Errorf("quota did not stop the provider: %d calls", searcher.calls)
	}
	if res := r.Execute(context.Background(), call, "turn-2"); res.Status != "ok" {
		t.Fatalf("new turn = %#v", res)
	}
}

func TestRegistryScrapeTruncatesAndValidatesBeforeQuota(t *testing.T) {
	long := strings.Repeat("A", 20*1024)
	scraper := &fakeScraper{available: true, page: firecrawl.ScrapeResult{
		RequestedURL: "https://example.com/a", SourceURL: "https://example.com/a",
		FinalURL: "https://example.com/a", Title: "Example", Markdown: long,
		PublishedAt: "2026-10-01", RetrievedAt: time.Now(), StatusCode: 200,
	}}
	r := registryFor(&fakeSearcher{}, scraper, func(s *Settings) {
		s.MaxScrapesPerTurn = 1
		s.MaxScrapeBytes = 1024
		s.MaxTurnBytes = minMaxTurnBytes
	})
	scraper.valErr = &Error{Code: CodeInvalidURL, Message: "refused"}
	bad := r.Execute(context.Background(), provider.ToolCall{
		Name: toolScrape, Arguments: map[string]any{"url": "https://127.0.0.1/"},
	}, "turn-1")
	if bad.Code != CodeInvalidURL {
		t.Fatalf("bad = %#v", bad)
	}
	scraper.valErr = nil
	res := r.Execute(context.Background(), provider.ToolCall{
		Name: toolScrape, Arguments: map[string]any{"url": "https://example.com/a"},
	}, "turn-1")
	if res.Status != "ok" || !res.Truncated || res.Bytes > 1024 {
		t.Fatalf("scrape = %#v", res)
	}
	var out struct {
		Provider  string `json:"provider"`
		FullBytes int    `json:"full_bytes"`
		Returned  int    `json:"returned_bytes"`
		Truncated bool   `json:"truncated"`
		Untrusted bool   `json:"untrusted"`
	}
	if err := json.Unmarshal([]byte(res.Content), &out); err != nil {
		t.Fatal(err)
	}
	if out.Provider != ProviderFirecrawl || out.FullBytes != len(long) ||
		out.Returned > 1024 || !out.Truncated || !out.Untrusted {
		t.Fatalf("output = %#v", out)
	}
}

func TestScrapeNeverFallsBackToSearchProviders(t *testing.T) {
	searcher := &fakeSearcher{available: true, result: SearchResult{
		Provider: ProviderExa, RetrievedAt: time.Now(),
		Results: []Hit{{Title: "search", URL: "https://example.com/s"}},
	}}
	r := registryFor(searcher, &fakeScraper{available: false, reason: "no key"}, nil)
	res := r.Execute(context.Background(), provider.ToolCall{
		Name: toolScrape, Arguments: map[string]any{"url": "https://example.com/page"},
	}, "turn-1")
	if res.Code != CodeMissingKey || res.Status != "failed" {
		t.Fatalf("scrape = %#v", res)
	}
	if searcher.calls != 0 {
		t.Errorf("scrape switched to a search provider: %d calls", searcher.calls)
	}
}

func TestRegistrySearchFailureExposesAttempts(t *testing.T) {
	searcher := &fakeSearcher{available: true, err: &Error{
		Code: CodeBlocked, Provider: ProviderDuckDuckGo, Message: "blocked",
		Attempts: []Attempt{
			{Provider: ProviderFirecrawl, Status: "failed", Code: CodeRateLimited},
			{Provider: ProviderExa, Status: "failed", Code: CodeCredits},
			{Provider: ProviderDuckDuckGo, Status: "failed", Code: CodeBlocked},
		},
	}}
	r := registryFor(searcher, &fakeScraper{}, nil)
	res := r.Execute(context.Background(), provider.ToolCall{
		Name: toolWebSearch, Arguments: map[string]any{"query": "q"},
	}, "turn-1")
	if res.Status != "failed" || res.Code != CodeBlocked || res.Summary != "all providers failed" {
		t.Fatalf("result = %#v", res)
	}
	var out struct {
		Attempts  []Attempt `json:"attempts"`
		Untrusted bool      `json:"untrusted"`
	}
	_ = json.Unmarshal([]byte(res.Content), &out)
	if len(out.Attempts) != 3 || !out.Untrusted {
		t.Fatalf("output = %#v", out)
	}
}

func TestRegistrySearchBudgetShortensExcerpts(t *testing.T) {
	searcher := &fakeSearcher{available: true, result: SearchResult{
		Provider: ProviderExa, RetrievedAt: time.Now(),
		Results: []Hit{
			{Title: "A", URL: "https://example.com/a", Excerpt: strings.Repeat("excerpt ", 400)},
			{Title: "B", URL: "https://example.com/b", Excerpt: strings.Repeat("excerpt ", 400)},
		},
	}}
	r := registryFor(searcher, &fakeScraper{}, func(s *Settings) {
		s.MaxSearchBytes = 2048
		s.MaxTurnBytes = minMaxTurnBytes
	})
	res := r.Execute(context.Background(), provider.ToolCall{
		Name: toolWebSearch, Arguments: map[string]any{"query": "q"},
	}, "turn-1")
	if res.Status != "ok" || !res.Truncated || len(res.Content) > 2048 ||
		!strings.Contains(res.Summary, "shortened") {
		t.Fatalf("result = %#v, bytes=%d", res, len(res.Content))
	}
}

func TestRegistryAuditIsContentFree(t *testing.T) {
	searcher := &fakeSearcher{available: true, result: SearchResult{
		Provider: ProviderExa, RetrievedAt: time.Now(),
		Results: []Hit{{Title: "secret title", URL: "https://example.com/a", Excerpt: "secret excerpt"}},
	}}
	r := registryFor(searcher, &fakeScraper{}, nil)
	audit, err := workspace.OpenAudit(filepath.Join(t.TempDir(), "audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	r.WithAudit(audit, func() string { return "chat" })
	res := r.Execute(context.Background(), provider.ToolCall{
		Name: toolWebSearch, Arguments: map[string]any{"query": "secret query"},
	}, "turn-1")
	if res.Status != "ok" {
		t.Fatalf("result = %#v", res)
	}
	records, _ := audit.Records()
	if len(records) != 1 {
		t.Fatalf("records = %d", len(records))
	}
	raw, _ := json.Marshal(records[0])
	if strings.Contains(string(raw), "secret") || strings.Contains(string(raw), "example.com") {
		t.Errorf("audit leaked content: %s", raw)
	}
	if records[0].Tool != toolWebSearch || records[0].Mode != "444" {
		t.Errorf("audit = %#v", records[0])
	}
}

func TestRegistryGuidanceAndStatus(t *testing.T) {
	r := registryFor(&fakeSearcher{available: true, describe: "firecrawl=ready · exa=ready · duckduckgo=ready"},
		&fakeScraper{available: true}, nil)
	g := r.Guidance()
	for _, want := range []string{"web_search", "firecrawl_scrape", "untrusted", "never enable 666", "provider chain"} {
		if !strings.Contains(g, want) {
			t.Errorf("guidance missing %q:\n%s", want, g)
		}
	}
	status := r.StatusLine()
	for _, want := range []string{"firecrawl=ready", "exa=ready", "duckduckgo=ready", "scrape"} {
		if !strings.Contains(status, want) {
			t.Errorf("status missing %q: %s", want, status)
		}
	}
}

func TestRegistryRejectsMalformedArguments(t *testing.T) {
	r := registryFor(&fakeSearcher{available: true}, &fakeScraper{available: true}, nil)
	cases := []provider.ToolCall{
		{Name: toolWebSearch, Arguments: map[string]any{}},
		{Name: toolWebSearch, Arguments: map[string]any{"query": "q", "limit": float64(0)}},
		{Name: toolWebSearch, Arguments: map[string]any{"query": "q", "extra": true}},
		{Name: toolScrape, Arguments: map[string]any{"url": "http://example.com/"}},
		{Name: toolScrape, Arguments: map[string]any{"url": "https://example.com/", "extra": true}},
	}
	for _, call := range cases {
		res := r.Execute(context.Background(), call, "turn-1")
		if res.Status != "failed" || res.Code == "" {
			t.Errorf("call = %#v -> %#v", call, res)
		}
	}
}

func TestRegistryModeIsRecordedButNotGranted(t *testing.T) {
	r := registryFor(&fakeSearcher{available: true}, &fakeScraper{available: true}, nil)
	r.WithMode(func() permission.Mode { return permission.Unrestricted })
	res := r.Execute(context.Background(), provider.ToolCall{
		Name: toolWebSearch, Arguments: map[string]any{"query": "q"},
	}, "turn-1")
	if res.Mode != permission.Unrestricted {
		t.Errorf("mode = %v", res.Mode)
	}
}
