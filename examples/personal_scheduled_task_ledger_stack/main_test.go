package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestRunExampleShowsScheduledTaskLedgerMaintenance(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	if err := runExample(context.Background(), &out); err != nil {
		t.Fatalf("runExample() error = %v", err)
	}
	got := out.String()
	for _, want := range []string{
		"tool use: list_tasks",
		"tool result: - [pending] week-2026-04-20-acme-owner",
		"tool use: upsert_task",
		"tool result: upserted week-2026-04-20-acme-owner",
		"tool result: upserted week-2026-04-20-demo-slides",
		"result: Scheduled task-ledger maintenance complete: Acme mitigation owner is confirmed; partner council demo slides remain blocked.",
		"task: week-2026-04-20-acme-owner completed Confirm Acme mitigation owner",
		"task: week-2026-04-20-demo-slides blocked Deliver partner council demo slides",
		"scheduled run: task-ledger-maintenance:2026-04-20T08:00:00Z succeeded",
		"duplicate fire reused run: task-ledger-maintenance:2026-04-20T08:00:00Z created=false",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("example output missing %q:\n%s", want, got)
		}
	}
	list := strings.Index(got, "tool use: list_tasks")
	upsert := strings.Index(got, "tool use: upsert_task")
	if list < 0 || upsert < 0 || list > upsert {
		t.Fatalf("example output should list persisted tasks before mutating them:\n%s", got)
	}
	if count := strings.Count(got, "tool use: upsert_task"); count != 2 {
		t.Fatalf("upsert_task count = %d, want 2:\n%s", count, got)
	}
}
