package messaging

import (
	"context"
	"testing"
	"time"
)

func TestThreadStoreSearchReadSend(t *testing.T) {
	t.Parallel()

	store := NewThreadStore([]Thread{{
		ID:      "thread-1",
		Subject: "Project kickoff follow-up",
		Summary: "Thread about concise action-oriented follow-up emails",
		Participants: []Participant{
			{Name: "Alex", Address: "alex@example.com"},
		},
		Messages: []Message{{
			ID:        "thread-1-msg-1",
			ThreadID:  "thread-1",
			Subject:   "Project kickoff follow-up",
			Body:      "Please keep follow-ups concise and include owners.",
			Direction: DirectionInbound,
			Sender:    Participant{Name: "Alex", Address: "alex@example.com"},
			SentAt:    time.Now().UTC().Add(-time.Hour),
		}},
	}})

	items, err := store.SearchThreads(context.Background(), SearchRequest{
		Query: "concise owners follow-up",
		Limit: 5,
	})
	if err != nil {
		t.Fatalf("SearchThreads() error = %v", err)
	}
	if len(items) != 1 || items[0].ID != "thread-1" {
		t.Fatalf("SearchThreads() = %#v, want kickoff thread", items)
	}

	thread, err := store.ReadThread(context.Background(), ReadRequest{ThreadID: "thread-1"})
	if err != nil {
		t.Fatalf("ReadThread() error = %v", err)
	}
	if len(thread.Messages) != 1 || thread.Messages[0].Body == "" {
		t.Fatalf("ReadThread() = %#v, want full thread content", thread)
	}

	result, err := store.SendMessage(context.Background(), SendRequest{
		ThreadID: "thread-1",
		Body:     "Thanks. I will send concise updates with owners and due dates.",
		Recipients: []Participant{
			{Name: "Alex", Address: "alex@example.com"},
		},
	})
	if err != nil {
		t.Fatalf("SendMessage() error = %v", err)
	}
	if result.CreatedThread {
		t.Fatalf("SendMessage() unexpectedly created thread: %#v", result)
	}
	if result.Message.ID == "" || result.Message.ThreadID != "thread-1" {
		t.Fatalf("SendMessage() message = %#v", result.Message)
	}

	thread, err = store.ReadThread(context.Background(), ReadRequest{ThreadID: "thread-1"})
	if err != nil {
		t.Fatalf("ReadThread() after send error = %v", err)
	}
	if len(thread.Messages) != 2 {
		t.Fatalf("thread messages = %d, want 2", len(thread.Messages))
	}
}

func TestSelectorBreaksTiesByLastMessageAt(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()
	items := []Thread{
		{ID: "thread-1", Subject: "Status update", Summary: "Daily status", LastMessageAt: now.Add(-time.Hour)},
		{ID: "thread-2", Subject: "Status update", Summary: "Daily status", LastMessageAt: now},
	}

	selected := (Selector{MaxThreads: 2}).Select(items, "status update")
	if len(selected) != 2 || selected[0].ID != "thread-2" {
		t.Fatalf("Select() = %#v, want most recent thread first", selected)
	}
}

func TestSendMessageInvalidNewThreadDoesNotMutateStore(t *testing.T) {
	t.Parallel()

	store := NewThreadStore(nil)

	_, err := store.SendMessage(context.Background(), SendRequest{
		Body: "Missing subject should fail before mutating store state.",
		Recipients: []Participant{
			{Name: "Alex", Address: "alex@example.com"},
		},
	})
	if err == nil || err.Error() != "messaging: subject is required when creating a thread" {
		t.Fatalf("SendMessage() error = %v, want missing subject validation", err)
	}

	items, err := store.SearchThreads(context.Background(), SearchRequest{Query: "subject", Limit: 5})
	if err != nil {
		t.Fatalf("SearchThreads() error = %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("SearchThreads() = %#v, want empty store after failed send", items)
	}

	result, err := store.SendMessage(context.Background(), SendRequest{
		Subject: "New follow-up thread",
		Body:    "Now the send is valid.",
		Recipients: []Participant{
			{Name: "Alex", Address: "alex@example.com"},
		},
	})
	if err != nil {
		t.Fatalf("SendMessage() second error = %v", err)
	}
	if result.Thread.ID != "thread-1" {
		t.Fatalf("result thread id = %q, want thread-1 after failed validation left no orphan IDs", result.Thread.ID)
	}
}
