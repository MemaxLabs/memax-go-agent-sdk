package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestRunExampleShowsMessageRecallAndApprovalFlow(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	if err := runExample(context.Background(), &out); err != nil {
		t.Fatalf("runExample() error = %v", err)
	}
	got := out.String()
	for _, want := range []string{
		"tool use: search_message_threads",
		"tool use: read_message_thread",
		"tool use: request_approval",
		"approval requested: send_message",
		"approval granted: send_message",
		"tool use: send_message",
		"approval consumed: send_message",
		"Project kickoff follow-up",
		"owners and due dates",
		"result: Recalled the existing thread guidance, sent an approved reply, and confirmed the thread is still discoverable.",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("example output missing %q:\n%s", want, got)
		}
	}
}
