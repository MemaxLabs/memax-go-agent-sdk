package notes

import (
	"context"
	"testing"
	"time"
)

func TestNoteStoreSearchReadWriteDelete(t *testing.T) {
	t.Parallel()

	store := NewNoteStore([]Note{{
		ID:      "note-1",
		Title:   "meeting style",
		Kind:    "preference",
		Summary: "Keep follow-ups action-oriented",
		Content: "Meeting follow-ups should list owners and due dates.",
		Tags:    []string{"meeting", "follow-up"},
	}})

	items, err := store.SearchNotes(context.Background(), SearchRequest{Query: "owners due dates", Limit: 5})
	if err != nil {
		t.Fatalf("SearchNotes() error = %v", err)
	}
	if len(items) != 1 || items[0].ID != "note-1" {
		t.Fatalf("SearchNotes() = %#v, want meeting-style note", items)
	}

	item, err := store.ReadNote(context.Background(), ReadRequest{Title: "meeting style"})
	if err != nil {
		t.Fatalf("ReadNote() error = %v", err)
	}
	if item.Content != "Meeting follow-ups should list owners and due dates." {
		t.Fatalf("ReadNote() content = %q", item.Content)
	}

	result, err := store.PutNote(context.Background(), PutRequest{Note: Note{
		Title:   "travel packing",
		Kind:    "checklist",
		Summary: "Reusable trip packing checklist",
		Content: "Passport, charger, and medication.",
	}})
	if err != nil {
		t.Fatalf("PutNote() error = %v", err)
	}
	if !result.Created || result.Note.ID == "" {
		t.Fatalf("PutNote() result = %#v, want created note with id", result)
	}
	if result.Note.CreatedAt.IsZero() || result.Note.UpdatedAt.IsZero() {
		t.Fatalf("PutNote() timestamps = created %v updated %v, want both set", result.Note.CreatedAt, result.Note.UpdatedAt)
	}

	if err := store.DeleteNote(context.Background(), DeleteRequest{ID: result.Note.ID}); err != nil {
		t.Fatalf("DeleteNote() error = %v", err)
	}
	if _, err := store.ReadNote(context.Background(), ReadRequest{ID: result.Note.ID}); err == nil {
		t.Fatal("ReadNote() after delete returned nil error")
	}
}

func TestSelectorMetadataFirstRanking(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()
	items := []Note{
		{
			ID:        "note-1",
			Title:     "meeting checklist",
			Summary:   "Checklist for follow-up hygiene",
			Content:   "Owners and due dates",
			Tags:      []string{"meeting", "checklist"},
			UpdatedAt: now,
		},
		{
			ID:        "note-2",
			Title:     "groceries",
			Summary:   "Weekly grocery list",
			Content:   "Milk and eggs",
			UpdatedAt: now.Add(-time.Hour),
		},
	}

	selected := (Selector{MaxNotes: 1}).Select(items, "meeting checklist")
	if len(selected) != 1 || selected[0].ID != "note-1" {
		t.Fatalf("Select() = %#v, want meeting note first", selected)
	}
}

func TestSelectorBreaksTiesByUpdatedAt(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()
	items := []Note{
		{ID: "note-1", Title: "daily brief", Summary: "Status note", Content: "Same relevance", UpdatedAt: now.Add(-time.Hour)},
		{ID: "note-2", Title: "daily brief", Summary: "Status note", Content: "Same relevance", UpdatedAt: now},
	}

	selected := (Selector{MaxNotes: 2}).Select(items, "daily brief")
	if len(selected) != 2 || selected[0].ID != "note-2" {
		t.Fatalf("Select() = %#v, want most recently updated note first", selected)
	}
}
