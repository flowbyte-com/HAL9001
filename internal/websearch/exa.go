package websearch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultExaBaseURL    = "https://api.exa.ai"
	DefaultExaTimeout    = 15 * time.Second
	maxExaTimeout        = 45 * time.Second
	maxExaResponseBytes  = 2 * 1024 * 1024
	maxExaResults        = 10
	EnvExaAPIKey         = "EXA_API_KEY"
	EnvExaBaseURL        = "EXA_API_URL"
	EnvExaTimeoutSeconds = "HAL9001_EXA_TIMEOUT_SECONDS"
)

// ExaSettings configures the Exa fallback. Search is the only Exa feature in
// this milestone: no contents endpoint, crawling or agent.
type ExaSettings struct {
	APIKey  string
	BaseURL string
	Timeout time.Duration
}

// ExaSettingsFromEnv reads EXA_API_KEY and the optional base URL/timeout.
func ExaSettingsFromEnv() ExaSettings {
	return ExaSettings{
		APIKey:  strings.TrimSpace(os.Getenv(EnvExaAPIKey)),
		BaseURL: strings.TrimSpace(os.Getenv(EnvExaBaseURL)),
		Timeout: time.Duration(envIntAny(0, EnvExaTimeoutSeconds)) * time.Second,
	}.normalized()
}

func (s ExaSettings) normalized() ExaSettings {
	if strings.TrimSpace(s.BaseURL) == "" {
		s.BaseURL = DefaultExaBaseURL
	}
	s.BaseURL = strings.TrimRight(strings.TrimSpace(s.BaseURL), "/")
	if s.Timeout <= 0 {
		s.Timeout = DefaultExaTimeout
	}
	if s.Timeout > maxExaTimeout {
		s.Timeout = maxExaTimeout
	}
	return s
}

// ExaProvider is the API-based fallback. It uses POST /search with
// highlights and returns normalized results.
type ExaProvider struct {
	settings  ExaSettings
	http      *http.Client
	now       func() time.Time
	configErr error
}

// NewExaProvider builds the provider. A missing key is not a panic: the
// fallback chain skips it.
func NewExaProvider(settings ExaSettings) *ExaProvider {
	p := &ExaProvider{settings: settings.normalized(), now: time.Now}
	if err := validateExaBaseURL(p.settings.BaseURL); err != nil {
		p.configErr = err
	}
	p.http = &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	return p
}

func (p *ExaProvider) Name() string { return ProviderExa }

func (p *ExaProvider) Available() bool {
	return p != nil && strings.TrimSpace(p.settings.APIKey) != "" && p.configErr == nil
}

func (p *ExaProvider) UnavailableReason() string {
	if p == nil {
		return "Exa is not configured"
	}
	if strings.TrimSpace(p.settings.APIKey) == "" {
		return "EXA_API_KEY is not set"
	}
	if p.configErr != nil {
		return "EXA_API_URL is invalid"
	}
	return "Exa is not configured"
}

func validateExaBaseURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return errors.New("invalid Exa base URL")
	}
	if u.User != nil {
		return errors.New("Exa base URL must not contain credentials")
	}
	switch strings.ToLower(u.Scheme) {
	case "https":
		return nil
	case "http":
		host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
		if host == "localhost" {
			return nil
		}
		if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
			return nil
		}
		return errors.New("plain HTTP is only allowed for a loopback Exa endpoint")
	default:
		return errors.New("Exa base URL must use HTTPS")
	}
}

type exaSearchResponse struct {
	Results []struct {
		Title         string   `json:"title"`
		URL           string   `json:"url"`
		PublishedDate string   `json:"publishedDate"`
		Highlights    []string `json:"highlights"`
		Text          string   `json:"text"`
		Summary       string   `json:"summary"`
	} `json:"results"`
	CostDollars struct {
		Total float64 `json:"total"`
	} `json:"costDollars"`
}

func (p *ExaProvider) Search(ctx context.Context, query string, limit int) (SearchResult, error) {
	if !p.Available() {
		return SearchResult{}, &Error{Code: CodeMissingKey, Provider: ProviderExa, Message: p.UnavailableReason()}
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return SearchResult{}, &Error{Code: CodeInvalidArgs, Provider: ProviderExa, Message: "search query is empty"}
	}
	if len([]rune(query)) > 500 {
		return SearchResult{}, &Error{Code: CodeInvalidArgs, Provider: ProviderExa, Message: "search query is too long"}
	}
	if limit <= 0 {
		limit = defaultSearchLimit
	}
	if limit > maxExaResults {
		limit = maxExaResults
	}
	payload := map[string]any{
		"query":      query,
		"numResults": limit,
		"type":       "auto",
		"contents":   map[string]any{"highlights": true},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return SearchResult{}, &Error{Code: CodeInvalidArgs, Provider: ProviderExa, Message: "request could not be encoded"}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	reqCtx, cancel := context.WithTimeout(ctx, p.settings.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, p.settings.BaseURL+"/search", bytes.NewReader(body))
	if err != nil {
		return SearchResult{}, &Error{Code: CodeConfig, Provider: ProviderExa, Message: "Exa endpoint is invalid"}
	}
	req.Header.Set("Authorization", "Bearer "+p.settings.APIKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "HAL9001-websearch/1")

	resp, err := p.http.Do(req)
	if err != nil {
		return SearchResult{}, p.transportError(ctx, err)
	}
	defer resp.Body.Close()
	data, readErr := io.ReadAll(io.LimitReader(resp.Body, maxExaResponseBytes+1))
	if readErr != nil {
		return SearchResult{}, &Error{Code: CodeNetwork, Provider: ProviderExa, Message: "Exa response could not be read", Retryable: true}
	}
	if len(data) > maxExaResponseBytes {
		return SearchResult{}, &Error{Code: CodeTooLarge, Provider: ProviderExa,
			Message: fmt.Sprintf("Exa response exceeded %d bytes", maxExaResponseBytes)}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return SearchResult{}, p.responseError(resp, data)
	}
	var env exaSearchResponse
	if err := json.Unmarshal(data, &env); err != nil {
		return SearchResult{}, &Error{Code: CodeMalformed, Provider: ProviderExa,
			Message: "Exa returned an unreadable response"}
	}
	out := SearchResult{
		Query: query, Provider: ProviderExa, RetrievedAt: p.now().UTC(),
		CostUSD: env.CostDollars.Total,
	}
	for _, item := range env.Results {
		rawURL := strings.TrimSpace(item.URL)
		if rawURL == "" {
			continue
		}
		valid, err := ValidResultURL(rawURL)
		if err != nil {
			continue
		}
		title := strings.TrimSpace(item.Title)
		if title == "" {
			title = valid
		}
		excerpt := ""
		for _, h := range item.Highlights {
			if s := strings.TrimSpace(h); s != "" {
				excerpt = s
				break
			}
		}
		if excerpt == "" {
			excerpt = strings.TrimSpace(item.Summary)
		}
		if excerpt == "" {
			excerpt = strings.TrimSpace(item.Text)
		}
		out.Results = append(out.Results, Hit{
			Position: len(out.Results) + 1, Title: clipRunes(title, 300),
			URL: valid, Excerpt: clipRunes(collapseSpace(excerpt), 1200),
			PublishedAt: clipRunes(item.PublishedDate, 80),
		})
		if len(out.Results) >= limit {
			break
		}
	}
	if len(env.Results) > 0 && len(out.Results) == 0 {
		return SearchResult{}, &Error{Code: CodeMalformed, Provider: ProviderExa,
			Message: "Exa returned results without usable URLs"}
	}
	return out, nil
}

func (p *ExaProvider) transportError(parent context.Context, err error) *Error {
	if parent != nil && parent.Err() == context.Canceled {
		return &Error{Code: CodeCancelled, Provider: ProviderExa, Message: "Exa request was cancelled"}
	}
	if parent != nil && parent.Err() == context.DeadlineExceeded {
		return &Error{Code: CodeTimeout, Provider: ProviderExa, Message: "Exa request timed out"}
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return &Error{Code: CodeTimeout, Provider: ProviderExa, Message: "Exa request timed out"}
	}
	if errors.Is(err, context.Canceled) {
		return &Error{Code: CodeCancelled, Provider: ProviderExa, Message: "Exa request was cancelled"}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return &Error{Code: CodeTimeout, Provider: ProviderExa, Message: "Exa request timed out"}
	}
	return &Error{Code: CodeNetwork, Provider: ProviderExa, Message: "Exa could not be reached", Retryable: true}
}

func (p *ExaProvider) responseError(resp *http.Response, data []byte) *Error {
	e := &Error{Provider: ProviderExa, HTTPStatus: resp.StatusCode}
	detail := exaErrorText(data)
	detail = redact(detail, p.settings.APIKey)
	if detail != "" {
		detail = ": " + detail
	}
	switch resp.StatusCode {
	case http.StatusUnauthorized:
		e.Code = CodeAuth
		e.Message = "Exa authentication failed (401); check EXA_API_KEY"
	case http.StatusPaymentRequired:
		e.Code = CodeCredits
		e.Message = "Exa credits are exhausted (402)"
	case http.StatusForbidden:
		e.Code = CodeForbidden
		e.Message = "Exa refused the request (403)" + detail
	case http.StatusTooManyRequests:
		e.Code = CodeRateLimited
		e.Message = "Exa rate limit reached (429)"
		e.Retryable = true
		e.RetryAfter = retryAfter(resp)
	case http.StatusRequestTimeout:
		e.Code = CodeTimeout
		e.Message = "Exa request timed out (408)"
		e.Retryable = true
	case http.StatusBadRequest, http.StatusUnprocessableEntity:
		e.Code = CodeRejected
		e.Message = fmt.Sprintf("Exa rejected the request (%d)%s", resp.StatusCode, detail)
	default:
		if resp.StatusCode >= 500 {
			e.Code = CodeUpstream
			e.Message = fmt.Sprintf("Exa upstream error (%d)", resp.StatusCode)
			e.Retryable = true
		} else {
			e.Code = CodeRejected
			e.Message = fmt.Sprintf("Exa rejected the request (%d)%s", resp.StatusCode, detail)
		}
	}
	return e
}

func exaErrorText(data []byte) string {
	var env struct {
		Error string `json:"error"`
		Tag   string `json:"tag"`
	}
	if err := json.Unmarshal(data, &env); err != nil {
		return ""
	}
	detail := strings.TrimSpace(env.Error)
	if detail == "" {
		detail = strings.TrimSpace(env.Tag)
	}
	detail = collapseSpace(detail)
	if len([]rune(detail)) > 240 {
		detail = string([]rune(detail)[:240]) + "…"
	}
	return detail
}

func retryAfter(resp *http.Response) time.Duration {
	raw := strings.TrimSpace(resp.Header.Get("Retry-After"))
	if raw == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(raw); err == nil && seconds >= 0 {
		return time.Duration(seconds) * time.Second
	}
	if when, err := http.ParseTime(raw); err == nil {
		if d := time.Until(when); d > 0 {
			return d
		}
	}
	return 0
}
