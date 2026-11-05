// Package websearch is HAL9001's unified live web-search surface.
//
// One model-facing tool, web_search, is backed by independent providers in a
// fixed order: Firecrawl, Exa, then DuckDuckGo Lite. The model never chooses a
// provider. A provider result is normalized into the same small shape, and the
// transcript records which provider actually answered.
//
// Every provider is read-only. This package has no permission controller,
// shell, filesystem writer or MPM client, so a search result or a scraped page
// cannot elevate authority or cause a privileged action.
package websearch

import (
	"context"
	"errors"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/flowbyte-com/HAL9001/internal/firecrawl"
)

// Provider names are stable labels for the transcript, ledger and diagnostics.
const (
	ProviderFirecrawl  = "firecrawl"
	ProviderExa        = "exa"
	ProviderDuckDuckGo = "duckduckgo"
)

// Error codes are content-free labels shared by every provider.
const (
	CodeMissingKey  = "missing_key"
	CodeConfig      = "configuration"
	CodeAuth        = "authentication"
	CodeForbidden   = "forbidden"
	CodeCredits     = "credits_exhausted"
	CodeRateLimited = "rate_limited"
	CodeTimeout     = "timeout"
	CodeCancelled   = "cancelled"
	CodeUpstream    = "upstream"
	CodeRejected    = "request_rejected"
	CodeMalformed   = "malformed_response"
	CodeTooLarge    = "response_too_large"
	CodeBlocked     = "blocked"
	CodeLayout      = "layout_changed"
	CodeInvalidURL  = "invalid_url"
	CodeInvalidArgs = "invalid_arguments"
	CodeNetwork     = "network"
	CodeQuota       = "quota"
)

// Error is one provider or fallback failure. Message is safe to show to the
// model and never contains an API key.
type Error struct {
	Code       string
	Provider   string
	HTTPStatus int
	Message    string
	Retryable  bool
	RetryAfter time.Duration
	Attempts   []Attempt
}

func (e *Error) Error() string {
	if e == nil {
		return "web search failed"
	}
	msg := strings.TrimSpace(e.Message)
	if msg == "" {
		msg = "web search failed"
	}
	if len(e.Attempts) > 0 {
		var parts []string
		for _, a := range e.Attempts {
			if a.Status == "ok" {
				continue
			}
			detail := a.Provider
			if a.Code != "" {
				detail += ": " + a.Code
			}
			parts = append(parts, detail)
		}
		if len(parts) > 0 {
			msg += " (attempts: " + strings.Join(parts, ", ") + ")"
		}
	}
	return msg
}

// Summary is the short, content-free detail shown beside the tool name.
func (e *Error) Summary() string {
	if e == nil {
		return "failed"
	}
	switch e.Code {
	case CodeMissingKey:
		return "not configured"
	case CodeConfig:
		return "misconfigured"
	case CodeAuth:
		return "authentication failed"
	case CodeForbidden:
		return "forbidden"
	case CodeCredits:
		return "credits exhausted"
	case CodeRateLimited:
		return "rate limited"
	case CodeTimeout:
		return "timed out"
	case CodeCancelled:
		return "cancelled"
	case CodeUpstream:
		return "upstream error"
	case CodeRejected:
		return "request rejected"
	case CodeMalformed:
		return "malformed response"
	case CodeTooLarge:
		return "response too large"
	case CodeBlocked:
		return "blocked"
	case CodeLayout:
		return "layout changed"
	case CodeInvalidURL:
		return "invalid URL"
	case CodeInvalidArgs:
		return "invalid arguments"
	case CodeNetwork:
		return "network error"
	case CodeQuota:
		return "limit reached"
	default:
		return "failed"
	}
}

// Fallbackable reports whether trying the next provider is useful. A
// cancellation or an invalid request stops the chain; a credential, quota,
// rate limit, blocking page or transient outage moves to the next provider.
func (e *Error) Fallbackable() bool {
	if e == nil {
		return true
	}
	switch e.Code {
	case CodeCancelled, CodeInvalidArgs, CodeQuota, CodeInvalidURL:
		return false
	default:
		return true
	}
}

// Hit is one normalized search result.
type Hit struct {
	Position    int    `json:"position"`
	Title       string `json:"title"`
	URL         string `json:"url"`
	Excerpt     string `json:"excerpt"`
	PublishedAt string `json:"published_at,omitempty"`
}

// SearchResult is one normalized search result set.
type SearchResult struct {
	Query       string    `json:"query"`
	Provider    string    `json:"provider"`
	Results     []Hit     `json:"results"`
	RetrievedAt time.Time `json:"retrieved_at"`
	CreditsUsed int       `json:"credits_used,omitempty"`
	CostUSD     float64   `json:"cost_usd,omitempty"`
	Attempts    []Attempt `json:"attempts,omitempty"`
}

// Attempt records one provider in the fallback chain. It is safe for the
// transcript and diagnostics: no query, page content or credential.
type Attempt struct {
	Provider string `json:"provider"`
	Status   string `json:"status"` // ok, failed, skipped
	Code     string `json:"code,omitempty"`
	Summary  string `json:"summary,omitempty"`
}

// Provider is one independent search backend.
type Provider interface {
	Name() string
	Available() bool
	UnavailableReason() string
	Search(ctx context.Context, query string, limit int) (SearchResult, error)
}

// Searcher is the fallback chain the registry calls.
type Searcher interface {
	Search(ctx context.Context, query string, limit int) (SearchResult, error)
	Available() bool
	Describe() string
}

// Scraper is the single-page reader. Firecrawl remains the only scraper in
// this milestone.
type Scraper interface {
	ValidatePageURL(ctx context.Context, pageURL string) (string, error)
	Scrape(ctx context.Context, pageURL string) (firecrawl.ScrapeResult, error)
	Available() bool
	UnavailableReason() string
}

// Settings are the provider-neutral request and context budgets. They reuse
// the phase-1 environment names when the new names are absent.
type Settings struct {
	SearchLimit        int
	MaxSearchesPerTurn int
	MaxScrapesPerTurn  int
	MaxSearchBytes     int
	MaxScrapeBytes     int
	MaxTurnBytes       int
}

const (
	defaultSearchLimit        = 5
	maxSearchLimit            = 10
	defaultMaxSearchesPerTurn = 3
	maxSearchesPerTurn        = 5
	defaultMaxScrapesPerTurn  = 2
	maxScrapesPerTurn         = 5
	defaultMaxSearchBytes     = 12 * 1024
	defaultMaxScrapeBytes     = 24 * 1024
	defaultMaxTurnBytes       = 64 * 1024
	minMaxTurnBytes           = 8 * 1024
	maxMaxTurnBytes           = 256 * 1024
)

// SettingsFromEnv resolves the unified budgets. New names take precedence;
// the phase-1 Firecrawl names remain compatible.
func SettingsFromEnv() Settings {
	return Settings{
		SearchLimit:        envIntAny(defaultSearchLimit, "HAL9001_WEB_SEARCH_LIMIT", "HAL9001_FIRECRAWL_SEARCH_LIMIT"),
		MaxSearchesPerTurn: envIntAny(defaultMaxSearchesPerTurn, "HAL9001_WEB_MAX_SEARCHES", "HAL9001_FIRECRAWL_MAX_SEARCHES"),
		MaxScrapesPerTurn:  envIntAny(defaultMaxScrapesPerTurn, "HAL9001_WEB_MAX_SCRAPES", "HAL9001_FIRECRAWL_MAX_SCRAPES"),
		MaxTurnBytes:       envIntAny(defaultMaxTurnBytes, "HAL9001_WEB_MAX_TURN_BYTES", "HAL9001_FIRECRAWL_MAX_TURN_BYTES"),
	}.normalized()
}

func (s Settings) normalized() Settings {
	if s.SearchLimit <= 0 {
		s.SearchLimit = defaultSearchLimit
	}
	if s.SearchLimit > maxSearchLimit {
		s.SearchLimit = maxSearchLimit
	}
	if s.MaxSearchesPerTurn <= 0 {
		s.MaxSearchesPerTurn = defaultMaxSearchesPerTurn
	}
	if s.MaxSearchesPerTurn > maxSearchesPerTurn {
		s.MaxSearchesPerTurn = maxSearchesPerTurn
	}
	if s.MaxScrapesPerTurn <= 0 {
		s.MaxScrapesPerTurn = defaultMaxScrapesPerTurn
	}
	if s.MaxScrapesPerTurn > maxScrapesPerTurn {
		s.MaxScrapesPerTurn = maxScrapesPerTurn
	}
	if s.MaxTurnBytes < minMaxTurnBytes {
		s.MaxTurnBytes = defaultMaxTurnBytes
	}
	if s.MaxTurnBytes > maxMaxTurnBytes {
		s.MaxTurnBytes = maxMaxTurnBytes
	}
	if s.MaxSearchBytes <= 0 {
		s.MaxSearchBytes = defaultMaxSearchBytes
	}
	if s.MaxScrapeBytes <= 0 {
		s.MaxScrapeBytes = defaultMaxScrapeBytes
	}
	if s.MaxSearchBytes > s.MaxTurnBytes {
		s.MaxSearchBytes = s.MaxTurnBytes
	}
	if s.MaxScrapeBytes > s.MaxTurnBytes {
		s.MaxScrapeBytes = s.MaxTurnBytes
	}
	return s
}

// Fallback tries each provider in order. A valid empty result is success and
// stops the chain. Missing credentials, exhausted credits, rate limits,
// blocking pages and transient failures move to the next provider.
type Fallback struct {
	providers []Provider
}

// NewFallback builds the ordered chain.
func NewFallback(providers ...Provider) *Fallback {
	return &Fallback{providers: append([]Provider(nil), providers...)}
}

// Available reports whether any provider can be attempted.
func (f *Fallback) Available() bool {
	if f == nil {
		return false
	}
	for _, p := range f.providers {
		if p.Available() {
			return true
		}
	}
	return false
}

// Describe lists the providers and their availability for /diag.
func (f *Fallback) Describe() string {
	if f == nil || len(f.providers) == 0 {
		return "no search providers"
	}
	parts := make([]string, 0, len(f.providers))
	for _, p := range f.providers {
		state := "ready"
		if !p.Available() {
			state = "unavailable"
			if reason := strings.TrimSpace(p.UnavailableReason()); reason != "" {
				state += " (" + reason + ")"
			}
		}
		parts = append(parts, p.Name()+"="+state)
	}
	return strings.Join(parts, " · ")
}

// Search runs the fallback chain.
func (f *Fallback) Search(ctx context.Context, query string, limit int) (SearchResult, error) {
	if f == nil || len(f.providers) == 0 {
		return SearchResult{}, &Error{Code: CodeMissingKey, Message: "no web search provider is configured"}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var attempts []Attempt
	var last *Error
	for _, p := range f.providers {
		if err := ctx.Err(); err != nil {
			e := contextError(err)
			e.Attempts = attempts
			return SearchResult{}, e
		}
		if !p.Available() {
			attempts = append(attempts, Attempt{
				Provider: p.Name(), Status: "skipped", Code: CodeMissingKey,
				Summary: "not configured",
			})
			continue
		}
		res, err := p.Search(ctx, query, limit)
		if err == nil {
			res.Provider = p.Name()
			res.Attempts = append(attempts, Attempt{Provider: p.Name(), Status: "ok"})
			return res, nil
		}
		var pe *Error
		if !errors.As(err, &pe) {
			pe = &Error{Code: CodeUpstream, Provider: p.Name(), Message: "provider search failed"}
		}
		if pe.Provider == "" {
			pe.Provider = p.Name()
		}
		attempts = append(attempts, Attempt{
			Provider: p.Name(), Status: "failed", Code: pe.Code, Summary: pe.Summary(),
		})
		pe.Attempts = append([]Attempt(nil), attempts...)
		last = pe
		if !pe.Fallbackable() {
			return SearchResult{}, pe
		}
	}
	if last != nil {
		return SearchResult{}, last
	}
	return SearchResult{}, &Error{
		Code: CodeMissingKey, Message: "no web search provider is configured",
		Attempts: attempts,
	}
}

func contextError(err error) *Error {
	if errors.Is(err, context.DeadlineExceeded) {
		return &Error{Code: CodeTimeout, Message: "web search timed out"}
	}
	return &Error{Code: CodeCancelled, Message: "web search was cancelled"}
}

func envIntAny(fallback int, names ...string) int {
	for _, name := range names {
		raw := strings.TrimSpace(getenv(name))
		if raw == "" {
			continue
		}
		n, err := strconv.Atoi(raw)
		if err == nil && n >= 0 {
			return n
		}
	}
	return fallback
}

// getenv is a package variable so tests can isolate the environment without
// mutating the process. Production uses os.Getenv.
var getenv = os.Getenv
