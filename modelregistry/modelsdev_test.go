package modelregistry

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

const fixture = `{
  "openai": {
    "id": "openai",
    "name": "OpenAI",
    "api": "https://api.openai.com/v1",
    "models": {
      "gpt-5.4": {
        "id": "gpt-5.4",
        "name": "GPT-5.4",
        "family": "gpt-5",
        "reasoning": true,
        "tool_call": true,
        "structured_output": true,
        "knowledge": "2026-01-01",
        "release_date": "2026-03-01",
        "last_updated": "2026-03-02",
        "limit": {"context": 1050000, "input": 922000, "output": 128000}
      }
    }
  },
  "anthropic": {
    "id": "anthropic",
    "name": "Anthropic",
    "api": "https://api.anthropic.com/v1",
    "models": {
      "claude-sonnet-4-6": {
        "id": "claude-sonnet-4-6",
        "name": "Claude Sonnet 4.6",
        "reasoning": true,
        "tool_call": true,
        "structured_output": false,
        "limit": {"context": 1000000, "output": 64000}
      }
    }
  },
  "openrouter": {
    "id": "openrouter",
    "name": "OpenRouter",
    "api": "https://openrouter.ai/api/v1",
    "models": {
      "openai/gpt-5.5-pro": {
        "id": "openai/gpt-5.5-pro",
        "name": "GPT-5.5 Pro",
        "reasoning": true,
        "tool_call": true,
        "structured_output": true,
        "limit": {"context": 2048000, "output": 128000}
      }
    }
  }
}`

func TestParseModelsDevAndLookupCapabilities(t *testing.T) {
	registry, err := ParseModelsDev([]byte(fixture))
	if err != nil {
		t.Fatalf("ParseModelsDev() error = %v", err)
	}
	caps, ok := registry.LookupCapabilities("openai", "gpt-5.4")
	if !ok {
		t.Fatalf("LookupCapabilities() did not find gpt-5.4")
	}
	if caps.ContextWindowTokens != 1050000 || caps.MaxOutputTokens != 128000 {
		t.Fatalf("caps = %+v, want registry limits", caps)
	}
	if caps.Provider != "openai" || caps.Model != "gpt-5.4" {
		t.Fatalf("identity = %s/%s, want openai/gpt-5.4", caps.Provider, caps.Model)
	}
	if caps.Metadata["reasoning"] != true || caps.Metadata["tool_call"] != true {
		t.Fatalf("metadata = %#v, want reasoning and tool_call true", caps.Metadata)
	}
}

func TestLookupCapabilitiesHonorsGatewayModelPrefix(t *testing.T) {
	registry, err := ParseModelsDev([]byte(fixture))
	if err != nil {
		t.Fatalf("ParseModelsDev() error = %v", err)
	}
	caps, ok := registry.LookupCapabilities("openai", "anthropic/claude-sonnet-4-6")
	if !ok {
		t.Fatalf("LookupCapabilities() did not find gateway-prefixed Anthropic model")
	}
	if caps.Provider != "anthropic" || caps.ContextWindowTokens != 1000000 {
		t.Fatalf("caps = %+v, want Anthropic registry entry", caps)
	}
}

func TestLookupCapabilitiesScansGatewayProviderModels(t *testing.T) {
	registry, err := ParseModelsDev([]byte(fixture))
	if err != nil {
		t.Fatalf("ParseModelsDev() error = %v", err)
	}
	caps, ok := registry.LookupCapabilities("openai", "openai/gpt-5.5-pro")
	if !ok {
		t.Fatalf("LookupCapabilities() did not find OpenRouter-style model id")
	}
	if caps.Provider != "openrouter" || caps.ContextWindowTokens != 2048000 {
		t.Fatalf("caps = %+v, want OpenRouter registry entry", caps)
	}
}

func TestLookupCapabilitiesDoesNotCrossProvidersForBareModelNames(t *testing.T) {
	registry, err := ParseModelsDev([]byte(fixture))
	if err != nil {
		t.Fatalf("ParseModelsDev() error = %v", err)
	}
	if caps, ok := registry.LookupCapabilities("anthropic", "gpt-5.4"); ok {
		t.Fatalf("LookupCapabilities() = %+v, want no cross-provider bare-name match", caps)
	}
}

func TestLoadUsesFreshCache(t *testing.T) {
	cachePath := filepath.Join(t.TempDir(), "models.json")
	if err := os.WriteFile(cachePath, []byte(fixture), 0o644); err != nil {
		t.Fatalf("write cache: %v", err)
	}
	now := time.Date(2026, 4, 26, 0, 0, 0, 0, time.UTC)
	if err := os.Chtimes(cachePath, now, now); err != nil {
		t.Fatalf("chtimes cache: %v", err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("fresh cache should avoid remote fetch")
	}))
	defer server.Close()

	registry, result, err := Load(context.Background(), LoadOptions{
		URL:       server.URL,
		CachePath: cachePath,
		MaxAge:    time.Hour,
		Now:       func() time.Time { return now.Add(30 * time.Minute) },
	})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if result.Source != LoadSourceCache || result.Stale {
		t.Fatalf("result = %+v, want fresh cache", result)
	}
	if _, ok := registry.LookupCapabilities("openai", "gpt-5.4"); !ok {
		t.Fatalf("fresh cache registry missing gpt-5.4")
	}
}

func TestLoadRefreshesStaleCache(t *testing.T) {
	cachePath := filepath.Join(t.TempDir(), "models.json")
	if err := os.WriteFile(cachePath, []byte(`{"openai":{"id":"openai","models":{}}}`), 0o644); err != nil {
		t.Fatalf("write cache: %v", err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, fixture)
	}))
	defer server.Close()

	registry, result, err := Load(context.Background(), LoadOptions{
		URL:       server.URL,
		CachePath: cachePath,
		MaxAge:    time.Nanosecond,
	})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if result.Source != LoadSourceRemote {
		t.Fatalf("Source = %q, want remote", result.Source)
	}
	if _, ok := registry.LookupCapabilities("openai", "gpt-5.4"); !ok {
		t.Fatalf("refreshed registry missing gpt-5.4")
	}
}

func TestLoadFallsBackToStaleCache(t *testing.T) {
	cachePath := filepath.Join(t.TempDir(), "models.json")
	if err := os.WriteFile(cachePath, []byte(fixture), 0o644); err != nil {
		t.Fatalf("write cache: %v", err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusBadGateway)
	}))
	defer server.Close()

	registry, result, err := Load(context.Background(), LoadOptions{
		URL:       server.URL,
		CachePath: cachePath,
		MaxAge:    time.Nanosecond,
	})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if result.Source != LoadSourceCache || !result.Stale {
		t.Fatalf("result = %+v, want stale cache", result)
	}
	if _, ok := registry.LookupCapabilities("openai", "gpt-5.4"); !ok {
		t.Fatalf("stale cache registry missing gpt-5.4")
	}
}

func TestLoadDoesNotFallBackToStaleCacheOnCancellation(t *testing.T) {
	cachePath := filepath.Join(t.TempDir(), "models.json")
	if err := os.WriteFile(cachePath, []byte(fixture), 0o644); err != nil {
		t.Fatalf("write cache: %v", err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	registry, result, err := Load(ctx, LoadOptions{
		URL:       server.URL,
		CachePath: cachePath,
		MaxAge:    time.Nanosecond,
	})
	if err == nil {
		t.Fatalf("Load() error = nil, want cancellation")
	}
	if registry != nil {
		t.Fatalf("registry = %+v, want nil on cancellation", registry)
	}
	if result.Source != "" || result.Stale {
		t.Fatalf("result = %+v, want no stale fallback on cancellation", result)
	}
}
