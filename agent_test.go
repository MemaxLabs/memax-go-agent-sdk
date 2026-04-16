package memaxagent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MemaxLabs/memax-go-agent-sdk/budget"
	"github.com/MemaxLabs/memax-go-agent-sdk/contextwindow"
	"github.com/MemaxLabs/memax-go-agent-sdk/hook"
	"github.com/MemaxLabs/memax-go-agent-sdk/identity"
	"github.com/MemaxLabs/memax-go-agent-sdk/memory"
	"github.com/MemaxLabs/memax-go-agent-sdk/model"
	"github.com/MemaxLabs/memax-go-agent-sdk/output"
	"github.com/MemaxLabs/memax-go-agent-sdk/planner"
	"github.com/MemaxLabs/memax-go-agent-sdk/resultstore"
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

func TestQueryStartsSafeToolBeforeAssistantStreamEnds(t *testing.T) {
	started := make(chan struct{})
	registry := tool.NewRegistry(tool.Definition{
		ToolSpec: model.ToolSpec{Name: "lookup", ReadOnly: true, ConcurrencySafe: true},
		Handler: func(context.Context, tool.Call) (model.ToolResult, error) {
			close(started)
			return model.ToolResult{Content: "lookup result"}, nil
		},
	})
	store := session.NewMemoryStore()
	events, err := Query(context.Background(), "lookup before finishing text", Options{
		Model: &earlyToolModel{
			toolStarted: started,
			first: []model.StreamEvent{
				{
					Kind: model.StreamToolUseStart,
					ToolUse: model.ToolUse{
						ID:   "tool-1",
						Name: "lookup",
					},
				},
				{
					Kind: model.StreamToolUseDelta,
					ToolUse: model.ToolUse{
						ID:   "tool-1",
						Name: "lookup",
					},
					ToolUseDelta: `{}`,
				},
				{
					Kind: model.StreamToolUse,
					ToolUse: model.ToolUse{
						ID:    "tool-1",
						Name:  "lookup",
						Input: json.RawMessage(`{}`),
					},
				},
				{Kind: model.StreamText, Text: " while continuing"},
			},
			second: []model.StreamEvent{{Kind: model.StreamText, Text: "done"}},
		},
		Tools:    registry,
		Sessions: store,
	})
	if err != nil {
		t.Fatalf("Query returned error: %v", err)
	}
	result, err := Drain(events)
	if err != nil {
		t.Fatalf("Drain returned error: %v", err)
	}
	if result != "done" {
		t.Fatalf("result = %q, want done", result)
	}
	sessions, err := store.List(context.Background())
	if err != nil {
		t.Fatalf("List returned error: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("sessions = %d, want 1", len(sessions))
	}
	messages, err := store.Messages(context.Background(), sessions[0].ID)
	if err != nil {
		t.Fatalf("Messages returned error: %v", err)
	}
	if len(messages) < 3 {
		t.Fatalf("messages = %#v, want user, assistant, tool result", messages)
	}
	if messages[1].Role != model.RoleAssistant || len(messages[1].Content) != 2 {
		t.Fatalf("assistant message = %#v, want tool use plus trailing text", messages[1])
	}
	if messages[2].Role != model.RoleTool || messages[2].ToolResult == nil || messages[2].ToolResult.Content != "lookup result" {
		t.Fatalf("tool result message = %#v, want persisted lookup result", messages[2])
	}
}

func TestQueryCancelsEarlyToolAndEmitsToolResultWhenStreamFails(t *testing.T) {
	started := make(chan struct{})
	cancelled := make(chan struct{})
	release := make(chan struct{})
	registry := tool.NewRegistry(tool.Definition{
		ToolSpec: model.ToolSpec{Name: "lookup", ReadOnly: true, ConcurrencySafe: true},
		Handler: func(ctx context.Context, _ tool.Call) (model.ToolResult, error) {
			close(started)
			<-ctx.Done()
			close(cancelled)
			<-release
			return model.ToolResult{Content: "observed cancel", IsError: true}, nil
		},
	})
	events, err := Query(context.Background(), "lookup before stream failure", Options{
		Model: &streamErrorModel{
			started: started,
			events: []model.StreamEvent{
				{
					Kind: model.StreamToolUse,
					ToolUse: model.ToolUse{
						ID:    "tool-1",
						Name:  "lookup",
						Input: json.RawMessage(`{}`),
					},
				},
			},
			err: errors.New("stream exploded"),
		},
		Tools: registry,
	})
	if err != nil {
		t.Fatalf("Query returned error: %v", err)
	}

	var toolResult *model.ToolResult
	var gotErr error
	for event := range events {
		switch event.Kind {
		case EventToolResult:
			result := *event.ToolResult
			toolResult = &result
		case EventError:
			gotErr = event.Err
		}
	}
	close(release)
	if gotErr == nil || !strings.Contains(gotErr.Error(), "stream exploded") {
		t.Fatalf("error = %v, want stream failure", gotErr)
	}
	if toolResult == nil {
		t.Fatal("missing cancellation tool result")
	}
	if !toolResult.IsError || toolResult.ToolUseID != "tool-1" || toolResult.Name != "lookup" {
		t.Fatalf("tool result = %#v, want cancellation error for lookup", toolResult)
	}
	if !strings.Contains(toolResult.Content, "model streaming stopped") {
		t.Fatalf("tool result content = %q, want streaming cancellation reason", toolResult.Content)
	}
	select {
	case <-cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("early tool did not observe cancellation")
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

func TestQueryProgressiveSkillDisclosureLoadsSkillThroughTool(t *testing.T) {
	fake := &fakeModel{turns: [][]model.StreamEvent{
		{
			{
				Kind: model.StreamToolUse,
				ToolUse: model.ToolUse{
					ID:    "skill-1",
					Name:  skill.LoadToolName,
					Input: json.RawMessage(`{"name":"database-review"}`),
				},
			},
		},
		{{Kind: model.StreamText, Text: "reviewed with skill"}},
	}}
	sourceCalls := 0
	source := skill.SourceFunc(func(context.Context) ([]skill.Skill, error) {
		sourceCalls++
		return []skill.Skill{{
			Name:        "database-review",
			Description: "Review database migrations.",
			WhenToUse:   "SQL changes are involved.",
			AlwaysOn:    true,
			Content:     "Check lock behavior and rollback safety.",
		}}, nil
	})

	events, err := Query(context.Background(), "review SQL migration", Options{
		Model:           fake,
		SkillSource:     source,
		SkillDisclosure: skill.DisclosureProgressive,
	})
	if err != nil {
		t.Fatalf("Query returned error: %v", err)
	}
	result, err := Drain(events)
	if err != nil {
		t.Fatalf("Drain returned error: %v", err)
	}
	if result != "reviewed with skill" {
		t.Fatalf("result = %q, want reviewed with skill", result)
	}
	if sourceCalls != 1 {
		t.Fatalf("source calls = %d, want one per run", sourceCalls)
	}
	if got := len(fake.requests); got != 2 {
		t.Fatalf("model calls = %d, want 2", got)
	}
	first := fake.requests[0]
	if !requestHasTool(first, skill.LoadToolName) {
		t.Fatalf("first request tools = %#v, want %s", first.Tools, skill.LoadToolName)
	}
	if strings.Contains(first.SystemPrompt, "Check lock behavior") {
		t.Fatalf("first prompt leaked full skill content:\n%s", first.SystemPrompt)
	}
	if !strings.Contains(first.SystemPrompt, "database-review") || !strings.Contains(first.SystemPrompt, "load_skill") {
		t.Fatalf("first prompt missing progressive skill metadata:\n%s", first.SystemPrompt)
	}
	second := fake.requests[1]
	if len(second.Messages) == 0 {
		t.Fatal("second request has no messages")
	}
	last := second.Messages[len(second.Messages)-1]
	if last.Role != model.RoleTool || last.ToolResult == nil || last.ToolResult.Name != skill.LoadToolName {
		t.Fatalf("last message = %#v, want load_skill tool result", last)
	}
	if !strings.Contains(last.ToolResult.Content, "Check lock behavior and rollback safety.") {
		t.Fatalf("load_skill result = %q, want full instructions", last.ToolResult.Content)
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

func TestQueryValidatesStructuredOutput(t *testing.T) {
	fake := &fakeModel{turns: [][]model.StreamEvent{{{Kind: model.StreamText, Text: `{"answer":"done"}`}}}}
	events, err := Query(context.Background(), "start", Options{
		Model:  fake,
		Output: answerOutputContract(),
	})
	if err != nil {
		t.Fatalf("Query returned error: %v", err)
	}
	result, err := Drain(events)
	if err != nil {
		t.Fatalf("Drain returned error: %v", err)
	}
	if result != `{"answer":"done"}` {
		t.Fatalf("result = %q, want structured JSON", result)
	}
	if len(fake.requests) != 1 {
		t.Fatalf("model requests = %d, want 1", len(fake.requests))
	}
	if !strings.Contains(fake.requests[0].SystemPrompt, "Final answer contract") {
		t.Fatalf("system prompt = %q, want output contract guidance", fake.requests[0].SystemPrompt)
	}
}

func TestQueryEmitsUsageAndAggregatesOnResult(t *testing.T) {
	meter := &recordingMeter{}
	fake := &fakeModel{turns: [][]model.StreamEvent{{{
		Kind: model.StreamText,
		Text: "done",
	}, {
		Kind: model.StreamUsage,
		Usage: &model.Usage{
			Provider:     "test",
			Model:        "fake",
			InputTokens:  3,
			OutputTokens: 5,
			TotalTokens:  8,
		},
	}}}}
	events, err := Query(context.Background(), "start", Options{
		Model: fake,
		Meter: meter,
	})
	if err != nil {
		t.Fatalf("Query returned error: %v", err)
	}
	var usageEvent *model.Usage
	var resultEvent *model.Usage
	for event := range events {
		if event.Kind == EventError {
			t.Fatalf("query error: %v", event.Err)
		}
		switch event.Kind {
		case EventUsage:
			usageEvent = event.Usage
		case EventResult:
			resultEvent = event.Usage
		}
	}
	if usageEvent == nil || usageEvent.InputTokens != 3 || usageEvent.OutputTokens != 5 || usageEvent.TotalTokens != 8 {
		t.Fatalf("usage event = %#v, want token counts", usageEvent)
	}
	if resultEvent == nil || resultEvent.InputTokens != 3 || resultEvent.OutputTokens != 5 || resultEvent.TotalTokens != 8 {
		t.Fatalf("result usage = %#v, want aggregate token counts", resultEvent)
	}
	for _, want := range []string{"memax.model.input_tokens", "memax.model.output_tokens", "memax.model.total_tokens"} {
		if !meter.hasCounter(want) {
			t.Fatalf("meter counters = %#v, missing %s", meter.counterNames(), want)
		}
	}
}

func TestQueryBudgetStopsBeforeSecondModelCall(t *testing.T) {
	registry := tool.NewRegistry(tool.Definition{
		ToolSpec: model.ToolSpec{Name: "lookup", ReadOnly: true},
		Handler: func(context.Context, tool.Call) (model.ToolResult, error) {
			return model.ToolResult{Content: "tool result"}, nil
		},
	})
	stopReasons := make(chan hook.StopReason, 1)
	hooks := hook.NewRunner(hook.WithStop(func(_ context.Context, input hook.StopInput) error {
		stopReasons <- input.Reason
		return nil
	}))
	fake := &fakeModel{turns: [][]model.StreamEvent{
		{{Kind: model.StreamToolUse, ToolUse: model.ToolUse{ID: "tool-1", Name: "lookup", Input: json.RawMessage(`{}`)}}},
		{{Kind: model.StreamText, Text: "should not run"}},
	}}

	events, err := Query(context.Background(), "start", Options{
		Model:  fake,
		Tools:  registry,
		Hooks:  hooks,
		Budget: budget.Policy{MaxModelCalls: 1},
	})
	if err != nil {
		t.Fatalf("Query returned error: %v", err)
	}
	var budgetErr error
	for event := range events {
		if event.Kind == EventError {
			budgetErr = event.Err
		}
	}
	if budgetErr == nil || !strings.Contains(budgetErr.Error(), "max model calls") {
		t.Fatalf("budget error = %v, want max model calls budget error", budgetErr)
	}
	if len(fake.requests) != 1 {
		t.Fatalf("model requests = %d, want 1", len(fake.requests))
	}
	select {
	case stopReason := <-stopReasons:
		if stopReason != hook.StopReasonBudget {
			t.Fatalf("stop reason = %q, want budget", stopReason)
		}
	default:
		t.Fatal("missing stop hook call")
	}
}

func TestQueryBudgetStopsBeforeToolBatch(t *testing.T) {
	runCount := 0
	registry := tool.NewRegistry(tool.Definition{
		ToolSpec: model.ToolSpec{Name: "lookup", ReadOnly: true},
		Handler: func(context.Context, tool.Call) (model.ToolResult, error) {
			runCount++
			return model.ToolResult{Content: "tool result"}, nil
		},
	})
	fake := &fakeModel{turns: [][]model.StreamEvent{{{
		Kind:    model.StreamToolUse,
		ToolUse: model.ToolUse{ID: "tool-1", Name: "lookup", Input: json.RawMessage(`{}`)},
	}, {
		Kind:    model.StreamToolUse,
		ToolUse: model.ToolUse{ID: "tool-2", Name: "lookup", Input: json.RawMessage(`{}`)},
	}}}}

	events, err := Query(context.Background(), "start", Options{
		Model:  fake,
		Tools:  registry,
		Budget: budget.Policy{MaxToolCalls: 1},
	})
	if err != nil {
		t.Fatalf("Query returned error: %v", err)
	}
	if _, err := Drain(events); err == nil || !strings.Contains(err.Error(), "max tool calls") {
		t.Fatalf("Drain error = %v, want max tool calls budget error", err)
	}
	if runCount != 0 {
		t.Fatalf("tool handler ran %d times, want 0", runCount)
	}
}

func TestQueryBudgetStopsAfterTokenUsage(t *testing.T) {
	fake := &fakeModel{turns: [][]model.StreamEvent{{{
		Kind: model.StreamText,
		Text: "done",
	}, {
		Kind:  model.StreamUsage,
		Usage: &model.Usage{InputTokens: 6, OutputTokens: 5, TotalTokens: 11},
	}}}}

	events, err := Query(context.Background(), "start", Options{
		Model:  fake,
		Budget: budget.Policy{MaxTotalTokens: 10},
	})
	if err != nil {
		t.Fatalf("Query returned error: %v", err)
	}
	if _, err := Drain(events); err == nil || !strings.Contains(err.Error(), "max total tokens") {
		t.Fatalf("Drain error = %v, want max total tokens budget error", err)
	}
	if len(fake.requests) != 1 {
		t.Fatalf("model requests = %d, want 1", len(fake.requests))
	}
}

func TestQueryRetriesInvalidStructuredOutput(t *testing.T) {
	store := session.NewMemoryStore()
	fake := &fakeModel{turns: [][]model.StreamEvent{
		{{Kind: model.StreamText, Text: `not json`}},
		{{Kind: model.StreamText, Text: `{"answer":"fixed"}`}},
	}}
	events, err := Query(context.Background(), "start", Options{
		Model:    fake,
		Sessions: store,
		Output:   answerOutputContract(),
	})
	if err != nil {
		t.Fatalf("Query returned error: %v", err)
	}
	var sessionID string
	var result string
	for event := range events {
		if event.SessionID != "" {
			sessionID = event.SessionID
		}
		if event.Kind == EventError {
			t.Fatalf("query error: %v", event.Err)
		}
		if event.Kind == EventResult {
			result = event.Result
		}
	}
	if result != `{"answer":"fixed"}` {
		t.Fatalf("result = %q, want repaired JSON", result)
	}
	if len(fake.requests) != 2 {
		t.Fatalf("model requests = %d, want retry", len(fake.requests))
	}
	if len(fake.requests[1].Messages) < 3 || !strings.Contains(fake.requests[1].Messages[len(fake.requests[1].Messages)-1].PlainText(), "structured output contract") {
		t.Fatalf("retry messages = %#v, want validation retry prompt", fake.requests[1].Messages)
	}
	messages, err := store.Messages(context.Background(), sessionID)
	if err != nil {
		t.Fatalf("Messages returned error: %v", err)
	}
	if len(messages) < 3 || messages[1].PlainText() != "not json" || !strings.Contains(messages[2].PlainText(), "not valid JSON") {
		t.Fatalf("session messages = %#v, want invalid answer and retry prompt", messages)
	}
}

func TestQueryStructuredOutputRetryUsesDefaultSessionStore(t *testing.T) {
	fake := &fakeModel{turns: [][]model.StreamEvent{
		{{Kind: model.StreamText, Text: `not json`}},
		{{Kind: model.StreamText, Text: `{"answer":"fixed"}`}},
	}}
	events, err := Query(context.Background(), "start", Options{
		Model:  fake,
		Output: answerOutputContract(),
	})
	if err != nil {
		t.Fatalf("Query returned error: %v", err)
	}
	if _, err := Drain(events); err != nil {
		t.Fatalf("Drain returned error: %v", err)
	}
	if len(fake.requests) != 2 {
		t.Fatalf("model requests = %d, want retry", len(fake.requests))
	}
	retryMessages := fake.requests[1].Messages
	if len(retryMessages) < 3 {
		t.Fatalf("retry messages = %#v, want user, invalid assistant, repair prompt", retryMessages)
	}
	if retryMessages[1].Role != model.RoleAssistant || retryMessages[1].PlainText() != "not json" {
		t.Fatalf("retry assistant message = %#v, want invalid assistant persisted", retryMessages[1])
	}
	if retryMessages[2].Role != model.RoleUser || !strings.Contains(retryMessages[2].PlainText(), "not valid JSON") {
		t.Fatalf("retry prompt message = %#v, want validation repair prompt", retryMessages[2])
	}
}

func TestQueryStructuredOutputExhaustionStopsRun(t *testing.T) {
	fake := &fakeModel{turns: [][]model.StreamEvent{{{Kind: model.StreamText, Text: `not json`}}}}
	events, err := Query(context.Background(), "start", Options{
		Model:  fake,
		Output: output.Contract{Schema: answerOutputContract().Schema, MaxRetries: -1},
	})
	if err != nil {
		t.Fatalf("Query returned error: %v", err)
	}
	if _, err := Drain(events); err == nil || !strings.Contains(err.Error(), "validate structured output") {
		t.Fatalf("Drain error = %v, want structured output validation error", err)
	}
	if len(fake.requests) != 1 {
		t.Fatalf("model requests = %d, want no retry", len(fake.requests))
	}
}

func TestQueryRejectsInvalidOutputSchema(t *testing.T) {
	_, err := Query(context.Background(), "start", Options{
		Model: &fakeModel{},
		Output: output.Contract{Schema: map[string]any{
			"type": "not-a-json-schema-type",
		}},
	})
	if err == nil || !strings.Contains(err.Error(), "compile output contract") {
		t.Fatalf("Query error = %v, want output schema compile error", err)
	}
}

func TestQueryWithoutStructuredOutputAcceptsPlainText(t *testing.T) {
	fake := &fakeModel{turns: [][]model.StreamEvent{{{Kind: model.StreamText, Text: `not json`}}}}
	events, err := Query(context.Background(), "start", Options{Model: fake})
	if err != nil {
		t.Fatalf("Query returned error: %v", err)
	}
	result, err := Drain(events)
	if err != nil {
		t.Fatalf("Drain returned error: %v", err)
	}
	if result != "not json" {
		t.Fatalf("result = %q, want plain text", result)
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

func TestQueryLoadsPlannerWithSessionContext(t *testing.T) {
	fake := &fakeModel{turns: [][]model.StreamEvent{{{Kind: model.StreamText, Text: "done"}}}}
	var got planner.Request
	events, err := Query(context.Background(), "inspect billing code", Options{
		Model:           fake,
		ParentSessionID: "parent-session",
		Identity:        identity.Identity{Name: "planner-agent"},
		Planner: planner.PolicyFunc(func(_ context.Context, req planner.Request) (planner.Plan, error) {
			got = req
			return planner.Plan{
				Goal: "inspect billing code safely",
				Steps: []planner.Step{{
					ID:        "step-1",
					Title:     "read relevant files",
					Status:    planner.StatusInProgress,
					ToolHints: []string{"read_file"},
				}},
			}, nil
		}),
	})
	if err != nil {
		t.Fatalf("Query returned error: %v", err)
	}
	if _, err := Drain(events); err != nil {
		t.Fatalf("Drain returned error: %v", err)
	}
	if got.SessionID == "" {
		t.Fatal("planner did not receive active session id")
	}
	if got.ParentSessionID != "parent-session" {
		t.Fatalf("planner parent session = %q, want parent-session", got.ParentSessionID)
	}
	if got.Identity.Name != "planner-agent" {
		t.Fatalf("planner identity = %#v, want planner-agent", got.Identity)
	}
	if len(got.Messages) != 1 || got.Messages[0].PlainText() != "inspect billing code" {
		t.Fatalf("planner messages = %#v, want current prompt", got.Messages)
	}
	if got.Query != "inspect billing code" {
		t.Fatalf("planner query = %q, want user prompt", got.Query)
	}
	if len(fake.requests) != 1 || !strings.Contains(fake.requests[0].SystemPrompt, "inspect billing code safely") {
		t.Fatalf("system prompt = %q, want plan injection", fake.requests[0].SystemPrompt)
	}
}

func TestQueryPlannerErrorStopsRun(t *testing.T) {
	plannerErr := errors.New("planner unavailable")
	events, err := Query(context.Background(), "start", Options{
		Model: &fakeModel{},
		Planner: planner.PolicyFunc(func(context.Context, planner.Request) (planner.Plan, error) {
			return planner.Plan{}, plannerErr
		}),
	})
	if err != nil {
		t.Fatalf("Query returned error: %v", err)
	}
	if _, err := Drain(events); err == nil || !strings.Contains(err.Error(), "build prompt") || !errors.Is(err, plannerErr) {
		t.Fatalf("Drain error = %v, want planner error", err)
	}
}

func TestQueryEmitsMemoryCandidatesAfterValidResult(t *testing.T) {
	fake := &fakeModel{turns: [][]model.StreamEvent{{{Kind: model.StreamText, Text: "rollback notes added"}}}}
	store := &countingMessageStore{inner: session.NewMemoryStore()}
	var got memory.DistillRequest
	events, err := Query(context.Background(), "review migration", Options{
		Model:    fake,
		Sessions: store,
		Planner: planner.Static(planner.Plan{
			Goal: "review migration",
			Steps: []planner.Step{{
				ID:     "task-1",
				Title:  "check rollback",
				Status: planner.StatusCompleted,
			}},
		}),
		MemoryDistiller: memory.DistillerFunc(func(_ context.Context, req memory.DistillRequest) ([]memory.Candidate, error) {
			got = req
			return []memory.Candidate{{
				Memory: memory.Memory{
					Name:    "migration-rollback",
					Scope:   memory.ScopeProject,
					Content: "Migration reviews require rollback notes.",
				},
				Reason:     "final answer confirmed rollback notes",
				Confidence: 0.9,
			}}, nil
		}),
	})
	if err != nil {
		t.Fatalf("Query returned error: %v", err)
	}
	var candidates []memory.Candidate
	var result string
	for event := range events {
		switch event.Kind {
		case EventError:
			t.Fatalf("query error: %v", event.Err)
		case EventMemoryCandidates:
			candidates = event.Memory.Candidates
		case EventResult:
			result = event.Result
		}
	}
	if result != "rollback notes added" {
		t.Fatalf("result = %q, want final result", result)
	}
	if len(candidates) != 1 || candidates[0].Memory.Name != "migration-rollback" {
		t.Fatalf("candidates = %#v, want distilled memory", candidates)
	}
	if got.SessionID == "" || got.Result != "rollback notes added" || got.Plan.Goal != "review migration" {
		t.Fatalf("distill request = %#v, want session, result, and plan", got)
	}
	if len(got.Messages) < 2 || got.Messages[len(got.Messages)-1].PlainText() != "rollback notes added" {
		t.Fatalf("distill messages = %#v, want final assistant in transcript", got.Messages)
	}
	if calls := store.messageCalls(); calls != 1 {
		t.Fatalf("session Messages calls = %d, want one load before distillation", calls)
	}
}

func TestQueryMemoryDistillerErrorStopsRun(t *testing.T) {
	distillErr := errors.New("distiller unavailable")
	events, err := Query(context.Background(), "start", Options{
		Model: &fakeModel{turns: [][]model.StreamEvent{{{Kind: model.StreamText, Text: "done"}}}},
		MemoryDistiller: memory.DistillerFunc(func(context.Context, memory.DistillRequest) ([]memory.Candidate, error) {
			return nil, distillErr
		}),
	})
	if err != nil {
		t.Fatalf("Query returned error: %v", err)
	}
	if _, err := Drain(events); err == nil || !strings.Contains(err.Error(), "distill memories") || !errors.Is(err, distillErr) {
		t.Fatalf("Drain error = %v, want distiller error", err)
	}
}

func TestQueryMemoryCandidateHandlerPersistsAfterEvent(t *testing.T) {
	store := memory.NewMemoryStore(nil)
	releaseHandler := make(chan struct{})
	events, err := Query(context.Background(), "review migration", Options{
		Model: &fakeModel{turns: [][]model.StreamEvent{{{Kind: model.StreamText, Text: "done"}}}},
		MemoryDistiller: memory.StaticDistiller{{
			Memory: memory.Memory{
				Name:    "migration-rollback",
				Scope:   memory.ScopeProject,
				Content: "Migration reviews require rollback notes.",
			},
			Confidence: 0.9,
		}},
		MemoryCandidateHandler: memory.CandidateHandlerFunc(func(ctx context.Context, req memory.CandidateRequest) error {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-releaseHandler:
			}
			return memory.WriterHandler{Writer: store, MinConfidence: 0.5}.HandleCandidates(ctx, req)
		}),
	})
	if err != nil {
		t.Fatalf("Query returned error: %v", err)
	}
	var sawCandidates bool
	var result string
	for event := range events {
		switch event.Kind {
		case EventError:
			t.Fatalf("query error: %v", event.Err)
		case EventMemoryCandidates:
			sawCandidates = true
			items, err := store.Memories(context.Background(), memory.Request{})
			if err != nil {
				t.Fatalf("Memories returned error: %v", err)
			}
			if len(items) != 0 {
				t.Fatalf("stored memories during candidate event = %#v, want handler to run after event", items)
			}
			close(releaseHandler)
		case EventResult:
			result = event.Result
		}
	}
	if !sawCandidates {
		t.Fatal("EventMemoryCandidates not emitted")
	}
	if result != "done" {
		t.Fatalf("result = %q, want done", result)
	}
	items, err := store.Memories(context.Background(), memory.Request{})
	if err != nil {
		t.Fatalf("Memories returned error: %v", err)
	}
	if len(items) != 1 || items[0].Name != "migration-rollback" {
		t.Fatalf("stored memories = %#v, want persisted candidate", items)
	}
}

func TestQueryMemoryCandidateHandlerErrorDoesNotStopRun(t *testing.T) {
	handlerErr := errors.New("review queue unavailable")
	events, err := Query(context.Background(), "start", Options{
		Model: &fakeModel{turns: [][]model.StreamEvent{{{Kind: model.StreamText, Text: "done"}}}},
		MemoryDistiller: memory.StaticDistiller{{
			Memory:     memory.Memory{Name: "lesson", Content: "Persist me."},
			Confidence: 0.9,
		}},
		MemoryCandidateHandler: memory.CandidateHandlerFunc(func(context.Context, memory.CandidateRequest) error {
			return handlerErr
		}),
	})
	if err != nil {
		t.Fatalf("Query returned error: %v", err)
	}
	var gotErr error
	var result string
	for event := range events {
		switch event.Kind {
		case EventError:
			t.Fatalf("terminal query error: %v", event.Err)
		case EventMemoryCandidateHandlerError:
			gotErr = event.Err
		case EventResult:
			result = event.Result
		}
	}
	if gotErr == nil || !strings.Contains(gotErr.Error(), "handle memory candidates") || !errors.Is(gotErr, handlerErr) {
		t.Fatalf("handler event error = %v, want handler error", gotErr)
	}
	if result != "done" {
		t.Fatalf("result = %q, want done", result)
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

func TestQueryEmitsContextCompactedEvent(t *testing.T) {
	store := session.NewMemoryStore()
	sess, err := store.Create(context.Background())
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}
	if err := store.Append(context.Background(), sess.ID, model.Message{
		Role:    model.RoleUser,
		Content: []model.ContentBlock{{Type: model.ContentText, Text: "old-old-old-old"}},
	}); err != nil {
		t.Fatalf("Append returned error: %v", err)
	}
	fake := &fakeModel{turns: [][]model.StreamEvent{{{Kind: model.StreamText, Text: "done"}}}}

	events, err := Query(context.Background(), "recent", Options{
		Model:     fake,
		Sessions:  store,
		SessionID: sess.ID,
		Context: contextwindow.SummarizingBudget{
			MaxTokens:        16,
			MaxSummaryTokens: 10,
			SummaryPrefix:    "S:",
			Summarizer: contextwindow.SummarizerFunc(func(context.Context, []model.Message) (string, error) {
				return "summary", nil
			}),
		},
	})
	if err != nil {
		t.Fatalf("Query returned error: %v", err)
	}
	var compacted *contextwindow.CompactionRecord
	for event := range events {
		if event.Kind == EventContextCompacted {
			compacted = event.Compaction
		}
		if event.Kind == EventError {
			t.Fatalf("query error: %v", event.Err)
		}
	}
	if compacted == nil {
		t.Fatal("missing context compacted event")
	}
	if compacted.OriginalMessages != 2 || compacted.SentMessages != 2 || compacted.SummaryHash == "" {
		t.Fatalf("compaction = %#v, want 2 -> 2 with summary hash", compacted)
	}
	if len(fake.requests) != 1 || len(fake.requests[0].Messages) != 2 {
		t.Fatalf("model request = %#v, want summary plus recent", fake.requests)
	}
	if !contextwindow.IsSummaryMessage(fake.requests[0].Messages[0]) {
		t.Fatalf("first model message metadata = %#v, want context summary", fake.requests[0].Messages[0].Metadata)
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

func TestQueryPersistsStoredResultMetadataInSession(t *testing.T) {
	store := session.NewMemoryStore()
	results := resultstore.NewMemoryStore()
	fake := &fakeModel{turns: [][]model.StreamEvent{
		{{Kind: model.StreamToolUse, ToolUse: model.ToolUse{ID: "tool-1", Name: "read", Input: json.RawMessage(`{}`)}}},
		{{Kind: model.StreamText, Text: "done"}},
	}}
	registry := tool.NewRegistry(tool.Definition{
		ToolSpec: model.ToolSpec{Name: "read", MaxResultBytes: 4},
		Handler: func(context.Context, tool.Call) (model.ToolResult, error) {
			return model.ToolResult{Content: "abcdef"}, nil
		},
	})

	events, err := Query(context.Background(), "start", Options{
		Model:       fake,
		Tools:       registry,
		Sessions:    store,
		ResultStore: results,
	})
	if err != nil {
		t.Fatalf("Query returned error: %v", err)
	}
	var sessionID string
	for event := range events {
		if event.SessionID != "" {
			sessionID = event.SessionID
		}
		if event.Kind == EventError {
			t.Fatalf("query error: %v", event.Err)
		}
		if event.Kind == EventResult {
			break
		}
	}
	messages, err := store.Messages(context.Background(), sessionID)
	if err != nil {
		t.Fatalf("Messages returned error: %v", err)
	}
	var toolResult *model.ToolResult
	for _, msg := range messages {
		if msg.ToolResult != nil {
			toolResult = msg.ToolResult
			break
		}
	}
	if toolResult == nil {
		t.Fatal("session transcript missing tool result")
	}
	id, ok := toolResult.Metadata["stored_result_id"].(string)
	if !ok || id == "" {
		t.Fatalf("tool result metadata = %#v, want stored result id", toolResult.Metadata)
	}
	entry, err := results.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("Get returned error: %v", err)
	}
	if entry.Content != "abcdef" || toolResult.Content != "abcd" {
		t.Fatalf("stored content = %q, transcript content = %q", entry.Content, toolResult.Content)
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

type countingMessageStore struct {
	inner *session.MemoryStore
	mu    sync.Mutex
	calls int
}

func (s *countingMessageStore) Create(ctx context.Context) (session.Session, error) {
	return s.inner.Create(ctx)
}

func (s *countingMessageStore) Append(ctx context.Context, id string, msg model.Message) error {
	return s.inner.Append(ctx, id, msg)
}

func (s *countingMessageStore) Messages(ctx context.Context, id string) ([]model.Message, error) {
	s.mu.Lock()
	s.calls++
	s.mu.Unlock()
	return s.inner.Messages(ctx, id)
}

func (s *countingMessageStore) messageCalls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
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

type earlyToolModel struct {
	toolStarted <-chan struct{}
	first       []model.StreamEvent
	second      []model.StreamEvent
	calls       int
}

func (m *earlyToolModel) Stream(context.Context, model.Request) (model.Stream, error) {
	m.calls++
	if m.calls == 1 {
		return &earlyToolStream{events: m.first, toolStarted: m.toolStarted}, nil
	}
	return &fakeStream{events: m.second}, nil
}

type earlyToolStream struct {
	events      []model.StreamEvent
	index       int
	toolStarted <-chan struct{}
}

func (s *earlyToolStream) Recv() (model.StreamEvent, error) {
	if s.index >= len(s.events) {
		return model.StreamEvent{}, model.ErrEndOfStream
	}
	if s.index == len(s.events)-1 {
		select {
		case <-s.toolStarted:
		case <-time.After(5 * time.Second):
			return model.StreamEvent{}, errors.New("safe tool did not start before trailing assistant text")
		}
	}
	event := s.events[s.index]
	s.index++
	return event, nil
}

func (s *earlyToolStream) Close() error {
	return nil
}

type streamErrorModel struct {
	started <-chan struct{}
	events  []model.StreamEvent
	err     error
}

func (m *streamErrorModel) Stream(context.Context, model.Request) (model.Stream, error) {
	return &streamErrorStream{events: m.events, started: m.started, err: m.err}, nil
}

type streamErrorStream struct {
	events  []model.StreamEvent
	index   int
	started <-chan struct{}
	err     error
}

func (s *streamErrorStream) Recv() (model.StreamEvent, error) {
	if s.index >= len(s.events) {
		if s.started != nil {
			select {
			case <-s.started:
			case <-time.After(5 * time.Second):
				return model.StreamEvent{}, errors.New("early tool did not start before stream error")
			}
		}
		return model.StreamEvent{}, s.err
	}
	event := s.events[s.index]
	s.index++
	return event, nil
}

func (s *streamErrorStream) Close() error {
	return nil
}

func requestHasTool(req model.Request, name string) bool {
	for _, spec := range req.Tools {
		if spec.Name == name {
			return true
		}
	}
	return false
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

func answerOutputContract() output.Contract {
	return output.Contract{Schema: map[string]any{
		"type":     "object",
		"required": []any{"answer"},
		"properties": map[string]any{
			"answer": map[string]any{"type": "string"},
		},
		"additionalProperties": false,
	}}
}

func requestToolNames(req model.Request) []string {
	out := make([]string, 0, len(req.Tools))
	for _, spec := range req.Tools {
		out = append(out, spec.Name)
	}
	return out
}
