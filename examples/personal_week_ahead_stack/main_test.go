package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestRunExampleShowsWeekAheadPlanningWorkflow(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	if err := runExample(context.Background(), &out); err != nil {
		t.Fatalf("runExample() error = %v", err)
	}
	got := out.String()
	for _, want := range []string{
		"tool use: search_memories",
		"tool use: search_notes",
		"tool use: search_message_threads",
		"tool use: search_schedule_events",
		"tool use: read_note",
		"tool use: read_message_thread",
		"tool use: read_schedule_event",
		"Q2 launch planning brief",
		"Acme renewal blocker",
		"Partner council demo slides",
		"Acme renewal meeting",
		"Internal launch risk review",
		"Partner council demo",
		"result: Week-ahead plan: Conflict first: Monday 13:30-14:30 UTC Acme renewal meeting overlaps the 14:00-14:30 UTC internal launch risk review",
		"Commitments: send Casey the 14:00 UTC blocker checkpoint",
		"Wednesday 17:00 UTC",
		"Thursday 16:00 UTC partner council",
		"Follow-ups: confirm the mitigation owner with Casey",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("example output missing %q:\n%s", want, got)
		}
	}
}
