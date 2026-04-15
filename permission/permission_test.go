package permission

import (
	"context"
	"testing"

	"github.com/MemaxLabs/memax-go-agent-sdk/model"
	"github.com/MemaxLabs/memax-go-agent-sdk/tool"
)

func TestAllowAllAllowsAnyTool(t *testing.T) {
	decision := (AllowAll{}).Check(context.Background(), model.ToolUse{Name: "write"}, model.ToolSpec{Destructive: true})
	if !decision.Allow {
		t.Fatalf("AllowAll decision = %#v, want allow", decision)
	}
}

func TestReadOnlyAllowsOnlyNonDestructiveReadOnlyTools(t *testing.T) {
	allow := (ReadOnly{}).Check(context.Background(), model.ToolUse{Name: "read"}, model.ToolSpec{ReadOnly: true})
	if !allow.Allow {
		t.Fatalf("ReadOnly allow decision = %#v, want allow", allow)
	}

	deny := (ReadOnly{}).Check(context.Background(), model.ToolUse{Name: "write"}, model.ToolSpec{ReadOnly: true, Destructive: true})
	if deny.Allow || deny.Reason == "" {
		t.Fatalf("ReadOnly deny decision = %#v, want deny with reason", deny)
	}
}

func TestFuncDelegates(t *testing.T) {
	called := false
	checker := Func(func(context.Context, model.ToolUse, model.ToolSpec) tool.Decision {
		called = true
		return tool.Decision{Allow: true}
	})

	decision := checker.Check(context.Background(), model.ToolUse{Name: "x"}, model.ToolSpec{Name: "x"})
	if !called || !decision.Allow {
		t.Fatalf("Func decision = %#v called=%v, want delegated allow", decision, called)
	}
}
