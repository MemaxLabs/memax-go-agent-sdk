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

// ErrScheduledRunNotificationStoreRequired reports that scheduled-run
// notification mirroring needs an explicit host-owned store.
var ErrScheduledRunNotificationStoreRequired = errors.New("personal stack: scheduled run notification store is required")

// ScheduledRunNotificationPolicy controls which scheduled-run lifecycle events
// become outbox notifications.
type ScheduledRunNotificationPolicy string

const (
	// ScheduledRunNotifyDoneOnly creates notifications only for terminal
	// scheduled-run states.
	ScheduledRunNotifyDoneOnly ScheduledRunNotificationPolicy = "done_only"
	// ScheduledRunNotifyStateChanges creates notifications for every
	// scheduled-run lifecycle transition.
	ScheduledRunNotifyStateChanges ScheduledRunNotificationPolicy = "state_changes"
	// ScheduledRunNotifySilent disables scheduled-run notifications while still
	// allowing lifecycle events to flow to other observers.
	ScheduledRunNotifySilent ScheduledRunNotificationPolicy = "silent"
)

// ScheduledRunNotificationRecord is one outbox notification derived from a
// proactive scheduled-run lifecycle event. The ID is deterministic per
// run/status pair, so repeated observer delivery does not create duplicate
// notifications for the same transition.
type ScheduledRunNotificationRecord struct {
	ID           string
	RunID        string
	Status       ScheduledRunStatus
	TriggerName  string
	OccurrenceAt time.Time
	Prompt       string
	Result       string
	Error        string
	CreatedAt    time.Time
}

// CreateScheduledRunNotificationRequest creates one scheduled-run notification
// outbox record.
type CreateScheduledRunNotificationRequest struct {
	ID           string
	RunID        string
	Status       ScheduledRunStatus
	TriggerName  string
	OccurrenceAt time.Time
	Prompt       string
	Result       string
	Error        string
	CreatedAt    time.Time
}

// ScheduledRunNotificationFilter filters notification outbox reads.
type ScheduledRunNotificationFilter struct {
	RunID  string
	Status ScheduledRunStatus
	Limit  int
}

// ScheduledRunNotificationStore persists host-owned scheduled-run notification
// outbox records. Implementations should treat CreateScheduledRunNotification
// as idempotent by ID, returning created=false with the existing record when a
// notification was already recorded. List ordering is backend-defined; callers
// that need a portable order should sort by CreatedAt and ID.
type ScheduledRunNotificationStore interface {
	CreateScheduledRunNotification(context.Context, CreateScheduledRunNotificationRequest) (ScheduledRunNotificationRecord, bool, error)
	ListScheduledRunNotifications(context.Context, ScheduledRunNotificationFilter) ([]ScheduledRunNotificationRecord, error)
}

// MemoryScheduledRunNotificationStore is the reference in-memory notification
// outbox backend.
type MemoryScheduledRunNotificationStore struct {
	mu   sync.RWMutex
	byID map[string]ScheduledRunNotificationRecord
	ids  []string
}

// NewMemoryScheduledRunNotificationStore constructs a reference in-memory
// scheduled-run notification outbox.
func NewMemoryScheduledRunNotificationStore() *MemoryScheduledRunNotificationStore {
	return &MemoryScheduledRunNotificationStore{byID: make(map[string]ScheduledRunNotificationRecord)}
}

// CreateScheduledRunNotification implements ScheduledRunNotificationStore.
func (s *MemoryScheduledRunNotificationStore) CreateScheduledRunNotification(ctx context.Context, req CreateScheduledRunNotificationRequest) (ScheduledRunNotificationRecord, bool, error) {
	if s == nil {
		return ScheduledRunNotificationRecord{}, false, fmt.Errorf("memory scheduled run notification store is nil")
	}
	if err := ctx.Err(); err != nil {
		return ScheduledRunNotificationRecord{}, false, err
	}
	record, err := req.Normalize()
	if err != nil {
		return ScheduledRunNotificationRecord{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.byID[record.ID]; ok {
		return cloneScheduledRunNotification(existing), false, nil
	}
	s.byID[record.ID] = record
	s.ids = append(s.ids, record.ID)
	return cloneScheduledRunNotification(record), true, nil
}

// ListScheduledRunNotifications implements ScheduledRunNotificationStore.
func (s *MemoryScheduledRunNotificationStore) ListScheduledRunNotifications(ctx context.Context, filter ScheduledRunNotificationFilter) ([]ScheduledRunNotificationRecord, error) {
	if s == nil {
		return nil, fmt.Errorf("memory scheduled run notification store is nil")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	notifications := make([]ScheduledRunNotificationRecord, 0, len(s.ids))
	for _, id := range s.ids {
		record := s.byID[id]
		if filter.RunID != "" && record.RunID != filter.RunID {
			continue
		}
		if filter.Status != "" && record.Status != filter.Status {
			continue
		}
		notifications = append(notifications, cloneScheduledRunNotification(record))
		if filter.Limit > 0 && len(notifications) >= filter.Limit {
			break
		}
	}
	return notifications, nil
}

type scheduledRunNotifierConfig struct {
	policy  ScheduledRunNotificationPolicy
	onError func(context.Context, CreateScheduledRunNotificationRequest, error)
}

// ScheduledRunNotifierOption configures a scheduled-run notification observer.
type ScheduledRunNotifierOption func(*scheduledRunNotifierConfig)

// WithScheduledRunNotificationPolicy sets which scheduled-run lifecycle events
// become outbox notifications. The default is ScheduledRunNotifyDoneOnly.
func WithScheduledRunNotificationPolicy(policy ScheduledRunNotificationPolicy) ScheduledRunNotifierOption {
	return func(config *scheduledRunNotifierConfig) {
		config.policy = policy
	}
}

// WithScheduledRunNotificationErrorHandler observes non-fatal outbox write
// errors. The handler runs synchronously on the observer path, so keep it fast
// and non-blocking.
func WithScheduledRunNotificationErrorHandler(handler func(context.Context, CreateScheduledRunNotificationRequest, error)) ScheduledRunNotifierOption {
	return func(config *scheduledRunNotifierConfig) {
		config.onError = handler
	}
}

// ScheduledRunNotifier mirrors personal scheduled-run lifecycle events into a
// host-owned notification outbox. It implements memaxagent.EventObserver and
// should be attached with memaxagent.WithEventObserver. Notification writes are
// observer-side effects: failures are reported to the optional error handler and
// never change the primary scheduled run.
type ScheduledRunNotifier struct {
	store   ScheduledRunNotificationStore
	policy  ScheduledRunNotificationPolicy
	onError func(context.Context, CreateScheduledRunNotificationRequest, error)
}

var _ memaxagent.EventObserver = (*ScheduledRunNotifier)(nil)

// NewScheduledRunNotifier constructs an event observer that records
// scheduled-run notifications in store.
func NewScheduledRunNotifier(store ScheduledRunNotificationStore, options ...ScheduledRunNotifierOption) (*ScheduledRunNotifier, error) {
	if store == nil {
		return nil, ErrScheduledRunNotificationStoreRequired
	}
	config := scheduledRunNotifierConfig{
		policy: ScheduledRunNotifyDoneOnly,
	}
	for _, option := range options {
		if option != nil {
			option(&config)
		}
	}
	switch config.policy {
	case ScheduledRunNotifyDoneOnly, ScheduledRunNotifyStateChanges, ScheduledRunNotifySilent:
	default:
		return nil, fmt.Errorf("personal stack: unknown scheduled run notification policy %q", config.policy)
	}
	return &ScheduledRunNotifier{
		store:   store,
		policy:  config.policy,
		onError: config.onError,
	}, nil
}

// ObserveEvent implements memaxagent.EventObserver.
func (n *ScheduledRunNotifier) ObserveEvent(ctx context.Context, event memaxagent.Event) {
	if n == nil || n.store == nil || n.policy == ScheduledRunNotifySilent {
		return
	}
	req, ok := scheduledRunNotificationFromEvent(event)
	if !ok || !n.shouldNotify(req.Status) {
		return
	}
	if _, _, err := n.store.CreateScheduledRunNotification(ctx, req); err != nil && n.onError != nil {
		n.onError(ctx, req, err)
	}
}

func (n *ScheduledRunNotifier) shouldNotify(status ScheduledRunStatus) bool {
	switch n.policy {
	case ScheduledRunNotifyStateChanges:
		return true
	case ScheduledRunNotifyDoneOnly:
		return status.Terminal()
	default:
		return false
	}
}

func scheduledRunNotificationFromEvent(event memaxagent.Event) (CreateScheduledRunNotificationRequest, bool) {
	if event.Kind != memaxagent.EventRunStateChanged || event.Run == nil {
		return CreateScheduledRunNotificationRequest{}, false
	}
	run := event.Run
	if strings.TrimSpace(run.RunID) == "" || strings.TrimSpace(run.TriggerName) == "" {
		return CreateScheduledRunNotificationRequest{}, false
	}
	status := ScheduledRunStatus(run.Status)
	if !scheduledRunStatusKnown(status) {
		return CreateScheduledRunNotificationRequest{}, false
	}
	createdAt := event.Time
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	return CreateScheduledRunNotificationRequest{
		ID:           scheduledRunNotificationID(run.RunID, status),
		RunID:        strings.TrimSpace(run.RunID),
		Status:       status,
		TriggerName:  strings.TrimSpace(run.TriggerName),
		OccurrenceAt: run.OccurrenceAt.UTC(),
		Prompt:       run.Prompt,
		Result:       run.Result,
		Error:        run.Error,
		CreatedAt:    createdAt.UTC(),
	}, true
}

// Normalize validates req and returns the durable notification record it
// represents. Stores should use this helper so validation stays consistent
// across in-memory and durable backends.
func (req CreateScheduledRunNotificationRequest) Normalize() (ScheduledRunNotificationRecord, error) {
	record := ScheduledRunNotificationRecord{
		ID:           strings.TrimSpace(req.ID),
		RunID:        strings.TrimSpace(req.RunID),
		Status:       req.Status,
		TriggerName:  strings.TrimSpace(req.TriggerName),
		OccurrenceAt: req.OccurrenceAt.UTC(),
		Prompt:       req.Prompt,
		Result:       req.Result,
		Error:        req.Error,
		CreatedAt:    req.CreatedAt.UTC(),
	}
	if record.ID == "" {
		return ScheduledRunNotificationRecord{}, fmt.Errorf("scheduled run notification id is required")
	}
	if record.RunID == "" {
		return ScheduledRunNotificationRecord{}, fmt.Errorf("scheduled run notification run id is required")
	}
	if !scheduledRunStatusKnown(record.Status) {
		return ScheduledRunNotificationRecord{}, fmt.Errorf("scheduled run notification status %q is invalid", record.Status)
	}
	if record.TriggerName == "" {
		return ScheduledRunNotificationRecord{}, fmt.Errorf("scheduled run notification trigger name is required")
	}
	if record.CreatedAt.IsZero() {
		record.CreatedAt = time.Now().UTC()
	}
	return record, nil
}

func scheduledRunStatusKnown(status ScheduledRunStatus) bool {
	switch status {
	case ScheduledRunQueued, ScheduledRunRunning, ScheduledRunSucceeded, ScheduledRunFailed:
		return true
	default:
		return false
	}
}

func scheduledRunNotificationID(runID string, status ScheduledRunStatus) string {
	return strings.TrimSpace(runID) + ":" + string(status)
}

func cloneScheduledRunNotification(record ScheduledRunNotificationRecord) ScheduledRunNotificationRecord {
	// All fields are value types today; keep this helper as the defensive-copy
	// boundary if the record later grows slices, maps, or pointer fields.
	return record
}
