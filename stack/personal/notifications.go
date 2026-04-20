package personal

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	memaxagent "github.com/MemaxLabs/memax-go-agent-sdk"
)

// ErrScheduledRunNotificationStoreRequired reports that scheduled-run
// notification mirroring needs an explicit host-owned store.
var ErrScheduledRunNotificationStoreRequired = errors.New("personal stack: scheduled run notification store is required")

// ErrScheduledRunNotificationNotFound reports that a requested notification
// outbox record does not exist.
var ErrScheduledRunNotificationNotFound = errors.New("personal stack: scheduled run notification not found")

// ErrScheduledRunNotificationWorkerMismatch reports that a delivery update was
// attempted by a worker that does not own the notification's delivery lease.
var ErrScheduledRunNotificationWorkerMismatch = errors.New("personal stack: scheduled run notification worker mismatch")

// ErrScheduledRunNotificationNotDelivering reports that a delivery update was
// attempted for a notification that is not currently leased to a worker.
var ErrScheduledRunNotificationNotDelivering = errors.New("personal stack: scheduled run notification is not delivering")

// DefaultScheduledRunNotificationLeaseDuration is the default delivery lease
// duration used when a claim request does not specify one.
const DefaultScheduledRunNotificationLeaseDuration = 5 * time.Minute

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
	ID                string
	RunID             string
	Status            ScheduledRunStatus
	TriggerName       string
	OccurrenceAt      time.Time
	Prompt            string
	Result            string
	Error             string
	CreatedAt         time.Time
	DeliveryStatus    ScheduledRunNotificationDeliveryStatus
	DeliveryWorkerID  string
	DeliveryAttempts  int
	DeliveryError     string
	DeliverAfter      time.Time
	DeliveredAt       time.Time
	DeliveryUpdatedAt time.Time
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
	RunID          string
	Status         ScheduledRunStatus
	DeliveryStatus ScheduledRunNotificationDeliveryStatus
	Limit          int
}

// ScheduledRunNotificationDeliveryStatus records the host-owned delivery
// lifecycle for a scheduled-run notification outbox item.
type ScheduledRunNotificationDeliveryStatus string

const (
	// ScheduledRunNotificationDeliveryPending means the notification is ready
	// to be claimed by a delivery worker.
	ScheduledRunNotificationDeliveryPending ScheduledRunNotificationDeliveryStatus = "pending"
	// ScheduledRunNotificationDeliveryDelivering means a worker has claimed
	// the notification and owns the current delivery lease.
	ScheduledRunNotificationDeliveryDelivering ScheduledRunNotificationDeliveryStatus = "delivering"
	// ScheduledRunNotificationDeliveryDelivered means the host reported the
	// notification as delivered to its external channel.
	ScheduledRunNotificationDeliveryDelivered ScheduledRunNotificationDeliveryStatus = "delivered"
	// ScheduledRunNotificationDeliveryFailed means the last delivery attempt
	// failed and the notification is eligible for retry at DeliverAfter.
	ScheduledRunNotificationDeliveryFailed ScheduledRunNotificationDeliveryStatus = "failed"
)

// ClaimScheduledRunNotificationsRequest claims deliverable notification outbox
// records for a host-owned delivery worker. Stores should atomically move
// claimable records to delivering, increment DeliveryAttempts, and clear any
// previous DeliveryError for the new attempt.
type ClaimScheduledRunNotificationsRequest struct {
	WorkerID      string
	Limit         int
	Now           time.Time
	LeaseDuration time.Duration
}

// MarkScheduledRunNotificationDeliveredRequest marks a claimed notification as
// delivered. WorkerID should match the current delivery lease owner. Repeating
// the call after delivery with the same WorkerID is idempotent; repeating it
// with another worker returns ErrScheduledRunNotificationWorkerMismatch.
type MarkScheduledRunNotificationDeliveredRequest struct {
	ID          string
	WorkerID    string
	DeliveredAt time.Time
}

// MarkScheduledRunNotificationFailedRequest marks a claimed notification
// attempt as failed. RetryAt controls when the record becomes claimable again;
// FailedAt records when the attempt failed and defaults to the current time.
// Failure acks are not idempotent: a repeated call after the record is failed
// returns ErrScheduledRunNotificationNotDelivering.
type MarkScheduledRunNotificationFailedRequest struct {
	ID       string
	WorkerID string
	Error    string
	RetryAt  time.Time
	FailedAt time.Time
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

// ScheduledRunNotificationDeliveryStore is the optional delivery extension for
// notification outboxes. It deliberately models claim/ack state only; actual
// channels such as email, mobile push, Slack, or webhooks remain host-owned.
// Implementations should return ErrScheduledRunNotificationNotFound,
// ErrScheduledRunNotificationWorkerMismatch, and
// ErrScheduledRunNotificationNotDelivering for the matching failure modes so
// hosts can switch on errors without string matching.
type ScheduledRunNotificationDeliveryStore interface {
	ClaimScheduledRunNotifications(context.Context, ClaimScheduledRunNotificationsRequest) ([]ScheduledRunNotificationRecord, error)
	MarkScheduledRunNotificationDelivered(context.Context, MarkScheduledRunNotificationDeliveredRequest) (ScheduledRunNotificationRecord, error)
	MarkScheduledRunNotificationFailed(context.Context, MarkScheduledRunNotificationFailedRequest) (ScheduledRunNotificationRecord, error)
}

// MemoryScheduledRunNotificationStore is the reference in-memory notification
// outbox backend.
type MemoryScheduledRunNotificationStore struct {
	mu   sync.RWMutex
	byID map[string]ScheduledRunNotificationRecord
	ids  []string
}

var _ ScheduledRunNotificationDeliveryStore = (*MemoryScheduledRunNotificationStore)(nil)

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
		if filter.DeliveryStatus != "" && record.DeliveryStatus != filter.DeliveryStatus {
			continue
		}
		notifications = append(notifications, cloneScheduledRunNotification(record))
		if filter.Limit > 0 && len(notifications) >= filter.Limit {
			break
		}
	}
	return notifications, nil
}

// ClaimScheduledRunNotifications implements
// ScheduledRunNotificationDeliveryStore.
func (s *MemoryScheduledRunNotificationStore) ClaimScheduledRunNotifications(ctx context.Context, req ClaimScheduledRunNotificationsRequest) ([]ScheduledRunNotificationRecord, error) {
	if s == nil {
		return nil, fmt.Errorf("memory scheduled run notification store is nil")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	claim, err := req.normalize()
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	candidates := make([]ScheduledRunNotificationRecord, 0, len(s.ids))
	for _, id := range s.ids {
		record := s.byID[id]
		if !record.deliveryClaimableAt(claim.now) {
			continue
		}
		candidates = append(candidates, record)
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if !candidates[i].DeliverAfter.Equal(candidates[j].DeliverAfter) {
			return candidates[i].DeliverAfter.Before(candidates[j].DeliverAfter)
		}
		if !candidates[i].CreatedAt.Equal(candidates[j].CreatedAt) {
			return candidates[i].CreatedAt.Before(candidates[j].CreatedAt)
		}
		return candidates[i].ID < candidates[j].ID
	})

	if len(candidates) > claim.limit {
		candidates = candidates[:claim.limit]
	}
	claimed := make([]ScheduledRunNotificationRecord, 0, len(candidates))
	for _, record := range candidates {
		record.DeliveryStatus = ScheduledRunNotificationDeliveryDelivering
		record.DeliveryWorkerID = claim.workerID
		record.DeliveryAttempts++
		record.DeliveryError = ""
		record.DeliverAfter = claim.now.Add(claim.leaseDuration).UTC()
		record.DeliveryUpdatedAt = claim.now
		s.byID[record.ID] = record
		claimed = append(claimed, cloneScheduledRunNotification(record))
	}
	return claimed, nil
}

// MarkScheduledRunNotificationDelivered implements
// ScheduledRunNotificationDeliveryStore.
func (s *MemoryScheduledRunNotificationStore) MarkScheduledRunNotificationDelivered(ctx context.Context, req MarkScheduledRunNotificationDeliveredRequest) (ScheduledRunNotificationRecord, error) {
	if s == nil {
		return ScheduledRunNotificationRecord{}, fmt.Errorf("memory scheduled run notification store is nil")
	}
	if err := ctx.Err(); err != nil {
		return ScheduledRunNotificationRecord{}, err
	}
	update, err := req.normalize()
	if err != nil {
		return ScheduledRunNotificationRecord{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.byID[update.id]
	if !ok {
		return ScheduledRunNotificationRecord{}, ErrScheduledRunNotificationNotFound
	}
	if record.DeliveryStatus == ScheduledRunNotificationDeliveryDelivered {
		if record.DeliveryWorkerID == update.workerID {
			return cloneScheduledRunNotification(record), nil
		}
		return ScheduledRunNotificationRecord{}, ErrScheduledRunNotificationWorkerMismatch
	}
	if err := record.ensureDeliveryWorker(update.workerID); err != nil {
		return ScheduledRunNotificationRecord{}, err
	}
	record.DeliveryStatus = ScheduledRunNotificationDeliveryDelivered
	record.DeliveryWorkerID = update.workerID
	record.DeliveryError = ""
	record.DeliveredAt = update.deliveredAt
	record.DeliverAfter = update.deliveredAt
	record.DeliveryUpdatedAt = update.deliveredAt
	s.byID[record.ID] = record
	return cloneScheduledRunNotification(record), nil
}

// MarkScheduledRunNotificationFailed implements
// ScheduledRunNotificationDeliveryStore.
func (s *MemoryScheduledRunNotificationStore) MarkScheduledRunNotificationFailed(ctx context.Context, req MarkScheduledRunNotificationFailedRequest) (ScheduledRunNotificationRecord, error) {
	if s == nil {
		return ScheduledRunNotificationRecord{}, fmt.Errorf("memory scheduled run notification store is nil")
	}
	if err := ctx.Err(); err != nil {
		return ScheduledRunNotificationRecord{}, err
	}
	update, err := req.normalize()
	if err != nil {
		return ScheduledRunNotificationRecord{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.byID[update.id]
	if !ok {
		return ScheduledRunNotificationRecord{}, ErrScheduledRunNotificationNotFound
	}
	if err := record.ensureDeliveryWorker(update.workerID); err != nil {
		return ScheduledRunNotificationRecord{}, err
	}
	record.DeliveryStatus = ScheduledRunNotificationDeliveryFailed
	record.DeliveryWorkerID = ""
	record.DeliveryError = update.errorText
	record.DeliverAfter = update.retryAt
	record.DeliveryUpdatedAt = update.failedAt
	s.byID[record.ID] = record
	return cloneScheduledRunNotification(record), nil
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
		ID:                strings.TrimSpace(req.ID),
		RunID:             strings.TrimSpace(req.RunID),
		Status:            req.Status,
		TriggerName:       strings.TrimSpace(req.TriggerName),
		OccurrenceAt:      req.OccurrenceAt.UTC(),
		Prompt:            req.Prompt,
		Result:            req.Result,
		Error:             req.Error,
		CreatedAt:         req.CreatedAt.UTC(),
		DeliveryStatus:    ScheduledRunNotificationDeliveryPending,
		DeliveryUpdatedAt: req.CreatedAt.UTC(),
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
	record.DeliverAfter = record.CreatedAt
	if record.DeliveryUpdatedAt.IsZero() {
		record.DeliveryUpdatedAt = record.CreatedAt
	}
	return record, nil
}

type normalizedScheduledRunNotificationClaim struct {
	workerID      string
	limit         int
	now           time.Time
	leaseDuration time.Duration
}

func (req ClaimScheduledRunNotificationsRequest) normalize() (normalizedScheduledRunNotificationClaim, error) {
	claim := normalizedScheduledRunNotificationClaim{
		workerID:      strings.TrimSpace(req.WorkerID),
		limit:         req.Limit,
		now:           req.Now.UTC(),
		leaseDuration: req.LeaseDuration,
	}
	if claim.workerID == "" {
		return normalizedScheduledRunNotificationClaim{}, fmt.Errorf("scheduled run notification delivery worker id is required")
	}
	if claim.limit <= 0 {
		claim.limit = 1
	}
	if claim.now.IsZero() {
		claim.now = time.Now().UTC()
	}
	if claim.leaseDuration <= 0 {
		claim.leaseDuration = DefaultScheduledRunNotificationLeaseDuration
	}
	return claim, nil
}

type normalizedScheduledRunNotificationDelivered struct {
	id          string
	workerID    string
	deliveredAt time.Time
}

func (req MarkScheduledRunNotificationDeliveredRequest) normalize() (normalizedScheduledRunNotificationDelivered, error) {
	update := normalizedScheduledRunNotificationDelivered{
		id:          strings.TrimSpace(req.ID),
		workerID:    strings.TrimSpace(req.WorkerID),
		deliveredAt: req.DeliveredAt.UTC(),
	}
	if update.id == "" {
		return normalizedScheduledRunNotificationDelivered{}, fmt.Errorf("scheduled run notification id is required")
	}
	if update.workerID == "" {
		return normalizedScheduledRunNotificationDelivered{}, fmt.Errorf("scheduled run notification delivery worker id is required")
	}
	if update.deliveredAt.IsZero() {
		update.deliveredAt = time.Now().UTC()
	}
	return update, nil
}

type normalizedScheduledRunNotificationFailed struct {
	id        string
	workerID  string
	errorText string
	retryAt   time.Time
	failedAt  time.Time
}

func (req MarkScheduledRunNotificationFailedRequest) normalize() (normalizedScheduledRunNotificationFailed, error) {
	update := normalizedScheduledRunNotificationFailed{
		id:        strings.TrimSpace(req.ID),
		workerID:  strings.TrimSpace(req.WorkerID),
		errorText: strings.TrimSpace(req.Error),
		retryAt:   req.RetryAt.UTC(),
		failedAt:  req.FailedAt.UTC(),
	}
	if update.id == "" {
		return normalizedScheduledRunNotificationFailed{}, fmt.Errorf("scheduled run notification id is required")
	}
	if update.workerID == "" {
		return normalizedScheduledRunNotificationFailed{}, fmt.Errorf("scheduled run notification delivery worker id is required")
	}
	if update.errorText == "" {
		return normalizedScheduledRunNotificationFailed{}, fmt.Errorf("scheduled run notification delivery error is required")
	}
	if update.retryAt.IsZero() {
		update.retryAt = time.Now().UTC()
	}
	if update.failedAt.IsZero() {
		update.failedAt = time.Now().UTC()
	}
	return update, nil
}

func scheduledRunStatusKnown(status ScheduledRunStatus) bool {
	switch status {
	case ScheduledRunQueued, ScheduledRunRunning, ScheduledRunSucceeded, ScheduledRunFailed:
		return true
	default:
		return false
	}
}

func scheduledRunNotificationDeliveryStatusKnown(status ScheduledRunNotificationDeliveryStatus) bool {
	switch status {
	case ScheduledRunNotificationDeliveryPending,
		ScheduledRunNotificationDeliveryDelivering,
		ScheduledRunNotificationDeliveryDelivered,
		ScheduledRunNotificationDeliveryFailed:
		return true
	default:
		return false
	}
}

func (record ScheduledRunNotificationRecord) deliveryClaimableAt(now time.Time) bool {
	if !scheduledRunNotificationDeliveryStatusKnown(record.DeliveryStatus) {
		return false
	}
	if record.DeliveryStatus == ScheduledRunNotificationDeliveryDelivered {
		return false
	}
	return record.DeliverAfter.IsZero() || !record.DeliverAfter.After(now)
}

func (record ScheduledRunNotificationRecord) ensureDeliveryWorker(workerID string) error {
	if record.DeliveryStatus != ScheduledRunNotificationDeliveryDelivering {
		return ErrScheduledRunNotificationNotDelivering
	}
	if record.DeliveryWorkerID != "" && record.DeliveryWorkerID != workerID {
		return ErrScheduledRunNotificationWorkerMismatch
	}
	return nil
}

func scheduledRunNotificationID(runID string, status ScheduledRunStatus) string {
	return strings.TrimSpace(runID) + ":" + string(status)
}

func cloneScheduledRunNotification(record ScheduledRunNotificationRecord) ScheduledRunNotificationRecord {
	// All fields are value types today; keep this helper as the defensive-copy
	// boundary if the record later grows slices, maps, or pointer fields.
	return record
}
