package checkpoint

import (
	"context"
	"testing"
	"time"
)

func TestMemoryManagerCreateListRestoreDelete(t *testing.T) {
	manager := NewMemoryManager(nil)
	created, err := manager.Create(context.Background(), CreateOptions{
		SessionID: "session-1",
		Label:     "before edits",
		Metadata:  map[string]any{"files": 2},
	})
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}
	if created.ID != "checkpoint-1" {
		t.Fatalf("ID = %q, want checkpoint-1", created.ID)
	}

	list, err := manager.List(context.Background(), ListOptions{SessionID: "session-1"})
	if err != nil {
		t.Fatalf("List returned error: %v", err)
	}
	if len(list) != 1 || list[0].Label != "before edits" {
		t.Fatalf("List = %#v, want created checkpoint", list)
	}

	restored, err := manager.Restore(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("Restore returned error: %v", err)
	}
	if restored.ID != created.ID {
		t.Fatalf("Restore = %#v, want created checkpoint", restored)
	}

	if err := manager.Delete(context.Background(), created.ID); err != nil {
		t.Fatalf("Delete returned error: %v", err)
	}
	if _, err := manager.Get(context.Background(), created.ID); err == nil {
		t.Fatal("Get returned nil, want missing checkpoint error")
	}
}

func TestMemoryManagerFiltersByParent(t *testing.T) {
	manager := NewMemoryManager([]Checkpoint{
		{ID: "checkpoint-7", SessionID: "session-1", ParentID: "parent-1", CreatedAt: time.Now().Add(-time.Minute)},
		{ID: "checkpoint-8", SessionID: "session-2", ParentID: "parent-2", CreatedAt: time.Now()},
	})

	list, err := manager.List(context.Background(), ListOptions{ParentID: "parent-1"})
	if err != nil {
		t.Fatalf("List returned error: %v", err)
	}
	if len(list) != 1 || list[0].ID != "checkpoint-7" {
		t.Fatalf("List = %#v, want parent-1 checkpoint", list)
	}

	created, err := manager.Create(context.Background(), CreateOptions{})
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}
	if created.ID != "checkpoint-9" {
		t.Fatalf("ID = %q, want checkpoint-9", created.ID)
	}
}

func TestMemoryManagerClonesMetadata(t *testing.T) {
	manager := NewMemoryManager(nil)
	metadata := map[string]any{"key": "original"}
	created, err := manager.Create(context.Background(), CreateOptions{Metadata: metadata})
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}
	metadata["key"] = "changed"

	got, err := manager.Get(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("Get returned error: %v", err)
	}
	if got.Metadata["key"] != "original" {
		t.Fatalf("metadata = %#v, want cloned original", got.Metadata)
	}
	got.Metadata["key"] = "mutated"

	again, err := manager.Get(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("Get returned error: %v", err)
	}
	if again.Metadata["key"] != "original" {
		t.Fatalf("metadata = %#v, want store not mutated", again.Metadata)
	}
}
