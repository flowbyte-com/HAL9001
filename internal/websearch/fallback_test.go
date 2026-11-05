package websearch

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type fakeProvider struct {
	name      string
	available bool
	reason    string
	result    SearchResult
	err       error
	calls     int
}

func (p *fakeProvider) Name() string              { return p.name }
func (p *fakeProvider) Available() bool           { return p.available }
func (p *fakeProvider) UnavailableReason() string { return p.reason }
func (p *fakeProvider) Search(context.Context, string, int) (SearchResult, error) {
	p.calls++
	if p.err != nil {
		return SearchResult{}, p.err
	}
	res := p.result
	res.Provider = p.name
	return res, nil
}

func TestFallbackUsesExaAfterFirecrawlRateLimit(t *testing.T) {
	fc := &fakeProvider{name: ProviderFirecrawl, available: true,
		err: &Error{Code: CodeRateLimited, Provider: ProviderFirecrawl, Message: "rate limited"}}
	exa := &fakeProvider{name: ProviderExa, available: true, result: SearchResult{
		Results: []Hit{{Title: "Exa result", URL: "https://example.com/e"}},
	}}
	ddg := &fakeProvider{name: ProviderDuckDuckGo, available: true}
	res, err := NewFallback(fc, exa, ddg).Search(context.Background(), "q", 5)
	if err != nil {
		t.Fatal(err)
	}
	if res.Provider != ProviderExa || len(res.Results) != 1 || res.Results[0].Title != "Exa result" {
		t.Fatalf("result = %#v", res)
	}
	if len(res.Attempts) != 2 || res.Attempts[0].Status != "failed" ||
		res.Attempts[0].Code != CodeRateLimited || res.Attempts[1].Status != "ok" {
		t.Fatalf("attempts = %#v", res.Attempts)
	}
	if fc.calls != 1 || exa.calls != 1 || ddg.calls != 0 {
		t.Errorf("calls = %d/%d/%d", fc.calls, exa.calls, ddg.calls)
	}
}

func TestFallbackUsesDuckDuckGoAfterExaCredits(t *testing.T) {
	exa := &fakeProvider{name: ProviderExa, available: true,
		err: &Error{Code: CodeCredits, Provider: ProviderExa, Message: "no credits"}}
	ddg := &fakeProvider{name: ProviderDuckDuckGo, available: true, result: SearchResult{
		Results: []Hit{{Title: "DDG result", URL: "https://example.com/d"}},
	}}
	res, err := NewFallback(exa, ddg).Search(context.Background(), "q", 5)
	if err != nil {
		t.Fatal(err)
	}
	if res.Provider != ProviderDuckDuckGo || len(res.Results) != 1 {
		t.Fatalf("result = %#v", res)
	}
	if len(res.Attempts) != 2 || res.Attempts[1].Provider != ProviderDuckDuckGo {
		t.Fatalf("attempts = %#v", res.Attempts)
	}
}

func TestFallbackStopsOnAValidEmptyResult(t *testing.T) {
	fc := &fakeProvider{name: ProviderFirecrawl, available: true}
	exa := &fakeProvider{name: ProviderExa, available: true, result: SearchResult{
		Results: []Hit{{Title: "should not run", URL: "https://example.com/x"}},
	}}
	res, err := NewFallback(fc, exa).Search(context.Background(), "q", 5)
	if err != nil {
		t.Fatal(err)
	}
	if res.Provider != ProviderFirecrawl || len(res.Results) != 0 {
		t.Fatalf("result = %#v", res)
	}
	if exa.calls != 0 {
		t.Errorf("an empty valid result triggered another provider: %d calls", exa.calls)
	}
}

func TestFallbackSkipsMissingCredentials(t *testing.T) {
	fc := &fakeProvider{name: ProviderFirecrawl, available: false, reason: "FIRECRAWL_API_KEY is not set"}
	exa := &fakeProvider{name: ProviderExa, available: true, result: SearchResult{
		Results: []Hit{{Title: "Exa", URL: "https://example.com/e"}},
	}}
	res, err := NewFallback(fc, exa).Search(context.Background(), "q", 5)
	if err != nil {
		t.Fatal(err)
	}
	if res.Provider != ProviderExa || len(res.Attempts) != 2 ||
		res.Attempts[0].Status != "skipped" || res.Attempts[0].Code != CodeMissingKey {
		t.Fatalf("result = %#v", res)
	}
	if fc.calls != 0 {
		t.Errorf("unavailable provider was called")
	}
}

func TestFallbackAllUnavailable(t *testing.T) {
	_, err := NewFallback(
		&fakeProvider{name: ProviderFirecrawl},
		&fakeProvider{name: ProviderExa},
		&fakeProvider{name: ProviderDuckDuckGo},
	).Search(context.Background(), "q", 5)
	var fe *Error
	if !errors.As(err, &fe) || fe.Code != CodeMissingKey {
		t.Fatalf("error = %#v", err)
	}
	if len(fe.Attempts) != 3 {
		t.Errorf("attempts = %#v", fe.Attempts)
	}
}

func TestFallbackStopsOnCancellationAndInvalidRequest(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := NewFallback(&fakeProvider{name: ProviderFirecrawl, available: true}).
		Search(ctx, "q", 5)
	var fe *Error
	if !errors.As(err, &fe) || fe.Code != CodeCancelled {
		t.Fatalf("cancel error = %#v", err)
	}

	first := &fakeProvider{name: ProviderFirecrawl, available: true,
		err: &Error{Code: CodeInvalidArgs, Provider: ProviderFirecrawl, Message: "bad query"}}
	second := &fakeProvider{name: ProviderExa, available: true}
	_, err = NewFallback(first, second).Search(context.Background(), "q", 5)
	if !errors.As(err, &fe) || fe.Code != CodeInvalidArgs {
		t.Fatalf("invalid error = %#v", err)
	}
	if second.calls != 0 {
		t.Error("an invalid request fanned out to another provider")
	}
}

func TestFallbackReportsAttemptsWhenEveryProviderFails(t *testing.T) {
	_, err := NewFallback(
		&fakeProvider{name: ProviderFirecrawl, available: true,
			err: &Error{Code: CodeUpstream, Provider: ProviderFirecrawl, Message: "firecrawl down"}},
		&fakeProvider{name: ProviderExa, available: true,
			err: &Error{Code: CodeRateLimited, Provider: ProviderExa, Message: "exa limited"}},
		&fakeProvider{name: ProviderDuckDuckGo, available: true,
			err: &Error{Code: CodeBlocked, Provider: ProviderDuckDuckGo, Message: "ddg blocked"}},
	).Search(context.Background(), "q", 5)
	var fe *Error
	if !errors.As(err, &fe) {
		t.Fatalf("error = %#v", err)
	}
	if len(fe.Attempts) != 3 || fe.Attempts[2].Code != CodeBlocked {
		t.Fatalf("attempts = %#v", fe.Attempts)
	}
	if !strings.Contains(fe.Error(), "firecrawl: upstream") ||
		!strings.Contains(fe.Error(), "duckduckgo: blocked") {
		t.Errorf("error = %q", fe.Error())
	}
}

func TestFallbackDescribeShowsAvailability(t *testing.T) {
	f := NewFallback(
		&fakeProvider{name: ProviderFirecrawl, available: true},
		&fakeProvider{name: ProviderExa, available: false, reason: "EXA_API_KEY is not set"},
		&fakeProvider{name: ProviderDuckDuckGo, available: true},
	)
	got := f.Describe()
	for _, want := range []string{"firecrawl=ready", "exa=unavailable", "EXA_API_KEY is not set", "duckduckgo=ready"} {
		if !strings.Contains(got, want) {
			t.Errorf("Describe() = %q, missing %q", got, want)
		}
	}
}

func TestSettingsFromEnvPrefersNewNamesAndKeepsOldOnes(t *testing.T) {
	t.Setenv("HAL9001_WEB_SEARCH_LIMIT", "7")
	t.Setenv("HAL9001_FIRECRAWL_SEARCH_LIMIT", "9")
	t.Setenv("HAL9001_WEB_MAX_SEARCHES", "4")
	t.Setenv("HAL9001_WEB_MAX_SCRAPES", "3")
	t.Setenv("HAL9001_WEB_MAX_TURN_BYTES", "32768")
	s := SettingsFromEnv()
	if s.SearchLimit != 7 || s.MaxSearchesPerTurn != 4 || s.MaxScrapesPerTurn != 3 ||
		s.MaxTurnBytes != 32768 {
		t.Fatalf("settings = %#v", s)
	}

	t.Setenv("HAL9001_WEB_SEARCH_LIMIT", "")
	t.Setenv("HAL9001_WEB_MAX_SEARCHES", "")
	t.Setenv("HAL9001_WEB_MAX_SCRAPES", "")
	t.Setenv("HAL9001_WEB_MAX_TURN_BYTES", "")
	s = SettingsFromEnv()
	if s.SearchLimit != 9 {
		t.Errorf("old Firecrawl search limit was not used: %#v", s)
	}
	if s.MaxSearchesPerTurn != defaultMaxSearchesPerTurn ||
		s.MaxScrapesPerTurn != defaultMaxScrapesPerTurn ||
		s.MaxTurnBytes != defaultMaxTurnBytes {
		t.Errorf("defaults = %#v", s)
	}
}

func TestSettingsAreBounded(t *testing.T) {
	s := Settings{
		SearchLimit: 99, MaxSearchesPerTurn: 99, MaxScrapesPerTurn: 99,
		MaxTurnBytes: 1 << 30,
	}.normalized()
	if s.SearchLimit != maxSearchLimit || s.MaxSearchesPerTurn != maxSearchesPerTurn ||
		s.MaxScrapesPerTurn != maxScrapesPerTurn || s.MaxTurnBytes != maxMaxTurnBytes {
		t.Fatalf("settings = %#v", s)
	}
	if s.MaxSearchBytes <= 0 || s.MaxScrapeBytes <= 0 {
		t.Fatalf("per-request budgets = %#v", s)
	}
}

func TestErrorClassification(t *testing.T) {
	cases := []struct {
		code     string
		summary  string
		fallback bool
	}{
		{CodeMissingKey, "not configured", true},
		{CodeAuth, "authentication failed", true},
		{CodeCredits, "credits exhausted", true},
		{CodeRateLimited, "rate limited", true},
		{CodeBlocked, "blocked", true},
		{CodeLayout, "layout changed", true},
		{CodeTimeout, "timed out", true},
		{CodeCancelled, "cancelled", false},
		{CodeInvalidArgs, "invalid arguments", false},
		{CodeQuota, "limit reached", false},
		{CodeInvalidURL, "invalid URL", false},
	}
	for _, tc := range cases {
		e := &Error{Code: tc.code}
		if e.Summary() != tc.summary || e.Fallbackable() != tc.fallback {
			t.Errorf("%s: summary=%q fallback=%v", tc.code, e.Summary(), e.Fallbackable())
		}
	}
}

func TestSleepContextHonoursCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := sleepContext(ctx, 50*time.Millisecond)
	var fe *Error
	if !errors.As(err, &fe) || fe.Code != CodeCancelled {
		t.Fatalf("error = %#v", err)
	}
}
