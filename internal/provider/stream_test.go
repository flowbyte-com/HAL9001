package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

// miniMaxServer streams raw SSE lines with a configurable delay between
// them, imitating a MiniMax-style reasoning model: a long reasoning phase
// first, then visible content.
func miniMaxServer(t *testing.T, lines []string, gap time.Duration) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		for _, line := range lines {
			fmt.Fprint(w, line+"\n\n")
			flusher.Flush()
			if gap > 0 {
				select {
				case <-time.After(gap):
				case <-r.Context().Done():
					return
				}
			}
		}
	}))
}

func reasoningLine(text string) string {
	raw, _ := json.Marshal(text)
	return `data: {"choices":[{"delta":{"reasoning_content":` + string(raw) + `},"index":0}]}`
}

func contentLine(text string) string {
	raw, _ := json.Marshal(text)
	return `data: {"choices":[{"delta":{"content":` + string(raw) + `},"index":0}]}`
}

const finishLine = `data: {"choices":[{"delta":{},"finish_reason":"stop","index":0}]}`
const usageLine = `data: {"choices":[],"usage":{"prompt_tokens":120,"completion_tokens":340,"total_tokens":460,"completion_tokens_details":{"reasoning_tokens":300}}}`
const doneLine = `data: [DONE]`

// The streaming request asks for usage. Without stream_options most
// providers never send any usage object, which is exactly how a finished
// turn ends up with zero totals.
func TestStreamingRequestAsksForUsage(t *testing.T) {
	var captured capturedRequest
	srv := streamServer(t, []string{contentLine("ok")}, &captured)
	defer srv.Close()

	p := &OpenAICompatible{Endpoint: srv.URL, APIKey: "k"}
	if _, err := p.ChatStream(context.Background(),
		[]Message{{Role: "user", Content: "x"}}, Model{ID: "m"}, func(Delta) error { return nil }); err != nil {
		t.Fatal(err)
	}
	opts, ok := captured.body["stream_options"].(map[string]any)
	if !ok || opts["include_usage"] != true {
		t.Errorf("stream_options = %v, want {include_usage:true}", captured.body["stream_options"])
	}

	// The non-streaming request carries no stream options.
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		captured.body = body
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`)
	}))
	defer srv2.Close()
	p2 := &OpenAICompatible{Endpoint: srv2.URL, APIKey: "k"}
	if _, err := p2.Chat(context.Background(),
		[]Message{{Role: "user", Content: "x"}}, Model{ID: "m"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := captured.body["stream_options"]; ok {
		t.Errorf("non-streaming request must not carry stream_options: %v", captured.body)
	}
}

// Usage that arrives after finish_reason but before [DONE] is preserved,
// and it is not mistaken for answer text.
func TestUsageAfterFinishReason(t *testing.T) {
	srv := miniMaxServer(t, []string{
		reasoningLine("private thought"),
		contentLine("An answer."),
		finishLine,
		usageLine,
		doneLine,
	}, 0)
	defer srv.Close()

	var texts []string
	var reasoning int
	p := &OpenAICompatible{Endpoint: srv.URL, APIKey: "k"}
	resp, err := p.ChatStream(context.Background(),
		[]Message{{Role: "user", Content: "x"}}, Model{ID: "m"},
		func(d Delta) error {
			texts = append(texts, d.Text)
			if d.Reasoning != "" {
				reasoning++
			}
			return nil
		})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(texts, "") != "An answer." {
		t.Errorf("usage leaked into the answer: %q", texts)
	}
	if reasoning != 1 {
		t.Errorf("reasoning events = %d, want 1", reasoning)
	}
	if !resp.Stats.HasUsage || resp.Stats.Usage.TotalTokens != 460 {
		t.Errorf("usage lost: %+v", resp.Stats)
	}
	if resp.Stats.FinishReason != "stop" || resp.Stats.OutputLimitHit {
		t.Errorf("finish mishandled: %+v", resp.Stats)
	}
}

// A second usage frame supersedes the first: providers send cumulative
// snapshots, so only the last one may be kept.
func TestUsageSnapshotsDoNotAccumulate(t *testing.T) {
	usage := func(prompt, completion int) string {
		return `data: {"choices":[],"usage":{"prompt_tokens":` + strconv.Itoa(prompt) +
			`,"completion_tokens":` + strconv.Itoa(completion) +
			`,"total_tokens":` + strconv.Itoa(prompt+completion) + `}}`
	}
	srv := miniMaxServer(t, []string{
		contentLine("ok"),
		usage(100, 200),
		usage(120, 340),
		doneLine,
	}, 0)
	defer srv.Close()

	p := &OpenAICompatible{Endpoint: srv.URL, APIKey: "k"}
	resp, err := p.ChatStream(context.Background(),
		[]Message{{Role: "user", Content: "x"}}, Model{ID: "m"}, func(Delta) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Stats.HasUsage {
		t.Fatal("usage missing")
	}
	if resp.Stats.Usage.PromptTokens != 120 || resp.Stats.Usage.CompletionTokens != 340 {
		t.Errorf("expected the last snapshot, got %+v", resp.Stats.Usage)
	}
}

// Explicitly reported zeros are kept and marked reported.
func TestExplicitZeroUsage(t *testing.T) {
	srv := miniMaxServer(t, []string{
		contentLine("ok"),
		`data: {"choices":[],"usage":{"prompt_tokens":0,"completion_tokens":0,"total_tokens":0}}`,
		doneLine,
	}, 0)
	defer srv.Close()

	p := &OpenAICompatible{Endpoint: srv.URL, APIKey: "k"}
	resp, err := p.ChatStream(context.Background(),
		[]Message{{Role: "user", Content: "x"}}, Model{ID: "m"}, func(Delta) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Stats.HasUsage {
		t.Error("explicit zero usage must be marked reported, not unknown")
	}
	if resp.Stats.Usage.PromptTokens != 0 || resp.Stats.Usage.CompletionTokens != 0 {
		t.Errorf("zeros not retained: %+v", resp.Stats.Usage)
	}
}

// A MiniMax-style turn: reasoning events arrive first and are invisible.
// The diagnostics must show the reasoning presence, the time to first
// visible output, and the usage totals.
func TestChatStreamReasoningThenContentStats(t *testing.T) {
	srv := miniMaxServer(t, []string{
		`: keep-alive`,
		reasoningLine("private thought one"),
		reasoningLine("private thought two"),
		contentLine("The answer "),
		contentLine("is 42."),
		finishLine,
		usageLine,
		doneLine,
	}, 2*time.Millisecond)
	defer srv.Close()

	p := &OpenAICompatible{Endpoint: srv.URL, APIKey: "k"}
	var reasoningEvents int
	var text strings.Builder
	resp, err := p.ChatStream(context.Background(),
		[]Message{{Role: "user", Content: "think hard"}}, Model{ID: "MiniMax-M3.1-Flash"},
		func(d Delta) error {
			if d.Reasoning != "" {
				reasoningEvents++
			}
			text.WriteString(d.Text)
			return nil
		})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Content != "The answer is 42." {
		t.Errorf("content = %q", resp.Content)
	}
	if reasoningEvents != 2 {
		t.Errorf("reasoning events = %d, want 2", reasoningEvents)
	}
	s := resp.Stats
	if s.Status != StatusDone {
		t.Errorf("status = %q, want done", s.Status)
	}
	if s.ReasoningChunks != 2 || s.VisibleChunks != 2 {
		t.Errorf("chunks = reasoning %d visible %d", s.ReasoningChunks, s.VisibleChunks)
	}
	if s.KeepAlives != 1 {
		t.Errorf("keep-alives = %d, want 1", s.KeepAlives)
	}
	if s.FirstReasoning <= 0 || s.FirstVisible < s.FirstReasoning {
		t.Errorf("first reasoning %v, first visible %v", s.FirstReasoning, s.FirstVisible)
	}
	if !s.HasUsage || s.Usage.TotalTokens != 460 || s.Usage.ReasoningTokens != 300 {
		t.Errorf("usage = %+v", s.Usage)
	}
	if s.FinishReason != "stop" {
		t.Errorf("finish = %q", s.FinishReason)
	}
	if s.OutputLimitHit {
		t.Error("stop must not be reported as an output limit")
	}
	if s.Duration <= 0 {
		t.Error("duration must be measured")
	}
}

// finish_reason "length" must be recorded as a truncated reply, not as a
// normal completion.
func TestChatStreamOutputLimit(t *testing.T) {
	srv := miniMaxServer(t, []string{
		contentLine("cut off"),
		`data: {"choices":[{"delta":{},"finish_reason":"length"}]}`,
		doneLine,
	}, 0)
	defer srv.Close()

	p := &OpenAICompatible{Endpoint: srv.URL, APIKey: "k"}
	resp, err := p.ChatStream(context.Background(),
		[]Message{{Role: "user", Content: "x"}}, Model{ID: "m"}, func(Delta) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Stats.OutputLimitHit {
		t.Error("finish_reason length must set OutputLimitHit")
	}
	if resp.Stats.FinishReason != "length" {
		t.Errorf("finish = %q", resp.Stats.FinishReason)
	}
}

// A stream that ends without [DONE] is an EOF, not a silent success.
func TestChatStreamEOFWithoutDone(t *testing.T) {
	srv := miniMaxServer(t, []string{contentLine("partial")}, 0)
	defer srv.Close()

	p := &OpenAICompatible{Endpoint: srv.URL, APIKey: "k"}
	resp, err := p.ChatStream(context.Background(),
		[]Message{{Role: "user", Content: "x"}}, Model{ID: "m"}, func(Delta) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if resp.Stats.Status != StatusEOF {
		t.Errorf("status = %q, want eof", resp.Stats.Status)
	}
}

// Malformed data lines are counted and skipped, never fatal.
func TestChatStreamCountsMalformed(t *testing.T) {
	srv := miniMaxServer(t, []string{
		`data: {not json`,
		contentLine("ok"),
		doneLine,
	}, 0)
	defer srv.Close()

	p := &OpenAICompatible{Endpoint: srv.URL, APIKey: "k"}
	resp, err := p.ChatStream(context.Background(),
		[]Message{{Role: "user", Content: "x"}}, Model{ID: "m"}, func(Delta) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if resp.Stats.Malformed != 1 {
		t.Errorf("malformed = %d, want 1", resp.Stats.Malformed)
	}
	if resp.Content != "ok" {
		t.Errorf("content = %q", resp.Content)
	}
}

// A provider that sends headers and then nothing must be cut by the idle
// watchdog, quickly and with a classifiable error.
func TestChatStreamIdleTimeout(t *testing.T) {
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		select {
		case <-block:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(block)

	p := &OpenAICompatible{Endpoint: srv.URL, APIKey: "k", IdleTimeout: 150 * time.Millisecond}
	start := time.Now()
	_, err := p.ChatStream(context.Background(),
		[]Message{{Role: "user", Content: "x"}}, Model{ID: "m"}, func(Delta) error { return nil })
	elapsed := time.Since(start)
	if !errors.Is(err, ErrStreamIdle) {
		t.Fatalf("err = %v, want ErrStreamIdle", err)
	}
	if elapsed > 2*time.Second {
		t.Errorf("idle watchdog took %v", elapsed)
	}
}

// Cancelling the caller's context must close the request promptly and
// report a cancellation, not a provider error.
func TestChatStreamCancellationClosesRequest(t *testing.T) {
	closed := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		fmt.Fprint(w, contentLine("one")+"\n\n")
		flusher.Flush()
		select {
		case <-r.Context().Done():
			close(closed)
		case <-time.After(5 * time.Second):
		}
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	p := &OpenAICompatible{Endpoint: srv.URL, APIKey: "k"}
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	_, err := p.ChatStream(ctx,
		[]Message{{Role: "user", Content: "x"}}, Model{ID: "m"}, func(Delta) error { return nil })
	elapsed := time.Since(start)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if elapsed > 2*time.Second {
		t.Errorf("cancellation took %v", elapsed)
	}
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Error("the server never saw the request close")
	}
}

// After finish_reason the provider may keep the socket open. The stream
// must end at the finish grace, not at the much longer idle timeout.
func TestChatStreamFinishGraceClosesPromptly(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		fmt.Fprint(w, contentLine("done")+"\n\n")
		fmt.Fprint(w, finishLine+"\n\n")
		flusher.Flush()
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(release)

	p := &OpenAICompatible{Endpoint: srv.URL, APIKey: "k", IdleTimeout: 10 * time.Second}
	start := time.Now()
	resp, err := p.ChatStream(context.Background(),
		[]Message{{Role: "user", Content: "x"}}, Model{ID: "m"}, func(Delta) error { return nil })
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if resp.Stats.Status != StatusFinished {
		t.Errorf("status = %q, want finished", resp.Stats.Status)
	}
	if elapsed > 6*time.Second {
		t.Errorf("finish grace took %v", elapsed)
	}
}

// Every request carries an output cap: the built-in default or the
// model's own limit.
func TestRequestAlwaysCarriesOutputCap(t *testing.T) {
	var captured capturedRequest
	srv := streamServer(t, []string{contentLine("x")}, &captured)
	defer srv.Close()

	p := &OpenAICompatible{Endpoint: srv.URL, APIKey: "k"}
	if _, err := p.ChatStream(context.Background(),
		[]Message{{Role: "user", Content: "x"}}, Model{ID: "m"}, func(Delta) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if got, ok := captured.body["max_tokens"].(float64); !ok || int(got) != 15000 {
		t.Errorf("default max_tokens on the wire = %v, want 15000", captured.body["max_tokens"])
	}

	p2 := &OpenAICompatible{Endpoint: srv.URL, APIKey: "k", DefaultMaxTokens: 1234}
	if _, err := p2.ChatStream(context.Background(),
		[]Message{{Role: "user", Content: "x"}}, Model{ID: "m"}, func(Delta) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if got, ok := captured.body["max_tokens"].(float64); !ok || int(got) != 1234 {
		t.Errorf("configured max_tokens = %v, want 1234", captured.body["max_tokens"])
	}

	if _, err := p.ChatStream(context.Background(),
		[]Message{{Role: "user", Content: "x"}}, Model{ID: "m", MaxTokens: 77}, func(Delta) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if got, ok := captured.body["max_tokens"].(float64); !ok || int(got) != 77 {
		t.Errorf("model max_tokens = %v, want 77", captured.body["max_tokens"])
	}
}

// The turn ceiling closes a stream that keeps trickling: the idle
// watchdog never fires because data keeps arriving, so only the total
// limit bounds a runaway turn.
func TestChatStreamTurnTimeoutWhileTrickling(t *testing.T) {
	stop := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			case <-r.Context().Done():
				return
			case <-time.After(20 * time.Millisecond):
			}
			fmt.Fprint(w, contentLine("tick")+"\n\n")
			flusher.Flush()
		}
	}))
	defer srv.Close()
	defer close(stop)

	p := &OpenAICompatible{
		Endpoint:    srv.URL,
		APIKey:      "k",
		IdleTimeout: 5 * time.Second, // never fires: data keeps coming
		TurnTimeout: 250 * time.Millisecond,
	}
	start := time.Now()
	_, err := p.ChatStream(context.Background(),
		[]Message{{Role: "user", Content: "x"}}, Model{ID: "m"}, func(Delta) error { return nil })
	elapsed := time.Since(start)
	if !errors.Is(err, ErrTurnTimeout) {
		t.Fatalf("err = %v, want ErrTurnTimeout", err)
	}
	if elapsed > 3*time.Second {
		t.Errorf("turn ceiling took %v", elapsed)
	}
}

// The operator's defaults are part of the contract: 15000 output tokens, a
// 45-second idle window, a 120-second turn ceiling, a 3-second finish
// grace.
func TestDefaultSafeguards(t *testing.T) {
	if DefaultMaxTokens != 15000 {
		t.Errorf("DefaultMaxTokens = %d, want 15000", DefaultMaxTokens)
	}
	if DefaultIdleTimeout != 45*time.Second {
		t.Errorf("DefaultIdleTimeout = %v, want 45s", DefaultIdleTimeout)
	}
	if DefaultTurnTimeout != 240*time.Second {
		t.Errorf("DefaultTurnTimeout = %v, want 240s", DefaultTurnTimeout)
	}
	if finishGrace != 3*time.Second {
		t.Errorf("finishGrace = %v, want 3s", finishGrace)
	}

	// A zero-valued provider resolves to exactly those defaults.
	p := &OpenAICompatible{}
	if got := p.maxTokens(); got != 15000 {
		t.Errorf("resolved max tokens = %d", got)
	}
	if got := p.idleTimeout(); got != 45*time.Second {
		t.Errorf("resolved idle timeout = %v", got)
	}
	if got := p.turnTimeout(); got != 240*time.Second {
		t.Errorf("resolved turn timeout = %v", got)
	}
}

// Any data from the provider resets the idle watchdog: a stream that keeps
// producing events for longer than the idle window is never cut.
func TestIdleWatchdogResetsOnActivity(t *testing.T) {
	const (
		idle = 400 * time.Millisecond
		gap  = 100 * time.Millisecond
	)
	var lines []string
	for i := 0; i < 10; i++ {
		lines = append(lines, contentLine("tick "))
	}
	lines = append(lines, doneLine)
	srv := miniMaxServer(t, lines, gap)
	defer srv.Close()

	p := &OpenAICompatible{Endpoint: srv.URL, APIKey: "k", IdleTimeout: idle}
	start := time.Now()
	resp, err := p.ChatStream(context.Background(),
		[]Message{{Role: "user", Content: "x"}}, Model{ID: "m"}, func(Delta) error { return nil })
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("a busy stream must not hit the idle timeout: %v", err)
	}
	if elapsed < 2*idle {
		t.Fatalf("test ran %v, shorter than the idle window; it proves nothing", elapsed)
	}
	if resp.Stats.Status != StatusDone {
		t.Errorf("status = %q", resp.Stats.Status)
	}
	if resp.Content != strings.Repeat("tick ", 10) {
		t.Errorf("content = %q", resp.Content)
	}
}

// A reasoning phase that keeps trickling output is still bounded by the
// turn ceiling: the idle watchdog never fires, so only the ceiling can
// stop it.
func TestTurnTimeoutDuringReasoningTrickle(t *testing.T) {
	stop := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		for {
			select {
			case <-stop:
				return
			case <-r.Context().Done():
				return
			case <-time.After(20 * time.Millisecond):
			}
			fmt.Fprint(w, reasoningLine("private thought")+"\n\n")
			flusher.Flush()
		}
	}))
	defer srv.Close()
	defer close(stop)

	p := &OpenAICompatible{
		Endpoint:    srv.URL,
		APIKey:      "k",
		IdleTimeout: 5 * time.Second, // never fires: reasoning keeps coming
		TurnTimeout: 250 * time.Millisecond,
	}
	start := time.Now()
	resp, err := p.ChatStream(context.Background(),
		[]Message{{Role: "user", Content: "x"}}, Model{ID: "m"}, func(Delta) error { return nil })
	elapsed := time.Since(start)
	if !errors.Is(err, ErrTurnTimeout) {
		t.Fatalf("err = %v, want ErrTurnTimeout", err)
	}
	if elapsed > 3*time.Second {
		t.Errorf("turn ceiling took %v", elapsed)
	}
	if resp.Stats.Status != StatusTimeout {
		t.Errorf("status = %q, want timeout", resp.Stats.Status)
	}
	if resp.Stats.ReasoningChunks == 0 {
		t.Error("the stream produced no reasoning events; the test proves nothing")
	}
}

// The configured output limit is the one sent, and /diag's stats report
// the same value.
func TestConfiguredLimitIsSentAndReported(t *testing.T) {
	var captured capturedRequest
	srv := streamServer(t, []string{contentLine("ok")}, &captured)
	defer srv.Close()

	p := &OpenAICompatible{Endpoint: srv.URL, APIKey: "k", DefaultMaxTokens: 2048,
		IdleTimeout: 30 * time.Second, TurnTimeout: 90 * time.Second}
	resp, err := p.ChatStream(context.Background(),
		[]Message{{Role: "user", Content: "x"}}, Model{ID: "m"}, func(Delta) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := captured.body["max_tokens"].(float64); !ok || int(got) != 2048 {
		t.Errorf("sent max_tokens = %v, want 2048", captured.body["max_tokens"])
	}
	if resp.Stats.MaxTokens != 2048 {
		t.Errorf("reported max tokens = %d, want 2048", resp.Stats.MaxTokens)
	}
	if resp.Stats.IdleTimeout != 30*time.Second || resp.Stats.TurnTimeout != 90*time.Second {
		t.Errorf("reported limits = idle %v turn %v", resp.Stats.IdleTimeout, resp.Stats.TurnTimeout)
	}
}

// A default provider reports the default limits in its stats.
func TestDefaultLimitsReportedInStats(t *testing.T) {
	srv := miniMaxServer(t, []string{contentLine("ok"), doneLine}, 0)
	defer srv.Close()

	p := &OpenAICompatible{Endpoint: srv.URL, APIKey: "k"}
	resp, err := p.ChatStream(context.Background(),
		[]Message{{Role: "user", Content: "x"}}, Model{ID: "m"}, func(Delta) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if resp.Stats.MaxTokens != 15000 {
		t.Errorf("max tokens = %d, want 15000", resp.Stats.MaxTokens)
	}
	if resp.Stats.IdleTimeout != 45*time.Second {
		t.Errorf("idle = %v, want 45s", resp.Stats.IdleTimeout)
	}
	if resp.Stats.TurnTimeout != 240*time.Second {
		t.Errorf("turn = %v, want 240s", resp.Stats.TurnTimeout)
	}
}

// An operator override still wins over the 15,000 default, in both
// directions.
func TestMaxTokensOverrideWinsOverDefault(t *testing.T) {
	var captured capturedRequest
	srv := streamServer(t, []string{contentLine("x")}, &captured)
	defer srv.Close()

	for _, want := range []int{512, 30000} {
		p := &OpenAICompatible{Endpoint: srv.URL, APIKey: "k", DefaultMaxTokens: want}
		if _, err := p.ChatStream(context.Background(),
			[]Message{{Role: "user", Content: "x"}}, Model{ID: "m"}, func(Delta) error { return nil }); err != nil {
			t.Fatal(err)
		}
		if got, ok := captured.body["max_tokens"].(float64); !ok || int(got) != want {
			t.Errorf("override max_tokens = %v, want %d", captured.body["max_tokens"], want)
		}
	}
}
