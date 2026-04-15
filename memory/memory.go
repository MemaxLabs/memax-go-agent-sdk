package memory

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"unicode"

	"github.com/MemaxLabs/memax-go-agent-sdk/identity"
	"github.com/MemaxLabs/memax-go-agent-sdk/model"
)

// Scope identifies the durability or ownership boundary for a memory.
type Scope string

const (
	ScopeProject      Scope = "project"
	ScopeUser         Scope = "user"
	ScopeSession      Scope = "session"
	ScopeOrganization Scope = "organization"
	ScopeCustom       Scope = "custom"
)

// Memory is durable context that can be injected into the model prompt.
type Memory struct {
	Name        string
	Scope       Scope
	Description string
	Content     string
	Priority    int
	AlwaysOn    bool
	Tags        []string
	Metadata    map[string]any
}

// Request gives memory sources enough context to select relevant memories.
type Request struct {
	SessionID       string
	ParentSessionID string
	Identity        identity.Identity
	Messages        []model.Message
	Query           string
}

// Source provides durable context to the prompt layer.
type Source interface {
	Memories(context.Context, Request) ([]Memory, error)
}

// SourceFunc adapts a function to Source.
type SourceFunc func(context.Context, Request) ([]Memory, error)

// Memories calls f(ctx, req).
func (f SourceFunc) Memories(ctx context.Context, req Request) ([]Memory, error) {
	if f == nil {
		return nil, fmt.Errorf("memory: nil SourceFunc")
	}
	return f(ctx, req)
}

// StaticSource is an in-memory Source implementation.
type StaticSource []Memory

// Memories returns a defensive copy of the configured memories.
func (s StaticSource) Memories(context.Context, Request) ([]Memory, error) {
	return cloneMemories(s), nil
}

// MultiSource merges memories from multiple sources. Named memories are
// deduplicated by scope and name using first-source-wins ordering. Unnamed
// memories are treated as anonymous context blocks and are not deduplicated.
type MultiSource []Source

// Memories loads each source in order and returns a defensive merged snapshot.
func (s MultiSource) Memories(ctx context.Context, req Request) ([]Memory, error) {
	out := make([]Memory, 0)
	seen := map[string]struct{}{}
	for _, source := range s {
		if source == nil {
			continue
		}
		items, err := source.Memories(ctx, req)
		if err != nil {
			return nil, err
		}
		for _, item := range items {
			key := memoryKey(item)
			if key == "" {
				out = append(out, clone(item))
				continue
			}
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			out = append(out, clone(item))
		}
	}
	return out, nil
}

// Selector selects a deterministic, relevant memory subset.
type Selector struct {
	MaxMemories int
}

// Select returns relevant memories for query. AlwaysOn memories are preserved
// even when MaxMemories would otherwise exclude them.
func (s Selector) Select(memories []Memory, query string) []Memory {
	if len(memories) == 0 {
		return nil
	}
	query = strings.ToLower(strings.TrimSpace(query))
	tokens := tokenize(query)
	items := make([]scoredMemory, 0, len(memories))
	for i, item := range memories {
		score := scoreMemory(item, query, tokens)
		if item.AlwaysOn || score > 0 || query == "" {
			items = append(items, scoredMemory{Memory: clone(item), index: i, score: score})
		}
	}
	sort.SliceStable(items, func(i, j int) bool {
		left, right := items[i], items[j]
		if left.AlwaysOn != right.AlwaysOn {
			return left.AlwaysOn
		}
		if left.score != right.score {
			return left.score > right.score
		}
		if left.Priority != right.Priority {
			if left.Priority == 0 {
				return false
			}
			if right.Priority == 0 {
				return true
			}
			return left.Priority < right.Priority
		}
		if left.index != right.index {
			return left.index < right.index
		}
		return left.Name < right.Name
	})
	items = limitMemories(items, s.MaxMemories)
	out := make([]Memory, len(items))
	for i, item := range items {
		out[i] = item.Memory
	}
	return out
}

type scoredMemory struct {
	Memory
	index int
	score int
}

func limitMemories(items []scoredMemory, max int) []scoredMemory {
	if max <= 0 || len(items) <= max {
		return items
	}
	var always []scoredMemory
	var rest []scoredMemory
	for _, item := range items {
		if item.AlwaysOn {
			always = append(always, item)
		} else {
			rest = append(rest, item)
		}
	}
	if len(always) >= max {
		return always
	}
	remaining := max - len(always)
	if len(rest) > remaining {
		rest = rest[:remaining]
	}
	return append(always, rest...)
}

func scoreMemory(item Memory, query string, tokens []string) int {
	combined := strings.ToLower(strings.Join([]string{
		item.Name,
		string(item.Scope),
		item.Description,
		strings.Join(item.Tags, " "),
		item.Content,
	}, " "))
	score := 0
	if query != "" && strings.Contains(combined, query) {
		score += 5
	}
	name := strings.ToLower(item.Name)
	for _, token := range tokens {
		if strings.Contains(name, token) {
			score += 3
			continue
		}
		if strings.Contains(combined, token) {
			score++
		}
	}
	return score
}

func memoryKey(item Memory) string {
	if item.Name == "" {
		return ""
	}
	return string(item.Scope) + "\x00" + item.Name
}

func cloneMemories(memories []Memory) []Memory {
	if len(memories) == 0 {
		return nil
	}
	out := make([]Memory, len(memories))
	for i, item := range memories {
		out[i] = clone(item)
	}
	return out
}

func clone(in Memory) Memory {
	in.Tags = append([]string(nil), in.Tags...)
	if len(in.Metadata) > 0 {
		metadata := make(map[string]any, len(in.Metadata))
		for key, value := range in.Metadata {
			metadata[key] = value
		}
		in.Metadata = metadata
	}
	return in
}

func tokenize(value string) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, field := range strings.FieldsFunc(value, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}) {
		field = strings.ToLower(strings.TrimSpace(field))
		if len(field) <= 1 {
			continue
		}
		if _, ok := seen[field]; ok {
			continue
		}
		seen[field] = struct{}{}
		out = append(out, field)
	}
	return out
}
