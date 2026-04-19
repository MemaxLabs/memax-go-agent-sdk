package remote

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	agenteval "github.com/MemaxLabs/memax-go-agent-sdk/agenteval"
	"github.com/MemaxLabs/memax-go-agent-sdk/model"
	"github.com/MemaxLabs/memax-go-agent-sdk/stack/cloudmanaged"
	"github.com/MemaxLabs/memax-go-agent-sdk/tenant"
)

func TestHTTPPoolClaim(t *testing.T) {
	t.Parallel()

	record := cloudmanaged.RunRecord{
		ID:     "run-1",
		Status: cloudmanaged.RunStatusQueued,
		Tenant: tenant.Scope{ID: "tenant-1", SubjectID: "user-1"},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer token-1" {
			t.Fatalf("Authorization = %q, want Bearer token-1", got)
		}
		if got := r.Header.Get("X-Memax-Worker"); got != "worker-1" {
			t.Fatalf("X-Memax-Worker = %q, want worker-1", got)
		}
		if err := json.NewEncoder(w).Encode(record); err != nil {
			t.Fatalf("Encode() error = %v", err)
		}
	}))
	defer server.Close()

	pool, err := NewHTTPPool(server.URL,
		WithBearerToken("token-1"),
		WithHeader("X-Memax-Worker", "worker-1"),
	)
	if err != nil {
		t.Fatalf("NewHTTPPool() error = %v", err)
	}
	got, err := pool.Claim(context.Background())
	if err != nil {
		t.Fatalf("Claim() error = %v", err)
	}
	if got.ID != record.ID || got.Tenant.ID != "tenant-1" {
		t.Fatalf("Claim() = %#v, want %#v", got, record)
	}
}

func TestHTTPPoolClaimNoRunAvailable(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	pool, err := NewHTTPPool(server.URL)
	if err != nil {
		t.Fatalf("NewHTTPPool() error = %v", err)
	}
	_, err = pool.Claim(context.Background())
	if !errors.Is(err, ErrNoRunAvailable) {
		t.Fatalf("Claim() error = %v, want ErrNoRunAvailable", err)
	}
}

func TestClaimHandlerServesNextQueuedRun(t *testing.T) {
	t.Parallel()

	store := cloudmanaged.NewMemoryRunStore()
	first, err := store.CreateRun(context.Background(), cloudmanaged.CreateRunRequest{
		Prompt: "first",
		Tenant: tenant.Scope{ID: "tenant-1", SubjectID: "user-1"},
	})
	if err != nil {
		t.Fatalf("CreateRun(first) error = %v", err)
	}
	second, err := store.CreateRun(context.Background(), cloudmanaged.CreateRunRequest{
		Prompt: "second",
		Tenant: tenant.Scope{ID: "tenant-1", SubjectID: "user-1"},
	})
	if err != nil {
		t.Fatalf("CreateRun(second) error = %v", err)
	}

	handler, err := ClaimHandler(store)
	if err != nil {
		t.Fatalf("ClaimHandler() error = %v", err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()

	pool, err := NewHTTPPool(server.URL)
	if err != nil {
		t.Fatalf("NewHTTPPool() error = %v", err)
	}
	got, err := pool.Claim(context.Background())
	if err != nil {
		t.Fatalf("Claim() error = %v", err)
	}
	if got.ID != first.ID || got.Prompt != "first" {
		t.Fatalf("Claim() = %#v, want first queued record %#v", got, first)
	}

	if _, err := store.ClaimRun(context.Background(), first.ID, "worker-1"); err != nil {
		t.Fatalf("ClaimRun(first) error = %v", err)
	}
	got, err = pool.Claim(context.Background())
	if err != nil {
		t.Fatalf("Claim(second) error = %v", err)
	}
	if got.ID != second.ID || got.Prompt != "second" {
		t.Fatalf("Claim(second) = %#v, want second queued record %#v", got, second)
	}
}

func TestClaimHandlerNoQueuedRun(t *testing.T) {
	t.Parallel()

	store := cloudmanaged.NewMemoryRunStore()
	handler, err := ClaimHandler(store)
	if err != nil {
		t.Fatalf("ClaimHandler() error = %v", err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()

	pool, err := NewHTTPPool(server.URL)
	if err != nil {
		t.Fatalf("NewHTTPPool() error = %v", err)
	}
	_, err = pool.Claim(context.Background())
	if !errors.Is(err, ErrNoRunAvailable) {
		t.Fatalf("Claim() error = %v, want ErrNoRunAvailable", err)
	}
}

func TestRunOnceExecutesQueuedRun(t *testing.T) {
	t.Parallel()

	modelClient := agenteval.NewScriptedModel([]model.StreamEvent{{Kind: model.StreamText, Text: "remote worker done"}})
	config, err := cloudmanaged.PresetManagedWorker.Config()
	if err != nil {
		t.Fatalf("PresetManagedWorker.Config() error = %v", err)
	}
	config.Base.Model = modelClient
	config.RunStore = cloudmanaged.NewMemoryRunStore()
	config.Policies.Quota.MaxModelRequests = 4
	config.Policies.Quota.MaxToolUses = 4
	stack, err := cloudmanaged.New(config)
	if err != nil {
		t.Fatalf("cloudmanaged.New() error = %v", err)
	}
	scope := tenant.Scope{ID: "tenant-1", SubjectID: "user-1"}
	record, err := stack.EnqueueRun(context.Background(), "finish remotely", scope)
	if err != nil {
		t.Fatalf("EnqueueRun() error = %v", err)
	}

	var served atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !served.CompareAndSwap(false, true) {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if err := json.NewEncoder(w).Encode(record); err != nil {
			t.Fatalf("Encode() error = %v", err)
		}
	}))
	defer server.Close()

	pool, err := NewHTTPPool(server.URL)
	if err != nil {
		t.Fatalf("NewHTTPPool() error = %v", err)
	}
	final, executed, err := RunOnce(context.Background(), stack, pool, cloudmanaged.WorkerOptions{
		ID:                "worker-1",
		HeartbeatInterval: time.Millisecond,
	})
	if err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	if !executed {
		t.Fatalf("RunOnce() executed = false, want true")
	}
	if final.Status != cloudmanaged.RunStatusSucceeded || final.WorkerID != "worker-1" || final.Result != "remote worker done" {
		t.Fatalf("RunOnce() final = %#v, want succeeded worker-owned run", final)
	}

	none, executed, err := RunOnce(context.Background(), stack, pool, cloudmanaged.WorkerOptions{ID: "worker-2"})
	if err != nil {
		t.Fatalf("RunOnce(second) error = %v, want nil", err)
	}
	if executed || none.ID != "" {
		t.Fatalf("RunOnce(second) = (%#v, %t), want no work", none, executed)
	}
}

func TestClaimHandlerRequiresQueuedRunDiscovery(t *testing.T) {
	t.Parallel()

	if _, err := ClaimHandler(staticRunStore{}); err == nil {
		t.Fatalf("ClaimHandler() error = nil, want unsupported store error")
	}
}

func TestRunOnceTreatsClaimRaceAsNoWork(t *testing.T) {
	t.Parallel()

	pool := staticPool{record: cloudmanaged.RunRecord{ID: "run-1"}}
	executor := executeFunc(func(context.Context, string, cloudmanaged.WorkerOptions) (cloudmanaged.RunRecord, error) {
		return cloudmanaged.RunRecord{}, cloudmanaged.ErrRunNotQueued
	})
	record, executed, err := RunOnce(context.Background(), executor, pool, cloudmanaged.WorkerOptions{ID: "worker-1"})
	if err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	if executed {
		t.Fatalf("RunOnce() executed = true, want false")
	}
	if record.ID != "run-1" {
		t.Fatalf("RunOnce() record = %#v, want offered run", record)
	}
}

type staticPool struct {
	record cloudmanaged.RunRecord
	err    error
}

func (p staticPool) Claim(context.Context) (cloudmanaged.RunRecord, error) {
	return p.record, p.err
}

type executeFunc func(context.Context, string, cloudmanaged.WorkerOptions) (cloudmanaged.RunRecord, error)

func (fn executeFunc) ExecuteRun(ctx context.Context, runID string, options cloudmanaged.WorkerOptions) (cloudmanaged.RunRecord, error) {
	return fn(ctx, runID, options)
}

type staticRunStore struct{}

func (staticRunStore) CreateRun(context.Context, cloudmanaged.CreateRunRequest) (cloudmanaged.RunRecord, error) {
	return cloudmanaged.RunRecord{}, nil
}

func (staticRunStore) UpdateRun(context.Context, cloudmanaged.RunUpdate) (cloudmanaged.RunRecord, error) {
	return cloudmanaged.RunRecord{}, nil
}

func (staticRunStore) GetRun(context.Context, string) (cloudmanaged.RunRecord, error) {
	return cloudmanaged.RunRecord{}, nil
}
