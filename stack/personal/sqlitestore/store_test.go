package sqlitestore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/MemaxLabs/memax-go-agent-sdk/stack/personal"
	_ "modernc.org/sqlite"
)

func TestNewNilDB(t *testing.T) {
	t.Parallel()

	if _, err := New(context.Background(), nil); err == nil {
		t.Fatal("New(nil) error = nil, want error")
	}
}

func TestStoreCreateUpdateAndGetScheduledRun(t *testing.T) {
	t.Parallel()

	store := newTestStore(t)
	occurrence := time.Date(2026, 4, 19, 7, 0, 0, 0, time.UTC)
	record, created, err := store.CreateScheduledRun(context.Background(), personal.CreateScheduledRunRequest{
		ID:           "daily-brief:2026-04-19T07:00:00Z",
		TriggerName:  "daily-brief",
		OccurrenceAt: occurrence,
		Prompt:       "Prepare the morning briefing.",
	})
	if err != nil {
		t.Fatalf("CreateScheduledRun() error = %v", err)
	}
	if !created || record.Status != personal.ScheduledRunQueued {
		t.Fatalf("CreateScheduledRun() = (%#v, %t), want queued created record", record, created)
	}

	duplicate, created, err := store.CreateScheduledRun(context.Background(), personal.CreateScheduledRunRequest{
		ID:           record.ID,
		TriggerName:  record.TriggerName,
		OccurrenceAt: occurrence,
		Prompt:       record.Prompt,
	})
	if err != nil {
		t.Fatalf("CreateScheduledRun(duplicate) error = %v", err)
	}
	if created || duplicate.ID != record.ID {
		t.Fatalf("CreateScheduledRun(duplicate) = (%#v, %t), want existing record and created=false", duplicate, created)
	}

	record, err = store.UpdateScheduledRun(context.Background(), personal.ScheduledRunUpdate{
		ID:        record.ID,
		Status:    personal.ScheduledRunRunning,
		SessionID: "session-1",
	})
	if err != nil {
		t.Fatalf("UpdateScheduledRun(running) error = %v", err)
	}
	if record.Status != personal.ScheduledRunRunning || record.SessionID != "session-1" || record.StartedAt.IsZero() {
		t.Fatalf("UpdateScheduledRun(running) = %#v, want running record with session and started time", record)
	}

	result := "Morning briefing ready."
	completedAt := time.Now().UTC().Truncate(time.Millisecond)
	record, err = store.UpdateScheduledRun(context.Background(), personal.ScheduledRunUpdate{
		ID:          record.ID,
		Status:      personal.ScheduledRunSucceeded,
		Result:      &result,
		CompletedAt: &completedAt,
	})
	if err != nil {
		t.Fatalf("UpdateScheduledRun(succeeded) error = %v", err)
	}
	if record.Status != personal.ScheduledRunSucceeded || record.Result != result || record.CompletedAt.IsZero() {
		t.Fatalf("UpdateScheduledRun(succeeded) = %#v, want succeeded record with result", record)
	}

	errText := "late failure"
	unchanged, err := store.UpdateScheduledRun(context.Background(), personal.ScheduledRunUpdate{
		ID:     record.ID,
		Status: personal.ScheduledRunFailed,
		Error:  &errText,
	})
	if err != nil {
		t.Fatalf("UpdateScheduledRun(terminal) error = %v", err)
	}
	if unchanged.Status != personal.ScheduledRunSucceeded || unchanged.Result != result || unchanged.Error != "" {
		t.Fatalf("UpdateScheduledRun(terminal) = %#v, want terminal record unchanged", unchanged)
	}

	got, err := store.GetScheduledRun(context.Background(), record.ID)
	if err != nil {
		t.Fatalf("GetScheduledRun() error = %v", err)
	}
	if got.TriggerName != "daily-brief" || !got.OccurrenceAt.Equal(occurrence) {
		t.Fatalf("GetScheduledRun() = %#v, want persisted trigger and occurrence", got)
	}
}

func TestStoreGetScheduledRunReturnsNotFound(t *testing.T) {
	t.Parallel()

	store := newTestStore(t)
	_, err := store.GetScheduledRun(context.Background(), "missing")
	if !errors.Is(err, personal.ErrScheduledRunNotFound) {
		t.Fatalf("GetScheduledRun(missing) error = %v, want ErrScheduledRunNotFound", err)
	}
}

func TestStoreCreateScheduledRunIsAtomic(t *testing.T) {
	t.Parallel()

	store := newTestStore(t)
	req := personal.CreateScheduledRunRequest{
		ID:           "daily-brief:2026-04-19T07:00:00Z",
		TriggerName:  "daily-brief",
		OccurrenceAt: time.Date(2026, 4, 19, 7, 0, 0, 0, time.UTC),
		Prompt:       "Prepare the morning briefing.",
	}

	const goroutines = 16
	var (
		wg           sync.WaitGroup
		createdCount int
		mu           sync.Mutex
	)
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, created, err := store.CreateScheduledRun(context.Background(), req)
			if err != nil {
				t.Errorf("CreateScheduledRun() error = %v", err)
				return
			}
			if created {
				mu.Lock()
				createdCount++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	if createdCount != 1 {
		t.Fatalf("created count = %d, want 1", createdCount)
	}
	got, err := store.GetScheduledRun(context.Background(), req.ID)
	if err != nil {
		t.Fatalf("GetScheduledRun() error = %v", err)
	}
	if got.Status != personal.ScheduledRunQueued {
		t.Fatalf("GetScheduledRun() = %#v, want queued durable record", got)
	}
}

func TestStoreFailStaleScheduledRuns(t *testing.T) {
	t.Parallel()

	store := newTestStore(t)
	occurrence := time.Date(2026, 4, 19, 7, 0, 0, 0, time.UTC)
	for _, req := range []personal.CreateScheduledRunRequest{
		{
			ID:           "stale-queued",
			TriggerName:  "daily-brief",
			OccurrenceAt: occurrence,
			Prompt:       "Prepare the morning briefing.",
		},
		{
			ID:           "stale-running",
			TriggerName:  "daily-brief",
			OccurrenceAt: occurrence,
			Prompt:       "Prepare the morning briefing.",
		},
		{
			ID:           "fresh-queued",
			TriggerName:  "daily-brief",
			OccurrenceAt: occurrence,
			Prompt:       "Prepare the morning briefing.",
		},
		{
			ID:           "terminal",
			TriggerName:  "daily-brief",
			OccurrenceAt: occurrence,
			Prompt:       "Prepare the morning briefing.",
		},
	} {
		if _, _, err := store.CreateScheduledRun(context.Background(), req); err != nil {
			t.Fatalf("CreateScheduledRun(%q) error = %v", req.ID, err)
		}
	}
	if _, err := store.UpdateScheduledRun(context.Background(), personal.ScheduledRunUpdate{
		ID:     "stale-running",
		Status: personal.ScheduledRunRunning,
	}); err != nil {
		t.Fatalf("UpdateScheduledRun(stale-running) error = %v", err)
	}
	completedAt := time.Now().UTC()
	result := "done"
	if _, err := store.UpdateScheduledRun(context.Background(), personal.ScheduledRunUpdate{
		ID:          "terminal",
		Status:      personal.ScheduledRunSucceeded,
		Result:      &result,
		CompletedAt: &completedAt,
	}); err != nil {
		t.Fatalf("UpdateScheduledRun(terminal) error = %v", err)
	}

	staleUpdatedAt := time.Now().UTC().Add(-2 * time.Hour)
	if _, err := store.db.ExecContext(context.Background(), `
		UPDATE memax_personal_scheduled_runs
		SET updated_at_unix_ms = ?
		WHERE id IN (?, ?, ?)
	`, staleUpdatedAt.UnixMilli(), "stale-queued", "stale-running", "terminal"); err != nil {
		t.Fatalf("age stale runs: %v", err)
	}

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
		if record.Status != personal.ScheduledRunFailed || record.Error != "reconciled stale run" || record.CompletedAt.IsZero() {
			t.Fatalf("record %q = %#v, want failed stale run", id, record)
		}
	}
	for _, id := range []string{"fresh-queued", "terminal"} {
		record, err := store.GetScheduledRun(context.Background(), id)
		if err != nil {
			t.Fatalf("GetScheduledRun(%q) error = %v", id, err)
		}
		if record.Status == personal.ScheduledRunFailed {
			t.Fatalf("record %q = %#v, want not failed", id, record)
		}
	}
}

func newTestStore(t *testing.T) *Store {
	t.Helper()

	db, err := sql.Open("sqlite", "file:"+t.Name()+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	t.Cleanup(func() {
		_ = db.Close()
	})
	store, err := New(context.Background(), db)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return store
}

func ExampleNew() {
	db, err := sql.Open("sqlite", "file:personal-scheduled-runs?mode=memory&cache=shared")
	if err != nil {
		panic(err)
	}
	defer db.Close()

	store, err := New(context.Background(), db)
	if err != nil {
		panic(err)
	}
	record, created, err := store.CreateScheduledRun(context.Background(), personal.CreateScheduledRunRequest{
		ID:           "daily-brief:2026-04-19T07:00:00Z",
		TriggerName:  "daily-brief",
		OccurrenceAt: time.Date(2026, 4, 19, 7, 0, 0, 0, time.UTC),
		Prompt:       "Prepare the morning briefing.",
	})
	if err != nil {
		panic(err)
	}
	fmt.Println(created, record.Status)
	// Output: true queued
}
