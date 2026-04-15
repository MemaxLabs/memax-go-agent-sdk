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

	// Intentionally adversarial: a bad tool declaration must not bypass
	// ReadOnly just by also setting ReadOnly=true.
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

func TestPolicyAppliesFirstMatchingRule(t *testing.T) {
	policy := Policy{
		Rules: []Rule{
			Deny(ToolName("write"), "blocked write"),
			Allow(Any()),
		},
	}

	deny := policy.Check(context.Background(), model.ToolUse{Name: "write"}, model.ToolSpec{Name: "write"})
	if deny.Allow || deny.Reason != "blocked write" {
		t.Fatalf("deny decision = %#v, want first rule denial", deny)
	}

	allow := policy.Check(context.Background(), model.ToolUse{Name: "read"}, model.ToolSpec{Name: "read"})
	if !allow.Allow {
		t.Fatalf("allow decision = %#v, want allow", allow)
	}
}

func TestPolicyDefaultsToDenyWhenNoRuleMatches(t *testing.T) {
	decision := (Policy{}).Check(context.Background(), model.ToolUse{Name: "write"}, model.ToolSpec{Name: "write"})
	if decision.Allow || decision.Reason == "" {
		t.Fatalf("decision = %#v, want default deny with reason", decision)
	}
}

func TestPolicyUsesConfiguredDefault(t *testing.T) {
	decision := (Policy{
		Default: tool.Decision{Allow: true},
	}).Check(context.Background(), model.ToolUse{Name: "read"}, model.ToolSpec{Name: "read"})
	if !decision.Allow {
		t.Fatalf("decision = %#v, want default allow", decision)
	}
}

func TestPolicyAsksApprover(t *testing.T) {
	var got ApprovalRequest
	policy := Policy{
		Rules: []Rule{
			Ask(ToolName("write"), "approve write?"),
		},
		Approver: ApproverFunc(func(_ context.Context, req ApprovalRequest) tool.Decision {
			got = req
			return tool.Decision{Allow: true}
		}),
	}

	decision := policy.Check(context.Background(), model.ToolUse{Name: "write"}, model.ToolSpec{Name: "write"})
	if !decision.Allow {
		t.Fatalf("decision = %#v, want approved", decision)
	}
	if got.Use.Name != "write" || got.Message != "approve write?" {
		t.Fatalf("approval request = %#v", got)
	}
}

func TestPolicyDeniesAskWithoutApprover(t *testing.T) {
	decision := (Policy{
		Rules: []Rule{Ask(ToolName("write"), "")},
	}).Check(context.Background(), model.ToolUse{Name: "write"}, model.ToolSpec{Name: "write"})
	if decision.Allow || decision.Reason == "" {
		t.Fatalf("decision = %#v, want denial", decision)
	}
}

func TestRuleConstructors(t *testing.T) {
	if got := Allow(ToolName("read")); got.Effect != EffectAllow || got.Match == nil {
		t.Fatalf("Allow rule = %#v", got)
	}
	if got := Deny(ToolName("write"), "no"); got.Effect != EffectDeny || got.Reason != "no" {
		t.Fatalf("Deny rule = %#v", got)
	}
	if got := Ask(ToolName("write"), "approve?"); got.Effect != EffectAsk || got.Message != "approve?" {
		t.Fatalf("Ask rule = %#v", got)
	}
}

func TestMatchers(t *testing.T) {
	use := model.ToolUse{Name: "write_file", Input: []byte(`{"path":"docs/readme.md"}`)}
	spec := model.ToolSpec{Name: "write_file", ReadOnly: true}

	cases := []struct {
		name    string
		matcher Matcher
		want    bool
	}{
		{name: "tool name", matcher: ToolName("read_file", "write_file"), want: true},
		{name: "tool pattern", matcher: ToolNamePattern("*_file"), want: true},
		{name: "read only", matcher: ReadOnlyTool(), want: true},
		{name: "destructive", matcher: DestructiveTool(), want: false},
		{name: "input string", matcher: InputString("path", "docs/*.md"), want: true},
		{name: "all", matcher: All(ToolNamePattern("write_*"), InputString("path", "docs/*")), want: true},
		{name: "any of", matcher: AnyOf(ToolName("missing"), ToolName("write_file")), want: true},
		{name: "not", matcher: Not(ToolName("read_file")), want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.matcher.Match(context.Background(), use, spec); got != tc.want {
				t.Fatalf("Match = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestInputStringRejectsInvalidOrMissingInput(t *testing.T) {
	matcher := InputString("path", "docs/*")
	if matcher.Match(context.Background(), model.ToolUse{Input: []byte(`{"path":42}`)}, model.ToolSpec{}) {
		t.Fatal("InputString matched non-string value")
	}
	if matcher.Match(context.Background(), model.ToolUse{Input: []byte(`not-json`)}, model.ToolSpec{}) {
		t.Fatal("InputString matched invalid JSON")
	}
}
