package websearch

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/html"
)

const (
	DefaultDuckDuckGoURL        = "https://lite.duckduckgo.com/lite/"
	DefaultDuckDuckGoTimeout    = 10 * time.Second
	DefaultDuckDuckGoInterval   = 2 * time.Second
	maxDuckDuckGoTimeout        = 30 * time.Second
	maxDuckDuckGoResponseBytes  = 1024 * 1024
	EnvDuckDuckGoURL            = "HAL9001_DDG_URL"
	EnvDuckDuckGoTimeoutSeconds = "HAL9001_DDG_TIMEOUT_SECONDS"
	EnvDuckDuckGoIntervalMS     = "HAL9001_DDG_MIN_INTERVAL_MS"
	duckDuckGoUserAgent         = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0 Safari/537.36"
)

// DuckDuckGoSettings configures the keyless emergency fallback.
type DuckDuckGoSettings struct {
	BaseURL     string
	Timeout     time.Duration
	MinInterval time.Duration
}

// DuckDuckGoSettingsFromEnv reads optional test/endpoint overrides.
func DuckDuckGoSettingsFromEnv() DuckDuckGoSettings {
	intervalMS := envIntAny(0, EnvDuckDuckGoIntervalMS)
	return DuckDuckGoSettings{
		BaseURL:     strings.TrimSpace(os.Getenv(EnvDuckDuckGoURL)),
		Timeout:     time.Duration(envIntAny(0, EnvDuckDuckGoTimeoutSeconds)) * time.Second,
		MinInterval: time.Duration(intervalMS) * time.Millisecond,
	}.normalized()
}

func (s DuckDuckGoSettings) normalized() DuckDuckGoSettings {
	if strings.TrimSpace(s.BaseURL) == "" {
		s.BaseURL = DefaultDuckDuckGoURL
	}
	s.BaseURL = strings.TrimSpace(s.BaseURL)
	if s.Timeout <= 0 {
		s.Timeout = DefaultDuckDuckGoTimeout
	}
	if s.Timeout > maxDuckDuckGoTimeout {
		s.Timeout = maxDuckDuckGoTimeout
	}
	if s.MinInterval < 0 {
		s.MinInterval = 0
	}
	if s.MinInterval == 0 {
		s.MinInterval = DefaultDuckDuckGoInterval
	}
	return s
}

// DuckDuckGoProvider is the keyless Lite HTML fallback. It makes one
// conservative request at a time, never runs a browser and never attempts to
// defeat a block or CAPTCHA.
type DuckDuckGoProvider struct {
	settings  DuckDuckGoSettings
	http      *http.Client
	now       func() time.Time
	sleep     func(context.Context, time.Duration) error
	mu        sync.Mutex
	last      time.Time
	configErr error
}

// NewDuckDuckGoProvider builds the fallback.
func NewDuckDuckGoProvider(settings DuckDuckGoSettings) *DuckDuckGoProvider {
	p := &DuckDuckGoProvider{
		settings: settings.normalized(),
		now:      time.Now,
		sleep:    sleepContext,
	}
	if err := validateDuckDuckGoURL(p.settings.BaseURL); err != nil {
		p.configErr = err
	}
	p.http = &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	return p
}

func (p *DuckDuckGoProvider) Name() string { return ProviderDuckDuckGo }

// Available is always true for a correctly configured keyless provider.
func (p *DuckDuckGoProvider) Available() bool {
	return p != nil && p.configErr == nil
}

func (p *DuckDuckGoProvider) UnavailableReason() string {
	if p == nil {
		return "DuckDuckGo is not configured"
	}
	if p.configErr != nil {
		return "HAL9001_DDG_URL is invalid"
	}
	return ""
}

func validateDuckDuckGoURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return errors.New("invalid DuckDuckGo URL")
	}
	if u.User != nil {
		return errors.New("DuckDuckGo URL must not contain credentials")
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
		return errors.New("plain HTTP is only allowed for a loopback DuckDuckGo endpoint")
	default:
		return errors.New("DuckDuckGo URL must use HTTPS")
	}
}

func (p *DuckDuckGoProvider) Search(ctx context.Context, query string, limit int) (SearchResult, error) {
	if !p.Available() {
		return SearchResult{}, &Error{Code: CodeMissingKey, Provider: ProviderDuckDuckGo, Message: p.UnavailableReason()}
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return SearchResult{}, &Error{Code: CodeInvalidArgs, Provider: ProviderDuckDuckGo, Message: "search query is empty"}
	}
	if len([]rune(query)) > 500 {
		return SearchResult{}, &Error{Code: CodeInvalidArgs, Provider: ProviderDuckDuckGo, Message: "search query is too long"}
	}
	if limit <= 0 {
		limit = defaultSearchLimit
	}
	if limit > maxSearchLimit {
		limit = maxSearchLimit
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := p.wait(ctx); err != nil {
		return SearchResult{}, err
	}
	form := url.Values{"q": {query}, "kl": {"us-en"}}
	reqCtx, cancel := context.WithTimeout(ctx, p.settings.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, p.settings.BaseURL,
		strings.NewReader(form.Encode()))
	if err != nil {
		return SearchResult{}, &Error{Code: CodeConfig, Provider: ProviderDuckDuckGo, Message: "DuckDuckGo endpoint is invalid"}
	}
	req.Header.Set("User-Agent", duckDuckGoUserAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Referer", "https://lite.duckduckgo.com/")

	resp, err := p.http.Do(req)
	if err != nil {
		return SearchResult{}, p.transportError(ctx, err)
	}
	defer resp.Body.Close()
	data, readErr := io.ReadAll(io.LimitReader(resp.Body, maxDuckDuckGoResponseBytes+1))
	if readErr != nil {
		return SearchResult{}, &Error{Code: CodeNetwork, Provider: ProviderDuckDuckGo, Message: "DuckDuckGo response could not be read", Retryable: true}
	}
	if len(data) > maxDuckDuckGoResponseBytes {
		return SearchResult{}, &Error{Code: CodeTooLarge, Provider: ProviderDuckDuckGo,
			Message: "DuckDuckGo response exceeded the bounded HTML size"}
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		return SearchResult{}, &Error{Code: CodeRateLimited, Provider: ProviderDuckDuckGo,
			Message: "DuckDuckGo rate limit reached (429)", Retryable: true}
	}
	if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusAccepted ||
		resp.StatusCode == http.StatusServiceUnavailable {
		return SearchResult{}, &Error{Code: CodeBlocked, Provider: ProviderDuckDuckGo,
			Message: "DuckDuckGo blocked or challenged the request"}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return SearchResult{}, &Error{Code: CodeUpstream, Provider: ProviderDuckDuckGo,
			Message: "DuckDuckGo upstream error"}
	}
	blocked, explicitEmpty, hits, err := parseDuckDuckGo(data, limit)
	if err != nil {
		return SearchResult{}, err
	}
	if blocked {
		return SearchResult{}, &Error{Code: CodeBlocked, Provider: ProviderDuckDuckGo,
			Message: "DuckDuckGo returned a block or CAPTCHA page"}
	}
	if len(hits) == 0 && !explicitEmpty {
		return SearchResult{}, &Error{Code: CodeLayout, Provider: ProviderDuckDuckGo,
			Message: "DuckDuckGo returned an unrecognised result layout"}
	}
	out := SearchResult{
		Query: query, Provider: ProviderDuckDuckGo, Results: hits,
		RetrievedAt: p.now().UTC(),
	}
	return out, nil
}

func (p *DuckDuckGoProvider) wait(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.now()
	if !p.last.IsZero() {
		wait := p.settings.MinInterval - now.Sub(p.last)
		if wait > 0 {
			if err := p.sleep(ctx, wait); err != nil {
				return err
			}
		}
	}
	p.last = p.now()
	return nil
}

func (p *DuckDuckGoProvider) transportError(parent context.Context, err error) *Error {
	if parent != nil && parent.Err() == context.Canceled {
		return &Error{Code: CodeCancelled, Provider: ProviderDuckDuckGo, Message: "DuckDuckGo request was cancelled"}
	}
	if parent != nil && parent.Err() == context.DeadlineExceeded {
		return &Error{Code: CodeTimeout, Provider: ProviderDuckDuckGo, Message: "DuckDuckGo request timed out"}
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return &Error{Code: CodeTimeout, Provider: ProviderDuckDuckGo, Message: "DuckDuckGo request timed out"}
	}
	if errors.Is(err, context.Canceled) {
		return &Error{Code: CodeCancelled, Provider: ProviderDuckDuckGo, Message: "DuckDuckGo request was cancelled"}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return &Error{Code: CodeTimeout, Provider: ProviderDuckDuckGo, Message: "DuckDuckGo request timed out"}
	}
	return &Error{Code: CodeNetwork, Provider: ProviderDuckDuckGo, Message: "DuckDuckGo could not be reached", Retryable: true}
}

func sleepContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return &Error{Code: CodeTimeout, Message: "DuckDuckGo request timed out"}
		}
		return &Error{Code: CodeCancelled, Message: "DuckDuckGo request was cancelled"}
	case <-timer.C:
		return nil
	}
}

// parseDuckDuckGo extracts organic Lite results. It ignores sponsored rows
// and DDG advertisement redirects, and detects a block page or a layout that
// no longer contains the known result markers.
func parseDuckDuckGo(body []byte, limit int) (blocked, explicitEmpty bool, hits []Hit, err error) {
	doc, parseErr := html.Parse(strings.NewReader(string(body)))
	if parseErr != nil {
		return false, false, nil, &Error{Code: CodeMalformed, Provider: ProviderDuckDuckGo,
			Message: "DuckDuckGo returned unreadable HTML"}
	}
	var resultRows []*html.Node
	walk(doc, func(n *html.Node) {
		if n.Type != html.ElementNode || n.Data != "tr" {
			return
		}
		if a := findResultAnchor(n); a != nil {
			resultRows = append(resultRows, n)
		}
	})
	if len(resultRows) == 0 {
		// Block/empty markers are only trusted on a page with no result
		// rows, so a legitimate snippet cannot turn into a false block.
		text := strings.ToLower(collapseSpace(textContent(doc)))
		if strings.Contains(text, "if this persists") ||
			strings.Contains(text, "unusual traffic") ||
			strings.Contains(text, "captcha") ||
			strings.Contains(text, "anomaly") {
			return true, false, nil, nil
		}
		if strings.Contains(text, "no results") {
			return false, true, nil, nil
		}
		return false, false, nil, nil
	}
	for _, row := range resultRows {
		a := findResultAnchor(row)
		if a == nil || isSponsored(a) {
			continue
		}
		href, _ := attr(a, "href")
		target, ok := decodeDuckDuckGoURL(href)
		if !ok || isAdRedirect(target) {
			continue
		}
		valid, err := ValidResultURL(target)
		if err != nil {
			continue
		}
		title := clipRunes(collapseSpace(textContent(a)), 300)
		if title == "" {
			title = valid
		}
		snippet := clipRunes(collapseSpace(followingSnippet(row)), 1200)
		hits = append(hits, Hit{
			Position: len(hits) + 1, Title: title, URL: valid,
			Excerpt: snippet,
		})
		if len(hits) >= limit {
			break
		}
	}
	return false, false, hits, nil
}

func findResultAnchor(n *html.Node) *html.Node {
	var found *html.Node
	walk(n, func(c *html.Node) {
		if found != nil || c.Type != html.ElementNode || c.Data != "a" {
			return
		}
		if hasClass(c, "result-link") || hasClass(c, "result__a") {
			found = c
		}
	})
	return found
}

func followingSnippet(row *html.Node) string {
	for sib := row.NextSibling; sib != nil; sib = sib.NextSibling {
		if sib.Type == html.ElementNode && sib.Data == "tr" && findResultAnchor(sib) != nil {
			break
		}
		var snippet string
		walk(sib, func(n *html.Node) {
			if snippet != "" || n.Type != html.ElementNode {
				return
			}
			if hasClass(n, "result-snippet") || hasClass(n, "result__snippet") {
				snippet = textContent(n)
			}
		})
		if snippet != "" {
			return snippet
		}
	}
	return ""
}

func isSponsored(n *html.Node) bool {
	for depth := 0; n != nil && depth < 8; depth, n = depth+1, n.Parent {
		if hasClass(n, "result-sponsored") || hasClass(n, "result--ad") ||
			hasClass(n, "badge--ad") || hasClass(n, "sponsored") {
			return true
		}
	}
	return false
}

func isAdRedirect(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return true
	}
	host := strings.ToLower(u.Hostname())
	if host != "duckduckgo.com" && host != "www.duckduckgo.com" {
		return false
	}
	if strings.EqualFold(u.Path, "/y.js") {
		return true
	}
	q := u.Query()
	return q.Get("ad_provider") != "" || q.Get("ad_domain") != ""
}

func decodeDuckDuckGoURL(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", false
	}
	base, _ := url.Parse(DefaultDuckDuckGoURL)
	u, err := url.Parse(raw)
	if err != nil {
		return "", false
	}
	if u.Scheme == "" {
		u = base.ResolveReference(u)
	}
	host := strings.ToLower(u.Hostname())
	if host == "duckduckgo.com" || host == "www.duckduckgo.com" {
		if u.Path == "/l/" {
			if target := strings.TrimSpace(u.Query().Get("uddg")); target != "" {
				return target, true
			}
		}
	}
	if u.Scheme == "http" || u.Scheme == "https" {
		return u.String(), true
	}
	return "", false
}

func hasClass(n *html.Node, want string) bool {
	if n == nil || n.Type != html.ElementNode {
		return false
	}
	value, ok := attr(n, "class")
	if !ok {
		return false
	}
	for _, field := range strings.Fields(value) {
		if strings.EqualFold(field, want) {
			return true
		}
	}
	return false
}

func attr(n *html.Node, name string) (string, bool) {
	for _, a := range n.Attr {
		if strings.EqualFold(a.Key, name) {
			return a.Val, true
		}
	}
	return "", false
}

func textContent(n *html.Node) string {
	if n == nil {
		return ""
	}
	if n.Type == html.TextNode {
		return n.Data
	}
	var b strings.Builder
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		b.WriteString(textContent(c))
		b.WriteByte(' ')
	}
	return b.String()
}

func walk(n *html.Node, visit func(*html.Node)) {
	if n == nil {
		return
	}
	visit(n)
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		walk(c, visit)
	}
}
