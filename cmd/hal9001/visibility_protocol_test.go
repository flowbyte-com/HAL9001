package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/flowbyte-com/HAL9001/internal/config"
)

// The visibility of the reasoning is the operator's, and it changes
// nothing the provider sees.
//
// The same two turns, with the reasoning shown and with it hidden,
// reach the provider in exactly the same shape: the request, the
// replayed blocks and the record are the same either way.
//
// The system prompt is left out of the comparison: it names the
// workspace, and the two runs use two workspaces.
func TestTheVisibilityDoesNotChangeTheProtocol(t *testing.T) {
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		bodies = append(bodies, body)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"msg_1","type":"message","role":"assistant",`+
			`"content":[{"type":"thinking","thinking":"the model weighed it"},{"type":"text","text":"the answer"}],`+
			`"model":"deepseek-flash","stop_reason":"end_turn"}`)
	}))
	defer srv.Close()

	var reference []map[string]any
	for _, show := range []bool{false, true} {
		bodies = nil
		a := effortApp(t, srv.URL, config.ProtocolAnthropic,
			config.ModelRecord{ID: "deepseek-flash", Effort: "high"})
		a.stored.ShowThinking = show
		if _, err := a.eng.RespondStream(context.Background(), "hi", nil); err != nil {
			t.Fatal(err)
		}
		if _, err := a.eng.RespondStream(context.Background(), "and again", nil); err != nil {
			t.Fatal(err)
		}
		if len(bodies) != 2 {
			t.Fatalf("the provider was asked %d times", len(bodies))
		}
		// The second turn carries the thinking of the first one back.
		conversation, _ := bodies[1]["messages"].([]any)
		var blocks []any
		for _, raw := range conversation {
			message, _ := raw.(map[string]any)
			if message["role"] != "assistant" {
				continue
			}
			blocks, _ = message["content"].([]any)
			break
		}
		if len(blocks) != 2 {
			t.Fatalf("the replayed turn = %+v", conversation)
		}
		block, _ := blocks[0].(map[string]any)
		if block["type"] != "thinking" || block["thinking"] != "the model weighed it" {
			t.Errorf("the replayed block = %+v", block)
		}
		if reference == nil {
			reference = protocolOf(bodies)
			continue
		}
		if !reflect.DeepEqual(reference, protocolOf(bodies)) {
			t.Errorf("the visibility changed the protocol:\n%+v\n%+v", reference, protocolOf(bodies))
		}
	}
}

// protocolOf keeps the parts of a request the provider reads, and
// leaves out the system prompt, which names the workspace.
func protocolOf(bodies []map[string]any) []map[string]any {
	out := make([]map[string]any, 0, len(bodies))
	for _, body := range bodies {
		copied := map[string]any{}
		for key, value := range body {
			if key == "system" {
				continue
			}
			copied[key] = value
		}
		out = append(out, copied)
	}
	return out
}
