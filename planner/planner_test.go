package planner

import (
	"context"
	"testing"
)

func TestStaticReturnsDefensiveCopy(t *testing.T) {
	policy := Static(Plan{
		Goal:        "ship safely",
		Constraints: []string{"read first"},
		Steps: []Step{{
			ID:        "step-1",
			Title:     "inspect",
			Status:    StatusInProgress,
			Evidence:  []string{"README.md"},
			ToolHints: []string{"read_file"},
		}},
		State: StateActive,
	})

	first, err := policy.Prepare(context.Background(), Request{})
	if err != nil {
		t.Fatalf("Prepare returned error: %v", err)
	}
	first.Constraints[0] = "mutated"
	first.Steps[0].Evidence[0] = "mutated"
	first.Steps[0].ToolHints[0] = "mutated"

	second, err := policy.Prepare(context.Background(), Request{})
	if err != nil {
		t.Fatalf("Prepare returned error: %v", err)
	}
	if second.Constraints[0] != "read first" {
		t.Fatalf("constraints = %#v, want defensive copy", second.Constraints)
	}
	if second.Steps[0].Evidence[0] != "README.md" || second.Steps[0].ToolHints[0] != "read_file" {
		t.Fatalf("steps = %#v, want defensive copy", second.Steps)
	}
}

func TestPolicyFuncNilReturnsEmptyPlan(t *testing.T) {
	plan, err := (PolicyFunc(nil)).Prepare(context.Background(), Request{})
	if err != nil {
		t.Fatalf("Prepare returned error: %v", err)
	}
	if !plan.Empty() {
		t.Fatalf("plan = %#v, want empty", plan)
	}
}
