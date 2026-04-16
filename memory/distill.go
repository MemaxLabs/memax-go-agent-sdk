package memory

import (
	"context"
	"strings"

	"github.com/MemaxLabs/memax-go-agent-sdk/identity"
	"github.com/MemaxLabs/memax-go-agent-sdk/model"
	"github.com/MemaxLabs/memax-go-agent-sdk/planner"
)

// Distiller proposes durable memory candidates from completed agent work.
// Distillers do not write memories; hosts decide whether and where candidates
// are persisted.
type Distiller interface {
	Distill(context.Context, DistillRequest) ([]Candidate, error)
}

// DistillerFunc adapts a function to Distiller.
type DistillerFunc func(context.Context, DistillRequest) ([]Candidate, error)

// Distill calls f(ctx, req). A nil DistillerFunc returns no candidates.
func (f DistillerFunc) Distill(ctx context.Context, req DistillRequest) ([]Candidate, error) {
	if f == nil {
		return nil, nil
	}
	return f(ctx, req)
}

// DistillRequest gives distillers completed run context. Messages may contain
// the full durable transcript for the run; model-backed distillers should apply
// their own context budgeting or summarization before sending it to a model.
type DistillRequest struct {
	SessionID       string
	ParentSessionID string
	Identity        identity.Identity
	Messages        []model.Message
	Plan            planner.Plan
	Result          string
}

// Candidate is one proposed durable memory plus provenance for host review.
type Candidate struct {
	Memory     Memory
	Reason     string
	Confidence float64
}

// StaticDistiller returns a fixed candidate set.
type StaticDistiller []Candidate

// Distill returns defensive copies of the configured candidates.
func (d StaticDistiller) Distill(ctx context.Context, _ DistillRequest) ([]Candidate, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	return CloneCandidates(d), nil
}

// DistillRule describes a deterministic rule for RuleDistiller.
type DistillRule struct {
	WhenResultContains string
	WhenPlanContains   string
	Memory             Memory
	Reason             string
	Confidence         float64
}

// RuleDistiller proposes memories when simple result/plan text predicates
// match. It is intended for deterministic tests, examples, and host-owned
// heuristics; model-backed distillation can implement Distiller directly.
type RuleDistiller []DistillRule

// Distill evaluates every rule and returns candidates for matching rules.
func (d RuleDistiller) Distill(ctx context.Context, req DistillRequest) ([]Candidate, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	resultText := strings.ToLower(req.Result)
	planText := strings.ToLower(planSearchText(req.Plan))
	out := make([]Candidate, 0, len(d))
	for _, rule := range d {
		if !containsFold(resultText, rule.WhenResultContains) {
			continue
		}
		if !containsFold(planText, rule.WhenPlanContains) {
			continue
		}
		out = append(out, Candidate{
			Memory:     clone(rule.Memory),
			Reason:     strings.TrimSpace(rule.Reason),
			Confidence: rule.Confidence,
		})
	}
	return out, nil
}

func containsFold(haystackLower string, needle string) bool {
	needle = strings.ToLower(strings.TrimSpace(needle))
	return needle == "" || strings.Contains(haystackLower, needle)
}

func planSearchText(plan planner.Plan) string {
	var b strings.Builder
	b.WriteString(plan.Goal)
	b.WriteByte(' ')
	b.WriteString(string(plan.State))
	for _, constraint := range plan.Constraints {
		b.WriteByte(' ')
		b.WriteString(constraint)
	}
	for _, step := range plan.Steps {
		b.WriteByte(' ')
		b.WriteString(step.ID)
		b.WriteByte(' ')
		b.WriteString(step.Title)
		b.WriteByte(' ')
		b.WriteString(string(step.Status))
		b.WriteByte(' ')
		b.WriteString(step.Notes)
		for _, evidence := range step.Evidence {
			b.WriteByte(' ')
			b.WriteString(evidence)
		}
		for _, hint := range step.ToolHints {
			b.WriteByte(' ')
			b.WriteString(hint)
		}
	}
	return b.String()
}

// CloneCandidates returns defensive copies of memory candidates.
func CloneCandidates(candidates []Candidate) []Candidate {
	if len(candidates) == 0 {
		return nil
	}
	out := make([]Candidate, len(candidates))
	for i, candidate := range candidates {
		out[i] = candidate
		out[i].Memory = clone(candidate.Memory)
	}
	return out
}
