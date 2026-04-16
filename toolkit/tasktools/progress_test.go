package tasktools

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/MemaxLabs/memax-go-agent-sdk/model"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/verifytools"
)

func TestVerificationProgressVerifierUpdatesTaskOnPass(t *testing.T) {
	store := NewMemoryStore([]Task{{ID: "task-1", Title: "fix README", Status: StatusInProgress}})
	verifier := NewVerificationProgressVerifier(store, verifytools.VerifierFunc(func(context.Context, verifytools.Request) (verifytools.Result, error) {
		return verifytools.Result{Name: "test", Passed: true, Output: "ok"}, nil
	}))

	result, err := verifier.Verify(context.Background(), verifytools.Request{
		Name:   "test",
		Target: "README.md",
		Metadata: map[string]any{
			model.MetadataTaskID: "task-1",
		},
	})
	if err != nil {
		t.Fatalf("Verify returned error: %v", err)
	}
	if result.Metadata[model.MetadataTaskID] != "task-1" || result.Metadata[model.MetadataTaskStatus] != string(StatusCompleted) {
		t.Fatalf("metadata = %#v, want task progress metadata", result.Metadata)
	}
	tasks, err := store.List(context.Background())
	if err != nil {
		t.Fatalf("List returned error: %v", err)
	}
	if tasks[0].Status != StatusCompleted {
		t.Fatalf("task = %#v, want completed", tasks[0])
	}
	if !strings.Contains(tasks[0].Notes, "verification test passed") {
		t.Fatalf("notes = %q, want verification note", tasks[0].Notes)
	}
	if len(tasks[0].Evidence) != 2 || tasks[0].Evidence[0] != "verification:test" || tasks[0].Evidence[1] != "README.md" {
		t.Fatalf("evidence = %#v, want verification evidence", tasks[0].Evidence)
	}
}

func TestVerificationProgressVerifierUpdatesTaskOnFailure(t *testing.T) {
	store := NewMemoryStore([]Task{{ID: "task-1", Title: "fix README", Status: StatusInProgress}})
	verifier := NewVerificationProgressVerifier(store, verifytools.VerifierFunc(func(context.Context, verifytools.Request) (verifytools.Result, error) {
		return verifytools.Result{
			Name:   "test",
			Passed: false,
			Diagnostics: []verifytools.Diagnostic{{
				Path:     "README.md",
				Severity: "error",
				Message:  "still broken",
			}},
		}, nil
	}), WithVerificationFailStatus(StatusBlocked))

	result, err := verifier.Verify(context.Background(), verifytools.Request{
		Name: "test",
		Metadata: map[string]any{
			model.MetadataTaskID: "task-1",
		},
	})
	if err != nil {
		t.Fatalf("Verify returned error: %v", err)
	}
	if result.Metadata[model.MetadataTaskStatus] != string(StatusBlocked) {
		t.Fatalf("metadata = %#v, want blocked", result.Metadata)
	}
	tasks, err := store.List(context.Background())
	if err != nil {
		t.Fatalf("List returned error: %v", err)
	}
	if tasks[0].Status != StatusBlocked {
		t.Fatalf("task = %#v, want blocked", tasks[0])
	}
	if !strings.Contains(tasks[0].Notes, "verification test failed with 1 diagnostics") {
		t.Fatalf("notes = %q, want failure note", tasks[0].Notes)
	}
}

func TestVerificationProgressVerifierRecordsUpdateErrorNonFatally(t *testing.T) {
	store := NewMemoryStore(nil)
	verifier := NewVerificationProgressVerifier(store, verifytools.VerifierFunc(func(context.Context, verifytools.Request) (verifytools.Result, error) {
		return verifytools.Result{Name: "test", Passed: true}, nil
	}))

	result, err := verifier.Verify(context.Background(), verifytools.Request{
		Name: "test",
		Metadata: map[string]any{
			model.MetadataTaskID: "missing",
		},
	})
	if err != nil {
		t.Fatalf("Verify returned error: %v", err)
	}
	if !strings.Contains(result.Metadata[model.MetadataTaskProgressError].(string), "task not found") {
		t.Fatalf("metadata = %#v, want progress error", result.Metadata)
	}
}

func TestVerificationProgressVerifierIgnoresNonStringTaskID(t *testing.T) {
	store := NewMemoryStore([]Task{{ID: "42", Title: "numeric-looking task", Status: StatusInProgress}})
	verifier := NewVerificationProgressVerifier(store, verifytools.VerifierFunc(func(context.Context, verifytools.Request) (verifytools.Result, error) {
		return verifytools.Result{Name: "test", Passed: true}, nil
	}))

	result, err := verifier.Verify(context.Background(), verifytools.Request{
		Name: "test",
		Metadata: map[string]any{
			model.MetadataTaskID: 42,
		},
	})
	if err != nil {
		t.Fatalf("Verify returned error: %v", err)
	}
	if result.Metadata != nil {
		t.Fatalf("metadata = %#v, want no task progress metadata", result.Metadata)
	}
	tasks, err := store.List(context.Background())
	if err != nil {
		t.Fatalf("List returned error: %v", err)
	}
	if tasks[0].Status != StatusInProgress {
		t.Fatalf("task = %#v, want unchanged status", tasks[0])
	}
}

func TestVerificationProgressVerifierPropagatesVerifierError(t *testing.T) {
	wantErr := errors.New("runner failed")
	verifier := NewVerificationProgressVerifier(NewMemoryStore(nil), verifytools.VerifierFunc(func(context.Context, verifytools.Request) (verifytools.Result, error) {
		return verifytools.Result{}, wantErr
	}))
	_, err := verifier.Verify(context.Background(), verifytools.Request{})
	if !errors.Is(err, wantErr) {
		t.Fatalf("Verify error = %v, want %v", err, wantErr)
	}
}
