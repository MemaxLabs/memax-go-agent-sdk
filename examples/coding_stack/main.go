package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
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
	ctx := context.Background()
	ws := workspace.NewMemoryStore(map[string]string{
		"README.md": "before\n",
	})
	tasks := tasktools.NewMemoryStore([]tasktools.Task{{
		ID:     "task-1",
		Title:  "Update README",
		Status: tasktools.StatusInProgress,
	}})

	config := coding.CIRepair()
	config.Workspace = ws
	config.Tasks = tasks
	config.Verifier.Verifier = verifytools.VerifierFunc(func(ctx context.Context, req verifytools.Request) (verifytools.Result, error) {
		content, err := ws.ReadFile(ctx, "README.md")
		if err != nil {
			return verifytools.Result{}, err
		}
		passed := strings.Contains(content, "after")
		result := verifytools.Result{
			Name:   req.Name,
			Passed: passed,
		}
		if !passed {
			result.Output = "README.md still needs the updated content"
		}
		return result, nil
	})
	stack, err := coding.New(config)
	if err != nil {
		log.Fatal(err)
	}

	events, err := memaxagent.Query(ctx, "Update the README through the coding stack and finish.", stack.WithModel(&stackModel{}))
	if err != nil {
		log.Fatal(err)
	}

	for event := range events {
		switch event.Kind {
		case memaxagent.EventToolUse:
			fmt.Printf("tool use: %s\n", event.ToolUse.Name)
		case memaxagent.EventToolResult:
			fmt.Printf("tool result: %s\n", event.ToolResult.Content)
		case memaxagent.EventResult:
			fmt.Printf("result: %s\n", event.Result)
		case memaxagent.EventError:
			log.Fatal(event.Err)
		}
	}
}

type stackModel struct {
	turn int
}

func (m *stackModel) Stream(context.Context, model.Request) (model.Stream, error) {
	m.turn++
	switch m.turn {
	case 1:
		return newStream(toolUse("tool-1", workspacetools.CheckpointToolName, map[string]any{
			"label": "before README edit",
		})), nil
	case 2:
		return newStream(toolUse("tool-2", workspacetools.ApplyPatchToolName, map[string]any{
			"operations": []map[string]any{{
				"path":        "README.md",
				"old_content": "before\n",
				"new_content": "after\n",
			}},
		})), nil
	case 3:
		return newStream(toolUse("tool-3", verifytools.ToolName, map[string]any{
			"name": "test",
			"metadata": map[string]any{
				model.MetadataTaskID: "task-1",
			},
		})), nil
	default:
		return newStream(model.StreamEvent{
			Kind: model.StreamText,
			Text: "Updated README.md, checkpointed the workspace first, verified the change, and completed the task.",
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
