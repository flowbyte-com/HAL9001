package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A hostile endpoint.
//
// An endpoint can answer a listing with anything at all: escape
// sequences, forged rows, control characters, names that are far too
// long and the same identifier twice. None of that may reach the
// terminal, and none of it may change the identifier HAL sends to the
// provider.

// hostileListing is a listing that tries every trick at once: an
// identifier carrying an escape sequence, one with surrounding
// whitespace, the same identifier twice, one with a newline, one with a
// bell, one that is far too long, and a display name that carries an
// escape sequence and a line break.
//
// The one identifier that must survive untouched is the exact one the
// endpoint wrote.
const hostileListing = `{
  "data": [
    {"id": "good-model", "display_name": "Good Model"},
    {"id": "\u001b[31mred-id"},
    {"id": " padded-id "},
    {"id": "good-model"},
    {"id": "newline-id\nsecond-line"},
    {"id": "bell-id\u0007"},
    {"id": "very-long-id-%s"},
    {"id": "label-hostile", "display_name": "Model\u001b[31mRed\u0007\nSecond line\u001b]8;;http://evil\u0007"},
    {"id": "vendor/Model-V2:latest", "display_name": "Exact Identifier"}
  ],
  "has_more": false
}`

// hostileBody returns the hostile listing with the long identifier
// filled in.
func hostileBody(t *testing.T) string {
	t.Helper()
	return fmt.Sprintf(hostileListing, strings.Repeat("x", MaxModelIDLength+10))
}

func assertNoTerminalSequences(t *testing.T, models []ModelInfo) {
	t.Helper()
	for _, m := range models {
		for _, field := range []string{m.ID, m.Label} {
			if strings.ContainsRune(field, 0x1b) {
				t.Fatalf("an escape reached the listing: %q in %#v", field, m)
			}
			for _, r := range field {
				if r < 0x20 && r != '\t' {
					t.Fatalf("a control character reached the listing: %q in %#v", field, m)
				}
				if r >= 0x80 && r <= 0x9f {
					t.Fatalf("a C1 control reached the listing: %q in %#v", field, m)
				}
			}
			if strings.Contains(field, "\n") || strings.Contains(field, "\r") {
				t.Fatalf("a line break reached the listing: %q in %#v", field, m)
			}
		}
		if len(m.ID) > MaxModelIDLength {
			t.Fatalf("an oversized identifier reached the listing: %q", m.ID)
		}
	}
}

func TestOpenAIListingSurvivesAHostileEndpoint(t *testing.T) {
	body := hostileBody(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, body)
	}))
	defer srv.Close()
	client := &OpenAICompatible{Endpoint: srv.URL, APIKey: "k"}
	models, err := client.ListModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	assertNoTerminalSequences(t, models)
	ids := make([]string, 0, len(models))
	for _, m := range models {
		ids = append(ids, m.ID)
	}
	want := []string{"good-model", "label-hostile", "vendor/Model-V2:latest"}
	if len(ids) != len(want) {
		t.Fatalf("ids = %#v, want %#v", ids, want)
	}
	for i, id := range want {
		if ids[i] != id {
			t.Fatalf("ids = %#v, want %#v", ids, want)
		}
	}
	for _, m := range models {
		if m.ID == "label-hostile" && strings.ContainsAny(m.Label, "\u001b\n") {
			t.Fatalf("the label was not sanitised: %q", m.Label)
		}
	}
}

func TestAnthropicListingSurvivesAHostileEndpoint(t *testing.T) {
	body := hostileBody(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, body)
	}))
	defer srv.Close()
	client := &AnthropicCompatible{Endpoint: srv.URL, APIKey: "k"}
	models, err := client.ListModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	assertNoTerminalSequences(t, models)
	for _, m := range models {
		if m.ID == "good-model" {
			return
		}
	}
	t.Fatalf("the legitimate model was not listed: %#v", models)
}

func TestLocalListingSurvivesAHostileEndpoint(t *testing.T) {
	body := hostileBody(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, body)
	}))
	defer srv.Close()
	local := newLocalRuntime(Endpoint{Wire: WireOpenAI, Category: "local", BaseURL: srv.URL}, Options{})
	models, err := local.ListModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	assertNoTerminalSequences(t, models)
}

func TestAnExactIdentifierIsSentUnchanged(t *testing.T) {
	// The identifier that reaches the provider is the one the endpoint
	// wrote, byte for byte. A sanitiser that rewrote it would send a
	// name the endpoint does not know.
	const id = "vendor/Model-V2:latest"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"data":[{"id":%q}]}`, id)
	}))
	defer srv.Close()
	client := &OpenAICompatible{Endpoint: srv.URL, APIKey: "k"}
	models, err := client.ListModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 1 || models[0].ID != id {
		t.Fatalf("models = %#v", models)
	}
}
