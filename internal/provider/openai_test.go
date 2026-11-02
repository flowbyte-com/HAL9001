package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type capturedRequest struct {
	header http.Header
	body   map[string]any
}

func streamServer(t *testing.T, chunks []string, captured *capturedRequest) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if captured != nil {
			captured.header = r.Header.Clone()
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decoding request: %v", err)
			}
			captured.body = body
		}
		if !strings.HasSuffix(r.URL.Path, "/chat/completions") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Error("streaming unsupported")
			return
		}
		for _, chunk := range chunks {
			fmt.Fprintf(w, "data: %s\n\n", chunk)
			flusher.Flush()
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
}

func TestChatStreamAssemblesDeltas(t *testing.T) {
	var captured capturedRequest
	srv := streamServer(t, []string{
		`{"choices":[{"delta":{"content":"Hello"}}]}`,
		`{"choices":[{"delta":{"content":" there"}}]}`,
		`{"choices":[{"delta":{},"finish_reason":"stop"}]}`,
	}, &captured)
	defer srv.Close()

	client := &OpenAICompatible{Endpoint: srv.URL, APIKey: "k"}
	var texts []string
	resp, err := client.ChatStream(context.Background(),
		[]Message{{Role: "user", Content: "hi"}}, Model{ID: "m"},
		func(d Delta) error { texts = append(texts, d.Text); return nil })
	if err != nil {
		t.Fatal(err)
	}
	if resp.Content != "Hello there" {
		t.Errorf("assembled = %q", resp.Content)
	}
	if strings.Join(texts, "") != "Hello there" {
		t.Errorf("deltas = %v", texts)
	}
	if resp.FinishReason != "stop" {
		t.Errorf("finish = %q", resp.FinishReason)
	}
	if captured.body["model"] != "m" {
		t.Errorf("model not forwarded: %v", captured.body)
	}
	if captured.body["stream"] != true {
		t.Errorf("stream flag missing: %v", captured.body)
	}
	if _, ok := captured.body["reasoning_effort"]; ok {
		t.Error("reasoning_effort must not be sent to a model that does not document it")
	}
	if _, ok := captured.body["thinking"]; ok {
		t.Error("no thinking parameter may be sent")
	}
	if got := captured.header.Get("Authorization"); got != "Bearer k" {
		t.Errorf("auth header = %q", got)
	}
	msgs, _ := captured.body["messages"].([]any)
	if len(msgs) != 1 {
		t.Fatalf("messages = %v", captured.body["messages"])
	}
}

func TestChatStreamSeparatesReasoning(t *testing.T) {
	srv := streamServer(t, []string{
		`{"choices":[{"delta":{"reasoning_content":"because","content":"Hi"}}]}`,
	}, nil)
	defer srv.Close()

	client := &OpenAICompatible{Endpoint: srv.URL, APIKey: "k"}
	var sawReasoning bool
	resp, err := client.ChatStream(context.Background(),
		[]Message{{Role: "user", Content: "hi"}}, Model{ID: "m"},
		func(d Delta) error {
			if d.Reasoning != "" {
				sawReasoning = true
			}
			if d.Text == "because" {
				t.Error("reasoning must not arrive as text")
			}
			return nil
		})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Content != "Hi" {
		t.Errorf("content = %q, reasoning must stay separate", resp.Content)
	}
	if resp.Reasoning != "because" {
		t.Errorf("reasoning not captured: %q", resp.Reasoning)
	}
	if !sawReasoning {
		t.Error("reasoning delta never emitted")
	}
}

func TestChatNonStreaming(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"choices":[{"message":{"content":"done","reasoning_content":"r"},"finish_reason":"stop"}]}`)
	}))
	defer srv.Close()

	client := &OpenAICompatible{Endpoint: srv.URL, APIKey: "k"}
	resp, err := client.Chat(context.Background(), []Message{{Role: "user", Content: "hi"}}, Model{ID: "m"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Content != "done" || resp.Reasoning != "r" || resp.FinishReason != "stop" {
		t.Errorf("unexpected response: %+v", resp)
	}
}

func TestChatAuthFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"error":{"message":"bad key","type":"auth"}}`)
	}))
	defer srv.Close()

	client := &OpenAICompatible{Endpoint: srv.URL, APIKey: "wrong"}
	_, err := client.Chat(context.Background(), []Message{{Role: "user", Content: "hi"}}, Model{ID: "m"})
	if err == nil || !strings.Contains(err.Error(), "authentication failed") {
		t.Errorf("expected an auth error, got %v", err)
	}
}

func TestChatServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		fmt.Fprint(w, `{"error":{"message":"upstream down"}}`)
	}))
	defer srv.Close()

	client := &OpenAICompatible{Endpoint: srv.URL, APIKey: "k"}
	_, err := client.Chat(context.Background(), []Message{{Role: "user", Content: "hi"}}, Model{ID: "m"})
	if err == nil || !strings.Contains(err.Error(), "502") || !strings.Contains(err.Error(), "upstream down") {
		t.Errorf("expected a status error, got %v", err)
	}
}

func TestChatStreamCancellation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		for i := 0; i < 100; i++ {
			fmt.Fprintf(w, "data: %s\n\n", `{"choices":[{"delta":{"content":"x"}}]}`)
			flusher.Flush()
			time.Sleep(20 * time.Millisecond)
		}
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	client := &OpenAICompatible{Endpoint: srv.URL, APIKey: "k"}
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()
	_, err := client.ChatStream(ctx, []Message{{Role: "user", Content: "hi"}}, Model{ID: "m"},
		func(Delta) error { return nil })
	if err == nil {
		t.Fatal("expected cancellation to abort the stream")
	}
}

func TestChatTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	client := &OpenAICompatible{Endpoint: srv.URL, APIKey: "k"}
	_, err := client.Chat(ctx, []Message{{Role: "user", Content: "hi"}}, Model{ID: "m"})
	if err == nil || !(errors.Is(err, context.DeadlineExceeded) || strings.Contains(err.Error(), "deadline")) {
		t.Errorf("expected a timeout error, got %v", err)
	}
}

func TestChatMalformedChunksSkipped(t *testing.T) {
	srv := streamServer(t, []string{
		`not json at all`,
		`{"choices":[{"delta":{"content":"ok"}}]}`,
	}, nil)
	defer srv.Close()

	client := &OpenAICompatible{Endpoint: srv.URL, APIKey: "k"}
	resp, err := client.ChatStream(context.Background(), []Message{{Role: "user", Content: "hi"}}, Model{ID: "m"},
		func(Delta) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if resp.Content != "ok" {
		t.Errorf("content = %q, malformed chunks must be skipped", resp.Content)
	}
}

func TestModelsListsIDs(t *testing.T) {
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		fmt.Fprint(w, `{"data":[{"id":"a"},{"id":"b"},{"id":""}]}`)
	}))
	defer srv.Close()

	client := &OpenAICompatible{Endpoint: srv.URL, APIKey: "k"}
	ids, err := client.Models(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 || ids[0] != "a" || ids[1] != "b" {
		t.Errorf("ids = %v", ids)
	}
	if auth != "Bearer k" {
		t.Errorf("auth header = %q", auth)
	}
}

func TestModelsAuthFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	client := &OpenAICompatible{Endpoint: srv.URL, APIKey: "bad"}
	if _, err := client.Models(context.Background()); err == nil ||
		!strings.Contains(err.Error(), "authentication failed") {
		t.Errorf("expected an auth error, got %v", err)
	}
}
