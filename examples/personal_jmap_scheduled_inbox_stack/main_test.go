package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestRunExampleShowsScheduledJMAPInboxTriageWorkflow(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	if err := runExample(context.Background(), &out); err != nil {
		t.Fatalf("runExample() error = %v", err)
	}
	got := out.String()
	for _, want := range []string{
		"tool use: search_message_threads",
		"tool use: read_message_thread",
		"tool use: request_approval",
		"approval requested: send_message",
		"approval granted: send_message",
		"approval consumed: send_message",
		"tool use: send_message",
		"Urgent: Acme renewal blocker",
		"result: Scheduled inbox triage sent the urgent Acme reply through the attached JMAP inbox backend and recorded the occurrence so the same hourly trigger does not run twice.",
		"scheduled run: inbox-triage:2026-04-19T09:00:00Z succeeded",
		"duplicate fire reused run: inbox-triage:2026-04-19T09:00:00Z created=false",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("example output missing %q:\n%s", want, got)
		}
	}
}
