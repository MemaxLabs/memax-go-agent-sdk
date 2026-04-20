package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestRunExampleShowsWebhookNotificationDelivery(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	if err := runExample(context.Background(), &out); err != nil {
		t.Fatalf("runExample() error = %v", err)
	}
	got := out.String()
	for _, want := range []string{
		"scheduled lifecycle: webhook-check:2026-04-20T09:00:00Z queued",
		"scheduled lifecycle: webhook-check:2026-04-20T09:00:00Z running",
		"scheduled lifecycle: webhook-check:2026-04-20T09:00:00Z succeeded",
		"scheduled run: webhook-check:2026-04-20T09:00:00Z succeeded",
		"notification recorded: webhook-check:2026-04-20T09:00:00Z:succeeded run=webhook-check:2026-04-20T09:00:00Z delivery=pending",
		"webhook received: method=POST idempotency=webhook-check:2026-04-20T09:00:00Z:succeeded route=personal-notifications signature=true event=personal.scheduled_run.notification",
		"webhook payload: run=webhook-check:2026-04-20T09:00:00Z status=succeeded result=\"Scheduled webhook notice complete: the owner update is ready for signed delivery.\"",
		"delivered: webhook-check:2026-04-20T09:00:00Z:succeeded status=delivered attempts=1",
		"final delivered notifications: 1",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("example output missing %q:\n%s", want, got)
		}
	}

	assertOrder(t, got,
		"notification recorded:",
		"webhook received:",
		"webhook payload:",
		"delivered:",
		"final delivered notifications:",
	)
}

func assertOrder(t *testing.T, got string, parts ...string) {
	t.Helper()
	previous := -1
	for _, part := range parts {
		index := strings.Index(got, part)
		if index < 0 {
			t.Fatalf("example output missing %q:\n%s", part, got)
		}
		if index < previous {
			t.Fatalf("example output out of order at %q:\n%s", part, got)
		}
		previous = index
	}
}
