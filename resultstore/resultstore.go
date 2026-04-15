// Package resultstore defines host-owned storage for oversized tool results.
package resultstore

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// PutRequest is the full tool result payload to store outside the model
// transcript.
type PutRequest struct {
	SessionID       string
	ParentSessionID string
	ToolUseID       string
	ToolName        string
	Content         string
	Metadata        map[string]any
}

// Handle identifies a stored tool result.
type Handle struct {
	ID        string
	URI       string
	Bytes     int
	CreatedAt time.Time
	Metadata  map[string]any
}

// Entry is a stored result record.
type Entry struct {
	Handle
	SessionID       string
	ParentSessionID string
	ToolUseID       string
	ToolName        string
	Content         string
}

// Store persists an oversized tool result and returns a model-visible handle.
type Store interface {
	Put(context.Context, PutRequest) (Handle, error)
}

// Getter is an optional extension for stores that can retrieve result records.
type Getter interface {
	Get(context.Context, string) (Entry, error)
}

// StoreFunc adapts a function to Store.
type StoreFunc func(context.Context, PutRequest) (Handle, error)

// Put calls f(ctx, req).
func (f StoreFunc) Put(ctx context.Context, req PutRequest) (Handle, error) {
	if f == nil {
		return Handle{}, fmt.Errorf("resultstore: nil StoreFunc")
	}
	return f(ctx, req)
}

// MemoryStore is a concurrency-safe in-memory Store implementation for tests,
// examples, and short-lived agents.
type MemoryStore struct {
	mu      sync.RWMutex
	next    int
	entries map[string]Entry
}

// NewMemoryStore returns an empty in-memory result store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{entries: make(map[string]Entry)}
}

// Put stores a defensive copy of req and returns a stable handle.
func (s *MemoryStore) Put(ctx context.Context, req PutRequest) (Handle, error) {
	if err := ctx.Err(); err != nil {
		return Handle{}, err
	}
	if s == nil {
		return Handle{}, fmt.Errorf("resultstore: nil MemoryStore")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.entries == nil {
		s.entries = make(map[string]Entry)
	}
	s.next++
	id := fmt.Sprintf("result-%d", s.next)
	handle := Handle{
		ID:        id,
		URI:       "memax-result://" + id,
		Bytes:     len(req.Content),
		CreatedAt: time.Now().UTC(),
		Metadata:  cloneMetadata(req.Metadata),
	}
	s.entries[id] = Entry{
		Handle:          cloneHandle(handle),
		SessionID:       req.SessionID,
		ParentSessionID: req.ParentSessionID,
		ToolUseID:       req.ToolUseID,
		ToolName:        req.ToolName,
		Content:         req.Content,
	}
	return cloneHandle(handle), nil
}

// Get returns a defensive copy of a stored result.
func (s *MemoryStore) Get(ctx context.Context, id string) (Entry, error) {
	if err := ctx.Err(); err != nil {
		return Entry{}, err
	}
	if s == nil {
		return Entry{}, fmt.Errorf("resultstore: nil MemoryStore")
	}
	s.mu.RLock()
	entry, ok := s.entries[id]
	s.mu.RUnlock()
	if !ok {
		return Entry{}, fmt.Errorf("resultstore: no such result: %s", id)
	}
	return cloneEntry(entry), nil
}

func cloneEntry(in Entry) Entry {
	in.Handle = cloneHandle(in.Handle)
	return in
}

func cloneHandle(in Handle) Handle {
	in.Metadata = cloneMetadata(in.Metadata)
	return in
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
