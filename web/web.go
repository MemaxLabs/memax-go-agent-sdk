// Package web defines host-owned web search and fetch contracts.
//
// The core SDK does not browse the public internet by itself. Hosts attach
// implementations that enforce their own provider choice, credentials, domain
// policy, caching, and audit posture, then expose them through tools such as
// toolkit/webtools.
package web

import (
	"context"
	"fmt"
	"time"

	"github.com/MemaxLabs/memax-go-agent-sdk/identity"
)

// SearchRequest carries web-search context and bounds.
type SearchRequest struct {
	SessionID       string
	ParentSessionID string
	Identity        identity.Identity
	Query           string
	Domains         []string
	Limit           int
}

// SearchResult is one metadata-first web search hit.
type SearchResult struct {
	Title       string
	URL         string
	Snippet     string
	Source      string
	PublishedAt time.Time
	Metadata    map[string]any
}

// Searcher searches a host-owned web search provider.
type Searcher interface {
	SearchWeb(context.Context, SearchRequest) ([]SearchResult, error)
}

// SearcherFunc adapts a function to Searcher.
type SearcherFunc func(context.Context, SearchRequest) ([]SearchResult, error)

// SearchWeb calls f(ctx, req).
func (f SearcherFunc) SearchWeb(ctx context.Context, req SearchRequest) ([]SearchResult, error) {
	if f == nil {
		return nil, fmt.Errorf("web: nil SearcherFunc")
	}
	return f(ctx, req)
}

// FetchRequest identifies one URL to fetch through a host-owned backend. Hosts
// remain responsible for credential stripping, SSRF policy, redirects, caching,
// and audit on the backend side.
type FetchRequest struct {
	SessionID       string
	ParentSessionID string
	Identity        identity.Identity
	URL             string
	MaxBytes        int
}

// FetchResult is the normalized result of one host-owned web fetch.
type FetchResult struct {
	URL         string
	FinalURL    string
	Title       string
	Content     string
	ContentType string
	StatusCode  int
	FetchedAt   time.Time
	Metadata    map[string]any
}

// Fetcher fetches one URL through a host-owned web backend.
type Fetcher interface {
	FetchURL(context.Context, FetchRequest) (FetchResult, error)
}

// FetcherFunc adapts a function to Fetcher.
type FetcherFunc func(context.Context, FetchRequest) (FetchResult, error)

// FetchURL calls f(ctx, req).
func (f FetcherFunc) FetchURL(ctx context.Context, req FetchRequest) (FetchResult, error) {
	if f == nil {
		return FetchResult{}, fmt.Errorf("web: nil FetcherFunc")
	}
	return f(ctx, req)
}
