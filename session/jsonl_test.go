package session

import (
	"context"
	"os"
	"path/filepath"
	"strings"
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

func TestJSONLStoreCanonicalizesInputSessionIDs(t *testing.T) {
	store := NewJSONLStore(t.TempDir())
	sess, err := store.Create(context.Background())
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}
	upperID := strings.ToUpper(sess.ID)
	if err := store.Append(context.Background(), upperID, model.Message{ID: "m1", Role: model.RoleUser}); err != nil {
		t.Fatalf("Append returned error: %v", err)
	}
	exists, err := store.Exists(context.Background(), upperID)
	if err != nil {
		t.Fatalf("Exists returned error: %v", err)
	}
	if !exists {
		t.Fatal("Exists returned false for uppercase session id")
	}
	got, err := store.Get(context.Background(), upperID)
	if err != nil {
		t.Fatalf("Get returned error: %v", err)
	}
	if got.ID != sess.ID {
		t.Fatalf("Get ID = %q, want canonical %q", got.ID, sess.ID)
	}
}

func TestJSONLStoreCanonicalizesMissingTranscriptSessionIDFallback(t *testing.T) {
	dir := t.TempDir()
	id := "00000000-0000-7000-8000-000000000000"
	path := filepath.Join(dir, id+transcriptExt)
	if err := os.WriteFile(path, []byte("{\"type\":\"session\",\"session\":{}}\n"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	got, err := NewJSONLStore(dir).Get(context.Background(), strings.ToUpper(id))
	if err != nil {
		t.Fatalf("Get returned error: %v", err)
	}
	if got.ID != id {
		t.Fatalf("Get ID = %q, want canonical %q", got.ID, id)
	}
}

func TestJSONLStoreCreateWithParent(t *testing.T) {
	store := NewJSONLStore(t.TempDir())
	sess, err := store.CreateWithOptions(context.Background(), CreateOptions{
		ParentID: "00000000-0000-7000-8000-000000000000",
	})
	if err != nil {
		t.Fatalf("CreateWithOptions returned error: %v", err)
	}
	if sess.ParentID != "00000000-0000-7000-8000-000000000000" {
		t.Fatalf("ParentID = %q, want parent id", sess.ParentID)
	}
	messages, err := store.Messages(context.Background(), sess.ID)
	if err != nil {
		t.Fatalf("Messages returned error: %v", err)
	}
	if len(messages) != 0 {
		t.Fatalf("messages = %#v, want empty transcript", messages)
	}
}

func TestJSONLStoreGetListAndFork(t *testing.T) {
	store := NewJSONLStore(t.TempDir())
	sess, err := store.Create(context.Background())
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}
	messages := []model.Message{
		{ID: "m1", Role: model.RoleUser, Content: []model.ContentBlock{{Type: model.ContentText, Text: "one"}}},
		{ID: "m2", Role: model.RoleAssistant, Content: []model.ContentBlock{{Type: model.ContentText, Text: "two"}}},
	}
	for _, msg := range messages {
		if err := store.Append(context.Background(), sess.ID, msg); err != nil {
			t.Fatalf("Append returned error: %v", err)
		}
	}

	got, err := store.Get(context.Background(), sess.ID)
	if err != nil {
		t.Fatalf("Get returned error: %v", err)
	}
	if got.ID != sess.ID || got.CreatedAt.IsZero() {
		t.Fatalf("Get = %#v, want persisted session metadata", got)
	}

	sessions, err := store.List(context.Background())
	if err != nil {
		t.Fatalf("List returned error: %v", err)
	}
	if len(sessions) != 1 || sessions[0].ID != sess.ID {
		t.Fatalf("List = %#v, want source session", sessions)
	}

	forked, err := store.Fork(context.Background(), sess.ID, ForkOptions{ThroughMessageID: "m1"})
	if err != nil {
		t.Fatalf("Fork returned error: %v", err)
	}
	if forked.ParentID != sess.ID {
		t.Fatalf("fork ParentID = %q, want source id", forked.ParentID)
	}
	forkMessages, err := store.Messages(context.Background(), forked.ID)
	if err != nil {
		t.Fatalf("Messages returned error: %v", err)
	}
	if len(forkMessages) != 1 || forkMessages[0].ID != "m1" {
		t.Fatalf("fork messages = %#v, want through m1", forkMessages)
	}
}

func TestJSONLStoreAssignsMessageID(t *testing.T) {
	store := NewJSONLStore(t.TempDir())
	sess, err := store.Create(context.Background())
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}
	if err := store.Append(context.Background(), sess.ID, model.Message{Role: model.RoleUser}); err != nil {
		t.Fatalf("Append returned error: %v", err)
	}
	messages, err := store.Messages(context.Background(), sess.ID)
	if err != nil {
		t.Fatalf("Messages returned error: %v", err)
	}
	if len(messages) != 1 || messages[0].ID == "" {
		t.Fatalf("messages = %#v, want generated message id", messages)
	}
}

func TestJSONLStoreListMissingDirectory(t *testing.T) {
	sessions, err := NewJSONLStore(filepath.Join(t.TempDir(), "missing")).List(context.Background())
	if err != nil {
		t.Fatalf("List returned error: %v", err)
	}
	if len(sessions) != 0 {
		t.Fatalf("sessions = %#v, want empty list", sessions)
	}
}

func TestValidID(t *testing.T) {
	if !ValidID("00000000-0000-7000-8000-000000000000") {
		t.Fatal("ValidID returned false for uuid shape")
	}
	if ValidID("../escape") {
		t.Fatal("ValidID returned true for path traversal")
	}
}

func TestJSONLStoreExists(t *testing.T) {
	ctx := context.Background()
	store := NewJSONLStore(t.TempDir())
	sess, err := store.Create(ctx)
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}
	exists, err := store.Exists(ctx, sess.ID)
	if err != nil {
		t.Fatalf("Exists returned error: %v", err)
	}
	if !exists {
		t.Fatal("Exists returned false for created session")
	}

	exists, err = store.Exists(ctx, "00000000-0000-7000-8000-000000000001")
	if err != nil {
		t.Fatalf("Exists missing returned error: %v", err)
	}
	if exists {
		t.Fatal("Exists returned true for missing session")
	}

	if _, err := store.Exists(ctx, "../escape"); err == nil {
		t.Fatal("Exists returned nil error for invalid session id")
	}
}

func TestJSONLStoreRejectsInvalidParentSessionID(t *testing.T) {
	_, err := NewJSONLStore(t.TempDir()).CreateWithOptions(context.Background(), CreateOptions{
		ParentID: "../escape",
	})
	if err == nil {
		t.Fatal("CreateWithOptions returned nil, want invalid parent session id error")
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
	id := "00000000-0000-7000-8000-000000000000"
	path := filepath.Join(dir, id+transcriptExt)
	if err := os.WriteFile(path, []byte("{not json}\n"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	_, err := NewJSONLStore(dir).Messages(context.Background(), id)
	if err == nil {
		t.Fatal("Messages returned nil, want corrupt transcript error")
	}
}
