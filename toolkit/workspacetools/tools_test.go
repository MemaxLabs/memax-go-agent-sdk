package workspacetools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/MemaxLabs/memax-go-agent-sdk/model"
	"github.com/MemaxLabs/memax-go-agent-sdk/tool"
	"github.com/MemaxLabs/memax-go-agent-sdk/workspace"
)

func TestWorkspaceToolsPatchDiffAndRestore(t *testing.T) {
	store := workspace.NewMemoryStore(map[string]string{"README.md": "hello"})
	tools, err := NewTools(store)
	if err != nil {
		t.Fatalf("NewTools returned error: %v", err)
	}
	registry := tool.NewRegistry(tools...)

	patch := mustRun(t, registry, model.ToolUse{
		ID:   "patch-1",
		Name: ApplyPatchToolName,
		Input: json.RawMessage(`{"operations":[
			{"path":"README.md","old_content":"hello","new_content":"hello world"},
			{"path":"docs/new.md","new_content":"new"}
		]}`),
	})
	if !strings.Contains(patch.Content, "modified README.md") || !strings.Contains(patch.Content, "added docs/new.md") {
		t.Fatalf("patch content = %q", patch.Content)
	}
	if patch.Metadata[model.MetadataWorkspaceOperation] != "patch" || patch.Metadata[model.MetadataWorkspaceChanges] != 2 {
		t.Fatalf("patch metadata = %#v, want workspace patch metadata", patch.Metadata)
	}

	diff := mustRun(t, registry, model.ToolUse{ID: "diff-1", Name: DiffToolName, Input: json.RawMessage(`{}`)})
	if !strings.Contains(diff.Content, "modified README.md") || !strings.Contains(diff.Content, "added docs/new.md") {
		t.Fatalf("diff content = %q", diff.Content)
	}
	if diff.Metadata[model.MetadataWorkspaceOperation] != "diff" || diff.Metadata[model.MetadataWorkspaceBaseID] != "checkpoint-0" {
		t.Fatalf("diff metadata = %#v, want workspace diff metadata", diff.Metadata)
	}

	cp := mustRun(t, registry, model.ToolUse{ID: "cp-1", Name: CheckpointToolName, Input: json.RawMessage(`{"label":"after patch"}`)})
	if cp.Metadata["id"] != "checkpoint-1" || cp.Metadata[model.MetadataWorkspaceOperation] != "checkpoint" {
		t.Fatalf("checkpoint metadata = %#v, want checkpoint-1", cp.Metadata)
	}
	_ = mustRun(t, registry, model.ToolUse{
		ID:   "patch-2",
		Name: ApplyPatchToolName,
		Input: json.RawMessage(`{"operations":[
			{"path":"README.md","old_content":"hello world","new_content":"broken"}
		]}`),
	})
	restore := mustRun(t, registry, model.ToolUse{ID: "restore-1", Name: RestoreToolName, Input: json.RawMessage(`{"id":"checkpoint-1"}`)})
	if !strings.Contains(restore.Content, "restored workspace checkpoint checkpoint-1") {
		t.Fatalf("restore content = %q", restore.Content)
	}
	if restore.Metadata[model.MetadataWorkspaceOperation] != "restore" || restore.Metadata[model.MetadataWorkspaceCheckpointID] != "checkpoint-1" {
		t.Fatalf("restore metadata = %#v, want workspace restore metadata", restore.Metadata)
	}
	read := mustRun(t, registry, model.ToolUse{ID: "read-1", Name: ReadToolName, Input: json.RawMessage(`{"path":"README.md"}`)})
	if read.Content != "hello world" {
		t.Fatalf("read content = %q, want restored hello world", read.Content)
	}
}

func TestApplyPatchToolRejectsAmbiguousOperation(t *testing.T) {
	store := workspace.NewMemoryStore(map[string]string{"README.md": "hello"})
	result, err := NewApplyPatchTool(store).Execute(context.Background(), tool.Call{
		Use: model.ToolUse{
			ID:    "patch-1",
			Name:  ApplyPatchToolName,
			Input: json.RawMessage(`{"operations":[{"path":"README.md"}]}`),
		},
	})
	if err == nil {
		t.Fatalf("Execute returned nil error with result %#v, want validation error", result)
	}
	content, readErr := store.ReadFile(context.Background(), "README.md")
	if readErr != nil {
		t.Fatalf("ReadFile returned error: %v", readErr)
	}
	if content != "hello" {
		t.Fatalf("content = %q, want unchanged", content)
	}
}

func mustRun(t *testing.T, registry *tool.Registry, use model.ToolUse) model.ToolResult {
	t.Helper()
	impl, ok := registry.Get(use.Name)
	if !ok {
		t.Fatalf("tool %q not registered", use.Name)
	}
	result, err := impl.Execute(context.Background(), tool.Call{Use: use})
	if err != nil {
		t.Fatalf("Execute %s returned error: %v", use.Name, err)
	}
	return result
}
