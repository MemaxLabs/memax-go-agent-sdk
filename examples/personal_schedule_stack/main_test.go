package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestRunExampleShowsScheduleRecallAndApprovalFlow(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	if err := runExample(context.Background(), &out); err != nil {
		t.Fatalf("runExample() error = %v", err)
	}
	got := out.String()
	for _, want := range []string{
		"tool use: search_schedule_events",
		"tool use: read_schedule_event",
		"tool use: request_approval",
		"approval requested: reschedule_schedule_event",
		"approval granted: reschedule_schedule_event",
		"tool use: reschedule_schedule_event",
		"approval consumed: reschedule_schedule_event",
		"tool use: search_schedule_events",
		"Project kickoff",
		"result: Recalled the existing event constraints, rescheduled the kickoff, and confirmed the updated event metadata.",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("example output missing %q:\n%s", want, got)
		}
	}
}
