package personal

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	memaxagent "github.com/MemaxLabs/memax-go-agent-sdk"
)

func TestMemoryScheduledRunNotificationStoreCreatesAndListsIdempotently(t *testing.T) {
	t.Parallel()

	store := NewMemoryScheduledRunNotificationStore()
	createdAt := time.Date(2026, 4, 19, 7, 5, 0, 0, time.UTC)
	req := CreateScheduledRunNotificationRequest{
		ID:           "daily-brief:2026-04-19T07:00:00Z:succeeded",
		RunID:        "daily-brief:2026-04-19T07:00:00Z",
		Status:       ScheduledRunSucceeded,
		TriggerName:  "daily-brief",
		OccurrenceAt: time.Date(2026, 4, 19, 7, 0, 0, 0, time.UTC),
		Prompt:       "Prepare the morning briefing.",
		Result:       "Morning briefing ready.",
		CreatedAt:    createdAt,
	}
	record, created, err := store.CreateScheduledRunNotification(context.Background(), req)
	if err != nil {
		t.Fatalf("CreateScheduledRunNotification() error = %v", err)
	}
	if !created || record.ID != req.ID || record.Result != req.Result {
		t.Fatalf("CreateScheduledRunNotification() = (%#v, %t), want created briefing notification", record, created)
	}
	if record.DeliveryStatus != ScheduledRunNotificationDeliveryPending || record.DeliveryAttempts != 0 || record.DeliverAfter.IsZero() {
		t.Fatalf("CreateScheduledRunNotification() = %#v, want pending delivery state", record)
	}
	duplicate, created, err := store.CreateScheduledRunNotification(context.Background(), req)
	if err != nil {
		t.Fatalf("CreateScheduledRunNotification(duplicate) error = %v", err)
	}
	if created || duplicate.ID != record.ID {
		t.Fatalf("CreateScheduledRunNotification(duplicate) = (%#v, %t), want existing notification", duplicate, created)
	}

	list, err := store.ListScheduledRunNotifications(context.Background(), ScheduledRunNotificationFilter{
		RunID: record.RunID,
	})
	if err != nil {
		t.Fatalf("ListScheduledRunNotifications() error = %v", err)
	}
	if len(list) != 1 || list[0].ID != record.ID {
		t.Fatalf("ListScheduledRunNotifications() = %#v, want one matching notification", list)
	}
	list[0].Result = "mutated"
	again, err := store.ListScheduledRunNotifications(context.Background(), ScheduledRunNotificationFilter{
		Status: ScheduledRunSucceeded,
	})
	if err != nil {
		t.Fatalf("ListScheduledRunNotifications(status) error = %v", err)
	}
	if len(again) != 1 || again[0].Result != req.Result {
		t.Fatalf("ListScheduledRunNotifications() = %#v, want defensive copy", again)
	}
}

func TestMemoryScheduledRunNotificationStoreDeliveryLifecycle(t *testing.T) {
	t.Parallel()

	store := NewMemoryScheduledRunNotificationStore()
	now := time.Date(2026, 4, 19, 7, 10, 0, 0, time.UTC)
	first := scheduledRunNotificationRequestForTest("daily-brief:2026-04-19T07:00:00Z", ScheduledRunSucceeded, 0)
	second := scheduledRunNotificationRequestForTest("weekly-plan:2026-04-20T07:00:00Z", ScheduledRunSucceeded, time.Minute)
	for _, req := range []CreateScheduledRunNotificationRequest{first, second} {
		if _, _, err := store.CreateScheduledRunNotification(context.Background(), req); err != nil {
			t.Fatalf("CreateScheduledRunNotification(%q) error = %v", req.ID, err)
		}
	}

	claimed, err := store.ClaimScheduledRunNotifications(context.Background(), ClaimScheduledRunNotificationsRequest{
		WorkerID:      "worker-1",
		Limit:         1,
		Now:           now,
		LeaseDuration: time.Minute,
	})
	if err != nil {
		t.Fatalf("ClaimScheduledRunNotifications() error = %v", err)
	}
	if len(claimed) != 1 || claimed[0].ID != first.ID || claimed[0].DeliveryStatus != ScheduledRunNotificationDeliveryDelivering || claimed[0].DeliveryAttempts != 1 {
		t.Fatalf("claimed = %#v, want first notification delivering on first attempt", claimed)
	}

	if _, err := store.MarkScheduledRunNotificationDelivered(context.Background(), MarkScheduledRunNotificationDeliveredRequest{
		ID:          claimed[0].ID,
		WorkerID:    "other-worker",
		DeliveredAt: now.Add(10 * time.Second),
	}); !errors.Is(err, ErrScheduledRunNotificationWorkerMismatch) {
		t.Fatalf("MarkScheduledRunNotificationDelivered(wrong worker) error = %v, want worker mismatch", err)
	}
	delivered, err := store.MarkScheduledRunNotificationDelivered(context.Background(), MarkScheduledRunNotificationDeliveredRequest{
		ID:          claimed[0].ID,
		WorkerID:    "worker-1",
		DeliveredAt: now.Add(10 * time.Second),
	})
	if err != nil {
		t.Fatalf("MarkScheduledRunNotificationDelivered() error = %v", err)
	}
	if delivered.DeliveryStatus != ScheduledRunNotificationDeliveryDelivered || delivered.DeliveredAt.IsZero() {
		t.Fatalf("delivered = %#v, want delivered state", delivered)
	}
	duplicateDelivered, err := store.MarkScheduledRunNotificationDelivered(context.Background(), MarkScheduledRunNotificationDeliveredRequest{
		ID:          claimed[0].ID,
		WorkerID:    "worker-1",
		DeliveredAt: now.Add(20 * time.Second),
	})
	if err != nil {
		t.Fatalf("MarkScheduledRunNotificationDelivered(duplicate) error = %v", err)
	}
	if !duplicateDelivered.DeliveredAt.Equal(delivered.DeliveredAt) || !duplicateDelivered.DeliveryUpdatedAt.Equal(delivered.DeliveryUpdatedAt) {
		t.Fatalf("duplicate delivered = %#v, want idempotent original delivery timestamps %#v", duplicateDelivered, delivered)
	}
	if _, err := store.MarkScheduledRunNotificationDelivered(context.Background(), MarkScheduledRunNotificationDeliveredRequest{
		ID:          claimed[0].ID,
		WorkerID:    "other-worker",
		DeliveredAt: now.Add(30 * time.Second),
	}); !errors.Is(err, ErrScheduledRunNotificationWorkerMismatch) {
		t.Fatalf("MarkScheduledRunNotificationDelivered(duplicate wrong worker) error = %v, want worker mismatch", err)
	}
	if _, err := store.MarkScheduledRunNotificationFailed(context.Background(), MarkScheduledRunNotificationFailedRequest{
		ID:       claimed[0].ID,
		WorkerID: "worker-1",
		Error:    "late failure",
	}); !errors.Is(err, ErrScheduledRunNotificationNotDelivering) {
		t.Fatalf("MarkScheduledRunNotificationFailed(delivered) error = %v, want not delivering", err)
	}

	remaining, err := store.ClaimScheduledRunNotifications(context.Background(), ClaimScheduledRunNotificationsRequest{
		WorkerID:      "worker-2",
		Limit:         10,
		Now:           now,
		LeaseDuration: time.Minute,
	})
	if err != nil {
		t.Fatalf("ClaimScheduledRunNotifications(remaining) error = %v", err)
	}
	if len(remaining) != 1 || remaining[0].ID != second.ID {
		t.Fatalf("remaining = %#v, want only undelivered second notification", remaining)
	}
	retryAt := now.Add(2 * time.Minute)
	failedAt := now.Add(30 * time.Second)
	failed, err := store.MarkScheduledRunNotificationFailed(context.Background(), MarkScheduledRunNotificationFailedRequest{
		ID:       remaining[0].ID,
		WorkerID: "worker-2",
		Error:    "push gateway unavailable",
		RetryAt:  retryAt,
		FailedAt: failedAt,
	})
	if err != nil {
		t.Fatalf("MarkScheduledRunNotificationFailed() error = %v", err)
	}
	if failed.DeliveryStatus != ScheduledRunNotificationDeliveryFailed || failed.DeliveryError != "push gateway unavailable" || !failed.DeliverAfter.Equal(retryAt) || !failed.DeliveryUpdatedAt.Equal(failedAt) {
		t.Fatalf("failed = %#v, want failed retry state", failed)
	}
	notYet, err := store.ClaimScheduledRunNotifications(context.Background(), ClaimScheduledRunNotificationsRequest{
		WorkerID: "worker-3",
		Limit:    10,
		Now:      retryAt.Add(-time.Second),
	})
	if err != nil {
		t.Fatalf("ClaimScheduledRunNotifications(before retry) error = %v", err)
	}
	if len(notYet) != 0 {
		t.Fatalf("notYet = %#v, want no retry before RetryAt", notYet)
	}
	reclaimed, err := store.ClaimScheduledRunNotifications(context.Background(), ClaimScheduledRunNotificationsRequest{
		WorkerID: "worker-3",
		Limit:    10,
		Now:      retryAt,
	})
	if err != nil {
		t.Fatalf("ClaimScheduledRunNotifications(retry) error = %v", err)
	}
	if len(reclaimed) != 1 || reclaimed[0].ID != second.ID || reclaimed[0].DeliveryAttempts != 2 || reclaimed[0].DeliveryWorkerID != "worker-3" {
		t.Fatalf("reclaimed = %#v, want second attempt by worker-3", reclaimed)
	}
}

func TestMemoryScheduledRunNotificationStoreReclaimsExpiredLease(t *testing.T) {
	t.Parallel()

	store := NewMemoryScheduledRunNotificationStore()
	req := scheduledRunNotificationRequestForTest("daily-brief:2026-04-19T07:00:00Z", ScheduledRunSucceeded, 0)
	if _, _, err := store.CreateScheduledRunNotification(context.Background(), req); err != nil {
		t.Fatalf("CreateScheduledRunNotification() error = %v", err)
	}
	now := time.Date(2026, 4, 19, 7, 10, 0, 0, time.UTC)
	claimed, err := store.ClaimScheduledRunNotifications(context.Background(), ClaimScheduledRunNotificationsRequest{
		WorkerID:      "worker-1",
		Limit:         1,
		Now:           now,
		LeaseDuration: time.Minute,
	})
	if err != nil {
		t.Fatalf("ClaimScheduledRunNotifications() error = %v", err)
	}
	if len(claimed) != 1 {
		t.Fatalf("claimed = %#v, want one notification", claimed)
	}
	blocked, err := store.ClaimScheduledRunNotifications(context.Background(), ClaimScheduledRunNotificationsRequest{
		WorkerID: "worker-2",
		Limit:    1,
		Now:      now.Add(30 * time.Second),
	})
	if err != nil {
		t.Fatalf("ClaimScheduledRunNotifications(active lease) error = %v", err)
	}
	if len(blocked) != 0 {
		t.Fatalf("blocked = %#v, want active lease hidden", blocked)
	}
	reclaimed, err := store.ClaimScheduledRunNotifications(context.Background(), ClaimScheduledRunNotificationsRequest{
		WorkerID: "worker-2",
		Limit:    1,
		Now:      now.Add(time.Minute),
	})
	if err != nil {
		t.Fatalf("ClaimScheduledRunNotifications(expired lease) error = %v", err)
	}
	if len(reclaimed) != 1 || reclaimed[0].DeliveryWorkerID != "worker-2" || reclaimed[0].DeliveryAttempts != 2 {
		t.Fatalf("reclaimed = %#v, want expired lease reclaimed by worker-2", reclaimed)
	}
}

func TestDrainScheduledRunNotificationsDeliversClaimedRecords(t *testing.T) {
	t.Parallel()

	store := NewMemoryScheduledRunNotificationStore()
	req := scheduledRunNotificationRequestForTest("daily-brief:2026-04-19T07:00:00Z", ScheduledRunSucceeded, 0)
	if _, _, err := store.CreateScheduledRunNotification(context.Background(), req); err != nil {
		t.Fatalf("CreateScheduledRunNotification() error = %v", err)
	}
	now := time.Date(2026, 4, 19, 7, 10, 0, 0, time.UTC)
	var delivered []string
	result, err := DrainScheduledRunNotifications(
		context.Background(),
		store,
		"worker-1",
		ScheduledRunNotificationDeliveryHandlerFunc(func(_ context.Context, record ScheduledRunNotificationRecord) error {
			delivered = append(delivered, record.ID)
			return nil
		}),
		WithScheduledRunNotificationDrainNow(now),
		WithScheduledRunNotificationDrainLeaseDuration(time.Minute),
	)
	if err != nil {
		t.Fatalf("DrainScheduledRunNotifications() error = %v", err)
	}
	if len(result.Claimed) != 1 || len(result.Delivered) != 1 || len(result.Failed) != 0 {
		t.Fatalf("DrainScheduledRunNotifications() = %#v, want one delivered notification", result)
	}
	if len(delivered) != 1 || delivered[0] != req.ID {
		t.Fatalf("handler delivered %v, want %s", delivered, req.ID)
	}
	got := result.Delivered[0]
	if got.DeliveryStatus != ScheduledRunNotificationDeliveryDelivered || got.DeliveryAttempts != 1 || got.DeliveryWorkerID != "worker-1" || !got.DeliveredAt.Equal(now) {
		t.Fatalf("delivered = %#v, want first attempt delivered by worker-1 at fixed time", got)
	}
}

func TestDrainScheduledRunNotificationsRecordsRetryableHandlerFailure(t *testing.T) {
	t.Parallel()

	store := NewMemoryScheduledRunNotificationStore()
	req := scheduledRunNotificationRequestForTest("daily-brief:2026-04-19T07:00:00Z", ScheduledRunSucceeded, 0)
	if _, _, err := store.CreateScheduledRunNotification(context.Background(), req); err != nil {
		t.Fatalf("CreateScheduledRunNotification() error = %v", err)
	}
	now := time.Date(2026, 4, 19, 7, 10, 0, 0, time.UTC)
	retryAt := now.Add(2 * time.Minute)
	pushErr := errors.New("push gateway unavailable")
	result, err := DrainScheduledRunNotifications(
		context.Background(),
		store,
		"worker-1",
		ScheduledRunNotificationDeliveryHandlerFunc(func(context.Context, ScheduledRunNotificationRecord) error {
			return pushErr
		}),
		WithScheduledRunNotificationDrainNow(now),
		WithScheduledRunNotificationRetryBackoff(func(_ ScheduledRunNotificationRecord, err error, failedAt time.Time) time.Time {
			if !errors.Is(err, pushErr) || !failedAt.Equal(now) {
				t.Fatalf("retry backoff err=%v failedAt=%s, want push error at fixed time", err, failedAt)
			}
			return retryAt
		}),
	)
	if err != nil {
		t.Fatalf("DrainScheduledRunNotifications() error = %v", err)
	}
	if len(result.Claimed) != 1 || len(result.Delivered) != 0 || len(result.Failed) != 1 {
		t.Fatalf("DrainScheduledRunNotifications() = %#v, want one retryable failure", result)
	}
	if !errors.Is(result.Failed[0].Err, pushErr) {
		t.Fatalf("failure error = %v, want push error", result.Failed[0].Err)
	}
	failed := result.Failed[0].Record
	if failed.DeliveryStatus != ScheduledRunNotificationDeliveryFailed || failed.DeliveryError != pushErr.Error() || !failed.DeliverAfter.Equal(retryAt) || failed.DeliveryAttempts != 1 {
		t.Fatalf("failed = %#v, want retryable failed attempt", failed)
	}

	early, err := DrainScheduledRunNotifications(
		context.Background(),
		store,
		"worker-2",
		ScheduledRunNotificationDeliveryHandlerFunc(func(context.Context, ScheduledRunNotificationRecord) error {
			t.Fatalf("handler should not run before retry")
			return nil
		}),
		WithScheduledRunNotificationDrainNow(retryAt.Add(-time.Second)),
	)
	if err != nil {
		t.Fatalf("DrainScheduledRunNotifications(early) error = %v", err)
	}
	if len(early.Claimed) != 0 || len(early.Delivered) != 0 || len(early.Failed) != 0 {
		t.Fatalf("early drain = %#v, want no claim before retry", early)
	}

	retry, err := DrainScheduledRunNotifications(
		context.Background(),
		store,
		"worker-2",
		ScheduledRunNotificationDeliveryHandlerFunc(func(context.Context, ScheduledRunNotificationRecord) error {
			return nil
		}),
		WithScheduledRunNotificationDrainNow(retryAt),
	)
	if err != nil {
		t.Fatalf("DrainScheduledRunNotifications(retry) error = %v", err)
	}
	if len(retry.Delivered) != 1 || retry.Delivered[0].DeliveryAttempts != 2 || retry.Delivered[0].DeliveryWorkerID != "worker-2" {
		t.Fatalf("retry drain = %#v, want second attempt delivered by worker-2", retry)
	}
}

func TestDrainScheduledRunNotificationsHandlesMixedBatch(t *testing.T) {
	t.Parallel()

	store := NewMemoryScheduledRunNotificationStore()
	first := scheduledRunNotificationRequestForTest("daily-brief:2026-04-19T07:00:00Z", ScheduledRunSucceeded, 0)
	second := scheduledRunNotificationRequestForTest("weekly-plan:2026-04-20T07:00:00Z", ScheduledRunSucceeded, time.Minute)
	for _, req := range []CreateScheduledRunNotificationRequest{first, second} {
		if _, _, err := store.CreateScheduledRunNotification(context.Background(), req); err != nil {
			t.Fatalf("CreateScheduledRunNotification(%q) error = %v", req.ID, err)
		}
	}
	now := time.Date(2026, 4, 19, 7, 10, 0, 0, time.UTC)
	pushErr := errors.New("push gateway unavailable")
	result, err := DrainScheduledRunNotifications(
		context.Background(),
		store,
		"worker-1",
		ScheduledRunNotificationDeliveryHandlerFunc(func(_ context.Context, record ScheduledRunNotificationRecord) error {
			if record.ID == second.ID {
				return pushErr
			}
			return nil
		}),
		WithScheduledRunNotificationDrainLimit(2),
		WithScheduledRunNotificationDrainNow(now),
	)
	if err != nil {
		t.Fatalf("DrainScheduledRunNotifications() error = %v", err)
	}
	if len(result.Claimed) != 2 || len(result.Delivered) != 1 || len(result.Failed) != 1 {
		t.Fatalf("DrainScheduledRunNotifications() = %#v, want mixed success/failure batch", result)
	}
	if result.Delivered[0].ID != first.ID || result.Failed[0].Record.ID != second.ID {
		t.Fatalf("DrainScheduledRunNotifications() = %#v, want first delivered and second failed", result)
	}
	if !errors.Is(result.Failed[0].Err, pushErr) {
		t.Fatalf("failed error = %v, want push error", result.Failed[0].Err)
	}
}

func TestDrainScheduledRunNotificationsRejectsInvalidConfiguration(t *testing.T) {
	t.Parallel()

	store := NewMemoryScheduledRunNotificationStore()
	handler := ScheduledRunNotificationDeliveryHandlerFunc(func(context.Context, ScheduledRunNotificationRecord) error {
		return nil
	})
	if _, err := DrainScheduledRunNotifications(context.Background(), nil, "worker-1", handler); !errors.Is(err, ErrScheduledRunNotificationDeliveryStoreRequired) {
		t.Fatalf("DrainScheduledRunNotifications(nil store) error = %v, want ErrScheduledRunNotificationDeliveryStoreRequired", err)
	}
	if _, err := DrainScheduledRunNotifications(context.Background(), store, "worker-1", nil); !errors.Is(err, ErrScheduledRunNotificationDeliveryHandlerRequired) {
		t.Fatalf("DrainScheduledRunNotifications(nil handler) error = %v, want ErrScheduledRunNotificationDeliveryHandlerRequired", err)
	}
	if _, err := DrainScheduledRunNotifications(context.Background(), store, "", handler); !errors.Is(err, ErrScheduledRunNotificationDeliveryWorkerIDRequired) {
		t.Fatalf("DrainScheduledRunNotifications(empty worker) error = %v, want ErrScheduledRunNotificationDeliveryWorkerIDRequired", err)
	}
}

func TestWatchScheduledRunNotificationsDrainsImmediatelyAndStopsOnCancel(t *testing.T) {
	t.Parallel()

	store := NewMemoryScheduledRunNotificationStore()
	first := scheduledRunNotificationRequestForTest("daily-brief:2026-04-19T07:00:00Z", ScheduledRunSucceeded, 0)
	second := scheduledRunNotificationRequestForTest("weekly-plan:2026-04-20T07:00:00Z", ScheduledRunSucceeded, time.Minute)
	for _, req := range []CreateScheduledRunNotificationRequest{first, second} {
		if _, _, err := store.CreateScheduledRunNotification(context.Background(), req); err != nil {
			t.Fatalf("CreateScheduledRunNotification(%q) error = %v", req.ID, err)
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	delivered := make(chan string, 2)
	errCh := make(chan error, 1)
	go func() {
		errCh <- WatchScheduledRunNotifications(
			ctx,
			store,
			"worker-1",
			ScheduledRunNotificationDeliveryHandlerFunc(func(_ context.Context, record ScheduledRunNotificationRecord) error {
				delivered <- record.ID
				return nil
			}),
			10*time.Millisecond,
			WithScheduledRunNotificationDrainLimit(1),
			WithScheduledRunNotificationDrainLeaseDuration(time.Minute),
		)
	}()

	gotIDs := make(map[string]bool)
	for len(gotIDs) < 2 {
		select {
		case id := <-delivered:
			gotIDs[id] = true
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for watcher delivery; got %v", gotIDs)
		}
	}
	cancel()
	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("WatchScheduledRunNotifications() error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for watcher shutdown")
	}
	if !gotIDs[first.ID] || !gotIDs[second.ID] {
		t.Fatalf("delivered ids = %v, want %s and %s", gotIDs, first.ID, second.ID)
	}
	records, err := store.ListScheduledRunNotifications(context.Background(), ScheduledRunNotificationFilter{
		DeliveryStatus: ScheduledRunNotificationDeliveryDelivered,
	})
	if err != nil {
		t.Fatalf("ListScheduledRunNotifications(delivered) error = %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("delivered records = %#v, want both notifications delivered", records)
	}
}

func TestWatchScheduledRunNotificationsContinuesAfterRetryableHandlerFailure(t *testing.T) {
	t.Parallel()

	store := NewMemoryScheduledRunNotificationStore()
	req := scheduledRunNotificationRequestForTest("daily-brief:2026-04-19T07:00:00Z", ScheduledRunSucceeded, 0)
	if _, _, err := store.CreateScheduledRunNotification(context.Background(), req); err != nil {
		t.Fatalf("CreateScheduledRunNotification() error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pushErr := errors.New("push gateway unavailable")
	var attempts atomic.Int64
	delivered := make(chan struct{}, 1)
	var observedMu sync.Mutex
	var observed []ScheduledRunNotificationDrainResult
	errCh := make(chan error, 1)
	go func() {
		errCh <- WatchScheduledRunNotifications(
			ctx,
			store,
			"worker-1",
			ScheduledRunNotificationDeliveryHandlerFunc(func(_ context.Context, _ ScheduledRunNotificationRecord) error {
				if attempts.Add(1) == 1 {
					return pushErr
				}
				delivered <- struct{}{}
				return nil
			}),
			10*time.Millisecond,
			WithScheduledRunNotificationRetryBackoff(func(ScheduledRunNotificationRecord, error, time.Time) time.Time {
				return time.Time{}
			}),
			WithScheduledRunNotificationDrainResultObserver(func(_ context.Context, result ScheduledRunNotificationDrainResult) {
				observedMu.Lock()
				observed = append(observed, result)
				observedMu.Unlock()
			}),
		)
	}()

	select {
	case <-delivered:
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for retry delivery; attempts=%d", attempts.Load())
	}
	waitForDeliveredNotifications(t, store, 1)
	cancel()
	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("WatchScheduledRunNotifications() error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for watcher shutdown")
	}
	if attempts.Load() != 2 {
		t.Fatalf("attempts = %d, want first failure then retry success", attempts.Load())
	}
	var sawRetryableFailure, sawDelivery bool
	observedMu.Lock()
	observedResults := append([]ScheduledRunNotificationDrainResult(nil), observed...)
	observedMu.Unlock()
	for _, result := range observedResults {
		if len(result.Failed) == 1 && errors.Is(result.Failed[0].Err, pushErr) {
			sawRetryableFailure = true
		}
		if len(result.Delivered) == 1 {
			sawDelivery = true
		}
	}
	if !sawRetryableFailure || !sawDelivery {
		t.Fatalf("observed retryable failure=%t delivery=%t, want both", sawRetryableFailure, sawDelivery)
	}
	records := waitForDeliveredNotifications(t, store, 1)
	if len(records) != 1 || records[0].DeliveryAttempts != 2 {
		t.Fatalf("delivered records = %#v, want notification delivered on second attempt", records)
	}
}

func TestWatchScheduledRunNotificationsRejectsInvalidConfiguration(t *testing.T) {
	t.Parallel()

	store := NewMemoryScheduledRunNotificationStore()
	handler := ScheduledRunNotificationDeliveryHandlerFunc(func(context.Context, ScheduledRunNotificationRecord) error {
		return nil
	})
	if err := WatchScheduledRunNotifications(context.Background(), store, "worker-1", handler, 0); !errors.Is(err, ErrScheduledRunNotificationWatchIntervalRequired) {
		t.Fatalf("WatchScheduledRunNotifications(zero interval) error = %v, want ErrScheduledRunNotificationWatchIntervalRequired", err)
	}
	if err := WatchScheduledRunNotifications(context.Background(), nil, "worker-1", handler, time.Millisecond); !errors.Is(err, ErrScheduledRunNotificationDeliveryStoreRequired) {
		t.Fatalf("WatchScheduledRunNotifications(nil store) error = %v, want ErrScheduledRunNotificationDeliveryStoreRequired", err)
	}
}

func TestDefaultScheduledRunNotificationRetryBackoff(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 4, 19, 7, 10, 0, 0, time.UTC)
	for _, test := range []struct {
		name     string
		attempts int
		want     time.Duration
	}{
		{name: "zero attempts", attempts: 0, want: time.Minute},
		{name: "first attempt", attempts: 1, want: time.Minute},
		{name: "second attempt", attempts: 2, want: 2 * time.Minute},
		{name: "third attempt", attempts: 3, want: 4 * time.Minute},
		{name: "later attempt", attempts: 10, want: 512 * time.Minute},
		{name: "capped attempt", attempts: 24, want: 24 * time.Hour},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := DefaultScheduledRunNotificationRetryBackoff(ScheduledRunNotificationRecord{
				DeliveryAttempts: test.attempts,
			}, errors.New("push gateway unavailable"), now)
			want := now.Add(test.want)
			if !got.Equal(want) {
				t.Fatalf("DefaultScheduledRunNotificationRetryBackoff(attempts=%d) = %s, want %s", test.attempts, got, want)
			}
		})
	}
}

func TestScheduledRunNotifierDoneOnlyWritesTerminalNotifications(t *testing.T) {
	t.Parallel()

	store := NewMemoryScheduledRunNotificationStore()
	notifier, err := NewScheduledRunNotifier(store)
	if err != nil {
		t.Fatalf("NewScheduledRunNotifier() error = %v", err)
	}
	ctx := context.Background()
	notifier.ObserveEvent(ctx, scheduledRunEvent(ScheduledRunQueued, ""))
	notifier.ObserveEvent(ctx, scheduledRunEvent(ScheduledRunRunning, ""))
	notifier.ObserveEvent(ctx, scheduledRunEvent(ScheduledRunSucceeded, "Morning briefing ready."))
	notifier.ObserveEvent(ctx, scheduledRunEvent(ScheduledRunSucceeded, "Morning briefing ready."))
	notifier.ObserveEvent(ctx, scheduledRunEvent(ScheduledRunFailed, "Partial briefing ready."))

	notifications, err := store.ListScheduledRunNotifications(ctx, ScheduledRunNotificationFilter{})
	if err != nil {
		t.Fatalf("ListScheduledRunNotifications() error = %v", err)
	}
	if len(notifications) != 2 {
		t.Fatalf("notifications = %#v, want succeeded and failed terminal notifications", notifications)
	}
	gotSucceeded := notifications[0]
	if gotSucceeded.Status != ScheduledRunSucceeded || gotSucceeded.Result != "Morning briefing ready." || gotSucceeded.TriggerName != "daily-brief" {
		t.Fatalf("notification = %#v, want succeeded daily brief result", gotSucceeded)
	}
	gotFailed := notifications[1]
	if gotFailed.Status != ScheduledRunFailed || gotFailed.Result != "Partial briefing ready." || gotFailed.Error != "provider unavailable" {
		t.Fatalf("notification = %#v, want failed daily brief with partial result and error", gotFailed)
	}
}

func TestScheduledRunNotifierStateChangesWritesEachTransition(t *testing.T) {
	t.Parallel()

	store := NewMemoryScheduledRunNotificationStore()
	notifier, err := NewScheduledRunNotifier(store, WithScheduledRunNotificationPolicy(ScheduledRunNotifyStateChanges))
	if err != nil {
		t.Fatalf("NewScheduledRunNotifier() error = %v", err)
	}
	ctx := context.Background()
	notifier.ObserveEvent(ctx, scheduledRunEvent(ScheduledRunQueued, ""))
	notifier.ObserveEvent(ctx, scheduledRunEvent(ScheduledRunRunning, ""))
	notifier.ObserveEvent(ctx, scheduledRunEvent(ScheduledRunFailed, "Partial briefing ready."))

	notifications, err := store.ListScheduledRunNotifications(ctx, ScheduledRunNotificationFilter{})
	if err != nil {
		t.Fatalf("ListScheduledRunNotifications() error = %v", err)
	}
	want := []ScheduledRunStatus{ScheduledRunQueued, ScheduledRunRunning, ScheduledRunFailed}
	if len(notifications) != len(want) {
		t.Fatalf("notifications = %#v, want statuses %v", notifications, want)
	}
	for i, status := range want {
		if notifications[i].Status != status {
			t.Fatalf("notifications = %#v, want statuses %v", notifications, want)
		}
	}
}

func TestScheduledRunNotifierSilentPolicyWritesNothing(t *testing.T) {
	t.Parallel()

	store := NewMemoryScheduledRunNotificationStore()
	notifier, err := NewScheduledRunNotifier(store, WithScheduledRunNotificationPolicy(ScheduledRunNotifySilent))
	if err != nil {
		t.Fatalf("NewScheduledRunNotifier() error = %v", err)
	}
	notifier.ObserveEvent(context.Background(), scheduledRunEvent(ScheduledRunSucceeded, "Morning briefing ready."))
	notifications, err := store.ListScheduledRunNotifications(context.Background(), ScheduledRunNotificationFilter{})
	if err != nil {
		t.Fatalf("ListScheduledRunNotifications() error = %v", err)
	}
	if len(notifications) != 0 {
		t.Fatalf("notifications = %#v, want none", notifications)
	}
}

func TestScheduledRunNotifierReportsSinkErrorsWithoutBreakingObservation(t *testing.T) {
	t.Parallel()

	storeErr := errors.New("outbox unavailable")
	store := failingScheduledRunNotificationStore{err: storeErr}
	var (
		mu     sync.Mutex
		gotErr error
		gotReq CreateScheduledRunNotificationRequest
		called int
	)
	notifier, err := NewScheduledRunNotifier(store, WithScheduledRunNotificationErrorHandler(func(_ context.Context, req CreateScheduledRunNotificationRequest, err error) {
		mu.Lock()
		defer mu.Unlock()
		called++
		gotReq = req
		gotErr = err
	}))
	if err != nil {
		t.Fatalf("NewScheduledRunNotifier() error = %v", err)
	}
	notifier.ObserveEvent(context.Background(), scheduledRunEvent(ScheduledRunSucceeded, "Morning briefing ready."))

	mu.Lock()
	defer mu.Unlock()
	if called != 1 || !errors.Is(gotErr, storeErr) || gotReq.RunID != "daily-brief:2026-04-19T07:00:00Z" {
		t.Fatalf("error handler called=%d req=%#v err=%v, want store error for scheduled run", called, gotReq, gotErr)
	}
}

func TestNewScheduledRunNotifierRejectsInvalidConfiguration(t *testing.T) {
	t.Parallel()

	if _, err := NewScheduledRunNotifier(nil); !errors.Is(err, ErrScheduledRunNotificationStoreRequired) {
		t.Fatalf("NewScheduledRunNotifier(nil) error = %v, want ErrScheduledRunNotificationStoreRequired", err)
	}
	_, err := NewScheduledRunNotifier(NewMemoryScheduledRunNotificationStore(), WithScheduledRunNotificationPolicy("loud"))
	if err == nil || !strings.Contains(err.Error(), "unknown scheduled run notification policy") {
		t.Fatalf("NewScheduledRunNotifier(policy) error = %v, want unknown policy", err)
	}
}

type failingScheduledRunNotificationStore struct {
	err error
}

func (s failingScheduledRunNotificationStore) CreateScheduledRunNotification(context.Context, CreateScheduledRunNotificationRequest) (ScheduledRunNotificationRecord, bool, error) {
	return ScheduledRunNotificationRecord{}, false, s.err
}

func (s failingScheduledRunNotificationStore) ListScheduledRunNotifications(context.Context, ScheduledRunNotificationFilter) ([]ScheduledRunNotificationRecord, error) {
	return nil, s.err
}

func scheduledRunEvent(status ScheduledRunStatus, result string) memaxagent.Event {
	eventTime := time.Date(2026, 4, 19, 7, 5, 0, 0, time.UTC)
	run := &memaxagent.RunEvent{
		RunID:        "daily-brief:2026-04-19T07:00:00Z",
		Status:       string(status),
		Prompt:       "Prepare the morning briefing.",
		TriggerName:  "daily-brief",
		OccurrenceAt: time.Date(2026, 4, 19, 7, 0, 0, 0, time.UTC),
		Result:       result,
	}
	if status == ScheduledRunFailed {
		run.Error = "provider unavailable"
	}
	return memaxagent.Event{
		Kind: memaxagent.EventRunStateChanged,
		Time: eventTime,
		Run:  run,
	}
}

func scheduledRunNotificationRequestForTest(runID string, status ScheduledRunStatus, offset time.Duration) CreateScheduledRunNotificationRequest {
	createdAt := time.Date(2026, 4, 19, 7, 5, 0, 0, time.UTC).Add(offset)
	return CreateScheduledRunNotificationRequest{
		ID:           runID + ":" + string(status),
		RunID:        runID,
		Status:       status,
		TriggerName:  "daily-brief",
		OccurrenceAt: time.Date(2026, 4, 19, 7, 0, 0, 0, time.UTC),
		Prompt:       "Prepare the morning briefing.",
		Result:       "Morning briefing ready.",
		CreatedAt:    createdAt,
	}
}

func waitForDeliveredNotifications(t *testing.T, store ScheduledRunNotificationStore, count int) []ScheduledRunNotificationRecord {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		records, err := store.ListScheduledRunNotifications(context.Background(), ScheduledRunNotificationFilter{
			DeliveryStatus: ScheduledRunNotificationDeliveryDelivered,
		})
		if err != nil {
			t.Fatalf("ListScheduledRunNotifications(delivered) error = %v", err)
		}
		if len(records) >= count {
			return records
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %d delivered notifications; got %#v", count, records)
		}
		time.Sleep(time.Millisecond)
	}
}
