package checkpoint

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

type Checkpoint struct {
	ID        string         `json:"id"`
	SessionID string         `json:"session_id,omitempty"`
	ParentID  string         `json:"parent_id,omitempty"`
	Label     string         `json:"label,omitempty"`
	CreatedAt time.Time      `json:"created_at"`
	Metadata  map[string]any `json:"metadata,omitempty"`
}

type CreateOptions struct {
	SessionID string
	ParentID  string
	Label     string
	Metadata  map[string]any
}

type ListOptions struct {
	SessionID string
	ParentID  string
}

type Manager interface {
	Create(context.Context, CreateOptions) (Checkpoint, error)
	Get(context.Context, string) (Checkpoint, error)
	List(context.Context, ListOptions) ([]Checkpoint, error)
	Restore(context.Context, string) (Checkpoint, error)
	Delete(context.Context, string) error
}

type MemoryManager struct {
	mu          sync.RWMutex
	next        int
	order       []string
	checkpoints map[string]Checkpoint
}

func NewMemoryManager(checkpoints []Checkpoint) *MemoryManager {
	manager := &MemoryManager{
		next:        1,
		checkpoints: make(map[string]Checkpoint),
	}
	for _, checkpoint := range checkpoints {
		_, _ = manager.insert(checkpoint)
	}
	return manager
}

func (m *MemoryManager) Create(_ context.Context, opts CreateOptions) (Checkpoint, error) {
	checkpoint := Checkpoint{
		SessionID: strings.TrimSpace(opts.SessionID),
		ParentID:  strings.TrimSpace(opts.ParentID),
		Label:     strings.TrimSpace(opts.Label),
		CreatedAt: time.Now().UTC(),
		Metadata:  cloneMetadata(opts.Metadata),
	}
	return m.insert(checkpoint)
}

func (m *MemoryManager) Get(_ context.Context, id string) (Checkpoint, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return Checkpoint{}, fmt.Errorf("checkpoint id is required")
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	checkpoint, ok := m.checkpoints[id]
	if !ok {
		return Checkpoint{}, fmt.Errorf("checkpoint not found: %s", id)
	}
	return cloneCheckpoint(checkpoint), nil
}

func (m *MemoryManager) List(_ context.Context, opts ListOptions) ([]Checkpoint, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []Checkpoint
	for _, id := range m.order {
		checkpoint, ok := m.checkpoints[id]
		if !ok {
			continue
		}
		if opts.SessionID != "" && checkpoint.SessionID != opts.SessionID {
			continue
		}
		if opts.ParentID != "" && checkpoint.ParentID != opts.ParentID {
			continue
		}
		out = append(out, cloneCheckpoint(checkpoint))
	}
	sortCheckpoints(out)
	return out, nil
}

func (m *MemoryManager) Restore(ctx context.Context, id string) (Checkpoint, error) {
	return m.Get(ctx, id)
}

func (m *MemoryManager) Delete(_ context.Context, id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return fmt.Errorf("checkpoint id is required")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.checkpoints[id]; !ok {
		return fmt.Errorf("checkpoint not found: %s", id)
	}
	delete(m.checkpoints, id)
	for i, existing := range m.order {
		if existing == id {
			m.order = append(m.order[:i], m.order[i+1:]...)
			break
		}
	}
	return nil
}

func (m *MemoryManager) insert(checkpoint Checkpoint) (Checkpoint, error) {
	checkpoint.ID = strings.TrimSpace(checkpoint.ID)
	checkpoint.SessionID = strings.TrimSpace(checkpoint.SessionID)
	checkpoint.ParentID = strings.TrimSpace(checkpoint.ParentID)
	checkpoint.Label = strings.TrimSpace(checkpoint.Label)
	checkpoint.Metadata = cloneMetadata(checkpoint.Metadata)
	if checkpoint.CreatedAt.IsZero() {
		checkpoint.CreatedAt = time.Now().UTC()
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if checkpoint.ID == "" {
		checkpoint.ID = m.nextIDLocked()
	} else if _, ok := m.checkpoints[checkpoint.ID]; ok {
		return Checkpoint{}, fmt.Errorf("checkpoint already exists: %s", checkpoint.ID)
	}
	m.checkpoints[checkpoint.ID] = checkpoint
	m.order = append(m.order, checkpoint.ID)
	m.bumpNextLocked(checkpoint.ID)
	return cloneCheckpoint(checkpoint), nil
}

func (m *MemoryManager) nextIDLocked() string {
	for {
		id := fmt.Sprintf("checkpoint-%d", m.next)
		m.next++
		if _, ok := m.checkpoints[id]; !ok {
			return id
		}
	}
}

func (m *MemoryManager) bumpNextLocked(id string) {
	var n int
	if _, err := fmt.Sscanf(id, "checkpoint-%d", &n); err == nil && n >= m.next {
		m.next = n + 1
	}
}

func cloneCheckpoint(checkpoint Checkpoint) Checkpoint {
	checkpoint.Metadata = cloneMetadata(checkpoint.Metadata)
	return checkpoint
}

func cloneMetadata(metadata map[string]any) map[string]any {
	if len(metadata) == 0 {
		return nil
	}
	out := make(map[string]any, len(metadata))
	for k, v := range metadata {
		out[k] = v
	}
	return out
}

func sortCheckpoints(checkpoints []Checkpoint) {
	sort.SliceStable(checkpoints, func(i int, j int) bool {
		left := checkpoints[i]
		right := checkpoints[j]
		if !left.CreatedAt.Equal(right.CreatedAt) {
			return left.CreatedAt.Before(right.CreatedAt)
		}
		return left.ID < right.ID
	})
}
