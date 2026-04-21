package scenarios

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	memaxagent "github.com/MemaxLabs/memax-go-agent-sdk"
	"github.com/MemaxLabs/memax-go-agent-sdk/agenteval"
	"github.com/MemaxLabs/memax-go-agent-sdk/model"
	"github.com/MemaxLabs/memax-go-agent-sdk/stack/coding"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/agentpolicy"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/approvaltools"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/commandtools"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/tasktools"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/verifytools"
	"github.com/MemaxLabs/memax-go-agent-sdk/workspace"
)

// CodingPresetSafeLocal returns a single-use scenario that exercises the
// cautious local-edit preset end to end: checkpoint, guarded patch, verify, and
// task completion with prompt-visible progress.
func CodingPresetSafeLocal() agenteval.Case {
	workspaceStore := workspace.NewMemoryStore(map[string]string{
		"README.md": "before\n",
	})
	taskStore := tasktools.NewMemoryStore([]tasktools.Task{{
		ID:     "task-1",
		Title:  "ship README update safely",
		Status: tasktools.StatusInProgress,
		Notes:  "checkpoint before edits and verify before final answer",
	}})
	modelClient := agenteval.NewScriptedModel(
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:    "checkpoint-1",
				Name:  "workspace_checkpoint",
				Input: json.RawMessage(`{"label":"before README update"}`),
			},
		}},
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:   "patch-1",
				Name: "workspace_apply_patch",
				Input: json.RawMessage(`{"operations":[
					{"path":"README.md","old_content":"before\n","new_content":"after\n"}
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
		[]model.StreamEvent{{Kind: model.StreamText, Text: "Safe local preset completed verified edit."}},
	)

	config := coding.SafeLocal()
	config.Workspace = workspaceStore
	config.Tasks = taskStore
	config.Verifier.Verifier = verifierForReadmeStatus(workspaceStore, "after\n")
	stack, stackErr := coding.New(config)

	return agenteval.Case{
		Name:    "coding_preset_safe_local",
		Prompt:  "Update README.md carefully, prove it, and finish the task.",
		Options: stack.WithModel(modelClient),
		Assertions: []agenteval.Assertion{
			toolConstructionSucceeded(stackErr),
			agenteval.ToolUsed("workspace_checkpoint"),
			agenteval.ToolUsed("workspace_apply_patch"),
			agenteval.ToolUsed(verifytools.ToolName),
			agenteval.NoToolErrors(),
			agenteval.EventKindEmitted(memaxagent.EventWorkspaceCheckpoint),
			agenteval.EventKindEmitted(memaxagent.EventWorkspacePatch),
			agenteval.EventKindEmitted(memaxagent.EventVerification),
			agenteval.FinalEquals("Safe local preset completed verified edit."),
			requestCountEquals(modelClient, 4),
			{
				Name: "safe local preset prompt and task progress are visible",
				Check: func(agenteval.Result) error {
					requests := modelClient.Requests()
					if len(requests) != 4 {
						return fmt.Errorf("requests = %d, want 4", len(requests))
					}
					initialPrompt := requests[0].SystemPrompt
					for _, want := range []string{
						"Operate cautiously in the local workspace.",
						"[in_progress] task-1",
						"checkpoint before edits and verify before final answer",
						"workspace_apply_patch",
						verifytools.ToolName,
					} {
						if !strings.Contains(initialPrompt, want) {
							return fmt.Errorf("initial prompt missing %q:\n%s", want, initialPrompt)
						}
					}
					finalPrompt := requests[3].SystemPrompt
					for _, want := range []string{"[completed] task-1", "verification:test"} {
						if !strings.Contains(finalPrompt, want) {
							return fmt.Errorf("final prompt missing %q:\n%s", want, finalPrompt)
						}
					}
					tasks, err := taskStore.List(context.Background())
					if err != nil {
						return err
					}
					if len(tasks) != 1 || tasks[0].Status != tasktools.StatusCompleted {
						return fmt.Errorf("tasks = %#v, want completed task", tasks)
					}
					content, err := workspaceStore.ReadFile(context.Background(), "README.md")
					if err != nil {
						return err
					}
					if content != "after\n" {
						return fmt.Errorf("README.md = %q, want %q", content, "after\n")
					}
					return nil
				},
			},
		},
	}
}

// CodingPresetSafeLocalRollbackRecovery returns a single-use scenario that
// exercises the safe_local preset under a failed verification: the preset's
// rollback guidance is surfaced, the model restores explicitly, re-verifies,
// and only then finalizes.
func CodingPresetSafeLocalRollbackRecovery() agenteval.Case {
	workspaceStore := workspace.NewMemoryStore(map[string]string{
		"README.md": "status: good",
	})
	taskStore := tasktools.NewMemoryStore([]tasktools.Task{{
		ID:     "task-1",
		Title:  "ship README update safely",
		Status: tasktools.StatusInProgress,
		Notes:  "checkpoint before edits, roll back on failed verification, then verify before final answer",
	}})
	modelClient := agenteval.NewScriptedModel(
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:    "checkpoint-1",
				Name:  "workspace_checkpoint",
				Input: json.RawMessage(`{"label":"before risky edit"}`),
			},
		}},
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:   "patch-1",
				Name: "workspace_apply_patch",
				Input: json.RawMessage(`{"operations":[
					{"path":"README.md","old_content":"status: good","new_content":"status: bad"}
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
				ID:    "restore-1",
				Name:  "workspace_restore",
				Input: json.RawMessage(`{"id":"checkpoint-1"}`),
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
		[]model.StreamEvent{{Kind: model.StreamText, Text: "Safe local preset restored and verified clean state."}},
	)

	config, configErr := coding.PresetSafeLocal.Config()
	config.Workspace = workspaceStore
	config.Tasks = taskStore
	config.Verifier.Verifier = verifierForReadmeStatus(workspaceStore, "status: good")
	stack, stackErr := coding.New(config)

	return agenteval.Case{
		Name:    "coding_preset_safe_local_rollback_recovery",
		Prompt:  "Attempt the README change safely, and if verification fails, follow the preset's rollback guidance before finishing the task.",
		Options: stack.WithModel(modelClient),
		Assertions: []agenteval.Assertion{
			toolConstructionSucceeded(configErr, stackErr),
			agenteval.ToolUsed("workspace_checkpoint"),
			agenteval.ToolUsed("workspace_apply_patch"),
			agenteval.ToolUsed(verifytools.ToolName),
			agenteval.ToolUsed("workspace_restore"),
			agenteval.EventKindEmitted(memaxagent.EventWorkspaceCheckpoint),
			agenteval.EventKindEmitted(memaxagent.EventWorkspacePatch),
			agenteval.EventKindEmitted(memaxagent.EventVerification),
			agenteval.EventKindEmitted(memaxagent.EventWorkspaceRestore),
			agenteval.FinalEquals("Safe local preset restored and verified clean state."),
			requestCountEquals(modelClient, 6),
			{
				Name: "safe local rollback guidance drives explicit restore and re-verification",
				Check: func(result agenteval.Result) error {
					toolResults := result.ToolResults()
					if len(toolResults) != 5 {
						return fmt.Errorf("tool results = %#v, want checkpoint patch verify restore verify", toolResults)
					}
					if toolResults[2].Metadata == nil {
						return fmt.Errorf("verification metadata = nil, want rollback recommendation")
					}
					if !toolResults[2].IsError || !strings.Contains(toolResults[2].Content, "restore workspace checkpoint checkpoint-1") {
						return fmt.Errorf("failed verification result = %#v, want rollback guidance", toolResults[2])
					}
					if toolResults[2].Metadata[agentpolicy.MetadataRollbackRecommended] != true {
						return fmt.Errorf("verification metadata = %#v, want rollback recommendation", toolResults[2].Metadata)
					}
					if toolResults[2].Metadata[agentpolicy.MetadataRollbackCheckpointID] != "checkpoint-1" {
						return fmt.Errorf("verification metadata = %#v, want checkpoint-1", toolResults[2].Metadata)
					}
					if toolResults[3].IsError || !strings.Contains(toolResults[3].Content, "restored workspace checkpoint checkpoint-1") {
						return fmt.Errorf("restore result = %#v, want successful restore", toolResults[3])
					}
					if toolResults[4].IsError || !strings.Contains(toolResults[4].Content, "verification test passed") {
						return fmt.Errorf("second verification result = %#v, want verification success", toolResults[4])
					}
					requests := modelClient.Requests()
					if len(requests) != 6 {
						return fmt.Errorf("requests = %d, want 6", len(requests))
					}
					initialPrompt := requests[0].SystemPrompt
					for _, want := range []string{
						"Operate cautiously in the local workspace.",
						"[in_progress] task-1",
						"roll back on failed verification",
					} {
						if !strings.Contains(initialPrompt, want) {
							return fmt.Errorf("initial prompt missing %q:\n%s", want, initialPrompt)
						}
					}
					finalPrompt := requests[5].SystemPrompt
					for _, want := range []string{"[completed] task-1", "verification:test"} {
						if !strings.Contains(finalPrompt, want) {
							return fmt.Errorf("final prompt missing %q:\n%s", want, finalPrompt)
						}
					}
					content, err := workspaceStore.ReadFile(context.Background(), "README.md")
					if err != nil {
						return err
					}
					if content != "status: good" {
						return fmt.Errorf("README.md = %q, want restored content", content)
					}
					tasks, err := taskStore.List(context.Background())
					if err != nil {
						return err
					}
					if len(tasks) != 1 || tasks[0].Status != tasktools.StatusCompleted {
						return fmt.Errorf("tasks = %#v, want completed task after rollback recovery", tasks)
					}
					return nil
				},
			},
		},
	}
}

// CodingPresetCIRepair returns a single-use scenario that exercises the CI
// repair preset through a failing command, checkpointed patch, re-run, and
// verification-driven task completion.
func CodingPresetCIRepair() agenteval.Case {
	workspaceStore := workspace.NewMemoryStore(map[string]string{
		"README.md": "status: broken",
	})
	taskStore := tasktools.NewMemoryStore([]tasktools.Task{{
		ID:     "task-1",
		Title:  "repair README status from CI failure",
		Status: tasktools.StatusInProgress,
		Notes:  "rerun the relevant check after the fix and verify before final answer",
	}})
	commandRunner := commandtools.NewScriptedRunner(
		commandtools.Result{
			Argv:     []string{"go", "test", "./..."},
			ExitCode: 1,
			Stderr:   "status must be fixed\n",
		},
		commandtools.Result{
			Argv:     []string{"go", "test", "./..."},
			ExitCode: 0,
			Stdout:   "ok\n",
		},
	)
	modelClient := agenteval.NewScriptedModel(
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:    "cmd-1",
				Name:  commandtools.ToolName,
				Input: json.RawMessage(`{"command":["go","test","./..."],"purpose":"reproduce CI failure"}`),
			},
		}},
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:    "checkpoint-1",
				Name:  "workspace_checkpoint",
				Input: json.RawMessage(`{"label":"before CI repair"}`),
			},
		}},
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:   "patch-1",
				Name: "workspace_apply_patch",
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
				Input: json.RawMessage(`{"command":["go","test","./..."],"purpose":"confirm CI repair"}`),
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
		[]model.StreamEvent{{Kind: model.StreamText, Text: "CI repair preset reran checks and verified."}},
	)

	config := coding.CIRepair()
	config.Workspace = workspaceStore
	config.Tasks = taskStore
	config.Command.Runner = commandRunner
	config.Verifier.Verifier = verifierForReadmeStatus(workspaceStore, "status: fixed")
	stack, stackErr := coding.New(config)

	return agenteval.Case{
		Name:    "coding_preset_ci_repair",
		Prompt:  "Repair the CI failure, rerun the right check, verify the workspace, and finish the task.",
		Options: stack.WithModel(modelClient),
		Assertions: []agenteval.Assertion{
			toolConstructionSucceeded(stackErr),
			agenteval.ToolUsed(commandtools.ToolName),
			agenteval.ToolUsed("workspace_checkpoint"),
			agenteval.ToolUsed("workspace_apply_patch"),
			agenteval.ToolUsed(verifytools.ToolName),
			agenteval.EventKindEmitted(memaxagent.EventCommandFinished),
			agenteval.EventKindEmitted(memaxagent.EventWorkspaceCheckpoint),
			agenteval.EventKindEmitted(memaxagent.EventWorkspacePatch),
			agenteval.EventKindEmitted(memaxagent.EventVerification),
			agenteval.FinalEquals("CI repair preset reran checks and verified."),
			requestCountEquals(modelClient, 6),
			{
				Name: "ci repair preset drives reproducible repair loop",
				Check: func(result agenteval.Result) error {
					toolResults := result.ToolResults()
					if len(toolResults) != 5 {
						return fmt.Errorf("tool results = %#v, want 5 tool results", toolResults)
					}
					if !toolResults[0].IsError || !strings.Contains(toolResults[0].Content, "status must be fixed") {
						return fmt.Errorf("first command result = %#v, want failing CI output", toolResults[0])
					}
					if toolResults[1].IsError || !strings.Contains(toolResults[1].Content, "created workspace checkpoint checkpoint-1") {
						return fmt.Errorf("checkpoint result = %#v, want checkpoint success", toolResults[1])
					}
					if toolResults[2].IsError || !strings.Contains(toolResults[2].Content, "modified README.md") {
						return fmt.Errorf("patch result = %#v, want successful patch", toolResults[2])
					}
					if toolResults[3].IsError || !strings.Contains(toolResults[3].Content, "ok") {
						return fmt.Errorf("second command result = %#v, want passing command output", toolResults[3])
					}
					if toolResults[4].IsError || !strings.Contains(toolResults[4].Content, "verification test passed") {
						return fmt.Errorf("verification result = %#v, want verification success", toolResults[4])
					}
					requests := commandRunner.Requests()
					if len(requests) != 2 {
						return fmt.Errorf("command requests = %#v, want 2", requests)
					}
					for i, req := range requests {
						if req.Timeout != 10*time.Minute {
							return fmt.Errorf("command request %d timeout = %s, want %s", i, req.Timeout, 10*time.Minute)
						}
					}
					if requests[0].Purpose != "reproduce CI failure" || requests[1].Purpose != "confirm CI repair" {
						return fmt.Errorf("command purposes = %#v, want reproduce/confirm purposes", requests)
					}
					prompt := modelClient.Requests()[0].SystemPrompt
					for _, want := range []string{
						"Focus on reproducible repair loops.",
						"[in_progress] task-1",
						commandtools.ToolName,
						verifytools.ToolName,
					} {
						if !strings.Contains(prompt, want) {
							return fmt.Errorf("initial prompt missing %q:\n%s", want, prompt)
						}
					}
					finalPrompt := modelClient.Requests()[5].SystemPrompt
					for _, want := range []string{"[completed] task-1", "verification:test"} {
						if !strings.Contains(finalPrompt, want) {
							return fmt.Errorf("final prompt missing %q:\n%s", want, finalPrompt)
						}
					}
					content, err := workspaceStore.ReadFile(context.Background(), "README.md")
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

// CodingPresetCIRepairApprovalRecovery returns a single-use scenario that
// exercises the ci_repair preset under an approval denial, then a successful
// re-request using the same input-bound patch before the repair loop continues.
func CodingPresetCIRepairApprovalRecovery() agenteval.Case {
	workspaceStore := workspace.NewMemoryStore(map[string]string{
		"README.md": "status: broken",
	})
	taskStore := tasktools.NewMemoryStore([]tasktools.Task{{
		ID:     "task-1",
		Title:  "repair README status from CI failure",
		Status: tasktools.StatusInProgress,
		Notes:  "request approval for workspace changes, rerun the relevant check, and verify before final answer",
	}})
	commandRunner := commandtools.NewScriptedRunner(
		commandtools.Result{
			Argv:     []string{"go", "test", "./..."},
			ExitCode: 1,
			Stderr:   "status must be fixed\n",
		},
		commandtools.Result{
			Argv:     []string{"go", "test", "./..."},
			ExitCode: 0,
			Stdout:   "ok\n",
		},
	)
	var (
		approverMu       sync.Mutex
		approvalRequests []approvaltools.Request
	)
	approver := approvaltools.ApproverFunc(func(ctx context.Context, req approvaltools.Request) (approvaltools.Decision, error) {
		if err := ctx.Err(); err != nil {
			return approvaltools.Decision{}, err
		}
		approverMu.Lock()
		defer approverMu.Unlock()
		approvalRequests = append(approvalRequests, req)
		if len(approvalRequests) == 1 {
			return approvaltools.Decision{
				Approved: false,
				Reason:   "README changes are temporarily frozen pending confirmation",
			}, nil
		}
		return approvaltools.Decision{
			Approved: true,
			Reason:   "approved after confirming the exact README patch",
		}, nil
	})
	patchInput := `{"operations":[{"path":"README.md","old_content":"status: broken","new_content":"status: fixed"}]}`
	modelClient := agenteval.NewScriptedModel(
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:    "cmd-1",
				Name:  commandtools.ToolName,
				Input: json.RawMessage(`{"command":["go","test","./..."],"purpose":"reproduce CI failure"}`),
			},
		}},
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:    "checkpoint-1",
				Name:  "workspace_checkpoint",
				Input: json.RawMessage(`{"label":"before approved CI repair"}`),
			},
		}},
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:    "patch-1",
				Name:  "workspace_apply_patch",
				Input: json.RawMessage(patchInput),
			},
		}},
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:    "approval-1",
				Name:  approvaltools.ToolName,
				Input: json.RawMessage(`{"action":"workspace_apply_patch","reason":"repairing README.md requires host approval","summary":{"title":"Review README.md CI repair patch","description":"Fix README status so CI passes","risk":"modifies tracked documentation","paths":["README.md"],"changes":1,"modified":1},"tool_input":` + patchInput + `}`),
			},
		}},
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:    "approval-2",
				Name:  approvaltools.ToolName,
				Input: json.RawMessage(`{"action":"workspace_apply_patch","reason":"retry the exact README.md CI repair after denial","summary":{"title":"Review README.md CI repair patch","description":"Fix README status so CI passes","risk":"modifies tracked documentation","paths":["README.md"],"changes":1,"modified":1},"tool_input":` + patchInput + `}`),
			},
		}},
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:    "patch-2",
				Name:  "workspace_apply_patch",
				Input: json.RawMessage(patchInput),
			},
		}},
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:    "cmd-2",
				Name:  commandtools.ToolName,
				Input: json.RawMessage(`{"command":["go","test","./..."],"purpose":"confirm CI repair"}`),
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
		[]model.StreamEvent{{Kind: model.StreamText, Text: "CI repair preset completed after approval recovery."}},
	)

	config, configErr := coding.PresetCIRepair.Config()
	config.Workspace = workspaceStore
	config.Tasks = taskStore
	config.Command.Runner = commandRunner
	config.Verifier.Verifier = verifierForReadmeStatus(workspaceStore, "status: fixed")
	config.Approval.Approver = approver
	config.Policies.RequirePatchApproval = true
	stack, stackErr := coding.New(config)

	return agenteval.Case{
		Name:    "coding_preset_ci_repair_approval_recovery",
		Prompt:  "Repair the CI failure, request approval for the workspace patch if denied, rerun the check, verify, and finish the task.",
		Options: stack.WithModel(modelClient),
		Assertions: []agenteval.Assertion{
			toolConstructionSucceeded(configErr, stackErr),
			agenteval.ToolUsed(commandtools.ToolName),
			agenteval.ToolUsed("workspace_checkpoint"),
			agenteval.ToolUsed("workspace_apply_patch"),
			agenteval.ToolUsed(approvaltools.ToolName),
			agenteval.ToolUsed(verifytools.ToolName),
			agenteval.EventKindEmitted(memaxagent.EventApprovalRequested),
			agenteval.EventKindEmitted(memaxagent.EventApprovalDenied),
			agenteval.EventKindEmitted(memaxagent.EventApprovalGranted),
			agenteval.EventKindEmitted(memaxagent.EventApprovalConsumed),
			agenteval.EventKindEmitted(memaxagent.EventCommandFinished),
			agenteval.EventKindEmitted(memaxagent.EventWorkspaceCheckpoint),
			agenteval.EventKindEmitted(memaxagent.EventWorkspacePatch),
			agenteval.EventKindEmitted(memaxagent.EventVerification),
			agenteval.FinalEquals("CI repair preset completed after approval recovery."),
			requestCountEquals(modelClient, 9),
			{
				Name: "ci repair approval denial recovers through exact reapproval",
				Check: func(result agenteval.Result) error {
					toolResults := result.ToolResults()
					if len(toolResults) != 8 {
						return fmt.Errorf("tool results = %#v, want 8", toolResults)
					}
					if !toolResults[0].IsError || !strings.Contains(toolResults[0].Content, "status must be fixed") {
						return fmt.Errorf("first command result = %#v, want failing CI output", toolResults[0])
					}
					if toolResults[1].IsError || !strings.Contains(toolResults[1].Content, "created workspace checkpoint checkpoint-1") {
						return fmt.Errorf("checkpoint result = %#v, want checkpoint success", toolResults[1])
					}
					if !toolResults[2].IsError || !strings.Contains(toolResults[2].Content, agentpolicy.ApprovalBeforeToolReason("workspace_apply_patch")) {
						return fmt.Errorf("first patch result = %#v, want approval denial", toolResults[2])
					}
					if !toolResults[3].IsError || toolResults[3].Metadata[approvaltools.MetadataApprovalApproved] != false {
						return fmt.Errorf("first approval result = %#v, want approval denial", toolResults[3])
					}
					if toolResults[4].IsError || toolResults[4].Metadata[approvaltools.MetadataApprovalApproved] != true {
						return fmt.Errorf("second approval result = %#v, want approval granted", toolResults[4])
					}
					if toolResults[5].IsError || !strings.Contains(toolResults[5].Content, "modified README.md") {
						return fmt.Errorf("second patch result = %#v, want patch success", toolResults[5])
					}
					if toolResults[6].IsError || !strings.Contains(toolResults[6].Content, "ok") {
						return fmt.Errorf("second command result = %#v, want passing command output", toolResults[6])
					}
					if toolResults[7].IsError || !strings.Contains(toolResults[7].Content, "verification test passed") {
						return fmt.Errorf("verification result = %#v, want verification success", toolResults[7])
					}
					approverMu.Lock()
					requests := append([]approvaltools.Request(nil), approvalRequests...)
					approverMu.Unlock()
					if len(requests) != 2 {
						return fmt.Errorf("approval requests = %#v, want two approval requests", requests)
					}
					if requests[0].Action != "workspace_apply_patch" || requests[1].Action != "workspace_apply_patch" {
						return fmt.Errorf("approval actions = %#v, want workspace_apply_patch", requests)
					}
					if requests[0].ToolInputHash == "" || requests[0].ToolInputHash != requests[1].ToolInputHash {
						return fmt.Errorf("approval input hashes = %q / %q, want matching non-empty hashes", requests[0].ToolInputHash, requests[1].ToolInputHash)
					}
					if requests[0].Summary.Title != "Review README.md CI repair patch" || requests[1].Summary.Title != "Review README.md CI repair patch" {
						return fmt.Errorf("approval summaries = %#v, want structured CI repair summary", requests)
					}
					commandRequests := commandRunner.Requests()
					if len(commandRequests) != 2 {
						return fmt.Errorf("command requests = %#v, want 2", commandRequests)
					}
					for i, req := range commandRequests {
						if req.Timeout != 10*time.Minute {
							return fmt.Errorf("command request %d timeout = %s, want %s", i, req.Timeout, 10*time.Minute)
						}
					}
					workspacePatchEvents := 0
					for _, event := range result.Events {
						if event.Kind == memaxagent.EventWorkspacePatch {
							workspacePatchEvents++
						}
					}
					if workspacePatchEvents != 1 {
						return fmt.Errorf("workspace patch events = %d, want exactly one successful patch", workspacePatchEvents)
					}
					initialPrompt := modelClient.Requests()[0].SystemPrompt
					for _, want := range []string{
						"Focus on reproducible repair loops.",
						"[in_progress] task-1",
						commandtools.ToolName,
						verifytools.ToolName,
					} {
						if !strings.Contains(initialPrompt, want) {
							return fmt.Errorf("initial prompt missing %q:\n%s", want, initialPrompt)
						}
					}
					finalPrompt := modelClient.Requests()[8].SystemPrompt
					for _, want := range []string{"[completed] task-1", "verification:test"} {
						if !strings.Contains(finalPrompt, want) {
							return fmt.Errorf("final prompt missing %q:\n%s", want, finalPrompt)
						}
					}
					content, err := workspaceStore.ReadFile(context.Background(), "README.md")
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

// CodingPresetInteractiveDev returns a single-use scenario that exercises the
// interactive development preset through a managed command session, incremental
// stdin writes, a repair, clean shutdown, and verification.
func CodingPresetInteractiveDev() agenteval.Case {
	workspaceStore := workspace.NewMemoryStore(map[string]string{
		"README.md": "status: broken",
	})
	taskStore := tasktools.NewMemoryStore([]tasktools.Task{{
		ID:     "task-1",
		Title:  "repair README status from watcher feedback",
		Status: tasktools.StatusInProgress,
		Notes:  "use the watcher, stop it when done, then verify before final answer",
	}})
	manager := commandtools.NewScriptedSessionManager(commandtools.ScriptedCommand{
		ID:  "watch-1",
		PID: 8181,
		TTY: true,
		WritePages: []commandtools.ScriptedWritePage{
			{
				Page: commandtools.ScriptedOutputPage{
					Chunks: []commandtools.OutputChunk{{
						Seq:    1,
						Stream: "stdout",
						Text:   "watch: status must be fixed\n",
					}},
					Running: true,
				},
			},
			{
				Page: commandtools.ScriptedOutputPage{
					Chunks: []commandtools.OutputChunk{{
						Seq:    2,
						Stream: "stdout",
						Text:   "watch: ok\n",
					}},
					Running: true,
				},
			},
		},
		StopExitCode: intPtr(0),
	})
	modelClient := agenteval.NewScriptedModel(
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:    "start-1",
				Name:  commandtools.StartToolName,
				Input: json.RawMessage(`{"id":"watch-1","command":["npm","run","watch"],"purpose":"watch README status","tty":true}`),
			},
		}},
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:    "write-1",
				Name:  commandtools.WriteInputToolName,
				Input: json.RawMessage(`{"id":"watch-1","input":"status\n","yield_ms":250}`),
			},
		}},
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:    "checkpoint-1",
				Name:  "workspace_checkpoint",
				Input: json.RawMessage(`{"label":"before watcher repair"}`),
			},
		}},
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:   "patch-1",
				Name: "workspace_apply_patch",
				Input: json.RawMessage(`{"operations":[
					{"path":"README.md","old_content":"status: broken","new_content":"status: fixed"}
				]}`),
			},
		}},
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:    "write-2",
				Name:  commandtools.WriteInputToolName,
				Input: json.RawMessage(`{"id":"watch-1","input":"status\n","yield_ms":250}`),
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
		[]model.StreamEvent{{Kind: model.StreamText, Text: "Interactive dev preset repaired from live feedback."}},
	)

	config := coding.InteractiveDev()
	config.Workspace = workspaceStore
	config.Tasks = taskStore
	config.CommandSessions = manager
	config.Verifier.Verifier = verifierForReadmeStatus(workspaceStore, "status: fixed")
	stack, stackErr := coding.New(config)

	return agenteval.Case{
		Name:    "coding_preset_interactive_dev",
		Prompt:  "Use the watcher to diagnose README.md, repair it, stop the session, verify, and finish the task.",
		Options: stack.WithModel(modelClient),
		Assertions: []agenteval.Assertion{
			toolConstructionSucceeded(stackErr),
			agenteval.ToolUsed(commandtools.StartToolName),
			agenteval.ToolUsed(commandtools.WriteInputToolName),
			agenteval.ToolUsed("workspace_checkpoint"),
			agenteval.ToolUsed("workspace_apply_patch"),
			agenteval.ToolUsed(commandtools.StopToolName),
			agenteval.ToolUsed(verifytools.ToolName),
			agenteval.NoToolErrors(),
			agenteval.EventKindEmitted(memaxagent.EventCommandStarted),
			agenteval.EventKindEmitted(memaxagent.EventCommandInput),
			agenteval.EventKindEmitted(memaxagent.EventCommandStopped),
			agenteval.EventKindEmitted(memaxagent.EventWorkspaceCheckpoint),
			agenteval.EventKindEmitted(memaxagent.EventWorkspacePatch),
			agenteval.EventKindEmitted(memaxagent.EventVerification),
			agenteval.FinalEquals("Interactive dev preset repaired from live feedback."),
			requestCountEquals(modelClient, 8),
			{
				Name: "interactive dev preset drives managed session repair loop",
				Check: func(result agenteval.Result) error {
					toolResults := result.ToolResults()
					if len(toolResults) != 7 {
						return fmt.Errorf("tool results = %#v, want 7 tool results", toolResults)
					}
					if toolResults[1].IsError || !strings.Contains(toolResults[1].Content, "status must be fixed") {
						return fmt.Errorf("first watcher write result = %#v, want failing watcher output", toolResults[1])
					}
					if toolResults[2].IsError || !strings.Contains(toolResults[2].Content, "created workspace checkpoint checkpoint-1") {
						return fmt.Errorf("checkpoint result = %#v, want checkpoint success", toolResults[2])
					}
					if toolResults[3].IsError || !strings.Contains(toolResults[3].Content, "modified README.md") {
						return fmt.Errorf("patch result = %#v, want successful patch", toolResults[3])
					}
					if toolResults[4].IsError || !strings.Contains(toolResults[4].Content, "watch: ok") {
						return fmt.Errorf("second watcher write result = %#v, want passing watcher output", toolResults[4])
					}
					if toolResults[5].IsError || !strings.Contains(toolResults[5].Content, "status: stopped") {
						return fmt.Errorf("stop result = %#v, want stopped session", toolResults[5])
					}
					if toolResults[6].IsError || !strings.Contains(toolResults[6].Content, "verification test passed") {
						return fmt.Errorf("verification result = %#v, want verification success", toolResults[6])
					}
					startRequests := manager.StartRequests()
					if len(startRequests) != 1 {
						return fmt.Errorf("start requests = %#v, want 1", startRequests)
					}
					if !startRequests[0].TTY || startRequests[0].Purpose != "watch README status" {
						return fmt.Errorf("start request = %#v, want tty watcher start", startRequests[0])
					}
					writeRequests := manager.WriteRequests()
					if len(writeRequests) != 2 {
						return fmt.Errorf("write requests = %#v, want 2", writeRequests)
					}
					for i, req := range writeRequests {
						if req.Input != "status\n" {
							return fmt.Errorf("write request %d = %#v, want status input", i, req)
						}
					}
					stopRequests := manager.StopRequests()
					if len(stopRequests) != 1 || stopRequests[0].ID != "watch-1" {
						return fmt.Errorf("stop requests = %#v, want stop watch-1", stopRequests)
					}
					initialPrompt := modelClient.Requests()[0].SystemPrompt
					for _, want := range []string{
						"Use managed command sessions when continuous feedback helps.",
						"[in_progress] task-1",
						commandtools.StartToolName,
						commandtools.WriteInputToolName,
						commandtools.StopToolName,
						verifytools.ToolName,
					} {
						if !strings.Contains(initialPrompt, want) {
							return fmt.Errorf("initial prompt missing %q:\n%s", want, initialPrompt)
						}
					}
					finalPrompt := modelClient.Requests()[7].SystemPrompt
					for _, want := range []string{"[completed] task-1", "verification:test"} {
						if !strings.Contains(finalPrompt, want) {
							return fmt.Errorf("final prompt missing %q:\n%s", want, finalPrompt)
						}
					}
					content, err := workspaceStore.ReadFile(context.Background(), "README.md")
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

// CodingPresetInteractiveDevWaitRepair returns a single-use scenario that
// exercises the interactive_dev preset's monitor loop: start a watcher, wait
// for failing output, checkpoint and patch, wait for fresh passing output,
// stop the live session, verify, and only then finalize.
func CodingPresetInteractiveDevWaitRepair() agenteval.Case {
	workspaceStore := workspace.NewMemoryStore(map[string]string{
		"README.md": "status: broken",
	})
	taskStore := tasktools.NewMemoryStore([]tasktools.Task{{
		ID:     "task-1",
		Title:  "repair README status from wait-driven watcher feedback",
		Status: tasktools.StatusInProgress,
		Notes:  "wait for fresh watcher output, checkpoint before edits, stop the watcher, and verify before final answer",
	}})
	manager := commandtools.NewScriptedSessionManager(commandtools.ScriptedCommand{
		ID:  "watch-1",
		PID: 5152,
		Pages: []commandtools.ScriptedOutputPage{
			{
				Chunks: []commandtools.OutputChunk{{
					Seq:    1,
					Stream: "stderr",
					Text:   "watch: README.md status must be fixed\n",
				}},
				Running: true,
			},
			{
				Chunks: []commandtools.OutputChunk{
					{
						Seq:    1,
						Stream: "stderr",
						Text:   "watch: README.md status must be fixed\n",
					},
					{
						Seq:    2,
						Stream: "stdout",
						Text:   "watch: ok\n",
					},
				},
				Running: true,
			},
		},
		StopExitCode: intPtr(0),
	})
	modelClient := agenteval.NewScriptedModel(
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:    "start-1",
				Name:  commandtools.StartToolName,
				Input: json.RawMessage(`{"id":"watch-1","command":["npm","run","test:watch"],"purpose":"run README status watcher"}`),
			},
		}},
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:    "wait-1",
				Name:  commandtools.WaitOutputToolName,
				Input: json.RawMessage(`{"id":"watch-1","timeout_ms":1000}`),
			},
		}},
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:    "checkpoint-1",
				Name:  "workspace_checkpoint",
				Input: json.RawMessage(`{"label":"before wait-driven README repair"}`),
			},
		}},
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:   "patch-1",
				Name: "workspace_apply_patch",
				Input: json.RawMessage(`{"operations":[
					{"path":"README.md","old_content":"status: broken","new_content":"status: fixed"}
				]}`),
			},
		}},
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:    "wait-2",
				Name:  commandtools.WaitOutputToolName,
				Input: json.RawMessage(`{"id":"watch-1","after_seq":1,"timeout_ms":1000}`),
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
		[]model.StreamEvent{{Kind: model.StreamText, Text: "Interactive dev preset completed wait-driven repair."}},
	)

	config, configErr := coding.PresetInteractiveDev.Config()
	config.Workspace = workspaceStore
	config.Tasks = taskStore
	config.CommandSessions = manager
	config.Verifier.Verifier = verifierForReadmeStatus(workspaceStore, "status: fixed")
	stack, stackErr := coding.New(config)

	return agenteval.Case{
		Name:    "coding_preset_interactive_dev_wait_repair",
		Prompt:  "Use watch mode to repair README.md. Wait for fresh output, patch only after checkpointing, stop the live watcher, verify, and finish the task.",
		Options: stack.WithModel(modelClient),
		Assertions: []agenteval.Assertion{
			toolConstructionSucceeded(configErr, stackErr),
			agenteval.ToolUsed(commandtools.StartToolName),
			agenteval.ToolUsed(commandtools.WaitOutputToolName),
			agenteval.ToolUsed("workspace_checkpoint"),
			agenteval.ToolUsed("workspace_apply_patch"),
			agenteval.ToolUsed(commandtools.StopToolName),
			agenteval.ToolUsed(verifytools.ToolName),
			agenteval.NoToolErrors(),
			agenteval.EventKindEmitted(memaxagent.EventCommandStarted),
			agenteval.EventKindEmitted(memaxagent.EventCommandOutput),
			agenteval.EventKindEmitted(memaxagent.EventCommandStopped),
			agenteval.EventKindEmitted(memaxagent.EventWorkspaceCheckpoint),
			agenteval.EventKindEmitted(memaxagent.EventWorkspacePatch),
			agenteval.EventKindEmitted(memaxagent.EventVerification),
			agenteval.FinalEquals("Interactive dev preset completed wait-driven repair."),
			requestCountEquals(modelClient, 8),
			{
				Name: "interactive dev wait path drives repair loop",
				Check: func(result agenteval.Result) error {
					toolResults := result.ToolResults()
					if len(toolResults) != 7 {
						return fmt.Errorf("tool results = %#v, want 7 tool results", toolResults)
					}
					if toolResults[1].IsError || !strings.Contains(toolResults[1].Content, "status must be fixed") {
						return fmt.Errorf("first wait result = %#v, want failing watcher output", toolResults[1])
					}
					if toolResults[2].IsError || !strings.Contains(toolResults[2].Content, "created workspace checkpoint checkpoint-1") {
						return fmt.Errorf("checkpoint result = %#v, want checkpoint success", toolResults[2])
					}
					if toolResults[3].IsError || !strings.Contains(toolResults[3].Content, "modified README.md") {
						return fmt.Errorf("patch result = %#v, want successful patch", toolResults[3])
					}
					if toolResults[4].IsError || !strings.Contains(toolResults[4].Content, "watch: ok") {
						return fmt.Errorf("second wait result = %#v, want passing watcher output", toolResults[4])
					}
					if strings.Contains(toolResults[4].Content, "status must be fixed") {
						return fmt.Errorf("second wait result = %#v, want after_seq to suppress stale failing output", toolResults[4])
					}
					if toolResults[4].Metadata[commandtools.MetadataCommandOutputChunks] != 1 ||
						toolResults[4].Metadata[commandtools.MetadataCommandNextSeq] != 3 {
						return fmt.Errorf("second wait metadata = %#v, want one fresh chunk and next_seq=3", toolResults[4].Metadata)
					}
					if toolResults[5].IsError || !strings.Contains(toolResults[5].Content, "status: stopped") {
						return fmt.Errorf("stop result = %#v, want live watcher stopped", toolResults[5])
					}
					if toolResults[6].IsError || !strings.Contains(toolResults[6].Content, "verification test passed") {
						return fmt.Errorf("verification result = %#v, want verification success", toolResults[6])
					}
					startRequests := manager.StartRequests()
					if len(startRequests) != 1 || startRequests[0].Purpose != "run README status watcher" {
						return fmt.Errorf("start requests = %#v, want one README watcher start", startRequests)
					}
					readRequests := manager.ReadRequests()
					if len(readRequests) != 2 || readRequests[1].AfterSeq != 1 {
						return fmt.Errorf("wait/read requests = %#v, want second wait after_seq=1", readRequests)
					}
					stopRequests := manager.StopRequests()
					if len(stopRequests) != 1 || !stopRequests[0].Force || stopRequests[0].ID != "watch-1" {
						return fmt.Errorf("stop requests = %#v, want forced stop for watch-1", stopRequests)
					}
					waitEvents := 0
					for _, event := range result.Events {
						if event.Kind != memaxagent.EventCommandOutput || event.Command == nil {
							continue
						}
						if event.Command.Operation != "wait" {
							return fmt.Errorf("command output event operation = %q, want wait", event.Command.Operation)
						}
						if event.Command.Status != string(commandtools.SessionRunning) {
							return fmt.Errorf("wait event status = %q, want running before stop", event.Command.Status)
						}
						waitEvents++
					}
					if waitEvents != 2 {
						return fmt.Errorf("wait command output events = %d, want 2", waitEvents)
					}
					initialPrompt := modelClient.Requests()[0].SystemPrompt
					for _, want := range []string{
						"Wait for fresh output from watchers",
						"[in_progress] task-1",
						commandtools.StartToolName,
						commandtools.WaitOutputToolName,
						commandtools.StopToolName,
						verifytools.ToolName,
					} {
						if !strings.Contains(initialPrompt, want) {
							return fmt.Errorf("initial prompt missing %q:\n%s", want, initialPrompt)
						}
					}
					finalPrompt := modelClient.Requests()[7].SystemPrompt
					for _, want := range []string{"[completed] task-1", "verification:test"} {
						if !strings.Contains(finalPrompt, want) {
							return fmt.Errorf("final prompt missing %q:\n%s", want, finalPrompt)
						}
					}
					content, err := workspaceStore.ReadFile(context.Background(), "README.md")
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

// CodingPresetInteractiveDevSessionCleanup returns a single-use scenario that
// exercises the interactive_dev preset with a long-lived session that must be
// read incrementally, force-stopped explicitly, and confirmed absent from the
// default session listing before finalization.
func CodingPresetInteractiveDevSessionCleanup() agenteval.Case {
	workspaceStore := workspace.NewMemoryStore(map[string]string{
		"README.md": "status: broken",
	})
	taskStore := tasktools.NewMemoryStore([]tasktools.Task{{
		ID:     "task-1",
		Title:  "repair README status from watcher feedback",
		Status: tasktools.StatusInProgress,
		Notes:  "read incremental output, stop the watcher when done, and verify before final answer",
	}})
	manager := commandtools.NewScriptedSessionManager(commandtools.ScriptedCommand{
		ID:  "watch-1",
		PID: 9191,
		TTY: true,
		Pages: []commandtools.ScriptedOutputPage{
			{
				Chunks: []commandtools.OutputChunk{{
					Seq:    1,
					Stream: "pty",
					Text:   "watch: README.md status must be fixed\n",
				}},
				Running: true,
			},
			{
				Chunks: []commandtools.OutputChunk{{
					Seq:    2,
					Stream: "pty",
					Text:   "watch: ok\n",
				}},
				Running: true,
			},
		},
		StopExitCode: intPtr(137),
	})
	modelClient := agenteval.NewScriptedModel(
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:    "start-1",
				Name:  commandtools.StartToolName,
				Input: json.RawMessage(`{"id":"watch-1","command":["npm","run","watch"],"purpose":"watch README status","tty":true}`),
			},
		}},
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:    "list-1",
				Name:  commandtools.ListToolName,
				Input: json.RawMessage(`{}`),
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
				ID:    "checkpoint-1",
				Name:  "workspace_checkpoint",
				Input: json.RawMessage(`{"label":"before watcher cleanup repair"}`),
			},
		}},
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:   "patch-1",
				Name: "workspace_apply_patch",
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
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:    "list-2",
				Name:  commandtools.ListToolName,
				Input: json.RawMessage(`{}`),
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
		[]model.StreamEvent{{Kind: model.StreamText, Text: "Interactive dev preset repaired, cleaned up the watcher, and verified."}},
	)

	config, configErr := coding.PresetInteractiveDev.Config()
	config.Workspace = workspaceStore
	config.Tasks = taskStore
	config.CommandSessions = manager
	config.Verifier.Verifier = verifierForReadmeStatus(workspaceStore, "status: fixed")
	stack, stackErr := coding.New(config)

	return agenteval.Case{
		Name:    "coding_preset_interactive_dev_session_cleanup",
		Prompt:  "Use the watcher to diagnose README.md, repair it, force-stop the session if needed, confirm no sessions remain, verify, and finish the task.",
		Options: stack.WithModel(modelClient),
		Assertions: []agenteval.Assertion{
			toolConstructionSucceeded(configErr, stackErr),
			agenteval.ToolUsed(commandtools.StartToolName),
			agenteval.ToolUsed(commandtools.ListToolName),
			agenteval.ToolUsed(commandtools.ReadOutputToolName),
			agenteval.ToolUsed("workspace_checkpoint"),
			agenteval.ToolUsed("workspace_apply_patch"),
			agenteval.ToolUsed(commandtools.StopToolName),
			agenteval.ToolUsed(verifytools.ToolName),
			agenteval.NoToolErrors(),
			agenteval.EventKindEmitted(memaxagent.EventCommandStarted),
			agenteval.EventKindEmitted(memaxagent.EventCommandOutput),
			agenteval.EventKindEmitted(memaxagent.EventCommandStopped),
			agenteval.EventKindEmitted(memaxagent.EventWorkspaceCheckpoint),
			agenteval.EventKindEmitted(memaxagent.EventWorkspacePatch),
			agenteval.EventKindEmitted(memaxagent.EventVerification),
			agenteval.FinalEquals("Interactive dev preset repaired, cleaned up the watcher, and verified."),
			requestCountEquals(modelClient, 10),
			{
				Name: "interactive dev preset manages session lifecycle explicitly",
				Check: func(result agenteval.Result) error {
					toolResults := result.ToolResults()
					if len(toolResults) != 9 {
						return fmt.Errorf("tool results = %#v, want 9", toolResults)
					}
					if toolResults[1].IsError || !strings.Contains(toolResults[1].Content, "watch-1\trunning\tnpm run watch") {
						return fmt.Errorf("first list result = %#v, want running watcher session", toolResults[1])
					}
					if toolResults[2].IsError || !strings.Contains(toolResults[2].Content, "status must be fixed") {
						return fmt.Errorf("first read result = %#v, want failing watcher output", toolResults[2])
					}
					if toolResults[3].IsError || !strings.Contains(toolResults[3].Content, "created workspace checkpoint checkpoint-1") {
						return fmt.Errorf("checkpoint result = %#v, want checkpoint success", toolResults[3])
					}
					if toolResults[4].IsError || !strings.Contains(toolResults[4].Content, "modified README.md") {
						return fmt.Errorf("patch result = %#v, want successful patch", toolResults[4])
					}
					if toolResults[5].IsError || !strings.Contains(toolResults[5].Content, "watch: ok") {
						return fmt.Errorf("second read result = %#v, want passing incremental output", toolResults[5])
					}
					if toolResults[6].IsError || !strings.Contains(toolResults[6].Content, "status: stopped") {
						return fmt.Errorf("stop result = %#v, want forced stop result", toolResults[6])
					}
					if toolResults[7].IsError || toolResults[7].Content != "no command sessions" {
						return fmt.Errorf("second list result = %#v, want no running sessions after cleanup", toolResults[7])
					}
					if toolResults[8].IsError || !strings.Contains(toolResults[8].Content, "verification test passed") {
						return fmt.Errorf("verification result = %#v, want verification success", toolResults[8])
					}
					startRequests := manager.StartRequests()
					if len(startRequests) != 1 || !startRequests[0].TTY || startRequests[0].Purpose != "watch README status" {
						return fmt.Errorf("start requests = %#v, want one tty watch start request", startRequests)
					}
					readRequests := manager.ReadRequests()
					if len(readRequests) != 2 || readRequests[1].AfterSeq != 1 {
						return fmt.Errorf("read requests = %#v, want second read after_seq=1", readRequests)
					}
					stopRequests := manager.StopRequests()
					if len(stopRequests) != 1 || !stopRequests[0].Force || stopRequests[0].ID != "watch-1" {
						return fmt.Errorf("stop requests = %#v, want forced stop for watch-1", stopRequests)
					}
					initialPrompt := modelClient.Requests()[0].SystemPrompt
					for _, want := range []string{
						"Use managed command sessions when continuous feedback helps.",
						"[in_progress] task-1",
						commandtools.StartToolName,
						commandtools.ListToolName,
						commandtools.ReadOutputToolName,
						commandtools.StopToolName,
						verifytools.ToolName,
					} {
						if !strings.Contains(initialPrompt, want) {
							return fmt.Errorf("initial prompt missing %q:\n%s", want, initialPrompt)
						}
					}
					finalPrompt := modelClient.Requests()[9].SystemPrompt
					for _, want := range []string{"[completed] task-1", "verification:test"} {
						if !strings.Contains(finalPrompt, want) {
							return fmt.Errorf("final prompt missing %q:\n%s", want, finalPrompt)
						}
					}
					content, err := workspaceStore.ReadFile(context.Background(), "README.md")
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
