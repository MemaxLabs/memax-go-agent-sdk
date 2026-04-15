package memaxagent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MemaxLabs/memax-go-agent-sdk/contextwindow"
	"github.com/MemaxLabs/memax-go-agent-sdk/hook"
	"github.com/MemaxLabs/memax-go-agent-sdk/identity"
	"github.com/MemaxLabs/memax-go-agent-sdk/memory"
	"github.com/MemaxLabs/memax-go-agent-sdk/model"
	"github.com/MemaxLabs/memax-go-agent-sdk/session"
	"github.com/MemaxLabs/memax-go-agent-sdk/skill"
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

func TestQueryAsyncReturnsBeforeStartupIOCompletes(t *testing.T) {
	store := &blockingCreateStore{
		inner:   session.NewMemoryStore(),
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	startedAt := time.Now()
	events := QueryAsync(context.Background(), "start", Options{
		Model:    &fakeModel{turns: [][]model.StreamEvent{{{Kind: model.StreamText, Text: "done"}}}},
		Sessions: store,
	})
	if elapsed := time.Since(startedAt); elapsed > 50*time.Millisecond {
		t.Fatalf("QueryAsync blocked caller for %s", elapsed)
	}
	<-store.started
	close(store.release)
	result, err := Drain(events)
	if err != nil {
		t.Fatalf("Drain returned error: %v", err)
	}
	if result != "done" {
		t.Fatalf("result = %q, want done", result)
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
	for event := range events {
		if event.Kind == EventError {
			t.Fatalf("query error: %v", event.Err)
		}
	}

	lastRequest := fake.requests[len(fake.requests)-1]
	if len(lastRequest.Messages) != 2 {
		t.Fatalf("len(messages) = %d, want 2", len(lastRequest.Messages))
	}
	if lastRequest.Messages[0].Role == model.RoleTool {
		t.Fatalf("context policy left orphan tool result first: %#v", lastRequest.Messages)
	}
}

func TestQueryRetriesContextWindowErrorWithRetryPolicy(t *testing.T) {
	fake := &fakeModel{
		turns: [][]model.StreamEvent{{{Kind: model.StreamText, Text: "done"}}},
	}
	retryModel := &contextRetryModel{fake: fake}
	registry := tool.NewRegistry(
		tool.Definition{ToolSpec: model.ToolSpec{Name: "read_file"}},
		tool.Definition{ToolSpec: model.ToolSpec{Name: "write_file"}},
	)
	selectCalls := 0
	events, err := Query(context.Background(), "start", Options{
		Model:        retryModel,
		Tools:        registry,
		ContextRetry: replaceContextPolicy{text: "compact"},
		ToolSelector: tool.SelectorFunc(func(_ context.Context, _ *tool.Registry, req tool.SelectRequest) ([]model.ToolSpec, error) {
			selectCalls++
			if len(req.Messages) > 0 && strings.Contains(req.Messages[0].PlainText(), "compact") {
				return []model.ToolSpec{{Name: "read_file"}}, nil
			}
			return []model.ToolSpec{{Name: "write_file"}}, nil
		}),
	})
	if err != nil {
		t.Fatalf("Query returned error: %v", err)
	}
	var contextEvent *ContextEvent
	result, err := drainWithContextEvent(events, &contextEvent)
	if err != nil {
		t.Fatalf("Drain returned error: %v", err)
	}
	if result != "done" {
		t.Fatalf("result = %q, want done", result)
	}
	if len(retryModel.requests) != 2 {
		t.Fatalf("model requests = %d, want retry", len(retryModel.requests))
	}
	if contextEvent == nil || contextEvent.OriginalMessages != 1 || contextEvent.SentMessages != 1 {
		t.Fatalf("context event = %#v, want retry context event", contextEvent)
	}
	if retryModel.requests[1].Messages[0].PlainText() != "compact" {
		t.Fatalf("retry messages = %#v, want compacted prompt", retryModel.requests[1].Messages)
	}
	if selectCalls != 2 {
		t.Fatalf("selector calls = %d, want original and retry selection", selectCalls)
	}
	if got := requestToolNames(retryModel.requests[0]); !sameStrings(got, []string{"write_file"}) {
		t.Fatalf("original retry tools = %#v, want write_file", got)
	}
	if got := requestToolNames(retryModel.requests[1]); !sameStrings(got, []string{"read_file"}) {
		t.Fatalf("compacted retry tools = %#v, want read_file", got)
	}
}

func drainWithContextEvent(events <-chan Event, contextEvent **ContextEvent) (string, error) {
	for event := range events {
		switch event.Kind {
		case EventContextApplied:
			*contextEvent = event.Context
		case EventResult:
			return event.Result, nil
		case EventError:
			return "", event.Err
		}
	}
	return "", nil
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

func TestQueryRecordsMetrics(t *testing.T) {
	meter := &recordingMeter{}
	events, err := Query(context.Background(), "start", Options{
		Model: &fakeModel{turns: [][]model.StreamEvent{
			{{Kind: model.StreamText, Text: "done"}},
		}},
		Meter: meter,
	})
	if err != nil {
		t.Fatalf("Query returned error: %v", err)
	}
	for event := range events {
		if event.Kind == EventError {
			t.Fatalf("query error: %v", event.Err)
		}
	}

	for _, name := range []string{"memax.query.started", "memax.turn.started", "memax.model.stream.started", "memax.query.completed"} {
		if !meter.hasCounter(name) {
			t.Fatalf("missing counter %q in %#v", name, meter.counterNames())
		}
	}
	if !meter.hasRecord("memax.model.stream.duration_ms") || !meter.hasRecord("memax.turn.duration_ms") {
		t.Fatalf("missing duration records in %#v", meter.recordNames())
	}
}

func TestQueryAppliesToolSelectorBeforeModelRequest(t *testing.T) {
	fake := &fakeModel{turns: [][]model.StreamEvent{{{Kind: model.StreamText, Text: "done"}}}}
	registry := tool.NewRegistry(
		tool.Definition{ToolSpec: model.ToolSpec{Name: "search_tools", AlwaysLoad: true}},
		tool.Definition{ToolSpec: model.ToolSpec{Name: "read_file", SearchHint: "read workspace file", ShouldDefer: true}},
		tool.Definition{ToolSpec: model.ToolSpec{Name: "write_file", SearchHint: "write workspace file", ShouldDefer: true}},
	)

	events, err := Query(context.Background(), "read the workspace", Options{
		Model:        fake,
		Tools:        registry,
		ToolSelector: tool.SearchSelector{},
	})
	if err != nil {
		t.Fatalf("Query returned error: %v", err)
	}
	if _, err := Drain(events); err != nil {
		t.Fatalf("Drain returned error: %v", err)
	}
	if len(fake.requests) != 1 {
		t.Fatalf("model requests = %d, want 1", len(fake.requests))
	}
	got := requestToolNames(fake.requests[0])
	want := []string{"search_tools", "read_file"}
	if !sameStrings(got, want) {
		t.Fatalf("tools = %#v, want %#v", got, want)
	}
}

func TestQueryBuildsPromptFromIdentityAndSkills(t *testing.T) {
	fake := &fakeModel{turns: [][]model.StreamEvent{{{Kind: model.StreamText, Text: "done"}}}}
	events, err := Query(context.Background(), "review the SQL migration", Options{
		Model: fake,
		Identity: identity.Identity{
			Name:    "reviewer",
			Mission: "find correctness risks",
		},
		Skills: []skill.Skill{{
			Name:        "database-review",
			Description: "SQL migration review",
			Content:     "Check locking and rollback behavior.",
		}},
	})
	if err != nil {
		t.Fatalf("Query returned error: %v", err)
	}
	if _, err := Drain(events); err != nil {
		t.Fatalf("Drain returned error: %v", err)
	}
	if len(fake.requests) != 1 {
		t.Fatalf("model requests = %d, want 1", len(fake.requests))
	}
	system := fake.requests[0].SystemPrompt
	for _, want := range []string{"reviewer", "find correctness risks", "database-review", "Check locking"} {
		if !strings.Contains(system, want) {
			t.Fatalf("system prompt missing %q:\n%s", want, system)
		}
	}
	if fake.requests[0].AppendSystemPrompt != "" {
		t.Fatalf("AppendSystemPrompt = %q, want empty after prompt assembly", fake.requests[0].AppendSystemPrompt)
	}
}

func TestQueryLoadsSkillsFromSource(t *testing.T) {
	fake := &fakeModel{turns: [][]model.StreamEvent{{{Kind: model.StreamText, Text: "done"}}}}
	events, err := Query(context.Background(), "inspect auth code", Options{
		Model: fake,
		SkillSource: skill.StaticSource{{
			Name:        "security-review",
			Description: "auth and access control review",
			Content:     "Check authorization boundaries.",
		}},
	})
	if err != nil {
		t.Fatalf("Query returned error: %v", err)
	}
	if _, err := Drain(events); err != nil {
		t.Fatalf("Drain returned error: %v", err)
	}
	if len(fake.requests) != 1 || !strings.Contains(fake.requests[0].SystemPrompt, "security-review") {
		t.Fatalf("system prompt = %q, want loaded skill", fake.requests[0].SystemPrompt)
	}
}

func TestQueryLoadsMemoriesFromSource(t *testing.T) {
	fake := &fakeModel{turns: [][]model.StreamEvent{{{Kind: model.StreamText, Text: "done"}}}}
	var got memory.Request
	events, err := Query(context.Background(), "inspect billing code", Options{
		Model:           fake,
		ParentSessionID: "parent-session",
		MemorySource: memory.SourceFunc(func(_ context.Context, req memory.Request) ([]memory.Memory, error) {
			got = req
			return []memory.Memory{{
				Name:    "billing-rules",
				Scope:   memory.ScopeProject,
				Content: "Billing changes require audit logging.",
			}}, nil
		}),
	})
	if err != nil {
		t.Fatalf("Query returned error: %v", err)
	}
	if _, err := Drain(events); err != nil {
		t.Fatalf("Drain returned error: %v", err)
	}
	if got.SessionID == "" {
		t.Fatal("memory source did not receive active session id")
	}
	if got.ParentSessionID != "parent-session" {
		t.Fatalf("memory source parent session = %q, want parent-session", got.ParentSessionID)
	}
	if len(got.Messages) != 1 || got.Messages[0].PlainText() != "inspect billing code" {
		t.Fatalf("memory source messages = %#v, want current messages", got.Messages)
	}
	if got.Query != "inspect billing code" {
		t.Fatalf("memory source query = %q, want prompt text", got.Query)
	}
	if len(fake.requests) != 1 || !strings.Contains(fake.requests[0].SystemPrompt, "Billing changes require audit logging.") {
		t.Fatalf("system prompt = %q, want loaded memory", fake.requests[0].SystemPrompt)
	}
}

func TestQueryLoadsMemorySourceOncePerRun(t *testing.T) {
	registry := tool.NewRegistry(tool.Definition{
		ToolSpec: model.ToolSpec{Name: "lookup"},
		Handler: func(context.Context, tool.Call) (model.ToolResult, error) {
			return model.ToolResult{Content: "tool result mentions frontend but should not reload memories"}, nil
		},
	})
	fake := &fakeModel{turns: [][]model.StreamEvent{
		{{Kind: model.StreamToolUse, ToolUse: model.ToolUse{ID: "tool-1", Name: "lookup", Input: json.RawMessage(`{}`)}}},
		{{Kind: model.StreamText, Text: "done"}},
	}}
	var calls int
	var got memory.Request
	events, err := Query(context.Background(), "inspect billing code", Options{
		Model: fake,
		Tools: registry,
		MemorySource: memory.SourceFunc(func(_ context.Context, req memory.Request) ([]memory.Memory, error) {
			calls++
			got = req
			return []memory.Memory{{Name: "billing", Scope: memory.ScopeProject, Content: "Billing changes require audit logging."}}, nil
		}),
	})
	if err != nil {
		t.Fatalf("Query returned error: %v", err)
	}
	if _, err := Drain(events); err != nil {
		t.Fatalf("Drain returned error: %v", err)
	}
	if calls != 1 {
		t.Fatalf("memory source calls = %d, want 1", calls)
	}
	if got.Query != "inspect billing code" {
		t.Fatalf("memory source query = %q, want user prompt only", got.Query)
	}
}

func TestMemoryQueryUsesRecentUserMessagesOnly(t *testing.T) {
	messages := []model.Message{
		{Role: model.RoleUser, Content: []model.ContentBlock{{Type: model.ContentText, Text: "old billing context"}}},
		{Role: model.RoleTool, ToolResult: &model.ToolResult{Name: "lookup", Content: "frontend noise"}},
		{Role: model.RoleAssistant, Content: []model.ContentBlock{{Type: model.ContentText, Text: "assistant noise"}}},
		{Role: model.RoleUser, Content: []model.ContentBlock{{Type: model.ContentText, Text: "first recent"}}},
		{Role: model.RoleUser, Content: []model.ContentBlock{{Type: model.ContentText, Text: "second recent"}}},
		{Role: model.RoleUser, Content: []model.ContentBlock{{Type: model.ContentText, Text: "third recent"}}},
	}
	got := memoryQuery(messages)
	if got != "first recent second recent third recent" {
		t.Fatalf("memoryQuery = %q", got)
	}
	for _, blocked := range []string{"old billing", "frontend", "assistant"} {
		if strings.Contains(got, blocked) {
			t.Fatalf("memoryQuery = %q, want no %q", got, blocked)
		}
	}
}

func TestQueryMemorySourceErrorStopsRun(t *testing.T) {
	sourceErr := errors.New("memory store unavailable")
	events, err := Query(context.Background(), "start", Options{
		Model: &fakeModel{},
		MemorySource: memory.SourceFunc(func(context.Context, memory.Request) ([]memory.Memory, error) {
			return nil, sourceErr
		}),
	})
	if err != nil {
		t.Fatalf("Query returned error: %v", err)
	}
	if _, err := Drain(events); err == nil || !strings.Contains(err.Error(), "build prompt") || !errors.Is(err, sourceErr) {
		t.Fatalf("Drain error = %v, want memory source error", err)
	}
}

func TestQueryToolSelectorErrorStopsRun(t *testing.T) {
	selectorErr := errors.New("selector unavailable")
	events, err := Query(context.Background(), "start", Options{
		Model: &fakeModel{},
		ToolSelector: tool.SelectorFunc(func(context.Context, *tool.Registry, tool.SelectRequest) ([]model.ToolSpec, error) {
			return nil, selectorErr
		}),
	})
	if err != nil {
		t.Fatalf("Query returned error: %v", err)
	}
	if _, err := Drain(events); err == nil || !strings.Contains(err.Error(), "select tools") {
		t.Fatalf("Drain error = %v, want select tools error", err)
	}
}

func TestQueryRunsLifecycleHooks(t *testing.T) {
	var calls []string
	hooks := hook.NewRunner(
		hook.WithSessionStarted(func(_ context.Context, input hook.SessionStartedInput) error {
			if input.SessionID == "" {
				t.Fatal("missing session id")
			}
			calls = append(calls, "session_started")
			return nil
		}),
		hook.WithUserPrompt(func(_ context.Context, input hook.UserPromptInput) (hook.UserPromptResult, error) {
			calls = append(calls, "user_prompt")
			return hook.UserPromptResult{Prompt: input.Prompt + " rewritten"}, nil
		}),
		hook.WithStop(func(_ context.Context, input hook.StopInput) error {
			if input.Reason != hook.StopReasonResult {
				t.Fatalf("stop reason = %q, want result", input.Reason)
			}
			calls = append(calls, "stop")
			return nil
		}),
		hook.WithSessionEnded(func(_ context.Context, input hook.SessionEndedInput) error {
			if input.Reason != hook.StopReasonResult {
				t.Fatalf("session ended reason = %q, want result", input.Reason)
			}
			calls = append(calls, "session_ended")
			return nil
		}),
	)
	fake := &fakeModel{turns: [][]model.StreamEvent{{{Kind: model.StreamText, Text: "done"}}}}

	events, err := Query(context.Background(), "start", Options{
		Model: fake,
		Hooks: hooks,
	})
	if err != nil {
		t.Fatalf("Query returned error: %v", err)
	}
	if _, err := Drain(events); err != nil {
		t.Fatalf("Drain returned error: %v", err)
	}
	if len(fake.requests) != 1 || fake.requests[0].Messages[0].PlainText() != "start rewritten" {
		t.Fatalf("model request = %#v", fake.requests)
	}
	want := []string{"session_started", "user_prompt", "stop", "session_ended"}
	if !sameStrings(calls, want) {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}
}

func TestQueryPropagatesParentSessionID(t *testing.T) {
	fake := &fakeModel{turns: [][]model.StreamEvent{{{Kind: model.StreamText, Text: "done"}}}}
	events, err := Query(context.Background(), "start", Options{
		Model:           fake,
		ParentSessionID: "parent-session",
	})
	if err != nil {
		t.Fatalf("Query returned error: %v", err)
	}
	var started Event
	for event := range events {
		if event.Kind == EventSessionStarted {
			started = event
		}
		if event.Kind == EventError {
			t.Fatalf("query error: %v", event.Err)
		}
		if event.Kind == EventResult {
			break
		}
	}
	if started.ParentSessionID != "parent-session" {
		t.Fatalf("started event = %#v, want parent session id", started)
	}
	if len(fake.requests) != 1 || fake.requests[0].ParentSessionID != "parent-session" {
		t.Fatalf("model request = %#v, want parent session id", fake.requests)
	}
}

func TestQueryResumesExistingSession(t *testing.T) {
	store := session.NewMemoryStore()
	sess, err := store.Create(context.Background())
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}
	if err := store.Append(context.Background(), sess.ID, model.Message{
		Role:    model.RoleUser,
		Content: []model.ContentBlock{{Type: model.ContentText, Text: "previous"}},
	}); err != nil {
		t.Fatalf("Append returned error: %v", err)
	}
	fake := &fakeModel{turns: [][]model.StreamEvent{{{Kind: model.StreamText, Text: "done"}}}}

	events, err := Query(context.Background(), "next", Options{
		Model:     fake,
		Sessions:  store,
		SessionID: sess.ID,
	})
	if err != nil {
		t.Fatalf("Query returned error: %v", err)
	}
	var started Event
	for event := range events {
		if event.Kind == EventSessionStarted {
			started = event
		}
		if event.Kind == EventError {
			t.Fatalf("query error: %v", event.Err)
		}
		if event.Kind == EventResult {
			break
		}
	}
	if started.SessionID != sess.ID {
		t.Fatalf("started event session = %q, want resumed session %q", started.SessionID, sess.ID)
	}
	if len(fake.requests) != 1 || len(fake.requests[0].Messages) != 2 {
		t.Fatalf("model request = %#v, want previous + next messages", fake.requests)
	}
	if fake.requests[0].Messages[0].PlainText() != "previous" || fake.requests[0].Messages[1].PlainText() != "next" {
		t.Fatalf("model messages = %#v, want resumed transcript", fake.requests[0].Messages)
	}
}

func TestQueryRunsContextAppliedHook(t *testing.T) {
	var got hook.ContextAppliedInput
	hooks := hook.NewRunner(hook.WithContextApplied(func(_ context.Context, input hook.ContextAppliedInput) error {
		got = input
		return nil
	}))
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
		Hooks:   hooks,
		Context: contextwindow.RecentMessages{MaxMessages: 2},
	})
	if err != nil {
		t.Fatalf("Query returned error: %v", err)
	}
	if _, err := Drain(events); err != nil {
		t.Fatalf("Drain returned error: %v", err)
	}
	if got.OriginalMessages != 3 || got.SentMessages != 2 {
		t.Fatalf("context hook input = %#v, want 3 -> 2", got)
	}
}

func TestQueryUserPromptHookCanDeny(t *testing.T) {
	_, err := Query(context.Background(), "start", Options{
		Model: &fakeModel{},
		Hooks: hook.NewRunner(hook.WithUserPrompt(func(context.Context, hook.UserPromptInput) (hook.UserPromptResult, error) {
			return hook.UserPromptResult{DenyReason: "blocked prompt"}, nil
		})),
	})
	if err == nil || err.Error() != "blocked prompt" {
		t.Fatalf("Query error = %v, want blocked prompt", err)
	}
}

func TestQueryStopHookErrorSurfacesBeforeResult(t *testing.T) {
	errStop := errors.New("stop sink unavailable")
	events, err := Query(context.Background(), "start", Options{
		Model: &fakeModel{turns: [][]model.StreamEvent{{{Kind: model.StreamText, Text: "done"}}}},
		Hooks: hook.NewRunner(hook.WithStop(func(context.Context, hook.StopInput) error {
			return errStop
		})),
	})
	if err != nil {
		t.Fatalf("Query returned error: %v", err)
	}
	if _, err := Drain(events); err == nil || !strings.Contains(err.Error(), "stop hook failed") {
		t.Fatalf("Drain error = %v, want stop hook failure", err)
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

type blockingCreateStore struct {
	inner   *session.MemoryStore
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (s *blockingCreateStore) Create(ctx context.Context) (session.Session, error) {
	s.once.Do(func() { close(s.started) })
	select {
	case <-s.release:
	case <-ctx.Done():
		return session.Session{}, ctx.Err()
	}
	return s.inner.Create(ctx)
}

func (s *blockingCreateStore) Append(ctx context.Context, id string, msg model.Message) error {
	return s.inner.Append(ctx, id, msg)
}

func (s *blockingCreateStore) Messages(ctx context.Context, id string) ([]model.Message, error) {
	return s.inner.Messages(ctx, id)
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

type contextRetryModel struct {
	fake     *fakeModel
	requests []model.Request
	calls    int
}

func (m *contextRetryModel) Stream(ctx context.Context, req model.Request) (model.Stream, error) {
	m.requests = append(m.requests, req)
	if m.calls == 0 {
		m.calls++
		return nil, model.ErrContextWindowExceeded
	}
	m.calls++
	return m.fake.Stream(ctx, req)
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

type recordingMeter struct {
	mu       sync.Mutex
	counters []string
	records  []string
}

func (m *recordingMeter) Add(_ context.Context, name string, _ int64, _ ...telemetry.Attribute) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.counters = append(m.counters, name)
}

func (m *recordingMeter) Record(_ context.Context, name string, _ float64, _ ...telemetry.Attribute) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.records = append(m.records, name)
}

func (m *recordingMeter) hasCounter(name string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, existing := range m.counters {
		if existing == name {
			return true
		}
	}
	return false
}

func (m *recordingMeter) hasRecord(name string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, existing := range m.records {
		if existing == name {
			return true
		}
	}
	return false
}

func (m *recordingMeter) counterNames() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.counters...)
}

func (m *recordingMeter) recordNames() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.records...)
}

func sameStrings(a []string, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func requestToolNames(req model.Request) []string {
	out := make([]string, 0, len(req.Tools))
	for _, spec := range req.Tools {
		out = append(out, spec.Name)
	}
	return out
}
