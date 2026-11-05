package websearch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const exaKey = "exa-test-secret-key"

func exaProvider(server *httptest.Server, mutate func(*ExaSettings)) *ExaProvider {
	s := ExaSettings{APIKey: exaKey, BaseURL: server.URL, Timeout: 2 * time.Second}
	if mutate != nil {
		mutate(&s)
	}
	return NewExaProvider(s)
}

func TestExaSearchNormalizesResults(t *testing.T) {
	var gotAuth, gotPath string
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		data, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(data, &gotBody)
		_, _ = w.Write([]byte(`{
			"requestId":"req-1",
			"results":[
				{"title":"First","url":"https://example.com/a","publishedDate":"2026-10-01T00:00:00.000Z","highlights":["First highlight."]},
				{"title":"Second","url":"https://example.com/b","highlights":["Second highlight."]}
			],
			"costDollars":{"total":0.007}
		}`))
	}))
	defer server.Close()

	p := exaProvider(server, nil)
	res, err := p.Search(context.Background(), "latest release", 2)
	if err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer "+exaKey || gotPath != "/search" {
		t.Errorf("auth/path = %q/%q", gotAuth, gotPath)
	}
	if gotBody["numResults"] != float64(2) || gotBody["type"] != "auto" {
		t.Errorf("body = %#v", gotBody)
	}
	contents, _ := gotBody["contents"].(map[string]any)
	if contents["highlights"] != true {
		t.Errorf("contents = %#v", gotBody["contents"])
	}
	if _, ok := contents["text"]; ok {
		t.Error("Exa requested full text")
	}
	if res.Provider != ProviderExa || len(res.Results) != 2 ||
		res.Results[0].Title != "First" || res.Results[0].Excerpt != "First highlight." ||
		res.Results[0].PublishedAt != "2026-10-01T00:00:00.000Z" {
		t.Fatalf("result = %#v", res)
	}
	if res.CostUSD != 0.007 || res.RetrievedAt.IsZero() {
		t.Errorf("cost/time = %#v", res)
	}
}

func TestExaEmptyResultsAreValid(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"results":[],"costDollars":{"total":0.001}}`))
	}))
	defer server.Close()
	res, err := exaProvider(server, nil).Search(context.Background(), "q", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Results) != 0 || res.Provider != ProviderExa {
		t.Fatalf("result = %#v", res)
	}
}

func TestExaErrors(t *testing.T) {
	cases := []struct {
		status int
		code   string
	}{
		{http.StatusUnauthorized, CodeAuth},
		{http.StatusPaymentRequired, CodeCredits},
		{http.StatusForbidden, CodeForbidden},
		{http.StatusTooManyRequests, CodeRateLimited},
		{http.StatusBadRequest, CodeRejected},
		{http.StatusInternalServerError, CodeUpstream},
	}
	for _, tc := range cases {
		t.Run(http.StatusText(tc.status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(`{"error":"upstream detail","tag":"TEST"}`))
			}))
			defer server.Close()
			_, err := exaProvider(server, nil).Search(context.Background(), "q", 5)
			var fe *Error
			if !errors.As(err, &fe) || fe.Code != tc.code {
				t.Fatalf("error = %#v", err)
			}
			if strings.Contains(fe.Error(), exaKey) {
				t.Error("error leaked the key")
			}
		})
	}
}

func TestExaMalformedOversizedAndInvalidURLs(t *testing.T) {
	malformed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`not json`))
	}))
	defer malformed.Close()
	_, err := exaProvider(malformed, nil).Search(context.Background(), "q", 5)
	var fe *Error
	if !errors.As(err, &fe) || fe.Code != CodeMalformed {
		t.Fatalf("malformed = %#v", err)
	}

	oversized := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(bytes.Repeat([]byte("x"), maxExaResponseBytes+1))
	}))
	defer oversized.Close()
	_, err = exaProvider(oversized, nil).Search(context.Background(), "q", 5)
	if !errors.As(err, &fe) || fe.Code != CodeTooLarge {
		t.Fatalf("oversized = %#v", err)
	}

	invalid := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"results":[{"title":"local","url":"http://127.0.0.1/secret"},{"title":"private","url":"https://10.0.0.1/"}]}`))
	}))
	defer invalid.Close()
	_, err = exaProvider(invalid, nil).Search(context.Background(), "q", 5)
	if !errors.As(err, &fe) || fe.Code != CodeMalformed {
		t.Fatalf("invalid URLs = %#v", err)
	}
}

func TestExaTimeoutCancellationAndRedaction(t *testing.T) {
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(250 * time.Millisecond)
		_, _ = w.Write([]byte(`{"results":[]}`))
	}))
	defer slow.Close()
	_, err := exaProvider(slow, func(s *ExaSettings) { s.Timeout = 30 * time.Millisecond }).
		Search(context.Background(), "q", 5)
	var fe *Error
	if !errors.As(err, &fe) || fe.Code != CodeTimeout {
		t.Fatalf("timeout = %#v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = exaProvider(slow, nil).Search(ctx, "q", 5)
	if !errors.As(err, &fe) || fe.Code != CodeCancelled {
		t.Fatalf("cancel = %#v", err)
	}

	echo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"bad key ` + exaKey + `"}`))
	}))
	defer echo.Close()
	_, err = exaProvider(echo, nil).Search(context.Background(), "q", 5)
	if strings.Contains(err.Error(), exaKey) || !strings.Contains(err.Error(), "[redacted]") {
		t.Fatalf("redaction = %q", err.Error())
	}
}

func TestExaMissingKeyIsUnavailable(t *testing.T) {
	p := NewExaProvider(ExaSettings{BaseURL: DefaultExaBaseURL})
	if p.Available() {
		t.Fatal("provider without a key is available")
	}
	if !strings.Contains(p.UnavailableReason(), "EXA_API_KEY") {
		t.Errorf("reason = %q", p.UnavailableReason())
	}
	_, err := p.Search(context.Background(), "q", 5)
	var fe *Error
	if !errors.As(err, &fe) || fe.Code != CodeMissingKey {
		t.Fatalf("error = %#v", err)
	}
}

func TestExaBaseURLValidation(t *testing.T) {
	if err := validateExaBaseURL("https://api.exa.ai"); err != nil {
		t.Errorf("public HTTPS refused: %v", err)
	}
	if err := validateExaBaseURL("http://127.0.0.1:8124"); err != nil {
		t.Errorf("loopback HTTP refused: %v", err)
	}
	for _, raw := range []string{"http://api.exa.ai", "ftp://api.exa.ai", "https://user:pass@api.exa.ai"} {
		if err := validateExaBaseURL(raw); err == nil {
			t.Errorf("invalid base %q accepted", raw)
		}
	}
}
