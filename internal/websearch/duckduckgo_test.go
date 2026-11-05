package websearch

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const ddgFixture = `<!DOCTYPE html><html><body>
<table>
<tr><td>1.</td><td><a rel="nofollow" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.com%2Fa%3Fx%3D1%26y%3D2&amp;rut=abc" class='result-link'>Alpha <b>Result</b></a></td></tr>
<tr><td>&nbsp;</td><td class='result-snippet'>Alpha <b>snippet</b> text.</td></tr>
<tr><td>&nbsp;</td><td><span class='link-text'>example.com/a</span></td></tr>
<tr><td>2.</td><td><a rel="nofollow" href="https://example.com/b" class='result-link'>Beta Result</a></td></tr>
<tr><td>&nbsp;</td><td class='result-snippet'>Beta snippet.</td></tr>
<tr><td>3.</td><td><a rel="nofollow" href="//duckduckgo.com/y.js?ad_provider=test" class='result-link result-sponsored'>Sponsored result</a></td></tr>
<tr><td>&nbsp;</td><td class='result-snippet'>Advertisement.</td></tr>
<tr><td>4.</td><td><a rel="nofollow" href="http://127.0.0.1/secret" class='result-link'>Internal result</a></td></tr>
<tr><td>&nbsp;</td><td class='result-snippet'>Must be ignored.</td></tr>
</table>
</body></html>`

func ddgProvider(server *httptest.Server, mutate func(*DuckDuckGoSettings)) *DuckDuckGoProvider {
	s := DuckDuckGoSettings{BaseURL: server.URL, Timeout: 2 * time.Second, MinInterval: time.Nanosecond}
	if mutate != nil {
		mutate(&s)
	}
	return NewDuckDuckGoProvider(s)
}

func TestDuckDuckGoParsesOrganicResultsAndIgnoresAds(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s", r.Method)
		}
		if !strings.Contains(r.Header.Get("Content-Type"), "application/x-www-form-urlencoded") {
			t.Errorf("content type = %q", r.Header.Get("Content-Type"))
		}
		_ = r.ParseForm()
		if r.Form.Get("q") != "alpha beta" {
			t.Errorf("query = %q", r.Form.Get("q"))
		}
		_, _ = w.Write([]byte(ddgFixture))
	}))
	defer server.Close()
	res, err := ddgProvider(server, nil).Search(context.Background(), "alpha beta", 5)
	if err != nil {
		t.Fatal(err)
	}
	if res.Provider != ProviderDuckDuckGo || len(res.Results) != 2 {
		t.Fatalf("result = %#v", res)
	}
	first := res.Results[0]
	if first.Title != "Alpha Result" || first.URL != "https://example.com/a?x=1&y=2" ||
		first.Excerpt != "Alpha snippet text." {
		t.Fatalf("first = %#v", first)
	}
	if res.Results[1].Title != "Beta Result" || res.Results[1].URL != "https://example.com/b" {
		t.Fatalf("second = %#v", res.Results[1])
	}
	for _, hit := range res.Results {
		if strings.Contains(hit.Title, "Sponsored") || strings.Contains(hit.URL, "127.0.0.1") {
			t.Errorf("unsafe or sponsored result survived: %#v", hit)
		}
	}
}

func TestDuckDuckGoEmptyBlockedAndLayoutPages(t *testing.T) {
	cases := []struct {
		body string
		code string
		ok   bool
	}{
		{"<html><body><p>No results.</p></body></html>", "", true},
		{"<html><body><p>If this persists, please email us.</p></body></html>", CodeBlocked, false},
		{"<html><body><p>Our systems detected an anomaly.</p></body></html>", CodeBlocked, false},
		{"<html><body><p>Completely new layout.</p></body></html>", CodeLayout, false},
	}
	for _, tc := range cases {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(tc.body))
		}))
		_, err := ddgProvider(server, nil).Search(context.Background(), "q", 5)
		server.Close()
		if tc.ok {
			if err != nil {
				t.Errorf("empty result: %v", err)
			}
			continue
		}
		var fe *Error
		if !errors.As(err, &fe) || fe.Code != tc.code {
			t.Errorf("body %q: error = %#v", tc.body, err)
		}
	}
}

func TestDuckDuckGoHTTPFailures(t *testing.T) {
	for status, code := range map[int]string{
		http.StatusForbidden:           CodeBlocked,
		http.StatusAccepted:            CodeBlocked,
		http.StatusServiceUnavailable:  CodeBlocked,
		http.StatusTooManyRequests:     CodeRateLimited,
		http.StatusInternalServerError: CodeUpstream,
	} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(status)
		}))
		_, err := ddgProvider(server, nil).Search(context.Background(), "q", 5)
		server.Close()
		var fe *Error
		if !errors.As(err, &fe) || fe.Code != code {
			t.Errorf("status %d: error = %#v", status, err)
		}
	}
}

func TestDuckDuckGoOversizedAndInvalidOnlyResults(t *testing.T) {
	oversized := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(bytes.Repeat([]byte("x"), maxDuckDuckGoResponseBytes+1))
	}))
	defer oversized.Close()
	_, err := ddgProvider(oversized, nil).Search(context.Background(), "q", 5)
	var fe *Error
	if !errors.As(err, &fe) || fe.Code != CodeTooLarge {
		t.Fatalf("oversized = %#v", err)
	}

	invalid := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<html><body><table><tr><td><a class="result-link" href="http://10.0.0.1/">bad</a></td></tr></table></body></html>`))
	}))
	defer invalid.Close()
	_, err = ddgProvider(invalid, nil).Search(context.Background(), "q", 5)
	if !errors.As(err, &fe) || fe.Code != CodeLayout {
		t.Fatalf("invalid only = %#v", err)
	}
}

func TestDuckDuckGoCancellationAndConservatism(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(ddgFixture))
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := ddgProvider(server, nil).Search(ctx, "q", 5)
	var fe *Error
	if !errors.As(err, &fe) || fe.Code != CodeCancelled {
		t.Fatalf("cancel = %#v", err)
	}

	p := ddgProvider(server, func(s *DuckDuckGoSettings) {
		s.MinInterval = 10 * time.Second
	})
	var waits []time.Duration
	p.sleep = func(_ context.Context, d time.Duration) error {
		waits = append(waits, d)
		return nil
	}
	if _, err := p.Search(context.Background(), "q", 5); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Search(context.Background(), "q", 5); err != nil {
		t.Fatal(err)
	}
	if len(waits) != 1 || waits[0] < 9*time.Second {
		t.Fatalf("conservative waits = %#v", waits)
	}
}

func TestDuckDuckGoSettingsBounds(t *testing.T) {
	s := DuckDuckGoSettings{Timeout: 999 * time.Second, MinInterval: -1}.normalized()
	if s.Timeout != maxDuckDuckGoTimeout || s.MinInterval != DefaultDuckDuckGoInterval {
		t.Fatalf("settings = %#v", s)
	}
	if got := (DuckDuckGoSettings{}).normalized(); got.MinInterval != DefaultDuckDuckGoInterval {
		t.Errorf("default interval = %v", got.MinInterval)
	}
	if err := validateDuckDuckGoURL("http://api.duckduckgo.com"); err == nil {
		t.Error("remote plain HTTP accepted")
	}
}
