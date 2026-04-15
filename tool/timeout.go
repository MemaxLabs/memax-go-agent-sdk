package tool

import (
	"context"
	"fmt"
	"time"

	"github.com/MemaxLabs/memax-go-agent-sdk/model"
)

// TimeoutTool runs another tool with a per-call timeout.
//
// If the wrapped tool ignores context cancellation, Execute returns when the
// timeout expires and the wrapped call may continue in its own goroutine until
// it returns. Tool implementations should still honor ctx for resource cleanup.
type TimeoutTool struct {
	Tool    Tool
	Timeout time.Duration
}

// WithTimeout wraps t with a per-call timeout.
func WithTimeout(t Tool, timeout time.Duration) TimeoutTool {
	return TimeoutTool{Tool: t, Timeout: timeout}
}

func (t TimeoutTool) Spec() model.ToolSpec {
	if t.Tool == nil {
		return model.ToolSpec{}
	}
	return t.Tool.Spec()
}

func (t TimeoutTool) CanRunConcurrently(use model.ToolUse) bool {
	return t.Tool != nil && t.Tool.CanRunConcurrently(use)
}

func (t TimeoutTool) Execute(ctx context.Context, call Call) (model.ToolResult, error) {
	if t.Tool == nil {
		return model.ToolResult{}, fmt.Errorf("tool timeout wrapper requires Tool")
	}
	if t.Timeout <= 0 {
		return t.Tool.Execute(ctx, call)
	}
	ctx, cancel := context.WithTimeout(ctx, t.Timeout)
	defer cancel()

	type response struct {
		result model.ToolResult
		err    error
	}
	done := make(chan response, 1)
	go func() {
		result, err := t.Tool.Execute(ctx, call)
		done <- response{result: result, err: err}
	}()

	select {
	case response := <-done:
		return response.result, response.err
	case <-ctx.Done():
		return model.ToolResult{}, ctx.Err()
	}
}
