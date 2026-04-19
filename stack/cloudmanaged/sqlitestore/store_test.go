package sqlitestore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/MemaxLabs/memax-go-agent-sdk/stack/cloudmanaged"
	"github.com/MemaxLabs/memax-go-agent-sdk/tenant"
	_ "modernc.org/sqlite"
)

func TestNewNilDB(t *testing.T) {
	t.Parallel()

	if _, err := New(context.Background(), nil); err == nil {
		t.Fatal("New(nil) error = nil, want error")
	}
}

func TestStoreReserveAndReset(t *testing.T) {
	t.Parallel()

	store := newTestStore(t)
	scope := tenant.Scope{ID: "tenant-1", SubjectID: "user-1"}
	if used, granted, err := store.Reserve(context.Background(), scope, "session-1", cloudmanaged.QuotaCounterModelRequests, 1); err != nil || !granted || used != 1 {
		t.Fatalf("Reserve(first model) = (%d, %t, %v), want (1, true, nil)", used, granted, err)
	}
	if used, granted, err := store.Reserve(context.Background(), scope, "session-1", cloudmanaged.QuotaCounterModelRequests, 1); err != nil || granted || used != 1 {
		t.Fatalf("Reserve(second model) = (%d, %t, %v), want (1, false, nil)", used, granted, err)
	}
	if used, granted, err := store.Reserve(context.Background(), scope, "session-1", cloudmanaged.QuotaCounterToolUses, 1); err != nil || !granted || used != 1 {
		t.Fatalf("Reserve(first tool) = (%d, %t, %v), want (1, true, nil)", used, granted, err)
	}
	if err := store.ResetSession(context.Background(), scope, "session-1"); err != nil {
		t.Fatalf("ResetSession() error = %v", err)
	}
	if used, granted, err := store.Reserve(context.Background(), scope, "session-1", cloudmanaged.QuotaCounterModelRequests, 1); err != nil || !granted || used != 1 {
		t.Fatalf("Reserve(after reset) = (%d, %t, %v), want (1, true, nil)", used, granted, err)
	}
}

func TestStoreRejectsUnknownCounter(t *testing.T) {
	t.Parallel()

	store := newTestStore(t)
	scope := tenant.Scope{ID: "tenant-1", SubjectID: "user-1"}
	if used, granted, err := store.Reserve(context.Background(), scope, "session-1", cloudmanaged.QuotaCounter("unknown"), 1); err == nil || granted || used != 0 {
		t.Fatalf("Reserve(unknown) = (%d, %t, %v), want (0, false, error)", used, granted, err)
	}
}

func TestStoreReserveIsAtomic(t *testing.T) {
	t.Parallel()

	store := newTestStore(t)
	scope := tenant.Scope{ID: "tenant-1", SubjectID: "user-1"}

	const goroutines = 16
	var wg sync.WaitGroup
	granted := make(chan bool, goroutines)
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, ok, err := store.Reserve(context.Background(), scope, "session-1", cloudmanaged.QuotaCounterModelRequests, 1)
			if err != nil {
				t.Errorf("Reserve() error = %v", err)
				return
			}
			granted <- ok
		}()
	}
	wg.Wait()
	close(granted)

	var grantedCount int
	for ok := range granted {
		if ok {
			grantedCount++
		}
	}
	if grantedCount != 1 {
		t.Fatalf("granted count = %d, want 1", grantedCount)
	}
}

func TestStoreScopesQuotaByTenant(t *testing.T) {
	t.Parallel()

	store := newTestStore(t)
	scopeA := tenant.Scope{ID: "tenant-a", SubjectID: "user-1"}
	scopeB := tenant.Scope{ID: "tenant-b", SubjectID: "user-1"}

	if _, granted, err := store.Reserve(context.Background(), scopeA, "session-1", cloudmanaged.QuotaCounterModelRequests, 1); err != nil || !granted {
		t.Fatalf("Reserve(scopeA) = (%t, %v), want (true, nil)", granted, err)
	}
	if _, granted, err := store.Reserve(context.Background(), scopeB, "session-1", cloudmanaged.QuotaCounterModelRequests, 1); err != nil || !granted {
		t.Fatalf("Reserve(scopeB) = (%t, %v), want (true, nil)", granted, err)
	}
}

func TestStorePruneBeforeRemovesStaleSessions(t *testing.T) {
	t.Parallel()

	store := newTestStore(t)
	scope := tenant.Scope{ID: "tenant-1", SubjectID: "user-1"}
	if _, granted, err := store.Reserve(context.Background(), scope, "session-1", cloudmanaged.QuotaCounterModelRequests, 1); err != nil || !granted {
		t.Fatalf("Reserve() = (%t, %v), want (true, nil)", granted, err)
	}
	deleted, err := store.PruneBefore(context.Background(), time.Now().UTC().Add(time.Hour))
	if err != nil {
		t.Fatalf("PruneBefore() error = %v", err)
	}
	if deleted != 1 {
		t.Fatalf("PruneBefore() deleted = %d, want 1", deleted)
	}
	if used, granted, err := store.Reserve(context.Background(), scope, "session-1", cloudmanaged.QuotaCounterModelRequests, 1); err != nil || !granted || used != 1 {
		t.Fatalf("Reserve(after prune) = (%d, %t, %v), want (1, true, nil)", used, granted, err)
	}
}

func TestStoreCreateUpdateAndGetRun(t *testing.T) {
	t.Parallel()

	store := newTestStore(t)
	scope := tenant.Scope{ID: "tenant-1", SubjectID: "user-1", Attributes: map[string]string{"plan": "managed"}}
	record, err := store.CreateRun(context.Background(), cloudmanaged.CreateRunRequest{
		Prompt: "Read README.md",
		Tenant: scope,
	})
	if err != nil {
		t.Fatalf("CreateRun() error = %v", err)
	}
	if record.ID == "" || record.Status != cloudmanaged.RunStatusQueued {
		t.Fatalf("CreateRun() = %#v, want queued record with id", record)
	}

	result := "done"
	completedAt := time.Now().UTC().Truncate(time.Millisecond)
	record, err = store.UpdateRun(context.Background(), cloudmanaged.RunUpdate{
		ID:        record.ID,
		Status:    cloudmanaged.RunStatusRunning,
		SessionID: "session-1",
	})
	if err != nil {
		t.Fatalf("UpdateRun(running) error = %v", err)
	}
	if record.Status != cloudmanaged.RunStatusRunning || record.StartedAt.IsZero() {
		t.Fatalf("UpdateRun(running) = %#v, want running record with started timestamp", record)
	}

	record, err = store.UpdateRun(context.Background(), cloudmanaged.RunUpdate{
		ID:          record.ID,
		Status:      cloudmanaged.RunStatusSucceeded,
		SessionID:   "session-1",
		Result:      &result,
		CompletedAt: &completedAt,
	})
	if err != nil {
		t.Fatalf("UpdateRun() error = %v", err)
	}
	if record.Status != cloudmanaged.RunStatusSucceeded || record.SessionID != "session-1" || record.Result != "done" {
		t.Fatalf("UpdateRun() = %#v, want succeeded record with session/result", record)
	}

	got, err := store.GetRun(context.Background(), record.ID)
	if err != nil {
		t.Fatalf("GetRun() error = %v", err)
	}
	if got.Tenant.ID != scope.ID || got.Tenant.SubjectID != scope.SubjectID || got.Tenant.Attributes["plan"] != "managed" {
		t.Fatalf("GetRun() = %#v, want stored tenant scope", got)
	}
	if got.CompletedAt.IsZero() {
		t.Fatalf("GetRun() = %#v, want completed timestamp", got)
	}
}

func TestStoreGetRunReturnsNotFound(t *testing.T) {
	t.Parallel()

	store := newTestStore(t)
	_, err := store.GetRun(context.Background(), "missing")
	if !errors.Is(err, cloudmanaged.ErrRunNotFound) {
		t.Fatalf("GetRun(missing) error = %v, want ErrRunNotFound", err)
	}
}

func TestStoreNextQueuedRun(t *testing.T) {
	t.Parallel()

	store := newTestStore(t)
	first, err := store.CreateRun(context.Background(), cloudmanaged.CreateRunRequest{
		Prompt: "first",
		Tenant: tenant.Scope{ID: "tenant-1", SubjectID: "user-1"},
	})
	if err != nil {
		t.Fatalf("CreateRun(first) error = %v", err)
	}
	time.Sleep(2 * time.Millisecond)
	second, err := store.CreateRun(context.Background(), cloudmanaged.CreateRunRequest{
		Prompt: "second",
		Tenant: tenant.Scope{ID: "tenant-1", SubjectID: "user-1"},
	})
	if err != nil {
		t.Fatalf("CreateRun(second) error = %v", err)
	}
	got, err := store.NextQueuedRun(context.Background())
	if err != nil {
		t.Fatalf("NextQueuedRun() error = %v", err)
	}
	wantFirst := first
	if second.CreatedAt.Before(first.CreatedAt) || (second.CreatedAt.Equal(first.CreatedAt) && second.ID < first.ID) {
		wantFirst = second
	}
	if got.ID != wantFirst.ID {
		t.Fatalf("NextQueuedRun() = %#v, want first queued record %#v", got, wantFirst)
	}
	if _, err := store.ClaimRun(context.Background(), wantFirst.ID, "worker-1"); err != nil {
		t.Fatalf("ClaimRun(first) error = %v", err)
	}
	got, err = store.NextQueuedRun(context.Background())
	if err != nil {
		t.Fatalf("NextQueuedRun(second) error = %v", err)
	}
	wantSecond := second
	if wantFirst.ID == second.ID {
		wantSecond = first
	}
	if got.ID != wantSecond.ID {
		t.Fatalf("NextQueuedRun(second) = %#v, want second queued record %#v", got, wantSecond)
	}
	if _, err := store.ClaimRun(context.Background(), wantSecond.ID, "worker-1"); err != nil {
		t.Fatalf("ClaimRun(second) error = %v", err)
	}
	if _, err := store.NextQueuedRun(context.Background()); !errors.Is(err, cloudmanaged.ErrRunQueueEmpty) {
		t.Fatalf("NextQueuedRun(empty) error = %v, want ErrRunQueueEmpty", err)
	}
}

func TestStoreClaimHeartbeatAndFailStaleRuns(t *testing.T) {
	t.Parallel()

	store := newTestStore(t)
	scope := tenant.Scope{ID: "tenant-1", SubjectID: "user-1"}
	record, err := store.CreateRun(context.Background(), cloudmanaged.CreateRunRequest{
		Prompt: "Read README.md",
		Tenant: scope,
	})
	if err != nil {
		t.Fatalf("CreateRun() error = %v", err)
	}
	record, err = store.ClaimRun(context.Background(), record.ID, "worker-1")
	if err != nil {
		t.Fatalf("ClaimRun() error = %v", err)
	}
	if record.Status != cloudmanaged.RunStatusRunning || record.WorkerID != "worker-1" || record.HeartbeatAt.IsZero() {
		t.Fatalf("ClaimRun() = %#v, want running record with worker and heartbeat", record)
	}
	before := record.HeartbeatAt
	time.Sleep(2 * time.Millisecond)
	record, err = store.HeartbeatRun(context.Background(), record.ID, "worker-1")
	if err != nil {
		t.Fatalf("HeartbeatRun() error = %v", err)
	}
	if !record.HeartbeatAt.After(before) {
		t.Fatalf("HeartbeatRun() heartbeat = %s, want after %s", record.HeartbeatAt, before)
	}
	if _, err := store.HeartbeatRun(context.Background(), record.ID, "worker-2"); !errors.Is(err, cloudmanaged.ErrRunWorkerMismatch) {
		t.Fatalf("HeartbeatRun(worker-2) error = %v, want ErrRunWorkerMismatch", err)
	}
	failed, err := store.FailStaleRuns(context.Background(), time.Now().UTC().Add(time.Hour), "worker heartbeat expired")
	if err != nil {
		t.Fatalf("FailStaleRuns() error = %v", err)
	}
	if len(failed) != 1 {
		t.Fatalf("FailStaleRuns() = %#v, want one failed record", failed)
	}
	if failed[0].ID != record.ID || failed[0].Status != cloudmanaged.RunStatusFailed {
		t.Fatalf("FailStaleRuns() = %#v, want failed record for %q", failed, record.ID)
	}
	record, err = store.GetRun(context.Background(), record.ID)
	if err != nil {
		t.Fatalf("GetRun() error = %v", err)
	}
	if record.Status != cloudmanaged.RunStatusFailed || record.Error != "worker heartbeat expired" || record.CompletedAt.IsZero() {
		t.Fatalf("GetRun() = %#v, want failed stale record", record)
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

func ExampleNew() {
	db, err := sql.Open("sqlite", "file:quota?mode=memory&cache=shared")
	if err != nil {
		panic(err)
	}
	defer db.Close()

	store, err := New(context.Background(), db)
	if err != nil {
		panic(err)
	}
	_, granted, err := store.Reserve(context.Background(), tenant.Scope{ID: "tenant-1", SubjectID: "user-1"}, "session-1", cloudmanaged.QuotaCounterModelRequests, 1)
	if err != nil {
		panic(err)
	}
	fmt.Println(granted)
	// Output: true
}
