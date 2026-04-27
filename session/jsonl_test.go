package session

import (
	"context"
	"encoding/json"
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

func TestJSONLStoreAppendNormalizesEmptyToolInput(t *testing.T) {
	store := NewJSONLStore(t.TempDir())
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

func TestJSONLStoreCompactionCheckpointPreservesRawTranscriptAndActiveView(t *testing.T) {
	ctx := context.Background()
	store := NewJSONLStore(t.TempDir())
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
	if err := store.SaveCompaction(ctx, sess.ID, CompactionCheckpoint{
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

func TestJSONLStoreSaveCompactionRejectsUnknownSession(t *testing.T) {
	store := NewJSONLStore(t.TempDir())
	err := store.SaveCompaction(context.Background(), "00000000-0000-7000-8000-000000000000", CompactionCheckpoint{
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

func TestJSONLStoreSaveCompactionRejectsRawMessageCountOvershoot(t *testing.T) {
	ctx := context.Background()
	store := NewJSONLStore(t.TempDir())
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

	err = store.SaveCompaction(ctx, sess.ID, CompactionCheckpoint{
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
	dir := t.TempDir()
	store := NewJSONLStore(dir)
	parentID := "00000000-0000-7000-8000-000000000000"
	sess, err := store.CreateWithOptions(context.Background(), CreateOptions{
		ParentID: parentID,
	})
	if err != nil {
		t.Fatalf("CreateWithOptions returned error: %v", err)
	}
	if sess.ParentID != parentID {
		t.Fatalf("ParentID = %q, want parent id", sess.ParentID)
	}
	if _, err := os.Stat(filepath.Join(dir, parentID, sess.ID+transcriptExt)); err != nil {
		t.Fatalf("stat child transcript: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, indexDir, sess.ID+indexExt)); err != nil {
		t.Fatalf("stat child index: %v", err)
	}
	messages, err := store.Messages(context.Background(), sess.ID)
	if err != nil {
		t.Fatalf("Messages returned error: %v", err)
	}
	if len(messages) != 0 {
		t.Fatalf("messages = %#v, want empty transcript", messages)
	}
}

func TestJSONLStoreHierarchicalChildrenAndIndex(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store := NewJSONLStore(dir)

	root, err := store.Create(ctx)
	if err != nil {
		t.Fatalf("Create root returned error: %v", err)
	}
	child, err := store.CreateWithOptions(ctx, CreateOptions{ParentID: root.ID})
	if err != nil {
		t.Fatalf("Create child returned error: %v", err)
	}
	grandchild, err := store.CreateWithOptions(ctx, CreateOptions{ParentID: child.ID})
	if err != nil {
		t.Fatalf("Create grandchild returned error: %v", err)
	}
	if err := store.Append(ctx, child.ID, model.Message{
		ID:      "child-message",
		Role:    model.RoleUser,
		Content: []model.ContentBlock{{Type: model.ContentText, Text: "child"}},
	}); err != nil {
		t.Fatalf("Append child returned error: %v", err)
	}
	if err := store.Append(ctx, grandchild.ID, model.Message{
		ID:      "grandchild-message",
		Role:    model.RoleUser,
		Content: []model.ContentBlock{{Type: model.ContentText, Text: "grandchild"}},
	}); err != nil {
		t.Fatalf("Append grandchild returned error: %v", err)
	}

	for _, path := range []string{
		filepath.Join(dir, root.ID+transcriptExt),
		filepath.Join(dir, root.ID, child.ID+transcriptExt),
		filepath.Join(dir, root.ID, child.ID, grandchild.ID+transcriptExt),
		filepath.Join(dir, indexDir, root.ID+indexExt),
		filepath.Join(dir, indexDir, child.ID+indexExt),
		filepath.Join(dir, indexDir, grandchild.ID+indexExt),
	} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("stat %s: %v", path, err)
		}
	}

	gotChild, err := store.Get(ctx, child.ID)
	if err != nil {
		t.Fatalf("Get child returned error: %v", err)
	}
	if gotChild.ParentID != root.ID {
		t.Fatalf("child ParentID = %q, want %q", gotChild.ParentID, root.ID)
	}
	childMessages, err := store.Messages(ctx, child.ID)
	if err != nil {
		t.Fatalf("Messages child returned error: %v", err)
	}
	if len(childMessages) != 1 || childMessages[0].PlainText() != "child" {
		t.Fatalf("child messages = %#v, want child message", childMessages)
	}
	gotGrandchild, err := store.Get(ctx, grandchild.ID)
	if err != nil {
		t.Fatalf("Get grandchild returned error: %v", err)
	}
	if gotGrandchild.ParentID != child.ID {
		t.Fatalf("grandchild ParentID = %q, want %q", gotGrandchild.ParentID, child.ID)
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
	if len(children) != 1 || children[0].ID != child.ID {
		t.Fatalf("children = %#v, want child", children)
	}
	grandchildren, err := store.Children(ctx, child.ID)
	if err != nil {
		t.Fatalf("Children child returned error: %v", err)
	}
	if len(grandchildren) != 1 || grandchildren[0].ID != grandchild.ID {
		t.Fatalf("grandchildren = %#v, want grandchild", grandchildren)
	}
	sessions, err := store.List(ctx)
	if err != nil {
		t.Fatalf("List returned error: %v", err)
	}
	if len(sessions) != 3 {
		t.Fatalf("List returned %d sessions, want 3: %#v", len(sessions), sessions)
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
