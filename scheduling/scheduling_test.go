package scheduling

import (
	"context"
	"testing"
	"time"
)

func TestEventStoreSearchReadCreateRescheduleCancel(t *testing.T) {
	t.Parallel()

	start := time.Date(2026, 4, 20, 15, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)
	store := NewEventStore([]Event{{
		ID:       "event-1",
		Title:    "Project kickoff",
		Summary:  "Weekly kickoff with owners and due dates",
		Location: "Zoom",
		Organizer: Participant{
			Name:    "Alex",
			Address: "alex@example.com",
		},
		Attendees: []Participant{
			{Name: "Jordan", Address: "jordan@example.com"},
		},
		Start:    start,
		End:      end,
		TimeZone: "UTC",
	}})

	items, err := store.SearchEvents(context.Background(), SearchRequest{
		Query:       "kickoff owners due dates",
		WindowStart: start.Add(-time.Hour),
		WindowEnd:   end.Add(24 * time.Hour),
		Limit:       5,
	})
	if err != nil {
		t.Fatalf("SearchEvents() error = %v", err)
	}
	if len(items) != 1 || items[0].ID != "event-1" {
		t.Fatalf("SearchEvents() = %#v, want kickoff event", items)
	}

	item, err := store.ReadEvent(context.Background(), ReadRequest{ID: "event-1"})
	if err != nil {
		t.Fatalf("ReadEvent() error = %v", err)
	}
	if item.Title != "Project kickoff" {
		t.Fatalf("ReadEvent() = %#v, want title", item)
	}

	createResult, err := store.CreateEvent(context.Background(), CreateRequest{
		Event: Event{
			Title:       "Design review",
			Summary:     "Review updated design with PM",
			Description: "Discuss detailed staffing risk matrix and approval path.",
			Location:    "Room 2",
			Organizer: Participant{
				Name:    "Taylor",
				Address: "taylor@example.com",
			},
			Start:    start.Add(24 * time.Hour),
			End:      end.Add(24 * time.Hour),
			TimeZone: "UTC",
			Attendees: []Participant{
				{Name: "Morgan", Address: "morgan@example.com"},
			},
		},
	})
	if err != nil {
		t.Fatalf("CreateEvent() error = %v", err)
	}
	if createResult.Event.ID == "" {
		t.Fatalf("CreateEvent() = %#v, want assigned ID", createResult)
	}

	rescheduleResult, err := store.RescheduleEvent(context.Background(), RescheduleRequest{
		ID:       createResult.Event.ID,
		Start:    createResult.Event.Start.Add(time.Hour),
		End:      createResult.Event.End.Add(time.Hour),
		TimeZone: "America/Los_Angeles",
	})
	if err != nil {
		t.Fatalf("RescheduleEvent() error = %v", err)
	}
	if rescheduleResult.Event.TimeZone != "America/Los_Angeles" {
		t.Fatalf("RescheduleEvent() = %#v, want updated timezone", rescheduleResult)
	}

	cancelResult, err := store.CancelEvent(context.Background(), CancelRequest{
		ID:     createResult.Event.ID,
		Reason: "conflict with customer call",
	})
	if err != nil {
		t.Fatalf("CancelEvent() error = %v", err)
	}
	if cancelResult.Event.Status != StatusCancelled {
		t.Fatalf("CancelEvent() = %#v, want cancelled status", cancelResult)
	}
}

func TestSelectorBreaksTiesByStartTime(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 4, 20, 12, 0, 0, 0, time.UTC)
	items := []Event{
		{ID: "event-1", Title: "Status review", Summary: "Shared summary", Start: now.Add(2 * time.Hour), End: now.Add(3 * time.Hour), TimeZone: "UTC", Organizer: Participant{Name: "Alex"}},
		{ID: "event-2", Title: "Status review", Summary: "Shared summary", Start: now.Add(time.Hour), End: now.Add(2 * time.Hour), TimeZone: "UTC", Organizer: Participant{Name: "Alex"}},
	}

	selected := (Selector{MaxEvents: 2}).Select(items, "status review", time.Time{}, time.Time{})
	if len(selected) != 2 || selected[0].ID != "event-2" {
		t.Fatalf("Select() = %#v, want earliest matching event first", selected)
	}
}

func TestCreateEventRequiresAttendeeOrOrganizer(t *testing.T) {
	t.Parallel()

	start := time.Date(2026, 4, 20, 15, 0, 0, 0, time.UTC)
	store := NewEventStore(nil)
	_, err := store.CreateEvent(context.Background(), CreateRequest{
		Event: Event{
			Title:    "Design review",
			Start:    start,
			End:      start.Add(time.Hour),
			TimeZone: "UTC",
		},
	})
	if err == nil || err.Error() != "scheduling: organizer or attendee is required" {
		t.Fatalf("CreateEvent() error = %v, want participant validation", err)
	}
}

func TestSelectorWindowBoundaries(t *testing.T) {
	t.Parallel()

	windowStart := time.Date(2026, 4, 20, 10, 0, 0, 0, time.UTC)
	windowEnd := windowStart.Add(time.Hour)
	makeEvent := func(id string, start, end time.Time) Event {
		return Event{
			ID:       id,
			Title:    "Window test " + id,
			Summary:  "Window boundary coverage",
			Start:    start,
			End:      end,
			TimeZone: "UTC",
			Organizer: Participant{
				Name: "Alex",
			},
		}
	}

	items := []Event{
		makeEvent("before", windowStart.Add(-2*time.Hour), windowStart.Add(-time.Hour)),
		makeEvent("end-at-start", windowStart.Add(-time.Hour), windowStart),
		makeEvent("spanning", windowStart.Add(-30*time.Minute), windowStart.Add(30*time.Minute)),
		makeEvent("start-at-end", windowEnd, windowEnd.Add(30*time.Minute)),
		makeEvent("after", windowEnd.Add(30*time.Minute), windowEnd.Add(time.Hour)),
	}

	selected := (Selector{}).Select(items, "", windowStart, windowEnd)
	if len(selected) != 1 || selected[0].ID != "spanning" {
		t.Fatalf("Select() = %#v, want only spanning event in [%s, %s)", selected, windowStart, windowEnd)
	}
}
