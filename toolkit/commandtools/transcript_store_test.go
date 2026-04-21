package commandtools

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/MemaxLabs/memax-go-agent-sdk/identity"
)

func TestMemoryCommandTranscriptStoreReadDefensiveCopiesAndPaging(t *testing.T) {
	store := NewMemoryCommandTranscriptStore()
	finished := time.Date(2026, 4, 20, 12, 0, 0, 0, time.UTC)
	originalFinished := finished
	exitCode := 0
	session := CommandSession{
		ID:        "cmd-1",
		SessionID: "agent-1",
		Identity: identity.Identity{
			Name:        "Build Agent",
			Role:        "tester",
			Constraints: []string{"never skip tests"},
		},
		Argv:       []string{"go", "test"},
		CWD:        "/repo",
		Status:     SessionExited,
		StartedAt:  finished.Add(-time.Minute),
		FinishedAt: &finished,
		ExitCode:   &exitCode,
		NextSeq:    1,
	}
	if err := store.SaveCommandSession(context.Background(), session); err != nil {
		t.Fatalf("SaveCommandSession returned error: %v", err)
	}
	session.Argv[0] = "mutated"
	session.Identity.Constraints[0] = "mutated"
	*session.ExitCode = 99
	*session.FinishedAt = finished.Add(time.Hour)

	chunks := []OutputChunk{
		{Seq: 1, Stream: "stdout", Text: "alpha\n", Time: finished},
		{Seq: 2, Stream: "stderr", Text: "beta\n", Time: finished.Add(time.Second)},
		{Seq: 3, Stream: "stdout", Text: "gamma\n", Time: finished.Add(2 * time.Second)},
	}
	if err := store.AppendCommandOutput(context.Background(), "cmd-1", chunks); err != nil {
		t.Fatalf("AppendCommandOutput returned error: %v", err)
	}
	chunks[0].Text = "mutated\n"

	read, err := store.ReadCommandOutput(context.Background(), ReadRequest{
		ID:        "cmd-1",
		SessionID: "agent-1",
		AfterSeq:  1,
		MaxChunks: 2,
		MaxBytes:  64,
	})
	if err != nil {
		t.Fatalf("ReadCommandOutput returned error: %v", err)
	}
	if read.Session.Argv[0] != "go" || read.Session.Identity.Constraints[0] != "never skip tests" || read.Session.ExitCode == nil || *read.Session.ExitCode != 0 || read.Session.FinishedAt == nil || !read.Session.FinishedAt.Equal(originalFinished) {
		t.Fatalf("read.Session = %#v, want defensive copy of original snapshot", read.Session)
	}
	gotText := joinChunkText(read.Chunks)
	if gotText != "beta\ngamma\n" {
		t.Fatalf("read text = %q, want beta/gamma", gotText)
	}
	if read.NextSeq != 4 {
		t.Fatalf("read.NextSeq = %d, want 4", read.NextSeq)
	}

	staleSession := read.Session
	staleSession.Status = SessionStopped
	staleSession.NextSeq = 1
	if err := store.SaveCommandSession(context.Background(), staleSession); err != nil {
		t.Fatalf("SaveCommandSession stale returned error: %v", err)
	}
	saved, err := store.CommandSession(context.Background(), "cmd-1")
	if err != nil {
		t.Fatalf("CommandSession returned error: %v", err)
	}
	if saved.NextSeq != 4 || saved.Status != SessionStopped {
		t.Fatalf("saved = %#v, want status update without NextSeq regression", saved)
	}

	read.Chunks[0].Text = "mutated\n"
	again, err := store.ReadCommandOutput(context.Background(), ReadRequest{ID: "cmd-1", SessionID: "agent-1"})
	if err != nil {
		t.Fatalf("ReadCommandOutput again returned error: %v", err)
	}
	if joinChunkText(again.Chunks) != "alpha\nbeta\ngamma\n" {
		t.Fatalf("stored chunks changed after caller mutation: %#v", again.Chunks)
	}
}

func TestMemoryCommandTranscriptStoreVisibilityListAndDelete(t *testing.T) {
	store := NewMemoryCommandTranscriptStore()
	now := time.Date(2026, 4, 20, 12, 0, 0, 0, time.UTC)
	sessions := []CommandSession{
		{ID: "cmd-b", SessionID: "agent-1", Status: SessionRunning, StartedAt: now.Add(time.Second), NextSeq: 1},
		{ID: "cmd-a", SessionID: "agent-1", Status: SessionRunning, StartedAt: now, NextSeq: 1},
		{ID: "cmd-c", SessionID: "agent-1", Status: SessionExited, StartedAt: now.Add(2 * time.Second), NextSeq: 1},
		{ID: "cmd-other", SessionID: "agent-2", Status: SessionRunning, StartedAt: now, NextSeq: 1},
	}
	for _, session := range sessions {
		if err := store.SaveCommandSession(context.Background(), session); err != nil {
			t.Fatalf("SaveCommandSession(%s) returned error: %v", session.ID, err)
		}
	}

	listed, err := store.ListCommands(context.Background(), ListRequest{SessionID: "agent-1", Limit: 2})
	if err != nil {
		t.Fatalf("ListCommands returned error: %v", err)
	}
	got := commandSessionIDs(listed)
	want := []string{"cmd-a", "cmd-b"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("listed IDs = %#v, want %#v", got, want)
	}

	all, err := store.ListCommands(context.Background(), ListRequest{SessionID: "agent-1", IncludeCompleted: true})
	if err != nil {
		t.Fatalf("ListCommands all returned error: %v", err)
	}
	got = commandSessionIDs(all)
	want = []string{"cmd-a", "cmd-b", "cmd-c"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("all IDs = %#v, want %#v", got, want)
	}

	unscoped, err := store.ReadCommandOutput(context.Background(), ReadRequest{ID: "cmd-a"})
	if err != nil {
		t.Fatalf("ReadCommandOutput unscoped returned error: %v", err)
	}
	if unscoped.Session.ID != "cmd-a" {
		t.Fatalf("unscoped read session ID = %q, want cmd-a", unscoped.Session.ID)
	}

	_, err = store.ReadCommandOutput(context.Background(), ReadRequest{ID: "cmd-a", SessionID: "agent-2"})
	if !errors.Is(err, ErrCommandSessionNotVisible) {
		t.Fatalf("ReadCommandOutput cross-session error = %v, want visibility error", err)
	}
	if err := store.DeleteCommandSession(context.Background(), "cmd-a"); err != nil {
		t.Fatalf("DeleteCommandSession returned error: %v", err)
	}
	_, err = store.CommandSession(context.Background(), "cmd-a")
	if !errors.Is(err, ErrCommandSessionUnknown) {
		t.Fatalf("CommandSession after delete error = %v, want unknown", err)
	}
	_, err = store.CommandSession(context.Background(), "")
	if err == nil || !strings.Contains(err.Error(), "id is required") {
		t.Fatalf("CommandSession empty ID error = %v, want required ID error", err)
	}
}

func TestMemoryCommandTranscriptStoreAppendValidation(t *testing.T) {
	store := NewMemoryCommandTranscriptStore()
	if err := store.AppendCommandOutput(context.Background(), "missing", []OutputChunk{{Seq: 1, Text: "x"}}); !errors.Is(err, ErrCommandSessionUnknown) {
		t.Fatalf("AppendCommandOutput missing error = %v, want unknown", err)
	}
	session := CommandSession{ID: "cmd-1", SessionID: "agent-1", Status: SessionRunning, StartedAt: time.Now().UTC(), NextSeq: 1}
	if err := store.SaveCommandSession(context.Background(), session); err != nil {
		t.Fatalf("SaveCommandSession returned error: %v", err)
	}
	if err := store.AppendCommandOutput(context.Background(), "cmd-1", []OutputChunk{{Seq: 1, Text: "one"}}); err != nil {
		t.Fatalf("AppendCommandOutput first returned error: %v", err)
	}
	saved, err := store.CommandSession(context.Background(), "cmd-1")
	if err != nil {
		t.Fatalf("CommandSession returned error: %v", err)
	}
	if saved.NextSeq != 2 {
		t.Fatalf("saved.NextSeq = %d, want 2 after first append", saved.NextSeq)
	}
	err = store.AppendCommandOutput(context.Background(), "cmd-1", []OutputChunk{{Seq: 1, Text: "duplicate"}})
	if err == nil || !strings.Contains(err.Error(), "must be greater") {
		t.Fatalf("AppendCommandOutput duplicate error = %v, want monotonic seq error", err)
	}
	err = store.AppendCommandOutput(context.Background(), "cmd-1", []OutputChunk{{Seq: 0, Text: "zero"}})
	if err == nil || !strings.Contains(err.Error(), "must be positive") {
		t.Fatalf("AppendCommandOutput zero seq error = %v, want positive seq error", err)
	}
}

func TestMemoryCommandTranscriptStoreCanceledContextDoesNotMutate(t *testing.T) {
	newStore := func(t *testing.T) *MemoryCommandTranscriptStore {
		t.Helper()
		store := NewMemoryCommandTranscriptStore()
		session := CommandSession{ID: "cmd-1", SessionID: "agent-1", Status: SessionRunning, StartedAt: time.Now().UTC(), NextSeq: 1}
		if err := store.SaveCommandSession(context.Background(), session); err != nil {
			t.Fatalf("SaveCommandSession returned error: %v", err)
		}
		return store
	}
	canceledContext := func() context.Context {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		return ctx
	}

	tests := []struct {
		name string
		run  func(context.Context, *MemoryCommandTranscriptStore) error
	}{
		{
			name: "SaveCommandSession",
			run: func(ctx context.Context, store *MemoryCommandTranscriptStore) error {
				return store.SaveCommandSession(ctx, CommandSession{ID: "cmd-1", SessionID: "agent-1", Status: SessionStopped, StartedAt: time.Now().UTC(), NextSeq: 99})
			},
		},
		{
			name: "AppendCommandOutput",
			run: func(ctx context.Context, store *MemoryCommandTranscriptStore) error {
				return store.AppendCommandOutput(ctx, "cmd-1", []OutputChunk{{Seq: 1, Text: "should not persist"}})
			},
		},
		{
			name: "CommandSession",
			run: func(ctx context.Context, store *MemoryCommandTranscriptStore) error {
				_, err := store.CommandSession(ctx, "cmd-1")
				return err
			},
		},
		{
			name: "ReadCommandOutput",
			run: func(ctx context.Context, store *MemoryCommandTranscriptStore) error {
				_, err := store.ReadCommandOutput(ctx, ReadRequest{ID: "cmd-1", SessionID: "agent-1"})
				return err
			},
		},
		{
			name: "ListCommands",
			run: func(ctx context.Context, store *MemoryCommandTranscriptStore) error {
				_, err := store.ListCommands(ctx, ListRequest{SessionID: "agent-1"})
				return err
			},
		},
		{
			name: "DeleteCommandSession",
			run: func(ctx context.Context, store *MemoryCommandTranscriptStore) error {
				return store.DeleteCommandSession(ctx, "cmd-1")
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newStore(t)
			if err := tt.run(canceledContext(), store); !errors.Is(err, context.Canceled) {
				t.Fatalf("%s canceled error = %v, want context.Canceled", tt.name, err)
			}
			read, err := store.ReadCommandOutput(context.Background(), ReadRequest{ID: "cmd-1", SessionID: "agent-1"})
			if err != nil {
				t.Fatalf("ReadCommandOutput returned error: %v", err)
			}
			if read.Session.Status != SessionRunning || len(read.Chunks) != 0 || read.NextSeq != 1 {
				t.Fatalf("read after canceled %s = %#v, want no mutation", tt.name, read)
			}
		})
	}
}

func commandSessionIDs(sessions []CommandSession) []string {
	ids := make([]string, len(sessions))
	for i, session := range sessions {
		ids[i] = session.ID
	}
	return ids
}
