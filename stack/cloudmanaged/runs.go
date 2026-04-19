package cloudmanaged

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"

	memaxagent "github.com/MemaxLabs/memax-go-agent-sdk"
	"github.com/MemaxLabs/memax-go-agent-sdk/tenant"
)

var (
	// ErrRunStoreRequired reports that durable run operations need Config.RunStore.
	ErrRunStoreRequired = errors.New("cloudmanaged run store is required")
	// ErrRunNotFound reports that a durable run record does not exist.
	ErrRunNotFound = errors.New("cloudmanaged run not found")
	// ErrRunNotActive reports that a durable run is not currently executing.
	ErrRunNotActive = errors.New("cloudmanaged run is not active")
)

// RunStatus is one durable managed-run lifecycle state.
type RunStatus string

const (
	RunStatusQueued    RunStatus = "queued"
	RunStatusRunning   RunStatus = "running"
	RunStatusSucceeded RunStatus = "succeeded"
	RunStatusFailed    RunStatus = "failed"
	RunStatusCanceled  RunStatus = "canceled"
)

// RunRecord is one durable managed-run snapshot stored by a host-owned
// RunStore. Prompt and tenant scope are stored explicitly so hosts can inspect
// and audit run admission without reparsing transcripts.
type RunRecord struct {
	ID              string
	Status          RunStatus
	Prompt          string
	Tenant          tenant.Scope
	SessionID       string
	ParentSessionID string
	Result          string
	Error           string
	CreatedAt       time.Time
	StartedAt       time.Time
	CompletedAt     time.Time
	UpdatedAt       time.Time
}

// Terminal reports whether r is in a finished lifecycle state.
func (r RunRecord) Terminal() bool {
	switch r.Status {
	case RunStatusSucceeded, RunStatusFailed, RunStatusCanceled:
		return true
	default:
		return false
	}
}

// CreateRunRequest creates one queued durable managed run.
type CreateRunRequest struct {
	Prompt string
	Tenant tenant.Scope
}

// RunUpdate applies one partial lifecycle update to a durable run.
type RunUpdate struct {
	ID              string
	Status          RunStatus
	SessionID       string
	ParentSessionID string
	Result          *string
	Error           *string
	CompletedAt     *time.Time
}

// RunStore persists durable managed-run state.
type RunStore interface {
	CreateRun(context.Context, CreateRunRequest) (RunRecord, error)
	UpdateRun(context.Context, RunUpdate) (RunRecord, error)
	GetRun(context.Context, string) (RunRecord, error)
}

// MemoryRunStore is the reference in-memory durable run backend.
type MemoryRunStore struct {
	mu   sync.RWMutex
	runs map[string]RunRecord
}

// NewMemoryRunStore constructs a reference in-memory durable run backend.
func NewMemoryRunStore() *MemoryRunStore {
	return &MemoryRunStore{runs: make(map[string]RunRecord)}
}

// CreateRun implements RunStore.
func (s *MemoryRunStore) CreateRun(_ context.Context, req CreateRunRequest) (RunRecord, error) {
	if s == nil {
		return RunRecord{}, fmt.Errorf("memory run store is nil")
	}
	id, err := newRunID()
	if err != nil {
		return RunRecord{}, err
	}
	now := time.Now().UTC()
	record := RunRecord{
		ID:        id,
		Status:    RunStatusQueued,
		Prompt:    req.Prompt,
		Tenant:    req.Tenant.Clone(),
		CreatedAt: now,
		UpdatedAt: now,
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.runs[id] = record
	return cloneRunRecord(record), nil
}

// UpdateRun implements RunStore.
func (s *MemoryRunStore) UpdateRun(_ context.Context, update RunUpdate) (RunRecord, error) {
	if s == nil {
		return RunRecord{}, fmt.Errorf("memory run store is nil")
	}
	if update.ID == "" {
		return RunRecord{}, fmt.Errorf("run id is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.runs[update.ID]
	if !ok {
		return RunRecord{}, ErrRunNotFound
	}
	if update.Status != "" {
		record.Status = update.Status
		if update.Status == RunStatusRunning && record.StartedAt.IsZero() {
			record.StartedAt = time.Now().UTC()
		}
	}
	if update.SessionID != "" {
		record.SessionID = update.SessionID
	}
	if update.ParentSessionID != "" {
		record.ParentSessionID = update.ParentSessionID
	}
	if update.Result != nil {
		record.Result = *update.Result
	}
	if update.Error != nil {
		record.Error = *update.Error
	}
	if update.CompletedAt != nil {
		record.CompletedAt = update.CompletedAt.UTC()
	}
	record.UpdatedAt = time.Now().UTC()
	s.runs[update.ID] = record
	return cloneRunRecord(record), nil
}

// GetRun implements RunStore.
func (s *MemoryRunStore) GetRun(_ context.Context, id string) (RunRecord, error) {
	if s == nil {
		return RunRecord{}, fmt.Errorf("memory run store is nil")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	record, ok := s.runs[id]
	if !ok {
		return RunRecord{}, ErrRunNotFound
	}
	return cloneRunRecord(record), nil
}

// StartRun begins one durable background run and persists lifecycle transitions
// into the configured RunStore. The run executes on a detached context so it is
// not canceled when the caller's request context ends; use CancelRun for
// explicit host-driven interruption.
func (s Stack) StartRun(ctx context.Context, prompt string, scope tenant.Scope) (RunRecord, error) {
	if err := ctx.Err(); err != nil {
		return RunRecord{}, err
	}
	if s.runs == nil {
		return RunRecord{}, ErrRunStoreRequired
	}
	record, err := s.runs.CreateRun(ctx, CreateRunRequest{
		Prompt: prompt,
		Tenant: scope,
	})
	if err != nil {
		return RunRecord{}, fmt.Errorf("create managed run: %w", err)
	}
	runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	if s.active != nil {
		s.active.set(record.ID, cancel)
	}
	go s.executeRun(runCtx, record.ID, prompt, scope)
	return record, nil
}

// GetRun returns the current durable run snapshot.
func (s Stack) GetRun(ctx context.Context, id string) (RunRecord, error) {
	if s.runs == nil {
		return RunRecord{}, ErrRunStoreRequired
	}
	record, err := s.runs.GetRun(ctx, id)
	if err != nil {
		return RunRecord{}, fmt.Errorf("get managed run %s: %w", id, err)
	}
	return record, nil
}

// CancelRun requests cancellation for one active durable run. It does not wait
// for the final terminal status; callers should poll GetRun until the run
// reaches canceled or another terminal state.
func (s Stack) CancelRun(_ context.Context, id string) error {
	if s.runs == nil {
		return ErrRunStoreRequired
	}
	if s.active == nil || !s.active.cancel(id) {
		return ErrRunNotActive
	}
	return nil
}

func (s Stack) executeRun(ctx context.Context, runID, prompt string, scope tenant.Scope) {
	if s.active != nil {
		defer s.active.delete(runID)
	}
	_, _ = s.runs.UpdateRun(ctx, RunUpdate{
		ID:     runID,
		Status: RunStatusRunning,
	})

	var (
		finalResult string
		sawResult   bool
		runErr      error
		sessionID   string
		parentID    string
	)
	for event := range s.QueryAsync(ctx, prompt, scope) {
		switch event.Kind {
		case memaxagent.EventSessionStarted:
			sessionID = event.SessionID
			parentID = event.ParentSessionID
			_, _ = s.runs.UpdateRun(ctx, RunUpdate{
				ID:              runID,
				SessionID:       sessionID,
				ParentSessionID: parentID,
			})
		case memaxagent.EventResult:
			finalResult = event.Result
			sawResult = true
		case memaxagent.EventError:
			runErr = event.Err
		}
	}

	now := time.Now().UTC()
	update := RunUpdate{
		ID:              runID,
		SessionID:       sessionID,
		ParentSessionID: parentID,
		CompletedAt:     &now,
	}
	switch {
	case sawResult && runErr == nil:
		update.Status = RunStatusSucceeded
		update.Result = &finalResult
	case errors.Is(ctx.Err(), context.Canceled):
		update.Status = RunStatusCanceled
		errText := context.Canceled.Error()
		update.Error = &errText
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		update.Status = RunStatusCanceled
		errText := context.DeadlineExceeded.Error()
		update.Error = &errText
	case runErr != nil:
		update.Status = RunStatusFailed
		errText := runErr.Error()
		update.Error = &errText
	default:
		update.Status = RunStatusFailed
		errText := "managed run ended without terminal result"
		update.Error = &errText
	}
	_, _ = s.runs.UpdateRun(ctx, update)
}

func cloneRunRecord(record RunRecord) RunRecord {
	record.Tenant = record.Tenant.Clone()
	return record
}

func newRunID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generate run id: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

type activeRuns struct {
	mu      sync.Mutex
	cancels map[string]context.CancelFunc
}

func newActiveRuns() *activeRuns {
	return &activeRuns{cancels: make(map[string]context.CancelFunc)}
}

func (a *activeRuns) set(id string, cancel context.CancelFunc) {
	if a == nil || id == "" || cancel == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.cancels[id] = cancel
}

func (a *activeRuns) cancel(id string) bool {
	if a == nil || id == "" {
		return false
	}
	a.mu.Lock()
	cancel, ok := a.cancels[id]
	a.mu.Unlock()
	if !ok {
		return false
	}
	cancel()
	return true
}

func (a *activeRuns) delete(id string) {
	if a == nil || id == "" {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.cancels, id)
}
