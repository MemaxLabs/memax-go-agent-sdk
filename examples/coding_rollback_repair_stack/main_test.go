package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestRunExampleShowsRollbackRepairFlow(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	if err := runExample(context.Background(), &out); err != nil {
		t.Fatalf("runExample() error = %v", err)
	}
	got := out.String()
	for _, want := range []string{
		"tool use: start_command",
		"command started: watch-1 status=running",
		"tool use: read_command_output",
		"watch: README.md status must be fixed",
		"resume_after_seq: 1",
		"tool use: workspace_apply_patch",
		"create a workspace checkpoint before applying patches",
		"tool use: workspace_checkpoint",
		"workspace checkpoint: checkpoint-1",
		"watch: status fixed but owner changed",
		"resume_after_seq: 2",
		"verification: test passed=false diagnostics=1",
		"Rollback policy: restore workspace checkpoint checkpoint-1",
		"workspace restore: checkpoint-1",
		"watch: ok",
		"resume_after_seq: 3",
		"tool use: stop_command",
		"verification: test passed=true diagnostics=0",
		"task: task-1 status=completed",
		"result: README restored, repaired, verified, and task completed.",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("example output missing %q:\n%s", want, got)
		}
	}
	if gotCount := strings.Count(got, "workspace patch: README.md"); gotCount != 2 {
		t.Fatalf("workspace patch events = %d, want 2:\n%s", gotCount, got)
	}
	if gotCount := strings.Count(got, "hint: owner line must stay api"); gotCount != 1 {
		t.Fatalf("after_seq did not suppress repeated owner hint; count = %d:\n%s", gotCount, got)
	}
	if gotCount := strings.Count(got, "watch: status fixed but owner changed"); gotCount != 1 {
		t.Fatalf("after_seq did not suppress repeated owner warning; count = %d:\n%s", gotCount, got)
	}
	if gotCount := strings.Count(got, "verification: test"); gotCount != 2 {
		t.Fatalf("verification events = %d, want 2:\n%s", gotCount, got)
	}
}
