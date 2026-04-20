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

// ErrScheduledRunNotificationDeliveryStoreRequired reports that scheduled-run
// notification delivery draining needs an explicit delivery-capable store.
var ErrScheduledRunNotificationDeliveryStoreRequired = errors.New("personal stack: scheduled run notification delivery store is required")

// ErrScheduledRunNotificationDeliveryHandlerRequired reports that scheduled-run
// notification delivery draining needs an explicit host delivery handler.
var ErrScheduledRunNotificationDeliveryHandlerRequired = errors.New("personal stack: scheduled run notification delivery handler is required")

// ErrScheduledRunNotificationDeliveryWorkerIDRequired reports that scheduled-run
// notification delivery draining needs an explicit worker identity.
var ErrScheduledRunNotificationDeliveryWorkerIDRequired = errors.New("personal stack: scheduled run notification delivery worker id is required")

// ErrScheduledRunNotificationWatchIntervalRequired reports that scheduled-run
// notification delivery watching needs a positive interval.
var ErrScheduledRunNotificationWatchIntervalRequired = errors.New("personal stack: scheduled run notification watch interval must be positive")

// ErrScheduledRunNotificationDeadLetterStoreRequired reports that max-attempt
// delivery draining needs a store that supports dead-letter acknowledgements.
var ErrScheduledRunNotificationDeadLetterStoreRequired = errors.New("personal stack: scheduled run notification store must implement ScheduledRunNotificationDeadLetterStore")

// ErrScheduledRunNotificationNotRecoverable reports that a notification is not
// in a failed or dead-lettered state that can be requeued for delivery.
var ErrScheduledRunNotificationNotRecoverable = errors.New("personal stack: scheduled run notification is not recoverable")

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

// ScheduledRunNotificationStats reports a current operational snapshot of a
// scheduled-run notification outbox. Counts are derived from current records;
// this is not a historical event counter. DeliveryAttemptsTotal preserves the
// accumulated attempt count on surviving records so hosts can detect retry
// pressure without scanning the full outbox.
type ScheduledRunNotificationStats struct {
	// TotalCount is the total number of surviving notification outbox records,
	// including delivered and dead-lettered records.
	TotalCount int
	// PendingCount is the number of records currently waiting for delivery.
	PendingCount int
	// DeliveringCount is the number of records claimed by a worker, including
	// records whose lease has expired and can be claimed again.
	DeliveringCount int
	// DeliveredCount is the number of records marked delivered.
	DeliveredCount int
	// FailedCount is the number of retryable records whose previous delivery
	// attempt failed.
	FailedCount int
	// DeadLetteredCount is the number of records that exhausted retry policy and
	// require host intervention.
	DeadLetteredCount int
	// ClaimableCount is the number of pending, failed, or delivering records
	// whose DeliverAfter time is not after the supplied snapshot time. This
	// includes delivering records with expired leases.
	ClaimableCount int
	// LeasedCount is the number of delivering records whose DeliverAfter time is
	// still after the supplied snapshot time.
	LeasedCount int
	// DeliveryAttemptsTotal is the sum of delivery attempts across surviving
	// records.
	DeliveryAttemptsTotal int
	// OldestUndeliveredAt is the earliest CreatedAt among records not yet
	// delivered or dead-lettered.
	OldestUndeliveredAt time.Time
	// OldestUndeliveredAge is the age of OldestUndeliveredAt at the supplied
	// snapshot time. It is zero when there are no undelivered records.
	OldestUndeliveredAge time.Duration
	// NextClaimableAt is the earliest DeliverAfter among pending, failed, and
	// delivering records. It may be in the past when backlog is already
	// claimable, or in the future for retry delays and active delivery leases.
	NextClaimableAt time.Time
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
	// ScheduledRunNotificationDeliveryDeadLettered means the notification
	// exhausted its configured delivery attempts and requires host intervention.
	ScheduledRunNotificationDeliveryDeadLettered ScheduledRunNotificationDeliveryStatus = "dead_lettered"
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

// MarkScheduledRunNotificationDeadLetteredRequest marks a claimed notification
// as permanently failed after exhausting the host-configured retry policy.
// DeadLetteredAt records when the delivery attempt became terminal and defaults
// to the current time.
type MarkScheduledRunNotificationDeadLetteredRequest struct {
	ID             string
	WorkerID       string
	Error          string
	DeadLetteredAt time.Time
}

// RequeueScheduledRunNotificationRequest moves a failed or dead-lettered
// notification back to pending delivery after host inspection or repair.
// Pending, delivering, and delivered records are rejected with
// ErrScheduledRunNotificationNotRecoverable. DeliverAfter defaults to
// RequeuedAt so callers can requeue immediately or schedule another retry.
// Requeue clears the current delivery worker and delivery error, preserves
// DeliveryAttempts for audit history, and leaves DeliveredAt unchanged.
type RequeueScheduledRunNotificationRequest struct {
	ID           string
	DeliverAfter time.Time
	RequeuedAt   time.Time
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

// ScheduledRunNotificationStatsStore is the optional store extension for
// efficient notification outbox health snapshots. Stores that do not implement
// it can still be inspected with GetScheduledRunNotificationStats, which falls
// back to ListScheduledRunNotifications and computes the same snapshot in
// memory.
type ScheduledRunNotificationStatsStore interface {
	ScheduledRunNotificationStats(context.Context, time.Time) (ScheduledRunNotificationStats, error)
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

// ScheduledRunNotificationDeadLetterStore is the optional delivery extension
// for hosts that want the SDK drain loop to stop retrying poison
// notifications after a configured attempt limit. It is separate from
// ScheduledRunNotificationDeliveryStore so existing retry-only stores remain
// source-compatible.
type ScheduledRunNotificationDeadLetterStore interface {
	MarkScheduledRunNotificationDeadLettered(context.Context, MarkScheduledRunNotificationDeadLetteredRequest) (ScheduledRunNotificationRecord, error)
}

// ScheduledRunNotificationRecoveryStore is the optional delivery extension for
// hosts that want to inspect a failed or dead-lettered notification and
// manually requeue it after remediation. Delivered, pending, and actively
// delivering records are not recoverable through this method. Implementations
// should return ErrScheduledRunNotificationNotFound and
// ErrScheduledRunNotificationNotRecoverable for the matching failure modes so
// hosts can switch on errors without string matching.
type ScheduledRunNotificationRecoveryStore interface {
	RequeueScheduledRunNotification(context.Context, RequeueScheduledRunNotificationRequest) (ScheduledRunNotificationRecord, error)
}

// ScheduledRunNotificationDeliveryHandler delivers one claimed scheduled-run
// notification to a host-owned channel such as email, push, chat, webhook, or a
// durable inbox. Returning an error records a retryable delivery failure in the
// notification store; it does not make DrainScheduledRunNotifications fail when
// the failure can be acknowledged successfully.
type ScheduledRunNotificationDeliveryHandler interface {
	DeliverScheduledRunNotification(context.Context, ScheduledRunNotificationRecord) error
}

// ScheduledRunNotificationDeliveryHandlerFunc adapts a function into a
// ScheduledRunNotificationDeliveryHandler.
type ScheduledRunNotificationDeliveryHandlerFunc func(context.Context, ScheduledRunNotificationRecord) error

// DeliverScheduledRunNotification implements
// ScheduledRunNotificationDeliveryHandler.
func (f ScheduledRunNotificationDeliveryHandlerFunc) DeliverScheduledRunNotification(ctx context.Context, record ScheduledRunNotificationRecord) error {
	if f == nil {
		return ErrScheduledRunNotificationDeliveryHandlerRequired
	}
	return f(ctx, record)
}

// GetScheduledRunNotificationStats returns an operational snapshot for a
// notification outbox. Stores with a native stats implementation can avoid a
// full scan; other stores fall back to ListScheduledRunNotifications. The now
// argument controls lease-expiry and age calculations; zero uses time.Now.
func GetScheduledRunNotificationStats(ctx context.Context, store ScheduledRunNotificationStore, now time.Time) (ScheduledRunNotificationStats, error) {
	if store == nil {
		return ScheduledRunNotificationStats{}, ErrScheduledRunNotificationStoreRequired
	}
	now = normalizeScheduledRunNotificationStatsNow(now)
	if statsStore, ok := store.(ScheduledRunNotificationStatsStore); ok {
		return statsStore.ScheduledRunNotificationStats(ctx, now)
	}
	records, err := store.ListScheduledRunNotifications(ctx, ScheduledRunNotificationFilter{})
	if err != nil {
		return ScheduledRunNotificationStats{}, err
	}
	return scheduledRunNotificationStatsFromRecords(records, now), nil
}

// ScheduledRunNotificationRetryBackoff returns the next retry time for a failed
// scheduled-run notification delivery attempt. The record has already been
// claimed for this attempt, so DeliveryAttempts includes the failed attempt.
// Returning zero, or a time in the past, makes the notification immediately
// claimable again; callers that want the default policy should pass nil to
// WithScheduledRunNotificationRetryBackoff instead.
type ScheduledRunNotificationRetryBackoff func(ScheduledRunNotificationRecord, error, time.Time) time.Time

// ScheduledRunNotificationDrainFailure records one host-channel delivery
// failure that was durably marked for retry.
type ScheduledRunNotificationDrainFailure struct {
	Record ScheduledRunNotificationRecord
	Err    error
}

// ScheduledRunNotificationDrainResult summarizes one delivery drain pass.
// Handler errors appear in Failed and are not returned as the function error
// when the store successfully records the retry state. The returned error is
// reserved for claim/ack/store failures or invalid configuration.
type ScheduledRunNotificationDrainResult struct {
	Claimed      []ScheduledRunNotificationRecord
	Delivered    []ScheduledRunNotificationRecord
	Failed       []ScheduledRunNotificationDrainFailure
	DeadLettered []ScheduledRunNotificationDrainFailure
}

type scheduledRunNotificationDrainConfig struct {
	limit         int
	leaseDuration time.Duration
	now           time.Time
	retryBackoff  ScheduledRunNotificationRetryBackoff
	onResult      func(context.Context, ScheduledRunNotificationDrainResult)
	maxAttempts   int
}

// ScheduledRunNotificationDrainOption configures one notification delivery
// drain pass.
type ScheduledRunNotificationDrainOption func(*scheduledRunNotificationDrainConfig)

// WithScheduledRunNotificationDrainLimit sets the maximum number of due
// notification records claimed in one drain pass. Values <= 0 use the default
// of one.
func WithScheduledRunNotificationDrainLimit(limit int) ScheduledRunNotificationDrainOption {
	return func(config *scheduledRunNotificationDrainConfig) {
		config.limit = limit
	}
}

// WithScheduledRunNotificationDrainLeaseDuration sets the delivery lease held
// while the host handler attempts external delivery. Values <= 0 use
// DefaultScheduledRunNotificationLeaseDuration.
func WithScheduledRunNotificationDrainLeaseDuration(duration time.Duration) ScheduledRunNotificationDrainOption {
	return func(config *scheduledRunNotificationDrainConfig) {
		config.leaseDuration = duration
	}
}

// WithScheduledRunNotificationDrainNow fixes the drain clock for deterministic
// tests and simulations. Production callers should usually omit it.
func WithScheduledRunNotificationDrainNow(now time.Time) ScheduledRunNotificationDrainOption {
	return func(config *scheduledRunNotificationDrainConfig) {
		config.now = now.UTC()
	}
}

// WithScheduledRunNotificationRetryBackoff sets the retry policy used when the
// host delivery handler returns an error. A nil backoff uses
// DefaultScheduledRunNotificationRetryBackoff.
func WithScheduledRunNotificationRetryBackoff(backoff ScheduledRunNotificationRetryBackoff) ScheduledRunNotificationDrainOption {
	return func(config *scheduledRunNotificationDrainConfig) {
		config.retryBackoff = backoff
	}
}

// WithScheduledRunNotificationMaxAttempts sets a terminal delivery-attempt
// limit. Values <= 0 preserve the default unlimited retry behavior.
// DeliveryAttempts includes the currently claimed attempt, so maxAttempts=1
// dead-letters a notification on the first failed handler call. Claiming work
// is at-least-once, so a crash after claim still consumes an attempt before the
// lease expires. When the current failed attempt reaches maxAttempts,
// DrainScheduledRunNotifications marks the record dead_lettered instead of
// scheduling another retry. The store must also implement
// ScheduledRunNotificationDeadLetterStore.
func WithScheduledRunNotificationMaxAttempts(maxAttempts int) ScheduledRunNotificationDrainOption {
	return func(config *scheduledRunNotificationDrainConfig) {
		config.maxAttempts = maxAttempts
	}
}

// WithScheduledRunNotificationDrainResultObserver observes each successful
// drain pass. Store and context errors are returned to the caller instead of
// being reported through this observer. The observer runs synchronously after
// claim/handler/ack work completes and receives a defensive copy of the result.
// Keep observers fast or offload slow telemetry to host-owned queues.
func WithScheduledRunNotificationDrainResultObserver(observer func(context.Context, ScheduledRunNotificationDrainResult)) ScheduledRunNotificationDrainOption {
	return func(config *scheduledRunNotificationDrainConfig) {
		config.onResult = observer
	}
}

// DefaultScheduledRunNotificationRetryBackoff returns an exponential retry time
// capped at 24 hours. Attempt one retries after one minute, attempt two after
// two minutes, and later attempts double until the cap.
func DefaultScheduledRunNotificationRetryBackoff(record ScheduledRunNotificationRecord, _ error, now time.Time) time.Time {
	attempts := record.DeliveryAttempts
	if attempts <= 0 {
		attempts = 1
	}
	delay := time.Minute
	for i := 1; i < attempts && delay < 24*time.Hour; i++ {
		delay *= 2
	}
	if delay > 24*time.Hour {
		delay = 24 * time.Hour
	}
	return now.Add(delay).UTC()
}

// DrainScheduledRunNotifications claims due scheduled-run notifications,
// delivers each one through handler, and durably acks delivered or retryable
// failed state in store. It performs one bounded drain pass and returns; hosts
// that want a long-running worker should call it from their own ticker or queue
// loop. The SDK owns claim/ack/retry state while external delivery remains
// host-owned. Store claim/ack errors fail fast; any records already claimed but
// not yet acked remain under their delivery lease until the lease expires.
func DrainScheduledRunNotifications(ctx context.Context, store ScheduledRunNotificationDeliveryStore, workerID string, handler ScheduledRunNotificationDeliveryHandler, options ...ScheduledRunNotificationDrainOption) (ScheduledRunNotificationDrainResult, error) {
	if store == nil {
		return ScheduledRunNotificationDrainResult{}, ErrScheduledRunNotificationDeliveryStoreRequired
	}
	workerID = strings.TrimSpace(workerID)
	if workerID == "" {
		return ScheduledRunNotificationDrainResult{}, ErrScheduledRunNotificationDeliveryWorkerIDRequired
	}
	if handler == nil {
		return ScheduledRunNotificationDrainResult{}, ErrScheduledRunNotificationDeliveryHandlerRequired
	}
	if handlerFunc, ok := handler.(ScheduledRunNotificationDeliveryHandlerFunc); ok && handlerFunc == nil {
		return ScheduledRunNotificationDrainResult{}, ErrScheduledRunNotificationDeliveryHandlerRequired
	}
	config := scheduledRunNotificationDrainConfig{
		limit:         1,
		leaseDuration: DefaultScheduledRunNotificationLeaseDuration,
		retryBackoff:  DefaultScheduledRunNotificationRetryBackoff,
	}
	for _, option := range options {
		if option != nil {
			option(&config)
		}
	}
	if config.limit <= 0 {
		config.limit = 1
	}
	if config.leaseDuration <= 0 {
		config.leaseDuration = DefaultScheduledRunNotificationLeaseDuration
	}
	if config.retryBackoff == nil {
		config.retryBackoff = DefaultScheduledRunNotificationRetryBackoff
	}
	var deadLetterStore ScheduledRunNotificationDeadLetterStore
	if config.maxAttempts > 0 {
		var ok bool
		deadLetterStore, ok = store.(ScheduledRunNotificationDeadLetterStore)
		if !ok {
			return ScheduledRunNotificationDrainResult{}, ErrScheduledRunNotificationDeadLetterStoreRequired
		}
	}

	claimNow := scheduledRunNotificationDrainNow(config)
	claimed, err := store.ClaimScheduledRunNotifications(ctx, ClaimScheduledRunNotificationsRequest{
		WorkerID:      workerID,
		Limit:         config.limit,
		Now:           claimNow,
		LeaseDuration: config.leaseDuration,
	})
	if err != nil {
		return ScheduledRunNotificationDrainResult{}, fmt.Errorf("claim scheduled run notifications: %w", err)
	}
	result := ScheduledRunNotificationDrainResult{
		Claimed: append([]ScheduledRunNotificationRecord(nil), claimed...),
	}
	for _, record := range claimed {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if err := handler.DeliverScheduledRunNotification(ctx, record); err != nil {
			failedAt := scheduledRunNotificationDrainNow(config)
			if config.maxAttempts > 0 && record.DeliveryAttempts >= config.maxAttempts {
				deadLettered, markErr := deadLetterStore.MarkScheduledRunNotificationDeadLettered(ctx, MarkScheduledRunNotificationDeadLetteredRequest{
					ID:             record.ID,
					WorkerID:       workerID,
					Error:          scheduledRunNotificationDeliveryErrorText(err),
					DeadLetteredAt: failedAt,
				})
				if markErr != nil {
					return result, fmt.Errorf("mark scheduled run notification %s dead-lettered: %w", record.ID, markErr)
				}
				result.DeadLettered = append(result.DeadLettered, ScheduledRunNotificationDrainFailure{
					Record: deadLettered,
					Err:    err,
				})
				continue
			}
			retryAt := config.retryBackoff(record, err, failedAt)
			if retryAt.IsZero() {
				retryAt = failedAt
			}
			failed, markErr := store.MarkScheduledRunNotificationFailed(ctx, MarkScheduledRunNotificationFailedRequest{
				ID:       record.ID,
				WorkerID: workerID,
				Error:    scheduledRunNotificationDeliveryErrorText(err),
				RetryAt:  retryAt,
				FailedAt: failedAt,
			})
			if markErr != nil {
				return result, fmt.Errorf("mark scheduled run notification %s failed: %w", record.ID, markErr)
			}
			result.Failed = append(result.Failed, ScheduledRunNotificationDrainFailure{
				Record: failed,
				Err:    err,
			})
			continue
		}
		delivered, err := store.MarkScheduledRunNotificationDelivered(ctx, MarkScheduledRunNotificationDeliveredRequest{
			ID:          record.ID,
			WorkerID:    workerID,
			DeliveredAt: scheduledRunNotificationDrainNow(config),
		})
		if err != nil {
			return result, fmt.Errorf("mark scheduled run notification %s delivered: %w", record.ID, err)
		}
		result.Delivered = append(result.Delivered, delivered)
	}
	observeScheduledRunNotificationDrainResult(ctx, config.onResult, result)
	return result, nil
}

// WatchScheduledRunNotifications continuously drains due scheduled-run
// notifications until ctx is canceled. It runs one drain pass immediately, then
// repeats on interval. Handler failures are recorded as retryable outbox state by
// DrainScheduledRunNotifications and do not stop the watcher; claim/ack/store
// errors are returned because the worker can no longer make reliable progress.
// interval must be positive. Invalid store, handler, or workerID configuration is
// surfaced by the first drain pass.
func WatchScheduledRunNotifications(ctx context.Context, store ScheduledRunNotificationDeliveryStore, workerID string, handler ScheduledRunNotificationDeliveryHandler, interval time.Duration, options ...ScheduledRunNotificationDrainOption) error {
	if interval <= 0 {
		return ErrScheduledRunNotificationWatchIntervalRequired
	}
	drain := func() error {
		_, err := DrainScheduledRunNotifications(ctx, store, workerID, handler, options...)
		return err
	}
	if err := drain(); err != nil {
		return err
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := drain(); err != nil {
				return err
			}
		}
	}
}

func observeScheduledRunNotificationDrainResult(ctx context.Context, observer func(context.Context, ScheduledRunNotificationDrainResult), result ScheduledRunNotificationDrainResult) {
	if observer == nil {
		return
	}
	observer(ctx, cloneScheduledRunNotificationDrainResult(result))
}

// ObserveScheduledRunNotificationDeliveryEvent emits one scheduled-run
// notification delivery transition through the EventObserver attached to ctx.
// Store implementations call this after durably recording a transition so
// hosts can audit claim, delivery, retry, dead-letter, and requeue lifecycle
// changes without polling the outbox.
func ObserveScheduledRunNotificationDeliveryEvent(ctx context.Context, kind memaxagent.EventKind, record ScheduledRunNotificationRecord) {
	memaxagent.ObserveEvent(ctx, ScheduledRunNotificationDeliveryEvent(kind, record))
}

// ScheduledRunNotificationDeliveryEvent builds the root event shape for one
// scheduled-run notification delivery transition.
func ScheduledRunNotificationDeliveryEvent(kind memaxagent.EventKind, record ScheduledRunNotificationRecord) memaxagent.Event {
	return memaxagent.Event{
		Kind: kind,
		Notification: &memaxagent.ScheduledRunNotificationEvent{
			NotificationID:    record.ID,
			RunID:             record.RunID,
			Status:            string(record.Status),
			TriggerName:       record.TriggerName,
			OccurrenceAt:      record.OccurrenceAt,
			DeliveryStatus:    string(record.DeliveryStatus),
			WorkerID:          record.DeliveryWorkerID,
			Attempts:          record.DeliveryAttempts,
			DeliveryError:     record.DeliveryError,
			DeliverAfter:      record.DeliverAfter,
			DeliveredAt:       record.DeliveredAt,
			DeliveryUpdatedAt: record.DeliveryUpdatedAt,
		},
	}
}

// MemoryScheduledRunNotificationStore is the reference in-memory notification
// outbox backend.
type MemoryScheduledRunNotificationStore struct {
	mu   sync.RWMutex
	byID map[string]ScheduledRunNotificationRecord
	ids  []string
}

var _ ScheduledRunNotificationDeliveryStore = (*MemoryScheduledRunNotificationStore)(nil)
var _ ScheduledRunNotificationDeadLetterStore = (*MemoryScheduledRunNotificationStore)(nil)
var _ ScheduledRunNotificationRecoveryStore = (*MemoryScheduledRunNotificationStore)(nil)
var _ ScheduledRunNotificationStatsStore = (*MemoryScheduledRunNotificationStore)(nil)

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

// ScheduledRunNotificationStats implements
// ScheduledRunNotificationStatsStore.
func (s *MemoryScheduledRunNotificationStore) ScheduledRunNotificationStats(ctx context.Context, now time.Time) (ScheduledRunNotificationStats, error) {
	if s == nil {
		return ScheduledRunNotificationStats{}, fmt.Errorf("memory scheduled run notification store is nil")
	}
	if err := ctx.Err(); err != nil {
		return ScheduledRunNotificationStats{}, err
	}
	now = normalizeScheduledRunNotificationStatsNow(now)
	s.mu.RLock()
	defer s.mu.RUnlock()
	records := make([]ScheduledRunNotificationRecord, 0, len(s.ids))
	for _, id := range s.ids {
		records = append(records, s.byID[id])
	}
	return scheduledRunNotificationStatsFromRecords(records, now), nil
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
	s.mu.Unlock()
	for _, record := range claimed {
		ObserveScheduledRunNotificationDeliveryEvent(ctx, memaxagent.EventScheduledRunNotificationClaimed, record)
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
	record, ok := s.byID[update.id]
	if !ok {
		s.mu.Unlock()
		return ScheduledRunNotificationRecord{}, ErrScheduledRunNotificationNotFound
	}
	if record.DeliveryStatus == ScheduledRunNotificationDeliveryDelivered {
		if record.DeliveryWorkerID == update.workerID {
			clone := cloneScheduledRunNotification(record)
			s.mu.Unlock()
			return clone, nil
		}
		s.mu.Unlock()
		return ScheduledRunNotificationRecord{}, ErrScheduledRunNotificationWorkerMismatch
	}
	if err := record.ensureDeliveryWorker(update.workerID); err != nil {
		s.mu.Unlock()
		return ScheduledRunNotificationRecord{}, err
	}
	record.DeliveryStatus = ScheduledRunNotificationDeliveryDelivered
	record.DeliveryWorkerID = update.workerID
	record.DeliveryError = ""
	record.DeliveredAt = update.deliveredAt
	record.DeliverAfter = update.deliveredAt
	record.DeliveryUpdatedAt = update.deliveredAt
	s.byID[record.ID] = record
	clone := cloneScheduledRunNotification(record)
	s.mu.Unlock()
	ObserveScheduledRunNotificationDeliveryEvent(ctx, memaxagent.EventScheduledRunNotificationDelivered, clone)
	return clone, nil
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
	record, ok := s.byID[update.id]
	if !ok {
		s.mu.Unlock()
		return ScheduledRunNotificationRecord{}, ErrScheduledRunNotificationNotFound
	}
	if err := record.ensureDeliveryWorker(update.workerID); err != nil {
		s.mu.Unlock()
		return ScheduledRunNotificationRecord{}, err
	}
	record.DeliveryStatus = ScheduledRunNotificationDeliveryFailed
	record.DeliveryWorkerID = ""
	record.DeliveryError = update.errorText
	record.DeliverAfter = update.retryAt
	record.DeliveryUpdatedAt = update.failedAt
	s.byID[record.ID] = record
	clone := cloneScheduledRunNotification(record)
	s.mu.Unlock()
	ObserveScheduledRunNotificationDeliveryEvent(ctx, memaxagent.EventScheduledRunNotificationFailed, clone)
	return clone, nil
}

// MarkScheduledRunNotificationDeadLettered implements
// ScheduledRunNotificationDeadLetterStore.
func (s *MemoryScheduledRunNotificationStore) MarkScheduledRunNotificationDeadLettered(ctx context.Context, req MarkScheduledRunNotificationDeadLetteredRequest) (ScheduledRunNotificationRecord, error) {
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
	record, ok := s.byID[update.id]
	if !ok {
		s.mu.Unlock()
		return ScheduledRunNotificationRecord{}, ErrScheduledRunNotificationNotFound
	}
	if err := record.ensureDeliveryWorker(update.workerID); err != nil {
		s.mu.Unlock()
		return ScheduledRunNotificationRecord{}, err
	}
	record.DeliveryStatus = ScheduledRunNotificationDeliveryDeadLettered
	record.DeliveryWorkerID = ""
	record.DeliveryError = update.errorText
	record.DeliverAfter = update.deadLetteredAt
	record.DeliveryUpdatedAt = update.deadLetteredAt
	s.byID[record.ID] = record
	clone := cloneScheduledRunNotification(record)
	s.mu.Unlock()
	ObserveScheduledRunNotificationDeliveryEvent(ctx, memaxagent.EventScheduledRunNotificationDeadLettered, clone)
	return clone, nil
}

// RequeueScheduledRunNotification implements
// ScheduledRunNotificationRecoveryStore.
func (s *MemoryScheduledRunNotificationStore) RequeueScheduledRunNotification(ctx context.Context, req RequeueScheduledRunNotificationRequest) (ScheduledRunNotificationRecord, error) {
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
	record, ok := s.byID[update.id]
	if !ok {
		s.mu.Unlock()
		return ScheduledRunNotificationRecord{}, ErrScheduledRunNotificationNotFound
	}
	if record.DeliveryStatus != ScheduledRunNotificationDeliveryFailed && record.DeliveryStatus != ScheduledRunNotificationDeliveryDeadLettered {
		s.mu.Unlock()
		return ScheduledRunNotificationRecord{}, ErrScheduledRunNotificationNotRecoverable
	}
	record.DeliveryStatus = ScheduledRunNotificationDeliveryPending
	record.DeliveryWorkerID = ""
	record.DeliveryError = ""
	record.DeliverAfter = update.deliverAfter
	record.DeliveryUpdatedAt = update.requeuedAt
	s.byID[record.ID] = record
	clone := cloneScheduledRunNotification(record)
	s.mu.Unlock()
	ObserveScheduledRunNotificationDeliveryEvent(ctx, memaxagent.EventScheduledRunNotificationRequeued, clone)
	return clone, nil
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

type normalizedScheduledRunNotificationDeadLettered struct {
	id             string
	workerID       string
	errorText      string
	deadLetteredAt time.Time
}

func (req MarkScheduledRunNotificationDeadLetteredRequest) normalize() (normalizedScheduledRunNotificationDeadLettered, error) {
	update := normalizedScheduledRunNotificationDeadLettered{
		id:             strings.TrimSpace(req.ID),
		workerID:       strings.TrimSpace(req.WorkerID),
		errorText:      strings.TrimSpace(req.Error),
		deadLetteredAt: req.DeadLetteredAt.UTC(),
	}
	if update.id == "" {
		return normalizedScheduledRunNotificationDeadLettered{}, fmt.Errorf("scheduled run notification id is required")
	}
	if update.workerID == "" {
		return normalizedScheduledRunNotificationDeadLettered{}, fmt.Errorf("scheduled run notification delivery worker id is required")
	}
	if update.errorText == "" {
		return normalizedScheduledRunNotificationDeadLettered{}, fmt.Errorf("scheduled run notification delivery error is required")
	}
	if update.deadLetteredAt.IsZero() {
		update.deadLetteredAt = time.Now().UTC()
	}
	return update, nil
}

type normalizedScheduledRunNotificationRequeue struct {
	id           string
	deliverAfter time.Time
	requeuedAt   time.Time
}

func (req RequeueScheduledRunNotificationRequest) normalize() (normalizedScheduledRunNotificationRequeue, error) {
	update := normalizedScheduledRunNotificationRequeue{
		id:           strings.TrimSpace(req.ID),
		deliverAfter: req.DeliverAfter.UTC(),
		requeuedAt:   req.RequeuedAt.UTC(),
	}
	if update.id == "" {
		return normalizedScheduledRunNotificationRequeue{}, fmt.Errorf("scheduled run notification id is required")
	}
	if update.requeuedAt.IsZero() {
		update.requeuedAt = time.Now().UTC()
	}
	if update.deliverAfter.IsZero() {
		update.deliverAfter = update.requeuedAt
	}
	return update, nil
}

func scheduledRunNotificationDrainNow(config scheduledRunNotificationDrainConfig) time.Time {
	if !config.now.IsZero() {
		return config.now
	}
	return time.Now().UTC()
}

func scheduledRunNotificationDeliveryErrorText(err error) string {
	text := strings.TrimSpace(err.Error())
	if text == "" {
		return "scheduled run notification delivery failed"
	}
	return text
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
		ScheduledRunNotificationDeliveryFailed,
		ScheduledRunNotificationDeliveryDeadLettered:
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
	if record.DeliveryStatus == ScheduledRunNotificationDeliveryDeadLettered {
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

func normalizeScheduledRunNotificationStatsNow(now time.Time) time.Time {
	if now.IsZero() {
		return time.Now().UTC()
	}
	return now.UTC()
}

func scheduledRunNotificationStatsFromRecords(records []ScheduledRunNotificationRecord, now time.Time) ScheduledRunNotificationStats {
	now = normalizeScheduledRunNotificationStatsNow(now)
	var stats ScheduledRunNotificationStats
	for _, record := range records {
		stats.TotalCount++
		stats.DeliveryAttemptsTotal += record.DeliveryAttempts
		switch record.DeliveryStatus {
		case ScheduledRunNotificationDeliveryPending:
			stats.PendingCount++
		case ScheduledRunNotificationDeliveryDelivering:
			stats.DeliveringCount++
		case ScheduledRunNotificationDeliveryDelivered:
			stats.DeliveredCount++
		case ScheduledRunNotificationDeliveryFailed:
			stats.FailedCount++
		case ScheduledRunNotificationDeliveryDeadLettered:
			stats.DeadLetteredCount++
		}
		if record.deliveryClaimableAt(now) {
			stats.ClaimableCount++
		}
		if record.DeliveryStatus == ScheduledRunNotificationDeliveryPending || record.DeliveryStatus == ScheduledRunNotificationDeliveryFailed || record.DeliveryStatus == ScheduledRunNotificationDeliveryDelivering {
			if stats.NextClaimableAt.IsZero() || record.DeliverAfter.Before(stats.NextClaimableAt) {
				stats.NextClaimableAt = record.DeliverAfter
			}
		}
		if record.DeliveryStatus == ScheduledRunNotificationDeliveryDelivering && record.DeliverAfter.After(now) {
			stats.LeasedCount++
		}
		if record.DeliveryStatus != ScheduledRunNotificationDeliveryDelivered && record.DeliveryStatus != ScheduledRunNotificationDeliveryDeadLettered {
			if stats.OldestUndeliveredAt.IsZero() || record.CreatedAt.Before(stats.OldestUndeliveredAt) {
				stats.OldestUndeliveredAt = record.CreatedAt
			}
		}
	}
	if !stats.OldestUndeliveredAt.IsZero() && now.After(stats.OldestUndeliveredAt) {
		stats.OldestUndeliveredAge = now.Sub(stats.OldestUndeliveredAt)
	}
	return stats
}

func cloneScheduledRunNotificationDrainResult(result ScheduledRunNotificationDrainResult) ScheduledRunNotificationDrainResult {
	cloned := ScheduledRunNotificationDrainResult{
		Claimed:      append([]ScheduledRunNotificationRecord(nil), result.Claimed...),
		Delivered:    append([]ScheduledRunNotificationRecord(nil), result.Delivered...),
		Failed:       append([]ScheduledRunNotificationDrainFailure(nil), result.Failed...),
		DeadLettered: append([]ScheduledRunNotificationDrainFailure(nil), result.DeadLettered...),
	}
	return cloned
}
