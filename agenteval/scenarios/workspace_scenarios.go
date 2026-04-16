package scenarios

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	memaxagent "github.com/MemaxLabs/memax-go-agent-sdk"
	"github.com/MemaxLabs/memax-go-agent-sdk/agenteval"
	"github.com/MemaxLabs/memax-go-agent-sdk/hook"
	"github.com/MemaxLabs/memax-go-agent-sdk/model"
	"github.com/MemaxLabs/memax-go-agent-sdk/planner"
	"github.com/MemaxLabs/memax-go-agent-sdk/tool"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/agentpolicy"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/tasktools"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/verifytools"
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
	reviewer := workspacetools.PatchReviewerFunc(func(_ context.Context, req workspacetools.PatchReviewRequest) (workspacetools.PatchReviewDecision, error) {
		for _, path := range req.Summary.Paths {
			if path == "README.md" {
				return workspacetools.PatchReviewDecision{Allow: false, Reason: "README.md is locked"}, nil
			}
		}
		return workspacetools.PatchReviewDecision{Allow: true}, nil
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

// WorkspaceOSStorePatchRollback returns a single-use scenario that exercises
// the standard workspace tools against a root-confined host directory.
func WorkspaceOSStorePatchRollback() agenteval.Case {
	root, setupErr := os.MkdirTemp("", "memax-workspace-*")
	if setupErr == nil {
		setupErr = os.WriteFile(filepath.Join(root, "README.md"), []byte("version one"), 0o644)
	}
	var store *workspace.OSStore
	if setupErr == nil {
		store, setupErr = workspace.NewOSStore(root)
	}
	var tools []tool.Tool
	var toolsErr error
	if setupErr == nil {
		tools, toolsErr = workspacetools.NewTools(store)
	}
	modelClient := agenteval.NewScriptedModel(
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:    "checkpoint-1",
				Name:  workspacetools.CheckpointToolName,
				Input: json.RawMessage(`{"label":"before disk patch"}`),
			},
		}},
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:   "patch-1",
				Name: workspacetools.ApplyPatchToolName,
				Input: json.RawMessage(`{
					"unified_diff":"--- a/README.md\n+++ b/README.md\n@@ -1 +1 @@\n-version one\n+version two"
				}`),
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
		[]model.StreamEvent{{Kind: model.StreamText, Text: "Disk workspace restored."}},
	)

	return agenteval.Case{
		Name:   "workspace_os_store_patch_rollback",
		Prompt: "Patch README.md on disk, then restore the checkpoint.",
		Options: memaxagent.Options{
			Model: modelClient,
			Tools: tool.NewRegistry(tools...),
		},
		Cleanup: func() {
			if root != "" {
				_ = os.RemoveAll(root)
			}
		},
		Assertions: []agenteval.Assertion{
			setupSucceeded(setupErr),
			toolConstructionSucceeded(toolsErr),
			agenteval.ToolUsed(workspacetools.CheckpointToolName),
			agenteval.ToolUsed(workspacetools.ApplyPatchToolName),
			agenteval.ToolUsed(workspacetools.RestoreToolName),
			agenteval.NoToolErrors(),
			agenteval.FinalEquals("Disk workspace restored."),
			requestCountEquals(modelClient, 4),
			{
				Name: "disk content restored",
				Check: func(result agenteval.Result) error {
					content, err := os.ReadFile(filepath.Join(root, "README.md"))
					if err != nil {
						return err
					}
					if string(content) != "version one" {
						return fmt.Errorf("README.md = %q, want restored version one", content)
					}
					return nil
				},
			},
		},
	}
}

// WorkspaceCheckpointPolicyRecovery returns a single-use scenario where a
// preset hook policy denies patching until the model creates a checkpoint, then
// the model recovers and applies the patch.
func WorkspaceCheckpointPolicyRecovery() agenteval.Case {
	store := workspace.NewMemoryStore(map[string]string{
		"README.md": "status: old",
	})
	workspaceTools, toolsErr := workspacetools.NewTools(store)
	policy := agentpolicy.RequireCheckpointBeforePatch()
	modelClient := agenteval.NewScriptedModel(
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:   "patch-1",
				Name: workspacetools.ApplyPatchToolName,
				Input: json.RawMessage(`{"operations":[
					{"path":"README.md","old_content":"status: old","new_content":"status: new"}
				]}`),
			},
		}},
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:    "checkpoint-1",
				Name:  workspacetools.CheckpointToolName,
				Input: json.RawMessage(`{"label":"before README patch"}`),
			},
		}},
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:   "patch-2",
				Name: workspacetools.ApplyPatchToolName,
				Input: json.RawMessage(`{"operations":[
					{"path":"README.md","old_content":"status: old","new_content":"status: new"}
				]}`),
			},
		}},
		[]model.StreamEvent{{Kind: model.StreamText, Text: "Patched after checkpoint."}},
	)

	return agenteval.Case{
		Name:   "workspace_checkpoint_policy_recovery",
		Prompt: "Patch README.md safely.",
		Options: memaxagent.Options{
			Model: modelClient,
			Tools: tool.NewRegistry(workspaceTools...),
			Hooks: hook.NewRunner(policy.Options()...),
		},
		Assertions: []agenteval.Assertion{
			toolConstructionSucceeded(toolsErr),
			agenteval.ToolUsed(workspacetools.ApplyPatchToolName),
			agenteval.ToolUsed(workspacetools.CheckpointToolName),
			agenteval.FinalEquals("Patched after checkpoint."),
			requestCountEquals(modelClient, 4),
			{
				Name: "checkpoint policy denial drives recovery",
				Check: func(result agenteval.Result) error {
					results := result.ToolResults()
					if len(results) < 3 {
						return fmt.Errorf("tool results = %#v, want denied patch checkpoint patch", results)
					}
					if !results[0].IsError || !strings.Contains(results[0].Content, agentpolicy.CheckpointBeforePatchReason()) {
						return fmt.Errorf("first patch result = %#v, want checkpoint denial", results[0])
					}
					if results[1].IsError || !strings.Contains(results[1].Content, "created workspace checkpoint") {
						return fmt.Errorf("checkpoint result = %#v, want checkpoint success", results[1])
					}
					if results[2].IsError || !strings.Contains(results[2].Content, "modified README.md") {
						return fmt.Errorf("second patch result = %#v, want patch success", results[2])
					}
					content, err := store.ReadFile(context.Background(), "README.md")
					if err != nil {
						return err
					}
					if content != "status: new" {
						return fmt.Errorf("README.md = %q, want patched content", content)
					}
					return nil
				},
			},
		},
	}
}

// WorkspaceRollbackPolicyRecovery returns a single-use scenario where a failed
// verification includes model-visible rollback guidance from an agent policy,
// and the model restores the latest checkpoint through the normal workspace
// tool.
func WorkspaceRollbackPolicyRecovery() agenteval.Case {
	store := workspace.NewMemoryStore(map[string]string{
		"README.md": "status: good",
	})
	workspaceTools, toolsErr := workspacetools.NewTools(store)
	policy := agentpolicy.RecommendRollbackOnFailedVerification()
	verifyTool := verifytools.NewTool(verifytools.Config{
		Verifier: policy.WrapVerifier(verifierForReadmeStatus(store, "status: good")),
	})
	modelClient := agenteval.NewScriptedModel(
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:    "checkpoint-1",
				Name:  workspacetools.CheckpointToolName,
				Input: json.RawMessage(`{"label":"before risky edit"}`),
			},
		}},
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:   "patch-1",
				Name: workspacetools.ApplyPatchToolName,
				Input: json.RawMessage(`{"operations":[
					{"path":"README.md","old_content":"status: good","new_content":"status: bad"}
				]}`),
			},
		}},
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:    "verify-1",
				Name:  verifytools.ToolName,
				Input: json.RawMessage(`{"name":"test","target":"README.md"}`),
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
		[]model.StreamEvent{{Kind: model.StreamText, Text: "Restored after rollback policy guidance."}},
	)

	return agenteval.Case{
		Name:   "workspace_rollback_policy_recovery",
		Prompt: "Checkpoint, patch README.md, verify it, and follow rollback policy guidance if verification fails.",
		Options: memaxagent.Options{
			Model: modelClient,
			Tools: tool.NewRegistry(append(workspaceTools, verifyTool)...),
			Hooks: hook.NewRunner(policy.Options()...),
		},
		Assertions: []agenteval.Assertion{
			toolConstructionSucceeded(toolsErr),
			agenteval.ToolUsed(workspacetools.CheckpointToolName),
			agenteval.ToolUsed(workspacetools.ApplyPatchToolName),
			agenteval.ToolUsed(verifytools.ToolName),
			agenteval.ToolUsed(workspacetools.RestoreToolName),
			agenteval.FinalEquals("Restored after rollback policy guidance."),
			requestCountEquals(modelClient, 5),
			{
				Name: "rollback policy guidance drives explicit restore",
				Check: func(result agenteval.Result) error {
					results := result.ToolResults()
					if len(results) < 4 {
						return fmt.Errorf("tool results = %#v, want checkpoint patch verify restore", results)
					}
					if !results[2].IsError || !strings.Contains(results[2].Content, "restore workspace checkpoint checkpoint-1") {
						return fmt.Errorf("verification result = %#v, want rollback guidance", results[2])
					}
					if results[2].Metadata[agentpolicy.MetadataRollbackRecommended] != true {
						return fmt.Errorf("verification metadata = %#v, want rollback recommendation", results[2].Metadata)
					}
					if results[2].Metadata[agentpolicy.MetadataRollbackCheckpointID] != "checkpoint-1" {
						return fmt.Errorf("verification metadata = %#v, want checkpoint-1", results[2].Metadata)
					}
					if results[3].IsError || !strings.Contains(results[3].Content, "restored workspace checkpoint checkpoint-1") {
						return fmt.Errorf("restore result = %#v, want checkpoint restore", results[3])
					}
					content, err := store.ReadFile(context.Background(), "README.md")
					if err != nil {
						return err
					}
					if content != "status: good" {
						return fmt.Errorf("README.md = %q, want rollback to good status", content)
					}
					return nil
				},
			},
		},
	}
}

// PlannerTaskProgressFromVerification returns a single-use scenario where
// verification results update task state and the next planner prompt reflects
// completed progress before the model finalizes.
func PlannerTaskProgressFromVerification() agenteval.Case {
	workspaceStore := workspace.NewMemoryStore(map[string]string{
		"README.md": "status: broken",
	})
	taskStore := tasktools.NewMemoryStore([]tasktools.Task{{
		ID:     "task-1",
		Title:  "repair README status",
		Status: tasktools.StatusInProgress,
	}})
	workspaceTools, toolsErr := workspacetools.NewTools(workspaceStore)
	verifyTool := verifytools.NewTool(verifytools.Config{
		Verifier: tasktools.NewVerificationProgressVerifier(
			taskStore,
			verifierForReadmeStatus(workspaceStore, "status: fixed"),
			tasktools.WithVerificationFailStatus(tasktools.StatusInProgress),
		),
	})
	modelClient := agenteval.NewScriptedModel(
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:   "patch-1",
				Name: workspacetools.ApplyPatchToolName,
				Input: json.RawMessage(`{"operations":[
					{"path":"README.md","old_content":"status: broken","new_content":"status: almost"}
				]}`),
			},
		}},
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:   "verify-1",
				Name: verifytools.ToolName,
				Input: json.RawMessage(`{
					"name":"test",
					"target":"README.md",
					"metadata":{"task_id":"task-1"}
				}`),
			},
		}},
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:   "patch-2",
				Name: workspacetools.ApplyPatchToolName,
				Input: json.RawMessage(`{"operations":[
					{"path":"README.md","old_content":"status: almost","new_content":"status: fixed"}
				]}`),
			},
		}},
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:   "verify-2",
				Name: verifytools.ToolName,
				Input: json.RawMessage(`{
					"name":"test",
					"target":"README.md",
					"metadata":{"task_id":"task-1"}
				}`),
			},
		}},
		[]model.StreamEvent{{Kind: model.StreamText, Text: "Task completed after verification."}},
	)

	return agenteval.Case{
		Name:   "planner_task_progress_from_verification",
		Prompt: "Repair README.md, verify it, and keep task progress current.",
		Options: memaxagent.Options{
			Model: modelClient,
			Tools: tool.NewRegistry(append(workspaceTools, verifyTool)...),
			Planner: tasktools.Planner(taskStore,
				planner.WithTaskGoal("repair README with verified evidence"),
				planner.WithTaskToolHints(workspacetools.ApplyPatchToolName, verifytools.ToolName),
				planner.WithTaskVerificationHints("call workspace_verify with metadata.task_id before final answer"),
			),
		},
		Assertions: []agenteval.Assertion{
			toolConstructionSucceeded(toolsErr),
			agenteval.ToolUsed(workspacetools.ApplyPatchToolName),
			agenteval.ToolUsed(verifytools.ToolName),
			agenteval.FinalEquals("Task completed after verification."),
			requestCountEquals(modelClient, 5),
			{
				Name: "verification updates task progress before final prompt",
				Check: func(result agenteval.Result) error {
					results := result.ToolResults()
					if len(results) < 4 {
						return fmt.Errorf("tool results = %#v, want patch verify patch verify", results)
					}
					if results[1].Metadata[model.MetadataTaskStatus] != string(tasktools.StatusInProgress) {
						return fmt.Errorf("first verification metadata = %#v, want in-progress task update", results[1].Metadata)
					}
					if results[3].Metadata[model.MetadataTaskStatus] != string(tasktools.StatusCompleted) {
						return fmt.Errorf("second verification metadata = %#v, want completed task update", results[3].Metadata)
					}
					tasks, err := taskStore.List(context.Background())
					if err != nil {
						return err
					}
					if len(tasks) != 1 || tasks[0].Status != tasktools.StatusCompleted {
						return fmt.Errorf("tasks = %#v, want completed task", tasks)
					}
					if len(tasks[0].Evidence) == 0 || !strings.Contains(strings.Join(tasks[0].Evidence, ","), "verification:test") {
						return fmt.Errorf("task evidence = %#v, want verification evidence", tasks[0].Evidence)
					}
					requests := modelClient.Requests()
					if len(requests) < 5 {
						return fmt.Errorf("requests = %d, want final request after verification", len(requests))
					}
					finalPrompt := requests[4].SystemPrompt
					for _, want := range []string{"[completed] task-1", "verification test passed", "verification:test"} {
						if !strings.Contains(finalPrompt, want) {
							return fmt.Errorf("final prompt missing %q:\n%s", want, finalPrompt)
						}
					}
					return nil
				},
			},
		},
	}
}

// PlannerVerificationGuidesRepair returns a single-use scenario where the
// host plan names the verification phase, the model uses the verifier, and a
// failed check drives a repair before the final answer.
func PlannerVerificationGuidesRepair() agenteval.Case {
	store := workspace.NewMemoryStore(map[string]string{
		"README.md": "status: broken",
	})
	workspaceTools, toolsErr := workspacetools.NewTools(store)
	verifyTool := verifytools.NewTool(verifytools.Config{
		Verifier: verifierForReadmeStatus(store, "status: fixed"),
	})
	modelClient := agenteval.NewScriptedModel(
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:   "patch-1",
				Name: workspacetools.ApplyPatchToolName,
				Input: json.RawMessage(`{"operations":[
					{"path":"README.md","old_content":"status: broken","new_content":"status: almost"}
				]}`),
			},
		}},
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:    "verify-1",
				Name:  verifytools.ToolName,
				Input: json.RawMessage(`{"name":"test","target":"README.md"}`),
			},
		}},
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:   "patch-2",
				Name: workspacetools.ApplyPatchToolName,
				Input: json.RawMessage(`{"operations":[
					{"path":"README.md","old_content":"status: almost","new_content":"status: fixed"}
				]}`),
			},
		}},
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:    "verify-2",
				Name:  verifytools.ToolName,
				Input: json.RawMessage(`{"name":"test","target":"README.md"}`),
			},
		}},
		[]model.StreamEvent{{Kind: model.StreamText, Text: "Planner-guided verification passed."}},
	)

	return agenteval.Case{
		Name:   "planner_verification_guides_repair",
		Prompt: "Fix README.md and follow the host plan before finalizing.",
		Options: memaxagent.Options{
			Model: modelClient,
			Tools: tool.NewRegistry(append(workspaceTools, verifyTool)...),
			Planner: planner.Static(planner.Plan{
				Goal:  "repair README and prove the result",
				State: planner.StateActive,
				Steps: []planner.Step{{
					ID:                "fix-readme",
					Title:             "patch README and verify status",
					Status:            planner.StatusInProgress,
					ToolHints:         []string{workspacetools.ApplyPatchToolName, verifytools.ToolName},
					VerificationHints: []string{"run workspace_verify test on README.md before final answer"},
					Evidence:          []string{"README.md"},
				}},
			}),
		},
		Assertions: []agenteval.Assertion{
			toolConstructionSucceeded(toolsErr),
			agenteval.ToolUsed(workspacetools.ApplyPatchToolName),
			agenteval.ToolUsed(verifytools.ToolName),
			agenteval.FinalEquals("Planner-guided verification passed."),
			requestCountEquals(modelClient, 5),
			{
				Name: "plan names verification before final answer",
				Check: func(agenteval.Result) error {
					requests := modelClient.Requests()
					if len(requests) == 0 {
						return fmt.Errorf("missing model request")
					}
					prompt := requests[0].SystemPrompt
					for _, want := range []string{"repair README and prove the result", "Verification hints", "workspace_verify test", "README.md"} {
						if !strings.Contains(prompt, want) {
							return fmt.Errorf("system prompt missing %q:\n%s", want, prompt)
						}
					}
					return nil
				},
			},
			{
				Name: "verification hint drives repair loop",
				Check: func(result agenteval.Result) error {
					results := result.ToolResults()
					if len(results) < 4 {
						return fmt.Errorf("tool results = %#v, want patch verify patch verify", results)
					}
					if !results[1].IsError || !strings.Contains(results[1].Content, "expected status: fixed") {
						return fmt.Errorf("first verification = %#v, want actionable failure", results[1])
					}
					if results[3].IsError || !strings.Contains(results[3].Content, "verification test passed") {
						return fmt.Errorf("second verification = %#v, want pass", results[3])
					}
					content, err := store.ReadFile(context.Background(), "README.md")
					if err != nil {
						return err
					}
					if content != "status: fixed" {
						return fmt.Errorf("README.md = %q, want repaired content", content)
					}
					return nil
				},
			},
		},
	}
}

// WorkspaceVerificationRepair returns a single-use scenario where verification
// fails after an edit, the model repairs the workspace, and verification passes.
func WorkspaceVerificationRepair() agenteval.Case {
	store := workspace.NewMemoryStore(map[string]string{
		"README.md": "status: broken",
	})
	workspaceTools, toolsErr := workspacetools.NewTools(store)
	verifyTool := verifytools.NewTool(verifytools.Config{
		Verifier: verifierForReadmeStatus(store, "status: fixed"),
	})
	modelClient := agenteval.NewScriptedModel(
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:   "patch-1",
				Name: workspacetools.ApplyPatchToolName,
				Input: json.RawMessage(`{"operations":[
					{"path":"README.md","old_content":"status: broken","new_content":"status: almost"}
				]}`),
			},
		}},
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:    "verify-1",
				Name:  verifytools.ToolName,
				Input: json.RawMessage(`{"name":"test","target":"README.md"}`),
			},
		}},
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:   "patch-2",
				Name: workspacetools.ApplyPatchToolName,
				Input: json.RawMessage(`{"operations":[
					{"path":"README.md","old_content":"status: almost","new_content":"status: fixed"}
				]}`),
			},
		}},
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:    "verify-2",
				Name:  verifytools.ToolName,
				Input: json.RawMessage(`{"name":"test","target":"README.md"}`),
			},
		}},
		[]model.StreamEvent{{Kind: model.StreamText, Text: "Workspace repaired and verified."}},
	)

	return agenteval.Case{
		Name:   "workspace_verification_repair",
		Prompt: "Patch README.md and verify the result. Repair if verification fails.",
		Options: memaxagent.Options{
			Model: modelClient,
			Tools: tool.NewRegistry(append(workspaceTools, verifyTool)...),
		},
		Assertions: []agenteval.Assertion{
			toolConstructionSucceeded(toolsErr),
			agenteval.ToolUsed(workspacetools.ApplyPatchToolName),
			agenteval.ToolUsed(verifytools.ToolName),
			agenteval.FinalEquals("Workspace repaired and verified."),
			requestCountEquals(modelClient, 5),
			{
				Name: "verification failure drives repair",
				Check: func(result agenteval.Result) error {
					results := result.ToolResults()
					if len(results) < 4 {
						return fmt.Errorf("tool results = %#v, want patch verify patch verify", results)
					}
					if !results[1].IsError || !strings.Contains(results[1].Content, "expected status: fixed") {
						return fmt.Errorf("first verification = %#v, want actionable failure", results[1])
					}
					if results[3].IsError || !strings.Contains(results[3].Content, "verification test passed") {
						return fmt.Errorf("second verification = %#v, want pass", results[3])
					}
					content, err := store.ReadFile(context.Background(), "README.md")
					if err != nil {
						return err
					}
					if content != "status: fixed" {
						return fmt.Errorf("README.md = %q, want repaired content", content)
					}
					return nil
				},
			},
		},
	}
}

// WorkspaceVerificationRollback returns a single-use scenario where a failed
// verification leads the model to restore the checkpoint instead of finalizing a
// broken edit.
func WorkspaceVerificationRollback() agenteval.Case {
	store := workspace.NewMemoryStore(map[string]string{
		"README.md": "status: good",
	})
	workspaceTools, toolsErr := workspacetools.NewTools(store)
	verifyTool := verifytools.NewTool(verifytools.Config{
		Verifier: verifierForReadmeStatus(store, "status: good"),
	})
	modelClient := agenteval.NewScriptedModel(
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:    "checkpoint-1",
				Name:  workspacetools.CheckpointToolName,
				Input: json.RawMessage(`{"label":"before risky edit"}`),
			},
		}},
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:   "patch-1",
				Name: workspacetools.ApplyPatchToolName,
				Input: json.RawMessage(`{"operations":[
					{"path":"README.md","old_content":"status: good","new_content":"status: bad"}
				]}`),
			},
		}},
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:    "verify-1",
				Name:  verifytools.ToolName,
				Input: json.RawMessage(`{"name":"test","target":"README.md"}`),
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
		[]model.StreamEvent{{Kind: model.StreamText, Text: "Restored after failed verification."}},
	)

	return agenteval.Case{
		Name:   "workspace_verification_rollback",
		Prompt: "Checkpoint, patch README.md, verify the result, and roll back if verification fails.",
		Options: memaxagent.Options{
			Model: modelClient,
			Tools: tool.NewRegistry(append(workspaceTools, verifyTool)...),
		},
		Assertions: []agenteval.Assertion{
			toolConstructionSucceeded(toolsErr),
			agenteval.ToolUsed(workspacetools.CheckpointToolName),
			agenteval.ToolUsed(workspacetools.ApplyPatchToolName),
			agenteval.ToolUsed(verifytools.ToolName),
			agenteval.ToolUsed(workspacetools.RestoreToolName),
			agenteval.FinalEquals("Restored after failed verification."),
			requestCountEquals(modelClient, 5),
			{
				Name: "failed verification rolls back checkpoint",
				Check: func(result agenteval.Result) error {
					results := result.ToolResults()
					if len(results) < 4 {
						return fmt.Errorf("tool results = %#v, want checkpoint patch verify restore", results)
					}
					if !results[2].IsError || !strings.Contains(results[2].Content, "expected status: good") {
						return fmt.Errorf("verification result = %#v, want failure", results[2])
					}
					if results[3].IsError || !strings.Contains(results[3].Content, "restored workspace checkpoint checkpoint-1") {
						return fmt.Errorf("restore result = %#v, want checkpoint restore", results[3])
					}
					content, err := store.ReadFile(context.Background(), "README.md")
					if err != nil {
						return err
					}
					if content != "status: good" {
						return fmt.Errorf("README.md = %q, want rollback to good status", content)
					}
					return nil
				},
			},
		},
	}
}

func verifierForReadmeStatus(store *workspace.MemoryStore, want string) verifytools.Verifier {
	return verifytools.VerifierFunc(func(ctx context.Context, req verifytools.Request) (verifytools.Result, error) {
		content, err := store.ReadFile(ctx, "README.md")
		if err != nil {
			return verifytools.Result{}, err
		}
		if content == want {
			return verifytools.Result{
				Name:   req.Name,
				Passed: true,
				Output: "README.md matched expected status.",
			}, nil
		}
		return verifytools.Result{
			Name:   req.Name,
			Passed: false,
			Output: fmt.Sprintf("got %q; expected %s", content, want),
			Diagnostics: []verifytools.Diagnostic{{
				Path:     "README.md",
				Severity: "error",
				Message:  "expected " + want,
			}},
		}, nil
	})
}
