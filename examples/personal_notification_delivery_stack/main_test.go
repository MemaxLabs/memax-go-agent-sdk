package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestRunExampleShowsNotificationDeliveryRetry(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	if err := runExample(context.Background(), &out); err != nil {
		t.Fatalf("runExample() error = %v", err)
	}
	got := out.String()
	for _, want := range []string{
		"scheduled lifecycle: delivery-check:2026-04-20T09:00:00Z queued",
		"scheduled lifecycle: delivery-check:2026-04-20T09:00:00Z running",
		"scheduled lifecycle: delivery-check:2026-04-20T09:00:00Z succeeded",
		"scheduled run: delivery-check:2026-04-20T09:00:00Z succeeded",
		"notification recorded: delivery-check:2026-04-20T09:00:00Z:succeeded run=delivery-check:2026-04-20T09:00:00Z delivery=pending",
		"claim 1: delivery-check:2026-04-20T09:00:00Z:succeeded attempts=1 status=delivering",
		"delivery failed: push gateway unavailable retry_after=2m status=failed",
		"claim before retry: 0",
		"claim 2: delivery-check:2026-04-20T09:00:00Z:succeeded attempts=2 status=delivering",
		"delivery sent to host channel: delivery-check -> Scheduled delivery check complete: the owner update is ready for notification.",
		"delivered: delivery-check:2026-04-20T09:00:00Z:succeeded status=delivered attempts=2",
		"final delivered notifications: 1",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("example output missing %q:\n%s", want, got)
		}
	}

	assertOrder(t, got,
		"notification recorded:",
		"claim 1:",
		"delivery failed:",
		"claim before retry: 0",
		"claim 2:",
		"delivery sent to host channel:",
		"delivered:",
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
