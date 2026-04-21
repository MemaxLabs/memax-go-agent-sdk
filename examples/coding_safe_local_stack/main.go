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

// runExample is a runnable walkthrough of the safe_local preset. The
// authoritative behavior lives in the coding_preset_safe_local eval; this
// example keeps the same checkpoint -> patch -> verify -> complete shape
// visible for developers trying the stack by hand.
func runExample(ctx context.Context, w io.Writer) error {
	ws := workspace.NewMemoryStore(map[string]string{
		"README.md": "status: draft\nowner: api\n",
	})
	tasks := tasktools.NewMemoryStore([]tasktools.Task{{
		ID:     "task-1",
		Title:  "Publish README status safely",
		Status: tasktools.StatusInProgress,
		Notes:  "checkpoint before edits and verify before final answer",
	}})

	config := coding.SafeLocal()
	config.Workspace = ws
	config.Tasks = tasks
	config.Verifier.Verifier = verifytools.VerifierFunc(func(ctx context.Context, req verifytools.Request) (verifytools.Result, error) {
		content, err := ws.ReadFile(ctx, "README.md")
		if err != nil {
			return verifytools.Result{}, err
		}
		if content == "status: published\nowner: api\n" {
			return verifytools.Result{
				Name:   req.Name,
				Passed: true,
				Output: "README.md status published and owner preserved.",
			}, nil
		}
		return verifytools.Result{
			Name:   req.Name,
			Passed: false,
			Output: "README.md must be published while preserving owner api.",
			Diagnostics: []verifytools.Diagnostic{{
				Path:     "README.md",
				Severity: "error",
				Message:  "status must be published and owner must remain api",
			}},
		}, nil
	})

	stack, err := coding.New(config)
	if err != nil {
		return err
	}

	events, err := memaxagent.Query(ctx, "Publish README.md safely and finish the task.", stack.WithModel(&stackModel{}))
	if err != nil {
		return err
	}

	for event := range events {
		switch event.Kind {
		case memaxagent.EventToolUse:
			fmt.Fprintf(w, "tool use: %s\n", event.ToolUse.Name)
		case memaxagent.EventWorkspaceCheckpoint:
			fmt.Fprintf(w, "workspace checkpoint: %s\n", event.Workspace.CheckpointID)
		case memaxagent.EventWorkspacePatch:
			fmt.Fprintf(w, "workspace patch: %s\n", strings.Join(event.Workspace.Paths, ","))
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
		return newStream(toolUse("tool-1", workspacetools.CheckpointToolName, map[string]any{
			"label": "before README publish",
		})), nil
	case 2:
		return newStream(toolUse("tool-2", workspacetools.ApplyPatchToolName, map[string]any{
			"operations": []map[string]any{{
				"path":        "README.md",
				"old_content": "status: draft\nowner: api\n",
				"new_content": "status: published\nowner: api\n",
			}},
		})), nil
	case 3:
		return newStream(toolUse("tool-3", verifytools.ToolName, map[string]any{
			"name":   "test",
			"target": "README.md",
			"metadata": map[string]any{
				model.MetadataTaskID: "task-1",
			},
		})), nil
	default:
		return newStream(model.StreamEvent{
			Kind: model.StreamText,
			Text: "Safe local edit checkpointed, patched, verified, and completed.",
		}), nil
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
