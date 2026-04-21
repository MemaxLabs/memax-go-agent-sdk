package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestRunExampleShowsWaitDrivenRepairFlow(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	if err := runExample(context.Background(), &out); err != nil {
		t.Fatalf("runExample() error = %v", err)
	}
	got := out.String()
	for _, want := range []string{
		"tool use: start_command",
		"command started: watch-1 status=running",
		"tool use: wait_command_output",
		"command output: wait id=watch-1 chunks=1",
		"watch: README.md status must be fixed",
		"tool use: workspace_checkpoint",
		"tool use: workspace_apply_patch",
		"workspace patch: README.md",
		"modified README.md",
		"watch: ok",
		"tool use: stop_command",
		"tool result: verification test passed",
		"result: Watch mode passed after wait-driven repair.",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("example output missing %q:\n%s", want, got)
		}
	}
	if gotCount := strings.Count(got, "watch: README.md status must be fixed"); gotCount != 1 {
		t.Fatalf("expected after_seq to suppress duplicate failing output; count = %d:\n%s", gotCount, got)
	}
}
