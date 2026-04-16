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

func TestFromTaskSourceBuildsPlan(t *testing.T) {
	policy := FromTaskSource(TaskSourceFunc(func(_ context.Context, req Request) ([]Task, error) {
		if req.Query != "review migration" {
			t.Fatalf("query = %q, want review migration", req.Query)
		}
		return []Task{
			{ID: "task-2", Title: "write summary", Status: StatusPending, Priority: 2},
			{ID: "task-1", Title: "read migration", Status: StatusInProgress, Notes: "check rollback", Priority: 1, ToolHints: []string{"read_file"}},
		}, nil
	}), WithTaskGoal("review safely"), WithTaskConstraints("read first"), WithTaskToolHints("list_tasks"))

	plan, err := policy.Prepare(context.Background(), Request{Query: "review migration"})
	if err != nil {
		t.Fatalf("Prepare returned error: %v", err)
	}
	if plan.Goal != "review safely" || plan.State != StateActive {
		t.Fatalf("plan = %#v, want goal and active state", plan)
	}
	if len(plan.Steps) != 2 || plan.Steps[0].ID != "task-1" || plan.Steps[1].ID != "task-2" {
		t.Fatalf("steps = %#v, want priority order", plan.Steps)
	}
	if plan.Steps[0].Notes != "check rollback" {
		t.Fatalf("step notes = %q, want preserved notes", plan.Steps[0].Notes)
	}
	if len(plan.Steps[0].ToolHints) != 2 || plan.Steps[0].ToolHints[0] != "list_tasks" || plan.Steps[0].ToolHints[1] != "read_file" {
		t.Fatalf("tool hints = %#v, want global and task hints", plan.Steps[0].ToolHints)
	}
}

func TestFromTaskSourceInfersTerminalState(t *testing.T) {
	policy := FromTaskSource(TaskSourceFunc(func(context.Context, Request) ([]Task, error) {
		return []Task{{ID: "task-1", Title: "done", Status: StatusCompleted}}, nil
	}))
	plan, err := policy.Prepare(context.Background(), Request{})
	if err != nil {
		t.Fatalf("Prepare returned error: %v", err)
	}
	if plan.State != StateCompleted {
		t.Fatalf("state = %q, want completed", plan.State)
	}
}

func TestFromTaskSourceInfersCanceledOnlyWhenAllTasksCanceled(t *testing.T) {
	policy := FromTaskSource(TaskSourceFunc(func(context.Context, Request) ([]Task, error) {
		return []Task{
			{ID: "task-1", Title: "skip optional path", Status: StatusCanceled},
			{ID: "task-2", Title: "continue active path", Status: StatusInProgress},
		}, nil
	}))
	plan, err := policy.Prepare(context.Background(), Request{})
	if err != nil {
		t.Fatalf("Prepare returned error: %v", err)
	}
	if plan.State != StateActive {
		t.Fatalf("state = %q, want active when work remains", plan.State)
	}

	policy = FromTaskSource(TaskSourceFunc(func(context.Context, Request) ([]Task, error) {
		return []Task{{ID: "task-1", Title: "skip", Status: StatusCanceled}}, nil
	}))
	plan, err = policy.Prepare(context.Background(), Request{})
	if err != nil {
		t.Fatalf("Prepare returned error: %v", err)
	}
	if plan.State != StateCanceled {
		t.Fatalf("state = %q, want canceled when all tasks canceled", plan.State)
	}
}
