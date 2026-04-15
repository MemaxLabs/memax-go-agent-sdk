package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/MemaxLabs/memax-go-agent-sdk/model"
)

func BenchmarkExecutorConcurrentReadTools(b *testing.B) {
	registry := NewRegistry(Definition{
		ToolSpec: model.ToolSpec{Name: "echo", ReadOnly: true, ConcurrencySafe: true},
		Handler: func(_ context.Context, call Call) (model.ToolResult, error) {
			return model.ToolResult{Content: string(call.Use.Input)}, nil
		},
	})
	uses := make([]model.ToolUse, 128)
	for i := range uses {
		uses[i] = model.ToolUse{
			ID:    fmt.Sprintf("tool-%d", i),
			Name:  "echo",
			Input: json.RawMessage(`{"value":"benchmark"}`),
		}
	}
	executor := Executor{Registry: registry, MaxConcurrency: 32}
	ctx := context.Background()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		count := 0
		for result := range executor.Run(ctx, uses) {
			if result.IsError {
				b.Fatalf("tool error: %s", result.Content)
			}
			count++
		}
		if count != len(uses) {
			b.Fatalf("got %d results, want %d", count, len(uses))
		}
	}
}
