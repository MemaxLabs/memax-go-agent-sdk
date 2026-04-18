package scenarios

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	memaxagent "github.com/MemaxLabs/memax-go-agent-sdk"
	"github.com/MemaxLabs/memax-go-agent-sdk/agenteval"
	"github.com/MemaxLabs/memax-go-agent-sdk/model"
	"github.com/MemaxLabs/memax-go-agent-sdk/stack/coding"
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
