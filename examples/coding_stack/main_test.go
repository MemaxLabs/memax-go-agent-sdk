package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestRunExampleShowsApprovalRepairFlow(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	if err := runExample(context.Background(), &out); err != nil {
		t.Fatalf("runExample() error = %v", err)
	}
	got := out.String()
	for _, want := range []string{
		"tool use: run_command",
		"tool use: request_approval",
		"approval requested: workspace_apply_patch",
		"approval granted: workspace_apply_patch",
		"approval consumed: workspace_apply_patch",
		"modified README.md",
		"tool result: verification test passed",
		"result: Repaired README after approval, reran the check, and verified the workspace.",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("example output missing %q:\n%s", want, got)
		}
	}
}
