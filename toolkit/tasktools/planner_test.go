package tasktools

import (
	"context"
	"testing"

	"github.com/MemaxLabs/memax-go-agent-sdk/planner"
)

func TestPlannerAdaptsTaskStore(t *testing.T) {
	store := NewMemoryStore([]Task{
		{ID: "task-2", Title: "write summary", Status: StatusPending, Priority: 2},
		{ID: "task-1", Title: "read migration", Status: StatusInProgress, Notes: "check rollback", Priority: 1},
	})
	policy := Planner(store,
		planner.WithTaskGoal("review safely"),
		planner.WithTaskConstraints("read first"),
		planner.WithTaskToolHints(ListToolName, UpsertToolName),
		planner.WithTaskVerificationHints("workspace_verify test"),
	)

	plan, err := policy.Prepare(context.Background(), planner.Request{})
	if err != nil {
		t.Fatalf("Prepare returned error: %v", err)
	}
	if plan.Goal != "review safely" || len(plan.Steps) != 2 {
		t.Fatalf("plan = %#v, want goal and steps", plan)
	}
	if plan.Steps[0].ID != "task-1" || plan.Steps[0].Status != planner.StatusInProgress || plan.Steps[0].Notes != "check rollback" {
		t.Fatalf("first step = %#v, want mapped task", plan.Steps[0])
	}
	if len(plan.Steps[0].ToolHints) != 2 || plan.Steps[0].ToolHints[0] != ListToolName || plan.Steps[0].ToolHints[1] != UpsertToolName {
		t.Fatalf("tool hints = %#v, want task tool hints", plan.Steps[0].ToolHints)
	}
	if len(plan.Steps[0].VerificationHints) != 1 || plan.Steps[0].VerificationHints[0] != "workspace_verify test" {
		t.Fatalf("verification hints = %#v, want task verification hint", plan.Steps[0].VerificationHints)
	}
}
