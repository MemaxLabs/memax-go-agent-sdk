package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestRunExampleShowsManagedObservability(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	if err := runExample(context.Background(), &out); err != nil {
		t.Fatalf("runExample() error = %v", err)
	}
	got := out.String()
	for _, want := range []string{
		"audit tenant denied: boundary=session_start",
		"audit run state: queued worker=",
		"audit run state: running worker=observability-worker-1",
		"audit run state: succeeded worker=observability-worker-1",
		"audit result: observed remote worker completed",
		"server GET /claim:",
		"worker: observability-worker-1",
		"run:",
		"succeeded",
		"session:",
		"secondary metric sink captured:",
		"metric counter: memax.cloudmanaged.run.lifecycle.events=1 run_status=queued run_terminal=false",
		"metric counter: memax.cloudmanaged.run.lifecycle.events=1 run_status=running run_terminal=false",
		"metric counter: memax.cloudmanaged.run.lifecycle.events=1 run_status=succeeded run_terminal=true",
		"metric record: memax.cloudmanaged.run.queue_latency_ms=",
		"metric record: memax.cloudmanaged.run.duration_ms=",
		"metric record: memax.cloudmanaged.run.total_duration_ms=",
		"metric counter: memax.cloudmanaged.tenant.denials=1 tenant_boundary=session_start",
		"metric counter: memax.cloudmanaged.worker.claims=1",
		"metric counter: memax.cloudmanaged.worker.heartbeats=1",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("example output missing %q:\n%s", want, got)
		}
	}
	for _, want := range []string{
		"audit tenant denied:",
		"audit run state: queued",
		"audit run state: running",
		"audit run state: succeeded",
		"audit result: observed remote worker completed",
	} {
		assertCount(t, got, want, 1)
	}
	assertOrder(t, got,
		"audit tenant denied: boundary=session_start",
		"audit run state: queued",
		"audit run state: running",
		"audit result: observed remote worker completed",
		"audit run state: succeeded",
	)
}

func assertOrder(t *testing.T, got string, wants ...string) {
	t.Helper()
	last := -1
	for _, want := range wants {
		index := strings.Index(got, want)
		if index < 0 {
			t.Fatalf("output missing %q:\n%s", want, got)
		}
		if index < last {
			t.Fatalf("output has %q before earlier milestone:\n%s", want, got)
		}
		last = index
	}
}

func assertCount(t *testing.T, got, want string, count int) {
	t.Helper()
	if actual := strings.Count(got, want); actual != count {
		t.Fatalf("strings.Count(output, %q) = %d, want %d:\n%s", want, actual, count, got)
	}
}
