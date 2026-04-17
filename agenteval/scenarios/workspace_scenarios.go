package scenarios

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	memaxagent "github.com/MemaxLabs/memax-go-agent-sdk"
	"github.com/MemaxLabs/memax-go-agent-sdk/agenteval"
	"github.com/MemaxLabs/memax-go-agent-sdk/hook"
	"github.com/MemaxLabs/memax-go-agent-sdk/model"
	"github.com/MemaxLabs/memax-go-agent-sdk/planner"
	"github.com/MemaxLabs/memax-go-agent-sdk/tool"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/agentpolicy"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/approvaltools"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/commandtools"
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

// CommandTestRepairLoop returns a single-use scenario where the model runs a
// host-owned test command, repairs the workspace after a failing exit, reruns
// the command, and only then finalizes.
func CommandTestRepairLoop() agenteval.Case {
	store := workspace.NewMemoryStore(map[string]string{
		"README.md": "status: broken",
	})
	workspaceTools, workspaceErr := workspacetools.NewTools(store)
	runner := commandtools.NewScriptedRunner(
		commandtools.Result{
			ExitCode: 1,
			Stdout:   "go test ./...\n",
			Stderr:   "README.md: status must be fixed\n",
			Duration: 12 * time.Millisecond,
		},
		commandtools.Result{
			ExitCode: 0,
			Stdout:   "ok ./...\n",
			Duration: 8 * time.Millisecond,
		},
	)
	commandTool := commandtools.NewTool(commandtools.Config{
		Runner:    runner,
		MayMutate: false,
	})
	tools := append(workspaceTools, commandTool)
	modelClient := agenteval.NewScriptedModel(
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:    "cmd-1",
				Name:  commandtools.ToolName,
				Input: json.RawMessage(`{"command":["go","test","./..."],"purpose":"verify current state"}`),
			},
		}},
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:   "patch-1",
				Name: workspacetools.ApplyPatchToolName,
				Input: json.RawMessage(`{"operations":[
					{"path":"README.md","old_content":"status: broken","new_content":"status: fixed"}
				]}`),
			},
		}},
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:    "cmd-2",
				Name:  commandtools.ToolName,
				Input: json.RawMessage(`{"command":["go","test","./..."],"purpose":"verify repaired state"}`),
			},
		}},
		[]model.StreamEvent{{Kind: model.StreamText, Text: "Tests pass after repair."}},
	)

	return agenteval.Case{
		Name:   "command_test_repair_loop",
		Prompt: "Run the tests, repair README.md if they fail, then rerun tests before finalizing.",
		Options: memaxagent.Options{
			Model: modelClient,
			Tools: tool.NewRegistry(tools...),
		},
		Assertions: []agenteval.Assertion{
			toolConstructionSucceeded(workspaceErr),
			agenteval.ToolUsed(commandtools.ToolName),
			agenteval.ToolUsed(workspacetools.ApplyPatchToolName),
			agenteval.EventKindEmitted(memaxagent.EventCommandFinished),
			agenteval.EventKindEmitted(memaxagent.EventWorkspacePatch),
			agenteval.FinalEquals("Tests pass after repair."),
			requestCountEquals(modelClient, 4),
			{
				Name: "command failure guides workspace repair and rerun",
				Check: func(result agenteval.Result) error {
					toolResults := result.ToolResults()
					commandResults := 0
					sawFail := false
					sawPass := false
					for _, toolResult := range toolResults {
						if toolResult.Name != commandtools.ToolName {
							continue
						}
						commandResults++
						if toolResult.IsError && strings.Contains(toolResult.Content, "status must be fixed") {
							sawFail = true
						}
						if !toolResult.IsError && strings.Contains(toolResult.Content, "ok ./...") {
							sawPass = true
						}
					}
					if commandResults != 2 || !sawFail || !sawPass {
						return fmt.Errorf("command results = %#v, want fail then pass", toolResults)
					}
					content, err := store.ReadFile(context.Background(), "README.md")
					if err != nil {
						return err
					}
					if content != "status: fixed" {
						return fmt.Errorf("README.md = %q, want repaired content", content)
					}
					requests := runner.Requests()
					if len(requests) != 2 || requests[0].Purpose != "verify current state" || requests[1].Purpose != "verify repaired state" {
						return fmt.Errorf("command requests = %#v", requests)
					}
					return nil
				},
			},
		},
	}
}

// CommandSessionRepairLoop returns a single-use scenario where the model starts
// a managed command session, reads failing output, repairs the workspace, reads
// passing output, stops the session, and then finalizes.
func CommandSessionRepairLoop() agenteval.Case {
	store := workspace.NewMemoryStore(map[string]string{
		"README.md": "status: broken",
	})
	workspaceTools, workspaceErr := workspacetools.NewTools(store)
	manager := commandtools.NewScriptedSessionManager(commandtools.ScriptedCommand{
		ID:  "watch-1",
		PID: 5150,
		Pages: []commandtools.ScriptedOutputPage{
			{
				Chunks: []commandtools.OutputChunk{{
					Seq:    1,
					Stream: "stdout",
					Text:   "watch: README.md status must be fixed\n",
				}},
				Running: true,
			},
			{
				Chunks: []commandtools.OutputChunk{{
					Seq:    2,
					Stream: "stdout",
					Text:   "watch: ok\n",
				}},
				Running:  false,
				ExitCode: intPtr(0),
			},
		},
		StopExitCode: intPtr(0),
	})
	tools := append(workspaceTools,
		commandtools.NewStartTool(manager),
		commandtools.NewReadOutputTool(manager),
		commandtools.NewStopTool(manager),
	)
	modelClient := agenteval.NewScriptedModel(
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:    "start-1",
				Name:  commandtools.StartToolName,
				Input: json.RawMessage(`{"id":"watch-1","command":["npm","run","test:watch"],"purpose":"start watch mode"}`),
			},
		}},
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:    "read-1",
				Name:  commandtools.ReadOutputToolName,
				Input: json.RawMessage(`{"id":"watch-1"}`),
			},
		}},
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:   "patch-1",
				Name: workspacetools.ApplyPatchToolName,
				Input: json.RawMessage(`{"operations":[
					{"path":"README.md","old_content":"status: broken","new_content":"status: fixed"}
				]}`),
			},
		}},
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:    "read-2",
				Name:  commandtools.ReadOutputToolName,
				Input: json.RawMessage(`{"id":"watch-1","after_seq":1}`),
			},
		}},
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:    "stop-1",
				Name:  commandtools.StopToolName,
				Input: json.RawMessage(`{"id":"watch-1","force":true}`),
			},
		}},
		[]model.StreamEvent{{Kind: model.StreamText, Text: "Watch mode passed after repair."}},
	)

	return agenteval.Case{
		Name:   "command_session_repair_loop",
		Prompt: "Start a watch command, repair README.md if it fails, confirm it passes, then stop it.",
		Options: memaxagent.Options{
			Model: modelClient,
			Tools: tool.NewRegistry(tools...),
		},
		Assertions: []agenteval.Assertion{
			toolConstructionSucceeded(workspaceErr),
			agenteval.ToolUsed(commandtools.StartToolName),
			agenteval.ToolUsed(commandtools.ReadOutputToolName),
			agenteval.ToolUsed(commandtools.StopToolName),
			agenteval.ToolUsed(workspacetools.ApplyPatchToolName),
			agenteval.EventKindEmitted(memaxagent.EventCommandStarted),
			agenteval.EventKindEmitted(memaxagent.EventCommandOutput),
			agenteval.EventKindEmitted(memaxagent.EventCommandStopped),
			agenteval.EventKindEmitted(memaxagent.EventWorkspacePatch),
			agenteval.FinalEquals("Watch mode passed after repair."),
			requestCountEquals(modelClient, 6),
			{
				Name: "command session output drives repair loop",
				Check: func(result agenteval.Result) error {
					content, err := store.ReadFile(context.Background(), "README.md")
					if err != nil {
						return err
					}
					if content != "status: fixed" {
						return fmt.Errorf("README.md = %q, want repaired content", content)
					}
					readRequests := manager.ReadRequests()
					if len(readRequests) != 2 || readRequests[1].AfterSeq != 1 {
						return fmt.Errorf("read requests = %#v, want second read after_seq=1", readRequests)
					}
					stopRequests := manager.StopRequests()
					if len(stopRequests) != 1 || stopRequests[0].ID != "watch-1" {
						return fmt.Errorf("stop requests = %#v, want stop watch-1", stopRequests)
					}
					results := result.ToolResults()
					sawFail := false
					sawPass := false
					for _, toolResult := range results {
						if toolResult.Name != commandtools.ReadOutputToolName {
							continue
						}
						if strings.Contains(toolResult.Content, "status must be fixed") {
							sawFail = true
						}
						if strings.Contains(toolResult.Content, "watch: ok") {
							sawPass = true
						}
					}
					if !sawFail || !sawPass {
						return fmt.Errorf("tool results = %#v, want failing and passing watch output", results)
					}
					return nil
				},
			},
		},
	}
}

// CommandApprovalPolicyRecovery returns a single-use scenario where a command
// is denied until the model obtains input-bound host approval.
func CommandApprovalPolicyRecovery() agenteval.Case {
	runner := commandtools.NewScriptedRunner(commandtools.Result{
		ExitCode: 0,
		Stdout:   "installed\n",
		Duration: 5 * time.Millisecond,
	})
	commandTool := commandtools.NewTool(commandtools.Config{
		Runner:    runner,
		MayMutate: true,
	})
	policy := agentpolicy.RequireApprovalBeforeCommands(
		[]agentpolicy.CommandMatcher{agentpolicy.MatchCommandPrefix("npm", "install")},
		agentpolicy.WithCommandInputBoundApprovals(),
		agentpolicy.WithCommandSingleUseApprovals(),
	)
	approvalTool := approvaltools.NewTool(approvaltools.Config{
		Approver: approvaltools.StaticApprover{Decision: approvaltools.Decision{
			Approved: true,
			Reason:   "approved dependency install",
		}},
	})
	toolInput := `{"command":["npm","install"],"purpose":"install dependencies"}`
	modelClient := agenteval.NewScriptedModel(
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:    "cmd-1",
				Name:  commandtools.ToolName,
				Input: json.RawMessage(toolInput),
			},
		}},
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:    "approval-1",
				Name:  approvaltools.ToolName,
				Input: json.RawMessage(`{"action":"run_command","reason":"dependency install changes workspace state","summary":{"title":"Run command: npm install","description":"Install dependencies","risk":"mutates dependency tree","changes":1},"tool_input":` + toolInput + `}`),
			},
		}},
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:    "cmd-2",
				Name:  commandtools.ToolName,
				Input: json.RawMessage(toolInput),
			},
		}},
		[]model.StreamEvent{{Kind: model.StreamText, Text: "Command ran after approval."}},
	)

	return agenteval.Case{
		Name:   "command_approval_policy_recovery",
		Prompt: "Run npm install only after approval.",
		Options: memaxagent.Options{
			Model: modelClient,
			Tools: tool.NewRegistry(commandTool, approvalTool),
			Hooks: hook.NewRunner(policy.Options()...),
		},
		Assertions: []agenteval.Assertion{
			agenteval.ToolUsed(commandtools.ToolName),
			agenteval.ToolUsed(approvaltools.ToolName),
			agenteval.EventKindEmitted(memaxagent.EventApprovalRequested),
			agenteval.EventKindEmitted(memaxagent.EventApprovalGranted),
			agenteval.EventKindEmitted(memaxagent.EventApprovalConsumed),
			agenteval.EventKindEmitted(memaxagent.EventCommandFinished),
			agenteval.FinalEquals("Command ran after approval."),
			requestCountEquals(modelClient, 4),
			{
				Name: "command approval is input-bound and consumed",
				Check: func(result agenteval.Result) error {
					results := result.ToolResults()
					if len(results) < 3 {
						return fmt.Errorf("tool results = %#v, want denied command approval command", results)
					}
					if !results[0].IsError || !strings.Contains(results[0].Content, "request approval before running command") {
						return fmt.Errorf("first command result = %#v, want approval denial", results[0])
					}
					if results[1].IsError || results[1].Metadata[approvaltools.MetadataApprovalApproved] != true {
						return fmt.Errorf("approval result = %#v, want granted approval", results[1])
					}
					if results[2].IsError || !strings.Contains(results[2].Content, "installed") {
						return fmt.Errorf("second command result = %#v, want command success", results[2])
					}
					requests := runner.Requests()
					if len(requests) != 1 || strings.Join(requests[0].Argv, " ") != "npm install" {
						return fmt.Errorf("runner requests = %#v, want only approved npm install", requests)
					}
					return nil
				},
			},
		},
	}
}

// CommandVerifyBeforeFinalPolicyRecovery returns a single-use scenario where a
// command marks the session dirty and the model must verify before finalizing.
func CommandVerifyBeforeFinalPolicyRecovery() agenteval.Case {
	runner := commandtools.NewScriptedRunner(commandtools.Result{
		ExitCode: 0,
		Stdout:   "generated\n",
		Duration: 5 * time.Millisecond,
	})
	commandTool := commandtools.NewTool(commandtools.Config{
		Runner:    runner,
		MayMutate: true,
	})
	verifier := verifytools.VerifierFunc(func(_ context.Context, req verifytools.Request) (verifytools.Result, error) {
		return verifytools.Result{Name: req.Name, Passed: true, Output: "generated files verified"}, nil
	})
	verifyTool := verifytools.NewTool(verifytools.Config{Verifier: verifier})
	policy := agentpolicy.RequireVerificationAfterCommands(agentpolicy.MatchCommandPrefix("go", "generate"))
	modelClient := agenteval.NewScriptedModel(
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:    "cmd-1",
				Name:  commandtools.ToolName,
				Input: json.RawMessage(`{"command":["go","generate","./..."],"purpose":"regenerate code"}`),
			},
		}},
		[]model.StreamEvent{{Kind: model.StreamText, Text: "Generated code."}},
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:    "verify-1",
				Name:  verifytools.ToolName,
				Input: json.RawMessage(`{"name":"test"}`),
			},
		}},
		[]model.StreamEvent{{Kind: model.StreamText, Text: "Generated code verified."}},
	)

	return agenteval.Case{
		Name:   "command_verify_before_final_policy_recovery",
		Prompt: "Run go generate, verify, then final.",
		Options: memaxagent.Options{
			Model: modelClient,
			Tools: tool.NewRegistry(commandTool, verifyTool),
			Hooks: hook.NewRunner(policy.Options()...),
		},
		Assertions: []agenteval.Assertion{
			agenteval.ToolUsed(commandtools.ToolName),
			agenteval.ToolUsed(verifytools.ToolName),
			agenteval.EventKindEmitted(memaxagent.EventCommandFinished),
			agenteval.EventKindEmitted(memaxagent.EventVerification),
			agenteval.FinalEquals("Generated code verified."),
			requestCountEquals(modelClient, 4),
			{
				Name: "command finalization waits for verification",
				Check: func(result agenteval.Result) error {
					results := result.ToolResults()
					if len(results) < 2 {
						return fmt.Errorf("tool results = %#v, want command and verification", results)
					}
					if results[0].Name != commandtools.ToolName || results[0].IsError {
						return fmt.Errorf("command result = %#v, want command success", results[0])
					}
					if results[1].Name != verifytools.ToolName || results[1].IsError {
						return fmt.Errorf("verify result = %#v, want verification success", results[1])
					}
					if len(runner.Requests()) != 1 {
						return fmt.Errorf("runner requests = %#v, want one command", runner.Requests())
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

// WorkspaceVerifyBeforeFinalPolicyRecovery returns a single-use scenario where
// the model attempts to finalize after a workspace mutation, the policy appends
// a recoverable verification requirement, and the model verifies before
// finalizing.
func WorkspaceVerifyBeforeFinalPolicyRecovery() agenteval.Case {
	store := workspace.NewMemoryStore(map[string]string{
		"README.md": "status: broken",
	})
	workspaceTools, toolsErr := workspacetools.NewTools(store)
	policy := agentpolicy.RequireVerificationBeforeFinal()
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
					{"path":"README.md","old_content":"status: broken","new_content":"status: fixed"}
				]}`),
			},
		}},
		[]model.StreamEvent{{Kind: model.StreamText, Text: "Fixed README."}},
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:    "verify-1",
				Name:  verifytools.ToolName,
				Input: json.RawMessage(`{"name":"test","target":"README.md"}`),
			},
		}},
		[]model.StreamEvent{{Kind: model.StreamText, Text: "Fixed README after verification."}},
	)

	return agenteval.Case{
		Name:   "workspace_verify_before_final_policy_recovery",
		Prompt: "Fix README.md and do not finalize until the workspace is verified.",
		Options: memaxagent.Options{
			Model: modelClient,
			Tools: tool.NewRegistry(append(workspaceTools, verifyTool)...),
			Hooks: hook.NewRunner(policy.Options()...),
		},
		Assertions: []agenteval.Assertion{
			toolConstructionSucceeded(toolsErr),
			agenteval.ToolUsed(workspacetools.ApplyPatchToolName),
			agenteval.ToolUsed(verifytools.ToolName),
			agenteval.FinalEquals("Fixed README after verification."),
			requestCountEquals(modelClient, 4),
			{
				Name: "finalization denial requires verification",
				Check: func(result agenteval.Result) error {
					results := result.ToolResults()
					if len(results) < 2 {
						return fmt.Errorf("tool results = %#v, want patch and verification", results)
					}
					if results[0].IsError || !strings.Contains(results[0].Content, "modified README.md") {
						return fmt.Errorf("patch result = %#v, want successful patch", results[0])
					}
					if results[1].IsError || !strings.Contains(results[1].Content, "verification test passed") {
						return fmt.Errorf("verification result = %#v, want passing verification", results[1])
					}
					requests := modelClient.Requests()
					if len(requests) < 3 {
						return fmt.Errorf("requests = %d, want retry prompt after denied final", len(requests))
					}
					retryMessages := requests[2].Messages
					if len(retryMessages) < 4 {
						return fmt.Errorf("retry messages = %#v, want user prompt after denied final", retryMessages)
					}
					last := retryMessages[len(retryMessages)-1].PlainText()
					if !strings.Contains(last, agentpolicy.VerifyBeforeFinalReason()) {
						return fmt.Errorf("retry prompt = %q, want verify-before-final reason", last)
					}
					content, err := store.ReadFile(context.Background(), "README.md")
					if err != nil {
						return err
					}
					if content != "status: fixed" {
						return fmt.Errorf("README.md = %q, want fixed content", content)
					}
					return nil
				},
			},
		},
	}
}

// WorkspaceApprovalPolicyRecovery returns a single-use scenario where a
// sensitive workspace patch is denied until the model requests host approval.
func WorkspaceApprovalPolicyRecovery() agenteval.Case {
	store := workspace.NewMemoryStore(map[string]string{
		"README.md": "status: old",
	})
	workspaceTools, toolsErr := workspacetools.NewTools(store)
	policy := agentpolicy.RequireApprovalBeforeToolsWithOptions(
		[]string{workspacetools.ApplyPatchToolName},
		agentpolicy.WithInputBoundApprovals(),
		agentpolicy.WithSingleUseApprovals(),
	)
	approvalTool := approvaltools.NewTool(approvaltools.Config{
		Approver: approvaltools.StaticApprover{Decision: approvaltools.Decision{
			Approved: true,
			Reason:   "approved README patch",
		}},
	})
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
				ID:   "approval-1",
				Name: approvaltools.ToolName,
				Input: json.RawMessage(`{"action":"workspace_apply_patch","reason":"README.md status update needs host approval","details":"README.md old to new","risk":"low","summary":{"title":"Review README.md status patch","description":"Change README.md status from old to new","risk":"low","paths":["README.md"],"changes":1,"modified":1,"byte_delta":0},"tool_input":{"operations":[
					{"path":"README.md","old_content":"status: old","new_content":"status: new"}
				]}}`),
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
		[]model.StreamEvent{{Kind: model.StreamText, Text: "Patched after approval."}},
	)

	return agenteval.Case{
		Name:   "workspace_approval_policy_recovery",
		Prompt: "Patch README.md, requesting approval if required.",
		Options: memaxagent.Options{
			Model: modelClient,
			Tools: tool.NewRegistry(append(workspaceTools, approvalTool)...),
			Hooks: hook.NewRunner(policy.Options()...),
		},
		Assertions: []agenteval.Assertion{
			toolConstructionSucceeded(toolsErr),
			agenteval.ToolUsed(workspacetools.ApplyPatchToolName),
			agenteval.ToolUsed(approvaltools.ToolName),
			agenteval.EventKindEmitted(memaxagent.EventApprovalRequested),
			agenteval.EventKindEmitted(memaxagent.EventApprovalGranted),
			agenteval.EventKindEmitted(memaxagent.EventApprovalConsumed),
			agenteval.FinalEquals("Patched after approval."),
			requestCountEquals(modelClient, 4),
			{
				Name: "approval drives patch recovery",
				Check: func(result agenteval.Result) error {
					results := result.ToolResults()
					if len(results) < 3 {
						return fmt.Errorf("tool results = %#v, want denied patch approval patch", results)
					}
					if !results[0].IsError || !strings.Contains(results[0].Content, agentpolicy.ApprovalBeforeToolReason(workspacetools.ApplyPatchToolName)) {
						return fmt.Errorf("first patch result = %#v, want approval denial", results[0])
					}
					if results[1].IsError || results[1].Metadata[approvaltools.MetadataApprovalApproved] != true {
						return fmt.Errorf("approval result = %#v, want approval granted", results[1])
					}
					if results[1].Metadata[approvaltools.MetadataApprovalSummaryTitle] != "Review README.md status patch" {
						return fmt.Errorf("approval metadata = %#v, want structured patch summary", results[1].Metadata)
					}
					if results[2].IsError || !strings.Contains(results[2].Content, "modified README.md") {
						return fmt.Errorf("second patch result = %#v, want patch success", results[2])
					}
					content, err := store.ReadFile(context.Background(), "README.md")
					if err != nil {
						return err
					}
					if content != "status: new" {
						return fmt.Errorf("README.md = %q, want approved patch applied", content)
					}
					return nil
				},
			},
		},
	}
}

// WorkspaceApprovalDeniedFallback returns a single-use scenario where approval
// is denied and the model chooses a safe read-only fallback instead of forcing
// the patch.
func WorkspaceApprovalDeniedFallback() agenteval.Case {
	store := workspace.NewMemoryStore(map[string]string{
		"README.md": "status: old",
	})
	workspaceTools, toolsErr := workspacetools.NewTools(store)
	policy := agentpolicy.RequireApprovalBeforeTools(workspacetools.ApplyPatchToolName)
	approvalTool := approvaltools.NewTool(approvaltools.Config{
		Approver: approvaltools.StaticApprover{Decision: approvaltools.Decision{
			Approved: false,
			Reason:   "README changes are frozen",
		}},
	})
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
				ID:    "approval-1",
				Name:  approvaltools.ToolName,
				Input: json.RawMessage(`{"action":"workspace_apply_patch","reason":"README.md status update needs host approval"}`),
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
		[]model.StreamEvent{{Kind: model.StreamText, Text: "Approval denied; left README.md unchanged."}},
	)

	return agenteval.Case{
		Name:   "workspace_approval_denied_fallback",
		Prompt: "Patch README.md only if the host approves.",
		Options: memaxagent.Options{
			Model: modelClient,
			Tools: tool.NewRegistry(append(workspaceTools, approvalTool)...),
			Hooks: hook.NewRunner(policy.Options()...),
		},
		Assertions: []agenteval.Assertion{
			toolConstructionSucceeded(toolsErr),
			agenteval.ToolUsed(workspacetools.ApplyPatchToolName),
			agenteval.ToolUsed(approvaltools.ToolName),
			agenteval.ToolUsed(workspacetools.ReadToolName),
			agenteval.EventKindEmitted(memaxagent.EventApprovalRequested),
			agenteval.EventKindEmitted(memaxagent.EventApprovalDenied),
			agenteval.FinalEquals("Approval denied; left README.md unchanged."),
			requestCountEquals(modelClient, 4),
			{
				Name: "approval denial leads to safe fallback",
				Check: func(result agenteval.Result) error {
					results := result.ToolResults()
					if len(results) < 3 {
						return fmt.Errorf("tool results = %#v, want denied patch approval read", results)
					}
					if !results[0].IsError || !strings.Contains(results[0].Content, agentpolicy.ApprovalBeforeToolReason(workspacetools.ApplyPatchToolName)) {
						return fmt.Errorf("first patch result = %#v, want approval policy denial", results[0])
					}
					if !results[1].IsError || results[1].Metadata[approvaltools.MetadataApprovalApproved] != false || !strings.Contains(results[1].Content, "README changes are frozen") {
						return fmt.Errorf("approval result = %#v, want approval denied", results[1])
					}
					if results[2].IsError || results[2].Content != "status: old" {
						return fmt.Errorf("read result = %#v, want unchanged README", results[2])
					}
					content, err := store.ReadFile(context.Background(), "README.md")
					if err != nil {
						return err
					}
					if content != "status: old" {
						return fmt.Errorf("README.md = %q, want unchanged after approval denial", content)
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

func intPtr(v int) *int { return &v }
