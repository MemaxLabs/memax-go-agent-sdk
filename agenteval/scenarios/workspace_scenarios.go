package scenarios

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
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

// CommandSessionWaitRepairLoop returns a single-use scenario where the model
// waits for managed command output instead of polling reads, repairs the
// workspace on failure, waits for passing output, then stops the session.
func CommandSessionWaitRepairLoop() agenteval.Case {
	store := workspace.NewMemoryStore(map[string]string{
		"README.md": "status: broken",
	})
	workspaceTools, workspaceErr := workspacetools.NewTools(store)
	manager := commandtools.NewScriptedSessionManager(commandtools.ScriptedCommand{
		ID:  "watch-1",
		PID: 5152,
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
		commandtools.NewWaitTool(manager),
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
				ID:    "wait-1",
				Name:  commandtools.WaitOutputToolName,
				Input: json.RawMessage(`{"id":"watch-1","timeout_ms":1000}`),
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
		[]model.StreamEvent{{Kind: model.StreamText, Text: "Watch mode passed after wait-driven repair."}},
	)

	return agenteval.Case{
		Name:   "command_session_wait_repair_loop",
		Prompt: "Start a watch command, wait for output, repair README.md if needed, wait for pass output, then stop it.",
		Options: memaxagent.Options{
			Model: modelClient,
			Tools: tool.NewRegistry(tools...),
		},
		Assertions: []agenteval.Assertion{
			toolConstructionSucceeded(workspaceErr),
			agenteval.ToolUsed(commandtools.StartToolName),
			agenteval.ToolUsed(commandtools.WaitOutputToolName),
			agenteval.ToolUsed(commandtools.StopToolName),
			agenteval.ToolUsed(workspacetools.ApplyPatchToolName),
			agenteval.EventKindEmitted(memaxagent.EventCommandStarted),
			agenteval.EventKindEmitted(memaxagent.EventCommandOutput),
			agenteval.EventKindEmitted(memaxagent.EventCommandStopped),
			agenteval.EventKindEmitted(memaxagent.EventWorkspacePatch),
			agenteval.FinalEquals("Watch mode passed after wait-driven repair."),
			requestCountEquals(modelClient, 6),
			{
				Name: "wait-driven command output drives repair loop",
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
						return fmt.Errorf("read requests = %#v, want wait path to read twice with second after_seq=1", readRequests)
					}
					stopRequests := manager.StopRequests()
					if len(stopRequests) != 1 || stopRequests[0].ID != "watch-1" {
						return fmt.Errorf("stop requests = %#v, want stop watch-1", stopRequests)
					}
					results := result.ToolResults()
					sawFail := false
					sawPass := false
					for _, toolResult := range results {
						if toolResult.Name != commandtools.WaitOutputToolName {
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
						return fmt.Errorf("tool results = %#v, want failing and passing wait output", results)
					}
					return nil
				},
			},
		},
	}
}

// CommandSessionInteractiveRepairLoop returns a single-use scenario where the
// model interacts with a managed command session through stdin writes, repairs
// the workspace, observes passing output, and exits the session.
func CommandSessionInteractiveRepairLoop() agenteval.Case {
	store := workspace.NewMemoryStore(map[string]string{
		"README.md": "status: broken",
	})
	workspaceTools, workspaceErr := workspacetools.NewTools(store)
	manager := commandtools.NewScriptedSessionManager(commandtools.ScriptedCommand{
		ID:  "watch-1",
		PID: 5151,
		TTY: true,
		WritePages: []commandtools.ScriptedWritePage{
			{
				Page: commandtools.ScriptedOutputPage{
					Chunks: []commandtools.OutputChunk{{
						Seq:    1,
						Stream: "pty",
						Text:   "watch: README.md status must be fixed\n",
					}},
					Running: true,
				},
			},
			{
				Page: commandtools.ScriptedOutputPage{
					Chunks: []commandtools.OutputChunk{{
						Seq:    2,
						Stream: "pty",
						Text:   "watch: ok\n",
					}},
					Running: true,
				},
			},
			{
				Page: commandtools.ScriptedOutputPage{
					Chunks: []commandtools.OutputChunk{{
						Seq:    3,
						Stream: "pty",
						Text:   "watch: bye\n",
					}},
					Running:  false,
					ExitCode: intPtr(0),
				},
			},
		},
	})
	tools := append(workspaceTools,
		commandtools.NewStartTool(manager),
		commandtools.NewWriteInputTool(manager),
	)
	modelClient := agenteval.NewScriptedModel(
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:    "start-1",
				Name:  commandtools.StartToolName,
				Input: json.RawMessage(`{"id":"watch-1","command":["npm","run","test:watch"],"purpose":"start interactive watch mode","tty":true}`),
			},
		}},
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:    "write-1",
				Name:  commandtools.WriteInputToolName,
				Input: json.RawMessage(`{"id":"watch-1","input":"check","append_newline":true}`),
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
				ID:    "write-2",
				Name:  commandtools.WriteInputToolName,
				Input: json.RawMessage(`{"id":"watch-1","input":"check","append_newline":true}`),
			},
		}},
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:    "write-3",
				Name:  commandtools.WriteInputToolName,
				Input: json.RawMessage(`{"id":"watch-1","input":"exit","append_newline":true}`),
			},
		}},
		[]model.StreamEvent{{Kind: model.StreamText, Text: "Interactive watch passed after repair."}},
	)

	return agenteval.Case{
		Name:   "command_session_interactive_repair_loop",
		Prompt: "Start an interactive watch command, send checks through stdin, repair README.md if it fails, confirm it passes, then exit the session.",
		Options: memaxagent.Options{
			Model: modelClient,
			Tools: tool.NewRegistry(tools...),
		},
		Assertions: []agenteval.Assertion{
			toolConstructionSucceeded(workspaceErr),
			agenteval.ToolUsed(commandtools.StartToolName),
			agenteval.ToolUsed(commandtools.WriteInputToolName),
			agenteval.ToolUsed(workspacetools.ApplyPatchToolName),
			agenteval.EventKindEmitted(memaxagent.EventCommandStarted),
			agenteval.EventKindEmitted(memaxagent.EventCommandInput),
			agenteval.EventKindEmitted(memaxagent.EventWorkspacePatch),
			agenteval.FinalEquals("Interactive watch passed after repair."),
			requestCountEquals(modelClient, 6),
			{
				Name: "interactive command writes drive repair loop",
				Check: func(result agenteval.Result) error {
					content, err := store.ReadFile(context.Background(), "README.md")
					if err != nil {
						return err
					}
					if content != "status: fixed" {
						return fmt.Errorf("README.md = %q, want repaired content", content)
					}
					writeRequests := manager.WriteRequests()
					if len(writeRequests) != 3 ||
						writeRequests[0].Input != "check\n" ||
						writeRequests[1].Input != "check\n" ||
						writeRequests[2].Input != "exit\n" {
						return fmt.Errorf("write requests = %#v, want check/check/exit with newline", writeRequests)
					}
					startRequests := manager.StartRequests()
					if len(startRequests) != 1 || !startRequests[0].TTY {
						return fmt.Errorf("start requests = %#v, want one tty start request", startRequests)
					}
					results := result.ToolResults()
					sawFail := false
					sawPass := false
					sawExit := false
					sawPTY := false
					for _, toolResult := range results {
						if toolResult.Name != commandtools.WriteInputToolName {
							continue
						}
						if strings.Contains(toolResult.Content, "status must be fixed") {
							sawFail = true
						}
						if strings.Contains(toolResult.Content, "watch: ok") {
							sawPass = true
						}
						if strings.Contains(toolResult.Content, "[pty #") && strings.Contains(toolResult.Content, "tty: true") {
							sawPTY = true
						}
						if strings.Contains(toolResult.Content, "watch: bye") && strings.Contains(toolResult.Content, "status: exited") {
							sawExit = true
						}
					}
					if !sawFail || !sawPass || !sawExit || !sawPTY {
						return fmt.Errorf("tool results = %#v, want PTY failing, passing, and exit write outputs", results)
					}
					return nil
				},
			},
		},
	}
}

// CommandSessionTTYResize returns a single-use scenario where the model starts
// a PTY-backed session, resizes it, then stops it cleanly.
func CommandSessionTTYResize() agenteval.Case {
	manager := commandtools.NewScriptedSessionManager(commandtools.ScriptedCommand{
		ID:           "shell-1",
		PID:          7171,
		TTY:          true,
		Cols:         80,
		Rows:         24,
		StopExitCode: intPtr(0),
	})
	modelClient := agenteval.NewScriptedModel(
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:    "start-1",
				Name:  commandtools.StartToolName,
				Input: json.RawMessage(`{"id":"shell-1","command":["bash"],"purpose":"start interactive shell","tty":true,"cols":80,"rows":24}`),
			},
		}},
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:    "resize-1",
				Name:  commandtools.ResizeToolName,
				Input: json.RawMessage(`{"id":"shell-1","cols":120,"rows":40}`),
			},
		}},
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:    "stop-1",
				Name:  commandtools.StopToolName,
				Input: json.RawMessage(`{"id":"shell-1","force":true}`),
			},
		}},
		[]model.StreamEvent{{Kind: model.StreamText, Text: "PTY session resized and stopped."}},
	)

	return agenteval.Case{
		Name:   "command_session_tty_resize",
		Prompt: "Start a PTY shell session, resize it for a wider terminal, then stop it.",
		Options: memaxagent.Options{
			Model: modelClient,
			Tools: tool.NewRegistry(
				commandtools.NewStartTool(manager),
				commandtools.NewResizeTool(manager),
				commandtools.NewStopTool(manager),
			),
		},
		Assertions: []agenteval.Assertion{
			agenteval.ToolUsed(commandtools.StartToolName),
			agenteval.ToolUsed(commandtools.ResizeToolName),
			agenteval.ToolUsed(commandtools.StopToolName),
			agenteval.NoToolErrors(),
			agenteval.EventKindEmitted(memaxagent.EventCommandStarted),
			agenteval.EventKindEmitted(memaxagent.EventCommandResized),
			agenteval.EventKindEmitted(memaxagent.EventCommandStopped),
			agenteval.FinalEquals("PTY session resized and stopped."),
			requestCountEquals(modelClient, 4),
			{
				Name: "tty start and resize requests are captured",
				Check: func(result agenteval.Result) error {
					startRequests := manager.StartRequests()
					if len(startRequests) != 1 || !startRequests[0].TTY || startRequests[0].Cols != 80 || startRequests[0].Rows != 24 {
						return fmt.Errorf("start requests = %#v, want one 80x24 tty request", startRequests)
					}
					resizeRequests := manager.ResizeRequests()
					if len(resizeRequests) != 1 || resizeRequests[0].Cols != 120 || resizeRequests[0].Rows != 40 {
						return fmt.Errorf("resize requests = %#v, want one 120x40 resize", resizeRequests)
					}
					results := result.ToolResults()
					sawResize := false
					sawStopped := false
					for _, toolResult := range results {
						if toolResult.Name == commandtools.ResizeToolName && strings.Contains(toolResult.Content, "size: 120x40") {
							sawResize = true
						}
						if toolResult.Name == commandtools.StopToolName && strings.Contains(toolResult.Content, "size: 120x40") {
							sawStopped = true
						}
					}
					if !sawResize || !sawStopped {
						return fmt.Errorf("tool results = %#v, want resize and stop outputs with 120x40 geometry", results)
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

// WorkspaceGitStorePatchRollback returns a single-use scenario that exercises
// the standard workspace tools against a git-backed workspace adapter.
func WorkspaceGitStorePatchRollback() agenteval.Case {
	root, setupErr := os.MkdirTemp("", "memax-workspace-git-*")
	if setupErr == nil {
		setupErr = os.WriteFile(filepath.Join(root, "README.md"), []byte("version one"), 0o644)
	}
	if setupErr == nil {
		if _, err := exec.LookPath("git"); err != nil {
			setupErr = fmt.Errorf("git not available: %w", err)
		}
	}
	if setupErr == nil {
		cmd := exec.Command("git", "-C", root, "init")
		if out, err := cmd.CombinedOutput(); err != nil {
			setupErr = fmt.Errorf("git init: %v: %s", err, strings.TrimSpace(string(out)))
		}
	}
	var store *workspace.GitStore
	if setupErr == nil {
		store, setupErr = workspace.NewGitStore(root)
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
				Input: json.RawMessage(`{"label":"before git patch"}`),
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
				Input: json.RawMessage(`{"base_id":"checkpoint-1"}`),
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
		[]model.StreamEvent{{Kind: model.StreamText, Text: "Git-backed workspace restored."}},
	)

	return agenteval.Case{
		Name:   "workspace_git_store_patch_rollback",
		Prompt: "Patch README.md in the git-backed workspace, inspect the diff, then restore the checkpoint.",
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
			agenteval.ToolUsed(workspacetools.DiffToolName),
			agenteval.ToolUsed(workspacetools.RestoreToolName),
			agenteval.ToolUsed(workspacetools.ReadToolName),
			agenteval.NoToolErrors(),
			agenteval.FinalEquals("Git-backed workspace restored."),
			requestCountEquals(modelClient, 6),
			{
				Name: "git-backed content restored",
				Check: func(result agenteval.Result) error {
					content, err := os.ReadFile(filepath.Join(root, "README.md"))
					if err != nil {
						return err
					}
					if string(content) != "version one" {
						return fmt.Errorf("README.md = %q, want restored version one", content)
					}
					checkpoints, err := store.ListCheckpoints(context.Background())
					if err != nil {
						return err
					}
					if len(checkpoints) < 2 || checkpoints[1].ID != "checkpoint-1" || checkpoints[1].Label != "before git patch" {
						return fmt.Errorf("checkpoints = %#v, want persisted git checkpoint", checkpoints)
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

// PlannerWorkspaceCommandRepairLoop returns a single-use scenario where a
// planner-guided task uses managed command-session output to drive repair, is
// forced through checkpoint and verify-before-final policies, and only
// finalizes after verification marks the task completed.
func PlannerWorkspaceCommandRepairLoop() agenteval.Case {
	workspaceStore := workspace.NewMemoryStore(map[string]string{
		"README.md": "status: broken",
	})
	taskStore := tasktools.NewMemoryStore([]tasktools.Task{{
		ID:     "task-1",
		Title:  "repair README status from watch output",
		Status: tasktools.StatusInProgress,
		Notes:  "use watch output, checkpoint before mutating, verify before final answer",
	}})
	workspaceTools, toolsErr := workspacetools.NewTools(workspaceStore)
	manager := commandtools.NewScriptedSessionManager(commandtools.ScriptedCommand{
		ID:  "watch-1",
		PID: 8181,
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
				Running: true,
			},
		},
		StopExitCode: intPtr(0),
	})
	verifyTool := verifytools.NewTool(verifytools.Config{
		Verifier: tasktools.NewVerificationProgressVerifier(
			taskStore,
			verifierForReadmeStatus(workspaceStore, "status: fixed"),
			tasktools.WithVerificationFailStatus(tasktools.StatusInProgress),
		),
	})
	checkpointPolicy := agentpolicy.RequireCheckpointBeforePatch()
	finalPolicy := agentpolicy.RequireVerificationBeforeFinal()
	modelClient := agenteval.NewScriptedModel(
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:    "start-1",
				Name:  commandtools.StartToolName,
				Input: json.RawMessage(`{"id":"watch-1","command":["npm","run","test:watch"],"purpose":"watch README status"}`),
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
				ID:    "checkpoint-1",
				Name:  workspacetools.CheckpointToolName,
				Input: json.RawMessage(`{"label":"before README repair"}`),
			},
		}},
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:   "patch-2",
				Name: workspacetools.ApplyPatchToolName,
				// Repeat the exact guarded edit after checkpoint creation. This keeps
				// the scenario honest about the policy boundary: patch-2 only succeeds
				// if patch-1 was denied before mutating the workspace.
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
		[]model.StreamEvent{{Kind: model.StreamText, Text: "README fixed from watch output."}},
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
		[]model.StreamEvent{{Kind: model.StreamText, Text: "README fixed, verified, and task completed."}},
	)

	return agenteval.Case{
		Name:   "planner_workspace_command_repair_loop",
		Prompt: "Use the watch command to diagnose README.md, repair it safely, verify it, and finish the active task.",
		Options: memaxagent.Options{
			Model: modelClient,
			Tools: tool.NewRegistry(append(workspaceTools,
				commandtools.NewStartTool(manager),
				commandtools.NewReadOutputTool(manager),
				commandtools.NewStopTool(manager),
				verifyTool,
			)...),
			Hooks: hook.NewRunner(append(checkpointPolicy.Options(), finalPolicy.Options()...)...),
			Planner: tasktools.Planner(taskStore,
				planner.WithTaskGoal("repair README status using watch output and verified evidence"),
				planner.WithTaskToolHints(
					commandtools.StartToolName,
					commandtools.ReadOutputToolName,
					commandtools.StopToolName,
					workspacetools.CheckpointToolName,
					workspacetools.ApplyPatchToolName,
					verifytools.ToolName,
				),
				planner.WithTaskVerificationHints("call workspace_verify with metadata.task_id before final answer"),
			),
		},
		Assertions: []agenteval.Assertion{
			toolConstructionSucceeded(toolsErr),
			agenteval.ToolUsed(commandtools.StartToolName),
			agenteval.ToolUsed(commandtools.ReadOutputToolName),
			agenteval.ToolUsed(workspacetools.ApplyPatchToolName),
			agenteval.ToolUsed(workspacetools.CheckpointToolName),
			agenteval.ToolUsed(commandtools.StopToolName),
			agenteval.ToolUsed(verifytools.ToolName),
			agenteval.EventKindEmitted(memaxagent.EventCommandStarted),
			agenteval.EventKindEmitted(memaxagent.EventCommandOutput),
			agenteval.EventKindEmitted(memaxagent.EventCommandStopped),
			agenteval.EventKindEmitted(memaxagent.EventWorkspaceCheckpoint),
			agenteval.EventKindEmitted(memaxagent.EventWorkspacePatch),
			agenteval.EventKindEmitted(memaxagent.EventVerification),
			agenteval.FinalEquals("README fixed, verified, and task completed."),
			requestCountEquals(modelClient, 10),
			{
				Name: "planner-guided loop composes command repair checkpoint and verification",
				Check: func(result agenteval.Result) error {
					results := result.ToolResults()
					if len(results) < 8 {
						return fmt.Errorf("tool results = %#v, want watch repair loop", results)
					}
					if results[1].IsError || !strings.Contains(results[1].Content, "status must be fixed") {
						return fmt.Errorf("first read result = %#v, want failing watch output", results[1])
					}
					if !results[2].IsError || !strings.Contains(results[2].Content, agentpolicy.CheckpointBeforePatchReason()) {
						return fmt.Errorf("first patch result = %#v, want checkpoint denial", results[2])
					}
					workspacePatchEvents := 0
					workspaceCheckpointEvents := 0
					for _, event := range result.Events {
						switch event.Kind {
						case memaxagent.EventWorkspacePatch:
							workspacePatchEvents++
						case memaxagent.EventWorkspaceCheckpoint:
							workspaceCheckpointEvents++
						}
					}
					if workspacePatchEvents != 1 {
						return fmt.Errorf("workspace patch events = %d, want exactly one successful patch after denial", workspacePatchEvents)
					}
					if workspaceCheckpointEvents != 1 {
						return fmt.Errorf("workspace checkpoint events = %d, want exactly one checkpoint event", workspaceCheckpointEvents)
					}
					if results[3].IsError || !strings.Contains(results[3].Content, "created workspace checkpoint checkpoint-1") {
						return fmt.Errorf("checkpoint result = %#v, want checkpoint success", results[3])
					}
					if results[4].IsError || !strings.Contains(results[4].Content, "modified README.md") {
						return fmt.Errorf("second patch result = %#v, want successful patch", results[4])
					}
					if results[5].IsError || !strings.Contains(results[5].Content, "watch: ok") {
						return fmt.Errorf("second read result = %#v, want passing watch output", results[5])
					}
					if results[6].IsError || !strings.Contains(results[6].Content, "status: stopped") {
						return fmt.Errorf("stop result = %#v, want clean stop", results[6])
					}
					if results[7].IsError || !strings.Contains(results[7].Content, "verification test passed") {
						return fmt.Errorf("verification result = %#v, want verification success", results[7])
					}
					if results[7].Metadata == nil {
						return fmt.Errorf("verification metadata = nil, want completed task update")
					}
					if results[7].Metadata[model.MetadataTaskStatus] != string(tasktools.StatusCompleted) {
						return fmt.Errorf("verification metadata = %#v, want completed task update", results[7].Metadata)
					}
					content, err := workspaceStore.ReadFile(context.Background(), "README.md")
					if err != nil {
						return err
					}
					if content != "status: fixed" {
						return fmt.Errorf("README.md = %q, want repaired content", content)
					}
					startRequests := manager.StartRequests()
					if len(startRequests) != 1 || startRequests[0].Purpose != "watch README status" {
						return fmt.Errorf("start requests = %#v, want one watch start request", startRequests)
					}
					readRequests := manager.ReadRequests()
					if len(readRequests) != 2 || readRequests[1].AfterSeq != 1 {
						return fmt.Errorf("read requests = %#v, want second read after_seq=1", readRequests)
					}
					stopRequests := manager.StopRequests()
					if len(stopRequests) != 1 || stopRequests[0].ID != "watch-1" {
						return fmt.Errorf("stop requests = %#v, want stop watch-1", stopRequests)
					}
					tasks, err := taskStore.List(context.Background())
					if err != nil {
						return err
					}
					if len(tasks) != 1 || tasks[0].Status != tasktools.StatusCompleted {
						return fmt.Errorf("tasks = %#v, want completed task", tasks)
					}
					if !strings.Contains(strings.Join(tasks[0].Evidence, ","), "verification:test") {
						return fmt.Errorf("task evidence = %#v, want verification evidence", tasks[0].Evidence)
					}
					return nil
				},
			},
			{
				Name: "planner prompt and finalization retry reflect composed workflow",
				Check: func(agenteval.Result) error {
					requests := modelClient.Requests()
					if len(requests) < 10 {
						return fmt.Errorf("requests = %d, want 10", len(requests))
					}
					initialPrompt := requests[0].SystemPrompt
					for _, want := range []string{
						"repair README status using watch output and verified evidence",
						"[in_progress] task-1",
						"use watch output, checkpoint before mutating, verify before final answer",
						commandtools.StartToolName,
						verifytools.ToolName,
						"call workspace_verify with metadata.task_id before final answer",
					} {
						if !strings.Contains(initialPrompt, want) {
							return fmt.Errorf("initial prompt missing %q:\n%s", want, initialPrompt)
						}
					}
					verifyRequest := requests[8]
					if len(verifyRequest.Messages) == 0 {
						return fmt.Errorf("verify request missing messages")
					}
					last := verifyRequest.Messages[len(verifyRequest.Messages)-1].PlainText()
					if !strings.Contains(last, agentpolicy.VerifyBeforeFinalReason()) {
						return fmt.Errorf("verify retry prompt = %q, want verify-before-final reason", last)
					}
					finalPrompt := requests[9].SystemPrompt
					for _, want := range []string{"[completed] task-1", "verification:test", "README.md"} {
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

// PlannerWorkspaceCommandRollbackRepairLoop returns a single-use scenario
// where a planner-guided task uses managed command output, checkpoints before a
// risky edit, receives rollback guidance from failed verification, restores the
// checkpoint, and repairs again before completing the task.
func PlannerWorkspaceCommandRollbackRepairLoop() agenteval.Case {
	const goodReadme = "status: fixed\nowner: api\n"
	workspaceStore := workspace.NewMemoryStore(map[string]string{
		"README.md": "status: broken\nowner: api\n",
	})
	taskStore := tasktools.NewMemoryStore([]tasktools.Task{{
		ID:     "task-1",
		Title:  "repair README status without changing owner",
		Status: tasktools.StatusInProgress,
		Notes:  "use watch output, checkpoint before risky edits, rollback on failed verification",
	}})
	workspaceTools, toolsErr := workspacetools.NewTools(workspaceStore)
	manager := commandtools.NewScriptedSessionManager(commandtools.ScriptedCommand{
		ID:  "watch-1",
		PID: 8282,
		Pages: []commandtools.ScriptedOutputPage{
			{
				Chunks: []commandtools.OutputChunk{{
					Seq:    1,
					Stream: "stdout",
					Text:   "watch: README.md status must be fixed\nhint: owner line must stay api\n",
				}},
				Running: true,
			},
			{
				Chunks: []commandtools.OutputChunk{{
					Seq:    2,
					Stream: "stdout",
					Text:   "watch: status fixed but owner changed\n",
				}},
				Running: true,
			},
			{
				Chunks: []commandtools.OutputChunk{{
					Seq:    3,
					Stream: "stdout",
					Text:   "watch: ok\n",
				}},
				Running: true,
			},
		},
		StopExitCode: intPtr(0),
	})
	verifier := verifytools.VerifierFunc(func(ctx context.Context, req verifytools.Request) (verifytools.Result, error) {
		content, err := workspaceStore.ReadFile(ctx, "README.md")
		if err != nil {
			return verifytools.Result{}, err
		}
		if content == goodReadme {
			return verifytools.Result{
				Name:   req.Name,
				Passed: true,
				Output: "README.md status fixed and owner preserved.",
			}, nil
		}
		return verifytools.Result{
			Name:   req.Name,
			Passed: false,
			Output: fmt.Sprintf("got %q; expected %q", content, goodReadme),
			Diagnostics: []verifytools.Diagnostic{{
				Path:     "README.md",
				Severity: "error",
				Message:  "status must be fixed and owner must remain api",
			}},
		}, nil
	})
	rollbackPolicy := agentpolicy.RecommendRollbackOnFailedVerification()
	verifyTool := verifytools.NewTool(verifytools.Config{
		Verifier: rollbackPolicy.WrapVerifier(tasktools.NewVerificationProgressVerifier(
			taskStore,
			verifier,
			tasktools.WithVerificationFailStatus(tasktools.StatusInProgress),
		)),
	})
	checkpointPolicy := agentpolicy.RequireCheckpointBeforePatch()
	finalPolicy := agentpolicy.RequireVerificationBeforeFinal()
	hookOptions := append(checkpointPolicy.Options(), finalPolicy.Options()...)
	hookOptions = append(hookOptions, rollbackPolicy.Options()...)
	modelClient := agenteval.NewScriptedModel(
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:    "start-1",
				Name:  commandtools.StartToolName,
				Input: json.RawMessage(`{"id":"watch-1","command":["npm","run","test:watch"],"purpose":"watch README status and owner"}`),
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
					{"path":"README.md","old_content":"status: broken\nowner: api\n","new_content":"status: fixed\nowner: ops\n"}
				]}`),
			},
		}},
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:    "checkpoint-1",
				Name:  workspacetools.CheckpointToolName,
				Input: json.RawMessage(`{"label":"before README repair"}`),
			},
		}},
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:   "patch-2",
				Name: workspacetools.ApplyPatchToolName,
				Input: json.RawMessage(`{"operations":[
					{"path":"README.md","old_content":"status: broken\nowner: api\n","new_content":"status: fixed\nowner: ops\n"}
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
				Name:  workspacetools.RestoreToolName,
				Input: json.RawMessage(`{"id":"checkpoint-1"}`),
			},
		}},
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:   "patch-3",
				Name: workspacetools.ApplyPatchToolName,
				Input: json.RawMessage(`{"operations":[
					{"path":"README.md","old_content":"status: broken\nowner: api\n","new_content":"status: fixed\nowner: api\n"}
				]}`),
			},
		}},
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:    "read-3",
				Name:  commandtools.ReadOutputToolName,
				Input: json.RawMessage(`{"id":"watch-1","after_seq":2}`),
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
				ID:   "verify-2",
				Name: verifytools.ToolName,
				Input: json.RawMessage(`{
					"name":"test",
					"target":"README.md",
					"metadata":{"task_id":"task-1"}
				}`),
			},
		}},
		[]model.StreamEvent{{Kind: model.StreamText, Text: "README restored, repaired, verified, and task completed."}},
	)

	return agenteval.Case{
		Name:   "planner_workspace_command_rollback_repair_loop",
		Prompt: "Use the watch command to diagnose README.md, repair it safely without changing the owner, verify it, and finish the active task.",
		Options: memaxagent.Options{
			Model: modelClient,
			Tools: tool.NewRegistry(append(workspaceTools,
				commandtools.NewStartTool(manager),
				commandtools.NewReadOutputTool(manager),
				commandtools.NewStopTool(manager),
				verifyTool,
			)...),
			Hooks: hook.NewRunner(hookOptions...),
			Planner: tasktools.Planner(taskStore,
				planner.WithTaskGoal("repair README status using watch output, rollback guidance, and verified evidence"),
				planner.WithTaskToolHints(
					commandtools.StartToolName,
					commandtools.ReadOutputToolName,
					commandtools.StopToolName,
					workspacetools.CheckpointToolName,
					workspacetools.ApplyPatchToolName,
					workspacetools.RestoreToolName,
					verifytools.ToolName,
				),
				planner.WithTaskVerificationHints("call workspace_verify with metadata.task_id before final answer"),
			),
		},
		Assertions: []agenteval.Assertion{
			toolConstructionSucceeded(toolsErr),
			agenteval.ToolUsed(commandtools.StartToolName),
			agenteval.ToolUsed(commandtools.ReadOutputToolName),
			agenteval.ToolUsed(workspacetools.ApplyPatchToolName),
			agenteval.ToolUsed(workspacetools.CheckpointToolName),
			agenteval.ToolUsed(workspacetools.RestoreToolName),
			agenteval.ToolUsed(commandtools.StopToolName),
			agenteval.ToolUsed(verifytools.ToolName),
			agenteval.EventKindEmitted(memaxagent.EventCommandStarted),
			agenteval.EventKindEmitted(memaxagent.EventCommandOutput),
			agenteval.EventKindEmitted(memaxagent.EventCommandStopped),
			agenteval.EventKindEmitted(memaxagent.EventWorkspaceCheckpoint),
			agenteval.EventKindEmitted(memaxagent.EventWorkspacePatch),
			agenteval.EventKindEmitted(memaxagent.EventWorkspaceRestore),
			agenteval.EventKindEmitted(memaxagent.EventVerification),
			agenteval.FinalEquals("README restored, repaired, verified, and task completed."),
			requestCountEquals(modelClient, 13),
			{
				Name: "rollback loop restores checkpoint and resumes command output by cursor",
				Check: func(result agenteval.Result) error {
					results := result.ToolResults()
					if len(results) < 12 {
						return fmt.Errorf("tool results = %d (%#v), want at least 12 for rollback repair loop", len(results), results)
					}
					if results[1].IsError ||
						!strings.Contains(results[1].Content, "owner line must stay api") ||
						!strings.Contains(results[1].Content, "resume_after_seq: 1") {
						return fmt.Errorf("first read result = %#v, want owner hint and resume cursor", results[1])
					}
					if !results[2].IsError || !strings.Contains(results[2].Content, agentpolicy.CheckpointBeforePatchReason()) {
						return fmt.Errorf("first patch result = %#v, want checkpoint denial", results[2])
					}
					if results[3].IsError || !strings.Contains(results[3].Content, "created workspace checkpoint checkpoint-1") {
						return fmt.Errorf("checkpoint result = %#v, want checkpoint success", results[3])
					}
					if results[4].IsError || !strings.Contains(results[4].Content, "modified README.md") {
						return fmt.Errorf("bad patch result = %#v, want successful risky patch", results[4])
					}
					if results[5].IsError ||
						!strings.Contains(results[5].Content, "owner changed") ||
						!strings.Contains(results[5].Content, "resume_after_seq: 2") {
						return fmt.Errorf("second read result = %#v, want cursor-derived owner warning", results[5])
					}
					if !results[6].IsError || !strings.Contains(results[6].Content, "Rollback policy: restore workspace checkpoint checkpoint-1") {
						return fmt.Errorf("failed verification = %#v, want rollback guidance", results[6])
					}
					if results[6].Metadata[agentpolicy.MetadataRollbackRecommended] != true ||
						results[6].Metadata[agentpolicy.MetadataRollbackCheckpointID] != "checkpoint-1" {
						return fmt.Errorf("failed verification metadata = %#v, want rollback checkpoint", results[6].Metadata)
					}
					if results[6].Metadata[model.MetadataTaskStatus] != string(tasktools.StatusInProgress) {
						return fmt.Errorf("failed verification metadata = %#v, want in-progress task", results[6].Metadata)
					}
					if results[7].IsError || !strings.Contains(results[7].Content, "restored workspace checkpoint checkpoint-1") {
						return fmt.Errorf("restore result = %#v, want checkpoint restore", results[7])
					}
					if results[8].IsError || !strings.Contains(results[8].Content, "modified README.md") {
						return fmt.Errorf("repair patch result = %#v, want successful repair", results[8])
					}
					if results[9].IsError ||
						!strings.Contains(results[9].Content, "watch: ok") ||
						!strings.Contains(results[9].Content, "resume_after_seq: 3") {
						return fmt.Errorf("third read result = %#v, want ok output and resume cursor", results[9])
					}
					if results[10].IsError || !strings.Contains(results[10].Content, "status: stopped") {
						return fmt.Errorf("stop result = %#v, want clean stop", results[10])
					}
					if results[11].IsError || !strings.Contains(results[11].Content, "verification test passed") {
						return fmt.Errorf("passing verification = %#v, want success", results[11])
					}
					if results[11].Metadata[model.MetadataTaskStatus] != string(tasktools.StatusCompleted) {
						return fmt.Errorf("passing verification metadata = %#v, want completed task", results[11].Metadata)
					}
					content, err := workspaceStore.ReadFile(context.Background(), "README.md")
					if err != nil {
						return err
					}
					if content != goodReadme {
						return fmt.Errorf("README.md = %q, want repaired content", content)
					}
					readRequests := manager.ReadRequests()
					if len(readRequests) != 3 ||
						readRequests[0].AfterSeq != 0 ||
						readRequests[1].AfterSeq != 1 ||
						readRequests[2].AfterSeq != 2 {
						return fmt.Errorf("read requests = %#v, want cursor progression 0,1,2", readRequests)
					}
					return nil
				},
			},
			{
				Name: "rollback loop keeps planner task and event history coherent",
				Check: func(result agenteval.Result) error {
					var patches, checkpoints, restores, verifications int
					for _, event := range result.Events {
						switch event.Kind {
						case memaxagent.EventWorkspacePatch:
							patches++
						case memaxagent.EventWorkspaceCheckpoint:
							checkpoints++
						case memaxagent.EventWorkspaceRestore:
							restores++
						case memaxagent.EventVerification:
							verifications++
						}
					}
					if patches != 2 || checkpoints != 1 || restores != 1 || verifications != 2 {
						return fmt.Errorf("events patch/checkpoint/restore/verify = %d/%d/%d/%d, want 2/1/1/2", patches, checkpoints, restores, verifications)
					}
					tasks, err := taskStore.List(context.Background())
					if err != nil {
						return err
					}
					if len(tasks) != 1 || tasks[0].Status != tasktools.StatusCompleted {
						return fmt.Errorf("tasks = %#v, want completed task", tasks)
					}
					if !strings.Contains(strings.Join(tasks[0].Evidence, ","), "verification:test") {
						return fmt.Errorf("task evidence = %#v, want verification evidence", tasks[0].Evidence)
					}
					requests := modelClient.Requests()
					if len(requests) < 13 {
						return fmt.Errorf("requests = %d, want 13", len(requests))
					}
					initialPrompt := requests[0].SystemPrompt
					for _, want := range []string{
						"repair README status using watch output, rollback guidance, and verified evidence",
						"[in_progress] task-1",
						"rollback on failed verification",
						workspacetools.RestoreToolName,
						verifytools.ToolName,
					} {
						if !strings.Contains(initialPrompt, want) {
							return fmt.Errorf("initial prompt missing %q:\n%s", want, initialPrompt)
						}
					}
					finalPrompt := requests[12].SystemPrompt
					for _, want := range []string{"[completed] task-1", "verification:test", "README.md"} {
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
