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
