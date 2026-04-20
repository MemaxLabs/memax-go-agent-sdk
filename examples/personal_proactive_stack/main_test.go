package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestRunExampleShowsProactiveBriefingWorkflow(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	if err := runExample(context.Background(), &out); err != nil {
		t.Fatalf("runExample() error = %v", err)
	}
	got := out.String()
	for _, want := range []string{
		"tool use: search_notes",
		"tool use: search_message_threads",
		"tool use: search_schedule_events",
		"tool use: read_note",
		"tool use: read_message_thread",
		"tool use: read_schedule_event",
		"Morning briefing template",
		"Travel update for today",
		"Design review",
		"result: Morning briefing: urgent change first, your design review is at 09:00 UTC in Room 5A, and Jordan says the flight moved to 3:30 PM so bring your passport.",
		"scheduled run: daily-brief:2026-04-19T07:00:00Z succeeded",
		"scheduled workflow: daily-brief",
		"duplicate fire reused run: daily-brief:2026-04-19T07:00:00Z created=false",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("example output missing %q:\n%s", want, got)
		}
	}
}
