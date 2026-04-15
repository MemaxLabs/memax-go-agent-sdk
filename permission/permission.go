package permission

import (
	"context"
	"fmt"

	"github.com/MemaxLabs/memax-go-agent-sdk/model"
	"github.com/MemaxLabs/memax-go-agent-sdk/tool"
)

type Checker interface {
	Check(context.Context, model.ToolUse, model.ToolSpec) tool.Decision
}

type AllowAll struct{}

func (AllowAll) Check(context.Context, model.ToolUse, model.ToolSpec) tool.Decision {
	return tool.Decision{Allow: true}
}

type ReadOnly struct{}

func (ReadOnly) Check(_ context.Context, use model.ToolUse, spec model.ToolSpec) tool.Decision {
	if spec.ReadOnly && !spec.Destructive {
		return tool.Decision{Allow: true}
	}
	return tool.Decision{Allow: false, Reason: fmt.Sprintf("tool %q is not read-only", use.Name)}
}

type Func func(context.Context, model.ToolUse, model.ToolSpec) tool.Decision

func (f Func) Check(ctx context.Context, use model.ToolUse, spec model.ToolSpec) tool.Decision {
	return f(ctx, use, spec)
}
