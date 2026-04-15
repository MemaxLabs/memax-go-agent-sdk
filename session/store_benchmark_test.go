package session

import (
	"context"
	"fmt"
	"testing"

	"github.com/MemaxLabs/memax-go-agent-sdk/model"
)

func BenchmarkMemoryStoreAppendAndRead(b *testing.B) {
	ctx := context.Background()

	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		store := NewMemoryStore()
		sess, err := store.Create(ctx)
		if err != nil {
			b.Fatalf("Create returned error: %v", err)
		}
		for j := 0; j < 1000; j++ {
			if err := store.Append(ctx, sess.ID, model.Message{
				Role: model.RoleUser,
				Content: []model.ContentBlock{
					{Type: model.ContentText, Text: fmt.Sprintf("message %d", j)},
				},
			}); err != nil {
				b.Fatalf("Append returned error: %v", err)
			}
		}
		messages, err := store.Messages(ctx, sess.ID)
		if err != nil {
			b.Fatalf("Messages returned error: %v", err)
		}
		if len(messages) != 1000 {
			b.Fatalf("got %d messages, want 1000", len(messages))
		}
	}
}

func BenchmarkMemoryStoreForkLongSession(b *testing.B) {
	ctx := context.Background()
	store := NewMemoryStore()
	sess, err := store.Create(ctx)
	if err != nil {
		b.Fatalf("Create returned error: %v", err)
	}
	for i := 0; i < 5000; i++ {
		if err := store.Append(ctx, sess.ID, model.Message{
			ID:   fmt.Sprintf("m-%d", i),
			Role: model.RoleUser,
			Content: []model.ContentBlock{
				{Type: model.ContentText, Text: "benchmark message"},
			},
		}); err != nil {
			b.Fatalf("Append returned error: %v", err)
		}
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		forked, err := store.Fork(ctx, sess.ID, ForkOptions{ThroughMessageID: "m-2499"})
		if err != nil {
			b.Fatalf("Fork returned error: %v", err)
		}
		messages, err := store.Messages(ctx, forked.ID)
		if err != nil {
			b.Fatalf("Messages returned error: %v", err)
		}
		if len(messages) != 2500 {
			b.Fatalf("got %d messages, want 2500", len(messages))
		}
	}
}
