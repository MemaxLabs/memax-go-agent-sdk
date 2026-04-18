package tenant

import (
	"context"
	"errors"
	"testing"
)

func TestScopeCloneDetachesAttributes(t *testing.T) {
	scope := Scope{
		ID:        "tenant-1",
		SubjectID: "user-1",
		Attributes: map[string]string{
			"region": "us",
		},
	}

	cloned := scope.Clone()
	scope.Attributes["region"] = "mutated"

	if cloned.Attributes["region"] != "us" {
		t.Fatalf("cloned attributes = %#v, want detached copy", cloned.Attributes)
	}
}

func TestCheckUsesValidatorWithClonedScope(t *testing.T) {
	scope := Scope{
		ID: "tenant-1",
		Attributes: map[string]string{
			"region": "us",
		},
	}
	calls := 0
	err := Check(context.Background(), ValidatorFunc(func(_ context.Context, req Request) error {
		calls++
		req.Scope.Attributes["region"] = "mutated"
		return errors.New("blocked")
	}), Request{Boundary: BoundaryModelRequest, Scope: scope})

	if err == nil || err.Error() != "blocked" {
		t.Fatalf("Check error = %v, want blocked", err)
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1", calls)
	}
	if scope.Attributes["region"] != "us" {
		t.Fatalf("scope attributes mutated: %#v", scope.Attributes)
	}
}
