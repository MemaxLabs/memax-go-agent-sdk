package personal

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	memaxagent "github.com/MemaxLabs/memax-go-agent-sdk"
	"github.com/MemaxLabs/memax-go-agent-sdk/model"
)

func TestMemoryScheduledRunStoreCreateUpdateAndGet(t *testing.T) {
	t.Parallel()

	store := NewMemoryScheduledRunStore()
	occurrence := time.Date(2026, 4, 19, 7, 0, 0, 0, time.UTC)
	record, created, err := store.CreateScheduledRun(context.Background(), CreateScheduledRunRequest{
		ID:           "daily-brief:2026-04-19T07:00:00Z",
		TriggerName:  "daily-brief",
		OccurrenceAt: occurrence,
		Prompt:       "Prepare the morning briefing.",
	})
	if err != nil {
		t.Fatalf("CreateScheduledRun() error = %v", err)
	}
	if !created || record.Status != ScheduledRunQueued {
		t.Fatalf("CreateScheduledRun() = (%#v, %t), want queued created record", record, created)
	}
	duplicate, created, err := store.CreateScheduledRun(context.Background(), CreateScheduledRunRequest{
		ID:           record.ID,
		TriggerName:  "daily-brief",
		OccurrenceAt: occurrence,
		Prompt:       "Prepare the morning briefing.",
	})
	if err != nil {
		t.Fatalf("CreateScheduledRun(duplicate) error = %v", err)
	}
	if created || duplicate.ID != record.ID {
		t.Fatalf("CreateScheduledRun(duplicate) = (%#v, %t), want existing record and created=false", duplicate, created)
	}

	completedAt := time.Date(2026, 4, 19, 7, 1, 0, 0, time.UTC)
	result := "Morning briefing ready."
	record, err = store.UpdateScheduledRun(context.Background(), ScheduledRunUpdate{
		ID:          record.ID,
		Status:      ScheduledRunSucceeded,
		SessionID:   "session-1",
		Result:      &result,
		CompletedAt: &completedAt,
	})
	if err != nil {
		t.Fatalf("UpdateScheduledRun() error = %v", err)
	}
	if record.Status != ScheduledRunSucceeded || record.SessionID != "session-1" || record.Result != result {
		t.Fatalf("UpdateScheduledRun() = %#v, want succeeded persisted record", record)
	}
	got, err := store.GetScheduledRun(context.Background(), record.ID)
	if err != nil {
		t.Fatalf("GetScheduledRun() error = %v", err)
	}
	if got.TriggerName != "daily-brief" || got.OccurrenceAt != occurrence {
		t.Fatalf("GetScheduledRun() = %#v, want trigger name and occurrence", got)
	}

	errText := "late failure"
	unchanged, err := store.UpdateScheduledRun(context.Background(), ScheduledRunUpdate{
		ID:     record.ID,
		Status: ScheduledRunFailed,
		Error:  &errText,
	})
	if err != nil {
		t.Fatalf("UpdateScheduledRun(terminal) error = %v", err)
	}
	if unchanged.Status != ScheduledRunSucceeded || unchanged.Result != result || unchanged.Error != "" {
		t.Fatalf("UpdateScheduledRun(terminal) = %#v, want terminal record unchanged", unchanged)
	}
}

func TestPeriodicTriggerIntentAtUsesDeterministicOccurrenceID(t *testing.T) {
	t.Parallel()

	trigger := PeriodicTrigger{
		Name:   "daily-brief",
		Prompt: "Prepare the morning briefing.",
		Every:  24 * time.Hour,
		Anchor: time.Date(2026, 4, 19, 7, 0, 0, 0, time.UTC),
	}
	intent, due := trigger.IntentAt(time.Date(2026, 4, 19, 7, 30, 0, 0, time.UTC))
	if !due {
		t.Fatal("IntentAt() due = false, want true")
	}
	if intent.ID != "daily-brief:2026-04-19T07:00:00Z" {
		t.Fatalf("intent id = %q, want deterministic daily occurrence", intent.ID)
	}
}

func TestStackStartScheduledRunPersistsAndDeduplicates(t *testing.T) {
	t.Parallel()

	modelClient := &scheduledRunModel{
		turns: [][]model.StreamEvent{
			{{Kind: model.StreamText, Text: "Morning briefing ready."}},
		},
	}
	stack, err := New(Config{
		Base: memaxagent.Options{
			Model: modelClient,
		},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	store := NewMemoryScheduledRunStore()
	intent := ScheduledIntent{
		ID:           "daily-brief:2026-04-19T07:00:00Z",
		TriggerName:  "daily-brief",
		OccurrenceAt: time.Date(2026, 4, 19, 7, 0, 0, 0, time.UTC),
		Prompt:       "Prepare the morning briefing.",
	}
	record, created, err := stack.StartScheduledRun(context.Background(), store, intent)
	if err != nil {
		t.Fatalf("StartScheduledRun() error = %v", err)
	}
	if !created || record.Status != ScheduledRunQueued {
		t.Fatalf("StartScheduledRun() = (%#v, %t), want queued created record", record, created)
	}
	final := waitForScheduledRun(t, store, record.ID, func(r ScheduledRunRecord) bool { return r.Terminal() })
	if final.Status != ScheduledRunSucceeded || final.Result != "Morning briefing ready." || final.SessionID == "" {
		t.Fatalf("final run = %#v, want succeeded persisted proactive run", final)
	}
	duplicate, created, err := stack.StartScheduledRun(context.Background(), store, intent)
	if err != nil {
		t.Fatalf("StartScheduledRun(duplicate) error = %v", err)
	}
	if created || duplicate.ID != record.ID {
		t.Fatalf("StartScheduledRun(duplicate) = (%#v, %t), want existing record created=false", duplicate, created)
	}
	if len(modelClient.requests) != 1 {
		t.Fatalf("model requests = %d, want 1", len(modelClient.requests))
	}
}

func TestStackStartScheduledRunEmitsLifecycleEventsAndSuppressesDuplicateQueued(t *testing.T) {
	t.Parallel()

	modelClient := &scheduledRunModel{
		turns: [][]model.StreamEvent{
			{{Kind: model.StreamText, Text: "Morning briefing ready."}},
		},
	}
	stack, err := New(Config{
		Base: memaxagent.Options{
			Model: modelClient,
		},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	store := NewMemoryScheduledRunStore()
	intent := ScheduledIntent{
		ID:           "daily-brief:2026-04-19T07:00:00Z",
		TriggerName:  "daily-brief",
		OccurrenceAt: time.Date(2026, 4, 19, 7, 0, 0, 0, time.UTC),
		Prompt:       "Prepare the morning briefing.",
	}
	var (
		mu     sync.Mutex
		events []memaxagent.RunEvent
	)
	observer := memaxagent.EventObserverFunc(func(_ context.Context, event memaxagent.Event) {
		if event.Kind != memaxagent.EventRunStateChanged || event.Run == nil {
			return
		}
		mu.Lock()
		events = append(events, *event.Run)
		mu.Unlock()
	})

	ctx := memaxagent.WithEventObserver(context.Background(), observer)
	record, created, err := stack.StartScheduledRun(ctx, store, intent)
	if err != nil {
		t.Fatalf("StartScheduledRun() error = %v", err)
	}
	if !created {
		t.Fatal("StartScheduledRun() created = false, want true")
	}
	final := waitForScheduledRun(t, store, record.ID, func(r ScheduledRunRecord) bool { return r.Terminal() })
	if final.Status != ScheduledRunSucceeded {
		t.Fatalf("final run = %#v, want succeeded", final)
	}
	got := waitForObservedRunEvents(t, func() []memaxagent.RunEvent {
		mu.Lock()
		defer mu.Unlock()
		return append([]memaxagent.RunEvent(nil), events...)
	}, 3)
	if len(got) != 3 {
		t.Fatalf("run lifecycle events = %#v, want queued running succeeded", got)
	}
	wantStatuses := []string{
		string(ScheduledRunQueued),
		string(ScheduledRunRunning),
		string(ScheduledRunSucceeded),
	}
	for i, want := range wantStatuses {
		if got[i].Status != want {
			t.Fatalf("event statuses = %#v, want %v", got, wantStatuses)
		}
		if got[i].RunID != intent.ID || got[i].TriggerName != intent.TriggerName || got[i].OccurrenceAt != intent.OccurrenceAt {
			t.Fatalf("event %d = %#v, want scheduled run identity", i, got[i])
		}
		if got[i].Prompt != intent.Prompt {
			t.Fatalf("event %d prompt = %q, want %q", i, got[i].Prompt, intent.Prompt)
		}
	}

	duplicate, created, err := stack.StartScheduledRun(ctx, store, intent)
	if err != nil {
		t.Fatalf("StartScheduledRun(duplicate) error = %v", err)
	}
	if created || duplicate.ID != final.ID {
		t.Fatalf("StartScheduledRun(duplicate) = (%#v, %t), want existing record", duplicate, created)
	}
	mu.Lock()
	afterDuplicate := len(events)
	mu.Unlock()
	if afterDuplicate != len(got) {
		t.Fatalf("events after duplicate = %d, want %d", afterDuplicate, len(got))
	}
}

func TestStackStartScheduledRunEmitsFailedLifecycleEvent(t *testing.T) {
	t.Parallel()

	stack, err := New(Config{
		Base: memaxagent.Options{
			Model: failingScheduledRunModel{err: errors.New("provider unavailable")},
		},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	store := NewMemoryScheduledRunStore()
	intent := ScheduledIntent{
		ID:           "daily-brief:2026-04-19T07:00:00Z",
		TriggerName:  "daily-brief",
		OccurrenceAt: time.Date(2026, 4, 19, 7, 0, 0, 0, time.UTC),
		Prompt:       "Prepare the morning briefing.",
	}
	var (
		mu     sync.Mutex
		events []memaxagent.RunEvent
	)
	observer := memaxagent.EventObserverFunc(func(_ context.Context, event memaxagent.Event) {
		if event.Kind != memaxagent.EventRunStateChanged || event.Run == nil {
			return
		}
		mu.Lock()
		events = append(events, *event.Run)
		mu.Unlock()
	})

	record, created, err := stack.StartScheduledRun(memaxagent.WithEventObserver(context.Background(), observer), store, intent)
	if err != nil {
		t.Fatalf("StartScheduledRun() error = %v", err)
	}
	if !created {
		t.Fatal("StartScheduledRun() created = false, want true")
	}
	final := waitForScheduledRun(t, store, record.ID, func(r ScheduledRunRecord) bool { return r.Terminal() })
	if final.Status != ScheduledRunFailed || !strings.Contains(final.Error, "provider unavailable") {
		t.Fatalf("final run = %#v, want failed provider error", final)
	}
	got := waitForObservedRunEvents(t, func() []memaxagent.RunEvent {
		mu.Lock()
		defer mu.Unlock()
		return append([]memaxagent.RunEvent(nil), events...)
	}, 3)
	if len(got) != 3 {
		t.Fatalf("run lifecycle events = %#v, want queued running failed", got)
	}
	if got[2].Status != string(ScheduledRunFailed) || !strings.Contains(got[2].Error, "provider unavailable") {
		t.Fatalf("terminal event = %#v, want failed event with provider error", got[2])
	}
}

func TestObserveScheduledRunStateIncludesFailedPartialResult(t *testing.T) {
	t.Parallel()

	var got memaxagent.RunEvent
	observer := memaxagent.EventObserverFunc(func(_ context.Context, event memaxagent.Event) {
		if event.Kind == memaxagent.EventRunStateChanged && event.Run != nil {
			got = *event.Run
		}
	})
	ctx := memaxagent.WithEventObserver(context.Background(), observer)
	observeScheduledRunState(ctx, ScheduledRunRecord{
		ID:           "daily-brief:2026-04-19T07:00:00Z",
		TriggerName:  "daily-brief",
		OccurrenceAt: time.Date(2026, 4, 19, 7, 0, 0, 0, time.UTC),
		Prompt:       "Prepare the morning briefing.",
		Status:       ScheduledRunFailed,
		Result:       "Partial briefing ready.",
		Error:        "provider unavailable",
		UpdatedAt:    time.Date(2026, 4, 19, 7, 5, 0, 0, time.UTC),
	})
	if got.Status != string(ScheduledRunFailed) || got.Result != "Partial briefing ready." || got.Error != "provider unavailable" {
		t.Fatalf("observed run = %#v, want failed partial result and error", got)
	}
	observeScheduledRunState(ctx, ScheduledRunRecord{
		ID:           "daily-brief:2026-04-19T07:00:00Z",
		TriggerName:  "daily-brief",
		OccurrenceAt: time.Date(2026, 4, 19, 7, 0, 0, 0, time.UTC),
		Prompt:       "Prepare the morning briefing.",
		Status:       ScheduledRunRunning,
		Result:       "Should not surface before terminal.",
		UpdatedAt:    time.Date(2026, 4, 19, 7, 1, 0, 0, time.UTC),
	})
	if got.Status != string(ScheduledRunRunning) || got.Result != "" {
		t.Fatalf("observed run = %#v, want non-terminal event without result", got)
	}
}

func TestStackStartScheduledRunStopsWhenRunningTransitionFails(t *testing.T) {
	t.Parallel()

	modelClient := &scheduledRunModel{
		turns: [][]model.StreamEvent{
			{{Kind: model.StreamText, Text: "Morning briefing ready."}},
		},
	}
	stack, err := New(Config{
		Base: memaxagent.Options{
			Model: modelClient,
		},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	baseStore := NewMemoryScheduledRunStore()
	store := &failingScheduledRunUpdateStore{
		ScheduledRunStore: baseStore,
		failStatus:        ScheduledRunRunning,
		err:               errors.New("persist running"),
		called:            make(chan struct{}),
	}
	intent := ScheduledIntent{
		ID:           "daily-brief:2026-04-19T07:00:00Z",
		TriggerName:  "daily-brief",
		OccurrenceAt: time.Date(2026, 4, 19, 7, 0, 0, 0, time.UTC),
		Prompt:       "Prepare the morning briefing.",
	}
	var (
		mu     sync.Mutex
		events []memaxagent.RunEvent
	)
	observer := memaxagent.EventObserverFunc(func(_ context.Context, event memaxagent.Event) {
		if event.Kind != memaxagent.EventRunStateChanged || event.Run == nil {
			return
		}
		mu.Lock()
		events = append(events, *event.Run)
		mu.Unlock()
	})

	record, created, err := stack.StartScheduledRun(memaxagent.WithEventObserver(context.Background(), observer), store, intent)
	if err != nil {
		t.Fatalf("StartScheduledRun() error = %v", err)
	}
	if !created {
		t.Fatal("StartScheduledRun() created = false, want true")
	}
	select {
	case <-store.called:
	case <-time.After(5 * time.Second):
		t.Fatal("scheduled run did not attempt running transition")
	}
	final, err := baseStore.GetScheduledRun(context.Background(), record.ID)
	if err != nil {
		t.Fatalf("GetScheduledRun() error = %v", err)
	}
	if final.Status != ScheduledRunQueued {
		t.Fatalf("final run = %#v, want still queued after failed running transition", final)
	}
	if len(modelClient.requests) != 0 {
		t.Fatalf("model requests = %d, want none after failed running transition", len(modelClient.requests))
	}
	mu.Lock()
	got := append([]memaxagent.RunEvent(nil), events...)
	mu.Unlock()
	if len(got) != 1 || got[0].Status != string(ScheduledRunQueued) {
		t.Fatalf("run lifecycle events = %#v, want only queued event", got)
	}
}

func TestMemoryScheduledRunStoreFailsStaleQueuedAndRunningRuns(t *testing.T) {
	t.Parallel()

	store := NewMemoryScheduledRunStore()
	now := time.Date(2026, 4, 19, 7, 0, 0, 0, time.UTC)
	for _, req := range []CreateScheduledRunRequest{
		{
			ID:           "stale-queued",
			TriggerName:  "daily-brief",
			OccurrenceAt: now,
			Prompt:       "Prepare the morning briefing.",
		},
		{
			ID:           "stale-running",
			TriggerName:  "daily-brief",
			OccurrenceAt: now,
			Prompt:       "Prepare the morning briefing.",
		},
		{
			ID:           "fresh-queued",
			TriggerName:  "daily-brief",
			OccurrenceAt: now,
			Prompt:       "Prepare the morning briefing.",
		},
		{
			ID:           "terminal",
			TriggerName:  "daily-brief",
			OccurrenceAt: now,
			Prompt:       "Prepare the morning briefing.",
		},
	} {
		if _, _, err := store.CreateScheduledRun(context.Background(), req); err != nil {
			t.Fatalf("CreateScheduledRun(%q) error = %v", req.ID, err)
		}
	}
	if _, err := store.UpdateScheduledRun(context.Background(), ScheduledRunUpdate{
		ID:     "stale-running",
		Status: ScheduledRunRunning,
	}); err != nil {
		t.Fatalf("UpdateScheduledRun(stale-running) error = %v", err)
	}
	completedAt := now.Add(1 * time.Minute)
	result := "done"
	if _, err := store.UpdateScheduledRun(context.Background(), ScheduledRunUpdate{
		ID:          "terminal",
		Status:      ScheduledRunSucceeded,
		Result:      &result,
		CompletedAt: &completedAt,
	}); err != nil {
		t.Fatalf("UpdateScheduledRun(terminal) error = %v", err)
	}

	staleUpdatedAt := time.Now().UTC().Add(-2 * time.Hour)
	store.mu.Lock()
	for _, id := range []string{"stale-queued", "stale-running", "terminal"} {
		record := store.runs[id]
		record.UpdatedAt = staleUpdatedAt
		store.runs[id] = record
	}
	store.mu.Unlock()

	failed, err := store.FailStaleScheduledRuns(context.Background(), time.Now().UTC().Add(-time.Hour), "reconciled stale run")
	if err != nil {
		t.Fatalf("FailStaleScheduledRuns() error = %v", err)
	}
	if len(failed) != 2 {
		t.Fatalf("failed = %#v, want stale queued and running only", failed)
	}
	for _, id := range []string{"stale-queued", "stale-running"} {
		record, err := store.GetScheduledRun(context.Background(), id)
		if err != nil {
			t.Fatalf("GetScheduledRun(%q) error = %v", id, err)
		}
		if record.Status != ScheduledRunFailed || record.Error != "reconciled stale run" || record.CompletedAt.IsZero() {
			t.Fatalf("record %q = %#v, want failed stale run", id, record)
		}
	}
	for _, id := range []string{"fresh-queued", "terminal"} {
		record, err := store.GetScheduledRun(context.Background(), id)
		if err != nil {
			t.Fatalf("GetScheduledRun(%q) error = %v", id, err)
		}
		if record.Status == ScheduledRunFailed {
			t.Fatalf("record %q = %#v, want not failed", id, record)
		}
	}
}

func TestStackFailStaleScheduledRunsEmitsLifecycleEvents(t *testing.T) {
	t.Parallel()

	stack, err := New(Config{
		Base: memaxagent.Options{
			Model: &scheduledRunModel{},
		},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	store := NewMemoryScheduledRunStore()
	if _, _, err := store.CreateScheduledRun(context.Background(), CreateScheduledRunRequest{
		ID:           "daily-brief:2026-04-19T07:00:00Z",
		TriggerName:  "daily-brief",
		OccurrenceAt: time.Date(2026, 4, 19, 7, 0, 0, 0, time.UTC),
		Prompt:       "Prepare the morning briefing.",
	}); err != nil {
		t.Fatalf("CreateScheduledRun() error = %v", err)
	}
	store.mu.Lock()
	record := store.runs["daily-brief:2026-04-19T07:00:00Z"]
	record.UpdatedAt = time.Now().UTC().Add(-2 * time.Hour)
	store.runs[record.ID] = record
	store.mu.Unlock()

	var (
		mu     sync.Mutex
		events []memaxagent.RunEvent
	)
	observer := memaxagent.EventObserverFunc(func(_ context.Context, event memaxagent.Event) {
		if event.Kind != memaxagent.EventRunStateChanged || event.Run == nil {
			return
		}
		mu.Lock()
		events = append(events, *event.Run)
		mu.Unlock()
	})

	count, err := stack.FailStaleScheduledRuns(memaxagent.WithEventObserver(context.Background(), observer), store, time.Now().UTC().Add(-time.Hour), "scheduled reconciliation")
	if err != nil {
		t.Fatalf("FailStaleScheduledRuns() error = %v", err)
	}
	if count != 1 {
		t.Fatalf("FailStaleScheduledRuns() count = %d, want 1", count)
	}
	got := waitForObservedRunEvents(t, func() []memaxagent.RunEvent {
		mu.Lock()
		defer mu.Unlock()
		return append([]memaxagent.RunEvent(nil), events...)
	}, 1)
	if got[0].RunID != record.ID || got[0].Status != string(ScheduledRunFailed) || got[0].Error != "scheduled reconciliation" {
		t.Fatalf("run event = %#v, want stale failure", got[0])
	}
}

func TestStackWatchStaleScheduledRunsFailsExpiredRun(t *testing.T) {
	t.Parallel()

	stack, err := New(Config{
		Base: memaxagent.Options{
			Model: &scheduledRunModel{},
		},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	store := NewMemoryScheduledRunStore()
	if _, _, err := store.CreateScheduledRun(context.Background(), CreateScheduledRunRequest{
		ID:           "daily-brief:2026-04-19T07:00:00Z",
		TriggerName:  "daily-brief",
		OccurrenceAt: time.Date(2026, 4, 19, 7, 0, 0, 0, time.UTC),
		Prompt:       "Prepare the morning briefing.",
	}); err != nil {
		t.Fatalf("CreateScheduledRun() error = %v", err)
	}
	store.mu.Lock()
	record := store.runs["daily-brief:2026-04-19T07:00:00Z"]
	record.UpdatedAt = time.Now().UTC().Add(-2 * time.Hour)
	store.runs[record.ID] = record
	store.mu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- stack.WatchStaleScheduledRuns(ctx, store, time.Millisecond, time.Hour)
	}()
	final := waitForScheduledRun(t, store, record.ID, func(r ScheduledRunRecord) bool { return r.Terminal() })
	cancel()
	if final.Status != ScheduledRunFailed || final.Error != staleScheduledRunFailureReason {
		t.Fatalf("final run = %#v, want stale failure", final)
	}
	if err := <-errCh; !errors.Is(err, context.Canceled) {
		t.Fatalf("WatchStaleScheduledRuns() error = %v, want context.Canceled", err)
	}
}

func TestStackStartScheduledRunDoesNotOverwriteStaleFailureAfterLateCompletion(t *testing.T) {
	modelClient := newBlockingScheduledRunModel()
	stack, err := New(Config{
		Base: memaxagent.Options{
			Model: modelClient,
		},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	store := NewMemoryScheduledRunStore()
	intent := ScheduledIntent{
		ID:           "daily-brief:2026-04-19T07:00:00Z",
		TriggerName:  "daily-brief",
		OccurrenceAt: time.Date(2026, 4, 19, 7, 0, 0, 0, time.UTC),
		Prompt:       "Prepare the morning briefing.",
	}
	var (
		mu     sync.Mutex
		events []memaxagent.RunEvent
	)
	observer := memaxagent.EventObserverFunc(func(_ context.Context, event memaxagent.Event) {
		if event.Kind != memaxagent.EventRunStateChanged || event.Run == nil {
			return
		}
		mu.Lock()
		events = append(events, *event.Run)
		mu.Unlock()
	})

	record, created, err := stack.StartScheduledRun(memaxagent.WithEventObserver(context.Background(), observer), store, intent)
	if err != nil {
		t.Fatalf("StartScheduledRun() error = %v", err)
	}
	if !created {
		t.Fatal("StartScheduledRun() created = false, want true")
	}
	_ = waitForScheduledRun(t, store, record.ID, func(r ScheduledRunRecord) bool { return r.Status == ScheduledRunRunning })
	select {
	case <-modelClient.blocked:
	case <-time.After(5 * time.Second):
		t.Fatal("scheduled run model did not block")
	}

	staleUpdatedAt := time.Now().UTC().Add(-2 * time.Hour)
	store.mu.Lock()
	running := store.runs[record.ID]
	running.UpdatedAt = staleUpdatedAt
	store.runs[record.ID] = running
	store.mu.Unlock()

	count, err := stack.FailStaleScheduledRuns(memaxagent.WithEventObserver(context.Background(), observer), store, time.Now().UTC().Add(-time.Hour), staleScheduledRunFailureReason)
	if err != nil {
		t.Fatalf("FailStaleScheduledRuns() error = %v", err)
	}
	if count != 1 {
		t.Fatalf("FailStaleScheduledRuns() count = %d, want 1", count)
	}
	modelClient.releaseStream()
	select {
	case <-modelClient.done:
	case <-time.After(5 * time.Second):
		t.Fatal("scheduled run model did not finish")
	}

	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		final, err := store.GetScheduledRun(context.Background(), record.ID)
		if err != nil {
			t.Fatalf("GetScheduledRun() error = %v", err)
		}
		if final.Status != ScheduledRunFailed || final.Error != staleScheduledRunFailureReason {
			t.Fatalf("final run = %#v, want stale failure to remain terminal", final)
		}
		time.Sleep(10 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, event := range events {
		if event.Status == string(ScheduledRunSucceeded) {
			t.Fatalf("events = %#v, want no late succeeded event after stale failure", events)
		}
	}
}

func TestStackExecuteScheduledRunDoesNotReopenTerminalRun(t *testing.T) {
	t.Parallel()

	modelClient := &scheduledRunModel{}
	stack, err := New(Config{
		Base: memaxagent.Options{
			Model: modelClient,
		},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	store := NewMemoryScheduledRunStore()
	record, _, err := store.CreateScheduledRun(context.Background(), CreateScheduledRunRequest{
		ID:           "daily-brief:2026-04-19T07:00:00Z",
		TriggerName:  "daily-brief",
		OccurrenceAt: time.Date(2026, 4, 19, 7, 0, 0, 0, time.UTC),
		Prompt:       "Prepare the morning briefing.",
	})
	if err != nil {
		t.Fatalf("CreateScheduledRun() error = %v", err)
	}
	reason := "scheduled reconciliation"
	completedAt := time.Now().UTC()
	if _, err := store.UpdateScheduledRun(context.Background(), ScheduledRunUpdate{
		ID:          record.ID,
		Status:      ScheduledRunFailed,
		Error:       &reason,
		CompletedAt: &completedAt,
	}); err != nil {
		t.Fatalf("UpdateScheduledRun(failed) error = %v", err)
	}

	var (
		mu     sync.Mutex
		events []memaxagent.RunEvent
	)
	observer := memaxagent.EventObserverFunc(func(_ context.Context, event memaxagent.Event) {
		if event.Kind != memaxagent.EventRunStateChanged || event.Run == nil {
			return
		}
		mu.Lock()
		events = append(events, *event.Run)
		mu.Unlock()
	})

	stack.executeScheduledRun(memaxagent.WithEventObserver(context.Background(), observer), store, record)

	final, err := store.GetScheduledRun(context.Background(), record.ID)
	if err != nil {
		t.Fatalf("GetScheduledRun() error = %v", err)
	}
	if final.Status != ScheduledRunFailed || final.Error != reason {
		t.Fatalf("final run = %#v, want terminal failed run unchanged", final)
	}
	if len(modelClient.requests) != 0 {
		t.Fatalf("model requests = %d, want none for terminal run", len(modelClient.requests))
	}
	mu.Lock()
	defer mu.Unlock()
	if len(events) != 0 {
		t.Fatalf("events = %#v, want no reopened lifecycle events", events)
	}
}

func TestStackFireScheduledTriggersStartsDueOnlyAndDeduplicates(t *testing.T) {
	t.Parallel()

	modelClient := &scheduledRunModel{
		turns: [][]model.StreamEvent{
			{{Kind: model.StreamText, Text: "Morning briefing ready."}},
		},
	}
	stack, err := New(Config{
		Base: memaxagent.Options{
			Model: modelClient,
		},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	store := NewMemoryScheduledRunStore()
	now := time.Date(2026, 4, 19, 7, 1, 0, 0, time.UTC)
	due := PeriodicTrigger{
		Name:   "daily-brief",
		Prompt: "Prepare the morning briefing.",
		Every:  24 * time.Hour,
		Anchor: time.Date(2026, 4, 19, 7, 0, 0, 0, time.UTC),
	}
	notDue := PeriodicTrigger{
		Name:   "tomorrow-brief",
		Prompt: "Prepare tomorrow's briefing.",
		Every:  24 * time.Hour,
		Anchor: time.Date(2026, 4, 20, 7, 0, 0, 0, time.UTC),
	}

	results, err := stack.FireScheduledTriggers(context.Background(), store, now, due, notDue)
	if err != nil {
		t.Fatalf("FireScheduledTriggers() error = %v", err)
	}
	if len(results) != 1 || !results[0].Created || results[0].Record.ID != "daily-brief:2026-04-19T07:00:00Z" {
		t.Fatalf("FireScheduledTriggers() = %#v, want one created due run", results)
	}
	final := waitForScheduledRun(t, store, results[0].Record.ID, func(r ScheduledRunRecord) bool { return r.Terminal() })
	if final.Status != ScheduledRunSucceeded || final.Result != "Morning briefing ready." || final.SessionID == "" {
		t.Fatalf("final run = %#v, want succeeded proactive run", final)
	}

	duplicates, err := stack.FireScheduledTriggers(context.Background(), store, now, due, notDue)
	if err != nil {
		t.Fatalf("FireScheduledTriggers(duplicate) error = %v", err)
	}
	if len(duplicates) != 1 || duplicates[0].Created || duplicates[0].Record.ID != final.ID {
		t.Fatalf("FireScheduledTriggers(duplicate) = %#v, want existing due run created=false", duplicates)
	}
	if len(modelClient.requests) != 1 {
		t.Fatalf("model requests = %d, want one run after duplicate fire", len(modelClient.requests))
	}
}

func TestStackFireScheduledTriggersReturnsSuccessfulResultsOnLaterFailure(t *testing.T) {
	t.Parallel()

	modelClient := &scheduledRunModel{
		turns: [][]model.StreamEvent{
			{{Kind: model.StreamText, Text: "Morning briefing ready."}},
		},
	}
	stack, err := New(Config{
		Base: memaxagent.Options{
			Model: modelClient,
		},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	store := NewMemoryScheduledRunStore()
	now := time.Date(2026, 4, 19, 7, 1, 0, 0, time.UTC)
	good := staticScheduledTrigger{intent: ScheduledIntent{
		ID:           "daily-brief:2026-04-19T07:00:00Z",
		TriggerName:  "daily-brief",
		OccurrenceAt: time.Date(2026, 4, 19, 7, 0, 0, 0, time.UTC),
		Prompt:       "Prepare the morning briefing.",
	}}
	bad := staticScheduledTrigger{intent: ScheduledIntent{
		ID:           "invalid-brief:2026-04-19T07:00:00Z",
		TriggerName:  "invalid-brief",
		OccurrenceAt: time.Date(2026, 4, 19, 7, 0, 0, 0, time.UTC),
	}}

	results, err := stack.FireScheduledTriggers(context.Background(), store, now, good, bad)
	if err == nil {
		t.Fatal("FireScheduledTriggers() error = nil, want invalid prompt error")
	}
	if len(results) != 1 || !results[0].Created || results[0].Record.ID != good.intent.ID {
		t.Fatalf("FireScheduledTriggers() results = %#v, want successful first fire preserved", results)
	}
	final := waitForScheduledRun(t, store, good.intent.ID, func(r ScheduledRunRecord) bool { return r.Terminal() })
	if final.Status != ScheduledRunSucceeded {
		t.Fatalf("final run = %#v, want first run to complete despite later failure", final)
	}
}

func TestMemoryScheduledWorkflowRegistryListsDefensiveCopies(t *testing.T) {
	t.Parallel()

	trigger := PeriodicTrigger{
		Name:   "daily-brief",
		Prompt: "Prepare the morning briefing.",
		Every:  24 * time.Hour,
		Anchor: time.Date(2026, 4, 19, 7, 0, 0, 0, time.UTC),
	}
	registry, err := NewMemoryScheduledWorkflowRegistry(ScheduledWorkflow{
		Name:        " daily-brief ",
		Description: " Prepare the morning briefing. ",
		Tags:        []string{" briefings ", "", " personal "},
		Trigger:     trigger,
	})
	if err != nil {
		t.Fatalf("NewMemoryScheduledWorkflowRegistry() error = %v", err)
	}
	workflows, err := registry.ListScheduledWorkflows(context.Background())
	if err != nil {
		t.Fatalf("ListScheduledWorkflows() error = %v", err)
	}
	if len(workflows) != 1 || workflows[0].Name != "daily-brief" || workflows[0].Description != "Prepare the morning briefing." {
		t.Fatalf("ListScheduledWorkflows() = %#v, want normalized workflow", workflows)
	}
	if len(workflows[0].Tags) != 2 || workflows[0].Tags[0] != "briefings" || workflows[0].Tags[1] != "personal" {
		t.Fatalf("workflow tags = %#v, want trimmed non-empty tags", workflows[0].Tags)
	}

	workflows[0].Tags[0] = "mutated"
	again, err := registry.ListScheduledWorkflows(context.Background())
	if err != nil {
		t.Fatalf("ListScheduledWorkflows(again) error = %v", err)
	}
	if again[0].Tags[0] != "briefings" {
		t.Fatalf("registry leaked mutable tags: %#v", again[0].Tags)
	}
}

func TestStackFireScheduledWorkflowsFiltersByNameAndDeduplicates(t *testing.T) {
	t.Parallel()

	modelClient := &scheduledRunModel{
		turns: [][]model.StreamEvent{
			{{Kind: model.StreamText, Text: "Inbox triage ready."}},
		},
	}
	stack, err := New(Config{
		Base: memaxagent.Options{
			Model: modelClient,
		},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	store := NewMemoryScheduledRunStore()
	now := time.Date(2026, 4, 19, 9, 1, 0, 0, time.UTC)
	registry, err := NewMemoryScheduledWorkflowRegistry(
		ScheduledWorkflow{
			Name:        "daily-brief",
			Description: "Daily briefing.",
			Tags:        []string{"briefing"},
			Trigger: PeriodicTrigger{
				Name:   "daily-brief",
				Prompt: "Prepare the daily briefing.",
				Every:  24 * time.Hour,
				Anchor: time.Date(2026, 4, 19, 7, 0, 0, 0, time.UTC),
			},
		},
		ScheduledWorkflow{
			Name:        "inbox-triage",
			Description: "Unread inbox triage.",
			Tags:        []string{"inbox"},
			Trigger: PeriodicTrigger{
				Name:   "inbox-triage",
				Prompt: "Triage unread inbox threads.",
				Every:  time.Hour,
				Anchor: time.Date(2026, 4, 19, 9, 0, 0, 0, time.UTC),
			},
		},
	)
	if err != nil {
		t.Fatalf("NewMemoryScheduledWorkflowRegistry() error = %v", err)
	}

	results, err := stack.FireScheduledWorkflows(context.Background(), store, registry, now, "inbox-triage")
	if err != nil {
		t.Fatalf("FireScheduledWorkflows() error = %v", err)
	}
	if len(results) != 1 || results[0].Workflow.Name != "inbox-triage" || !results[0].Fire.Created {
		t.Fatalf("FireScheduledWorkflows() = %#v, want one created selected workflow run", results)
	}
	final := waitForScheduledRun(t, store, results[0].Fire.Record.ID, func(r ScheduledRunRecord) bool { return r.Terminal() })
	if final.Status != ScheduledRunSucceeded || final.Result != "Inbox triage ready." {
		t.Fatalf("final run = %#v, want succeeded selected workflow run", final)
	}

	duplicates, err := stack.FireScheduledWorkflows(context.Background(), store, registry, now, "inbox-triage")
	if err != nil {
		t.Fatalf("FireScheduledWorkflows(duplicate) error = %v", err)
	}
	if len(duplicates) != 1 || duplicates[0].Fire.Created || duplicates[0].Fire.Record.ID != final.ID {
		t.Fatalf("FireScheduledWorkflows(duplicate) = %#v, want existing workflow run", duplicates)
	}
	if len(modelClient.requests) != 1 {
		t.Fatalf("model requests = %d, want one selected workflow run", len(modelClient.requests))
	}
}

func TestStackFireScheduledWorkflowsValidatesSelections(t *testing.T) {
	t.Parallel()

	stack, err := New(Config{
		Base: memaxagent.Options{
			Model: &scheduledRunModel{},
		},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	store := NewMemoryScheduledRunStore()
	registry, err := NewMemoryScheduledWorkflowRegistry(ScheduledWorkflow{
		Name: "daily-brief",
		Trigger: PeriodicTrigger{
			Name:   "daily-brief",
			Prompt: "Prepare the daily briefing.",
			Every:  24 * time.Hour,
			Anchor: time.Date(2026, 4, 19, 7, 0, 0, 0, time.UTC),
		},
	})
	if err != nil {
		t.Fatalf("NewMemoryScheduledWorkflowRegistry() error = %v", err)
	}

	_, err = stack.FireScheduledWorkflows(context.Background(), store, registry, time.Date(2026, 4, 19, 7, 0, 0, 0, time.UTC), "missing-workflow")
	if err == nil {
		t.Fatal("FireScheduledWorkflows(missing) error = nil, want not found error")
	}
	if !errors.Is(err, ErrScheduledWorkflowNotFound) {
		t.Fatalf("FireScheduledWorkflows(missing) error = %v, want ErrScheduledWorkflowNotFound", err)
	}

	empty, err := NewMemoryScheduledWorkflowRegistry()
	if err != nil {
		t.Fatalf("NewMemoryScheduledWorkflowRegistry(empty) error = %v", err)
	}
	_, err = stack.FireScheduledWorkflows(context.Background(), store, empty, time.Date(2026, 4, 19, 7, 0, 0, 0, time.UTC))
	if err == nil {
		t.Fatal("FireScheduledWorkflows(empty) error = nil, want workflow-required error")
	}
}

func TestStackFireScheduledWorkflowsReturnsSuccessfulResultsOnLaterFailure(t *testing.T) {
	t.Parallel()

	modelClient := &scheduledRunModel{
		turns: [][]model.StreamEvent{
			{{Kind: model.StreamText, Text: "Morning briefing ready."}},
		},
	}
	stack, err := New(Config{
		Base: memaxagent.Options{
			Model: modelClient,
		},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	store := NewMemoryScheduledRunStore()
	now := time.Date(2026, 4, 19, 7, 1, 0, 0, time.UTC)
	registry, err := NewMemoryScheduledWorkflowRegistry(
		ScheduledWorkflow{
			Name: "daily-brief",
			Trigger: staticScheduledTrigger{intent: ScheduledIntent{
				ID:           "daily-brief:2026-04-19T07:00:00Z",
				TriggerName:  "daily-brief",
				OccurrenceAt: time.Date(2026, 4, 19, 7, 0, 0, 0, time.UTC),
				Prompt:       "Prepare the morning briefing.",
			}},
		},
		ScheduledWorkflow{
			Name: "invalid-brief",
			Trigger: staticScheduledTrigger{intent: ScheduledIntent{
				ID:           "invalid-brief:2026-04-19T07:00:00Z",
				TriggerName:  "invalid-brief",
				OccurrenceAt: time.Date(2026, 4, 19, 7, 0, 0, 0, time.UTC),
			}},
		},
	)
	if err != nil {
		t.Fatalf("NewMemoryScheduledWorkflowRegistry() error = %v", err)
	}

	results, err := stack.FireScheduledWorkflows(context.Background(), store, registry, now)
	if err == nil {
		t.Fatal("FireScheduledWorkflows() error = nil, want invalid prompt error")
	}
	if len(results) != 1 || results[0].Workflow.Name != "daily-brief" || !results[0].Fire.Created {
		t.Fatalf("FireScheduledWorkflows() results = %#v, want successful first workflow fire preserved", results)
	}
	final := waitForScheduledRun(t, store, results[0].Fire.Record.ID, func(r ScheduledRunRecord) bool { return r.Terminal() })
	if final.Status != ScheduledRunSucceeded {
		t.Fatalf("final run = %#v, want first workflow run to complete despite later failure", final)
	}
}

func TestStackWatchScheduledTriggersFiresOncePerOccurrence(t *testing.T) {
	t.Parallel()

	modelClient := &scheduledRunModel{
		turns: [][]model.StreamEvent{
			{{Kind: model.StreamText, Text: "Morning briefing ready."}},
		},
	}
	stack, err := New(Config{
		Base: memaxagent.Options{
			Model: modelClient,
		},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	store := NewMemoryScheduledRunStore()
	now := time.Date(2026, 4, 19, 7, 1, 0, 0, time.UTC)
	trigger := PeriodicTrigger{
		Name:   "daily-brief",
		Prompt: "Prepare the morning briefing.",
		Every:  24 * time.Hour,
		Anchor: time.Date(2026, 4, 19, 7, 0, 0, 0, time.UTC),
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 1)
	go func() {
		errCh <- stack.WatchScheduledTriggers(ctx, store, TriggerWatcherOptions{
			Interval: time.Millisecond,
			Now: func() time.Time {
				return now
			},
		}, trigger)
	}()

	final := waitForScheduledRun(t, store, "daily-brief:2026-04-19T07:00:00Z", func(r ScheduledRunRecord) bool { return r.Terminal() })
	cancel()
	if err := <-errCh; !errors.Is(err, context.Canceled) {
		t.Fatalf("WatchScheduledTriggers() error = %v, want context.Canceled", err)
	}
	if final.Status != ScheduledRunSucceeded {
		t.Fatalf("final run = %#v, want succeeded scheduled run", final)
	}
	if len(modelClient.requests) != 1 {
		t.Fatalf("model requests = %d, want one run for the same occurrence", len(modelClient.requests))
	}
}

type scheduledRunModel struct {
	turns    [][]model.StreamEvent
	requests []model.Request
}

type staticScheduledTrigger struct {
	intent ScheduledIntent
}

type failingScheduledRunModel struct {
	err error
}

type failingScheduledRunUpdateStore struct {
	ScheduledRunStore
	failStatus ScheduledRunStatus
	err        error
	called     chan struct{}
	once       sync.Once
}

func (t staticScheduledTrigger) IntentAt(time.Time) (ScheduledIntent, bool) {
	return t.intent, true
}

func (s *failingScheduledRunUpdateStore) UpdateScheduledRun(ctx context.Context, update ScheduledRunUpdate) (ScheduledRunRecord, error) {
	if update.Status == s.failStatus {
		s.once.Do(func() {
			if s.called != nil {
				close(s.called)
			}
		})
		return ScheduledRunRecord{}, s.err
	}
	return s.ScheduledRunStore.UpdateScheduledRun(ctx, update)
}

func (m failingScheduledRunModel) Stream(context.Context, model.Request) (model.Stream, error) {
	return nil, m.err
}

func (m *scheduledRunModel) Stream(_ context.Context, req model.Request) (model.Stream, error) {
	m.requests = append(m.requests, req)
	if len(m.turns) == 0 {
		return &scheduledRunStream{}, nil
	}
	events := m.turns[0]
	m.turns = m.turns[1:]
	return &scheduledRunStream{events: events}, nil
}

type blockingScheduledRunModel struct {
	blocked     chan struct{}
	release     chan struct{}
	done        chan struct{}
	blockOnce   sync.Once
	releaseOnce sync.Once
	doneOnce    sync.Once
}

func newBlockingScheduledRunModel() *blockingScheduledRunModel {
	return &blockingScheduledRunModel{
		blocked: make(chan struct{}),
		release: make(chan struct{}),
		done:    make(chan struct{}),
	}
}

func (m *blockingScheduledRunModel) Stream(context.Context, model.Request) (model.Stream, error) {
	return &blockingScheduledRunStream{model: m}, nil
}

func (m *blockingScheduledRunModel) releaseStream() {
	m.releaseOnce.Do(func() {
		close(m.release)
	})
}

func (m *blockingScheduledRunModel) markBlocked() {
	m.blockOnce.Do(func() {
		close(m.blocked)
	})
}

func (m *blockingScheduledRunModel) markDone() {
	m.doneOnce.Do(func() {
		close(m.done)
	})
}

type blockingScheduledRunStream struct {
	model    *blockingScheduledRunModel
	released bool
	sent     bool
}

func (s *blockingScheduledRunStream) Recv() (model.StreamEvent, error) {
	if !s.released {
		s.model.markBlocked()
		<-s.model.release
		s.released = true
	}
	if s.sent {
		s.model.markDone()
		return model.StreamEvent{}, model.ErrEndOfStream
	}
	s.sent = true
	return model.StreamEvent{Kind: model.StreamText, Text: "Morning briefing ready."}, nil
}

func (s *blockingScheduledRunStream) Close() error {
	s.model.markDone()
	return nil
}

type scheduledRunStream struct {
	events []model.StreamEvent
	index  int
}

func (s *scheduledRunStream) Recv() (model.StreamEvent, error) {
	if s.index >= len(s.events) {
		return model.StreamEvent{}, model.ErrEndOfStream
	}
	event := s.events[s.index]
	s.index++
	return event, nil
}

func (s *scheduledRunStream) Close() error { return nil }

func waitForScheduledRun(t *testing.T, store ScheduledRunStore, id string, done func(ScheduledRunRecord) bool) ScheduledRunRecord {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		record, err := store.GetScheduledRun(context.Background(), id)
		if err == nil && done(record) {
			return record
		}
		time.Sleep(10 * time.Millisecond)
	}
	record, err := store.GetScheduledRun(context.Background(), id)
	if err != nil {
		t.Fatalf("GetScheduledRun(%q) error = %v", id, err)
	}
	t.Fatalf("scheduled run %q did not reach expected state: %#v", id, record)
	return ScheduledRunRecord{}
}

func waitForObservedRunEvents(t *testing.T, snapshot func() []memaxagent.RunEvent, count int) []memaxagent.RunEvent {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		events := snapshot()
		if len(events) >= count {
			return events
		}
		time.Sleep(10 * time.Millisecond)
	}
	events := snapshot()
	t.Fatalf("observed run events = %#v, want at least %d", events, count)
	return nil
}
