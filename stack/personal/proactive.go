package personal

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	memaxagent "github.com/MemaxLabs/memax-go-agent-sdk"
)

// ErrScheduledRunStoreRequired reports that proactive scheduled execution
// needs an explicit store.
var ErrScheduledRunStoreRequired = errors.New("personal stack: scheduled run store is required")

// ErrScheduledRunStoreStaleUnsupported reports that stale scheduled-run
// reconciliation needs a store that can atomically fail old queued/running
// records.
var ErrScheduledRunStoreStaleUnsupported = errors.New("personal stack: scheduled run store does not support stale reconciliation")

// ErrScheduledWorkflowRegistryRequired reports that proactive workflow
// execution needs an explicit workflow registry.
var ErrScheduledWorkflowRegistryRequired = errors.New("personal stack: scheduled workflow registry is required")

// ErrScheduledWorkflowNotFound reports that one requested scheduled workflow
// does not exist in a registry.
var ErrScheduledWorkflowNotFound = errors.New("personal stack: scheduled workflow not found")

// ErrScheduledRunNotFound reports that one scheduled run record does not
// exist.
var ErrScheduledRunNotFound = errors.New("personal stack: scheduled run not found")

// ScheduledRunStatus is one proactive scheduled-run lifecycle state.
type ScheduledRunStatus string

const (
	ScheduledRunQueued    ScheduledRunStatus = "queued"
	ScheduledRunRunning   ScheduledRunStatus = "running"
	ScheduledRunSucceeded ScheduledRunStatus = "succeeded"
	ScheduledRunFailed    ScheduledRunStatus = "failed"
)

// ScheduledRunRecord is one durable proactive-run snapshot keyed by a
// deterministic trigger occurrence.
type ScheduledRunRecord struct {
	ID           string
	TriggerName  string
	OccurrenceAt time.Time
	Prompt       string
	Status       ScheduledRunStatus
	SessionID    string
	Result       string
	Error        string
	CreatedAt    time.Time
	StartedAt    time.Time
	CompletedAt  time.Time
	UpdatedAt    time.Time
}

// Terminal reports whether r is finished.
func (r ScheduledRunRecord) Terminal() bool {
	switch r.Status {
	case ScheduledRunSucceeded, ScheduledRunFailed:
		return true
	default:
		return false
	}
}

// CreateScheduledRunRequest creates one queued proactive run keyed by a
// deterministic occurrence ID.
type CreateScheduledRunRequest struct {
	ID           string
	TriggerName  string
	OccurrenceAt time.Time
	Prompt       string
}

// ScheduledRunUpdate applies one partial lifecycle update to a scheduled run.
type ScheduledRunUpdate struct {
	ID          string
	Status      ScheduledRunStatus
	SessionID   string
	Result      *string
	Error       *string
	CompletedAt *time.Time
}

// ScheduledRunStore persists proactive scheduled-run state.
//
// This is intentionally separate from cloudmanaged.RunStore: the personal
// stack's distinctive contract is idempotency by deterministic trigger intent,
// not multi-tenant worker claiming or heartbeat-based liveness.
//
// Implementations must treat terminal records as immutable. An
// UpdateScheduledRun call against a terminal record should return the existing
// record unchanged without reporting an error.
type ScheduledRunStore interface {
	CreateScheduledRun(context.Context, CreateScheduledRunRequest) (ScheduledRunRecord, bool, error)
	UpdateScheduledRun(context.Context, ScheduledRunUpdate) (ScheduledRunRecord, error)
	GetScheduledRun(context.Context, string) (ScheduledRunRecord, error)
}

// ScheduledRunStoreWithStaleReconciliation atomically marks stale queued or
// running scheduled runs as failed. Stores use UpdatedAt as the liveness clock:
// queued records that never transition to running and running records that
// never reach a terminal update both become explicit failed runs. The
// reconciliation must be atomic with UpdateScheduledRun so a late executor
// cannot overwrite a reconciled terminal failure.
type ScheduledRunStoreWithStaleReconciliation interface {
	FailStaleScheduledRuns(context.Context, time.Time, string) ([]ScheduledRunRecord, error)
}

// MemoryScheduledRunStore is the reference in-memory scheduled-run backend.
type MemoryScheduledRunStore struct {
	mu   sync.RWMutex
	runs map[string]ScheduledRunRecord
}

// NewMemoryScheduledRunStore constructs a reference in-memory scheduled-run
// backend.
func NewMemoryScheduledRunStore() *MemoryScheduledRunStore {
	return &MemoryScheduledRunStore{runs: make(map[string]ScheduledRunRecord)}
}

// CreateScheduledRun implements ScheduledRunStore.
func (s *MemoryScheduledRunStore) CreateScheduledRun(_ context.Context, req CreateScheduledRunRequest) (ScheduledRunRecord, bool, error) {
	if s == nil {
		return ScheduledRunRecord{}, false, fmt.Errorf("memory scheduled run store is nil")
	}
	if strings.TrimSpace(req.ID) == "" {
		return ScheduledRunRecord{}, false, fmt.Errorf("scheduled run id is required")
	}
	if strings.TrimSpace(req.TriggerName) == "" {
		return ScheduledRunRecord{}, false, fmt.Errorf("scheduled trigger name is required")
	}
	if strings.TrimSpace(req.Prompt) == "" {
		return ScheduledRunRecord{}, false, fmt.Errorf("scheduled prompt is required")
	}
	now := time.Now().UTC()
	record := ScheduledRunRecord{
		ID:           strings.TrimSpace(req.ID),
		TriggerName:  strings.TrimSpace(req.TriggerName),
		OccurrenceAt: req.OccurrenceAt.UTC(),
		Prompt:       req.Prompt,
		Status:       ScheduledRunQueued,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.runs[record.ID]; ok {
		return cloneScheduledRunRecord(existing), false, nil
	}
	s.runs[record.ID] = record
	return cloneScheduledRunRecord(record), true, nil
}

// UpdateScheduledRun implements ScheduledRunStore.
func (s *MemoryScheduledRunStore) UpdateScheduledRun(_ context.Context, update ScheduledRunUpdate) (ScheduledRunRecord, error) {
	if s == nil {
		return ScheduledRunRecord{}, fmt.Errorf("memory scheduled run store is nil")
	}
	if strings.TrimSpace(update.ID) == "" {
		return ScheduledRunRecord{}, fmt.Errorf("scheduled run id is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.runs[update.ID]
	if !ok {
		return ScheduledRunRecord{}, ErrScheduledRunNotFound
	}
	if record.Terminal() {
		return cloneScheduledRunRecord(record), nil
	}
	if update.Status != "" {
		record.Status = update.Status
		if update.Status == ScheduledRunRunning && record.StartedAt.IsZero() {
			record.StartedAt = time.Now().UTC()
		}
	}
	if update.SessionID != "" {
		record.SessionID = update.SessionID
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
	s.runs[record.ID] = record
	return cloneScheduledRunRecord(record), nil
}

// GetScheduledRun implements ScheduledRunStore.
func (s *MemoryScheduledRunStore) GetScheduledRun(_ context.Context, id string) (ScheduledRunRecord, error) {
	if s == nil {
		return ScheduledRunRecord{}, fmt.Errorf("memory scheduled run store is nil")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	record, ok := s.runs[id]
	if !ok {
		return ScheduledRunRecord{}, ErrScheduledRunNotFound
	}
	return cloneScheduledRunRecord(record), nil
}

// FailStaleScheduledRuns implements ScheduledRunStoreWithStaleReconciliation.
func (s *MemoryScheduledRunStore) FailStaleScheduledRuns(ctx context.Context, staleBefore time.Time, reason string) ([]ScheduledRunRecord, error) {
	if s == nil {
		return nil, fmt.Errorf("memory scheduled run store is nil")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var failed []ScheduledRunRecord
	now := time.Now().UTC()
	for id, record := range s.runs {
		if record.Status != ScheduledRunQueued && record.Status != ScheduledRunRunning {
			continue
		}
		if !record.UpdatedAt.Before(staleBefore) {
			continue
		}
		record.Status = ScheduledRunFailed
		record.Error = reason
		record.CompletedAt = now
		record.UpdatedAt = now
		s.runs[id] = record
		failed = append(failed, cloneScheduledRunRecord(record))
	}
	return failed, nil
}

// ScheduledIntent identifies one deterministic proactive run occurrence.
type ScheduledIntent struct {
	ID           string
	TriggerName  string
	OccurrenceAt time.Time
	Prompt       string
}

// ScheduledTrigger resolves whether a proactive workflow should fire at now.
// Stack trigger evaluators pass now as UTC.
type ScheduledTrigger interface {
	IntentAt(now time.Time) (ScheduledIntent, bool)
}

// ScheduledWorkflow describes one named proactive workflow exposed by a host.
// Names are trimmed, exact, and case-sensitive identifiers. Trigger
// implementations should be immutable or concurrency-safe because registries
// return shallow copies of the trigger value. The registry is intentionally
// separate from ScheduledRunStore: it is discoverable workflow configuration,
// while ScheduledRunStore is durable execution state.
type ScheduledWorkflow struct {
	Name        string
	Description string
	Tags        []string
	Trigger     ScheduledTrigger
}

// ScheduledWorkflowRegistry lists host-owned proactive workflows.
type ScheduledWorkflowRegistry interface {
	ListScheduledWorkflows(context.Context) ([]ScheduledWorkflow, error)
}

// MemoryScheduledWorkflowRegistry is the reference in-memory scheduled
// workflow registry.
type MemoryScheduledWorkflowRegistry struct {
	mu        sync.RWMutex
	workflows []ScheduledWorkflow
}

// NewMemoryScheduledWorkflowRegistry constructs a reference in-memory
// scheduled workflow registry.
func NewMemoryScheduledWorkflowRegistry(workflows ...ScheduledWorkflow) (*MemoryScheduledWorkflowRegistry, error) {
	normalized, err := normalizeScheduledWorkflows(workflows)
	if err != nil {
		return nil, err
	}
	return &MemoryScheduledWorkflowRegistry{workflows: normalized}, nil
}

// ListScheduledWorkflows implements ScheduledWorkflowRegistry.
func (r *MemoryScheduledWorkflowRegistry) ListScheduledWorkflows(_ context.Context) ([]ScheduledWorkflow, error) {
	if r == nil {
		return nil, fmt.Errorf("memory scheduled workflow registry is nil")
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return cloneScheduledWorkflows(r.workflows), nil
}

// ScheduledFireResult reports the outcome of evaluating one due scheduled
// trigger.
type ScheduledFireResult struct {
	Intent  ScheduledIntent
	Record  ScheduledRunRecord
	Created bool
}

// ScheduledWorkflowFireResult reports the outcome of firing one workflow
// occurrence from a ScheduledWorkflowRegistry.
type ScheduledWorkflowFireResult struct {
	Workflow ScheduledWorkflow
	Fire     ScheduledFireResult
}

// PeriodicTrigger fires one deterministic occurrence each time its cadence
// window is crossed. Idempotency is handled by the ScheduledRunStore, so the
// same occurrence can be re-evaluated safely on every watcher tick.
type PeriodicTrigger struct {
	Name   string
	Prompt string
	Every  time.Duration
	Anchor time.Time
}

// IntentAt implements ScheduledTrigger.
func (t PeriodicTrigger) IntentAt(now time.Time) (ScheduledIntent, bool) {
	name := strings.TrimSpace(t.Name)
	prompt := strings.TrimSpace(t.Prompt)
	if name == "" || prompt == "" || t.Every <= 0 || t.Anchor.IsZero() {
		return ScheduledIntent{}, false
	}
	now = now.UTC()
	anchor := t.Anchor.UTC()
	if now.Before(anchor) {
		return ScheduledIntent{}, false
	}
	slot := time.Duration(int64(now.Sub(anchor) / t.Every))
	occurrence := anchor.Add(slot * t.Every).UTC()
	return ScheduledIntent{
		ID:           fmt.Sprintf("%s:%s", name, occurrence.Format(time.RFC3339)),
		TriggerName:  name,
		OccurrenceAt: occurrence,
		Prompt:       prompt,
	}, true
}

// TriggerWatcherOptions configure one proactive trigger watcher loop.
type TriggerWatcherOptions struct {
	Interval time.Duration
	Now      func() time.Time
}

const staleScheduledRunFailureReason = "scheduled run stale timeout"

func (o TriggerWatcherOptions) withDefaults() TriggerWatcherOptions {
	if o.Now == nil {
		o.Now = time.Now
	}
	return o
}

// StartScheduledRun persists one queued proactive run and starts it on a
// detached context. If the occurrence already exists, the existing record is
// returned and created=false.
func (s Stack) StartScheduledRun(ctx context.Context, store ScheduledRunStore, intent ScheduledIntent) (record ScheduledRunRecord, created bool, err error) {
	if store == nil {
		return ScheduledRunRecord{}, false, ErrScheduledRunStoreRequired
	}
	record, created, err = store.CreateScheduledRun(ctx, CreateScheduledRunRequest{
		ID:           strings.TrimSpace(intent.ID),
		TriggerName:  strings.TrimSpace(intent.TriggerName),
		OccurrenceAt: intent.OccurrenceAt,
		Prompt:       strings.TrimSpace(intent.Prompt),
	})
	if err != nil || !created {
		return record, created, err
	}
	observeScheduledRunState(ctx, record)
	runCtx := context.WithoutCancel(ctx)
	go s.executeScheduledRun(runCtx, store, record)
	return record, true, nil
}

// FireScheduledTriggers evaluates triggers once at now and starts due
// proactive runs that have not already been recorded. This is the one-shot
// counterpart to WatchScheduledTriggers for cron jobs, serverless handlers,
// examples, and tests that already have an external scheduler. If one trigger
// fails after earlier triggers started, the returned results include the
// successful earlier fires.
func (s Stack) FireScheduledTriggers(ctx context.Context, store ScheduledRunStore, now time.Time, triggers ...ScheduledTrigger) ([]ScheduledFireResult, error) {
	if store == nil {
		return nil, ErrScheduledRunStoreRequired
	}
	if len(triggers) == 0 {
		return nil, fmt.Errorf("at least one scheduled trigger is required")
	}
	var results []ScheduledFireResult
	for _, trigger := range triggers {
		intent, due := trigger.IntentAt(now.UTC())
		if !due {
			continue
		}
		record, created, err := s.StartScheduledRun(ctx, store, intent)
		if err != nil {
			return results, err
		}
		results = append(results, ScheduledFireResult{
			Intent:  intent,
			Record:  record,
			Created: created,
		})
	}
	return results, nil
}

// FireScheduledWorkflows lists workflows from registry, evaluates the selected
// workflows once at now, and starts due proactive runs that have not already
// been recorded. If names is empty, all registered workflows are evaluated. If
// the selected workflow set is empty, an error is returned. If one workflow
// fails after earlier workflows started, the returned results include the
// successful earlier fires.
func (s Stack) FireScheduledWorkflows(ctx context.Context, store ScheduledRunStore, registry ScheduledWorkflowRegistry, now time.Time, names ...string) ([]ScheduledWorkflowFireResult, error) {
	if store == nil {
		return nil, ErrScheduledRunStoreRequired
	}
	if registry == nil {
		return nil, ErrScheduledWorkflowRegistryRequired
	}
	workflows, err := registry.ListScheduledWorkflows(ctx)
	if err != nil {
		return nil, fmt.Errorf("list scheduled workflows: %w", err)
	}
	selected, err := selectScheduledWorkflows(workflows, names)
	if err != nil {
		return nil, err
	}
	if len(selected) == 0 {
		return nil, fmt.Errorf("at least one scheduled workflow is required")
	}
	var results []ScheduledWorkflowFireResult
	for _, workflow := range selected {
		fires, err := s.FireScheduledTriggers(ctx, store, now, workflow.Trigger)
		for _, fire := range fires {
			results = append(results, ScheduledWorkflowFireResult{
				Workflow: cloneScheduledWorkflow(workflow),
				Fire:     fire,
			})
		}
		if err != nil {
			return results, err
		}
	}
	return results, nil
}

// WatchScheduledTriggers polls the supplied triggers on a ticker and starts
// any due proactive runs. Cancel ctx to stop the watcher.
func (s Stack) WatchScheduledTriggers(ctx context.Context, store ScheduledRunStore, options TriggerWatcherOptions, triggers ...ScheduledTrigger) error {
	if store == nil {
		return ErrScheduledRunStoreRequired
	}
	if len(triggers) == 0 {
		return fmt.Errorf("at least one scheduled trigger is required")
	}
	options = options.withDefaults()
	if options.Interval <= 0 {
		return fmt.Errorf("scheduled trigger interval must be positive")
	}
	fire := func(now time.Time) error {
		_, err := s.FireScheduledTriggers(ctx, store, now, triggers...)
		return err
	}
	if err := fire(options.Now().UTC()); err != nil {
		return err
	}
	ticker := time.NewTicker(options.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := fire(options.Now().UTC()); err != nil {
				return err
			}
		}
	}
}

// FailStaleScheduledRuns marks queued or running scheduled runs whose UpdatedAt
// is older than staleBefore as failed, then emits lifecycle events for the
// failed records. Use it from host-owned reconciliation jobs when a process may
// have died after creating a scheduled occurrence or while running it.
func (s Stack) FailStaleScheduledRuns(ctx context.Context, store ScheduledRunStore, staleBefore time.Time, reason string) (int64, error) {
	if store == nil {
		return 0, ErrScheduledRunStoreRequired
	}
	reconciler, ok := store.(ScheduledRunStoreWithStaleReconciliation)
	if !ok {
		return 0, ErrScheduledRunStoreStaleUnsupported
	}
	failed, err := reconciler.FailStaleScheduledRuns(ctx, staleBefore, reason)
	if err != nil {
		return 0, fmt.Errorf("fail stale scheduled runs: %w", err)
	}
	for _, record := range failed {
		observeScheduledRunState(ctx, record)
	}
	return int64(len(failed)), nil
}

// WatchStaleScheduledRuns continuously fails queued or running scheduled runs
// whose UpdatedAt ages past staleThreshold. Cancel ctx to stop the watcher.
func (s Stack) WatchStaleScheduledRuns(ctx context.Context, store ScheduledRunStore, interval, staleThreshold time.Duration) error {
	if store == nil {
		return ErrScheduledRunStoreRequired
	}
	if interval <= 0 {
		return fmt.Errorf("stale scheduled run watch interval must be positive")
	}
	if staleThreshold <= 0 {
		return fmt.Errorf("stale scheduled run threshold must be positive")
	}
	check := func() error {
		_, err := s.FailStaleScheduledRuns(ctx, store, time.Now().UTC().Add(-staleThreshold), staleScheduledRunFailureReason)
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

func (s Stack) executeScheduledRun(ctx context.Context, store ScheduledRunStore, record ScheduledRunRecord) {
	record, err := store.UpdateScheduledRun(ctx, ScheduledRunUpdate{
		ID:     record.ID,
		Status: ScheduledRunRunning,
	})
	if err != nil {
		return
	}
	if record.Status != ScheduledRunRunning {
		return
	}
	observeScheduledRunState(ctx, record)

	events := memaxagent.QueryAsync(ctx, record.Prompt, s.options)
	var (
		finalResult string
		runErr      error
	)
	for event := range events {
		switch event.Kind {
		case memaxagent.EventSessionStarted:
			record, _ = store.UpdateScheduledRun(ctx, ScheduledRunUpdate{
				ID:        record.ID,
				SessionID: event.SessionID,
			})
		case memaxagent.EventResult:
			finalResult = event.Result
		case memaxagent.EventError:
			runErr = event.Err
		}
	}
	current, err := store.GetScheduledRun(context.Background(), record.ID)
	if err == nil && current.Terminal() {
		return
	}

	now := time.Now().UTC()
	update := ScheduledRunUpdate{
		ID:          record.ID,
		CompletedAt: &now,
	}
	if runErr != nil {
		update.Status = ScheduledRunFailed
		errText := runErr.Error()
		update.Error = &errText
	} else {
		update.Status = ScheduledRunSucceeded
		update.Result = &finalResult
	}
	record, err = store.UpdateScheduledRun(ctx, update)
	if err == nil && record.Status == update.Status {
		observeScheduledRunState(ctx, record)
	}
}

func cloneScheduledRunRecord(record ScheduledRunRecord) ScheduledRunRecord {
	return record
}

func observeScheduledRunState(ctx context.Context, record ScheduledRunRecord) {
	if record.ID == "" || record.Status == "" {
		return
	}
	var errText string
	if record.Status == ScheduledRunFailed {
		errText = record.Error
	}
	event := memaxagent.Event{
		Kind:      memaxagent.EventRunStateChanged,
		SessionID: record.SessionID,
		Time:      record.UpdatedAt,
		Run: &memaxagent.RunEvent{
			RunID:        record.ID,
			Status:       string(record.Status),
			Prompt:       record.Prompt,
			TriggerName:  record.TriggerName,
			OccurrenceAt: record.OccurrenceAt,
			Error:        errText,
		},
	}
	memaxagent.ObserveEvent(ctx, event)
}

func normalizeScheduledWorkflows(workflows []ScheduledWorkflow) ([]ScheduledWorkflow, error) {
	normalized := make([]ScheduledWorkflow, 0, len(workflows))
	seen := make(map[string]struct{}, len(workflows))
	for _, workflow := range workflows {
		workflow, err := normalizeScheduledWorkflow(workflow)
		if err != nil {
			return nil, err
		}
		if _, ok := seen[workflow.Name]; ok {
			return nil, fmt.Errorf("duplicate scheduled workflow %q", workflow.Name)
		}
		seen[workflow.Name] = struct{}{}
		normalized = append(normalized, workflow)
	}
	return normalized, nil
}

func normalizeScheduledWorkflow(workflow ScheduledWorkflow) (ScheduledWorkflow, error) {
	name := strings.TrimSpace(workflow.Name)
	if name == "" {
		return ScheduledWorkflow{}, fmt.Errorf("scheduled workflow name is required")
	}
	if workflow.Trigger == nil {
		return ScheduledWorkflow{}, fmt.Errorf("scheduled workflow %q trigger is required", name)
	}
	workflow.Name = name
	workflow.Description = strings.TrimSpace(workflow.Description)
	workflow.Tags = cloneScheduledWorkflowTags(workflow.Tags)
	return workflow, nil
}

func selectScheduledWorkflows(workflows []ScheduledWorkflow, names []string) ([]ScheduledWorkflow, error) {
	normalized, err := normalizeScheduledWorkflows(workflows)
	if err != nil {
		return nil, err
	}
	if len(names) == 0 {
		return normalized, nil
	}
	byName := make(map[string]ScheduledWorkflow, len(normalized))
	for _, workflow := range normalized {
		byName[workflow.Name] = workflow
	}
	selected := make([]ScheduledWorkflow, 0, len(names))
	seen := make(map[string]struct{}, len(names))
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			return nil, fmt.Errorf("scheduled workflow name is required")
		}
		if _, ok := seen[name]; ok {
			return nil, fmt.Errorf("duplicate scheduled workflow selection %q", name)
		}
		workflow, ok := byName[name]
		if !ok {
			return nil, fmt.Errorf("%w: %s", ErrScheduledWorkflowNotFound, name)
		}
		seen[name] = struct{}{}
		selected = append(selected, workflow)
	}
	return selected, nil
}

func cloneScheduledWorkflows(workflows []ScheduledWorkflow) []ScheduledWorkflow {
	if len(workflows) == 0 {
		return nil
	}
	cloned := make([]ScheduledWorkflow, 0, len(workflows))
	for _, workflow := range workflows {
		cloned = append(cloned, cloneScheduledWorkflow(workflow))
	}
	return cloned
}

func cloneScheduledWorkflow(workflow ScheduledWorkflow) ScheduledWorkflow {
	workflow.Tags = cloneScheduledWorkflowTags(workflow.Tags)
	return workflow
}

func cloneScheduledWorkflowTags(tags []string) []string {
	if len(tags) == 0 {
		return nil
	}
	cloned := make([]string, 0, len(tags))
	for _, tag := range tags {
		tag = strings.TrimSpace(tag)
		if tag != "" {
			cloned = append(cloned, tag)
		}
	}
	return cloned
}
