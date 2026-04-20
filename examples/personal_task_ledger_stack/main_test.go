package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestRunExampleShowsDurableTaskLedgerResume(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	if err := runExample(context.Background(), &out); err != nil {
		t.Fatalf("runExample() error = %v", err)
	}
	got := out.String()
	for _, want := range []string{
		"== first run ==",
		"tool use: search_memories",
		"tool use: search_message_threads",
		"tool use: read_message_thread",
		"tool use: search_schedule_events",
		"tool use: read_schedule_event",
		"tool use: upsert_task",
		"result: Week-ahead task ledger updated: created follow-up tasks for Acme mitigation owner and partner council demo slides.",
		"reopened sqlite task ledger",
		"== second run ==",
		"tool use: list_tasks",
		"result: Resumed week-ahead task ledger: Acme owner follow-up is complete; partner council demo slides remain pending.",
		"task: task-1 in_progress Assemble the week-ahead follow-up ledger",
		"task: week-2026-04-20-acme-owner completed Confirm Acme mitigation owner",
		"task: week-2026-04-20-demo-slides pending Deliver partner council demo slides",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("example output missing %q:\n%s", want, got)
		}
	}
}
