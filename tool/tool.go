package tool

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/MemaxLabs/memax-go-agent-sdk/model"
	"github.com/MemaxLabs/memax-go-agent-sdk/session"
)

type Runtime struct {
	SessionID       string
	ParentSessionID string
	Sessions        session.Store
}

type Tool interface {
	Spec() model.ToolSpec
	Execute(context.Context, Call) (model.ToolResult, error)
	CanRunConcurrently(model.ToolUse) bool
}

type Call struct {
	Use     model.ToolUse
	Runtime Runtime
}

type Handler func(context.Context, Call) (model.ToolResult, error)

type Definition struct {
	ToolSpec model.ToolSpec
	Handler  Handler
}

func (d Definition) Spec() model.ToolSpec {
	return d.ToolSpec
}

func (d Definition) Execute(ctx context.Context, call Call) (model.ToolResult, error) {
	if d.Handler == nil {
		return model.ToolResult{}, fmt.Errorf("tool %q has no handler", d.ToolSpec.Name)
	}
	return d.Handler(ctx, call)
}

func (d Definition) CanRunConcurrently(_ model.ToolUse) bool {
	return d.ToolSpec.ConcurrencySafe
}

func DecodeInput[T any](use model.ToolUse) (T, error) {
	var out T
	if len(use.Input) == 0 {
		return out, nil
	}
	if err := json.Unmarshal(use.Input, &out); err != nil {
		return out, fmt.Errorf("decode %s input: %w", use.Name, err)
	}
	return out, nil
}
