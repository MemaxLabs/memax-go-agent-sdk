package checkpointtools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/MemaxLabs/memax-go-agent-sdk/checkpoint"
	"github.com/MemaxLabs/memax-go-agent-sdk/model"
	"github.com/MemaxLabs/memax-go-agent-sdk/tool"
)

func TestCheckpointToolsCreateListRestoreDelete(t *testing.T) {
	manager := checkpoint.NewMemoryManager(nil)

	created := mustRunTool(t, NewCreateTool(manager), model.ToolUse{
		ID:    "create-1",
		Name:  CreateToolName,
		Input: json.RawMessage(`{"label":"before refactor","metadata":{"files":2}}`),
	}, tool.Runtime{SessionID: "session-1", ParentSessionID: "parent-1"})
	if created.Content != "created checkpoint checkpoint-1" {
		t.Fatalf("create content = %q, want checkpoint confirmation", created.Content)
	}
	if created.Metadata["session_id"] != "session-1" || created.Metadata["parent_id"] != "parent-1" {
		t.Fatalf("metadata = %#v, want session correlation", created.Metadata)
	}

	list := mustRunTool(t, NewListTool(manager), model.ToolUse{
		ID:    "list-1",
		Name:  ListToolName,
		Input: json.RawMessage(`{}`),
	}, tool.Runtime{SessionID: "session-1"})
	if !strings.Contains(list.Content, "checkpoint-1") || !strings.Contains(list.Content, "before refactor") {
		t.Fatalf("list content = %q, want checkpoint", list.Content)
	}
	if list.Metadata["count"] != 1 {
		t.Fatalf("list metadata = %#v, want count 1", list.Metadata)
	}

	restored := mustRunTool(t, NewRestoreTool(manager), model.ToolUse{
		ID:    "restore-1",
		Name:  RestoreToolName,
		Input: json.RawMessage(`{"id":"checkpoint-1"}`),
	}, tool.Runtime{})
	if restored.Content != "restored checkpoint checkpoint-1" {
		t.Fatalf("restore content = %q, want restore confirmation", restored.Content)
	}

	deleted := mustRunTool(t, NewDeleteTool(manager), model.ToolUse{
		ID:    "delete-1",
		Name:  DeleteToolName,
		Input: json.RawMessage(`{"id":"checkpoint-1"}`),
	}, tool.Runtime{})
	if deleted.Content != "deleted checkpoint checkpoint-1" {
		t.Fatalf("delete content = %q, want delete confirmation", deleted.Content)
	}
}

func TestListToolCanListAllCheckpoints(t *testing.T) {
	manager := checkpoint.NewMemoryManager(nil)
	_, err := manager.Create(context.Background(), checkpoint.CreateOptions{SessionID: "session-1", Label: "one"})
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}
	_, err = manager.Create(context.Background(), checkpoint.CreateOptions{SessionID: "session-2", Label: "two"})
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}

	filtered := mustRunTool(t, NewListTool(manager), model.ToolUse{
		ID:    "list-1",
		Name:  ListToolName,
		Input: json.RawMessage(`{}`),
	}, tool.Runtime{SessionID: "session-1"})
	if strings.Contains(filtered.Content, "session-2") {
		t.Fatalf("filtered content = %q, should default to current session", filtered.Content)
	}

	all := mustRunTool(t, NewListTool(manager), model.ToolUse{
		ID:    "list-2",
		Name:  ListToolName,
		Input: json.RawMessage(`{"all":true}`),
	}, tool.Runtime{SessionID: "session-1"})
	if !strings.Contains(all.Content, "session-1") || !strings.Contains(all.Content, "session-2") {
		t.Fatalf("all content = %q, want both sessions", all.Content)
	}
}

func TestRestoreToolReportsMissingCheckpoint(t *testing.T) {
	_, err := NewRestoreTool(checkpoint.NewMemoryManager(nil)).Execute(context.Background(), tool.Call{
		Use: model.ToolUse{
			ID:    "restore-1",
			Name:  RestoreToolName,
			Input: json.RawMessage(`{"id":"missing"}`),
		},
	})
	if err == nil {
		t.Fatal("Execute returned nil, want missing checkpoint error")
	}
}

func mustRunTool(t *testing.T, impl tool.Tool, use model.ToolUse, runtime tool.Runtime) model.ToolResult {
	t.Helper()
	result, err := impl.Execute(context.Background(), tool.Call{Use: use, Runtime: runtime})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	return result
}
