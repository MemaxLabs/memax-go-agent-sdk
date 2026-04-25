// Package webtools exposes host-owned web search and fetch tools.
package webtools

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/MemaxLabs/memax-go-agent-sdk/model"
	"github.com/MemaxLabs/memax-go-agent-sdk/tool"
	"github.com/MemaxLabs/memax-go-agent-sdk/web"
)

const (
	SearchToolName = "web_search"
	FetchToolName  = "web_fetch"

	defaultSearchLimit          = 8
	maxSearchLimit              = 50
	defaultSearchOutputMaxBytes = 64 * 1024
	defaultFetchInputMaxBytes   = 64 * 1024
	defaultFetchOutputMaxBytes  = 96 * 1024
	maxFetchInputMaxBytes       = 4 * 1024 * 1024
)

// Config controls the web tools exposed for one host-owned web backend.
type Config struct {
	Searcher        web.Searcher
	Fetcher         web.Fetcher
	SearchName      string
	FetchName       string
	DefaultLimit    int
	DefaultMaxBytes int
	MaxResultBytes  int
}

// NewTools returns tools for the configured web capabilities.
func NewTools(config Config) ([]tool.Tool, error) {
	var tools []tool.Tool
	if config.Searcher != nil {
		search, err := NewSearchTool(config)
		if err != nil {
			return nil, err
		}
		tools = append(tools, search)
	}
	if config.Fetcher != nil {
		fetch, err := NewFetchTool(config)
		if err != nil {
			return nil, err
		}
		tools = append(tools, fetch)
	}
	if len(tools) == 0 {
		return nil, fmt.Errorf("webtools: at least one searcher or fetcher is required")
	}
	return tools, nil
}

// NewSearchTool returns a metadata-first web search tool.
func NewSearchTool(config Config) (tool.Tool, error) {
	if config.Searcher == nil {
		return nil, fmt.Errorf("webtools: searcher is required")
	}
	name := config.SearchName
	if name == "" {
		name = SearchToolName
	}
	limit := config.DefaultLimit
	if limit <= 0 {
		limit = defaultSearchLimit
	}
	if limit > maxSearchLimit {
		limit = maxSearchLimit
	}
	maxResultBytes := config.MaxResultBytes
	if maxResultBytes <= 0 {
		maxResultBytes = defaultSearchOutputMaxBytes
	}
	return tool.Definition{
		ToolSpec: model.ToolSpec{
			Name:            name,
			Description:     "Search a host-owned web search provider for current public information. Returns titles, URLs, snippets, and source metadata; use web_fetch to read a result.",
			SearchHint:      "web search internet current latest news docs external sources urls",
			ReadOnly:        true,
			ConcurrencySafe: true,
			AlwaysLoad:      true,
			MaxResultBytes:  maxResultBytes,
			InputSchema:     searchInputSchema(),
		},
		Handler: func(ctx context.Context, call tool.Call) (model.ToolResult, error) {
			input, err := tool.DecodeInput[searchInput](call.Use)
			if err != nil {
				return model.ToolResult{}, err
			}
			query := strings.TrimSpace(input.Query)
			if query == "" {
				return model.ToolResult{}, fmt.Errorf("webtools: query is required")
			}
			selectedLimit := input.Limit
			if selectedLimit <= 0 {
				selectedLimit = limit
			}
			if selectedLimit > maxSearchLimit {
				selectedLimit = maxSearchLimit
			}
			domains := compactStrings(input.Domains)
			results, err := config.Searcher.SearchWeb(ctx, web.SearchRequest{
				SessionID:       call.Runtime.SessionID,
				ParentSessionID: call.Runtime.ParentSessionID,
				Identity:        call.Runtime.Identity,
				Query:           query,
				Domains:         domains,
				Limit:           selectedLimit,
			})
			if err != nil {
				return model.ToolResult{}, err
			}
			return model.ToolResult{
				Content:  formatSearchResults(results),
				Metadata: searchMetadata(query, domains, results),
			}, nil
		},
	}, nil
}

// NewFetchTool returns a bounded web fetch tool.
func NewFetchTool(config Config) (tool.Tool, error) {
	if config.Fetcher == nil {
		return nil, fmt.Errorf("webtools: fetcher is required")
	}
	name := config.FetchName
	if name == "" {
		name = FetchToolName
	}
	defaultMaxBytes := config.DefaultMaxBytes
	if defaultMaxBytes <= 0 {
		defaultMaxBytes = defaultFetchInputMaxBytes
	}
	if defaultMaxBytes > maxFetchInputMaxBytes {
		defaultMaxBytes = maxFetchInputMaxBytes
	}
	maxResultBytes := config.MaxResultBytes
	if maxResultBytes <= 0 {
		maxResultBytes = defaultFetchOutputMaxBytes
	}
	return tool.Definition{
		ToolSpec: model.ToolSpec{
			Name:            name,
			Description:     "Fetch one http or https URL through a host-owned web backend after search or user-provided URL discovery.",
			SearchHint:      "fetch web page url read external docs article",
			ReadOnly:        true,
			ConcurrencySafe: true,
			AlwaysLoad:      true,
			MaxResultBytes:  maxResultBytes,
			InputSchema:     fetchInputSchema(),
		},
		Handler: func(ctx context.Context, call tool.Call) (model.ToolResult, error) {
			input, err := tool.DecodeInput[fetchInput](call.Use)
			if err != nil {
				return model.ToolResult{}, err
			}
			requestURL, err := cleanHTTPURL(input.URL)
			if err != nil {
				return model.ToolResult{}, err
			}
			maxBytes := input.MaxBytes
			if maxBytes <= 0 {
				maxBytes = defaultMaxBytes
			}
			if maxBytes > maxFetchInputMaxBytes {
				maxBytes = maxFetchInputMaxBytes
			}
			result, err := config.Fetcher.FetchURL(ctx, web.FetchRequest{
				SessionID:       call.Runtime.SessionID,
				ParentSessionID: call.Runtime.ParentSessionID,
				Identity:        call.Runtime.Identity,
				URL:             requestURL,
				MaxBytes:        maxBytes,
			})
			if err != nil {
				return model.ToolResult{}, err
			}
			return model.ToolResult{
				Content:  formatFetchResult(result),
				Metadata: fetchMetadata(requestURL, result),
			}, nil
		},
	}, nil
}

type searchInput struct {
	Query   string   `json:"query"`
	Domains []string `json:"domains"`
	Limit   int      `json:"limit"`
}

type fetchInput struct {
	URL      string `json:"url"`
	MaxBytes int    `json:"max_bytes"`
}

func searchInputSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"required":             []any{"query"},
		"additionalProperties": false,
		"properties": map[string]any{
			"query":   map[string]any{"type": "string", "minLength": 1, "description": "Web search query."},
			"domains": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Optional domain filters when the backend supports them, such as example.com."},
			"limit":   map[string]any{"type": "integer", "minimum": 1, "maximum": maxSearchLimit, "description": "Maximum search results to return."},
		},
	}
}

func fetchInputSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"required":             []any{"url"},
		"additionalProperties": false,
		"properties": map[string]any{
			"url":       map[string]any{"type": "string", "minLength": 1, "description": "HTTP or HTTPS URL to fetch."},
			"max_bytes": map[string]any{"type": "integer", "minimum": 1, "maximum": maxFetchInputMaxBytes, "description": "Optional fetch size bound in bytes."},
		},
	}
}

func formatSearchResults(results []web.SearchResult) string {
	if len(results) == 0 {
		return "No web results matched."
	}
	var b strings.Builder
	for i, result := range results {
		if i > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString("- ")
		b.WriteString(resultLabel(result))
		if result.URL != "" {
			b.WriteString("\n  URL: ")
			b.WriteString(result.URL)
		}
		if result.Source != "" {
			b.WriteString("\n  Source: ")
			b.WriteString(result.Source)
		}
		if !result.PublishedAt.IsZero() {
			b.WriteString("\n  Published: ")
			b.WriteString(result.PublishedAt.Format(time.RFC3339))
		}
		if result.Snippet != "" {
			b.WriteString("\n  Snippet: ")
			b.WriteString(result.Snippet)
		}
	}
	return b.String()
}

func formatFetchResult(result web.FetchResult) string {
	var b strings.Builder
	if result.URL != "" {
		writeField(&b, "URL", result.URL)
	}
	if result.FinalURL != "" && result.FinalURL != result.URL {
		writeField(&b, "Final URL", result.FinalURL)
	}
	if result.StatusCode != 0 {
		writeField(&b, "Status", fmt.Sprintf("%d", result.StatusCode))
	}
	if result.ContentType != "" {
		writeField(&b, "Content-Type", result.ContentType)
	}
	if !result.FetchedAt.IsZero() {
		writeField(&b, "Fetched", result.FetchedAt.Format(time.RFC3339))
	}
	if result.Title != "" {
		writeField(&b, "Title", result.Title)
	}
	if result.Content != "" {
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString(result.Content)
	}
	if b.Len() == 0 {
		return "Fetched URL with no content."
	}
	return b.String()
}

func writeField(b *strings.Builder, name, value string) {
	if b.Len() > 0 {
		b.WriteString("\n")
	}
	b.WriteString(name)
	b.WriteString(": ")
	b.WriteString(value)
}

func searchMetadata(query string, domains []string, results []web.SearchResult) map[string]any {
	metadata := map[string]any{
		model.MetadataWebOperation:   "search",
		model.MetadataWebQuery:       query,
		model.MetadataWebResultCount: len(results),
	}
	if len(domains) > 0 {
		metadata[model.MetadataWebDomains] = append([]string(nil), domains...)
	}
	urls := make([]string, 0, len(results))
	for _, result := range results {
		if result.URL != "" {
			urls = append(urls, result.URL)
		}
	}
	if len(urls) > 0 {
		metadata[model.MetadataWebURLs] = urls
	}
	resultMetadata := searchResultMetadata(results)
	if len(resultMetadata) > 0 {
		metadata[model.MetadataWebResultMetadata] = resultMetadata
	}
	return metadata
}

func fetchMetadata(requestURL string, result web.FetchResult) map[string]any {
	metadata := model.CloneMetadata(result.Metadata)
	if metadata == nil {
		metadata = make(map[string]any, 8)
	}
	deleteWebFetchMetadata(metadata)
	metadata[model.MetadataWebOperation] = "fetch"
	metadata[model.MetadataWebURL] = requestURL
	urls := []string{requestURL}
	if result.URL != "" && result.URL != requestURL {
		urls = append(urls, result.URL)
	}
	if result.FinalURL != "" && result.FinalURL != result.URL && result.FinalURL != requestURL {
		urls = append(urls, result.FinalURL)
	}
	metadata[model.MetadataWebURLs] = urls
	if result.FinalURL != "" {
		metadata[model.MetadataWebFinalURL] = result.FinalURL
	}
	if result.StatusCode != 0 {
		metadata[model.MetadataWebStatusCode] = result.StatusCode
	}
	if result.ContentType != "" {
		metadata[model.MetadataWebContentType] = result.ContentType
	}
	if result.Content != "" {
		metadata[model.MetadataWebContentBytes] = len(result.Content)
	}
	if !result.FetchedAt.IsZero() {
		metadata[model.MetadataWebFetchedAt] = result.FetchedAt.Format(time.RFC3339Nano)
	}
	return metadata
}

func searchResultMetadata(results []web.SearchResult) []map[string]any {
	var out []map[string]any
	for i, result := range results {
		item := model.CloneMetadata(result.Metadata)
		if len(item) == 0 {
			continue
		}
		if out == nil {
			out = make([]map[string]any, len(results))
		}
		out[i] = item
	}
	return out
}

func deleteWebFetchMetadata(metadata map[string]any) {
	for _, key := range []string{
		model.MetadataWebOperation,
		model.MetadataWebURL,
		model.MetadataWebURLs,
		model.MetadataWebFinalURL,
		model.MetadataWebStatusCode,
		model.MetadataWebContentType,
		model.MetadataWebContentBytes,
		model.MetadataWebFetchedAt,
	} {
		delete(metadata, key)
	}
}

func resultLabel(result web.SearchResult) string {
	if result.Title != "" {
		return result.Title
	}
	if result.URL != "" {
		return result.URL
	}
	return "Untitled result"
}

func cleanHTTPURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("webtools: url is required")
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("webtools: invalid url: %w", err)
	}
	if parsed.Host == "" {
		return "", fmt.Errorf("webtools: url must include a host")
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https":
	default:
		return "", fmt.Errorf("webtools: url scheme must be http or https")
	}
	parsed.User = nil
	return parsed.String(), nil
}

func compactStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			out = append(out, value)
		}
	}
	return out
}
