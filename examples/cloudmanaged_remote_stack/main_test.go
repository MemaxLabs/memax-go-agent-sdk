package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestRunDemoShowsRemoteWorkerLifecycle(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	if err := run(context.Background(), &out, "demo", "", "", ""); err != nil {
		t.Fatalf("run(demo) error = %v", err)
	}
	got := out.String()
	for _, want := range []string{
		"run state: queued",
		"run state: running",
		"run state: succeeded",
		"result: remote worker finished the managed run",
		"server GET /ready:",
		"server GET /claim:",
		"worker: example-worker-1",
		"run:",
		"succeeded",
		"session:",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("example output missing %q:\n%s", want, got)
		}
	}
}

func TestRunRejectsUnknownMode(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	if err := run(context.Background(), &out, "wat", "", "", ""); err == nil {
		t.Fatalf("run(unknown) error = nil, want error")
	}
}
