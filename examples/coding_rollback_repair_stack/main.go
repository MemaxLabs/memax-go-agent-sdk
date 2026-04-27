package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"strings"

	memaxagent "github.com/MemaxLabs/memax-go-agent-sdk"
	"github.com/MemaxLabs/memax-go-agent-sdk/model"
	"github.com/MemaxLabs/memax-go-agent-sdk/stack/coding"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/commandtools"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/tasktools"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/verifytools"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/workspacetools"
	"github.com/MemaxLabs/memax-go-agent-sdk/workspace"
)

const (
	initialReadme = "status: broken\nowner: api\n"
	goodReadme    = "status: fixed\nowner: api\n"
	badReadme     = "status: fixed\nowner: ops\n"
)

func main() {
	if err := runExample(context.Background(), os.Stdout); err != nil {
		log.Fatal(err)
	}
}

// runExample is a runnable walkthrough of a competitive coding-agent recovery
// loop: observe a watcher, rely on the stack's automatic pre-patch checkpoint,
// make a risky edit, fail verification, follow rollback guidance, restore
// explicitly, repair, resume command output by cursor, and only then complete
// the task.
func runExample(ctx context.Context, w io.Writer) error {
	ws := workspace.NewMemoryStore(map[string]string{
		"README.md": initialReadme,
	})
	tasks := tasktools.NewMemoryStore([]tasktools.Task{{
		ID:     "task-1",
		Title:  "Repair README status without changing owner",
		Status: tasktools.StatusInProgress,
		Notes:  "Use watch output, rely on automatic pre-patch checkpoints, rollback on failed verification, and verify before final answer.",
	}})
	exitOK := 0
	// Later pages repeat prior chunks so after_seq filtering has to suppress
	// already-read output before the example can advance cleanly.
	sessions := commandtools.NewScriptedSessionManager(commandtools.ScriptedCommand{
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
				Chunks: []commandtools.OutputChunk{
					{
						Seq:    1,
						Stream: "stdout",
						Text:   "watch: README.md status must be fixed\nhint: owner line must stay api\n",
					},
					{
						Seq:    2,
						Stream: "stdout",
						Text:   "watch: status fixed but owner changed\n",
					},
				},
				Running: true,
			},
			{
				Chunks: []commandtools.OutputChunk{
					{
						Seq:    1,
						Stream: "stdout",
						Text:   "watch: README.md status must be fixed\nhint: owner line must stay api\n",
					},
					{
						Seq:    2,
						Stream: "stdout",
						Text:   "watch: status fixed but owner changed\n",
					},
					{
						Seq:    3,
						Stream: "stdout",
						Text:   "watch: ok\n",
					},
				},
				Running: true,
			},
		},
		StopExitCode: &exitOK,
	})

	config := coding.InteractiveDev()
	config.Workspace = ws
	config.Tasks = tasks
	config.CommandSessions = sessions
	config.Verifier.Verifier = verifytools.VerifierFunc(func(ctx context.Context, req verifytools.Request) (verifytools.Result, error) {
		content, err := ws.ReadFile(ctx, "README.md")
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

	stack, err := coding.New(config)
	if err != nil {
		return err
	}

	events, err := memaxagent.Query(ctx, "Use the watcher to repair README.md safely, rollback if verification fails, and complete the task.", stack.WithModel(&stackModel{}))
	if err != nil {
		return err
	}

	for event := range events {
		switch event.Kind {
		case memaxagent.EventToolUse:
			fmt.Fprintf(w, "tool use: %s\n", event.ToolUse.Name)
		case memaxagent.EventCommandStarted:
			fmt.Fprintf(w, "command started: %s status=%s\n", event.Command.CommandID, event.Command.Status)
		case memaxagent.EventCommandOutput:
			fmt.Fprintf(w, "command output: %s id=%s chunks=%d next_seq=%d status=%s\n", event.Command.Operation, event.Command.CommandID, event.Command.OutputChunks, event.Command.NextSeq, event.Command.Status)
		case memaxagent.EventCommandStopped:
			fmt.Fprintf(w, "command stopped: %s status=%s\n", event.Command.CommandID, event.Command.Status)
		case memaxagent.EventWorkspaceCheckpoint:
			fmt.Fprintf(w, "workspace checkpoint: %s\n", event.Workspace.CheckpointID)
		case memaxagent.EventWorkspacePatch:
			fmt.Fprintf(w, "workspace patch: %s\n", strings.Join(event.Workspace.Paths, ","))
		case memaxagent.EventWorkspaceRestore:
			fmt.Fprintf(w, "workspace restore: %s\n", event.Workspace.CheckpointID)
		case memaxagent.EventVerification:
			fmt.Fprintf(w, "verification: %s passed=%t diagnostics=%d\n", event.Verification.Name, event.Verification.Passed, event.Verification.Diagnostics)
		case memaxagent.EventToolResult:
			fmt.Fprintf(w, "tool result: %s\n", event.ToolResult.Content)
		case memaxagent.EventResult:
			fmt.Fprintf(w, "result: %s\n", event.Result)
		case memaxagent.EventError:
			return event.Err
		}
	}
	taskList, err := tasks.List(ctx)
	if err != nil {
		return err
	}
	for _, task := range taskList {
		fmt.Fprintf(w, "task: %s status=%s\n", task.ID, task.Status)
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
		return newStream(toolUse("tool-1", commandtools.StartToolName, map[string]any{
			"id":      "watch-1",
			"command": []string{"npm", "run", "test:watch"},
			"purpose": "watch README status and owner",
		})), nil
	case 2:
		return newStream(toolUse("tool-2", commandtools.ReadOutputToolName, map[string]any{
			"id": "watch-1",
		})), nil
	case 3:
		return newStream(toolUse("tool-3", workspacetools.ApplyPatchToolName, patchInput(initialReadme, badReadme))), nil
	case 4:
		return newStream(toolUse("tool-6", commandtools.ReadOutputToolName, map[string]any{
			"id":        "watch-1",
			"after_seq": 1,
		})), nil
	case 5:
		return newStream(toolUse("tool-7", verifytools.ToolName, map[string]any{
			"name":   "test",
			"target": "README.md",
			"metadata": map[string]any{
				model.MetadataTaskID: "task-1",
			},
		})), nil
	case 6:
		return newStream(toolUse("tool-8", workspacetools.RestoreToolName, map[string]any{
			"id": "checkpoint-1",
		})), nil
	case 7:
		return newStream(toolUse("tool-9", workspacetools.ApplyPatchToolName, patchInput(initialReadme, goodReadme))), nil
	case 8:
		return newStream(toolUse("tool-10", commandtools.ReadOutputToolName, map[string]any{
			"id":        "watch-1",
			"after_seq": 2,
		})), nil
	case 9:
		return newStream(toolUse("tool-11", commandtools.StopToolName, map[string]any{
			"id":    "watch-1",
			"force": true,
		})), nil
	case 10:
		return newStream(toolUse("tool-12", verifytools.ToolName, map[string]any{
			"name":   "test",
			"target": "README.md",
			"metadata": map[string]any{
				model.MetadataTaskID: "task-1",
			},
		})), nil
	default:
		return newStream(model.StreamEvent{
			Kind: model.StreamText,
			Text: "README restored, repaired, verified, and task completed.",
		}), nil
	}
}

func patchInput(oldContent, newContent string) map[string]any {
	return map[string]any{
		"operations": []map[string]any{{
			"path":        "README.md",
			"old_content": oldContent,
			"new_content": newContent,
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
