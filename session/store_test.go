package session

import (
	"context"
	"testing"

	"github.com/MemaxLabs/memax-go-agent-sdk/model"
)

func TestMemoryStoreCreateWithParent(t *testing.T) {
	store := NewMemoryStore()
	sess, err := store.CreateWithOptions(context.Background(), CreateOptions{ParentID: "parent-session"})
	if err != nil {
		t.Fatalf("CreateWithOptions returned error: %v", err)
	}
	if sess.ParentID != "parent-session" {
		t.Fatalf("ParentID = %q, want parent-session", sess.ParentID)
	}
}

func TestCreateUsesExtendedStore(t *testing.T) {
	store := NewMemoryStore()
	sess, err := Create(context.Background(), store, CreateOptions{ParentID: "parent-session"})
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}
	if sess.ParentID != "parent-session" {
		t.Fatalf("ParentID = %q, want parent-session", sess.ParentID)
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
