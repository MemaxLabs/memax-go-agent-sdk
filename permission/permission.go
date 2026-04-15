package permission

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"slices"

	"github.com/MemaxLabs/memax-go-agent-sdk/model"
	"github.com/MemaxLabs/memax-go-agent-sdk/tool"
)

type Checker interface {
	Check(context.Context, model.ToolUse, model.ToolSpec) tool.Decision
}

// AllowAll allows every tool call.
type AllowAll struct{}

func (AllowAll) Check(context.Context, model.ToolUse, model.ToolSpec) tool.Decision {
	return tool.Decision{Allow: true}
}

// ReadOnly allows tools declared read-only and not destructive.
type ReadOnly struct{}

func (ReadOnly) Check(_ context.Context, use model.ToolUse, spec model.ToolSpec) tool.Decision {
	if spec.ReadOnly && !spec.Destructive {
		return tool.Decision{Allow: true}
	}
	return tool.Decision{Allow: false, Reason: fmt.Sprintf("tool %q is not read-only", use.Name)}
}

// Func adapts a function to Checker.
type Func func(context.Context, model.ToolUse, model.ToolSpec) tool.Decision

func (f Func) Check(ctx context.Context, use model.ToolUse, spec model.ToolSpec) tool.Decision {
	return f(ctx, use, spec)
}

// Effect controls what a matching rule does.
type Effect string

const (
	// EffectAllow allows a matching tool call.
	EffectAllow Effect = "allow"
	// EffectDeny denies a matching tool call.
	EffectDeny Effect = "deny"
	// EffectAsk delegates a matching tool call to a host approver.
	EffectAsk Effect = "ask"
)

// Rule is one ordered permission policy rule.
type Rule struct {
	Effect  Effect
	Match   Matcher
	Reason  string
	Message string
}

// Allow creates an allow rule.
func Allow(match Matcher) Rule {
	return Rule{Effect: EffectAllow, Match: match}
}

// Deny creates a deny rule with a model-visible reason.
func Deny(match Matcher, reason string) Rule {
	return Rule{Effect: EffectDeny, Match: match, Reason: reason}
}

// Ask creates an approval rule with a host-facing message.
func Ask(match Matcher, message string) Rule {
	return Rule{Effect: EffectAsk, Match: match, Message: message}
}

// Policy applies ordered rules and optionally delegates matching ask rules to
// a host approver. If no rule matches, Policy denies unless Default is set.
type Policy struct {
	Rules    []Rule
	Default  tool.Decision
	Approver Approver
}

func (p Policy) Check(ctx context.Context, use model.ToolUse, spec model.ToolSpec) tool.Decision {
	for _, rule := range p.Rules {
		match := rule.Match
		if match == nil {
			match = Any()
		}
		if !match.Match(ctx, use, spec) {
			continue
		}
		return p.applyRule(ctx, rule, use, spec)
	}
	if p.Default.Allow || p.Default.Reason != "" {
		return p.Default
	}
	return tool.Decision{Allow: false, Reason: fmt.Sprintf("no permission rule matched tool %q", use.Name)}
}

func (p Policy) applyRule(ctx context.Context, rule Rule, use model.ToolUse, spec model.ToolSpec) tool.Decision {
	switch rule.Effect {
	case EffectAllow:
		return tool.Decision{Allow: true}
	case EffectDeny:
		return deny(rule, fmt.Sprintf("tool %q denied by policy", use.Name))
	case EffectAsk:
		if p.Approver == nil {
			return deny(rule, fmt.Sprintf("tool %q requires approval", use.Name))
		}
		decision := p.Approver.Approve(ctx, ApprovalRequest{
			Use:     use,
			Spec:    spec,
			Message: rule.Message,
		})
		if !decision.Allow && decision.Reason == "" {
			decision.Reason = fmt.Sprintf("tool %q was not approved", use.Name)
		}
		return decision
	default:
		return deny(rule, fmt.Sprintf("tool %q matched invalid permission effect %q", use.Name, rule.Effect))
	}
}

func deny(rule Rule, fallback string) tool.Decision {
	if rule.Reason != "" {
		return tool.Decision{Allow: false, Reason: rule.Reason}
	}
	return tool.Decision{Allow: false, Reason: fallback}
}

// ApprovalRequest is sent to the host approver for ask rules.
type ApprovalRequest struct {
	Use     model.ToolUse
	Spec    model.ToolSpec
	Message string
}

// Approver decides whether an ask rule may run.
type Approver interface {
	Approve(context.Context, ApprovalRequest) tool.Decision
}

// ApproverFunc adapts a function to Approver.
type ApproverFunc func(context.Context, ApprovalRequest) tool.Decision

// Approve calls f(ctx, req).
func (f ApproverFunc) Approve(ctx context.Context, req ApprovalRequest) tool.Decision {
	return f(ctx, req)
}

// Matcher checks whether a policy rule applies to a tool call.
type Matcher interface {
	Match(context.Context, model.ToolUse, model.ToolSpec) bool
}

// MatcherFunc adapts a function to Matcher.
type MatcherFunc func(context.Context, model.ToolUse, model.ToolSpec) bool

// Match calls f(ctx, use, spec).
func (f MatcherFunc) Match(ctx context.Context, use model.ToolUse, spec model.ToolSpec) bool {
	return f(ctx, use, spec)
}

// Any matches every tool call.
func Any() Matcher {
	return MatcherFunc(func(context.Context, model.ToolUse, model.ToolSpec) bool {
		return true
	})
}

// ToolName matches exact tool names.
func ToolName(names ...string) Matcher {
	allowed := append([]string(nil), names...)
	return MatcherFunc(func(_ context.Context, use model.ToolUse, _ model.ToolSpec) bool {
		return slices.Contains(allowed, use.Name)
	})
}

// ToolNamePattern matches tool names using path.Match glob patterns.
func ToolNamePattern(patterns ...string) Matcher {
	allowed := append([]string(nil), patterns...)
	return MatcherFunc(func(_ context.Context, use model.ToolUse, _ model.ToolSpec) bool {
		for _, pattern := range allowed {
			if ok, err := path.Match(pattern, use.Name); err == nil && ok {
				return true
			}
		}
		return false
	})
}

// ReadOnlyTool matches tools declared read-only and not destructive.
func ReadOnlyTool() Matcher {
	return MatcherFunc(func(_ context.Context, _ model.ToolUse, spec model.ToolSpec) bool {
		return spec.ReadOnly && !spec.Destructive
	})
}

// DestructiveTool matches tools declared destructive.
func DestructiveTool() Matcher {
	return MatcherFunc(func(_ context.Context, _ model.ToolUse, spec model.ToolSpec) bool {
		return spec.Destructive
	})
}

// InputString matches a top-level string field in the tool input using
// path.Match glob patterns.
func InputString(field string, patterns ...string) Matcher {
	allowed := append([]string(nil), patterns...)
	return MatcherFunc(func(_ context.Context, use model.ToolUse, _ model.ToolSpec) bool {
		var input map[string]any
		if err := json.Unmarshal(use.Input, &input); err != nil {
			return false
		}
		value, ok := input[field].(string)
		if !ok {
			return false
		}
		for _, pattern := range allowed {
			if ok, err := path.Match(pattern, value); err == nil && ok {
				return true
			}
		}
		return false
	})
}

// All matches when every non-nil matcher matches.
func All(matchers ...Matcher) Matcher {
	copied := append([]Matcher(nil), matchers...)
	return MatcherFunc(func(ctx context.Context, use model.ToolUse, spec model.ToolSpec) bool {
		for _, matcher := range copied {
			if matcher == nil {
				continue
			}
			if !matcher.Match(ctx, use, spec) {
				return false
			}
		}
		return true
	})
}

// AnyOf matches when any matcher matches.
func AnyOf(matchers ...Matcher) Matcher {
	copied := append([]Matcher(nil), matchers...)
	return MatcherFunc(func(ctx context.Context, use model.ToolUse, spec model.ToolSpec) bool {
		for _, matcher := range copied {
			if matcher != nil && matcher.Match(ctx, use, spec) {
				return true
			}
		}
		return false
	})
}

// Not negates a matcher.
func Not(matcher Matcher) Matcher {
	return MatcherFunc(func(ctx context.Context, use model.ToolUse, spec model.ToolSpec) bool {
		return matcher == nil || !matcher.Match(ctx, use, spec)
	})
}
