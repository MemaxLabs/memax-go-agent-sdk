package skill

import (
	"context"
	"fmt"
)

// Decision is the result of checking a skill against host policy.
type Decision struct {
	Allow   bool
	Reason  string
	Rewrite *Skill
}

// Policy checks or rewrites skills loaded from another source.
type Policy interface {
	CheckSkill(context.Context, Skill) Decision
}

// PolicyFunc adapts a function to Policy.
type PolicyFunc func(context.Context, Skill) Decision

// CheckSkill calls f(ctx, skill).
func (f PolicyFunc) CheckSkill(ctx context.Context, item Skill) Decision {
	return f(ctx, item)
}

// PolicySource applies a skill policy to another source.
type PolicySource struct {
	Source      Source
	Policy      Policy
	DenyAsError bool
}

// Skills loads skills, applies Policy, and returns the allowed skills.
func (s PolicySource) Skills(ctx context.Context) ([]Skill, error) {
	if s.Source == nil {
		return nil, fmt.Errorf("skill: policy source requires Source")
	}
	if s.Policy == nil {
		return s.Source.Skills(ctx)
	}
	items, err := s.Source.Skills(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Skill, 0, len(items))
	for _, item := range items {
		decision := s.Policy.CheckSkill(ctx, item)
		if !decision.Allow {
			if s.DenyAsError {
				if decision.Reason == "" {
					decision.Reason = fmt.Sprintf("skill %q denied by policy", item.Name)
				}
				return nil, fmt.Errorf("%s", decision.Reason)
			}
			continue
		}
		if decision.Rewrite == nil {
			out = append(out, clone(item))
			continue
		}
		out = append(out, clone(*decision.Rewrite))
	}
	return out, nil
}

// AllowAllPolicy allows every skill.
type AllowAllPolicy struct{}

// CheckSkill allows item.
func (AllowAllPolicy) CheckSkill(_ context.Context, item Skill) Decision {
	return Decision{Allow: true}
}
