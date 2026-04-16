package scenarios

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	memaxagent "github.com/MemaxLabs/memax-go-agent-sdk"
	"github.com/MemaxLabs/memax-go-agent-sdk/agenteval"
	"github.com/MemaxLabs/memax-go-agent-sdk/model"
	"github.com/MemaxLabs/memax-go-agent-sdk/tool"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/workspacetools"
	"github.com/MemaxLabs/memax-go-agent-sdk/workspace"
)

// WorkspacePatchCheckpointRollback returns a single-use scenario where the
// model creates a workspace checkpoint, applies a guarded patch, inspects the
// diff, restores the checkpoint, and verifies the file content.
func WorkspacePatchCheckpointRollback() agenteval.Case {
	store := workspace.NewMemoryStore(map[string]string{
		"README.md": "version one",
	})
	tools, toolsErr := workspacetools.NewTools(store)
	modelClient := agenteval.NewScriptedModel(
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:    "checkpoint-1",
				Name:  workspacetools.CheckpointToolName,
				Input: json.RawMessage(`{"label":"before patch"}`),
			},
		}},
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:   "patch-1",
				Name: workspacetools.ApplyPatchToolName,
				Input: json.RawMessage(`{"operations":[
					{"path":"README.md","old_content":"version one","new_content":"version two"}
				]}`),
			},
		}},
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:    "diff-1",
				Name:  workspacetools.DiffToolName,
				Input: json.RawMessage(`{}`),
			},
		}},
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:    "restore-1",
				Name:  workspacetools.RestoreToolName,
				Input: json.RawMessage(`{"id":"checkpoint-1"}`),
			},
		}},
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:    "read-1",
				Name:  workspacetools.ReadToolName,
				Input: json.RawMessage(`{"path":"README.md"}`),
			},
		}},
		[]model.StreamEvent{{Kind: model.StreamText, Text: "Workspace patch was rolled back."}},
	)

	return agenteval.Case{
		Name:   "workspace_patch_checkpoint_rollback",
		Prompt: "Patch README.md, inspect the diff, then roll back to the checkpoint.",
		Options: memaxagent.Options{
			Model: modelClient,
			Tools: tool.NewRegistry(tools...),
		},
		Assertions: []agenteval.Assertion{
			toolConstructionSucceeded(toolsErr),
			agenteval.ToolUsed(workspacetools.CheckpointToolName),
			agenteval.ToolUsed(workspacetools.ApplyPatchToolName),
			agenteval.ToolUsed(workspacetools.DiffToolName),
			agenteval.ToolUsed(workspacetools.RestoreToolName),
			agenteval.ToolUsed(workspacetools.ReadToolName),
			agenteval.NoToolErrors(),
			agenteval.FinalEquals("Workspace patch was rolled back."),
			requestCountEquals(modelClient, 6),
			{
				Name: "workspace diff and restore are observable",
				Check: func(result agenteval.Result) error {
					sawPatch := false
					sawDiff := false
					sawRestore := false
					sawRestoredRead := false
					for _, toolResult := range result.ToolResults() {
						switch toolResult.Name {
						case workspacetools.ApplyPatchToolName:
							sawPatch = strings.Contains(toolResult.Content, "modified README.md")
						case workspacetools.DiffToolName:
							sawDiff = strings.Contains(toolResult.Content, "modified README.md")
						case workspacetools.RestoreToolName:
							sawRestore = strings.Contains(toolResult.Content, "restored workspace checkpoint checkpoint-1")
						case workspacetools.ReadToolName:
							sawRestoredRead = toolResult.Content == "version one"
						}
					}
					if !sawPatch || !sawDiff || !sawRestore || !sawRestoredRead {
						return fmt.Errorf("tool results missing workspace lifecycle: %#v", result.ToolResults())
					}
					content, err := store.ReadFile(context.Background(), "README.md")
					if err != nil {
						return err
					}
					if content != "version one" {
						return fmt.Errorf("README.md = %q, want rollback to version one", content)
					}
					return nil
				},
			},
		},
	}
}

// WorkspaceUnifiedDiffRecovery returns a single-use scenario where a unified
// diff conflicts, the model receives actionable diagnostics, and a corrected
// diff succeeds on the next turn.
func WorkspaceUnifiedDiffRecovery() agenteval.Case {
	store := workspace.NewMemoryStore(map[string]string{
		"README.md": "hello\nactual\nfooter",
	})
	tools, toolsErr := workspacetools.NewTools(store)
	modelClient := agenteval.NewScriptedModel(
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:   "patch-1",
				Name: workspacetools.ApplyPatchToolName,
				Input: json.RawMessage(`{
					"unified_diff":"--- a/README.md\n+++ b/README.md\n@@ -1,3 +1,3 @@\n hello\n-expected\n+changed\n footer"
				}`),
			},
		}},
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:   "patch-2",
				Name: workspacetools.ApplyPatchToolName,
				Input: json.RawMessage(`{
					"unified_diff":"--- a/README.md\n+++ b/README.md\n@@ -1,3 +1,3 @@\n hello\n-actual\n+changed\n footer"
				}`),
			},
		}},
		[]model.StreamEvent{{Kind: model.StreamText, Text: "Unified diff repaired and applied."}},
	)

	return agenteval.Case{
		Name:   "workspace_unified_diff_recovery",
		Prompt: "Patch README.md using a unified diff. Recover if the patch is stale.",
		Options: memaxagent.Options{
			Model: modelClient,
			Tools: tool.NewRegistry(tools...),
		},
		Assertions: []agenteval.Assertion{
			toolConstructionSucceeded(toolsErr),
			agenteval.ToolUsed(workspacetools.ApplyPatchToolName),
			agenteval.FinalEquals("Unified diff repaired and applied."),
			requestCountEquals(modelClient, 3),
			{
				Name: "conflict diagnostics guide repair",
				Check: func(result agenteval.Result) error {
					results := result.ToolResults()
					if len(results) < 2 {
						return fmt.Errorf("tool results = %#v, want conflict and success", results)
					}
					if !results[0].IsError || !strings.Contains(results[0].Content, "nearby current content") || !strings.Contains(results[0].Content, "expected") {
						return fmt.Errorf("first patch result = %#v, want actionable conflict", results[0])
					}
					if results[1].IsError || !strings.Contains(results[1].Content, "modified README.md") {
						return fmt.Errorf("second patch result = %#v, want successful patch", results[1])
					}
					content, err := store.ReadFile(context.Background(), "README.md")
					if err != nil {
						return err
					}
					if content != "hello\nchanged\nfooter" {
						return fmt.Errorf("README.md = %q, want repaired unified diff applied", content)
					}
					return nil
				},
			},
		},
	}
}

// WorkspacePatchReviewDenialRecovery returns a single-use scenario where a
// host reviewer denies one patch and the model recovers with an allowed patch.
func WorkspacePatchReviewDenialRecovery() agenteval.Case {
	store := workspace.NewMemoryStore(map[string]string{
		"README.md": "locked",
	})
	reviewer := workspacetools.PatchReviewerFunc(func(_ context.Context, req workspacetools.PatchReviewRequest) workspacetools.PatchReviewDecision {
		for _, path := range req.Summary.Paths {
			if path == "README.md" {
				return workspacetools.PatchReviewDecision{Allow: false, Reason: "README.md is locked"}
			}
		}
		return workspacetools.PatchReviewDecision{Allow: true}
	})
	modelClient := agenteval.NewScriptedModel(
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:   "patch-1",
				Name: workspacetools.ApplyPatchToolName,
				Input: json.RawMessage(`{
					"operations":[{"path":"README.md","old_content":"locked","new_content":"changed"}]
				}`),
			},
		}},
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:   "patch-2",
				Name: workspacetools.ApplyPatchToolName,
				Input: json.RawMessage(`{
					"operations":[{"path":"docs/notes.md","new_content":"safe note"}]
				}`),
			},
		}},
		[]model.StreamEvent{{Kind: model.StreamText, Text: "Applied allowed workspace patch."}},
	)

	return agenteval.Case{
		Name:   "workspace_patch_review_denial_recovery",
		Prompt: "Update workspace files. Recover if the host denies a patch.",
		Options: memaxagent.Options{
			Model: modelClient,
			Tools: tool.NewRegistry(workspacetools.NewApplyPatchToolWithReview(store, reviewer)),
		},
		Assertions: []agenteval.Assertion{
			agenteval.ToolUsed(workspacetools.ApplyPatchToolName),
			agenteval.FinalEquals("Applied allowed workspace patch."),
			requestCountEquals(modelClient, 3),
			{
				Name: "review denial is recoverable and atomic",
				Check: func(result agenteval.Result) error {
					results := result.ToolResults()
					if len(results) < 2 {
						return fmt.Errorf("tool results = %#v, want denial and success", results)
					}
					if !results[0].IsError || !strings.Contains(results[0].Content, "README.md is locked") {
						return fmt.Errorf("first patch result = %#v, want reviewer denial", results[0])
					}
					if results[1].IsError || !strings.Contains(results[1].Content, "added docs/notes.md") {
						return fmt.Errorf("second patch result = %#v, want allowed patch", results[1])
					}
					readme, err := store.ReadFile(context.Background(), "README.md")
					if err != nil {
						return err
					}
					if readme != "locked" {
						return fmt.Errorf("README.md = %q, want denial to prevent mutation", readme)
					}
					notes, err := store.ReadFile(context.Background(), "docs/notes.md")
					if err != nil {
						return err
					}
					if notes != "safe note" {
						return fmt.Errorf("docs/notes.md = %q, want allowed patch", notes)
					}
					return nil
				},
			},
		},
	}
}
