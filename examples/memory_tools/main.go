package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"

	memaxagent "github.com/MemaxLabs/memax-go-agent-sdk"
	"github.com/MemaxLabs/memax-go-agent-sdk/model"
	"github.com/MemaxLabs/memax-go-agent-sdk/tool"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/filetools"
)

func main() {
	ctx := context.Background()
	fs := filetools.NewMemoryFS(map[string]string{
		"README.md": "Memax Agent SDK keeps capabilities behind tools.",
	})
	registry := tool.NewRegistry(
		filetools.NewListTool(fs),
		filetools.NewReadTool(fs),
		filetools.NewWriteTool(fs),
	)

	events, err := memaxagent.Query(ctx, "Inspect the workspace and write a summary.", memaxagent.Options{
		Model: &scriptedModel{},
		Tools: registry,
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

type scriptedModel struct {
	turn int
}

func (m *scriptedModel) Stream(context.Context, model.Request) (model.Stream, error) {
	m.turn++
	switch m.turn {
	case 1:
		return newStream(model.StreamEvent{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:    "tool-1",
				Name:  filetools.ListToolName,
				Input: mustJSON(map[string]string{}),
			},
		}), nil
	case 2:
		return newStream(model.StreamEvent{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:    "tool-2",
				Name:  filetools.ReadToolName,
				Input: mustJSON(map[string]string{"path": "README.md"}),
			},
		}), nil
	case 3:
		return newStream(model.StreamEvent{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:   "tool-3",
				Name: filetools.WriteToolName,
				Input: mustJSON(map[string]string{
					"path":    "SUMMARY.md",
					"content": "The workspace documents a tool-first agent SDK.",
				}),
			},
		}), nil
	default:
		return newStream(model.StreamEvent{
			Kind: model.StreamText,
			Text: "Created SUMMARY.md from the in-memory workspace.",
		}), nil
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
