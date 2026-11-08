package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Local runtime probing.
//
// A local endpoint is a model server on the operator's own machine. It
// speaks one of the supported protocols, and it may also report what it
// actually allocated to the model it is serving. That number is the
// local runtime's, not the model's: a server can hold a 200k-token
// model in an 8k-token window, and only the server knows.
//
// The probes are optional by design. A local server that offers no
// metadata simply yields no metadata; HAL then uses the conservative
// provisional budget and says that is what it did.

// localRuntime probes a local model server for its models and for the
// context it allocated.
type localRuntime struct {
	root    string
	key     string
	client  *http.Client
	timeout time.Duration
}

// newLocalRuntime builds the probe for a local endpoint.
func newLocalRuntime(e Endpoint, opts Options) *localRuntime {
	client := opts.Client
	if client == nil {
		client = newHTTPClient()
	}
	timeout := opts.IdleTimeout
	if timeout <= 0 {
		timeout = DefaultDiscoveryTimeout
	}
	return &localRuntime{
		root:    localRoot(e.BaseURL),
		key:     strings.TrimSpace(e.Credential),
		client:  client,
		timeout: timeout,
	}
}

// localRoot returns the server root: the base URL without a trailing
// slash and without a version segment, so the server's own metadata
// endpoints are reachable whether or not the operator included /v1.
func localRoot(base string) string {
	root := strings.TrimRight(strings.TrimSpace(base), "/")
	if strings.HasSuffix(root, "/v1") {
		root = strings.TrimSuffix(root, "/v1")
	}
	return root
}

// get performs one bounded GET against the local server.
func (l *localRuntime) get(ctx context.Context, path string) ([]byte, int, error) {
	ctx, cancel := context.WithTimeout(ctx, l.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, l.root+path, nil)
	if err != nil {
		return nil, 0, fmt.Errorf("provider: %w", err)
	}
	if l.key != "" {
		req.Header.Set("Authorization", "Bearer "+l.key)
	}
	resp, err := l.client.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("provider: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxModelListBytes))
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("provider: %w", err)
	}
	return raw, resp.StatusCode, nil
}

// post performs one bounded POST against the local server.
func (l *localRuntime) post(ctx context.Context, path string, body any) ([]byte, int, error) {
	ctx, cancel := context.WithTimeout(ctx, l.timeout)
	defer cancel()
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, 0, fmt.Errorf("provider: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, l.root+path, bytes.NewReader(payload))
	if err != nil {
		return nil, 0, fmt.Errorf("provider: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if l.key != "" {
		req.Header.Set("Authorization", "Bearer "+l.key)
	}
	resp, err := l.client.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("provider: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxModelListBytes))
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("provider: %w", err)
	}
	return raw, resp.StatusCode, nil
}

// ListModels asks the local server which models it serves.
//
// Three conventions are tried, in the order a local server is most
// likely to answer: the OpenAI-style listing, the runtime's own tag
// listing, and the runtime's model objects. A server that answers none
// of them has no listing, and the operator adds model identifiers by
// hand.
func (l *localRuntime) ListModels(ctx context.Context) ([]ModelInfo, error) {
	if models, err := l.listOpenAI(ctx); err == nil && len(models) > 0 {
		return models, nil
	}
	if models, err := l.listTags(ctx); err == nil && len(models) > 0 {
		return models, nil
	}
	if models, err := l.listRuntimeModels(ctx); err == nil && len(models) > 0 {
		return models, nil
	}
	return nil, ErrNoModelListing
}

// listOpenAI reads the OpenAI-style listing of a local server.
func (l *localRuntime) listOpenAI(ctx context.Context) ([]ModelInfo, error) {
	raw, status, err := l.get(ctx, "/models")
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("provider: HTTP %d", status)
	}
	var listing struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(raw, &listing); err != nil {
		return nil, fmt.Errorf("provider: bad model list: %w", err)
	}
	out := make([]ModelInfo, 0, len(listing.Data))
	for _, entry := range listing.Data {
		info, ok := modelRecord(entry, stringField(entry, "id"))
		if !ok {
			continue
		}
		out = append(out, info)
		if len(out) >= maxDiscoveredModels {
			break
		}
	}
	return out, nil
}

// listTags reads the runtime's own tag listing, the convention used by
// local runtimes that serve models under their own names.
func (l *localRuntime) listTags(ctx context.Context) ([]ModelInfo, error) {
	raw, status, err := l.get(ctx, "/api/tags")
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("provider: HTTP %d", status)
	}
	var listing struct {
		Models []map[string]any `json:"models"`
	}
	if err := json.Unmarshal(raw, &listing); err != nil {
		return nil, fmt.Errorf("provider: bad model list: %w", err)
	}
	out := make([]ModelInfo, 0, len(listing.Models))
	for _, entry := range listing.Models {
		raw := stringField(entry, "name")
		if raw == "" {
			raw = stringField(entry, "model")
		}
		info, ok := modelRecord(entry, raw)
		if !ok {
			continue
		}
		out = append(out, info)
		if len(out) >= maxDiscoveredModels {
			break
		}
	}
	return out, nil
}

// listRuntimeModels reads a runtime's model objects, the convention
// used by local servers that publish one object per loaded model.
func (l *localRuntime) listRuntimeModels(ctx context.Context) ([]ModelInfo, error) {
	raw, status, err := l.get(ctx, "/api/v0/models")
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("provider: HTTP %d", status)
	}
	var listing struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(raw, &listing); err != nil {
		return nil, fmt.Errorf("provider: bad model list: %w", err)
	}
	out := make([]ModelInfo, 0, len(listing.Data))
	for _, entry := range listing.Data {
		raw := stringField(entry, "id")
		if raw == "" {
			raw = stringField(entry, "model")
		}
		info, ok := modelRecord(entry, raw)
		if !ok {
			continue
		}
		out = append(out, info)
		if len(out) >= maxDiscoveredModels {
			break
		}
	}
	return out, nil
}

// ContextFor asks the local runtime what context it allocated for a
// model.
//
// The runtime's own answer is authoritative for a local endpoint: it is
// the window the model is actually being served with. HAL never
// substitutes the model's advertised maximum for it, and it never
// generates a huge request to find out.
func (l *localRuntime) ContextFor(ctx context.Context, modelID string) (ContextMetadata, error) {
	if strings.TrimSpace(modelID) == "" {
		return ContextMetadata{}, fmt.Errorf("provider: a model id is required")
	}
	raw, status, err := l.post(ctx, "/api/show", map[string]any{"model": modelID})
	if err == nil && status >= 200 && status < 300 {
		if window := showContextLength(raw); window > 0 {
			return ContextMetadata{
				ContextWindow: window,
				Source:        ContextSourceLocal,
				Mode:          ContextModeLocal,
				Verified:      true,
				FetchedAt:     modelFetchedAt(),
				Note:          "the local runtime reported the context it allocated",
			}, nil
		}
	}
	models, err := l.ListModels(ctx)
	if err != nil {
		return ContextMetadata{}, ErrNoModelListing
	}
	for _, m := range models {
		if m.ID != modelID {
			continue
		}
		if m.ContextWindow > 0 {
			return ContextMetadata{
				ContextWindow:   m.ContextWindow,
				MaxOutputTokens: m.MaxOutputTokens,
				Source:          ContextSourceLocal,
				Mode:            ContextModeLocal,
				Verified:        true,
				FetchedAt:       modelFetchedAt(),
				Note:            "the local runtime reported the context it allocated",
			}, nil
		}
	}
	return ContextMetadata{}, ErrNoModelListing
}

// showContextLength reads the context length from a runtime's model
// report. The report names the architecture's fields, so the key is
// matched by its suffix rather than by an exact name.
func showContextLength(raw []byte) int {
	var report struct {
		ModelInfo map[string]any `json:"model_info"`
	}
	if err := json.Unmarshal(raw, &report); err != nil {
		return 0
	}
	for key, value := range report.ModelInfo {
		if !strings.HasSuffix(strings.ToLower(key), "context_length") {
			continue
		}
		if n := intField(map[string]any{"v": value}, "v"); n > 0 {
			return n
		}
	}
	return 0
}
