package sqlitestore

import (
	"context"
	"database/sql"
	"testing"

	"github.com/MemaxLabs/memax-go-agent-sdk/model"
	"github.com/MemaxLabs/memax-go-agent-sdk/session"
	_ "modernc.org/sqlite"
)

func TestStoreRoundTripGetListAndFork(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	parentID := "00000000-0000-7000-8000-000000000000"
	sess, err := store.CreateWithOptions(ctx, session.CreateOptions{ParentID: parentID})
	if err != nil {
		t.Fatalf("CreateWithOptions returned error: %v", err)
	}
	if sess.ParentID != parentID || sess.CreatedAt.IsZero() {
		t.Fatalf("session = %#v, want parent and created time", sess)
	}

	messages := []model.Message{
		{ID: "m1", Role: model.RoleUser, Content: []model.ContentBlock{{Type: model.ContentText, Text: "one"}}},
		{ID: "m2", Role: model.RoleAssistant, Content: []model.ContentBlock{{Type: model.ContentText, Text: "two"}}},
	}
	for _, msg := range messages {
		if err := store.Append(ctx, sess.ID, msg); err != nil {
			t.Fatalf("Append returned error: %v", err)
		}
	}

	got, err := store.Get(ctx, sess.ID)
	if err != nil {
		t.Fatalf("Get returned error: %v", err)
	}
	if got.ID != sess.ID || got.ParentID != parentID {
		t.Fatalf("Get = %#v, want session metadata", got)
	}

	gotMessages, err := store.Messages(ctx, sess.ID)
	if err != nil {
		t.Fatalf("Messages returned error: %v", err)
	}
	if len(gotMessages) != 2 || gotMessages[0].PlainText() != "one" || gotMessages[1].PlainText() != "two" {
		t.Fatalf("messages = %#v, want ordered transcript", gotMessages)
	}

	sessions, err := store.List(ctx)
	if err != nil {
		t.Fatalf("List returned error: %v", err)
	}
	if len(sessions) != 1 || sessions[0].ID != sess.ID {
		t.Fatalf("sessions = %#v, want source session", sessions)
	}

	forked, err := store.Fork(ctx, sess.ID, session.ForkOptions{ThroughMessageID: "m1"})
	if err != nil {
		t.Fatalf("Fork returned error: %v", err)
	}
	if forked.ParentID != sess.ID {
		t.Fatalf("fork ParentID = %q, want source session", forked.ParentID)
	}
	forkMessages, err := store.Messages(ctx, forked.ID)
	if err != nil {
		t.Fatalf("fork Messages returned error: %v", err)
	}
	if len(forkMessages) != 1 || forkMessages[0].ID != "m1" {
		t.Fatalf("fork messages = %#v, want through m1", forkMessages)
	}
}

func TestStoreRejectsInvalidParentSessionID(t *testing.T) {
	store := newTestStore(t)
	_, err := store.CreateWithOptions(context.Background(), session.CreateOptions{ParentID: "parent"})
	if err == nil {
		t.Fatal("CreateWithOptions returned nil, want invalid parent session id")
	}
}

func TestStoreAssignsMessageID(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	sess, err := store.Create(ctx)
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}
	if err := store.Append(ctx, sess.ID, model.Message{Role: model.RoleUser}); err != nil {
		t.Fatalf("Append returned error: %v", err)
	}
	messages, err := store.Messages(ctx, sess.ID)
	if err != nil {
		t.Fatalf("Messages returned error: %v", err)
	}
	if len(messages) != 1 || messages[0].ID == "" {
		t.Fatalf("messages = %#v, want generated message id", messages)
	}
}

func TestStoreForkRejectsMissingMessageID(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	sess, err := store.Create(ctx)
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}
	_, err = store.Fork(ctx, sess.ID, session.ForkOptions{ThroughMessageID: "missing"})
	if err == nil {
		t.Fatal("Fork returned nil, want missing message error")
	}
}

func newTestStore(t *testing.T) *Store {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+t.Name()+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Close()
	})
	store, err := New(context.Background(), db)
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	return store
}
