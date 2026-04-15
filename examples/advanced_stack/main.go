package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"

	memaxagent "github.com/MemaxLabs/memax-go-agent-sdk"
	"github.com/MemaxLabs/memax-go-agent-sdk/checkpoint"
	"github.com/MemaxLabs/memax-go-agent-sdk/contextwindow"
	"github.com/MemaxLabs/memax-go-agent-sdk/model"
	"github.com/MemaxLabs/memax-go-agent-sdk/session"
	"github.com/MemaxLabs/memax-go-agent-sdk/tool"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/checkpointtools"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/filetools"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/tasktools"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/toolsearch"
)

func main() {
	ctx := context.Background()
	sessions := session.NewMemoryStore()
	fs := filetools.NewMemoryFS(map[string]string{
		"docs/plan.md": "Use task state, checkpoints, and memory-backed tools together.",
	})
	tasks := tasktools.NewMemoryStore(nil)
	checkpoints := checkpoint.NewMemoryManager(nil)
	registry := tool.NewRegistry(
		filetools.NewListTool(fs),
		filetools.NewReadTool(fs),
		filetools.NewWriteTool(fs),
		tasktools.NewListTool(tasks),
		tasktools.NewUpsertTool(tasks),
		tasktools.NewDeleteTool(tasks),
		checkpointtools.NewCreateTool(checkpoints),
		checkpointtools.NewListTool(checkpoints),
		checkpointtools.NewRestoreTool(checkpoints),
		checkpointtools.NewDeleteTool(checkpoints),
	)
	searchTool, err := toolsearch.NewTool(toolsearch.Config{Registry: registry})
	if err != nil {
		log.Fatal(err)
	}
	registry.Register(searchTool)

	events, err := memaxagent.Query(ctx, "Plan the work, inspect docs, checkpoint progress, and finish.", memaxagent.Options{
		Model:        &stackModel{},
		Tools:        registry,
		Sessions:     sessions,
		Context:      contextwindow.TokenBudget{MaxTokens: 2048},
		ToolSelector: tool.SearchSelector{MaxTools: 8},
		MaxTurns:     8,
	})
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
		return newStream(toolUse("tool-1", tasktools.UpsertToolName, map[string]any{
			"title":    "Inspect documentation",
			"status":   string(tasktools.StatusInProgress),
			"priority": 1,
		})), nil
	case 2:
		return newStream(toolUse("tool-2", checkpointtools.CreateToolName, map[string]any{
			"label": "before documentation inspection",
		})), nil
	case 3:
		return newStream(toolUse("tool-3", filetools.ListToolName, map[string]any{
			"prefix": "docs",
		})), nil
	case 4:
		return newStream(toolUse("tool-4", filetools.ReadToolName, map[string]any{
			"path": "docs/plan.md",
		})), nil
	case 5:
		return newStream(toolUse("tool-5", tasktools.UpsertToolName, map[string]any{
			"id":     "task-1",
			"status": string(tasktools.StatusCompleted),
			"notes":  "docs/plan.md inspected",
		})), nil
	default:
		return newStream(model.StreamEvent{
			Kind: model.StreamText,
			Text: "Completed the plan using task state, checkpointing, and memory-backed file tools.",
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
