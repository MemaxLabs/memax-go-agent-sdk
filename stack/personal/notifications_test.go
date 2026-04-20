package personal

import (
	"context"
	"errors"
	"strings"
	"sync"
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
