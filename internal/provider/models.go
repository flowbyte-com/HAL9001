package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Model listing.
//
// A listing is bounded twice: the response body is read through a
// limit, and the number of models and pages is capped. A hostile or
// broken endpoint cannot make HAL read an unbounded body or follow an
// unbounded number of pages.
//
// The identifiers that come back are the endpoint's own. HAL keeps them
// exactly as they are.

const (
	// maxModelListBytes bounds one model-listing response body.
	maxModelListBytes = 4 << 20
	// maxDiscoveredModels bounds how many models one listing returns.
	maxDiscoveredModels = 500
	// maxModelListPages bounds how many pages one listing follows.
	maxModelListPages = 20
	// modelListPageSize is the page size HAL asks for.
	modelListPageSize = 100
)

// contextKeys are the field names endpoints use to advertise a context
// window. The list is short and documented: HAL reads what an endpoint
// reports and never guesses a number from a model identifier.
var contextKeys = []string{
	"context_length",
	"context_window",
	"max_context_length",
	"max_model_len",
	"max_input_tokens",
	"n_ctx",
}

// outputKeys are the field names endpoints use to advertise an output
// cap.
var outputKeys = []string{
	"max_output_tokens",
	"max_completion_tokens",
	"output_token_limit",
}

// advertisedContext reads the context and output limits an endpoint
// reports for one model. A server that reports none leaves both at
// zero, which means "unknown" and never "zero".
func advertisedContext(entry map[string]any) (contextWindow int, maxOutput int) {
	for _, key := range contextKeys {
		if v := intField(entry, key); v > 0 {
			contextWindow = v
			break
		}
	}
	for _, key := range outputKeys {
		if v := intField(entry, key); v > 0 {
			maxOutput = v
			break
		}
	}
	return contextWindow, maxOutput
}

// stringField reads a string field from a decoded object.
func stringField(entry map[string]any, key string) string {
	switch v := entry[key].(type) {
	case string:
		return v
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(v)
	}
	return ""
}

// intField reads a numeric field from a decoded object. A number that
// arrived as a string is accepted, because endpoints are inconsistent
// about quoting their limits.
func intField(entry map[string]any, key string) int {
	switch v := entry[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	case json.Number:
		n, err := v.Int64()
		if err != nil {
			return 0
		}
		return int(n)
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil {
			return 0
		}
		return n
	}
	return 0
}

// truncateText shortens a provider error body for an error message.
func truncateText(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return "no response body"
	}
	if len(text) > 300 {
		return text[:300] + "…"
	}
	return text
}

// modelFetchedAt is the timestamp discovery stamps on a record.
func modelFetchedAt() time.Time { return time.Now() }

// --- OpenAI-compatible listing ----------------------------------------

// catalogueError reports an endpoint that answered but did not return a
// usable model list.
//
// The endpoint is reachable: the failure is in discovery, not in
// connectivity. The status, the content type and a truncated snippet of
// what came back are reported so the operator can tell a proxy error
// page from a provider that simply does not implement discovery. The
// credential never appears: it travels only in a header and is never
// read back. The error wraps ErrNoModelListing so a caller that only
// needs to know "reachable, but no catalogue" can test for it, while a
// caller that shows the message gets the actionable detail. A model can
// always be added by hand.
func catalogueError(status int, contentType string, body []byte) error {
	ctype := strings.TrimSpace(contentType)
	if ctype == "" {
		ctype = "unknown content type"
	} else if semi := strings.Index(ctype, ";"); semi >= 0 {
		ctype = strings.TrimSpace(ctype[:semi])
	}
	snippet := strings.TrimSpace(string(body))
	if snippet == "" {
		snippet = "empty body"
	} else if len(snippet) > 200 {
		snippet = snippet[:200] + "…"
	}
	// A snippet that could forge a terminal row is flattened: it is
	// evidence, not a row.
	snippet = strings.ReplaceAll(snippet, "\n", " ")
	snippet = strings.ReplaceAll(snippet, "\r", " ")
	what := "did not return a model list"
	if strings.Contains(strings.ToLower(ctype), "html") || strings.HasPrefix(strings.TrimSpace(string(body)), "<") {
		what = "returned HTML instead of a model list"
	}
	return fmt.Errorf("provider: catalogue unavailable (HTTP %d, %s): the endpoint answered but %s (%s); add the model id by hand: %w",
		status, ctype, what, snippet, ErrNoModelListing)
}

// ListModels implements ModelCatalog for an OpenAI-compatible endpoint.
func (o *OpenAICompatible) ListModels(ctx context.Context) ([]ModelInfo, error) {
	listURL := strings.TrimRight(o.Endpoint, "/") + "/models"
	ctx, cancel := context.WithTimeout(ctx, DefaultDiscoveryTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, listURL, nil)
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
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusMethodNotAllowed {
		return nil, ErrNoModelListing
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("provider: %s", readErrorBody(resp))
	}
	contentType := resp.Header.Get("Content-Type")
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxModelListBytes))
	if err != nil {
		return nil, fmt.Errorf("provider: %w", err)
	}
	var listing struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(raw, &listing); err != nil {
		return nil, catalogueError(resp.StatusCode, contentType, raw)
	}
	return modelInfos(listing.Data), nil
}

// modelInfos turns raw listing entries into model records, keeping the
// identifiers exactly as the endpoint wrote them.
//
// What the endpoint says is untrusted: an identifier that could forge a
// terminal row or that is not an identifier at all is refused, a
// display label is sanitised, and a repeated identifier is listed once.
func modelInfos(entries []map[string]any) []ModelInfo {
	out := make([]ModelInfo, 0, len(entries))
	seen := make(map[string]bool, len(entries))
	for _, entry := range entries {
		info, ok := modelRecord(entry, stringField(entry, "id"))
		if !ok {
			continue
		}
		if seen[info.ID] {
			continue
		}
		seen[info.ID] = true
		out = append(out, info)
		if len(out) >= maxDiscoveredModels {
			break
		}
	}
	return out
}

// modelRecord builds one model record from a raw listing entry.
//
// It reports false when the identifier the entry carries is not usable:
// a malformed or unsafe identifier is refused, never repaired.
func modelRecord(entry map[string]any, rawID string) (ModelInfo, bool) {
	id, ok := SafeModelID(rawID)
	if !ok {
		return ModelInfo{}, false
	}
	contextWindow, maxOutput := advertisedContext(entry)
	return ModelInfo{
		ID:              id,
		Label:           SafeDisplayText(stringField(entry, "display_name"), MaxModelLabelLength),
		ContextWindow:   contextWindow,
		MaxOutputTokens: maxOutput,
		Access:          AccessUnknown,
	}, true
}

// --- Anthropic listing ------------------------------------------------

// ListModels implements ModelCatalog for an Anthropic-compatible
// endpoint.
//
// The Messages API lists models with cursor pagination: the response
// says whether more models follow, and the last identifier of the page
// is the cursor for the next one. HAL follows the cursor while it is
// bounded by the page and model caps.
func (a *AnthropicCompatible) ListModels(ctx context.Context) ([]ModelInfo, error) {
	ctx, cancel := context.WithTimeout(ctx, DefaultDiscoveryTimeout)
	defer cancel()
	out := make([]ModelInfo, 0, modelListPageSize)
	after := ""
	for page := 0; page < maxModelListPages; page++ {
		listURL := a.modelsURL()
		query := url.Values{}
		query.Set("limit", strconv.Itoa(modelListPageSize))
		if after != "" {
			query.Set("after_id", after)
		}
		listURL += "?" + query.Encode()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, listURL, nil)
		if err != nil {
			return nil, fmt.Errorf("provider: %w", err)
		}
		req.Header.Set("anthropic-version", a.version())
		req.Header.Set("Accept", "application/json")
		a.setCredential(req)
		resp, err := a.client().Do(req)
		if err != nil {
			return nil, fmt.Errorf("provider: %w", err)
		}
		raw, readErr := io.ReadAll(io.LimitReader(resp.Body, maxModelListBytes))
		resp.Body.Close()
		if readErr != nil {
			return nil, fmt.Errorf("provider: %w", readErr)
		}
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			return nil, fmt.Errorf("provider: authentication failed (%d): check the API key", resp.StatusCode)
		}
		if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusMethodNotAllowed {
			return nil, ErrNoModelListing
		}
		contentType := resp.Header.Get("Content-Type")
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return nil, fmt.Errorf("provider: HTTP %d: %s", resp.StatusCode, truncateText(string(raw)))
		}
		var listing struct {
			Data    []map[string]any `json:"data"`
			HasMore bool             `json:"has_more"`
			LastID  string           `json:"last_id"`
		}
		if err := json.Unmarshal(raw, &listing); err != nil {
			return nil, catalogueError(resp.StatusCode, contentType, raw)
		}
		out = append(out, modelInfos(listing.Data)...)
		if len(out) >= maxDiscoveredModels {
			return out[:maxDiscoveredModels], nil
		}
		if !listing.HasMore {
			break
		}
		next := strings.TrimSpace(listing.LastID)
		if next == "" || next == after {
			// A cursor that does not advance would loop forever.
			break
		}
		after = next
	}
	return out, nil
}

// modelsURL is the endpoint of the model listing.
func (a *AnthropicCompatible) modelsURL() string { return a.base() + "/models" }

// messagesURL is the endpoint of one Messages request.
func (a *AnthropicCompatible) messagesURL() string { return a.base() + "/messages" }
