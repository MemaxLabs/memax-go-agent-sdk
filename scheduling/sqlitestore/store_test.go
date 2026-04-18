package sqlitestore

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/MemaxLabs/memax-go-agent-sdk/scheduling"
	_ "modernc.org/sqlite"
)

func TestStoreRoundTripSearchReadCreateRescheduleCancel(t *testing.T) {
	t.Parallel()

	store := newTestStore(t)
	ctx := context.Background()
	start := time.Date(2026, 4, 20, 15, 0, 0, 0, time.UTC)

	createResult, err := store.CreateEvent(ctx, scheduling.CreateRequest{
		Event: scheduling.Event{
			Title:       "Project kickoff",
			Summary:     "Weekly kickoff with owners and due dates",
			Description: "Keep this kickoff to 45 minutes and do not move it after 4 PM Pacific.",
			Location:    "Zoom",
			Organizer: scheduling.Participant{
				Name:    "Alex",
				Address: "alex@example.com",
			},
			Start:    start,
			End:      start.Add(time.Hour),
			TimeZone: "UTC",
			Tags:     []string{"project", "kickoff"},
		},
	})
	if err != nil {
		t.Fatalf("CreateEvent() error = %v", err)
	}
	if createResult.Event.ID == "" {
		t.Fatalf("CreateEvent() = %#v, want assigned ID", createResult)
	}

	items, err := store.SearchEvents(ctx, scheduling.SearchRequest{
		Query:       "kickoff owners due dates",
		WindowStart: start.Add(-time.Hour),
		WindowEnd:   start.Add(24 * time.Hour),
		Limit:       5,
	})
	if err != nil {
		t.Fatalf("SearchEvents() error = %v", err)
	}
	if len(items) != 1 || items[0].ID != createResult.Event.ID {
		t.Fatalf("SearchEvents() = %#v, want kickoff event", items)
	}
	if items[0].Description != "" {
		t.Fatalf("SearchEvents() = %#v, want metadata-only event without description", items[0])
	}

	readResult, err := store.ReadEvent(ctx, scheduling.ReadRequest{ID: createResult.Event.ID})
	if err != nil {
		t.Fatalf("ReadEvent() error = %v", err)
	}
	if readResult.Description == "" {
		t.Fatalf("ReadEvent() = %#v, want full description", readResult)
	}

	rescheduleResult, err := store.RescheduleEvent(ctx, scheduling.RescheduleRequest{
		ID:       createResult.Event.ID,
		Start:    start.Add(2 * time.Hour),
		End:      start.Add(2*time.Hour + 45*time.Minute),
		TimeZone: "America/Los_Angeles",
		Metadata: map[string]any{"source": "unit-test"},
	})
	if err != nil {
		t.Fatalf("RescheduleEvent() error = %v", err)
	}
	if rescheduleResult.Previous.TimeZone != "UTC" || rescheduleResult.Event.TimeZone != "America/Los_Angeles" {
		t.Fatalf("RescheduleEvent() = %#v, want previous and updated timezone", rescheduleResult)
	}

	cancelResult, err := store.CancelEvent(ctx, scheduling.CancelRequest{
		ID:     createResult.Event.ID,
		Reason: "conflict with customer call",
	})
	if err != nil {
		t.Fatalf("CancelEvent() error = %v", err)
	}
	if cancelResult.Event.Status != scheduling.StatusCancelled {
		t.Fatalf("CancelEvent() = %#v, want cancelled status", cancelResult)
	}
	if got := cancelResult.Event.Metadata["cancel_reason"]; got != "conflict with customer call" {
		t.Fatalf("CancelEvent() metadata = %#v, want cancel reason", cancelResult.Event.Metadata)
	}
}

func TestStoreSearchWindowBoundaries(t *testing.T) {
	t.Parallel()

	store := newTestStore(t)
	ctx := context.Background()
	windowStart := time.Date(2026, 4, 20, 10, 0, 0, 0, time.UTC)
	windowEnd := windowStart.Add(time.Hour)
	makeEvent := func(id string, start, end time.Time) {
		_, err := store.CreateEvent(ctx, scheduling.CreateRequest{
			Event: scheduling.Event{
				ID:      id,
				Title:   "Window " + id,
				Summary: "Boundary coverage",
				Organizer: scheduling.Participant{
					Name: "Alex",
				},
				Start:    start,
				End:      end,
				TimeZone: "UTC",
			},
		})
		if err != nil {
			t.Fatalf("CreateEvent(%s) error = %v", id, err)
		}
	}

	makeEvent("before", windowStart.Add(-2*time.Hour), windowStart.Add(-time.Hour))
	makeEvent("end-at-start", windowStart.Add(-time.Hour), windowStart)
	makeEvent("spanning", windowStart.Add(-30*time.Minute), windowStart.Add(30*time.Minute))
	makeEvent("start-at-end", windowEnd, windowEnd.Add(30*time.Minute))
	makeEvent("after", windowEnd.Add(30*time.Minute), windowEnd.Add(time.Hour))

	items, err := store.SearchEvents(ctx, scheduling.SearchRequest{
		Query:       "",
		WindowStart: windowStart,
		WindowEnd:   windowEnd,
		Limit:       10,
	})
	if err != nil {
		t.Fatalf("SearchEvents() error = %v", err)
	}
	if len(items) != 1 || items[0].ID != "spanning" {
		t.Fatalf("SearchEvents() = %#v, want only spanning event in [%s, %s)", items, windowStart, windowEnd)
	}
}

func TestStoreRejectsInvalidCreate(t *testing.T) {
	t.Parallel()

	store := newTestStore(t)
	_, err := store.CreateEvent(context.Background(), scheduling.CreateRequest{
		Event: scheduling.Event{
			Title: "Invalid kickoff",
			Start: time.Date(2026, 4, 20, 15, 0, 0, 0, time.UTC),
			End:   time.Date(2026, 4, 20, 16, 0, 0, 0, time.UTC),
		},
	})
	if err == nil || err.Error() != "scheduling: organizer or attendee is required" {
		t.Fatalf("CreateEvent() error = %v, want participant validation", err)
	}
}

func TestStoreRescheduleSerializesPreviousState(t *testing.T) {
	t.Parallel()

	store := newTestStore(t)
	ctx := context.Background()
	start := time.Date(2026, 4, 20, 15, 0, 0, 0, time.UTC)

	createResult, err := store.CreateEvent(ctx, scheduling.CreateRequest{
		Event: scheduling.Event{
			ID:      "event-1",
			Title:   "Project kickoff",
			Summary: "Weekly kickoff with owners and due dates",
			Organizer: scheduling.Participant{
				Name: "Alex",
			},
			Start:    start,
			End:      start.Add(time.Hour),
			TimeZone: "UTC",
		},
	})
	if err != nil {
		t.Fatalf("CreateEvent() error = %v", err)
	}

	conn, err := store.db.Conn(ctx)
	if err != nil {
		t.Fatalf("Conn() error = %v", err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		t.Fatalf("BEGIN IMMEDIATE error = %v", err)
	}
	defer func() {
		_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
	}()

	updatedStart := start.Add(time.Hour)
	updatedEnd := updatedStart.Add(time.Hour)
	_, err = conn.ExecContext(ctx, `
		UPDATE memax_schedule_events
		SET start_at = ?, end_at = ?, time_zone = ?
		WHERE id = ?
	`, formatTime(updatedStart), formatTime(updatedEnd), "America/New_York", createResult.Event.ID)
	if err != nil {
		t.Fatalf("intermediate UPDATE error = %v", err)
	}

	resultCh := make(chan scheduling.RescheduleResult, 1)
	errCh := make(chan error, 1)
	go func() {
		result, err := store.RescheduleEvent(context.Background(), scheduling.RescheduleRequest{
			ID:       createResult.Event.ID,
			Start:    start.Add(2 * time.Hour),
			End:      start.Add(2*time.Hour + 45*time.Minute),
			TimeZone: "America/Los_Angeles",
		})
		if err != nil {
			errCh <- err
			return
		}
		resultCh <- result
	}()

	select {
	case err := <-errCh:
		t.Fatalf("concurrent RescheduleEvent() early error = %v", err)
	case <-resultCh:
		t.Fatal("concurrent RescheduleEvent() completed before lock release")
	case <-time.After(50 * time.Millisecond):
	}

	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		t.Fatalf("COMMIT error = %v", err)
	}

	select {
	case err := <-errCh:
		t.Fatalf("RescheduleEvent() error = %v", err)
	case result := <-resultCh:
		if result.Previous.TimeZone != "America/New_York" {
			t.Fatalf("Previous = %#v, want committed intermediate state", result.Previous)
		}
		if !result.Previous.Start.Equal(updatedStart) || !result.Previous.End.Equal(updatedEnd) {
			t.Fatalf("Previous timing = %#v, want committed intermediate timing", result.Previous)
		}
		if result.Event.TimeZone != "America/Los_Angeles" {
			t.Fatalf("Event = %#v, want final rescheduled state", result.Event)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RescheduleEvent() did not complete after lock release")
	}
}

func newTestStore(t *testing.T) *Store {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+t.Name()+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
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
