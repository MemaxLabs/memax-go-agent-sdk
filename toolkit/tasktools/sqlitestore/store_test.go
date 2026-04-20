package sqlitestore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/tasktools"
	_ "modernc.org/sqlite"
)

func TestNewNilDB(t *testing.T) {
	t.Parallel()

	if _, err := New(context.Background(), nil); err == nil {
		t.Fatal("New(nil) error = nil, want error")
	}
}

func TestStoreCreateListUpdateDelete(t *testing.T) {
	t.Parallel()

	store := newTestStore(t)
	empty, err := store.List(context.Background())
	if err != nil {
		t.Fatalf("List(empty) error = %v", err)
	}
	if empty == nil || len(empty) != 0 {
		t.Fatalf("List(empty) = %#v, want non-nil empty slice", empty)
	}

	first, err := store.Upsert(context.Background(), tasktools.Task{
		Title:    " inspect session API ",
		Status:   tasktools.StatusInProgress,
		Notes:    " cover persistence ",
		Priority: 2,
		Evidence: []string{" README.md ", "", "docs/architecture.md"},
	})
	if err != nil {
		t.Fatalf("Upsert(first) error = %v", err)
	}
	if first.ID != "task-1" || first.Title != "inspect session API" || first.Notes != "cover persistence" {
		t.Fatalf("Upsert(first) = %#v, want normalized generated task", first)
	}
	if len(first.Evidence) != 2 || first.Evidence[0] != "README.md" || first.Evidence[1] != "docs/architecture.md" {
		t.Fatalf("Upsert(first).Evidence = %#v, want trimmed evidence", first.Evidence)
	}

	second, err := store.Upsert(context.Background(), tasktools.Task{
		ID:     "task-7",
		Title:  "write tests",
		Status: tasktools.StatusPending,
	})
	if err != nil {
		t.Fatalf("Upsert(second) error = %v", err)
	}
	if second.ID != "task-7" {
		t.Fatalf("Upsert(second).ID = %q, want explicit id", second.ID)
	}
	third, err := store.Upsert(context.Background(), tasktools.Task{Title: "generated after explicit"})
	if err != nil {
		t.Fatalf("Upsert(third) error = %v", err)
	}
	if third.ID != "task-8" {
		t.Fatalf("Upsert(third).ID = %q, want task-8 after explicit task-7", third.ID)
	}

	updated, err := store.Upsert(context.Background(), tasktools.Task{
		ID:       "task-1",
		Status:   tasktools.StatusCompleted,
		Notes:    "done",
		Evidence: []string{"verified"},
	})
	if err != nil {
		t.Fatalf("Upsert(update) error = %v", err)
	}
	if updated.Title != first.Title || updated.Priority != first.Priority {
		t.Fatalf("Upsert(update) = %#v, want preserved title and priority", updated)
	}
	if updated.Status != tasktools.StatusCompleted || updated.Notes != "done" || len(updated.Evidence) != 1 || updated.Evidence[0] != "verified" {
		t.Fatalf("Upsert(update) = %#v, want merged update", updated)
	}

	tasks, err := store.List(context.Background())
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	gotIDs := taskIDs(tasks)
	wantIDs := []string{"task-1", "task-7", "task-8"}
	if !equalStrings(gotIDs, wantIDs) {
		t.Fatalf("List IDs = %#v, want insertion order %#v", gotIDs, wantIDs)
	}

	tasks[0].Evidence[0] = "mutated"
	tasks, err = store.List(context.Background())
	if err != nil {
		t.Fatalf("List(after mutation) error = %v", err)
	}
	if tasks[0].Evidence[0] != "verified" {
		t.Fatalf("List returned aliased evidence: %#v", tasks[0].Evidence)
	}

	if err := store.Delete(context.Background(), "task-7"); err != nil {
		t.Fatalf("Delete(task-7) error = %v", err)
	}
	tasks, err = store.List(context.Background())
	if err != nil {
		t.Fatalf("List(after delete) error = %v", err)
	}
	if gotIDs := taskIDs(tasks); !equalStrings(gotIDs, []string{"task-1", "task-8"}) {
		t.Fatalf("List after delete IDs = %#v, want task-1 and task-8", gotIDs)
	}
	if err := store.Delete(context.Background(), "task-7"); err == nil || !strings.Contains(err.Error(), "task not found") {
		t.Fatalf("Delete(missing) error = %v, want task not found", err)
	}
}

func TestStoreRejectsInvalidInputs(t *testing.T) {
	t.Parallel()

	store := newTestStore(t)
	if _, err := store.Upsert(context.Background(), tasktools.Task{Status: tasktools.StatusPending}); err == nil || !strings.Contains(err.Error(), "task title is required") {
		t.Fatalf("Upsert(empty title) error = %v, want title error", err)
	}
	if _, err := store.Upsert(context.Background(), tasktools.Task{Title: "bad", Status: "unknown"}); err == nil || !strings.Contains(err.Error(), "invalid task status") {
		t.Fatalf("Upsert(invalid status) error = %v, want status error", err)
	}
	if err := store.Delete(context.Background(), " "); err == nil || !strings.Contains(err.Error(), "task id is required") {
		t.Fatalf("Delete(empty id) error = %v, want id error", err)
	}
}

func TestStorePersistsAcrossInstances(t *testing.T) {
	t.Parallel()

	db := newTestDB(t)
	first, err := New(context.Background(), db)
	if err != nil {
		t.Fatalf("New(first) error = %v", err)
	}
	if _, err := first.Upsert(context.Background(), tasktools.Task{
		Title:    "persist task ledger",
		Status:   tasktools.StatusInProgress,
		Evidence: []string{"eval"},
	}); err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}

	second, err := New(context.Background(), db)
	if err != nil {
		t.Fatalf("New(second) error = %v", err)
	}
	tasks, err := second.List(context.Background())
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(tasks) != 1 || tasks[0].Title != "persist task ledger" || tasks[0].Evidence[0] != "eval" {
		t.Fatalf("List() = %#v, want durable task from first store", tasks)
	}

	var version int
	if err := db.QueryRowContext(context.Background(), `SELECT value FROM memax_task_meta WHERE name = ?`, schemaVersionKey).Scan(&version); err != nil {
		t.Fatalf("query schema version error = %v", err)
	}
	if version != schemaVersion {
		t.Fatalf("schema version = %d, want %d", version, schemaVersion)
	}
}

func TestStoreGeneratedIDsAreAtomic(t *testing.T) {
	t.Parallel()

	store := newTestStore(t)
	const goroutines = 16
	var (
		wg  sync.WaitGroup
		mu  sync.Mutex
		ids []string
	)
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			task, err := store.Upsert(context.Background(), tasktools.Task{Title: fmt.Sprintf("task %02d", i)})
			if err != nil {
				t.Errorf("Upsert(%d) error = %v", i, err)
				return
			}
			mu.Lock()
			ids = append(ids, task.ID)
			mu.Unlock()
		}(i)
	}
	wg.Wait()

	if len(ids) != goroutines {
		t.Fatalf("created ids = %#v, want %d ids", ids, goroutines)
	}
	gotIDs := make(map[string]bool, len(ids))
	for _, id := range ids {
		gotIDs[id] = true
	}
	for i := 1; i <= goroutines; i++ {
		want := fmt.Sprintf("task-%d", i)
		if !gotIDs[want] {
			sort.Strings(ids)
			t.Fatalf("missing generated id %q; all ids=%#v", want, ids)
		}
	}
	tasks, err := store.List(context.Background())
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(tasks) != goroutines {
		t.Fatalf("List() length = %d, want %d", len(tasks), goroutines)
	}
}

func TestStoreHonorsContextCancellation(t *testing.T) {
	t.Parallel()

	store := newTestStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.List(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("List(canceled) error = %v, want context.Canceled", err)
	}
	if _, err := store.Upsert(ctx, tasktools.Task{Title: "canceled"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("Upsert(canceled) error = %v, want context.Canceled", err)
	}
	if err := store.Delete(ctx, "task-1"); !errors.Is(err, context.Canceled) {
		t.Fatalf("Delete(canceled) error = %v, want context.Canceled", err)
	}
}

func TestStoreAcceptsNilContext(t *testing.T) {
	t.Parallel()

	store := newTestStore(t)
	task, err := store.Upsert(nil, tasktools.Task{Title: "nil context"})
	if err != nil {
		t.Fatalf("Upsert(nil ctx) error = %v", err)
	}
	if _, err := store.List(nil); err != nil {
		t.Fatalf("List(nil ctx) error = %v", err)
	}
	if err := store.Delete(nil, task.ID); err != nil {
		t.Fatalf("Delete(nil ctx) error = %v", err)
	}
}

func newTestStore(t *testing.T) *Store {
	t.Helper()

	store, err := New(context.Background(), newTestDB(t))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return store
}

func newTestDB(t *testing.T) *sql.DB {
	t.Helper()

	name := strings.NewReplacer("/", "_", " ", "_").Replace(t.Name())
	db, err := sql.Open("sqlite", "file:"+name+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	t.Cleanup(func() {
		_ = db.Close()
	})
	return db
}

func taskIDs(tasks []tasktools.Task) []string {
	ids := make([]string, 0, len(tasks))
	for _, task := range tasks {
		ids = append(ids, task.ID)
	}
	return ids
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func ExampleNew() {
	db, err := sql.Open("sqlite", "file:example-task-ledger?mode=memory&cache=shared")
	if err != nil {
		panic(err)
	}
	defer db.Close()

	store, err := New(context.Background(), db)
	if err != nil {
		panic(err)
	}
	task, err := store.Upsert(context.Background(), tasktools.Task{
		Title:  "write durable task store tests",
		Status: tasktools.StatusInProgress,
	})
	if err != nil {
		panic(err)
	}
	fmt.Println(task.ID, task.Status)
	// Output: task-1 in_progress
}
