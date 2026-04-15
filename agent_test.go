package memaxagent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/MemaxLabs/memax-go-agent-sdk/hook"
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

func TestQueryFeedsValidationErrorBackToModel(t *testing.T) {
	registry := tool.NewRegistry(tool.Definition{
		ToolSpec: model.ToolSpec{
			Name: "read",
			InputSchema: map[string]any{
				"type":     "object",
				"required": []any{"path"},
				"properties": map[string]any{
					"path": map[string]any{"type": "string"},
				},
				"additionalProperties": false,
			},
			ReadOnly: true,
		},
		Handler: func(context.Context, tool.Call) (model.ToolResult, error) {
			t.Fatal("handler should not run for invalid model input")
			return model.ToolResult{}, nil
		},
	})
	fake := &fakeModel{turns: [][]model.StreamEvent{
		{
			{
				Kind: model.StreamToolUse,
				ToolUse: model.ToolUse{
					ID:    "tool-1",
					Name:  "read",
					Input: json.RawMessage(`{"path":42}`),
				},
			},
		},
		{{Kind: model.StreamText, Text: "recovered"}},
	}}

	events, err := Query(context.Background(), "read the file", Options{
		Model: fake,
		Tools: registry,
	})
	if err != nil {
		t.Fatalf("Query returned error: %v", err)
	}
	result, err := Drain(events)
	if err != nil {
		t.Fatalf("Drain returned error: %v", err)
	}
	if result != "recovered" {
		t.Fatalf("result = %q, want %q", result, "recovered")
	}
	if got, want := len(fake.requests), 2; got != want {
		t.Fatalf("model calls = %d, want %d", got, want)
	}
	last := fake.requests[1].Messages[len(fake.requests[1].Messages)-1]
	if last.Role != model.RoleTool || last.ToolResult == nil || !last.ToolResult.IsError {
		t.Fatalf("last message before recovery = %#v, want tool error", last)
	}
}

func TestQueryFeedsHookDenialBackToModel(t *testing.T) {
	registry := tool.NewRegistry(tool.Definition{
		ToolSpec: model.ToolSpec{Name: "write"},
		Handler: func(context.Context, tool.Call) (model.ToolResult, error) {
			t.Fatal("handler should not run after hook denial")
			return model.ToolResult{}, nil
		},
	})
	hooks := hook.NewRunner(hook.WithBeforeToolUse(func(context.Context, hook.BeforeToolUseInput) (hook.BeforeToolUseResult, error) {
		return hook.BeforeToolUseResult{DenyReason: "writes are disabled"}, nil
	}))
	fake := &fakeModel{turns: [][]model.StreamEvent{
		{
			{
				Kind: model.StreamToolUse,
				ToolUse: model.ToolUse{
					ID:    "tool-1",
					Name:  "write",
					Input: json.RawMessage(`{}`),
				},
			},
		},
		{{Kind: model.StreamText, Text: "recovered"}},
	}}

	events, err := Query(context.Background(), "write the file", Options{
		Model: fake,
		Tools: registry,
		Hooks: hooks,
	})
	if err != nil {
		t.Fatalf("Query returned error: %v", err)
	}
	result, err := Drain(events)
	if err != nil {
		t.Fatalf("Drain returned error: %v", err)
	}
	if result != "recovered" {
		t.Fatalf("result = %q, want recovered", result)
	}
	last := fake.requests[1].Messages[len(fake.requests[1].Messages)-1]
	if last.ToolResult == nil || !last.ToolResult.IsError || last.ToolResult.Content != "writes are disabled" {
		t.Fatalf("last message before recovery = %#v, want hook denial tool error", last)
	}
}

type fakeModel struct {
	turns    [][]model.StreamEvent
	requests []model.Request
	calls    int
}

func (f *fakeModel) Stream(_ context.Context, req model.Request) (model.Stream, error) {
	f.requests = append(f.requests, req)
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
