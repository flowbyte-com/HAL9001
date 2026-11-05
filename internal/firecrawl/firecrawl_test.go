package firecrawl

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const testKey = "fc-test-secret-key-do-not-leak"

func testClient(server *httptest.Server, mutate func(*Settings)) *Client {
	s := Settings{
		APIKey:     testKey,
		BaseURL:    server.URL + "/v2",
		Timeout:    2 * time.Second,
		MaxRetries: 0,
	}
	if mutate != nil {
		mutate(&s)
	}
	c := New(s)
	c.lookup = func(context.Context, string, string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("93.184.216.34")}, nil
	}
	c.sleep = func(context.Context, time.Duration) error { return nil }
	return c
}

func TestSearchReturnsRankedSourcesAndExcerpts(t *testing.T) {
	var gotAuth, gotQuery string
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/search" {
			t.Errorf("path = %q", r.URL.Path)
		}
		gotAuth = r.Header.Get("Authorization")
		data, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(data, &gotBody)
		gotQuery = r.URL.RawQuery
		_, _ = w.Write([]byte(`{
			"success": true,
			"creditsUsed": 2,
			"data": {"web": [
				{"position": 1, "title": "First", "url": "https://example.com/a", "description": "First excerpt."},
				{"position": 2, "title": "Second", "url": "https://example.com/b", "description": "Second excerpt."}
			]}
		}`))
	}))
	defer server.Close()

	c := testClient(server, nil)
	res, err := c.Search(context.Background(), "latest Go release", 9)
	if err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer "+testKey {
		t.Errorf("authorization header = %q", gotAuth)
	}
	if gotQuery != "" {
		t.Errorf("search sent a query string: %q", gotQuery)
	}
	if gotBody["query"] != "latest Go release" {
		t.Errorf("query = %#v", gotBody["query"])
	}
	if gotBody["limit"] != float64(DefaultSearchLimit) {
		t.Errorf("limit = %#v, want %d", gotBody["limit"], DefaultSearchLimit)
	}
	if _, ok := gotBody["scrapeOptions"]; ok {
		t.Error("search automatically requested scrapes")
	}
	if len(res.Results) != 2 || res.Results[0].Title != "First" ||
		res.Results[0].URL != "https://example.com/a" ||
		res.Results[0].Excerpt != "First excerpt." {
		t.Fatalf("results = %#v", res.Results)
	}
	if res.CreditsUsed != 2 || res.RetrievedAt.IsZero() {
		t.Errorf("metadata = %#v", res)
	}
}

func TestSearchAcceptsEmptyResults(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":{"web":[]}}`))
	}))
	defer server.Close()
	res, err := testClient(server, nil).Search(context.Background(), "nothing", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Results) != 0 {
		t.Errorf("results = %#v", res.Results)
	}
}

func TestSearchAcceptsLegacyDataArray(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":[{"title":"One","url":"https://example.com/1","description":"x"}]}`))
	}))
	defer server.Close()
	res, err := testClient(server, nil).Search(context.Background(), "q", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Results) != 1 || res.Results[0].Title != "One" {
		t.Fatalf("results = %#v", res.Results)
	}
}

func TestScrapeReturnsMarkdownAndMetadata(t *testing.T) {
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/scrape" {
			t.Errorf("path = %q", r.URL.Path)
		}
		data, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(data, &gotBody)
		_, _ = w.Write([]byte(`{
			"success": true,
			"creditsUsed": 1,
			"data": {
				"markdown": "# Release notes\n\nThe current version is 2.0.",
				"metadata": {
					"title": ["Release Notes", "HAL"],
					"description": "What changed.",
					"sourceURL": "https://example.com/article",
					"url": "https://example.com/article",
					"language": "en",
					"keywords": ["release", "notes"],
					"statusCode": 200,
					"contentType": "text/html",
					"publishedTime": "2026-10-01T12:00:00Z"
				}
			}
		}`))
	}))
	defer server.Close()

	c := testClient(server, nil)
	res, err := c.Scrape(context.Background(), "https://example.com/article")
	if err != nil {
		t.Fatal(err)
	}
	if res.Title != "Release Notes" || res.FinalURL != "https://example.com/article" ||
		!strings.Contains(res.Markdown, "current version") {
		t.Fatalf("result = %#v", res)
	}
	if res.PublishedAt != "2026-10-01T12:00:00Z" || len(res.Keywords) != 2 ||
		res.StatusCode != 200 || res.ContentType != "text/html" {
		t.Errorf("metadata = %#v", res)
	}
	formats, _ := gotBody["formats"].([]any)
	if len(formats) != 1 || formats[0] != "markdown" {
		t.Errorf("formats = %#v", gotBody["formats"])
	}
	if gotBody["onlyMainContent"] != true || gotBody["blockAds"] != true {
		t.Errorf("scrape options = %#v", gotBody)
	}
	if _, ok := gotBody["scrapeOptions"]; ok {
		t.Error("scrape body unexpectedly contains scrapeOptions")
	}
}

func TestAuthenticationAndPermissionErrorsAreSafe(t *testing.T) {
	cases := []struct {
		status int
		code   string
		body   string
	}{
		{http.StatusUnauthorized, CodeAuth, `{"success":false,"error":"Unauthorized: Invalid token"}`},
		{http.StatusForbidden, CodeForbidden, `{"success":false,"error":"Forbidden"}`},
		{http.StatusPaymentRequired, CodeCredits, `{"success":false,"error":"Insufficient credits"}`},
	}
	for _, tc := range cases {
		t.Run(http.StatusText(tc.status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			_, err := testClient(server, nil).Search(context.Background(), "q", 5)
			var fe *Error
			if !errors.As(err, &fe) || fe.Code != tc.code {
				t.Fatalf("error = %#v", err)
			}
			if strings.Contains(fe.Error(), testKey) {
				t.Errorf("error leaked the key: %q", fe.Error())
			}
		})
	}
}

func TestRateLimitIsRetriedAndThenReported(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		if calls == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"success":false,"error":"Rate limit exceeded"}`))
			return
		}
		_, _ = w.Write([]byte(`{"success":true,"data":{"web":[]}}`))
	}))
	defer server.Close()
	_, err := testClient(server, func(s *Settings) { s.MaxRetries = 1 }).
		Search(context.Background(), "q", 5)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Errorf("calls = %d, want 2", calls)
	}

	server2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"success":false,"error":"Rate limit exceeded"}`))
	}))
	defer server2.Close()
	_, err = testClient(server2, nil).Search(context.Background(), "q", 5)
	var fe *Error
	if !errors.As(err, &fe) || fe.Code != CodeRateLimited || fe.Summary() != "rate limited" {
		t.Fatalf("error = %#v", err)
	}
}

func TestTransientServerErrorIsRetried(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		if calls == 1 {
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte(`{"success":false,"error":"bad gateway"}`))
			return
		}
		_, _ = w.Write([]byte(`{"success":true,"data":{"web":[]}}`))
	}))
	defer server.Close()
	_, err := testClient(server, func(s *Settings) { s.MaxRetries = 1 }).
		Search(context.Background(), "q", 5)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Errorf("calls = %d, want 2", calls)
	}
}

func TestTimeoutAndCancellation(t *testing.T) {
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(250 * time.Millisecond)
		_, _ = w.Write([]byte(`{"success":true,"data":{"web":[]}}`))
	}))
	defer slow.Close()
	_, err := testClient(slow, func(s *Settings) { s.Timeout = 30 * time.Millisecond }).
		Search(context.Background(), "q", 5)
	var fe *Error
	if !errors.As(err, &fe) || fe.Code != CodeTimeout {
		t.Fatalf("timeout error = %#v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = testClient(slow, nil).Search(ctx, "q", 5)
	if !errors.As(err, &fe) || fe.Code != CodeCancelled {
		t.Fatalf("cancel error = %#v", err)
	}
}

func TestMalformedAndFailedResponses(t *testing.T) {
	malformed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`not json`))
	}))
	defer malformed.Close()
	_, err := testClient(malformed, nil).Search(context.Background(), "q", 5)
	var fe *Error
	if !errors.As(err, &fe) || fe.Code != CodeMalformed {
		t.Fatalf("malformed error = %#v", err)
	}

	failed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"success":false,"error":"the index is unavailable"}`))
	}))
	defer failed.Close()
	_, err = testClient(failed, nil).Search(context.Background(), "q", 5)
	if !errors.As(err, &fe) || fe.Code != CodeUpstream ||
		!strings.Contains(fe.Error(), "index is unavailable") {
		t.Fatalf("failed error = %#v", err)
	}
}

func TestOversizedResponseIsRejected(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(bytes.Repeat([]byte("x"), MaxResponseBytes+1))
	}))
	defer server.Close()
	_, err := testClient(server, nil).Search(context.Background(), "q", 5)
	var fe *Error
	if !errors.As(err, &fe) || fe.Code != CodeTooLarge {
		t.Fatalf("error = %#v", err)
	}
}

func TestSearchRejectsEmptyAndOversizedQueries(t *testing.T) {
	c := New(Settings{APIKey: testKey})
	if _, err := c.Search(context.Background(), "  ", 5); err == nil {
		t.Fatal("empty query accepted")
	}
	long := strings.Repeat("q", MaxQueryRunes+1)
	if _, err := c.Search(context.Background(), long, 5); err == nil {
		t.Fatal("oversized query accepted")
	}
}

func TestScrapeURLValidation(t *testing.T) {
	c := New(Settings{APIKey: testKey, BaseURL: DefaultBaseURL})
	public := func(context.Context, string, string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("93.184.216.34")}, nil
	}
	private := func(context.Context, string, string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("10.0.0.5")}, nil
	}
	c.lookup = public
	valid := []string{
		"https://example.com/",
		"https://example.com/path?q=1#section",
	}
	for _, raw := range valid {
		if _, err := c.ValidatePageURL(context.Background(), raw); err != nil {
			t.Errorf("valid URL %q refused: %v", raw, err)
		}
	}
	invalid := []string{
		"http://example.com/",
		"ftp://example.com/",
		"https://localhost/",
		"https://127.0.0.1/",
		"https://10.0.0.1/",
		"https://169.254.169.254/latest/meta-data",
		"https://[::1]/",
		"https://printer.local/",
		"https://intranet/",
		"https://user:pass@example.com/",
		"https://example.com:bad/",
	}
	for _, raw := range invalid {
		if _, err := c.ValidatePageURL(context.Background(), raw); err == nil {
			t.Errorf("invalid URL %q accepted", raw)
		}
	}
	c.lookup = private
	if _, err := c.ValidatePageURL(context.Background(), "https://evil.example.com/"); err == nil {
		t.Error("URL resolving to a private address accepted")
	}
}

func TestBaseURLValidation(t *testing.T) {
	if err := validateBaseURL("https://api.firecrawl.dev/v2"); err != nil {
		t.Errorf("public HTTPS refused: %v", err)
	}
	if err := validateBaseURL("http://127.0.0.1:8123/v2"); err != nil {
		t.Errorf("loopback HTTP refused: %v", err)
	}
	for _, raw := range []string{
		"http://api.firecrawl.dev/v2",
		"ftp://api.firecrawl.dev/v2",
		"https://user:pass@api.firecrawl.dev/v2",
	} {
		if err := validateBaseURL(raw); err == nil {
			t.Errorf("invalid base URL %q accepted", raw)
		}
	}
}

func TestMissingKeyAndLimitsDoNotExposeTheSecret(t *testing.T) {
	c := New(Settings{BaseURL: DefaultBaseURL})
	if c.Available() {
		t.Fatal("client without a key is available")
	}
	if strings.Contains(c.UnavailableReason(), testKey) {
		t.Error("unavailable reason leaked the key")
	}
	_, err := c.Search(context.Background(), "q", 5)
	var fe *Error
	if !errors.As(err, &fe) || fe.Code != CodeMissingKey {
		t.Fatalf("error = %#v", err)
	}

	withKey := New(Settings{APIKey: testKey, BaseURL: DefaultBaseURL})
	if got := withKey.Limits().APIKey; got != "" {
		t.Errorf("Limits exposed the key %q", got)
	}
}

func TestSettingsFromEnvAreBounded(t *testing.T) {
	t.Setenv(EnvAPIKey, "  fc-env-key  ")
	t.Setenv(EnvBaseURL, "http://127.0.0.1:8123/v2/")
	t.Setenv(EnvSearchLimit, "99")
	t.Setenv(EnvMaxSearches, "99")
	t.Setenv(EnvMaxScrapes, "99")
	t.Setenv(EnvTimeout, "999")
	t.Setenv(EnvMaxTurn, "999999")
	s := SettingsFromEnv()
	if s.APIKey != "fc-env-key" {
		t.Errorf("key = %q", s.APIKey)
	}
	if s.BaseURL != "http://127.0.0.1:8123/v2" {
		t.Errorf("base = %q", s.BaseURL)
	}
	if s.SearchLimit != MaxSearchLimit || s.MaxSearchesPerTurn != MaxSearchesPerTurn ||
		s.MaxScrapesPerTurn != MaxScrapesPerTurn || s.Timeout != MaxTimeout ||
		s.MaxTurnBytes != MaxMaxTurnBytes || s.MaxRetries != DefaultRetries {
		t.Errorf("settings = %#v", s)
	}
}

func TestErrorEchoedByAPIStillRedactsTheKey(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"success":false,"error":"bad key ` + testKey + `"}`))
	}))
	defer server.Close()
	_, err := testClient(server, nil).Search(context.Background(), "q", 5)
	if strings.Contains(err.Error(), testKey) {
		t.Fatalf("error leaked the key: %q", err)
	}
	if !strings.Contains(err.Error(), "[redacted]") {
		t.Fatalf("error was not redacted: %q", err)
	}
}
