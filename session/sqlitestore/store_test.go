package sqlitestore

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
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

func TestStoreChildren(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	root, err := store.Create(ctx)
	if err != nil {
		t.Fatalf("Create root returned error: %v", err)
	}
	childA, err := store.CreateWithOptions(ctx, session.CreateOptions{ParentID: root.ID})
	if err != nil {
		t.Fatalf("Create childA returned error: %v", err)
	}
	childB, err := store.CreateWithOptions(ctx, session.CreateOptions{ParentID: root.ID})
	if err != nil {
		t.Fatalf("Create childB returned error: %v", err)
	}
	grandchild, err := store.CreateWithOptions(ctx, session.CreateOptions{ParentID: childA.ID})
	if err != nil {
		t.Fatalf("Create grandchild returned error: %v", err)
	}

	roots, err := store.Children(ctx, "")
	if err != nil {
		t.Fatalf("Children roots returned error: %v", err)
	}
	if len(roots) != 1 || roots[0].ID != root.ID {
		t.Fatalf("roots = %#v, want root", roots)
	}
	children, err := store.Children(ctx, root.ID)
	if err != nil {
		t.Fatalf("Children root returned error: %v", err)
	}
	if len(children) != 2 || children[0].ID != childA.ID || children[1].ID != childB.ID {
		t.Fatalf("children = %#v, want childA then childB", children)
	}
	grandchildren, err := session.Children(ctx, store, childA.ID)
	if err != nil {
		t.Fatalf("session.Children returned error: %v", err)
	}
	if len(grandchildren) != 1 || grandchildren[0].ID != grandchild.ID {
		t.Fatalf("grandchildren = %#v, want grandchild", grandchildren)
	}
}

func TestStoreCanonicalizesInputSessionIDs(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	sess, err := store.Create(ctx)
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}
	upperID := strings.ToUpper(sess.ID)
	if err := store.Append(ctx, upperID, model.Message{ID: "m1", Role: model.RoleUser}); err != nil {
		t.Fatalf("Append returned error: %v", err)
	}
	got, err := store.Get(ctx, upperID)
	if err != nil {
		t.Fatalf("Get returned error: %v", err)
	}
	if got.ID != sess.ID {
		t.Fatalf("Get ID = %q, want canonical %q", got.ID, sess.ID)
	}
	messages, err := store.Messages(ctx, upperID)
	if err != nil {
		t.Fatalf("Messages returned error: %v", err)
	}
	if len(messages) != 1 || messages[0].ID != "m1" {
		t.Fatalf("Messages = %#v, want appended message", messages)
	}
}

func TestStoreAppendNormalizesEmptyToolInput(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	sess, err := store.Create(ctx)
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}
	err = store.Append(ctx, sess.ID, model.Message{
		Role: model.RoleAssistant,
		Content: []model.ContentBlock{{
			Type: model.ContentToolUse,
			ToolUse: &model.ToolUse{
				ID:    "tool-1",
				Name:  "workspace_apply_patch",
				Input: json.RawMessage{},
			},
		}},
	})
	if err != nil {
		t.Fatalf("Append returned error: %v", err)
	}
	messages, err := store.Messages(ctx, sess.ID)
	if err != nil {
		t.Fatalf("Messages returned error: %v", err)
	}
	if got := string(messages[0].Content[0].ToolUse.Input); got != `{}` {
		t.Fatalf("stored tool input = %q, want {}", got)
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

func TestStoreCompactionCheckpointPreservesRawTranscriptAndActiveView(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	sess, err := store.Create(ctx)
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}
	for _, msg := range []model.Message{
		{ID: "m1", Role: model.RoleUser, Content: []model.ContentBlock{{Type: model.ContentText, Text: "old"}}},
		{ID: "m2", Role: model.RoleAssistant, Content: []model.ContentBlock{{Type: model.ContentText, Text: "middle"}}},
	} {
		if err := store.Append(ctx, sess.ID, msg); err != nil {
			t.Fatalf("Append returned error: %v", err)
		}
	}
	if err := store.SaveCompaction(ctx, sess.ID, session.CompactionCheckpoint{
		RawMessageCount: 2,
		Messages: []model.Message{{
			ID:      "summary",
			Role:    model.RoleUser,
			Content: []model.ContentBlock{{Type: model.ContentText, Text: "summary"}},
		}},
		Policy: "test",
		Reason: "budget",
	}); err != nil {
		t.Fatalf("SaveCompaction returned error: %v", err)
	}
	if err := store.Append(ctx, sess.ID, model.Message{
		ID:      "m3",
		Role:    model.RoleUser,
		Content: []model.ContentBlock{{Type: model.ContentText, Text: "new"}},
	}); err != nil {
		t.Fatalf("Append returned error: %v", err)
	}

	raw, err := store.Messages(ctx, sess.ID)
	if err != nil {
		t.Fatalf("Messages returned error: %v", err)
	}
	if len(raw) != 3 || raw[0].PlainText() != "old" {
		t.Fatalf("raw messages = %#v, want full transcript", raw)
	}
	view, err := store.MessageView(ctx, sess.ID)
	if err != nil {
		t.Fatalf("MessageView returned error: %v", err)
	}
	if len(view.Messages) != 2 || view.Messages[0].PlainText() != "summary" || view.Messages[1].PlainText() != "new" {
		t.Fatalf("active messages = %#v, want summary plus new message", view.Messages)
	}
	if view.Compaction == nil || view.Compaction.Policy != "test" {
		t.Fatalf("compaction = %#v, want persisted checkpoint", view.Compaction)
	}
}

func TestStoreSaveCompactionRejectsUnknownSession(t *testing.T) {
	store := newTestStore(t)
	err := store.SaveCompaction(context.Background(), "00000000-0000-7000-8000-000000000000", session.CompactionCheckpoint{
		RawMessageCount: 0,
		Messages: []model.Message{{
			Role:    model.RoleUser,
			Content: []model.ContentBlock{{Type: model.ContentText, Text: "summary"}},
		}},
	})
	if err == nil || !strings.Contains(err.Error(), "unknown session") {
		t.Fatalf("SaveCompaction error = %v, want unknown session", err)
	}
}

func TestStoreSaveCompactionRejectsRawMessageCountOvershoot(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	sess, err := store.Create(ctx)
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}
	if err := store.Append(ctx, sess.ID, model.Message{
		ID:      "m1",
		Role:    model.RoleUser,
		Content: []model.ContentBlock{{Type: model.ContentText, Text: "old"}},
	}); err != nil {
		t.Fatalf("Append returned error: %v", err)
	}

	err = store.SaveCompaction(ctx, sess.ID, session.CompactionCheckpoint{
		RawMessageCount: 2,
		Messages: []model.Message{{
			ID:      "summary",
			Role:    model.RoleUser,
			Content: []model.ContentBlock{{Type: model.ContentText, Text: "summary"}},
		}},
	})
	if err == nil || !strings.Contains(err.Error(), "exceeds transcript length") {
		t.Fatalf("SaveCompaction error = %v, want raw count overshoot", err)
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
