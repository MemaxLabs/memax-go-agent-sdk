package sqlitestore

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MemaxLabs/memax-go-agent-sdk/identity"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/commandtools"
	_ "modernc.org/sqlite"
)

func TestNewNilDB(t *testing.T) {
	t.Parallel()

	if _, err := New(context.Background(), nil); err == nil {
		t.Fatal("New(nil) error = nil, want error")
	}
}

func TestStoreReadDefensiveCopiesPagingAndPersistence(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "command-transcripts.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	store, err := New(context.Background(), db)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	finished := time.Date(2026, 4, 21, 12, 0, 0, 0, time.UTC)
	originalFinished := finished
	exitCode := 0
	session := commandtools.CommandSession{
		ID:                 "cmd-1",
		SessionID:          "agent-1",
		ParentSessionID:    "parent-1",
		Identity:           identity.Identity{Name: "Build Agent", Role: "tester", Constraints: []string{"never skip tests"}},
		Argv:               []string{"go", "test", "./..."},
		CWD:                "/repo",
		Purpose:            "verify",
		Status:             commandtools.SessionExited,
		PID:                1234,
		TTY:                true,
		Cols:               120,
		Rows:               40,
		SignalsProcessTree: true,
		StartedAt:          finished.Add(-time.Minute),
		FinishedAt:         &finished,
		ExitCode:           &exitCode,
		NextSeq:            1,
		DroppedChunks:      2,
		DroppedBytes:       64,
	}
	if err := store.SaveCommandSession(context.Background(), session); err != nil {
		t.Fatalf("SaveCommandSession() error = %v", err)
	}
	session.Argv[0] = "mutated"
	session.Identity.Constraints[0] = "mutated"
	*session.ExitCode = 99
	*session.FinishedAt = finished.Add(time.Hour)

	chunks := []commandtools.OutputChunk{
		{Seq: 1, Stream: "stdout", Text: "alpha\n", Time: finished},
		{Seq: 2, Stream: "stderr", Text: "beta\n", Time: finished.Add(time.Second)},
		{Seq: 3, Stream: "stdout", Text: "gamma\n", Time: finished.Add(2 * time.Second)},
	}
	if err := store.AppendCommandOutput(context.Background(), "cmd-1", chunks); err != nil {
		t.Fatalf("AppendCommandOutput() error = %v", err)
	}
	chunks[0].Text = "mutated\n"

	read, err := store.ReadCommandOutput(context.Background(), commandtools.ReadRequest{
		ID:        "cmd-1",
		SessionID: "agent-1",
		AfterSeq:  1,
		MaxChunks: 2,
		MaxBytes:  64,
	})
	if err != nil {
		t.Fatalf("ReadCommandOutput() error = %v", err)
	}
	if got := joinChunkText(read.Chunks); got != "beta\ngamma\n" {
		t.Fatalf("joinChunkText() = %q, want beta/gamma", got)
	}
	if read.NextSeq != 4 {
		t.Fatalf("read.NextSeq = %d, want 4", read.NextSeq)
	}
	if read.Session.Argv[0] != "go" || read.Session.Identity.Constraints[0] != "never skip tests" || read.Session.ExitCode == nil || *read.Session.ExitCode != 0 || read.Session.FinishedAt == nil || !read.Session.FinishedAt.Equal(originalFinished) {
		t.Fatalf("read.Session = %#v, want defensive copy of original snapshot", read.Session)
	}

	stale := read.Session
	stale.Status = commandtools.SessionStopped
	stale.NextSeq = 1
	if err := store.SaveCommandSession(context.Background(), stale); err != nil {
		t.Fatalf("SaveCommandSession(stale) error = %v", err)
	}
	saved, err := store.CommandSession(context.Background(), "cmd-1")
	if err != nil {
		t.Fatalf("CommandSession() error = %v", err)
	}
	if saved.NextSeq != 4 || saved.Status != commandtools.SessionStopped {
		t.Fatalf("saved = %#v, want status update without NextSeq regression", saved)
	}

	read.Chunks[0].Text = "mutated\n"
	again, err := store.ReadCommandOutput(context.Background(), commandtools.ReadRequest{ID: "cmd-1", SessionID: "agent-1"})
	if err != nil {
		t.Fatalf("ReadCommandOutput(again) error = %v", err)
	}
	if got := joinChunkText(again.Chunks); got != "alpha\nbeta\ngamma\n" {
		t.Fatalf("stored chunks changed after caller mutation: %#v", again.Chunks)
	}

	if err := db.Close(); err != nil {
		t.Fatalf("db.Close() error = %v", err)
	}
	reopenedDB, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("sql.Open(reopen) error = %v", err)
	}
	defer reopenedDB.Close()
	reopened, err := New(context.Background(), reopenedDB)
	if err != nil {
		t.Fatalf("New(reopen) error = %v", err)
	}
	persisted, err := reopened.CommandSession(context.Background(), "cmd-1")
	if err != nil {
		t.Fatalf("CommandSession(reopen) error = %v", err)
	}
	if !reflect.DeepEqual(persisted, saved) {
		t.Fatalf("persisted session = %#v, want %#v", persisted, saved)
	}
	persistedRead, err := reopened.ReadCommandOutput(context.Background(), commandtools.ReadRequest{ID: "cmd-1", SessionID: "agent-1"})
	if err != nil {
		t.Fatalf("ReadCommandOutput(reopen) error = %v", err)
	}
	if got := joinChunkText(persistedRead.Chunks); got != "alpha\nbeta\ngamma\n" {
		t.Fatalf("persisted chunk text = %q, want full transcript", got)
	}
}

func TestStoreVisibilityListAndDelete(t *testing.T) {
	t.Parallel()

	store := newTestStore(t)
	now := time.Date(2026, 4, 21, 12, 0, 0, 0, time.UTC)
	sessions := []commandtools.CommandSession{
		{ID: "cmd-b", SessionID: "agent-1", Status: commandtools.SessionRunning, StartedAt: now.Add(time.Second), NextSeq: 1},
		{ID: "cmd-a", SessionID: "agent-1", Status: commandtools.SessionRunning, StartedAt: now, NextSeq: 1},
		{ID: "cmd-c", SessionID: "agent-1", Status: commandtools.SessionExited, StartedAt: now.Add(2 * time.Second), NextSeq: 1},
		{ID: "cmd-other", SessionID: "agent-2", Status: commandtools.SessionRunning, StartedAt: now, NextSeq: 1},
		{ID: "cmd-empty", SessionID: "", Status: commandtools.SessionRunning, StartedAt: now.Add(3 * time.Second), NextSeq: 1},
	}
	for _, session := range sessions {
		if err := store.SaveCommandSession(context.Background(), session); err != nil {
			t.Fatalf("SaveCommandSession(%s) error = %v", session.ID, err)
		}
	}

	listed, err := store.ListCommands(context.Background(), commandtools.ListRequest{SessionID: "agent-1", Limit: 2})
	if err != nil {
		t.Fatalf("ListCommands() error = %v", err)
	}
	if got, want := commandSessionIDs(listed), []string{"cmd-a", "cmd-b"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("listed IDs = %#v, want %#v", got, want)
	}

	all, err := store.ListCommands(context.Background(), commandtools.ListRequest{SessionID: "agent-1", IncludeCompleted: true})
	if err != nil {
		t.Fatalf("ListCommands(all) error = %v", err)
	}
	if got, want := commandSessionIDs(all), []string{"cmd-a", "cmd-b", "cmd-c"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("all IDs = %#v, want %#v", got, want)
	}

	unscoped, err := store.ReadCommandOutput(context.Background(), commandtools.ReadRequest{ID: "cmd-a"})
	if err != nil {
		t.Fatalf("ReadCommandOutput(unscoped) error = %v", err)
	}
	if unscoped.Session.ID != "cmd-a" {
		t.Fatalf("unscoped read session ID = %q, want cmd-a", unscoped.Session.ID)
	}

	if _, err := store.ReadCommandOutput(context.Background(), commandtools.ReadRequest{ID: "cmd-a", SessionID: "agent-2"}); !errors.Is(err, commandtools.ErrCommandSessionNotVisible) {
		t.Fatalf("ReadCommandOutput(cross-session) error = %v, want visibility error", err)
	}
	emptyScoped, err := store.ReadCommandOutput(context.Background(), commandtools.ReadRequest{ID: "cmd-empty", SessionID: "agent-1"})
	if err != nil {
		t.Fatalf("ReadCommandOutput(empty scoped) error = %v", err)
	}
	if emptyScoped.Session.ID != "cmd-empty" {
		t.Fatalf("empty scoped read session ID = %q, want cmd-empty", emptyScoped.Session.ID)
	}

	if err := store.DeleteCommandSession(context.Background(), "cmd-a"); err != nil {
		t.Fatalf("DeleteCommandSession() error = %v", err)
	}
	if _, err := store.CommandSession(context.Background(), "cmd-a"); !errors.Is(err, commandtools.ErrCommandSessionUnknown) {
		t.Fatalf("CommandSession(after delete) error = %v, want unknown", err)
	}
	if _, err := store.CommandSession(context.Background(), ""); err == nil || !strings.Contains(err.Error(), "id is required") {
		t.Fatalf("CommandSession(empty ID) error = %v, want required ID error", err)
	}
}

func TestStoreAppendValidation(t *testing.T) {
	t.Parallel()

	store := newTestStore(t)
	if err := store.AppendCommandOutput(context.Background(), "missing", []commandtools.OutputChunk{{Seq: 1, Text: "x"}}); !errors.Is(err, commandtools.ErrCommandSessionUnknown) {
		t.Fatalf("AppendCommandOutput(missing) error = %v, want unknown", err)
	}
	session := commandtools.CommandSession{ID: "cmd-1", SessionID: "agent-1", Status: commandtools.SessionRunning, StartedAt: time.Now().UTC(), NextSeq: 1}
	if err := store.SaveCommandSession(context.Background(), session); err != nil {
		t.Fatalf("SaveCommandSession() error = %v", err)
	}
	if err := store.AppendCommandOutput(context.Background(), "cmd-1", []commandtools.OutputChunk{{Seq: 1, Text: "one"}}); err != nil {
		t.Fatalf("AppendCommandOutput(first) error = %v", err)
	}
	saved, err := store.CommandSession(context.Background(), "cmd-1")
	if err != nil {
		t.Fatalf("CommandSession() error = %v", err)
	}
	if saved.NextSeq != 2 {
		t.Fatalf("saved.NextSeq = %d, want 2 after first append", saved.NextSeq)
	}
	if err := store.AppendCommandOutput(context.Background(), "cmd-1", []commandtools.OutputChunk{{Seq: 1, Text: "duplicate"}}); err == nil || !strings.Contains(err.Error(), "must be greater") {
		t.Fatalf("AppendCommandOutput(duplicate) error = %v, want monotonic seq error", err)
	}
	if err := store.AppendCommandOutput(context.Background(), "cmd-1", []commandtools.OutputChunk{{Seq: 0, Text: "zero"}}); err == nil || !strings.Contains(err.Error(), "must be positive") {
		t.Fatalf("AppendCommandOutput(zero) error = %v, want positive seq error", err)
	}
}

func TestStoreCanceledContextDoesNotMutate(t *testing.T) {
	t.Parallel()

	newStore := func(t *testing.T) *Store {
		t.Helper()
		store := newTestStore(t)
		session := commandtools.CommandSession{ID: "cmd-1", SessionID: "agent-1", Status: commandtools.SessionRunning, StartedAt: time.Now().UTC(), NextSeq: 1}
		if err := store.SaveCommandSession(context.Background(), session); err != nil {
			t.Fatalf("SaveCommandSession() error = %v", err)
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
		run  func(context.Context, *Store) error
	}{
		{
			name: "SaveCommandSession",
			run: func(ctx context.Context, store *Store) error {
				return store.SaveCommandSession(ctx, commandtools.CommandSession{ID: "cmd-1", SessionID: "agent-1", Status: commandtools.SessionStopped, StartedAt: time.Now().UTC(), NextSeq: 99})
			},
		},
		{
			name: "AppendCommandOutput",
			run: func(ctx context.Context, store *Store) error {
				return store.AppendCommandOutput(ctx, "cmd-1", []commandtools.OutputChunk{{Seq: 1, Text: "should not persist"}})
			},
		},
		{
			name: "CommandSession",
			run: func(ctx context.Context, store *Store) error {
				_, err := store.CommandSession(ctx, "cmd-1")
				return err
			},
		},
		{
			name: "ReadCommandOutput",
			run: func(ctx context.Context, store *Store) error {
				_, err := store.ReadCommandOutput(ctx, commandtools.ReadRequest{ID: "cmd-1", SessionID: "agent-1"})
				return err
			},
		},
		{
			name: "ListCommands",
			run: func(ctx context.Context, store *Store) error {
				_, err := store.ListCommands(ctx, commandtools.ListRequest{SessionID: "agent-1"})
				return err
			},
		},
		{
			name: "DeleteCommandSession",
			run: func(ctx context.Context, store *Store) error {
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
			read, err := store.ReadCommandOutput(context.Background(), commandtools.ReadRequest{ID: "cmd-1", SessionID: "agent-1"})
			if err != nil {
				t.Fatalf("ReadCommandOutput() error = %v", err)
			}
			if read.Session.Status != commandtools.SessionRunning || len(read.Chunks) != 0 || read.NextSeq != 1 {
				t.Fatalf("read after canceled %s = %#v, want no mutation", tt.name, read)
			}
		})
	}
}

func TestStoreAppendIsAtomic(t *testing.T) {
	t.Parallel()

	store := newTestStore(t)
	session := commandtools.CommandSession{ID: "cmd-1", SessionID: "agent-1", Status: commandtools.SessionRunning, StartedAt: time.Now().UTC(), NextSeq: 1}
	if err := store.SaveCommandSession(context.Background(), session); err != nil {
		t.Fatalf("SaveCommandSession() error = %v", err)
	}

	const goroutines = 16
	results := make(chan error, goroutines)
	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- store.AppendCommandOutput(context.Background(), "cmd-1", []commandtools.OutputChunk{{Seq: 1, Text: "only once"}})
		}()
	}
	wg.Wait()
	close(results)

	successes := 0
	for err := range results {
		if err == nil {
			successes++
			continue
		}
		if !strings.Contains(err.Error(), "must be greater") {
			t.Fatalf("AppendCommandOutput() error = %v, want monotonic seq error", err)
		}
	}
	if successes != 1 {
		t.Fatalf("append successes = %d, want 1", successes)
	}
	read, err := store.ReadCommandOutput(context.Background(), commandtools.ReadRequest{ID: "cmd-1", SessionID: "agent-1"})
	if err != nil {
		t.Fatalf("ReadCommandOutput() error = %v", err)
	}
	if len(read.Chunks) != 1 || read.NextSeq != 2 {
		t.Fatalf("read after concurrent append = %#v, want one chunk and NextSeq 2", read)
	}
}

func newTestStore(t *testing.T) *Store {
	t.Helper()

	db, err := sql.Open("sqlite", "file:"+t.Name()+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	t.Cleanup(func() {
		_ = db.Close()
	})
	store, err := New(context.Background(), db)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return store
}

func commandSessionIDs(sessions []commandtools.CommandSession) []string {
	ids := make([]string, len(sessions))
	for i, session := range sessions {
		ids[i] = session.ID
	}
	return ids
}

func joinChunkText(chunks []commandtools.OutputChunk) string {
	var b strings.Builder
	for _, chunk := range chunks {
		b.WriteString(chunk.Text)
	}
	return b.String()
}
