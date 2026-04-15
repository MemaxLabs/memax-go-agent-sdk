package memaxagent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/MemaxLabs/memax-go-agent-sdk/model"
	"github.com/MemaxLabs/memax-go-agent-sdk/tool"
)

func TestQueryRunsToolAndContinuesToResult(t *testing.T) {
	registry := tool.NewRegistry(tool.Definition{
		ToolSpec: model.ToolSpec{Name: "read", ReadOnly: true, ConcurrencySafe: true},
		Handler: func(_ context.Context, call tool.Call) (model.ToolResult, error) {
			var input struct {
				Path string `json:"path"`
			}
			if err := json.Unmarshal(call.Use.Input, &input); err != nil {
				t.Fatalf("unmarshal tool input: %v", err)
			}
			return model.ToolResult{Content: "content from " + input.Path}, nil
		},
	})

	events, err := Query(context.Background(), "read the file", Options{
		Model: &fakeModel{turns: [][]model.StreamEvent{
			{
				{
					Kind: model.StreamToolUse,
					ToolUse: model.ToolUse{
						ID:    "tool-1",
						Name:  "read",
						Input: json.RawMessage(`{"path":"README.md"}`),
					},
				},
			},
			{{Kind: model.StreamText, Text: "done"}},
		}},
		Tools: registry,
	})
	if err != nil {
		t.Fatalf("Query returned error: %v", err)
	}

	result, err := Drain(events)
	if err != nil {
		t.Fatalf("Drain returned error: %v", err)
	}
	if result != "done" {
		t.Fatalf("result = %q, want %q", result, "done")
	}
}

type fakeModel struct {
	turns [][]model.StreamEvent
	calls int
}

func (f *fakeModel) Stream(context.Context, model.Request) (model.Stream, error) {
	if f.calls >= len(f.turns) {
		return &fakeStream{}, nil
	}
	stream := &fakeStream{events: f.turns[f.calls]}
	f.calls++
	return stream, nil
}

type fakeStream struct {
	events []model.StreamEvent
	index  int
}

func (s *fakeStream) Recv() (model.StreamEvent, error) {
	if s.index >= len(s.events) {
		return model.StreamEvent{}, model.ErrEndOfStream
	}
	event := s.events[s.index]
	s.index++
	return event, nil
}

func (s *fakeStream) Close() error {
	return nil
}
