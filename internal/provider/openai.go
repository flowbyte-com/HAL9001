package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Delta is one streaming unit. Text is user-facing content; Reasoning is
// provider thinking metadata (for example a reasoning_content field) and
// must never be displayed as the answer.
type Delta struct {
	Text         string
	Reasoning    string
	FinishReason string
	Done         bool
}

// Streamer is optionally implemented by providers with SSE support.
type Streamer interface {
	ChatStream(ctx context.Context, messages []Message, model Model, emit func(Delta) error) (Response, error)
}

// OpenAICompatible talks to any OpenAI-style /chat/completions endpoint
// (custom base URL, bearer key, model id). It sends no reasoning or
// thinking parameters: adapters must only use parameters the API
// documents, and thinking controls are out of scope for this milestone.
type OpenAICompatible struct {
	Endpoint string
	APIKey   string
	Client   *http.Client
}

// Name implements Provider.
func (o *OpenAICompatible) Name() string { return "openai-compatible" }

func (o *OpenAICompatible) client() *http.Client {
	if o.Client != nil {
		return o.Client
	}
	return &http.Client{Timeout: 120 * time.Second}
}

type openAIMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type openAIRequest struct {
	Model     string          `json:"model"`
	Messages  []openAIMessage `json:"messages"`
	Stream    bool            `json:"stream"`
	MaxTokens *int            `json:"max_tokens,omitempty"`
}

type openAIChoice struct {
	Delta *struct {
		Content          *string `json:"content"`
		ReasoningContent *string `json:"reasoning_content"`
	} `json:"delta,omitempty"`
	Message *struct {
		Content          *string `json:"content"`
		ReasoningContent *string `json:"reasoning_content"`
	} `json:"message,omitempty"`
	FinishReason *string `json:"finish_reason,omitempty"`
}

type openAIResponse struct {
	Choices []openAIChoice `json:"choices"`
	Error   *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    any    `json:"code"`
	} `json:"error,omitempty"`
}

func (o *OpenAICompatible) requestBody(messages []Message, model Model, stream bool) ([]byte, error) {
	out := openAIRequest{Model: model.ID, Stream: stream}
	for _, m := range messages {
		out.Messages = append(out.Messages, openAIMessage{Role: m.Role, Content: m.Content})
	}
	if model.MaxTokens > 0 {
		out.MaxTokens = &model.MaxTokens
	}
	return json.Marshal(out)
}

func (o *OpenAICompatible) post(ctx context.Context, body []byte, stream bool) (*http.Response, error) {
	url := strings.TrimRight(o.Endpoint, "/") + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("provider: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if o.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+o.APIKey)
	}
	if stream {
		req.Header.Set("Accept", "text/event-stream")
	}
	resp, err := o.client().Do(req)
	if err != nil {
		return nil, fmt.Errorf("provider: %w", err)
	}
	return resp, nil
}

// readErrorBody returns a bounded, credential-free error description and
// closes the body.
func readErrorBody(resp *http.Response) string {
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	var parsed openAIResponse
	if err := json.Unmarshal(raw, &parsed); err == nil && parsed.Error != nil && parsed.Error.Message != "" {
		return fmt.Sprintf("HTTP %d: %s", resp.StatusCode, parsed.Error.Message)
	}
	text := strings.TrimSpace(string(raw))
	if text == "" {
		text = resp.Status
	}
	if len(text) > 300 {
		text = text[:300] + "…"
	}
	return fmt.Sprintf("HTTP %d: %s", resp.StatusCode, text)
}

// Chat implements Provider with a single non-streaming request.
func (o *OpenAICompatible) Chat(ctx context.Context, messages []Message, model Model) (Response, error) {
	body, err := o.requestBody(messages, model, false)
	if err != nil {
		return Response{}, err
	}
	resp, err := o.post(ctx, body, false)
	if err != nil {
		return Response{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return Response{}, fmt.Errorf("provider: authentication failed (%d): check the API key", resp.StatusCode)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Response{}, fmt.Errorf("provider: %s", readErrorBody(resp))
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return Response{}, fmt.Errorf("provider: %w", err)
	}
	var parsed openAIResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return Response{}, fmt.Errorf("provider: bad response: %w", err)
	}
	if parsed.Error != nil && parsed.Error.Message != "" {
		return Response{}, fmt.Errorf("provider: %s", parsed.Error.Message)
	}
	if len(parsed.Choices) == 0 {
		return Response{}, fmt.Errorf("provider: empty response (no choices)")
	}
	out := Response{}
	if parsed.Choices[0].Message != nil {
		if parsed.Choices[0].Message.Content != nil {
			out.Content = *parsed.Choices[0].Message.Content
		}
		if parsed.Choices[0].Message.ReasoningContent != nil {
			out.Reasoning = *parsed.Choices[0].Message.ReasoningContent
		}
	}
	if parsed.Choices[0].FinishReason != nil {
		out.FinishReason = *parsed.Choices[0].FinishReason
	}
	return out, nil
}

// Models probes the provider's model catalogue with a short timeout. It
// powers the onboarding connection test: success means the endpoint and
// key work, and the count tells the operator models are reachable. The
// key travels only in the Authorization header, never into errors or logs.
func (o *OpenAICompatible) Models(ctx context.Context) ([]string, error) {
	url := strings.TrimRight(o.Endpoint, "/") + "/models"
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("provider: %w", err)
	}
	if o.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+o.APIKey)
	}
	resp, err := o.client().Do(req)
	if err != nil {
		return nil, fmt.Errorf("provider: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("provider: authentication failed (%d): check the API key", resp.StatusCode)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("provider: %s", readErrorBody(resp))
	}
	var listing struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("provider: %w", err)
	}
	if err := json.Unmarshal(raw, &listing); err != nil {
		return nil, fmt.Errorf("provider: bad model list: %w", err)
	}
	ids := make([]string, 0, len(listing.Data))
	for _, m := range listing.Data {
		if strings.TrimSpace(m.ID) != "" {
			ids = append(ids, m.ID)
		}
	}
	return ids, nil
}

// ChatStream implements Streamer using server-sent events.
func (o *OpenAICompatible) ChatStream(ctx context.Context, messages []Message, model Model, emit func(Delta) error) (Response, error) {
	body, err := o.requestBody(messages, model, true)
	if err != nil {
		return Response{}, err
	}
	resp, err := o.post(ctx, body, true)
	if err != nil {
		return Response{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return Response{}, fmt.Errorf("provider: authentication failed (%d): check the API key", resp.StatusCode)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Response{}, fmt.Errorf("provider: %s", readErrorBody(resp))
	}
	var out Response
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		if ctx.Err() != nil {
			return Response{}, fmt.Errorf("provider: %w", ctx.Err())
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, ":") {
			continue
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			break
		}
		var chunk openAIResponse
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue // skip malformed keep-alive chunks, don't fail the turn
		}
		if chunk.Error != nil && chunk.Error.Message != "" {
			return Response{}, fmt.Errorf("provider: %s", chunk.Error.Message)
		}
		if len(chunk.Choices) == 0 || chunk.Choices[0].Delta == nil {
			continue
		}
		delta := Delta{}
		if c := chunk.Choices[0].Delta.Content; c != nil {
			delta.Text = *c
			out.Content += *c
		}
		if r := chunk.Choices[0].Delta.ReasoningContent; r != nil {
			delta.Reasoning = *r
			out.Reasoning += *r
		}
		if chunk.Choices[0].FinishReason != nil {
			delta.FinishReason = *chunk.Choices[0].FinishReason
			out.FinishReason = *chunk.Choices[0].FinishReason
		}
		if delta.Text != "" || delta.Reasoning != "" {
			if err := emit(delta); err != nil {
				return Response{}, err
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return Response{}, fmt.Errorf("provider: stream read: %w", err)
	}
	return out, nil
}
