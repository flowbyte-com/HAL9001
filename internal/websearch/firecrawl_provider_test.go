package websearch

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/flowbyte-com/HAL9001/internal/firecrawl"
)

func TestFirecrawlProviderNormalizesAndMapsErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer fc-provider-key" {
			t.Errorf("authorization = %q", r.Header.Get("Authorization"))
		}
		_, _ = w.Write([]byte(`{"success":true,"creditsUsed":2,"data":{"web":[
			{"position":1,"title":"Firecrawl result","url":"https://example.com/fc","description":"Excerpt."}
		]}}`))
	}))
	defer server.Close()
	client := firecrawl.New(firecrawl.Settings{
		APIKey: "fc-provider-key", BaseURL: server.URL + "/v2", Timeout: 2 * time.Second,
	})
	p := NewFirecrawlProvider(client)
	res, err := p.Search(context.Background(), "q", 5)
	if err != nil {
		t.Fatal(err)
	}
	if res.Provider != ProviderFirecrawl || len(res.Results) != 1 ||
		res.Results[0].Title != "Firecrawl result" || res.CreditsUsed != 2 {
		t.Fatalf("result = %#v", res)
	}

	limited := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"success":false,"error":"rate limited"}`))
	}))
	defer limited.Close()
	limitedClient := firecrawl.New(firecrawl.Settings{
		APIKey: "fc-provider-key", BaseURL: limited.URL + "/v2", Timeout: 2 * time.Second, MaxRetries: 0,
	})
	_, err = NewFirecrawlProvider(limitedClient).Search(context.Background(), "q", 5)
	var fe *Error
	if !errors.As(err, &fe) || fe.Code != CodeRateLimited || fe.Provider != ProviderFirecrawl {
		t.Fatalf("error = %#v", err)
	}
}

func TestFirecrawlProviderUnavailableWithoutKey(t *testing.T) {
	p := NewFirecrawlProvider(firecrawl.New(firecrawl.Settings{BaseURL: firecrawl.DefaultBaseURL}))
	if p.Available() {
		t.Fatal("provider without a key is available")
	}
	_, err := p.Search(context.Background(), "q", 5)
	var fe *Error
	if !errors.As(err, &fe) || fe.Code != CodeMissingKey {
		t.Fatalf("error = %#v", err)
	}
}

func TestFirecrawlScraperErrorMapping(t *testing.T) {
	scraper := NewFirecrawlScraper(firecrawl.New(firecrawl.Settings{
		APIKey: "fc-provider-key", BaseURL: firecrawl.DefaultBaseURL,
	}))
	if !scraper.Available() {
		t.Fatal("scraper with a key is unavailable")
	}
	// An HTTP URL is refused before any network request.
	_, err := scraper.ValidatePageURL(context.Background(), "http://example.com/")
	var fe *Error
	if !errors.As(err, &fe) || fe.Code != CodeInvalidURL || fe.Provider != ProviderFirecrawl {
		t.Fatalf("error = %#v", err)
	}
}

func TestSearchResultJSONShape(t *testing.T) {
	// Guard the unified fields that the model and the transcript rely on.
	raw, err := json.Marshal(SearchResult{
		Query: "q", Provider: ProviderExa,
		Results:     []Hit{{Position: 1, Title: "T", URL: "https://example.com/", Excerpt: "E", PublishedAt: "2026-01-01"}},
		RetrievedAt: time.Unix(0, 0).UTC(), CostUSD: 0.01,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"provider":"exa"`, `"published_at":"2026-01-01"`, `"cost_usd":0.01`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("JSON %s is missing %s", raw, want)
		}
	}
}
