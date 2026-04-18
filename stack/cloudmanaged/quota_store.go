package cloudmanaged

import (
	"context"
	"fmt"
	"sync"

	"github.com/MemaxLabs/memax-go-agent-sdk/tenant"
)

// QuotaCounter identifies one managed-runtime quota bucket.
type QuotaCounter string

const (
	QuotaCounterModelRequests QuotaCounter = "model_requests"
	QuotaCounterToolUses      QuotaCounter = "tool_uses"
)

// QuotaStore tracks session-scoped quota usage for managed runs.
//
// Reserve must be atomic with respect to the limit check: implementations
// should grant at most limit successful reservations for the same
// session/counter pair, even under contention. On success, used is the granted
// count after the reservation. On denial, used is the current granted count and
// granted is false. Reserve is admission-time accounting: implementations do
// not automatically release granted quota if a later model or tool action
// aborts.
type QuotaStore interface {
	EnsureSession(context.Context, tenant.Scope, string) error
	Reserve(context.Context, tenant.Scope, string, QuotaCounter, int) (used int, granted bool, err error)
	ResetSession(context.Context, tenant.Scope, string) error
}

type memoryQuotaState struct {
	ModelRequests int
	ToolUses      int
}

// MemoryQuotaStore is the reference in-memory quota backend.
//
// It keys state only on session ID and ignores tenant scope, so it is intended
// for local or single-process managed hosts. Multi-tenant distributed
// deployments should attach a scope-aware QuotaStore instead.
type MemoryQuotaStore struct {
	mu       sync.RWMutex
	sessions map[string]memoryQuotaState
}

// NewMemoryQuotaStore constructs a reference in-memory quota store.
func NewMemoryQuotaStore() *MemoryQuotaStore {
	return &MemoryQuotaStore{}
}

// EnsureSession implements QuotaStore.
func (s *MemoryQuotaStore) EnsureSession(ctx context.Context, _ tenant.Scope, sessionID string) error {
	if s == nil || sessionID == "" {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sessions == nil {
		s.sessions = make(map[string]memoryQuotaState)
	}
	if _, ok := s.sessions[sessionID]; !ok {
		s.sessions[sessionID] = memoryQuotaState{}
	}
	return nil
}

// Reserve implements QuotaStore.
func (s *MemoryQuotaStore) Reserve(ctx context.Context, _ tenant.Scope, sessionID string, counter QuotaCounter, limit int) (int, bool, error) {
	if s == nil || sessionID == "" || limit <= 0 {
		return 0, true, nil
	}
	if err := ctx.Err(); err != nil {
		return 0, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sessions == nil {
		s.sessions = make(map[string]memoryQuotaState)
	}
	state := s.sessions[sessionID]
	switch counter {
	case QuotaCounterModelRequests:
		if state.ModelRequests >= limit {
			return state.ModelRequests, false, nil
		}
		state.ModelRequests++
		s.sessions[sessionID] = state
		return state.ModelRequests, true, nil
	case QuotaCounterToolUses:
		if state.ToolUses >= limit {
			return state.ToolUses, false, nil
		}
		state.ToolUses++
		s.sessions[sessionID] = state
		return state.ToolUses, true, nil
	default:
		return 0, false, fmt.Errorf("unknown quota counter %q", counter)
	}
}

// ResetSession implements QuotaStore.
func (s *MemoryQuotaStore) ResetSession(ctx context.Context, _ tenant.Scope, sessionID string) error {
	if s == nil || sessionID == "" {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, sessionID)
	return nil
}
