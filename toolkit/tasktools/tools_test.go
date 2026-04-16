package tasktools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/MemaxLabs/memax-go-agent-sdk/model"
	"github.com/MemaxLabs/memax-go-agent-sdk/tool"
)

func TestTaskToolsCreateListUpdateDelete(t *testing.T) {
	store := NewMemoryStore(nil)

	first := mustRunTool(t, NewUpsertTool(store), model.ToolUse{
		ID:    "upsert-1",
		Name:  UpsertToolName,
		Input: json.RawMessage(`{"title":"inspect session API","status":"in_progress","priority":2,"evidence":["README.md"]}`),
	})
	if first.Content != "upserted task-1" {
		t.Fatalf("first content = %q, want upsert confirmation", first.Content)
	}
	second := mustRunTool(t, NewUpsertTool(store), model.ToolUse{
		ID:    "upsert-2",
		Name:  UpsertToolName,
		Input: json.RawMessage(`{"title":"write tests","status":"pending","priority":1,"notes":"cover ordering"}`),
	})
	if second.Content != "upserted task-2" {
		t.Fatalf("second content = %q, want upsert confirmation", second.Content)
	}

	list := mustRunTool(t, NewListTool(store), model.ToolUse{
		ID:    "list-1",
		Name:  ListToolName,
		Input: json.RawMessage(`{}`),
	})
	want := "- [pending] task-2 p1: write tests - cover ordering\n- [in_progress] task-1 p2: inspect session API evidence: README.md"
	if list.Content != want {
		t.Fatalf("list content = %q, want %q", list.Content, want)
	}

	update := mustRunTool(t, NewUpsertTool(store), model.ToolUse{
		ID:    "upsert-3",
		Name:  UpsertToolName,
		Input: json.RawMessage(`{"id":"task-1","status":"completed","notes":"done"}`),
	})
	if update.Metadata["status"] != string(StatusCompleted) {
		t.Fatalf("update metadata = %#v, want completed", update.Metadata)
	}
	if update.Metadata["title"] != "inspect session API" {
		t.Fatalf("update metadata = %#v, want preserved title", update.Metadata)
	}
	evidence, ok := update.Metadata["evidence"].([]string)
	if !ok || len(evidence) != 1 || evidence[0] != "README.md" {
		t.Fatalf("update metadata = %#v, want preserved evidence", update.Metadata)
	}

	filtered := mustRunTool(t, NewListTool(store), model.ToolUse{
		ID:    "list-2",
		Name:  ListToolName,
		Input: json.RawMessage(`{"status":"completed"}`),
	})
	if !strings.Contains(filtered.Content, "task-1") || strings.Contains(filtered.Content, "task-2") {
		t.Fatalf("filtered content = %q, want only task-1", filtered.Content)
	}

	deleted := mustRunTool(t, NewDeleteTool(store), model.ToolUse{
		ID:    "delete-1",
		Name:  DeleteToolName,
		Input: json.RawMessage(`{"id":"task-2"}`),
	})
	if deleted.Content != "deleted task-2" {
		t.Fatalf("deleted content = %q, want delete confirmation", deleted.Content)
	}
}

func TestListToolEmptyStore(t *testing.T) {
	result := mustRunTool(t, NewListTool(NewMemoryStore(nil)), model.ToolUse{
		ID:    "list-1",
		Name:  ListToolName,
		Input: json.RawMessage(`{}`),
	})
	if result.Content != "no tasks" {
		t.Fatalf("Content = %q, want no tasks", result.Content)
	}
}

func TestMemoryStorePreservesExplicitIDsAndNextGeneratedID(t *testing.T) {
	store := NewMemoryStore([]Task{{ID: "task-7", Title: "existing"}})
	task, err := store.Upsert(context.Background(), Task{Title: "new"})
	if err != nil {
		t.Fatalf("Upsert returned error: %v", err)
	}
	if task.ID != "task-8" {
		t.Fatalf("ID = %q, want task-8", task.ID)
	}
}

func TestMemoryStoreReturnsDefensiveTaskCopies(t *testing.T) {
	store := NewMemoryStore([]Task{{ID: "task-1", Title: "existing", Evidence: []string{"README.md"}}})
	tasks, err := store.List(context.Background())
	if err != nil {
		t.Fatalf("List returned error: %v", err)
	}
	tasks[0].Evidence[0] = "mutated"

	tasks, err = store.List(context.Background())
	if err != nil {
		t.Fatalf("List returned error: %v", err)
	}
	if tasks[0].Evidence[0] != "README.md" {
		t.Fatalf("tasks = %#v, want defensive evidence copy", tasks)
	}

	task, err := store.Upsert(context.Background(), Task{ID: "task-1", Evidence: []string{"verified"}})
	if err != nil {
		t.Fatalf("Upsert returned error: %v", err)
	}
	task.Evidence[0] = "mutated"
	tasks, err = store.List(context.Background())
	if err != nil {
		t.Fatalf("List returned error: %v", err)
	}
	if tasks[0].Evidence[0] != "verified" {
		t.Fatalf("tasks = %#v, want stored evidence isolated from returned task", tasks)
	}
}

func TestMemoryStoreRejectsInvalidTask(t *testing.T) {
	store := NewMemoryStore(nil)
	_, err := store.Upsert(context.Background(), Task{Title: "bad", Status: "unknown"})
	if err == nil {
		t.Fatal("Upsert returned nil, want invalid status error")
	}
}

func TestDeleteToolReportsMissingTask(t *testing.T) {
	_, err := NewDeleteTool(NewMemoryStore(nil)).Execute(context.Background(), tool.Call{
		Use: model.ToolUse{
			ID:    "delete-1",
			Name:  DeleteToolName,
			Input: json.RawMessage(`{"id":"missing"}`),
		},
	})
	if err == nil {
		t.Fatal("Execute returned nil, want missing task error")
	}
}

func mustRunTool(t *testing.T, impl tool.Tool, use model.ToolUse) model.ToolResult {
	t.Helper()
	result, err := impl.Execute(context.Background(), tool.Call{Use: use})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	return result
}
