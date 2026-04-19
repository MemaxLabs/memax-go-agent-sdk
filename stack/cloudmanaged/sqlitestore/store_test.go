package sqlitestore

import (
	"context"
	"database/sql"
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
