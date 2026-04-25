package webtools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/MemaxLabs/memax-go-agent-sdk/identity"
	"github.com/MemaxLabs/memax-go-agent-sdk/model"
	"github.com/MemaxLabs/memax-go-agent-sdk/tool"
	"github.com/MemaxLabs/memax-go-agent-sdk/web"
)

func TestNewToolsRequiresCapability(t *testing.T) {
	t.Parallel()

	if _, err := NewTools(Config{}); err == nil {
		t.Fatal("NewTools() returned nil error without configured capabilities")
	}
}

func TestSearchToolPassesRuntimeAndFormatsResults(t *testing.T) {
	t.Parallel()

	published := time.Date(2026, 4, 25, 9, 0, 0, 0, time.UTC)
	var got web.SearchRequest
	searchTool, err := NewSearchTool(Config{
		DefaultLimit: 4,
		Searcher: web.SearcherFunc(func(ctx context.Context, req web.SearchRequest) ([]web.SearchResult, error) {
			got = req
			return []web.SearchResult{{
				Title:       "Agent runtime notes",
				URL:         "https://example.com/runtime",
				Snippet:     "Current notes on agent runtime design.",
				Source:      "example",
				PublishedAt: published,
				Metadata:    map[string]any{"rank": 1},
			}}, nil
		}),
	})
	if err != nil {
		t.Fatalf("NewSearchTool() error = %v", err)
	}

	result := runTool(t, searchTool, SearchToolName, map[string]any{
		"query":   " agent runtime ",
		"domains": []string{" example.com ", ""},
	})
	if result.IsError {
		t.Fatalf("search result = %#v", result)
	}
	if got.SessionID != "session-1" || got.ParentSessionID != "parent-1" {
		t.Fatalf("runtime ids = %q/%q", got.SessionID, got.ParentSessionID)
	}
	if got.Identity.Name != "Agent" {
		t.Fatalf("identity = %#v", got.Identity)
	}
	if got.Query != "agent runtime" || got.Limit != 4 {
		t.Fatalf("request query/limit = %q/%d", got.Query, got.Limit)
	}
	if len(got.Domains) != 1 || got.Domains[0] != "example.com" {
		t.Fatalf("domains = %#v", got.Domains)
	}
	for _, want := range []string{"Agent runtime notes", "https://example.com/runtime", "Current notes", published.Format(time.RFC3339)} {
		if !strings.Contains(result.Content, want) {
			t.Fatalf("search content = %q, want %q", result.Content, want)
		}
	}
	if result.Metadata[model.MetadataWebOperation] != "search" ||
		result.Metadata[model.MetadataWebQuery] != "agent runtime" ||
		result.Metadata[model.MetadataWebResultCount] != 1 {
		t.Fatalf("search metadata = %#v", result.Metadata)
	}
	urls, ok := result.Metadata[model.MetadataWebURLs].([]string)
	if !ok || len(urls) != 1 || urls[0] != "https://example.com/runtime" {
		t.Fatalf("web urls metadata = %#v", result.Metadata[model.MetadataWebURLs])
	}
	resultMetadata, ok := result.Metadata[model.MetadataWebResultMetadata].([]map[string]any)
	if !ok || len(resultMetadata) != 1 || resultMetadata[0]["rank"] != 1 {
		t.Fatalf("web result metadata = %#v", result.Metadata[model.MetadataWebResultMetadata])
	}
}

func TestSearchToolCapsDefaultLimit(t *testing.T) {
	t.Parallel()

	var got web.SearchRequest
	searchTool, err := NewSearchTool(Config{
		DefaultLimit: 100,
		Searcher: web.SearcherFunc(func(ctx context.Context, req web.SearchRequest) ([]web.SearchResult, error) {
			got = req
			return nil, nil
		}),
	})
	if err != nil {
		t.Fatalf("NewSearchTool() error = %v", err)
	}

	result := runTool(t, searchTool, SearchToolName, map[string]any{"query": "agent runtime"})
	if result.IsError {
		t.Fatalf("search result = %#v", result)
	}
	if got.Limit != maxSearchLimit {
		t.Fatalf("request limit = %d, want %d", got.Limit, maxSearchLimit)
	}
}

func TestSearchToolRejectsBlankQuery(t *testing.T) {
	t.Parallel()

	searchTool, err := NewSearchTool(Config{
		Searcher: web.SearcherFunc(func(ctx context.Context, req web.SearchRequest) ([]web.SearchResult, error) {
			t.Fatal("searcher should not be called")
			return nil, nil
		}),
	})
	if err != nil {
		t.Fatalf("NewSearchTool() error = %v", err)
	}

	result := runTool(t, searchTool, SearchToolName, map[string]any{"query": "  "})
	if !result.IsError || !strings.Contains(result.Content, "query is required") {
		t.Fatalf("search result = %#v, want query error", result)
	}
}

func TestFetchToolPassesRuntimeAndFormatsResult(t *testing.T) {
	t.Parallel()

	fetched := time.Date(2026, 4, 25, 10, 0, 0, 0, time.UTC)
	var got web.FetchRequest
	fetchTool, err := NewFetchTool(Config{
		DefaultMaxBytes: 128,
		Fetcher: web.FetcherFunc(func(ctx context.Context, req web.FetchRequest) (web.FetchResult, error) {
			got = req
			return web.FetchResult{
				URL:         req.URL,
				FinalURL:    "https://example.com/final",
				Title:       "Runtime",
				Content:     "Full fetched page body.",
				ContentType: "text/plain",
				StatusCode:  200,
				FetchedAt:   fetched,
				Metadata:    map[string]any{"source": "fixture"},
			}, nil
		}),
	})
	if err != nil {
		t.Fatalf("NewFetchTool() error = %v", err)
	}

	result := runTool(t, fetchTool, FetchToolName, map[string]any{
		"url": " https://example.com/runtime ",
	})
	if result.IsError {
		t.Fatalf("fetch result = %#v", result)
	}
	if got.SessionID != "session-1" || got.ParentSessionID != "parent-1" {
		t.Fatalf("runtime ids = %q/%q", got.SessionID, got.ParentSessionID)
	}
	if got.Identity.Name != "Agent" {
		t.Fatalf("identity = %#v", got.Identity)
	}
	if got.URL != "https://example.com/runtime" || got.MaxBytes != 128 {
		t.Fatalf("request url/max = %q/%d", got.URL, got.MaxBytes)
	}
	for _, want := range []string{"URL: https://example.com/runtime", "Final URL: https://example.com/final", "Status: 200", "Full fetched page body."} {
		if !strings.Contains(result.Content, want) {
			t.Fatalf("fetch content = %q, want %q", result.Content, want)
		}
	}
	if result.Metadata[model.MetadataWebOperation] != "fetch" ||
		result.Metadata[model.MetadataWebURL] != "https://example.com/runtime" ||
		result.Metadata[model.MetadataWebFinalURL] != "https://example.com/final" ||
		result.Metadata[model.MetadataWebStatusCode] != 200 ||
		result.Metadata["source"] != "fixture" {
		t.Fatalf("fetch metadata = %#v", result.Metadata)
	}
}

func TestFetchToolRejectsNonHTTPURL(t *testing.T) {
	t.Parallel()

	fetchTool, err := NewFetchTool(Config{
		Fetcher: web.FetcherFunc(func(ctx context.Context, req web.FetchRequest) (web.FetchResult, error) {
			t.Fatal("fetcher should not be called")
			return web.FetchResult{}, nil
		}),
	})
	if err != nil {
		t.Fatalf("NewFetchTool() error = %v", err)
	}

	result := runTool(t, fetchTool, FetchToolName, map[string]any{"url": "file:///etc/passwd"})
	hasValidationMessage := strings.Contains(result.Content, "url must include a host") ||
		strings.Contains(result.Content, "url scheme must be http or https")
	if !result.IsError || !hasValidationMessage {
		t.Fatalf("fetch result = %#v, want URL validation error", result)
	}
}

func TestFetchToolStripsUserinfoAndCapsMaxBytes(t *testing.T) {
	t.Parallel()

	var got web.FetchRequest
	fetchTool, err := NewFetchTool(Config{
		DefaultMaxBytes: maxFetchInputMaxBytes * 2,
		Fetcher: web.FetcherFunc(func(ctx context.Context, req web.FetchRequest) (web.FetchResult, error) {
			got = req
			return web.FetchResult{URL: req.URL}, nil
		}),
	})
	if err != nil {
		t.Fatalf("NewFetchTool() error = %v", err)
	}

	result := runTool(t, fetchTool, FetchToolName, map[string]any{"url": "https://user:pass@example.com/path"})
	if result.IsError {
		t.Fatalf("fetch result = %#v", result)
	}
	if got.URL != "https://example.com/path" {
		t.Fatalf("request url = %q, want stripped userinfo", got.URL)
	}
	if got.MaxBytes != maxFetchInputMaxBytes {
		t.Fatalf("request max bytes = %d, want %d", got.MaxBytes, maxFetchInputMaxBytes)
	}
}

func TestToolsAreReadOnlyAndConcurrent(t *testing.T) {
	t.Parallel()

	tools, err := NewTools(Config{
		Searcher: web.SearcherFunc(func(ctx context.Context, req web.SearchRequest) ([]web.SearchResult, error) {
			return nil, nil
		}),
		Fetcher: web.FetcherFunc(func(ctx context.Context, req web.FetchRequest) (web.FetchResult, error) {
			return web.FetchResult{}, nil
		}),
	})
	if err != nil {
		t.Fatalf("NewTools() error = %v", err)
	}
	for _, item := range tools {
		spec := item.Spec()
		if !spec.ReadOnly || !spec.ConcurrencySafe || !spec.AlwaysLoad {
			t.Fatalf("%s spec = %#v, want read-only concurrent always-load", spec.Name, spec)
		}
	}
}

func runTool(t *testing.T, toolImpl tool.Tool, name string, input map[string]any) model.ToolResult {
	t.Helper()
	registry := tool.NewRegistry(toolImpl)
	exec := tool.Executor{
		Registry: registry,
		Runtime: tool.Runtime{
			SessionID:       "session-1",
			ParentSessionID: "parent-1",
			Identity:        identity.Identity{Name: "Agent"},
		},
	}

	payload, err := json.Marshal(input)
	if err != nil {
		t.Fatalf("Marshal(%s) error = %v", name, err)
	}
	results := exec.Run(context.Background(), []model.ToolUse{{
		ID:    name + "-1",
		Name:  name,
		Input: payload,
	}})
	var out []model.ToolResult
	for item := range results {
		out = append(out, item)
	}
	if len(out) != 1 {
		t.Fatalf("Run(%s) results = %d, want 1", name, len(out))
	}
	return out[0]
}
