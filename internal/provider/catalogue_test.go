package provider

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A catalogue that returns HTML instead of JSON is the DeepSeek defect:
// the endpoint answered, but there is no model list to read. The error
// must say the endpoint was reached, name the status and the content
// type, and allow adding the model by hand — never a raw JSON parsing
// failure.
func TestOpenAIListingWithHTMLReportsCatalogueUnavailable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("<html><body>proxy error</body></html>"))
	}))
	defer srv.Close()
	client := &OpenAICompatible{Endpoint: srv.URL, APIKey: "synthetic-key"}
	_, err := client.ListModels(context.Background())
	if err == nil {
		t.Fatal("expected an error for an HTML catalogue")
	}
	if !errors.Is(err, ErrNoModelListing) {
		t.Fatalf("an HTML catalogue must wrap ErrNoModelListing, got %v", err)
	}
	for _, want := range []string{"catalogue unavailable", "HTTP 200", "text/html", "HTML", "by hand"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error should contain %q: %v", want, err)
		}
	}
	if strings.Contains(err.Error(), "invalid character") {
		t.Errorf("the error must not be a raw JSON parsing failure: %v", err)
	}
	if strings.Contains(err.Error(), "synthetic-key") {
		t.Errorf("the error must not carry the credential: %v", err)
	}
}

func TestAnthropicListingWithHTMLReportsCatalogueUnavailable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("<html>not a catalogue</html>"))
	}))
	defer srv.Close()
	client := &AnthropicCompatible{Endpoint: srv.URL, APIKey: "synthetic-key"}
	_, err := client.ListModels(context.Background())
	if err == nil {
		t.Fatal("expected an error for an HTML catalogue")
	}
	if !errors.Is(err, ErrNoModelListing) {
		t.Fatalf("an HTML catalogue must wrap ErrNoModelListing, got %v", err)
	}
	for _, want := range []string{"catalogue unavailable", "HTTP 200", "HTML", "by hand"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error should contain %q: %v", want, err)
		}
	}
	if strings.Contains(err.Error(), "invalid character") {
		t.Errorf("the error must not be a raw JSON parsing failure: %v", err)
	}
}

func TestListingWithValidCatalogueStillWorks(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"deepseek-flash"},{"id":"deepseek-v4-pro"}]}`))
	}))
	defer srv.Close()
	client := &OpenAICompatible{Endpoint: srv.URL, APIKey: "k"}
	models, err := client.ListModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 2 || models[0].ID != "deepseek-flash" {
		t.Fatalf("models = %#v", models)
	}
}

func TestListingWithoutDiscoveryStillReportsNoListing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv.Close()
	client := &OpenAICompatible{Endpoint: srv.URL, APIKey: "k"}
	_, err := client.ListModels(context.Background())
	if !errors.Is(err, ErrNoModelListing) {
		t.Fatalf("a 404 must be ErrNoModelListing, got %v", err)
	}
}

func TestListingWithAuthFailureStillReportsAuth(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no", http.StatusUnauthorized)
	}))
	defer srv.Close()
	client2 := &OpenAICompatible{Endpoint: srv.URL, APIKey: "bad-key"}
	_, err := client2.ListModels(context.Background())
	if err == nil || !strings.Contains(err.Error(), "authentication failed") {
		t.Fatalf("a 401 must report authentication failure, got %v", err)
	}
	if errors.Is(err, ErrNoModelListing) {
		t.Fatalf("an auth failure must not look like missing discovery: %v", err)
	}
}
