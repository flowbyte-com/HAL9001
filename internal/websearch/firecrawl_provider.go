package websearch

import (
	"context"
	"errors"

	"github.com/flowbyte-com/HAL9001/internal/firecrawl"
)

// FirecrawlProvider adapts the phase-1 Firecrawl client to the unified search
// interface. Search is the primary provider; scrape remains Firecrawl-only.
type FirecrawlProvider struct {
	client *firecrawl.Client
}

// NewFirecrawlProvider wraps one client.
func NewFirecrawlProvider(client *firecrawl.Client) *FirecrawlProvider {
	return &FirecrawlProvider{client: client}
}

func (p *FirecrawlProvider) Name() string { return ProviderFirecrawl }

func (p *FirecrawlProvider) Available() bool {
	return p != nil && p.client != nil && p.client.Available()
}

func (p *FirecrawlProvider) UnavailableReason() string {
	if p == nil || p.client == nil {
		return "Firecrawl is not configured"
	}
	return p.client.UnavailableReason()
}

func (p *FirecrawlProvider) Search(ctx context.Context, query string, limit int) (SearchResult, error) {
	if !p.Available() {
		return SearchResult{}, &Error{
			Code: CodeMissingKey, Provider: ProviderFirecrawl, Message: p.UnavailableReason(),
		}
	}
	res, err := p.client.Search(ctx, query, limit)
	if err != nil {
		return SearchResult{}, AsError(err, ProviderFirecrawl)
	}
	out := SearchResult{
		Query: query, Provider: ProviderFirecrawl,
		Results:     make([]Hit, 0, len(res.Results)),
		RetrievedAt: res.RetrievedAt, CreditsUsed: res.CreditsUsed,
	}
	for _, item := range res.Results {
		out.Results = append(out.Results, Hit{
			Position: item.Position, Title: item.Title, URL: item.URL,
			Excerpt: item.Excerpt, PublishedAt: item.PublishedAt,
		})
	}
	return out, nil
}

// FirecrawlScraper adapts the client to the registry's scraper interface and
// normalizes its errors into the shared provider error type.
type FirecrawlScraper struct {
	client *firecrawl.Client
}

// NewFirecrawlScraper wraps one client.
func NewFirecrawlScraper(client *firecrawl.Client) *FirecrawlScraper {
	return &FirecrawlScraper{client: client}
}

func (s *FirecrawlScraper) Available() bool {
	return s != nil && s.client != nil && s.client.Available()
}

func (s *FirecrawlScraper) UnavailableReason() string {
	if s == nil || s.client == nil {
		return "Firecrawl is not configured"
	}
	return s.client.UnavailableReason()
}

func (s *FirecrawlScraper) ValidatePageURL(ctx context.Context, pageURL string) (string, error) {
	if !s.Available() {
		return "", &Error{
			Code: CodeMissingKey, Provider: ProviderFirecrawl, Message: s.UnavailableReason(),
		}
	}
	parsed, err := s.client.ValidatePageURL(ctx, pageURL)
	if err != nil {
		return "", AsError(err, ProviderFirecrawl)
	}
	return parsed.String(), nil
}

func (s *FirecrawlScraper) Scrape(ctx context.Context, pageURL string) (firecrawl.ScrapeResult, error) {
	if !s.Available() {
		return firecrawl.ScrapeResult{}, &Error{
			Code: CodeMissingKey, Provider: ProviderFirecrawl, Message: s.UnavailableReason(),
		}
	}
	res, err := s.client.Scrape(ctx, pageURL)
	if err != nil {
		return firecrawl.ScrapeResult{}, AsError(err, ProviderFirecrawl)
	}
	return res, nil
}

// AsError converts a Firecrawl client error into the shared error shape. An
// unknown error is treated as a retryable upstream failure.
func AsError(err error, provider string) *Error {
	if err == nil {
		return nil
	}
	var fe *firecrawl.Error
	if errors.As(err, &fe) {
		code := fe.Code
		switch code {
		case firecrawl.CodeTooLarge:
			code = CodeTooLarge
		case firecrawl.CodeConfig:
			code = CodeConfig
		}
		return &Error{
			Code: code, Provider: provider, HTTPStatus: fe.HTTPStatus,
			Message: fe.Message, Retryable: fe.Retryable, RetryAfter: fe.RetryAfter,
		}
	}
	return &Error{Code: CodeUpstream, Provider: provider, Message: "provider request failed"}
}
