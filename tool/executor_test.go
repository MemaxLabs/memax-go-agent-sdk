package tool

import (
	"context"
	"encoding/json"
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
