package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestRunExampleShowsNoteRecallAndApprovalFlow(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	if err := runExample(context.Background(), &out); err != nil {
		t.Fatalf("runExample() error = %v", err)
	}
	got := out.String()
	for _, want := range []string{
		"tool use: search_notes",
		"tool use: read_note",
		"tool use: request_approval",
		"approval requested: save_note",
		"approval granted: save_note",
		"tool use: save_note",
		"approval consumed: save_note",
		"tool use: search_notes",
		"owner and due-date bullets",
		"meeting follow-up template",
		"result: Recalled the existing note style, saved a matching reusable template, and confirmed it is now searchable.",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("example output missing %q:\n%s", want, got)
		}
	}
}
