package workspacetools

import (
	"context"
	"encoding/json"
	"fmt"
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
	if patch.Metadata[model.MetadataWorkspaceAdded] != 1 || patch.Metadata[model.MetadataWorkspaceModified] != 1 {
		t.Fatalf("patch summary metadata = %#v, want add and modify counts", patch.Metadata)
	}

	diff := mustRun(t, registry, model.ToolUse{ID: "diff-1", Name: DiffToolName, Input: json.RawMessage(`{}`)})
	if !strings.Contains(diff.Content, "modified README.md") || !strings.Contains(diff.Content, "added docs/new.md") {
		t.Fatalf("diff content = %q", diff.Content)
	}
	if diff.Metadata[model.MetadataWorkspaceOperation] != "diff" || diff.Metadata[model.MetadataWorkspaceBaseID] != "checkpoint-0" {
		t.Fatalf("diff metadata = %#v, want workspace diff metadata", diff.Metadata)
	}
	if diff.Metadata[model.MetadataWorkspaceAdded] != 1 || diff.Metadata[model.MetadataWorkspaceModified] != 1 {
		t.Fatalf("diff summary metadata = %#v, want add and modify counts", diff.Metadata)
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

func TestApplyPatchToolAppliesUnifiedDiffAndDryRun(t *testing.T) {
	store := workspace.NewMemoryStore(map[string]string{"README.md": "hello\nworld"})
	dryRun := mustExecute(t, NewApplyPatchTool(store), model.ToolUse{
		ID:   "patch-1",
		Name: ApplyPatchToolName,
		Input: json.RawMessage(`{
			"dry_run": true,
			"unified_diff": "--- a/README.md\n+++ b/README.md\n@@ -1,2 +1,2 @@\n hello\n-world\n+workspace"
		}`),
	})
	if !strings.Contains(dryRun.Content, "modified README.md") || dryRun.Metadata["dry_run"] != true {
		t.Fatalf("dry-run result = %#v, want preview metadata", dryRun)
	}
	content, err := store.ReadFile(context.Background(), "README.md")
	if err != nil {
		t.Fatalf("ReadFile returned error: %v", err)
	}
	if content != "hello\nworld" {
		t.Fatalf("content = %q, want dry-run to leave file unchanged", content)
	}

	applied := mustExecute(t, NewApplyPatchTool(store), model.ToolUse{
		ID:   "patch-2",
		Name: ApplyPatchToolName,
		Input: json.RawMessage(`{
			"unified_diff": "--- a/README.md\n+++ b/README.md\n@@ -1,2 +1,2 @@\n hello\n-world\n+workspace"
		}`),
	})
	if applied.Metadata[model.MetadataWorkspaceOperation] != "patch" || applied.Metadata[model.MetadataWorkspaceChanges] != 1 {
		t.Fatalf("applied metadata = %#v, want workspace patch metadata", applied.Metadata)
	}
	content, err = store.ReadFile(context.Background(), "README.md")
	if err != nil {
		t.Fatalf("ReadFile returned error: %v", err)
	}
	if content != "hello\nworkspace" {
		t.Fatalf("content = %q, want applied unified diff", content)
	}
}

func TestApplyPatchToolRejectsMultiplePatchFormats(t *testing.T) {
	store := workspace.NewMemoryStore(map[string]string{"README.md": "hello"})
	_, err := NewApplyPatchTool(store).Execute(context.Background(), tool.Call{
		Use: model.ToolUse{
			ID:   "patch-1",
			Name: ApplyPatchToolName,
			Input: json.RawMessage(`{
				"operations":[{"path":"README.md","new_content":"changed"}],
				"unified_diff":"--- a/README.md\n+++ b/README.md\n@@ -1 +1 @@\n-hello\n+changed"
			}`),
		},
	})
	if err == nil || !strings.Contains(err.Error(), "exactly one") {
		t.Fatalf("Execute error = %v, want one-format validation", err)
	}
}

func TestApplyPatchToolReviewerDeniesWithoutMutation(t *testing.T) {
	store := workspace.NewMemoryStore(map[string]string{"README.md": "hello"})
	var got PatchReviewRequest
	reviewer := PatchReviewerFunc(func(_ context.Context, req PatchReviewRequest) (PatchReviewDecision, error) {
		got = req
		return PatchReviewDecision{Allow: false, Reason: "README edits need approval"}, nil
	})
	result, err := NewApplyPatchToolWithReview(store, reviewer).Execute(context.Background(), tool.Call{
		Use: model.ToolUse{
			ID:    "patch-1",
			Name:  ApplyPatchToolName,
			Input: json.RawMessage(`{"operations":[{"path":"README.md","old_content":"hello","new_content":"changed"}]}`),
		},
	})
	if err == nil {
		t.Fatalf("Execute returned nil error with result %#v, want reviewer denial", result)
	}
	if !strings.Contains(err.Error(), "README edits need approval") {
		t.Fatalf("Execute error = %v, want reviewer reason", err)
	}
	if got.ToolUse.ID != "patch-1" || got.DryRun || got.Summary.Modified != 1 || got.Summary.Files != 1 {
		t.Fatalf("review request = %#v, want mutation preview summary", got)
	}
	content, readErr := store.ReadFile(context.Background(), "README.md")
	if readErr != nil {
		t.Fatalf("ReadFile returned error: %v", readErr)
	}
	if content != "hello" {
		t.Fatalf("content = %q, want reviewer denial to leave file unchanged", content)
	}
}

func TestApplyPatchToolReviewerObservesDryRun(t *testing.T) {
	store := workspace.NewMemoryStore(map[string]string{"README.md": "hello"})
	var got PatchReviewRequest
	reviewer := PatchReviewerFunc(func(_ context.Context, req PatchReviewRequest) (PatchReviewDecision, error) {
		got = req
		return PatchReviewDecision{Allow: true}, nil
	})
	result := mustExecute(t, NewApplyPatchToolWithReview(store, reviewer), model.ToolUse{
		ID:    "patch-1",
		Name:  ApplyPatchToolName,
		Input: json.RawMessage(`{"dry_run":true,"operations":[{"path":"README.md","old_content":"hello","new_content":"changed"}]}`),
	})
	if !strings.HasPrefix(result.Content, "dry run:") || result.Metadata["dry_run"] != true {
		t.Fatalf("result = %#v, want dry-run preview", result)
	}
	if !got.DryRun || got.Summary.Modified != 1 {
		t.Fatalf("review request = %#v, want dry-run review summary", got)
	}
	content, err := store.ReadFile(context.Background(), "README.md")
	if err != nil {
		t.Fatalf("ReadFile returned error: %v", err)
	}
	if content != "hello" {
		t.Fatalf("content = %q, want dry-run to leave file unchanged", content)
	}
}

func TestApplyPatchToolReviewerErrorBlocksMutation(t *testing.T) {
	store := workspace.NewMemoryStore(map[string]string{"README.md": "hello"})
	reviewer := PatchReviewerFunc(func(context.Context, PatchReviewRequest) (PatchReviewDecision, error) {
		return PatchReviewDecision{}, fmt.Errorf("policy service unavailable")
	})
	result, err := NewApplyPatchToolWithReview(store, reviewer).Execute(context.Background(), tool.Call{
		Use: model.ToolUse{
			ID:    "patch-1",
			Name:  ApplyPatchToolName,
			Input: json.RawMessage(`{"operations":[{"path":"README.md","old_content":"hello","new_content":"changed"}]}`),
		},
	})
	if err == nil {
		t.Fatalf("Execute returned nil error with result %#v, want reviewer error", result)
	}
	if !strings.Contains(err.Error(), "policy service unavailable") {
		t.Fatalf("Execute error = %v, want wrapped reviewer error", err)
	}
	content, readErr := store.ReadFile(context.Background(), "README.md")
	if readErr != nil {
		t.Fatalf("ReadFile returned error: %v", readErr)
	}
	if content != "hello" {
		t.Fatalf("content = %q, want reviewer error to leave file unchanged", content)
	}
}

func TestApprovalSummaryFromPatchInput(t *testing.T) {
	summary, err := ApprovalSummaryFromPatchInput([]byte(`{"operations":[
		{"path":"README.md","old_content":"old","new_content":"newer"},
		{"path":"docs/new.md","new_content":"new"},
		{"path":"docs/old.md","old_content":"gone","delete":true}
	]}`))
	if err != nil {
		t.Fatalf("ApprovalSummaryFromPatchInput returned error: %v", err)
	}
	if summary.Title != "Review workspace patch" || summary.Risk != "workspace mutation" || summary.Changes != 3 || summary.Modified != 1 || summary.Added != 1 || summary.Deleted != 1 {
		t.Fatalf("summary = %#v, want change counts", summary)
	}
	if !sameStringSlice(summary.Paths, []string{"README.md", "docs/new.md", "docs/old.md"}) {
		t.Fatalf("paths = %#v, want affected paths", summary.Paths)
	}
	if summary.ByteDelta != 1 {
		t.Fatalf("byte delta = %d, want old->newer (+2), add new (+3), delete gone (-4)", summary.ByteDelta)
	}

	diffSummary, err := ApprovalSummaryFromPatchInput([]byte(`{"unified_diff":"--- a/README.md\n+++ b/README.md\n@@ -1 +1 @@\n-old\n+new"}`))
	if err != nil {
		t.Fatalf("ApprovalSummaryFromPatchInput diff returned error: %v", err)
	}
	if diffSummary.Changes != 1 || !sameStringSlice(diffSummary.Paths, []string{"README.md"}) {
		t.Fatalf("diff summary = %#v, want README path", diffSummary)
	}
}

func sameStringSlice(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func mustRun(t *testing.T, registry *tool.Registry, use model.ToolUse) model.ToolResult {
	t.Helper()
	impl, ok := registry.Get(use.Name)
	if !ok {
		t.Fatalf("tool %q not registered", use.Name)
	}
	return mustExecute(t, impl, use)
}

func mustExecute(t *testing.T, impl tool.Tool, use model.ToolUse) model.ToolResult {
	t.Helper()
	result, err := impl.Execute(context.Background(), tool.Call{Use: use})
	if err != nil {
		t.Fatalf("Execute %s returned error: %v", use.Name, err)
	}
	return result
}
