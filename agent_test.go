package memaxagent

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/MemaxLabs/memax-go-agent-sdk/contextwindow"
	"github.com/MemaxLabs/memax-go-agent-sdk/hook"
	"github.com/MemaxLabs/memax-go-agent-sdk/model"
	"github.com/MemaxLabs/memax-go-agent-sdk/telemetry"
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

func TestQueryAppliesContextPolicyBeforeModelRequest(t *testing.T) {
	fake := &fakeModel{turns: [][]model.StreamEvent{
		{{Kind: model.StreamToolUse, ToolUse: model.ToolUse{ID: "tool-1", Name: "noop", Input: json.RawMessage(`{}`)}}},
		{{Kind: model.StreamToolUse, ToolUse: model.ToolUse{ID: "tool-2", Name: "noop", Input: json.RawMessage(`{}`)}}},
		{{Kind: model.StreamText, Text: "done"}},
	}}
	registry := tool.NewRegistry(tool.Definition{
		ToolSpec: model.ToolSpec{Name: "noop"},
		Handler: func(context.Context, tool.Call) (model.ToolResult, error) {
			return model.ToolResult{Content: "ok"}, nil
		},
	})

	events, err := Query(context.Background(), "start", Options{
		Model:   fake,
		Tools:   registry,
		Context: contextwindow.RecentMessages{MaxMessages: 2},
	})
	if err != nil {
		t.Fatalf("Query returned error: %v", err)
	}
	if _, err := Drain(events); err != nil {
		t.Fatalf("Drain returned error: %v", err)
	}

	lastRequest := fake.requests[len(fake.requests)-1]
	if len(lastRequest.Messages) != 2 {
		t.Fatalf("len(messages) = %d, want 2", len(lastRequest.Messages))
	}
	if lastRequest.Messages[0].Role == model.RoleTool {
		t.Fatalf("context policy left orphan tool result first: %#v", lastRequest.Messages)
	}
}

func TestQueryEmitsContextAppliedEvent(t *testing.T) {
	fake := &fakeModel{turns: [][]model.StreamEvent{
		{{Kind: model.StreamToolUse, ToolUse: model.ToolUse{ID: "tool-1", Name: "noop", Input: json.RawMessage(`{}`)}}},
		{{Kind: model.StreamText, Text: "done"}},
	}}
	registry := tool.NewRegistry(tool.Definition{
		ToolSpec: model.ToolSpec{Name: "noop"},
		Handler: func(context.Context, tool.Call) (model.ToolResult, error) {
			return model.ToolResult{Content: "ok"}, nil
		},
	})

	events, err := Query(context.Background(), "start", Options{
		Model:   fake,
		Tools:   registry,
		Context: contextwindow.RecentMessages{MaxMessages: 2},
	})
	if err != nil {
		t.Fatalf("Query returned error: %v", err)
	}

	var contextEvent *ContextEvent
	for event := range events {
		if event.Kind == EventContextApplied {
			contextEvent = event.Context
		}
		if event.Kind == EventError {
			t.Fatalf("query error: %v", event.Err)
		}
	}
	if contextEvent == nil {
		t.Fatal("missing context applied event")
	}
	if contextEvent.OriginalMessages != 3 || contextEvent.SentMessages != 2 {
		t.Fatalf("context event = %#v, want 3 -> 2", contextEvent)
	}
}

func TestQueryEmitsContextAppliedEventWhenMessageCountIsUnchanged(t *testing.T) {
	fake := &fakeModel{turns: [][]model.StreamEvent{
		{{Kind: model.StreamText, Text: "done"}},
	}}

	events, err := Query(context.Background(), "start", Options{
		Model:   fake,
		Context: replaceContextPolicy{text: "summary"},
	})
	if err != nil {
		t.Fatalf("Query returned error: %v", err)
	}

	var contextEvent *ContextEvent
	for event := range events {
		if event.Kind == EventContextApplied {
			contextEvent = event.Context
		}
		if event.Kind == EventError {
			t.Fatalf("query error: %v", event.Err)
		}
	}
	if contextEvent == nil {
		t.Fatal("missing context applied event")
	}
	if contextEvent.OriginalMessages != 1 || contextEvent.SentMessages != 1 {
		t.Fatalf("context event = %#v, want 1 -> 1", contextEvent)
	}
	if len(fake.requests) != 1 || fake.requests[0].Messages[0].PlainText() != "summary" {
		t.Fatalf("model request = %#v", fake.requests)
	}
}

func TestQueryStartsTracingSpans(t *testing.T) {
	tracer := &recordingTracer{}
	events, err := Query(context.Background(), "start", Options{
		Model: &fakeModel{turns: [][]model.StreamEvent{
			{{Kind: model.StreamText, Text: "done"}},
		}},
		Tracer: tracer,
	})
	if err != nil {
		t.Fatalf("Query returned error: %v", err)
	}
	if _, err := Drain(events); err != nil {
		t.Fatalf("Drain returned error: %v", err)
	}

	for _, name := range []string{"memaxagent.query", "memaxagent.turn", "memaxagent.model.stream"} {
		if !tracer.hasSpan(name) {
			t.Fatalf("missing span %q in %#v", name, tracer.names())
		}
	}
}

type replaceContextPolicy struct {
	text string
}

func (p replaceContextPolicy) Apply(_ context.Context, messages []model.Message) ([]model.Message, error) {
	if len(messages) == 0 {
		return nil, nil
	}
	return []model.Message{
		{
			Role: model.RoleUser,
			Content: []model.ContentBlock{
				{Type: model.ContentText, Text: p.text},
			},
		},
	}, nil
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

type recordingTracer struct {
	mu    sync.Mutex
	spans []*recordingSpan
}

func (t *recordingTracer) Start(ctx context.Context, name string, attrs ...telemetry.Attribute) (context.Context, telemetry.Span) {
	span := &recordingSpan{name: name, attrs: append([]telemetry.Attribute(nil), attrs...)}
	t.mu.Lock()
	t.spans = append(t.spans, span)
	t.mu.Unlock()
	return ctx, span
}

func (t *recordingTracer) hasSpan(name string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, span := range t.spans {
		if span.name == name {
			return true
		}
	}
	return false
}

func (t *recordingTracer) names() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	names := make([]string, 0, len(t.spans))
	for _, span := range t.spans {
		names = append(names, span.name)
	}
	return names
}

type recordingSpan struct {
	name   string
	attrs  []telemetry.Attribute
	ended  bool
	errors []error
}

func (s *recordingSpan) Set(attrs ...telemetry.Attribute) {
	s.attrs = append(s.attrs, attrs...)
}

func (s *recordingSpan) RecordError(err error) {
	if err != nil {
		s.errors = append(s.errors, err)
	}
}

func (s *recordingSpan) End() {
	s.ended = true
}
