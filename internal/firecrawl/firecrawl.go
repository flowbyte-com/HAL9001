// Package firecrawl adds read-only live web research to HAL9001.
//
// The client speaks Firecrawl's v2 REST API directly: a small native HTTP
// client, no Node runtime and no generic MCP bridge. Search returns ranked
// titles, URLs and excerpts; scrape returns the main-page Markdown for one
// public HTTPS URL. Neither tool can execute anything, change the permission
// mode or write to MPM. Retrieved pages are untrusted external data.
//
// The API key is read from FIRECRAWL_API_KEY and is only ever placed in the
// Authorization header. It is never stored in a session, an audit record, a
// tool result or an error message.
package firecrawl

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
	"unicode/utf8"
)

// The public v2 API and the conservative defaults for this milestone.
const (
	DefaultBaseURL = "https://api.firecrawl.dev/v2"
	DefaultTimeout = 20 * time.Second
	MaxTimeout     = 60 * time.Second

	DefaultSearchLimit = 5
	MaxSearchLimit     = 10

	DefaultMaxSearchesPerTurn = 3
	MaxSearchesPerTurn        = 5
	DefaultMaxScrapesPerTurn  = 2
	MaxScrapesPerTurn         = 5

	DefaultMaxSearchBytes = 12 * 1024
	DefaultMaxScrapeBytes = 24 * 1024
	DefaultMaxTurnBytes   = 64 * 1024
	MinMaxTurnBytes       = 8 * 1024
	MaxMaxTurnBytes       = 256 * 1024

	// MaxResponseBytes bounds the raw Firecrawl envelope held in memory.
	// A page larger than this is reported as a failure rather than cut
	// into invalid JSON.
	MaxResponseBytes = 4 * 1024 * 1024

	DefaultRetries = 2
	MaxRetries     = 2

	// MaxPageURLLength bounds the URL before it reaches the API.
	MaxPageURLLength = 2048
	// MaxQueryRunes is Firecrawl's documented query limit.
	MaxQueryRunes = 500
)

// Environment variables. The API key is the only secret.
const (
	EnvAPIKey      = "FIRECRAWL_API_KEY"
	EnvBaseURL     = "FIRECRAWL_API_URL"
	EnvSearchLimit = "HAL9001_FIRECRAWL_SEARCH_LIMIT"
	EnvMaxSearches = "HAL9001_FIRECRAWL_MAX_SEARCHES"
	EnvMaxScrapes  = "HAL9001_FIRECRAWL_MAX_SCRAPES"
	EnvTimeout     = "HAL9001_FIRECRAWL_TIMEOUT_SECONDS"
	EnvMaxTurn     = "HAL9001_FIRECRAWL_MAX_TURN_BYTES"
)

// Error codes. They are stable, content-free labels suitable for the
// transcript, the audit trail and diagnostics.
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
	CodeInvalidURL  = "invalid_url"
	CodeInvalidArgs = "invalid_arguments"
	CodeNetwork     = "network"
	CodeQuota       = "quota"
)

// Error is one Firecrawl failure. Message is safe to show to the model and
// never contains the API key.
type Error struct {
	Code       string
	HTTPStatus int
	Message    string
	Retryable  bool
	RetryAfter time.Duration
}

func (e *Error) Error() string {
	if e == nil {
		return "firecrawl: unknown error"
	}
	msg := strings.TrimSpace(e.Message)
	if msg == "" {
		msg = "Firecrawl request failed"
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
	case CodeInvalidURL:
		return "invalid URL"
	case CodeMalformed:
		return "malformed response"
	case CodeTooLarge:
		return "response too large"
	case CodeUpstream:
		return "upstream error"
	case CodeRejected:
		return "request rejected"
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

// Settings are the client and per-turn limits. Zero values use the
// conservative defaults; the environment can tune the basic limits.
type Settings struct {
	APIKey             string
	BaseURL            string
	Timeout            time.Duration
	SearchLimit        int
	MaxSearchesPerTurn int
	MaxScrapesPerTurn  int
	MaxSearchBytes     int
	MaxScrapeBytes     int
	MaxTurnBytes       int
	MaxRetries         int
}

// SettingsFromEnv resolves the settings. The API key is never logged or
// written anywhere.
func SettingsFromEnv() Settings {
	s := Settings{
		APIKey:             strings.TrimSpace(os.Getenv(EnvAPIKey)),
		BaseURL:            strings.TrimSpace(os.Getenv(EnvBaseURL)),
		SearchLimit:        envInt(EnvSearchLimit, DefaultSearchLimit),
		MaxSearchesPerTurn: envInt(EnvMaxSearches, DefaultMaxSearchesPerTurn),
		MaxScrapesPerTurn:  envInt(EnvMaxScrapes, DefaultMaxScrapesPerTurn),
		MaxTurnBytes:       envInt(EnvMaxTurn, DefaultMaxTurnBytes),
		Timeout:            time.Duration(envInt(EnvTimeout, int(DefaultTimeout/time.Second))) * time.Second,
		MaxRetries:         DefaultRetries,
	}
	return s.normalized()
}

func (s Settings) normalized() Settings {
	if strings.TrimSpace(s.BaseURL) == "" {
		s.BaseURL = DefaultBaseURL
	}
	s.BaseURL = strings.TrimRight(strings.TrimSpace(s.BaseURL), "/")
	if s.Timeout <= 0 {
		s.Timeout = DefaultTimeout
	}
	if s.Timeout > MaxTimeout {
		s.Timeout = MaxTimeout
	}
	if s.SearchLimit <= 0 {
		s.SearchLimit = DefaultSearchLimit
	}
	if s.SearchLimit > MaxSearchLimit {
		s.SearchLimit = MaxSearchLimit
	}
	if s.MaxSearchesPerTurn <= 0 {
		s.MaxSearchesPerTurn = DefaultMaxSearchesPerTurn
	}
	if s.MaxSearchesPerTurn > MaxSearchesPerTurn {
		s.MaxSearchesPerTurn = MaxSearchesPerTurn
	}
	if s.MaxScrapesPerTurn <= 0 {
		s.MaxScrapesPerTurn = DefaultMaxScrapesPerTurn
	}
	if s.MaxScrapesPerTurn > MaxScrapesPerTurn {
		s.MaxScrapesPerTurn = MaxScrapesPerTurn
	}
	if s.MaxTurnBytes < MinMaxTurnBytes {
		s.MaxTurnBytes = DefaultMaxTurnBytes
	}
	if s.MaxTurnBytes > MaxMaxTurnBytes {
		s.MaxTurnBytes = MaxMaxTurnBytes
	}
	if s.MaxSearchBytes <= 0 {
		s.MaxSearchBytes = DefaultMaxSearchBytes
	}
	if s.MaxScrapeBytes <= 0 {
		s.MaxScrapeBytes = DefaultMaxScrapeBytes
	}
	if s.MaxSearchBytes > s.MaxTurnBytes {
		s.MaxSearchBytes = s.MaxTurnBytes
	}
	if s.MaxScrapeBytes > s.MaxTurnBytes {
		s.MaxScrapeBytes = s.MaxTurnBytes
	}
	if s.MaxRetries < 0 {
		s.MaxRetries = 0
	}
	if s.MaxRetries > MaxRetries {
		s.MaxRetries = MaxRetries
	}
	return s
}

func envInt(name string, fallback int) int {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		return fallback
	}
	return n
}

// Client is the Firecrawl v2 HTTP client.
type Client struct {
	settings  Settings
	http      *http.Client
	lookup    func(context.Context, string, string) ([]net.IP, error)
	sleep     func(context.Context, time.Duration) error
	now       func() time.Time
	configErr error
}

// New builds a client. A missing key or an invalid endpoint is not a panic:
// the registry reports the reason and offers no tools.
func New(settings Settings) *Client {
	c := &Client{
		settings: settings.normalized(),
		lookup:   net.DefaultResolver.LookupIP,
		sleep:    sleepContext,
		now:      time.Now,
	}
	if err := validateBaseURL(c.settings.BaseURL); err != nil {
		c.configErr = err
	}
	c.http = &http.Client{
		// Firecrawl's API does not require redirects. Refusing them keeps
		// the bearer header from travelling to an unexpected host.
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	return c
}

// Available reports whether the client can make a request.
func (c *Client) Available() bool {
	return c != nil && strings.TrimSpace(c.settings.APIKey) != "" && c.configErr == nil
}

// UnavailableReason explains the missing prerequisite without exposing a
// secret.
func (c *Client) UnavailableReason() string {
	if c == nil {
		return "Firecrawl is not configured"
	}
	if strings.TrimSpace(c.settings.APIKey) == "" {
		return "FIRECRAWL_API_KEY is not set"
	}
	if c.configErr != nil {
		return "FIRECRAWL_API_URL is invalid"
	}
	return "Firecrawl is not configured"
}

// Settings returns the effective limits. The key is deliberately not part
// of the returned copy for callers that display settings.
func (c *Client) Limits() Settings {
	s := c.settings
	s.APIKey = ""
	return s
}

// SearchHit is one ranked result.
type SearchHit struct {
	Position    int
	Title       string
	URL         string
	Excerpt     string
	PublishedAt string
}

// SearchResult is the normalized search response.
type SearchResult struct {
	Query       string
	Results     []SearchHit
	CreditsUsed int
	RetrievedAt time.Time
}

// ScrapeResult is the normalized scrape response.
type ScrapeResult struct {
	RequestedURL string
	FinalURL     string
	SourceURL    string
	Title        string
	Description  string
	Language     string
	Keywords     []string
	PublishedAt  string
	ModifiedAt   string
	StatusCode   int
	ContentType  string
	Markdown     string
	CreditsUsed  int
	RetrievedAt  time.Time
}

type searchEnvelope struct {
	Success     *bool           `json:"success"`
	Error       string          `json:"error"`
	CreditsUsed int             `json:"creditsUsed"`
	Data        json.RawMessage `json:"data"`
}

type searchWebItem struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	URL         string `json:"url"`
	Position    int    `json:"position"`
	Date        string `json:"date"`
	PublishedAt string `json:"published_at"`
}

type searchData struct {
	Web []searchWebItem `json:"web"`
}

type scrapeEnvelope struct {
	Success     *bool          `json:"success"`
	Error       string         `json:"error"`
	CreditsUsed int            `json:"creditsUsed"`
	Data        scrapeDocument `json:"data"`
}

type scrapeDocument struct {
	Markdown string         `json:"markdown"`
	Metadata map[string]any `json:"metadata"`
}

// Search runs one web search. It never requests scrapeOptions: excerpts
// are the default, and every full page is a deliberate scrape.
func (c *Client) Search(ctx context.Context, query string, limit int) (SearchResult, error) {
	if !c.Available() {
		return SearchResult{}, c.unavailable()
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return SearchResult{}, &Error{Code: CodeInvalidArgs, Message: "search query is empty"}
	}
	if utf8.RuneCountInString(query) > MaxQueryRunes {
		return SearchResult{}, &Error{Code: CodeInvalidArgs,
			Message: fmt.Sprintf("search query is longer than %d characters", MaxQueryRunes)}
	}
	if limit <= 0 {
		limit = c.settings.SearchLimit
	}
	if limit > c.settings.SearchLimit {
		limit = c.settings.SearchLimit
	}
	payload := map[string]any{
		"query":             query,
		"limit":             limit,
		"sources":           []string{"web"},
		"highlights":        true,
		"ignoreInvalidURLs": true,
		"timeout":           int(c.settings.Timeout / time.Millisecond),
	}
	var env searchEnvelope
	if err := c.post(ctx, "/search", payload, &env); err != nil {
		return SearchResult{}, err
	}
	if env.Success == nil || !*env.Success {
		return SearchResult{}, c.apiFailure(env.Error, "search")
	}
	items, err := searchItems(env.Data)
	if err != nil {
		return SearchResult{}, err
	}
	out := SearchResult{
		Query:       query,
		Results:     make([]SearchHit, 0, len(items)),
		CreditsUsed: env.CreditsUsed,
		RetrievedAt: c.now().UTC(),
	}
	for _, item := range items {
		title := strings.TrimSpace(item.Title)
		rawURL := strings.TrimSpace(item.URL)
		if rawURL == "" || len(rawURL) > 4096 {
			continue
		}
		if title == "" {
			title = rawURL
		}
		position := item.Position
		if position <= 0 {
			position = len(out.Results) + 1
		}
		published := strings.TrimSpace(item.PublishedAt)
		if published == "" {
			published = strings.TrimSpace(item.Date)
		}
		out.Results = append(out.Results, SearchHit{
			Position:    position,
			Title:       clipText(title, 300),
			URL:         rawURL,
			Excerpt:     strings.TrimSpace(item.Description),
			PublishedAt: clipText(published, 80),
		})
		if len(out.Results) >= limit {
			break
		}
	}
	if len(items) > 0 && len(out.Results) == 0 {
		return SearchResult{}, &Error{Code: CodeMalformed,
			Message: "Firecrawl returned search results without usable URLs"}
	}
	return out, nil
}

// searchItems accepts both documented v2 shapes: data.web and a bare data
// array. The object form is current; the array form keeps older proxies and
// stored test fixtures working.
func searchItems(raw json.RawMessage) ([]searchWebItem, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	if raw[0] == '[' {
		var items []searchWebItem
		if err := json.Unmarshal(raw, &items); err != nil {
			return nil, &Error{Code: CodeMalformed, Message: "Firecrawl returned an unreadable search payload"}
		}
		return items, nil
	}
	var data searchData
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, &Error{Code: CodeMalformed, Message: "Firecrawl returned an unreadable search payload"}
	}
	return data.Web, nil
}

// Scrape retrieves one public HTTPS page as main-content Markdown.
func (c *Client) Scrape(ctx context.Context, pageURL string) (ScrapeResult, error) {
	if !c.Available() {
		return ScrapeResult{}, c.unavailable()
	}
	parsed, err := c.ValidatePageURL(ctx, pageURL)
	if err != nil {
		return ScrapeResult{}, err
	}
	payload := map[string]any{
		"url":                parsed.String(),
		"formats":            []string{"markdown"},
		"onlyMainContent":    true,
		"blockAds":           true,
		"removeBase64Images": true,
		"maxAge":             0,
		// Do not bill PDF page parsing in this milestone. A PDF that
		// returns no Markdown is reported as unreadable instead.
		"parsers": []string{},
		"timeout": int(c.settings.Timeout / time.Millisecond),
	}
	var env scrapeEnvelope
	if err := c.post(ctx, "/scrape", payload, &env); err != nil {
		return ScrapeResult{}, err
	}
	if env.Success == nil || !*env.Success {
		return ScrapeResult{}, c.apiFailure(env.Error, "scrape")
	}
	markdown := strings.TrimSpace(env.Data.Markdown)
	meta := env.Data.Metadata
	if markdown == "" {
		if detail := metadataString(meta, "error"); detail != "" {
			return ScrapeResult{}, &Error{Code: CodeRejected,
				Message: "Firecrawl could not read the page: " + sanitizeDetail(detail, 240)}
		}
		return ScrapeResult{}, &Error{Code: CodeRejected,
			Message: "Firecrawl returned no readable Markdown for that page"}
	}
	out := ScrapeResult{
		RequestedURL: parsed.String(),
		FinalURL:     clipText(metadataString(meta, "url", "sourceURL"), MaxPageURLLength),
		SourceURL:    clipText(metadataString(meta, "sourceURL"), MaxPageURLLength),
		Title:        clipText(metadataString(meta, "title"), 300),
		Description:  clipText(metadataString(meta, "description"), 2000),
		Language:     clipText(metadataString(meta, "language"), 40),
		Keywords:     metadataStrings(meta["keywords"]),
		PublishedAt:  clipText(metadataString(meta, "publishedTime", "published_time", "publishedDate", "article:published_time", "date"), 80),
		ModifiedAt:   clipText(metadataString(meta, "modifiedTime", "modified_time", "lastModified", "article:modified_time"), 80),
		StatusCode:   metadataInt(meta, "statusCode"),
		ContentType:  clipText(metadataString(meta, "contentType"), 100),
		Markdown:     markdown,
		CreditsUsed:  env.CreditsUsed,
		RetrievedAt:  c.now().UTC(),
	}
	if out.FinalURL == "" {
		out.FinalURL = out.SourceURL
	}
	if out.FinalURL == "" {
		out.FinalURL = out.RequestedURL
	}
	if out.SourceURL == "" {
		out.SourceURL = out.RequestedURL
	}
	if out.Title == "" {
		out.Title = parsed.Hostname()
	}
	return out, nil
}

// ValidatePageURL accepts exactly one absolute HTTPS URL whose host resolves
// only to public addresses. It rejects credentials, local names and special
// use networks before any request reaches Firecrawl.
func (c *Client) ValidatePageURL(ctx context.Context, raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, &Error{Code: CodeInvalidURL, Message: "url is required"}
	}
	if len(raw) > MaxPageURLLength {
		return nil, &Error{Code: CodeInvalidURL, Message: "url is too long"}
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return nil, &Error{Code: CodeInvalidURL, Message: "url is not valid"}
	}
	if !strings.EqualFold(parsed.Scheme, "https") {
		return nil, &Error{Code: CodeInvalidURL, Message: "only HTTPS webpage URLs may be scraped"}
	}
	if parsed.User != nil {
		return nil, &Error{Code: CodeInvalidURL, Message: "url must not contain credentials"}
	}
	host := strings.TrimSuffix(strings.ToLower(parsed.Hostname()), ".")
	if host == "" {
		return nil, &Error{Code: CodeInvalidURL, Message: "url has no host"}
	}
	if isLocalHostname(host) {
		return nil, &Error{Code: CodeInvalidURL, Message: "url points to a local or internal host"}
	}
	if ip := net.ParseIP(host); ip != nil {
		if !isPublicIP(ip) {
			return nil, &Error{Code: CodeInvalidURL, Message: "url points to a non-public address"}
		}
		return parsed, nil
	}
	if !strings.Contains(host, ".") {
		return nil, &Error{Code: CodeInvalidURL, Message: "url points to a local or internal host"}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	lookupCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	ips, err := c.lookup(lookupCtx, "ip", host)
	if err != nil || len(ips) == 0 {
		return nil, &Error{Code: CodeInvalidURL, Message: "url host cannot be resolved"}
	}
	for _, ip := range ips {
		if !isPublicIP(ip) {
			return nil, &Error{Code: CodeInvalidURL, Message: "url resolves to a non-public address"}
		}
	}
	return parsed, nil
}

func validateBaseURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return errors.New("invalid Firecrawl base URL")
	}
	if u.User != nil {
		return errors.New("Firecrawl base URL must not contain credentials")
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
		return errors.New("plain HTTP is only allowed for a loopback Firecrawl endpoint")
	default:
		return errors.New("Firecrawl base URL must use HTTPS")
	}
}

func isLocalHostname(host string) bool {
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	for _, suffix := range []string{
		".local", ".localdomain", ".internal", ".intranet", ".lan",
		".home.arpa", ".home", ".corp", ".private",
	} {
		if strings.HasSuffix(host, suffix) {
			return true
		}
	}
	return false
}

var specialUseNetworks = func() []*net.IPNet {
	cidrs := []string{
		"0.0.0.0/8",
		"100.64.0.0/10",
		"192.0.0.0/24",
		"192.0.2.0/24",
		"198.18.0.0/15",
		"198.51.100.0/24",
		"203.0.113.0/24",
		"240.0.0.0/4",
		"2001:db8::/32",
	}
	out := make([]*net.IPNet, 0, len(cidrs))
	for _, cidr := range cidrs {
		if _, block, err := net.ParseCIDR(cidr); err == nil {
			out = append(out, block)
		}
	}
	return out
}()

func isPublicIP(ip net.IP) bool {
	if ip == nil {
		return false
	}
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() ||
		ip.IsMulticast() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return false
	}
	if !ip.IsGlobalUnicast() {
		return false
	}
	for _, block := range specialUseNetworks {
		if block.Contains(ip) {
			return false
		}
	}
	return true
}

func (c *Client) unavailable() *Error {
	if c == nil {
		return &Error{Code: CodeMissingKey, Message: "Firecrawl is not configured"}
	}
	if strings.TrimSpace(c.settings.APIKey) == "" {
		return &Error{Code: CodeMissingKey,
			Message: "Firecrawl is not configured: FIRECRAWL_API_KEY is not set"}
	}
	return &Error{Code: CodeConfig,
		Message: "Firecrawl is not configured: FIRECRAWL_API_URL is invalid"}
}

func (c *Client) apiFailure(detail, operation string) *Error {
	detail = sanitizeDetail(detail, 240)
	detail = redact(detail, c.settings.APIKey)
	if detail == "" {
		detail = "Firecrawl did not confirm success for the " + operation
	}
	return &Error{Code: CodeUpstream, Message: detail}
}

func (c *Client) post(ctx context.Context, path string, payload any, out any) error {
	if ctx == nil {
		ctx = context.Background()
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return &Error{Code: CodeInvalidArgs, Message: "request could not be encoded"}
	}
	endpoint := c.settings.BaseURL + path
	var last error
	for attempt := 0; attempt <= c.settings.MaxRetries; attempt++ {
		attemptCtx, cancel := context.WithTimeout(ctx, c.settings.Timeout)
		req, err := http.NewRequestWithContext(attemptCtx, http.MethodPost, endpoint, bytes.NewReader(body))
		if err != nil {
			cancel()
			return &Error{Code: CodeConfig, Message: "Firecrawl endpoint is invalid"}
		}
		req.Header.Set("Authorization", "Bearer "+c.settings.APIKey)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")
		req.Header.Set("User-Agent", "HAL9001-firecrawl/1")

		resp, err := c.http.Do(req)
		if err != nil {
			cancel()
			last = c.transportError(ctx, err)
			var fe *Error
			if errors.As(last, &fe) && fe.Retryable && attempt < c.settings.MaxRetries {
				if err := c.sleep(ctx, backoff(attempt)); err != nil {
					return err
				}
				continue
			}
			return last
		}
		data, readErr := io.ReadAll(io.LimitReader(resp.Body, MaxResponseBytes+1))
		_ = resp.Body.Close()
		cancel()
		if readErr != nil {
			last = &Error{Code: CodeNetwork, Message: "Firecrawl response could not be read", Retryable: true}
			if attempt < c.settings.MaxRetries {
				if err := c.sleep(ctx, backoff(attempt)); err != nil {
					return err
				}
				continue
			}
			return last
		}
		if len(data) > MaxResponseBytes {
			return &Error{Code: CodeTooLarge,
				Message: fmt.Sprintf("Firecrawl response exceeded %d bytes", MaxResponseBytes)}
		}
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			if err := json.Unmarshal(data, out); err != nil {
				return &Error{Code: CodeMalformed,
					Message: "Firecrawl returned an unreadable response"}
			}
			return nil
		}
		last = responseError(resp, data)
		var fe *Error
		if errors.As(last, &fe) {
			fe.Message = redact(fe.Message, c.settings.APIKey)
		}
		if errors.As(last, &fe) && fe.Retryable && attempt < c.settings.MaxRetries {
			delay := fe.RetryAfter
			if delay <= 0 {
				delay = backoff(attempt)
			}
			if delay > 5*time.Second {
				delay = 5 * time.Second
			}
			if err := c.sleep(ctx, delay); err != nil {
				return err
			}
			continue
		}
		return last
	}
	if last != nil {
		return last
	}
	return &Error{Code: CodeNetwork, Message: "Firecrawl request failed"}
}

func (c *Client) transportError(parent context.Context, err error) *Error {
	if parent != nil && parent.Err() == context.Canceled {
		return &Error{Code: CodeCancelled, Message: "Firecrawl request was cancelled"}
	}
	if parent != nil && parent.Err() == context.DeadlineExceeded {
		return &Error{Code: CodeTimeout, Message: "Firecrawl request timed out"}
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return &Error{Code: CodeTimeout, Message: "Firecrawl request timed out"}
	}
	if errors.Is(err, context.Canceled) {
		return &Error{Code: CodeCancelled, Message: "Firecrawl request was cancelled"}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return &Error{Code: CodeTimeout, Message: "Firecrawl request timed out"}
	}
	return &Error{Code: CodeNetwork, Message: "Firecrawl could not be reached", Retryable: true}
}

func responseError(resp *http.Response, data []byte) *Error {
	status := resp.StatusCode
	detail := apiErrorText(data)
	e := &Error{HTTPStatus: status}
	switch status {
	case http.StatusUnauthorized:
		e.Code = CodeAuth
		e.Message = "Firecrawl authentication failed (401); check FIRECRAWL_API_KEY"
	case http.StatusPaymentRequired:
		e.Code = CodeCredits
		e.Message = "Firecrawl credits are exhausted (402)"
	case http.StatusForbidden:
		e.Code = CodeForbidden
		e.Message = "Firecrawl refused the request (403)"
		if detail != "" {
			e.Message += ": " + detail
		}
	case http.StatusTooManyRequests:
		e.Code = CodeRateLimited
		e.Message = "Firecrawl rate limit reached (429)"
		e.Retryable = true
		e.RetryAfter = retryAfter(resp)
	case http.StatusRequestTimeout:
		e.Code = CodeTimeout
		e.Message = "Firecrawl request timed out (408)"
		e.Retryable = true
	case http.StatusBadRequest, http.StatusUnprocessableEntity:
		e.Code = CodeRejected
		e.Message = fmt.Sprintf("Firecrawl rejected the request (%d)", status)
		if detail != "" {
			e.Message += ": " + detail
		}
	default:
		if status >= 500 {
			e.Code = CodeUpstream
			e.Message = fmt.Sprintf("Firecrawl upstream error (%d)", status)
			e.Retryable = true
		} else {
			e.Code = CodeRejected
			e.Message = fmt.Sprintf("Firecrawl rejected the request (%d)", status)
			if detail != "" {
				e.Message += ": " + detail
			}
		}
	}
	return e
}

func apiErrorText(data []byte) string {
	var body struct {
		Error string `json:"error"`
		Code  string `json:"code"`
	}
	if err := json.Unmarshal(data, &body); err != nil {
		return ""
	}
	detail := strings.TrimSpace(body.Error)
	if detail == "" {
		detail = strings.TrimSpace(body.Code)
	}
	return sanitizeDetail(detail, 240)
}

func sanitizeDetail(s string, max int) string {
	s = strings.Join(strings.Fields(strings.TrimSpace(s)), " ")
	if s == "" {
		return ""
	}
	r := []rune(s)
	if len(r) > max {
		s = string(r[:max]) + "…"
	}
	return s
}

// redact is the last line of defence for an API that echoes a credential
// back in an error body. The key is never included in a tool result.
func redact(s, key string) string {
	key = strings.TrimSpace(key)
	if key == "" {
		return s
	}
	return strings.ReplaceAll(s, key, "[redacted]")
}

// clipText bounds a metadata field. It is display/context hygiene, not a
// substitute for the Markdown budget.
func clipText(s string, max int) string {
	s = strings.TrimSpace(s)
	if max <= 0 || len(s) == 0 {
		return s
	}
	clipped, _ := truncateRunes(s, max)
	return clipped
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
		d := time.Until(when)
		if d > 0 {
			return d
		}
	}
	return 0
}

func backoff(attempt int) time.Duration {
	if attempt < 0 {
		attempt = 0
	}
	d := time.Duration(1<<uint(attempt)) * 500 * time.Millisecond
	if d > 2*time.Second {
		d = 2 * time.Second
	}
	return d
}

func sleepContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		if ctx.Err() == context.DeadlineExceeded {
			return &Error{Code: CodeTimeout, Message: "Firecrawl request timed out"}
		}
		return &Error{Code: CodeCancelled, Message: "Firecrawl request was cancelled"}
	case <-timer.C:
		return nil
	}
}

func metadataString(meta map[string]any, keys ...string) string {
	for _, key := range keys {
		if v, ok := meta[key]; ok {
			if s := anyString(v); s != "" {
				return s
			}
		}
	}
	return ""
}

func anyString(v any) string {
	switch x := v.(type) {
	case string:
		return strings.TrimSpace(x)
	case []any:
		for _, item := range x {
			if s := anyString(item); s != "" {
				return s
			}
		}
	case []string:
		for _, item := range x {
			if s := strings.TrimSpace(item); s != "" {
				return s
			}
		}
	case json.Number:
		return x.String()
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	}
	return ""
}

func metadataStrings(v any) []string {
	var out []string
	add := func(s string) {
		s = strings.TrimSpace(s)
		if s == "" || len(out) >= 10 {
			return
		}
		out = append(out, sanitizeDetail(s, 80))
	}
	switch x := v.(type) {
	case string:
		for _, part := range strings.Split(x, ",") {
			add(part)
		}
	case []any:
		for _, item := range x {
			add(anyString(item))
		}
	case []string:
		for _, item := range x {
			add(item)
		}
	}
	return out
}

func metadataInt(meta map[string]any, key string) int {
	v, ok := meta[key]
	if !ok {
		return 0
	}
	switch x := v.(type) {
	case float64:
		return int(x)
	case int:
		return x
	case json.Number:
		n, _ := x.Int64()
		return int(n)
	case string:
		n, _ := strconv.Atoi(strings.TrimSpace(x))
		return n
	}
	return 0
}
