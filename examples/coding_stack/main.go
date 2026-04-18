package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"

	memaxagent "github.com/MemaxLabs/memax-go-agent-sdk"
	"github.com/MemaxLabs/memax-go-agent-sdk/model"
	"github.com/MemaxLabs/memax-go-agent-sdk/stack/coding"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/approvaltools"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/commandtools"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/tasktools"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/verifytools"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/workspacetools"
	"github.com/MemaxLabs/memax-go-agent-sdk/workspace"
)

func main() {
	if err := runExample(context.Background(), os.Stdout); err != nil {
		log.Fatal(err)
	}
}

func runExample(ctx context.Context, w io.Writer) error {
	ws := workspace.NewMemoryStore(map[string]string{
		"README.md": "status: broken",
	})
	tasks := tasktools.NewMemoryStore([]tasktools.Task{{
		ID:     "task-1",
		Title:  "Repair README status through the coding stack",
		Status: tasktools.StatusInProgress,
		Notes:  "checkpoint before edits, request approval for the patch, rerun the relevant check, and verify before final answer",
	}})
	runner := commandtools.NewScriptedRunner(
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

	config := coding.CIRepair()
	config.Workspace = ws
	config.Tasks = tasks
	config.Command.Runner = runner
	config.Verifier.Verifier = verifytools.VerifierFunc(func(ctx context.Context, req verifytools.Request) (verifytools.Result, error) {
		content, err := ws.ReadFile(ctx, "README.md")
		if err != nil {
			return verifytools.Result{}, err
		}
		if content == "status: fixed" {
			return verifytools.Result{
				Name:   req.Name,
				Passed: true,
				Output: "README.md matched expected status.",
			}, nil
		}
		return verifytools.Result{
			Name:   req.Name,
			Passed: false,
			Output: "README.md still needs the fixed status",
		}, nil
	})
	config.Approval.Approver = approvaltools.StaticApprover{
		Decision: approvaltools.Decision{
			Approved: true,
			Reason:   "approved example repair",
		},
	}
	config.Policies.RequirePatchApproval = true

	stack, err := coding.New(config)
	if err != nil {
		return err
	}

	events, err := memaxagent.Query(ctx, "Repair README.md through the coding stack and finish.", stack.WithModel(&stackModel{}))
	if err != nil {
		return err
	}

	for event := range events {
		switch event.Kind {
		case memaxagent.EventToolUse:
			fmt.Fprintf(w, "tool use: %s\n", event.ToolUse.Name)
		case memaxagent.EventApprovalRequested:
			fmt.Fprintf(w, "approval requested: %s\n", event.Approval.Action)
		case memaxagent.EventApprovalGranted:
			fmt.Fprintf(w, "approval granted: %s\n", event.Approval.Action)
		case memaxagent.EventApprovalConsumed:
			fmt.Fprintf(w, "approval consumed: %s\n", event.Approval.Action)
		case memaxagent.EventToolResult:
			fmt.Fprintf(w, "tool result: %s\n", event.ToolResult.Content)
		case memaxagent.EventResult:
			fmt.Fprintf(w, "result: %s\n", event.Result)
		case memaxagent.EventError:
			return event.Err
		}
	}
	return nil
}

type stackModel struct {
	turn int
}

func (m *stackModel) Stream(context.Context, model.Request) (model.Stream, error) {
	m.turn++
	switch m.turn {
	case 1:
		return newStream(toolUse("tool-1", commandtools.ToolName, map[string]any{
			"command": []string{"go", "test", "./..."},
			"purpose": "reproduce CI failure",
		})), nil
	case 2:
		return newStream(toolUse("tool-2", workspacetools.CheckpointToolName, map[string]any{
			"label": "before approved README repair",
		})), nil
	case 3:
		return newStream(toolUse("tool-3", workspacetools.ApplyPatchToolName, patchInput())), nil
	case 4:
		return newStream(toolUse("tool-4", approvaltools.ToolName, map[string]any{
			"action":     workspacetools.ApplyPatchToolName,
			"reason":     "repairing README.md requires approval in this workflow",
			"tool_input": patchInput(),
			"summary": map[string]any{
				"title":       "Review README.md CI repair patch",
				"description": "Fix README status so CI passes",
				"risk":        "modifies tracked documentation",
				"paths":       []string{"README.md"},
				"changes":     1,
				"modified":    1,
			},
		})), nil
	case 5:
		return newStream(toolUse("tool-5", workspacetools.ApplyPatchToolName, patchInput())), nil
	case 6:
		return newStream(toolUse("tool-6", commandtools.ToolName, map[string]any{
			"command": []string{"go", "test", "./..."},
			"purpose": "confirm CI repair",
		})), nil
	case 7:
		return newStream(toolUse("tool-7", verifytools.ToolName, map[string]any{
			"name": "test",
			"metadata": map[string]any{
				model.MetadataTaskID: "task-1",
			},
		})), nil
	default:
		return newStream(model.StreamEvent{
			Kind: model.StreamText,
			Text: "Repaired README after approval, reran the check, and verified the workspace.",
		}), nil
	}
}

func patchInput() map[string]any {
	return map[string]any{
		"operations": []map[string]any{{
			"path":        "README.md",
			"old_content": "status: broken",
			"new_content": "status: fixed",
		}},
	}
}

func toolUse(id string, name string, input map[string]any) model.StreamEvent {
	return model.StreamEvent{
		Kind: model.StreamToolUse,
		ToolUse: model.ToolUse{
			ID:    id,
			Name:  name,
			Input: mustJSON(input),
		},
	}
}

func mustJSON(v any) json.RawMessage {
	data, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return data
}

type stream struct {
	events []model.StreamEvent
	index  int
}

func newStream(events ...model.StreamEvent) *stream {
	return &stream{events: events}
}

func (s *stream) Recv() (model.StreamEvent, error) {
	if s.index >= len(s.events) {
		return model.StreamEvent{}, model.ErrEndOfStream
	}
	event := s.events[s.index]
	s.index++
	return event, nil
}

func (s *stream) Close() error {
	return nil
}
