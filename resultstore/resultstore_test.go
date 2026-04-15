package resultstore

import (
	"context"
	"testing"
)

func TestMemoryStoreStoresDefensiveCopies(t *testing.T) {
	store := NewMemoryStore()
	metadata := map[string]any{"kind": "log"}
	handle, err := store.Put(context.Background(), PutRequest{
		SessionID:       "session-1",
		ParentSessionID: "parent-1",
		ToolUseID:       "tool-1",
		ToolName:        "read",
		Content:         "full result",
		Metadata:        metadata,
	})
	if err != nil {
		t.Fatalf("Put returned error: %v", err)
	}
	metadata["kind"] = "mutated"
	handle.Metadata["kind"] = "changed"

	entry, err := store.Get(context.Background(), handle.ID)
	if err != nil {
		t.Fatalf("Get returned error: %v", err)
	}
	if entry.Content != "full result" || entry.SessionID != "session-1" || entry.ParentSessionID != "parent-1" {
		t.Fatalf("entry = %#v, want stored request fields", entry)
	}
	if entry.Metadata["kind"] != "log" {
		t.Fatalf("entry metadata = %#v, want defensive copy", entry.Metadata)
	}

	entry.Metadata["kind"] = "mutated again"
	again, err := store.Get(context.Background(), handle.ID)
	if err != nil {
		t.Fatalf("second Get returned error: %v", err)
	}
	if again.Metadata["kind"] != "log" {
		t.Fatalf("stored metadata changed through returned entry: %#v", again.Metadata)
	}

	list, err := store.List(context.Background())
	if err != nil {
		t.Fatalf("List returned error: %v", err)
	}
	if len(list) != 1 || list[0].ID != handle.ID {
		t.Fatalf("List = %#v, want stored entry", list)
	}
	list[0].Metadata["kind"] = "list mutation"
	afterList, err := store.Get(context.Background(), handle.ID)
	if err != nil {
		t.Fatalf("Get after List returned error: %v", err)
	}
	if afterList.Metadata["kind"] != "log" {
		t.Fatalf("List returned mutable metadata: %#v", afterList.Metadata)
	}
	if store.Len() != 1 {
		t.Fatalf("Len = %d, want 1", store.Len())
	}
}

func TestStoreFuncRejectsNilFunction(t *testing.T) {
	_, err := (StoreFunc(nil)).Put(context.Background(), PutRequest{})
	if err == nil {
		t.Fatal("Put returned nil, want error")
	}
}
