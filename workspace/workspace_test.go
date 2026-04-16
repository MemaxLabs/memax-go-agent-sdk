package workspace

import (
	"context"
	"strings"
	"testing"
)

func TestMemoryStorePatchDiffCheckpointRestore(t *testing.T) {
	store := NewMemoryStore(map[string]string{
		"README.md": "hello",
		"old.txt":   "remove me",
	})
	created, err := store.Checkpoint(context.Background(), CheckpointOptions{Label: "before"})
	if err != nil {
		t.Fatalf("Checkpoint returned error: %v", err)
	}
	result, err := store.ApplyPatch(context.Background(), []PatchOperation{
		{Path: "README.md", OldContent: StringPtr("hello"), NewContent: StringPtr("hello world")},
		{Path: "docs/new.md", NewContent: StringPtr("new")},
		{Path: "old.txt", OldContent: StringPtr("remove me")},
	})
	if err != nil {
		t.Fatalf("ApplyPatch returned error: %v", err)
	}
	if len(result.Changes) != 3 {
		t.Fatalf("changes = %#v, want 3", result.Changes)
	}
	diff, err := store.Diff(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("Diff returned error: %v", err)
	}
	if got := summarizeChanges(diff.Changes); got != "README.md:modified,docs/new.md:added,old.txt:deleted" {
		t.Fatalf("diff = %s", got)
	}
	if _, err := store.Restore(context.Background(), created.ID); err != nil {
		t.Fatalf("Restore returned error: %v", err)
	}
	content, err := store.ReadFile(context.Background(), "README.md")
	if err != nil {
		t.Fatalf("ReadFile returned error: %v", err)
	}
	if content != "hello" {
		t.Fatalf("README.md = %q, want restored content", content)
	}
	if _, err := store.ReadFile(context.Background(), "docs/new.md"); err == nil {
		t.Fatal("ReadFile docs/new.md returned nil, want restored deletion")
	}
}

func TestMemoryStorePatchIsAtomicOnGuardFailure(t *testing.T) {
	store := NewMemoryStore(map[string]string{"README.md": "hello"})
	if _, err := store.ApplyPatch(context.Background(), []PatchOperation{
		{Path: "README.md", OldContent: StringPtr("wrong"), NewContent: StringPtr("changed")},
	}); err == nil {
		t.Fatal("ApplyPatch returned nil, want guard error")
	}
	content, err := store.ReadFile(context.Background(), "README.md")
	if err != nil {
		t.Fatalf("ReadFile returned error: %v", err)
	}
	if content != "hello" {
		t.Fatalf("content = %q, want unchanged", content)
	}
}

func TestMemoryStoreListAndCheckpointCopies(t *testing.T) {
	store := NewMemoryStore(map[string]string{
		"b.txt":      "b",
		"docs/a.txt": "a",
	})
	files, err := store.ListFiles(context.Background(), "")
	if err != nil {
		t.Fatalf("ListFiles returned error: %v", err)
	}
	if strings.Join(files, "\n") != "b.txt\ndocs/a.txt" {
		t.Fatalf("files = %#v", files)
	}
	checkpoints, err := store.ListCheckpoints(context.Background())
	if err != nil {
		t.Fatalf("ListCheckpoints returned error: %v", err)
	}
	if len(checkpoints) != 1 || checkpoints[0].ID != "checkpoint-0" {
		t.Fatalf("checkpoints = %#v, want initial checkpoint", checkpoints)
	}
	checkpoints[0].Metadata = map[string]any{"mutated": true}
	again, err := store.ListCheckpoints(context.Background())
	if err != nil {
		t.Fatalf("ListCheckpoints again returned error: %v", err)
	}
	if again[0].Metadata != nil {
		t.Fatalf("metadata = %#v, want defensive copy", again[0].Metadata)
	}
}

func summarizeChanges(changes []Change) string {
	parts := make([]string, len(changes))
	for i, change := range changes {
		parts[i] = change.Path + ":" + string(change.Kind)
	}
	return strings.Join(parts, ",")
}
