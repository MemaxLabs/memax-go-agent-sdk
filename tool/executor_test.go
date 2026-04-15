package tool

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/MemaxLabs/memax-go-agent-sdk/hook"
	"github.com/MemaxLabs/memax-go-agent-sdk/model"
	"github.com/MemaxLabs/memax-go-agent-sdk/telemetry"
)

func TestExecutorRunsConcurrentBatchInInputOrder(t *testing.T) {
	reg := NewRegistry(Definition{
		ToolSpec: model.ToolSpec{Name: "echo", ConcurrencySafe: true},
		Handler: func(_ context.Context, call Call) (model.ToolResult, error) {
			var input struct {
				Value string `json:"value"`
			}
			if err := json.Unmarshal(call.Use.Input, &input); err != nil {
				t.Fatalf("unmarshal input: %v", err)
			}
			return model.ToolResult{Content: input.Value}, nil
		},
	})

	uses := []model.ToolUse{
		{ID: "1", Name: "echo", Input: json.RawMessage(`{"value":"a"}`)},
		{ID: "2", Name: "echo", Input: json.RawMessage(`{"value":"b"}`)},
	}
	results := collect(Executor{Registry: reg}.Run(context.Background(), uses))

	if got, want := len(results), 2; got != want {
		t.Fatalf("len(results) = %d, want %d", got, want)
	}
	if results[0].Content != "a" || results[1].Content != "b" {
		t.Fatalf("results out of order: %#v", results)
	}
}

func TestExecutorDeniesByPermission(t *testing.T) {
	reg := NewRegistry(Definition{
		ToolSpec: model.ToolSpec{Name: "write", Destructive: true},
		Handler: func(context.Context, Call) (model.ToolResult, error) {
			t.Fatal("handler should not run")
			return model.ToolResult{}, nil
		},
	})

	use := model.ToolUse{ID: "1", Name: "write"}
	results := collect(Executor{
		Registry: reg,
		Permissions: permissionFunc(func(context.Context, model.ToolUse, model.ToolSpec) Decision {
			return Decision{Allow: false, Reason: "blocked"}
		}),
	}.Run(context.Background(), []model.ToolUse{use}))

	if got, want := len(results), 1; got != want {
		t.Fatalf("len(results) = %d, want %d", got, want)
	}
	if !results[0].IsError || results[0].Content != "blocked" {
		t.Fatalf("unexpected result: %#v", results[0])
	}
}

func TestExecutorValidatesInputBeforePermissionAndHandler(t *testing.T) {
	reg := NewRegistry(Definition{
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
		Handler: func(context.Context, Call) (model.ToolResult, error) {
			t.Fatal("handler should not run")
			return model.ToolResult{}, nil
		},
	})

	permissionCalled := false
	results := collect(Executor{
		Registry: reg,
		Permissions: permissionFunc(func(context.Context, model.ToolUse, model.ToolSpec) Decision {
			permissionCalled = true
			return Decision{Allow: true}
		}),
	}.Run(context.Background(), []model.ToolUse{
		{ID: "1", Name: "read", Input: json.RawMessage(`{"path":42}`)},
	}))

	if permissionCalled {
		t.Fatal("permission checker should not run after validation failure")
	}
	if got, want := len(results), 1; got != want {
		t.Fatalf("len(results) = %d, want %d", got, want)
	}
	if !results[0].IsError {
		t.Fatalf("result should be an error: %#v", results[0])
	}
	if !strings.Contains(results[0].Content, "invalid input for tool") {
		t.Fatalf("result content = %q, want validation error", results[0].Content)
	}
}

func TestRegistryRejectsInvalidInputSchema(t *testing.T) {
	reg := NewRegistry()
	err := reg.Register(Definition{
		ToolSpec: model.ToolSpec{
			Name:        "bad",
			InputSchema: map[string]any{"type": "not-a-json-schema-type"},
		},
		Handler: func(context.Context, Call) (model.ToolResult, error) {
			return model.ToolResult{}, nil
		},
	})
	if err == nil {
		t.Fatal("Register returned nil, want schema error")
	}
}

func TestExecutorBeforeHookDeniesBeforePermissionAndHandler(t *testing.T) {
	reg := NewRegistry(Definition{
		ToolSpec: model.ToolSpec{Name: "write"},
		Handler: func(context.Context, Call) (model.ToolResult, error) {
			t.Fatal("handler should not run")
			return model.ToolResult{}, nil
		},
	})

	permissionCalled := false
	hooks := hook.NewRunner(hook.WithBeforeToolUse(func(_ context.Context, input hook.BeforeToolUseInput) (hook.BeforeToolUseResult, error) {
		if input.SessionID != "session-1" {
			t.Fatalf("SessionID = %q, want session-1", input.SessionID)
		}
		return hook.BeforeToolUseResult{DenyReason: "blocked by hook"}, nil
	}))

	results := collect(Executor{
		Registry: reg,
		Permissions: permissionFunc(func(context.Context, model.ToolUse, model.ToolSpec) Decision {
			permissionCalled = true
			return Decision{Allow: true}
		}),
		Hooks:   hooks,
		Runtime: Runtime{SessionID: "session-1"},
	}.Run(context.Background(), []model.ToolUse{{ID: "1", Name: "write"}}))

	if permissionCalled {
		t.Fatal("permission checker should not run after hook denial")
	}
	if got, want := len(results), 1; got != want {
		t.Fatalf("len(results) = %d, want %d", got, want)
	}
	if !results[0].IsError || results[0].Content != "blocked by hook" {
		t.Fatalf("unexpected result: %#v", results[0])
	}
}

func TestExecutorAfterHookErrorsAttachMetadataWithoutFailingResult(t *testing.T) {
	reg := NewRegistry(Definition{
		ToolSpec: model.ToolSpec{Name: "read"},
		Handler: func(context.Context, Call) (model.ToolResult, error) {
			return model.ToolResult{Content: "ok"}, nil
		},
	})
	hooks := hook.NewRunner(hook.WithAfterToolUse(func(context.Context, hook.AfterToolUseInput) error {
		return errors.New("audit sink unavailable")
	}))

	results := collect(Executor{
		Registry: reg,
		Hooks:    hooks,
	}.Run(context.Background(), []model.ToolUse{{ID: "1", Name: "read"}}))

	if got, want := len(results), 1; got != want {
		t.Fatalf("len(results) = %d, want %d", got, want)
	}
	if results[0].IsError || results[0].Content != "ok" {
		t.Fatalf("unexpected result: %#v", results[0])
	}
	hookErrors, ok := results[0].Metadata["hook_errors"].([]string)
	if !ok || len(hookErrors) != 1 || hookErrors[0] != "audit sink unavailable" {
		t.Fatalf("hook_errors = %#v, want audit sink error", results[0].Metadata["hook_errors"])
	}
}

func TestExecutorTruncatesLargeToolResults(t *testing.T) {
	reg := NewRegistry(Definition{
		ToolSpec: model.ToolSpec{Name: "read", MaxResultBytes: 4},
		Handler: func(context.Context, Call) (model.ToolResult, error) {
			return model.ToolResult{Content: "abcdef"}, nil
		},
	})

	results := collect(Executor{
		Registry: reg,
	}.Run(context.Background(), []model.ToolUse{{ID: "1", Name: "read"}}))

	if got, want := len(results), 1; got != want {
		t.Fatalf("len(results) = %d, want %d", got, want)
	}
	if results[0].Content != "abcd" {
		t.Fatalf("Content = %q, want abcd", results[0].Content)
	}
	if results[0].Metadata["truncated"] != true {
		t.Fatalf("truncated metadata = %#v, want true", results[0].Metadata["truncated"])
	}
	if results[0].Metadata["original_bytes"] != 6 || results[0].Metadata["returned_bytes"] != 4 {
		t.Fatalf("metadata = %#v, want byte counts", results[0].Metadata)
	}
}

func TestExecutorTruncatesAtUTF8Boundary(t *testing.T) {
	reg := NewRegistry(Definition{
		ToolSpec: model.ToolSpec{Name: "read", MaxResultBytes: 2},
		Handler: func(context.Context, Call) (model.ToolResult, error) {
			return model.ToolResult{Content: "éx"}, nil
		},
	})

	results := collect(Executor{
		Registry: reg,
	}.Run(context.Background(), []model.ToolUse{{ID: "1", Name: "read"}}))

	if results[0].Content != "é" {
		t.Fatalf("Content = %q, want é", results[0].Content)
	}
}

func TestExecutorStartsToolSpan(t *testing.T) {
	tracer := &toolTracer{}
	reg := NewRegistry(Definition{
		ToolSpec: model.ToolSpec{Name: "read", ReadOnly: true},
		Handler: func(context.Context, Call) (model.ToolResult, error) {
			return model.ToolResult{Content: "ok"}, nil
		},
	})

	results := collect(Executor{
		Registry: reg,
		Runtime:  Runtime{SessionID: "session-1"},
		Tracer:   tracer,
	}.Run(context.Background(), []model.ToolUse{{ID: "toolu_1", Name: "read", Input: json.RawMessage(`{}`)}}))

	if len(results) != 1 || results[0].Content != "ok" {
		t.Fatalf("results = %#v", results)
	}
	if tracer.name != "memaxagent.tool.execute" {
		t.Fatalf("span name = %q", tracer.name)
	}
	if !tracer.ended {
		t.Fatal("tool span was not ended")
	}
	if !hasAttr(tracer.attrs, "memax.tool.name", "read") || !hasAttr(tracer.attrs, "memax.session_id", "session-1") {
		t.Fatalf("span attrs = %#v", tracer.attrs)
	}
}

type permissionFunc func(context.Context, model.ToolUse, model.ToolSpec) Decision

func (f permissionFunc) Check(ctx context.Context, use model.ToolUse, spec model.ToolSpec) Decision {
	return f(ctx, use, spec)
}

func collect(ch <-chan model.ToolResult) []model.ToolResult {
	var out []model.ToolResult
	for item := range ch {
		out = append(out, item)
	}
	return out
}

type toolTracer struct {
	name  string
	attrs []telemetry.Attribute
	ended bool
}

func (t *toolTracer) Start(ctx context.Context, name string, attrs ...telemetry.Attribute) (context.Context, telemetry.Span) {
	t.name = name
	t.attrs = append(t.attrs, attrs...)
	return ctx, t
}

func (t *toolTracer) Set(attrs ...telemetry.Attribute) {
	t.attrs = append(t.attrs, attrs...)
}

func (t *toolTracer) RecordError(error) {}

func (t *toolTracer) End() {
	t.ended = true
}

func hasAttr(attrs []telemetry.Attribute, key string, value any) bool {
	for _, attr := range attrs {
		if attr.Key == key && attr.Value == value {
			return true
		}
	}
	return false
}
