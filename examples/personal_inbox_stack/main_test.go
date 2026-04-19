package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestRunExampleShowsInboxTriageApprovalAndFollowupFlow(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	if err := runExample(context.Background(), &out); err != nil {
		t.Fatalf("runExample() error = %v", err)
	}
	got := out.String()
	for _, want := range []string{
		"tool use: search_message_threads",
		"tool use: read_message_thread",
		"tool use: send_message",
		"approval requested: send_message",
		"approval granted: send_message",
		"approval consumed: send_message",
		"tool use: create_schedule_event",
		"approval requested: create_schedule_event",
		"approval granted: create_schedule_event",
		"approval consumed: create_schedule_event",
		"Urgent: Acme renewal blocker",
		"created schedule event Acme blocker follow-up",
		"result: Triaged the urgent Acme inbox thread from metadata, recovered through approval to send the reply, and created a same-day follow-up reminder.",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("example output missing %q:\n%s", want, got)
		}
	}
}
