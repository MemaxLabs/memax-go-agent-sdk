package session

import (
	"context"
	"testing"
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
