package session

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/MemaxLabs/memax-go-agent-sdk/model"
)

func TestJSONLStoreRoundTrip(t *testing.T) {
	store := NewJSONLStore(t.TempDir())
	sess, err := store.Create(context.Background())
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}

	want := []model.Message{
		{
			Role: model.RoleUser,
			Content: []model.ContentBlock{
				{Type: model.ContentText, Text: "hello"},
			},
		},
		{
			Role: model.RoleTool,
			ToolResult: &model.ToolResult{
				ToolUseID: "tool-1",
				Name:      "read",
				Content:   "result",
			},
		},
	}
	for _, msg := range want {
		if err := store.Append(context.Background(), sess.ID, msg); err != nil {
			t.Fatalf("Append returned error: %v", err)
		}
	}

	got, err := store.Messages(context.Background(), sess.ID)
	if err != nil {
		t.Fatalf("Messages returned error: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("len(messages) = %d, want %d", len(got), len(want))
	}
	if got[0].PlainText() != "hello" {
		t.Fatalf("first message = %#v, want hello", got[0])
	}
	if got[1].ToolResult == nil || got[1].ToolResult.Content != "result" {
		t.Fatalf("second message = %#v, want tool result", got[1])
	}
}

func TestJSONLStoreRejectsInvalidSessionID(t *testing.T) {
	store := NewJSONLStore(t.TempDir())
	err := store.Append(context.Background(), "../escape", model.Message{})
	if err == nil {
		t.Fatal("Append returned nil, want invalid session id error")
	}
}

func TestJSONLStoreReportsCorruptTranscriptLine(t *testing.T) {
	dir := t.TempDir()
	id := "0123456789abcdef0123456789abcdef"
	path := filepath.Join(dir, id+transcriptExt)
	if err := os.WriteFile(path, []byte("{not json}\n"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	_, err := NewJSONLStore(dir).Messages(context.Background(), id)
	if err == nil {
		t.Fatal("Messages returned nil, want corrupt transcript error")
	}
}
