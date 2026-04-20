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
