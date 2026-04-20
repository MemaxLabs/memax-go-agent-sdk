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
	// ErrRunNotQueued reports that a worker can only claim queued runs.
	ErrRunNotQueued = errors.New("cloudmanaged run is not queued")
	// ErrRunWorkerMismatch reports that a heartbeat or claim came from a
	// different worker than the run is currently assigned to.
	ErrRunWorkerMismatch = errors.New("cloudmanaged run worker mismatch")
	// ErrRunStoreClaimRequired reports that worker-driven execution needs a store
	// that can atomically claim queued runs.
	ErrRunStoreClaimRequired = errors.New("cloudmanaged run store does not support claiming queued runs")
	// ErrRunStoreHeartbeatUnsupported reports that stale-run failure handling
	// needs a store that persists worker heartbeats.
	ErrRunStoreHeartbeatUnsupported = errors.New("cloudmanaged run store does not support worker heartbeats")
	// ErrRunQueueEmpty reports that no queued durable run is currently available
	// for discovery.
	ErrRunQueueEmpty = errors.New("cloudmanaged run queue is empty")
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
	WorkerID        string
	Result          string
	Error           string
	CreatedAt       time.Time
	StartedAt       time.Time
	HeartbeatAt     time.Time
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
	WorkerID        string
	Result          *string
	Error           *string
	HeartbeatAt     *time.Time
	CompletedAt     *time.Time
}

// RunStore persists durable managed-run state.
type RunStore interface {
	CreateRun(context.Context, CreateRunRequest) (RunRecord, error)
	UpdateRun(context.Context, RunUpdate) (RunRecord, error)
	GetRun(context.Context, string) (RunRecord, error)
}

// RunStoreWithClaim atomically transitions queued runs into a claimed running
// state for one worker.
type RunStoreWithClaim interface {
	ClaimRun(context.Context, string, string) (RunRecord, error)
}

// RunStoreWithHeartbeat persists worker liveness for running runs and can mark
// stale runs as failed when hosts decide a worker has died.
type RunStoreWithHeartbeat interface {
	HeartbeatRun(context.Context, string, string) (RunRecord, error)
	FailStaleRuns(context.Context, time.Time, string) ([]RunRecord, error)
}

// RunStoreWithNextQueued discovers the next queued durable run without
// claiming it. This keeps host-owned remote dispatch discovery separate from
// ExecuteRun's atomic claim-and-run boundary.
type RunStoreWithNextQueued interface {
	NextQueuedRun(context.Context) (RunRecord, error)
}

// WorkerOptions configure one explicit worker-side execution attempt.
type WorkerOptions struct {
	ID                string
	HeartbeatInterval time.Duration
}

const staleRunFailureReason = "worker heartbeat timeout"

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
	if update.WorkerID != "" {
		record.WorkerID = update.WorkerID
	}
	if update.Result != nil {
		record.Result = *update.Result
	}
	if update.Error != nil {
		record.Error = *update.Error
	}
	if update.HeartbeatAt != nil {
		record.HeartbeatAt = update.HeartbeatAt.UTC()
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

// NextQueuedRun implements RunStoreWithNextQueued.
func (s *MemoryRunStore) NextQueuedRun(_ context.Context) (RunRecord, error) {
	if s == nil {
		return RunRecord{}, fmt.Errorf("memory run store is nil")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	var (
		selected RunRecord
		found    bool
	)
	for _, record := range s.runs {
		if record.Status != RunStatusQueued {
			continue
		}
		if !found || record.CreatedAt.Before(selected.CreatedAt) || (record.CreatedAt.Equal(selected.CreatedAt) && record.ID < selected.ID) {
			selected = record
			found = true
		}
	}
	if !found {
		return RunRecord{}, ErrRunQueueEmpty
	}
	return cloneRunRecord(selected), nil
}

// ClaimRun implements RunStoreWithClaim.
func (s *MemoryRunStore) ClaimRun(_ context.Context, id, workerID string) (RunRecord, error) {
	if s == nil {
		return RunRecord{}, fmt.Errorf("memory run store is nil")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.runs[id]
	if !ok {
		return RunRecord{}, ErrRunNotFound
	}
	if record.Status != RunStatusQueued {
		return RunRecord{}, ErrRunNotQueued
	}
	now := time.Now().UTC()
	record.Status = RunStatusRunning
	record.WorkerID = workerID
	record.StartedAt = now
	if workerID != "" {
		record.HeartbeatAt = now
	}
	record.UpdatedAt = now
	s.runs[id] = record
	return cloneRunRecord(record), nil
}

// HeartbeatRun implements RunStoreWithHeartbeat.
func (s *MemoryRunStore) HeartbeatRun(_ context.Context, id, workerID string) (RunRecord, error) {
	if s == nil {
		return RunRecord{}, fmt.Errorf("memory run store is nil")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.runs[id]
	if !ok {
		return RunRecord{}, ErrRunNotFound
	}
	if record.Status != RunStatusRunning {
		return RunRecord{}, ErrRunNotActive
	}
	if record.WorkerID != "" && workerID != "" && record.WorkerID != workerID {
		return RunRecord{}, ErrRunWorkerMismatch
	}
	now := time.Now().UTC()
	record.WorkerID = workerID
	record.HeartbeatAt = now
	record.UpdatedAt = now
	s.runs[id] = record
	return cloneRunRecord(record), nil
}

// FailStaleRuns implements RunStoreWithHeartbeat.
func (s *MemoryRunStore) FailStaleRuns(_ context.Context, staleBefore time.Time, reason string) ([]RunRecord, error) {
	if s == nil {
		return nil, fmt.Errorf("memory run store is nil")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	failed := make([]RunRecord, 0, len(s.runs))
	now := time.Now().UTC()
	for id, record := range s.runs {
		if record.Status != RunStatusRunning || record.HeartbeatAt.IsZero() || !record.HeartbeatAt.Before(staleBefore) {
			continue
		}
		record.Status = RunStatusFailed
		record.Error = reason
		record.CompletedAt = now
		record.UpdatedAt = now
		s.runs[id] = record
		failed = append(failed, cloneRunRecord(record))
	}
	return failed, nil
}

// EnqueueRun persists one queued durable run without starting execution.
func (s Stack) EnqueueRun(ctx context.Context, prompt string, scope tenant.Scope) (RunRecord, error) {
	if err := ctx.Err(); err != nil {
		return RunRecord{}, err
	}
	if s.runs == nil {
		return RunRecord{}, ErrRunStoreRequired
	}
	if s.audit.Sink != nil {
		ctx = memaxagent.WithEventObserver(ctx, s.audit)
	}
	record, err := s.runs.CreateRun(ctx, CreateRunRequest{
		Prompt: prompt,
		Tenant: scope,
	})
	if err != nil {
		return RunRecord{}, fmt.Errorf("create managed run: %w", err)
	}
	observeRunState(ctx, record)
	return record, nil
}

// StartRun begins one durable background run and persists lifecycle transitions
// into the configured RunStore. The run executes on a detached context so it is
// not canceled when the caller's request context ends. Once started, the run
// continues until it terminates naturally or CancelRun requests explicit
// host-driven interruption.
func (s Stack) StartRun(ctx context.Context, prompt string, scope tenant.Scope) (RunRecord, error) {
	record, err := s.EnqueueRun(ctx, prompt, scope)
	if err != nil {
		return RunRecord{}, err
	}
	if _, ok := s.runs.(RunStoreWithClaim); !ok {
		return RunRecord{}, ErrRunStoreClaimRequired
	}
	runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	if s.active != nil {
		s.active.set(record.ID, cancel)
	}
	go func() {
		defer cancel()
		_, _ = s.ExecuteRun(runCtx, record.ID, WorkerOptions{})
	}()
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

// ExecuteRun claims one queued run for the worker identified by options and
// executes it synchronously. This is the foundation remote-execution seam:
// workers explicitly dequeue by run ID, and a dead worker is expected to be
// surfaced as a failed run via heartbeat timeout rather than implicit resume.
func (s Stack) ExecuteRun(ctx context.Context, runID string, options WorkerOptions) (RunRecord, error) {
	if err := ctx.Err(); err != nil {
		return RunRecord{}, err
	}
	if s.runs == nil {
		return RunRecord{}, ErrRunStoreRequired
	}
	if s.audit.Sink != nil {
		ctx = memaxagent.WithEventObserver(ctx, s.audit)
	}
	claiming, ok := s.runs.(RunStoreWithClaim)
	if !ok {
		return RunRecord{}, ErrRunStoreClaimRequired
	}
	record, err := claiming.ClaimRun(ctx, runID, options.ID)
	if err != nil {
		return RunRecord{}, fmt.Errorf("claim managed run %s: %w", runID, err)
	}
	observeRunState(ctx, record)

	stopHeartbeat := startRunHeartbeat(ctx, s.runs, record.ID, options)
	if stopHeartbeat != nil {
		defer stopHeartbeat()
	}
	return s.executeRun(ctx, record)
}

// FailStaleRuns marks running runs whose persisted heartbeat is older than
// staleBefore as failed. Hosts can use this from their own liveness monitor to
// turn worker death into explicit terminal run state.
func (s Stack) FailStaleRuns(ctx context.Context, staleBefore time.Time, reason string) (int64, error) {
	if s.runs == nil {
		return 0, ErrRunStoreRequired
	}
	heartbeats, ok := s.runs.(RunStoreWithHeartbeat)
	if !ok {
		return 0, ErrRunStoreHeartbeatUnsupported
	}
	if s.audit.Sink != nil {
		ctx = memaxagent.WithEventObserver(ctx, s.audit)
	}
	failed, err := heartbeats.FailStaleRuns(ctx, staleBefore, reason)
	if err != nil {
		return 0, fmt.Errorf("fail stale managed runs: %w", err)
	}
	for _, record := range failed {
		observeRunState(ctx, record)
	}
	return int64(len(failed)), nil
}

// WatchStaleRuns continuously fails worker-owned runs whose heartbeats age
// past staleThreshold. Cancel ctx to stop the monitoring loop.
func (s Stack) WatchStaleRuns(ctx context.Context, interval, staleThreshold time.Duration) error {
	if interval <= 0 {
		return fmt.Errorf("stale run watch interval must be positive")
	}
	if staleThreshold <= 0 {
		return fmt.Errorf("stale run threshold must be positive")
	}
	check := func() error {
		_, err := s.FailStaleRuns(ctx, time.Now().UTC().Add(-staleThreshold), staleRunFailureReason)
		return err
	}
	if err := check(); err != nil {
		return err
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := check(); err != nil {
				return err
			}
		}
	}
}

func (s Stack) executeRun(ctx context.Context, record RunRecord) (RunRecord, error) {
	if s.active != nil {
		defer s.active.delete(record.ID)
	}
	var (
		finalResult string
		sawResult   bool
		runErr      error
	)
	for event := range s.QueryAsync(ctx, record.Prompt, record.Tenant) {
		switch event.Kind {
		case memaxagent.EventSessionStarted:
			record, _ = s.runs.UpdateRun(ctx, RunUpdate{
				ID:              record.ID,
				SessionID:       event.SessionID,
				ParentSessionID: event.ParentSessionID,
			})
		case memaxagent.EventResult:
			finalResult = event.Result
			sawResult = true
		case memaxagent.EventError:
			runErr = event.Err
		}
	}
	current, err := s.runs.GetRun(context.Background(), record.ID)
	if err == nil && current.Terminal() {
		switch current.Status {
		case RunStatusSucceeded, RunStatusCanceled:
			return current, nil
		case RunStatusFailed:
			return current, errors.New(current.Error)
		}
	}

	now := time.Now().UTC()
	update := RunUpdate{
		ID:              record.ID,
		SessionID:       record.SessionID,
		ParentSessionID: record.ParentSessionID,
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
	record, _ = s.runs.UpdateRun(ctx, update)
	observeRunState(ctx, record)
	switch record.Status {
	case RunStatusSucceeded, RunStatusCanceled:
		return record, nil
	case RunStatusFailed:
		return record, errors.New(record.Error)
	default:
		return record, nil
	}
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

func startRunHeartbeat(ctx context.Context, store RunStore, runID string, options WorkerOptions) func() {
	if options.ID == "" {
		return nil
	}
	heartbeats, ok := store.(RunStoreWithHeartbeat)
	if !ok {
		return nil
	}
	interval := options.HeartbeatInterval
	if interval <= 0 {
		interval = 5 * time.Second
	}
	ctx, cancel := context.WithCancel(ctx)
	ticker := time.NewTicker(interval)
	go func() {
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				_, _ = heartbeats.HeartbeatRun(context.Background(), runID, options.ID)
			}
		}
	}()
	return cancel
}

func observeRunState(ctx context.Context, record RunRecord) {
	if record.ID == "" || record.Status == "" {
		return
	}
	event := memaxagent.Event{
		Kind:            memaxagent.EventRunStateChanged,
		SessionID:       record.SessionID,
		ParentSessionID: record.ParentSessionID,
		Time:            record.UpdatedAt,
		Run: &memaxagent.RunEvent{
			RunID:  record.ID,
			Status: string(record.Status),
			Prompt: record.Prompt,
		},
	}
	memaxagent.ObserveEvent(ctx, event)
}
