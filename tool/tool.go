package tool

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/MemaxLabs/memax-go-agent-sdk/identity"
	"github.com/MemaxLabs/memax-go-agent-sdk/model"
	"github.com/MemaxLabs/memax-go-agent-sdk/session"
	"github.com/MemaxLabs/memax-go-agent-sdk/tenant"
)

type Runtime struct {
	SessionID       string
	ParentSessionID string
	Identity        identity.Identity
	Tenant          tenant.Scope
	TenantValidator tenant.Validator
	Sessions        session.Store
}

type Tool interface {
	Spec() model.ToolSpec
	Execute(context.Context, Call) (model.ToolResult, error)
	CanRunConcurrently(model.ToolUse) bool
}

// InputNormalizer is an optional Tool extension for canonicalizing model tool
// input before schema validation, hooks, permissions, and execution. Use it for
// narrow, deterministic repairs of common shape mistakes, not for changing tool
// semantics.
type InputNormalizer interface {
	NormalizeInput(context.Context, model.ToolUse) (model.ToolUse, bool, error)
}

// InputNormalizerFunc adapts a function into an InputNormalizer.
type InputNormalizerFunc func(context.Context, model.ToolUse) (model.ToolUse, bool, error)

func (f InputNormalizerFunc) NormalizeInput(ctx context.Context, use model.ToolUse) (model.ToolUse, bool, error) {
	if f == nil {
		return use, false, nil
	}
	return f(ctx, use)
}

type Call struct {
	Use     model.ToolUse
	Runtime Runtime
}

type Handler func(context.Context, Call) (model.ToolResult, error)

type Definition struct {
	ToolSpec   model.ToolSpec
	Handler    Handler
	Normalizer InputNormalizer
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

func (d Definition) NormalizeInput(ctx context.Context, use model.ToolUse) (model.ToolUse, bool, error) {
	if d.Normalizer == nil {
		return use, false, nil
	}
	return d.Normalizer.NormalizeInput(ctx, use)
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
