package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestRunExampleShowsSafeLocalFlow(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	if err := runExample(context.Background(), &out); err != nil {
		t.Fatalf("runExample() error = %v", err)
	}
	got := out.String()
	for _, want := range []string{
		"tool use: workspace_checkpoint",
		"workspace checkpoint: checkpoint-1",
		"tool use: workspace_apply_patch",
		"workspace patch: README.md",
		"verification: test passed=true diagnostics=0",
		"tool result: verification test passed",
		"task: task-1 status=completed",
		"result: Safe local edit checkpointed, patched, verified, and completed.",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("example output missing %q:\n%s", want, got)
		}
	}
	if gotCount := strings.Count(got, "workspace patch: README.md"); gotCount != 1 {
		t.Fatalf("workspace patch events = %d, want 1:\n%s", gotCount, got)
	}
	if gotCount := strings.Count(got, "verification: test"); gotCount != 1 {
		t.Fatalf("verification events = %d, want 1:\n%s", gotCount, got)
	}
}
