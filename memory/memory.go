package memory

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
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
	ID          string
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

// PutRequest describes one durable memory write requested by a host or tool.
type PutRequest struct {
	SessionID       string
	ParentSessionID string
	Identity        identity.Identity
	Memory          Memory
}

// DeleteRequest describes one durable memory delete requested by a host or tool.
// Stores may support ID, scope/name, or both.
type DeleteRequest struct {
	SessionID       string
	ParentSessionID string
	Identity        identity.Identity
	ID              string
	Name            string
	Scope           Scope
}

// Source provides durable context to the prompt layer.
type Source interface {
	Memories(context.Context, Request) ([]Memory, error)
}

// Writer is an optional memory mutation capability.
type Writer interface {
	PutMemory(context.Context, PutRequest) (PutResult, error)
}

// PutResult is the outcome of a durable memory write.
type PutResult struct {
	Memory  Memory
	Created bool
	Updated bool
}

// Deleter is an optional memory deletion capability.
type Deleter interface {
	DeleteMemory(context.Context, DeleteRequest) error
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

// MemoryStore is a concurrency-safe in-memory Source, Writer, and Deleter for
// tests, examples, and short-lived agents.
type MemoryStore struct {
	mu       sync.RWMutex
	memories map[string]Memory
	order    []string
	next     int
}

// NewMemoryStore returns an in-memory memory store seeded with memories.
func NewMemoryStore(memories []Memory) *MemoryStore {
	store := &MemoryStore{
		memories: make(map[string]Memory),
		next:     1,
	}
	for _, item := range memories {
		_, _ = store.insert(item)
	}
	return store
}

// Memories returns a defensive snapshot of all memories. Selection is handled
// by Selector or the prompt builder.
func (s *MemoryStore) Memories(ctx context.Context, _ Request) ([]Memory, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	if s == nil {
		return nil, fmt.Errorf("memory: nil MemoryStore")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Memory, 0, len(s.order))
	for _, id := range s.order {
		if item, ok := s.memories[id]; ok {
			out = append(out, clone(item))
		}
	}
	return out, nil
}

// PutMemory creates or replaces a memory by ID when provided, otherwise by
// scope/name when name is present.
func (s *MemoryStore) PutMemory(ctx context.Context, req PutRequest) (PutResult, error) {
	if err := contextError(ctx); err != nil {
		return PutResult{}, err
	}
	if s == nil {
		return PutResult{}, fmt.Errorf("memory: nil MemoryStore")
	}
	if strings.TrimSpace(req.Memory.Content) == "" {
		return PutResult{}, fmt.Errorf("memory: content is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.insert(req.Memory)
}

// DeleteMemory deletes a memory by ID or by scope/name.
func (s *MemoryStore) DeleteMemory(ctx context.Context, req DeleteRequest) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	if s == nil {
		return fmt.Errorf("memory: nil MemoryStore")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureLocked()
	id := strings.TrimSpace(req.ID)
	if id == "" && req.Name != "" {
		for _, existingID := range s.order {
			item, ok := s.memories[existingID]
			if ok && item.Name == req.Name && (req.Scope == "" || item.Scope == normalizeScope(req.Scope)) {
				id = existingID
				break
			}
		}
	}
	if id == "" {
		return fmt.Errorf("memory: delete requires id or name")
	}
	if _, ok := s.memories[id]; !ok {
		return fmt.Errorf("memory not found: %s", id)
	}
	delete(s.memories, id)
	for i, existingID := range s.order {
		if existingID == id {
			s.order = append(s.order[:i], s.order[i+1:]...)
			break
		}
	}
	return nil
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
	in.ID = strings.TrimSpace(in.ID)
	in.Tags = append([]string(nil), in.Tags...)
	in.Metadata = cloneMetadata(in.Metadata)
	return in
}

func (s *MemoryStore) insert(item Memory) (PutResult, error) {
	s.ensureLocked()
	item = normalizeMemory(item)
	if item.ID == "" {
		if item.Name != "" {
			for _, id := range s.order {
				existing, ok := s.memories[id]
				if ok && existing.Name == item.Name && existing.Scope == item.Scope {
					item.ID = id
					s.memories[id] = clone(item)
					return PutResult{Memory: clone(item), Updated: true}, nil
				}
			}
		}
		item.ID = s.nextIDLocked()
	}
	_, exists := s.memories[item.ID]
	if !exists {
		s.order = append(s.order, item.ID)
	}
	s.memories[item.ID] = clone(item)
	s.bumpNextLocked(item.ID)
	return PutResult{Memory: clone(item), Created: !exists, Updated: exists}, nil
}

func (s *MemoryStore) ensureLocked() {
	if s.memories == nil {
		s.memories = make(map[string]Memory)
	}
	if s.next <= 0 {
		s.next = 1
	}
}

func normalizeMemory(item Memory) Memory {
	item.ID = strings.TrimSpace(item.ID)
	item.Name = strings.TrimSpace(item.Name)
	item.Scope = normalizeScope(item.Scope)
	item.Description = strings.TrimSpace(item.Description)
	item.Content = strings.TrimSpace(item.Content)
	item.Tags = trimStrings(item.Tags)
	item.Metadata = cloneMetadata(item.Metadata)
	return item
}

func normalizeScope(scope Scope) Scope {
	if scope == "" {
		return ScopeCustom
	}
	return scope
}

func trimStrings(values []string) []string {
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

func cloneMetadata(metadata map[string]any) map[string]any {
	if len(metadata) == 0 {
		return nil
	}
	out := make(map[string]any, len(metadata))
	for key, value := range metadata {
		out[key] = value
	}
	return out
}

func (s *MemoryStore) nextIDLocked() string {
	for {
		id := fmt.Sprintf("memory-%d", s.next)
		s.next++
		if _, ok := s.memories[id]; !ok {
			return id
		}
	}
}

func (s *MemoryStore) bumpNextLocked(id string) {
	var n int
	if _, err := fmt.Sscanf(id, "memory-%d", &n); err == nil && n >= s.next {
		s.next = n + 1
	}
}

func contextError(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
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
