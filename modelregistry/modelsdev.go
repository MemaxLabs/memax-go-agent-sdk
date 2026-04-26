// Package modelregistry loads provider-neutral model metadata from external
// registries and maps it into the SDK's stable model.Capabilities contract.
package modelregistry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/MemaxLabs/memax-go-agent-sdk/model"
)

// ModelsDevURL is the default registry endpoint published by models.dev.
const ModelsDevURL = "https://models.dev/api.json"

// Registry is a parsed models.dev-compatible provider registry.
type Registry struct {
	Providers map[string]Provider
}

// Provider describes one model provider in a models.dev-compatible registry.
type Provider struct {
	ID     string
	Name   string
	API    string
	Env    []string
	Models map[string]Model
}

// Model describes one provider model in a models.dev-compatible registry.
type Model struct {
	ID               string
	Name             string
	Family           string
	Attachment       bool
	Reasoning        bool
	ToolCall         bool
	StructuredOutput bool
	Temperature      *bool
	Knowledge        string
	ReleaseDate      string
	LastUpdated      string
	OpenWeights      bool
	Limit            Limit
}

// Limit contains token limits from a models.dev-compatible registry.
type Limit struct {
	Context int
	Input   int
	Output  int
}

type rawProvider struct {
	ID     string              `json:"id"`
	Name   string              `json:"name"`
	API    string              `json:"api"`
	Env    []string            `json:"env"`
	Models map[string]rawModel `json:"models"`
}

type rawModel struct {
	ID               string   `json:"id"`
	Name             string   `json:"name"`
	Family           string   `json:"family"`
	Attachment       bool     `json:"attachment"`
	Reasoning        bool     `json:"reasoning"`
	ToolCall         bool     `json:"tool_call"`
	StructuredOutput bool     `json:"structured_output"`
	Temperature      *bool    `json:"temperature"`
	Knowledge        string   `json:"knowledge"`
	ReleaseDate      string   `json:"release_date"`
	LastUpdated      string   `json:"last_updated"`
	OpenWeights      bool     `json:"open_weights"`
	Limit            rawLimit `json:"limit"`
}

type rawLimit struct {
	Context int `json:"context"`
	Input   int `json:"input"`
	Output  int `json:"output"`
}

// ParseModelsDev parses a models.dev-compatible JSON registry document.
func ParseModelsDev(data []byte) (*Registry, error) {
	var raw map[string]rawProvider
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse models.dev registry: %w", err)
	}
	registry := &Registry{Providers: make(map[string]Provider, len(raw))}
	for providerKey, provider := range raw {
		id := strings.TrimSpace(provider.ID)
		if id == "" {
			id = strings.TrimSpace(providerKey)
		}
		normalizedProvider := normalize(id)
		if normalizedProvider == "" {
			continue
		}
		out := Provider{
			ID:     id,
			Name:   provider.Name,
			API:    provider.API,
			Env:    append([]string(nil), provider.Env...),
			Models: make(map[string]Model, len(provider.Models)),
		}
		for modelKey, providerModel := range provider.Models {
			modelID := strings.TrimSpace(providerModel.ID)
			if modelID == "" {
				modelID = strings.TrimSpace(modelKey)
			}
			normalizedModel := normalize(modelID)
			if normalizedModel == "" {
				continue
			}
			out.Models[normalizedModel] = Model{
				ID:               modelID,
				Name:             providerModel.Name,
				Family:           providerModel.Family,
				Attachment:       providerModel.Attachment,
				Reasoning:        providerModel.Reasoning,
				ToolCall:         providerModel.ToolCall,
				StructuredOutput: providerModel.StructuredOutput,
				Temperature:      providerModel.Temperature,
				Knowledge:        providerModel.Knowledge,
				ReleaseDate:      providerModel.ReleaseDate,
				LastUpdated:      providerModel.LastUpdated,
				OpenWeights:      providerModel.OpenWeights,
				Limit: Limit{
					Context: providerModel.Limit.Context,
					Input:   providerModel.Limit.Input,
					Output:  providerModel.Limit.Output,
				},
			}
		}
		registry.Providers[normalizedProvider] = out
	}
	return registry, nil
}

// LookupCapabilities returns model capabilities for provider/modelName.
// Bare model names are scoped to the selected provider. Gateway model names
// such as "anthropic/claude-..." can resolve through the explicit model family
// or through gateway provider catalogs that index models by fully qualified ID.
func (r *Registry) LookupCapabilities(provider, modelName string) (model.Capabilities, bool) {
	if r == nil {
		return model.Capabilities{}, false
	}
	modelName = strings.TrimSpace(modelName)
	if modelName == "" {
		return model.Capabilities{}, false
	}
	providerKey := normalize(provider)
	modelKey := normalize(modelName)
	if caps, ok := r.lookupInProvider(providerKey, modelKey, modelName); ok {
		return caps, true
	}
	if family, suffix, ok := strings.Cut(modelKey, "/"); ok {
		if caps, found := r.lookupInProvider(family, modelKey, modelName); found {
			return caps, true
		}
		if caps, found := r.lookupInProvider(family, suffix, modelName); found {
			return caps, true
		}
		if caps, found := r.lookupInProvider(providerKey, suffix, modelName); found {
			return caps, true
		}
	}
	if strings.Contains(modelKey, "/") {
		for _, key := range sortedProviderKeys(r.Providers) {
			if caps, ok := capabilities(r.Providers[key], modelKey, modelName); ok {
				return caps, true
			}
		}
	}
	return model.Capabilities{}, false
}

func sortedProviderKeys(providers map[string]Provider) []string {
	keys := make([]string, 0, len(providers))
	for key := range providers {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func (r *Registry) lookupInProvider(providerKey, modelKey, requested string) (model.Capabilities, bool) {
	if providerKey == "" {
		return model.Capabilities{}, false
	}
	provider, ok := r.Providers[providerKey]
	if !ok {
		return model.Capabilities{}, false
	}
	return capabilities(provider, modelKey, requested)
}

func capabilities(provider Provider, modelKey, requested string) (model.Capabilities, bool) {
	m, ok := provider.Models[modelKey]
	if !ok {
		return model.Capabilities{}, false
	}
	contextLimit := m.Limit.Context
	if contextLimit == 0 {
		contextLimit = m.Limit.Input
	}
	metadata := map[string]any{
		"source":            "models.dev",
		"registry_model_id": m.ID,
	}
	if provider.API != "" {
		metadata["provider_api"] = provider.API
	}
	if m.Family != "" {
		metadata["family"] = m.Family
	}
	if m.Knowledge != "" {
		metadata["knowledge"] = m.Knowledge
	}
	if m.ReleaseDate != "" {
		metadata["release_date"] = m.ReleaseDate
	}
	if m.LastUpdated != "" {
		metadata["last_updated"] = m.LastUpdated
	}
	metadata["reasoning"] = m.Reasoning
	metadata["tool_call"] = m.ToolCall
	metadata["structured_output"] = m.StructuredOutput
	if m.Temperature != nil {
		metadata["temperature"] = *m.Temperature
	}
	return model.Capabilities{
		Provider:            provider.ID,
		Model:               requested,
		ContextWindowTokens: contextLimit,
		MaxOutputTokens:     m.Limit.Output,
		Metadata:            metadata,
	}, true
}

func normalize(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

// LoadOptions controls loading a models.dev-compatible registry with optional
// local caching. Callers own the cache path so SDK users can decide where
// product state belongs.
type LoadOptions struct {
	URL       string
	CachePath string
	MaxAge    time.Duration
	Client    *http.Client
	Now       func() time.Time
}

// LoadSource describes where a registry was loaded from.
type LoadSource string

const (
	LoadSourceRemote LoadSource = "remote"
	LoadSourceCache  LoadSource = "cache"
)

// LoadResult describes the source and staleness of a loaded registry.
type LoadResult struct {
	Source    LoadSource
	CachePath string
	Stale     bool
}

// Load fetches a models.dev-compatible registry using a local cache when
// configured. If MaxAge is greater than zero, fresh cache entries are used
// without a network request. If refresh fails and a cache exists, Load returns
// the stale cache without failing the caller's startup path. Context
// cancellation is returned directly and does not fall back to stale cache.
func Load(ctx context.Context, opts LoadOptions) (*Registry, LoadResult, error) {
	if opts.URL == "" {
		opts.URL = ModelsDevURL
	}
	now := time.Now
	if opts.Now != nil {
		now = opts.Now
	}
	if opts.CachePath != "" && opts.MaxAge > 0 {
		if info, err := os.Stat(opts.CachePath); err == nil && now().Sub(info.ModTime()) <= opts.MaxAge {
			registry, err := loadCache(opts.CachePath)
			if err == nil {
				return registry, LoadResult{Source: LoadSourceCache, CachePath: opts.CachePath}, nil
			}
		}
	}
	data, err := fetch(ctx, opts.URL, opts.Client)
	if err == nil {
		registry, parseErr := ParseModelsDev(data)
		if parseErr != nil {
			err = parseErr
		} else {
			if opts.CachePath != "" {
				_ = writeCache(opts.CachePath, data)
			}
			return registry, LoadResult{Source: LoadSourceRemote, CachePath: opts.CachePath}, nil
		}
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil {
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		return nil, LoadResult{CachePath: opts.CachePath}, err
	}
	if opts.CachePath != "" {
		if registry, cacheErr := loadCache(opts.CachePath); cacheErr == nil {
			return registry, LoadResult{Source: LoadSourceCache, CachePath: opts.CachePath, Stale: true}, nil
		}
	}
	return nil, LoadResult{CachePath: opts.CachePath}, err
}

func fetch(ctx context.Context, url string, client *http.Client) ([]byte, error) {
	if client == nil {
		client = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("create model registry request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "memax-go-agent-sdk/0")
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch model registry: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("fetch model registry: unexpected HTTP status %s", resp.Status)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read model registry: %w", err)
	}
	return data, nil
}

func loadCache(path string) (*Registry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read model registry cache %s: %w", path, err)
	}
	registry, err := ParseModelsDev(data)
	if err != nil {
		return nil, fmt.Errorf("parse model registry cache %s: %w", path, err)
	}
	return registry, nil
}

func writeCache(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create model registry cache dir: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("create model registry cache temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write model registry cache: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close model registry cache: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("replace model registry cache: %w", err)
	}
	return nil
}
