package tool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/MemaxLabs/memax-go-agent-sdk/model"
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
