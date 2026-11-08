package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The local runtime.
//
// A local server may report the context it actually allocated to the
// model it serves. That number is the runtime's, not the model's, and
// it is the budget in force: a server can hold a 200k-token model in an
// 8k-token window, and only the server knows which one it is.

// localFixture is a recording local model server.
type localFixture struct {
	t      *testing.T
	server *httptest.Server
	paths  []string
}

func newLocalFixture(t *testing.T, handler http.HandlerFunc) *localFixture {
	t.Helper()
	f := &localFixture{t: t}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.paths = append(f.paths, r.URL.Path)
		handler(w, r)
	}))
	t.Cleanup(f.server.Close)
	return f
}

func TestLocalRuntimeFindsTheOpenAIStyleListing(t *testing.T) {
	f := newLocalFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, `{"data":[{"id":"llama3"},{"id":"qwen2"}]}`)
	})
	d, err := NewDiscoverer(Endpoint{Wire: WireOpenAI, Category: "local", BaseURL: f.server.URL}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	models, err := d.ListModels(context.Background(), DiscoveryOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 2 || models[0].ID != "llama3" {
		t.Fatalf("models = %#v", models)
	}
}

func TestLocalRuntimeFindsTheRuntimeTagListing(t *testing.T) {
	f := newLocalFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/tags" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, `{"models":[{"name":"llama3:latest"},{"name":"qwen2:7b"}]}`)
	})
	d, err := NewDiscoverer(Endpoint{Wire: WireOpenAI, Category: "local", BaseURL: f.server.URL}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	models, err := d.ListModels(context.Background(), DiscoveryOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 2 || models[0].ID != "llama3:latest" {
		t.Fatalf("models = %#v", models)
	}
}

func TestLocalRuntimeReportsTheAllocatedContext(t *testing.T) {
	f := newLocalFixture(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/show":
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			if body["model"] != "llama3" {
				t.Errorf("show asked for %#v", body["model"])
			}
			fmt.Fprint(w, `{"model_info":{"llama.context_length":8192,"llama.attention.head_count":32}}`)
		default:
			http.NotFound(w, r)
		}
	})
	d, err := NewDiscoverer(Endpoint{Wire: WireOpenAI, Category: "local", BaseURL: f.server.URL}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	meta, err := d.ContextFor(context.Background(), "llama3")
	if err != nil {
		t.Fatal(err)
	}
	if meta.ContextWindow != 8192 {
		t.Errorf("window = %d", meta.ContextWindow)
	}
	if meta.Source != ContextSourceLocal || meta.Mode != ContextModeLocal {
		t.Errorf("meta = %#v", meta)
	}
	if !meta.Verified {
		t.Error("a number the runtime reported is verified")
	}
}

func TestLocalRuntimeKeepsTheAdvertisedMaximumApart(t *testing.T) {
	f := newLocalFixture(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/models":
			fmt.Fprint(w, `{"data":[{"id":"llama3","context_length":200000}]}`)
		case "/api/show":
			fmt.Fprint(w, `{"model_info":{"llama.context_length":8192}}`)
		default:
			http.NotFound(w, r)
		}
	})
	d, err := NewDiscoverer(Endpoint{Wire: WireOpenAI, Category: "local", BaseURL: f.server.URL}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	meta, err := d.ContextFor(context.Background(), "llama3")
	if err != nil {
		t.Fatal(err)
	}
	if meta.ContextWindow != 8192 {
		t.Errorf("window = %d, the allocation is the budget in force", meta.ContextWindow)
	}
	if meta.MaxContextWindow != 200000 {
		t.Errorf("the advertised maximum must be kept apart: %d", meta.MaxContextWindow)
	}
}

func TestLocalRuntimeWithoutMetadataIsProvisional(t *testing.T) {
	f := newLocalFixture(t, func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	})
	d, err := NewDiscoverer(Endpoint{Wire: WireOpenAI, Category: "local", BaseURL: f.server.URL}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	meta, err := d.ContextFor(context.Background(), "llama3")
	if err != nil {
		t.Fatal(err)
	}
	if meta.Source != ContextSourceProvisional || meta.Verified {
		t.Errorf("meta = %#v", meta)
	}
	if meta.ContextWindow != ProvisionalContextWindow {
		t.Errorf("window = %d", meta.ContextWindow)
	}
}

func TestLocalRuntimeUsesTheRootWithoutTheVersionSegment(t *testing.T) {
	f := newLocalFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/tags" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, `{"models":[{"name":"llama3"}]}`)
	})
	d, err := NewDiscoverer(Endpoint{Wire: WireOpenAI, Category: "local", BaseURL: f.server.URL + "/v1"}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	models, err := d.ListModels(context.Background(), DiscoveryOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 1 {
		t.Fatalf("models = %#v", models)
	}
	for _, path := range f.paths {
		if strings.Contains(path, "/v1/api") {
			t.Errorf("the version segment was doubled: %v", f.paths)
		}
	}
}
