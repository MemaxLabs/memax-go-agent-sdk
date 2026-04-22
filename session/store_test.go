package session

import (
	"context"
	"strings"
	"testing"

	"github.com/MemaxLabs/memax-go-agent-sdk/model"
)

func TestMemoryStoreCreateWithParent(t *testing.T) {
	store := NewMemoryStore()
	parentID := "00000000-0000-7000-8000-000000000000"
	sess, err := store.CreateWithOptions(context.Background(), CreateOptions{ParentID: parentID})
	if err != nil {
		t.Fatalf("CreateWithOptions returned error: %v", err)
	}
	if sess.ParentID != parentID {
		t.Fatalf("ParentID = %q, want %q", sess.ParentID, parentID)
	}
}

func TestCreateUsesExtendedStore(t *testing.T) {
	store := NewMemoryStore()
	parentID := "00000000-0000-7000-8000-000000000000"
	sess, err := Create(context.Background(), store, CreateOptions{ParentID: parentID})
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}
	if sess.ParentID != parentID {
		t.Fatalf("ParentID = %q, want %q", sess.ParentID, parentID)
	}
}

func TestMemoryStoreRejectsInvalidParentSessionID(t *testing.T) {
	_, err := NewMemoryStore().CreateWithOptions(context.Background(), CreateOptions{ParentID: "parent-session"})
	if err == nil {
		t.Fatal("CreateWithOptions returned nil, want invalid parent session id")
	}
}

func TestValidIDAcceptsCanonicalUUIDCase(t *testing.T) {
	lower := "0194d9a4-7b8c-7d20-9a1b-4f6c6f4f7a01"
	upper := "0194D9A4-7B8C-7D20-9A1B-4F6C6F4F7A01"
	if !ValidID(lower) {
		t.Fatalf("ValidID(%q) = false, want true", lower)
	}
	if !ValidID(upper) {
		t.Fatalf("ValidID(%q) = false, want true", upper)
	}
	canonical, ok := CanonicalID(upper)
	if !ok || canonical != lower {
		t.Fatalf("CanonicalID(%q) = %q, %t; want %q, true", upper, canonical, ok, lower)
	}
}

func TestMemoryStoreGetListAndFork(t *testing.T) {
	store := NewMemoryStore()
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

	got, err := Get(context.Background(), store, sess.ID)
	if err != nil {
		t.Fatalf("Get returned error: %v", err)
	}
	if got.ID != sess.ID {
		t.Fatalf("Get = %#v, want session id", got)
	}

	sessions, err := List(context.Background(), store)
	if err != nil {
		t.Fatalf("List returned error: %v", err)
	}
	if len(sessions) != 1 || sessions[0].ID != sess.ID {
		t.Fatalf("List = %#v, want source session", sessions)
	}

	forked, err := Fork(context.Background(), store, sess.ID, ForkOptions{ThroughMessageID: "m1"})
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

func TestMemoryStoreCanonicalizesInputSessionIDs(t *testing.T) {
	store := NewMemoryStore()
	sess, err := store.Create(context.Background())
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}
	upperID := strings.ToUpper(sess.ID)
	if err := store.Append(context.Background(), upperID, model.Message{ID: "m1", Role: model.RoleUser}); err != nil {
		t.Fatalf("Append returned error: %v", err)
	}
	got, err := store.Get(context.Background(), upperID)
	if err != nil {
		t.Fatalf("Get returned error: %v", err)
	}
	if got.ID != sess.ID {
		t.Fatalf("Get ID = %q, want canonical %q", got.ID, sess.ID)
	}
	messages, err := store.Messages(context.Background(), upperID)
	if err != nil {
		t.Fatalf("Messages returned error: %v", err)
	}
	if len(messages) != 1 || messages[0].ID != "m1" {
		t.Fatalf("Messages = %#v, want appended message", messages)
	}
}

func TestMemoryStoreRejectsInvalidForkParentSessionID(t *testing.T) {
	store := NewMemoryStore()
	sess, err := store.Create(context.Background())
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}
	_, err = store.Fork(context.Background(), sess.ID, ForkOptions{ParentID: "parent-session"})
	if err == nil {
		t.Fatal("Fork returned nil, want invalid parent session id")
	}
}

func TestMemoryStoreAssignsMessageID(t *testing.T) {
	store := NewMemoryStore()
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

func TestMemoryStoreReturnsDefensiveMessageCopies(t *testing.T) {
	store := NewMemoryStore()
	sess, err := store.Create(context.Background())
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}
	if err := store.Append(context.Background(), sess.ID, model.Message{
		Role: model.RoleAssistant,
		Content: []model.ContentBlock{{
			Type:    model.ContentToolUse,
			ToolUse: &model.ToolUse{ID: "tool-1", Name: "read", Input: []byte(`{"path":"README.md"}`)},
		}},
		Metadata: map[string]any{"summary": true},
		ToolResult: &model.ToolResult{
			ToolUseID: "tool-1",
			Name:      "read",
			Content:   "result",
			Metadata:  map[string]any{"stored_result_id": "result-1"},
		},
	}); err != nil {
		t.Fatalf("Append returned error: %v", err)
	}

	first, err := store.Messages(context.Background(), sess.ID)
	if err != nil {
		t.Fatalf("Messages returned error: %v", err)
	}
	first[0].Content[0].ToolUse.Name = "mutated"
	first[0].Metadata["summary"] = false
	first[0].ToolResult.Metadata["stored_result_id"] = "mutated"

	second, err := store.Messages(context.Background(), sess.ID)
	if err != nil {
		t.Fatalf("Messages returned error: %v", err)
	}
	if second[0].Content[0].ToolUse.Name != "read" {
		t.Fatalf("tool use = %#v, want defensive copy", second[0].Content[0].ToolUse)
	}
	if second[0].Metadata["summary"] != true {
		t.Fatalf("metadata = %#v, want defensive copy", second[0].Metadata)
	}
	if second[0].ToolResult.Metadata["stored_result_id"] != "result-1" {
		t.Fatalf("tool result metadata = %#v, want defensive copy", second[0].ToolResult.Metadata)
	}
}

func TestMemoryStoreForkRejectsMissingMessageID(t *testing.T) {
	store := NewMemoryStore()
	sess, err := store.Create(context.Background())
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}
	_, err = store.Fork(context.Background(), sess.ID, ForkOptions{ThroughMessageID: "missing"})
	if err == nil {
		t.Fatal("Fork returned nil, want missing message error")
	}
}
