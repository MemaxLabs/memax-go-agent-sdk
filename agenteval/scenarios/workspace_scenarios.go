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
	"github.com/MemaxLabs/memax-go-agent-sdk/model"
	"github.com/MemaxLabs/memax-go-agent-sdk/tool"
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
