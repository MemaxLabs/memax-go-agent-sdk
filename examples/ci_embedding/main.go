package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"time"

	memaxagent "github.com/MemaxLabs/memax-go-agent-sdk"
	"github.com/MemaxLabs/memax-go-agent-sdk/contextwindow"
	"github.com/MemaxLabs/memax-go-agent-sdk/model"
	"github.com/MemaxLabs/memax-go-agent-sdk/permission"
	"github.com/MemaxLabs/memax-go-agent-sdk/tool"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/filetools"
)

func main() {
	ctx := context.Background()
	fs := filetools.NewMemoryFS(map[string]string{
		"changes.patch": getenv("CI_CHANGES_PATCH", "No patch was provided."),
	})
	registry := tool.NewRegistry(
		filetools.NewListTool(fs),
		filetools.NewReadTool(fs),
	)
	events, err := memaxagent.Query(ctx, "Read changes.patch and report whether CI should continue.", memaxagent.Options{
		Model:          &ciModel{},
		Tools:          registry,
		Permissions:    permission.ReadOnly{},
		Context:        contextwindow.TokenBudget{MaxTokens: 12000},
		MaxTurns:       4,
		MaxRunDuration: 30 * time.Second,
	})
	if err != nil {
		log.Fatal(err)
	}

	result, err := memaxagent.Drain(events)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(result)
}

type ciModel struct {
	turn int
}

func (m *ciModel) Stream(context.Context, model.Request) (model.Stream, error) {
	m.turn++
	switch m.turn {
	case 1:
		return newStream(model.StreamEvent{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:    "tool-1",
				Name:  filetools.ReadToolName,
				Input: mustJSON(map[string]string{"path": "changes.patch"}),
			},
		}), nil
	default:
		return newStream(model.StreamEvent{
			Kind: model.StreamText,
			Text: "CI agent completed its read-only inspection.",
		}), nil
	}
}

func getenv(name string, fallback string) string {
	value := os.Getenv(name)
	if value == "" {
		return fallback
	}
	return value
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
