package tasktools

import (
	"context"
	"strings"
	"testing"

	"github.com/MemaxLabs/memax-go-agent-sdk/model"
	"github.com/MemaxLabs/memax-go-agent-sdk/planner"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/subagents"
)

func TestSubagentPlannerScopesTask(t *testing.T) {
	store := NewMemoryStore([]Task{
		{ID: "task-1", Title: "fix README", Status: StatusInProgress, Evidence: []string{"README.md"}},
		{ID: "task-2", Title: "unrelated", Status: StatusPending},
	})
	source := SubagentPlanner(store,
		planner.WithTaskToolHints("read_file"),
		planner.WithTaskVerificationHints("workspace_verify test"),
	)

	plan, err := source.SubagentPlan(context.Background(), subagents.PlanRequest{
		Agent:  "worker",
		TaskID: "task-1",
		Prompt: "handle task-1",
	})
	if err != nil {
		t.Fatalf("SubagentPlan returned error: %v", err)
	}
	if len(plan.Steps) != 1 || plan.Steps[0].ID != "task-1" {
		t.Fatalf("plan = %#v, want scoped task-1", plan)
	}
	if strings.Contains(plan.Steps[0].Title, "unrelated") {
		t.Fatalf("plan = %#v, leaked unrelated task", plan)
	}
	if len(plan.Steps[0].ToolHints) != 1 || plan.Steps[0].ToolHints[0] != "read_file" {
		t.Fatalf("tool hints = %#v, want global hint", plan.Steps[0].ToolHints)
	}
	if len(plan.Steps[0].VerificationHints) != 1 || plan.Steps[0].VerificationHints[0] != "workspace_verify test" {
		t.Fatalf("verification hints = %#v, want global hint", plan.Steps[0].VerificationHints)
	}
	if len(plan.Steps[0].Evidence) != 1 || plan.Steps[0].Evidence[0] != "README.md" {
		t.Fatalf("evidence = %#v, want task evidence", plan.Steps[0].Evidence)
	}
}

func TestSubagentProgressHandlerUpdatesTaskOnSuccess(t *testing.T) {
	store := NewMemoryStore([]Task{{ID: "task-1", Title: "delegate", Status: StatusInProgress}})
	handler := NewSubagentProgressHandler(store)

	metadata, err := handler.HandleSubagentResult(context.Background(), subagents.ResultRequest{
		Agent:          "worker",
		TaskID:         "task-1",
		ChildSessionID: "child-1",
		Result:         "done",
	})
	if err != nil {
		t.Fatalf("HandleSubagentResult returned error: %v", err)
	}
	if metadata[model.MetadataTaskStatus] != string(StatusCompleted) {
		t.Fatalf("metadata = %#v, want completed", metadata)
	}
	tasks, err := store.List(context.Background())
	if err != nil {
		t.Fatalf("List returned error: %v", err)
	}
	if tasks[0].Status != StatusCompleted {
		t.Fatalf("task = %#v, want completed", tasks[0])
	}
	if !strings.Contains(tasks[0].Notes, "subagent worker completed") {
		t.Fatalf("notes = %q, want subagent note", tasks[0].Notes)
	}
	if len(tasks[0].Evidence) != 2 || tasks[0].Evidence[0] != "subagent:worker" || tasks[0].Evidence[1] != "child_session:child-1" {
		t.Fatalf("evidence = %#v, want subagent evidence", tasks[0].Evidence)
	}
}

func TestSubagentProgressHandlerUpdatesTaskOnFailure(t *testing.T) {
	store := NewMemoryStore([]Task{{ID: "task-1", Title: "delegate", Status: StatusInProgress}})
	handler := NewSubagentProgressHandler(store, WithSubagentFailureStatus(StatusBlocked))

	metadata, err := handler.HandleSubagentResult(context.Background(), subagents.ResultRequest{
		Agent:   "worker",
		TaskID:  "task-1",
		IsError: true,
	})
	if err != nil {
		t.Fatalf("HandleSubagentResult returned error: %v", err)
	}
	if metadata[model.MetadataTaskStatus] != string(StatusBlocked) {
		t.Fatalf("metadata = %#v, want blocked", metadata)
	}
	tasks, err := store.List(context.Background())
	if err != nil {
		t.Fatalf("List returned error: %v", err)
	}
	if tasks[0].Status != StatusBlocked {
		t.Fatalf("task = %#v, want blocked", tasks[0])
	}
}
