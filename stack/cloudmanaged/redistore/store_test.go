package redistore

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MemaxLabs/memax-go-agent-sdk/stack/cloudmanaged"
	"github.com/MemaxLabs/memax-go-agent-sdk/tenant"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func TestStoreEnsureReserveReset(t *testing.T) {
	t.Parallel()

	store, server := newTestStore(t)
	ctx := context.Background()
	scope := tenant.Scope{ID: "tenant-1", SubjectID: "user-1"}

	if err := store.EnsureSession(ctx, scope, "session-1"); err != nil {
		t.Fatalf("EnsureSession() error = %v", err)
	}
	sessionKey := store.sessionKey(scope, "session-1")
	if !server.Exists(sessionKey) {
		t.Fatalf("session key %q missing", sessionKey)
	}
	if got := server.TTL(sessionKey); got <= 0 {
		t.Fatalf("session key TTL = %v, want > 0", got)
	}

	if used, granted, err := store.Reserve(ctx, scope, "session-1", cloudmanaged.QuotaCounterModelRequests, 1); err != nil || !granted || used != 1 {
		t.Fatalf("Reserve(first) = (%d, %t, %v), want (1, true, nil)", used, granted, err)
	}
	if used, granted, err := store.Reserve(ctx, scope, "session-1", cloudmanaged.QuotaCounterModelRequests, 1); err != nil || granted || used != 1 {
		t.Fatalf("Reserve(second) = (%d, %t, %v), want (1, false, nil)", used, granted, err)
	}
	counterKey := store.counterKey(scope, "session-1", cloudmanaged.QuotaCounterModelRequests)
	if got, err := server.Get(counterKey); err != nil || got != "1" {
		t.Fatalf("counter key %q = (%q, %v), want (1, nil)", counterKey, got, err)
	}
	if got := server.TTL(counterKey); got <= 0 {
		t.Fatalf("counter key TTL = %v, want > 0", got)
	}

	if err := store.ResetSession(ctx, scope, "session-1"); err != nil {
		t.Fatalf("ResetSession() error = %v", err)
	}
	if server.Exists(sessionKey) || server.Exists(counterKey) {
		t.Fatalf("ResetSession() left redis keys behind: %v", server.Keys())
	}
}

func TestStoreReserveIsAtomic(t *testing.T) {
	t.Parallel()

	store, _ := newTestStore(t)
	ctx := context.Background()
	scope := tenant.Scope{ID: "tenant-1", SubjectID: "user-1"}

	const goroutines = 16
	var wg sync.WaitGroup
	granted := make(chan bool, goroutines)
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, ok, err := store.Reserve(ctx, scope, "session-1", cloudmanaged.QuotaCounterModelRequests, 1)
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

	store, _ := newTestStore(t)
	ctx := context.Background()
	scopeA := tenant.Scope{ID: "tenant-a", SubjectID: "user-1"}
	scopeB := tenant.Scope{ID: "tenant-b", SubjectID: "user-1"}

	if _, granted, err := store.Reserve(ctx, scopeA, "session-1", cloudmanaged.QuotaCounterModelRequests, 1); err != nil || !granted {
		t.Fatalf("Reserve(scopeA) = (%t, %v), want grant", granted, err)
	}
	if _, granted, err := store.Reserve(ctx, scopeB, "session-1", cloudmanaged.QuotaCounterModelRequests, 1); err != nil || !granted {
		t.Fatalf("Reserve(scopeB) = (%t, %v), want independent grant", granted, err)
	}
}

func TestStoreRejectsUnknownCounter(t *testing.T) {
	t.Parallel()

	store, _ := newTestStore(t)
	_, granted, err := store.Reserve(context.Background(), tenant.Scope{ID: "tenant-1"}, "session-1", cloudmanaged.QuotaCounter("unknown"), 1)
	if err == nil || granted || !strings.Contains(err.Error(), "unknown quota counter") {
		t.Fatalf("Reserve(unknown) = (%t, %v), want unknown counter error", granted, err)
	}
}

func TestStoreTTLCanBeDisabled(t *testing.T) {
	t.Parallel()

	store, server := newTestStore(t, WithTTL(0))
	ctx := context.Background()
	scope := tenant.Scope{ID: "tenant-1", SubjectID: "user-1"}

	if err := store.EnsureSession(ctx, scope, "session-1"); err != nil {
		t.Fatalf("EnsureSession() error = %v", err)
	}
	if _, granted, err := store.Reserve(ctx, scope, "session-1", cloudmanaged.QuotaCounterToolUses, 1); err != nil || !granted {
		t.Fatalf("Reserve() = (%t, %v), want grant", granted, err)
	}

	if got := server.TTL(store.sessionKey(scope, "session-1")); got != 0 {
		t.Fatalf("session TTL = %v, want 0 when disabled", got)
	}
	if got := server.TTL(store.counterKey(scope, "session-1", cloudmanaged.QuotaCounterToolUses)); got != 0 {
		t.Fatalf("counter TTL = %v, want 0 when disabled", got)
	}
}

func newTestStore(t *testing.T, options ...Option) (*Store, *miniredis.Miniredis) {
	t.Helper()

	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() {
		_ = client.Close()
	})
	store, err := New(client, options...)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return store, server
}

func TestTTLSecondsRoundsUp(t *testing.T) {
	t.Parallel()

	if got := ttlSeconds(500 * time.Millisecond); got != 1 {
		t.Fatalf("ttlSeconds(500ms) = %d, want 1", got)
	}
}
