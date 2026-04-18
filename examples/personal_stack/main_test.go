package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestRunExampleShowsRecallAndApprovalFlow(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	if err := runExample(context.Background(), &out); err != nil {
		t.Fatalf("runExample() error = %v", err)
	}
	got := out.String()
	for _, want := range []string{
		"tool use: request_approval",
		"approval requested: save_memory",
		"approval granted: save_memory",
		"tool use: save_memory",
		"approval consumed: save_memory",
		"tool use: search_memories",
		"action-oriented format",
		"meeting-follow-up-format",
		"result: Recalled the user's action-oriented style, saved a matching durable follow-up preference, and confirmed it is now searchable.",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("example output missing %q:\n%s", want, got)
		}
	}
}
