package personal

import (
	"context"
	"errors"
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

func (t staticScheduledTrigger) IntentAt(time.Time) (ScheduledIntent, bool) {
	return t.intent, true
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
