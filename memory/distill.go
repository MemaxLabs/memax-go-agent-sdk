package memory

import (
	"context"
	"errors"
	"fmt"
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

// DistillRequest gives distillers completed run context. Messages contain the
// agent's active model-visible transcript for the run, including any persisted
// compaction checkpoint plus newer raw messages. Model-backed distillers may
// still apply their own context budgeting before sending it to a model.
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

// CandidateRequest gives a host memory candidate handler the completed run
// context and the candidates proposed by a Distiller.
type CandidateRequest struct {
	SessionID       string
	ParentSessionID string
	Identity        identity.Identity
	Messages        []model.Message
	Plan            planner.Plan
	Result          string
	Candidates      []Candidate
}

// CandidateHandler handles proposed durable memories after they have been
// emitted to callers. Agent runs pass defensive copies to handlers, so handlers
// may mutate the request without affecting the completed transcript or emitted
// events. Handlers are opt-in; the SDK never persists candidates unless the
// host configures a handler.
type CandidateHandler interface {
	HandleCandidates(context.Context, CandidateRequest) error
}

// CandidateHandlerFunc adapts a function to CandidateHandler. A nil function
// is a no-op, making optional handler plumbing safe.
type CandidateHandlerFunc func(context.Context, CandidateRequest) error

// HandleCandidates calls f(ctx, req). A nil CandidateHandlerFunc is a no-op.
func (f CandidateHandlerFunc) HandleCandidates(ctx context.Context, req CandidateRequest) error {
	if f == nil {
		return nil
	}
	return f(ctx, req)
}

// WriterHandler persists accepted candidates with a memory Writer using
// best-effort semantics. If one write fails, later candidates are still
// attempted and the returned error joins all write failures; partial writes are
// therefore possible. Use a custom CandidateHandler for transactional or
// approval-gated persistence. By default it writes every candidate. Set
// MinConfidence or Scopes to narrow what is accepted before writing.
type WriterHandler struct {
	Writer        Writer
	MinConfidence float64
	Scopes        []Scope
}

// HandleCandidates writes accepted candidates to h.Writer.
func (h WriterHandler) HandleCandidates(ctx context.Context, req CandidateRequest) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	if h.Writer == nil {
		return fmt.Errorf("memory: nil candidate writer")
	}
	allowedScopes := scopeSet(h.Scopes)
	var errs []error
	for _, candidate := range req.Candidates {
		if h.MinConfidence > 0 && candidate.Confidence < h.MinConfidence {
			continue
		}
		item := clone(candidate.Memory)
		if len(allowedScopes) > 0 {
			if _, ok := allowedScopes[normalizeScope(item.Scope)]; !ok {
				continue
			}
		}
		_, err := h.Writer.PutMemory(ctx, PutRequest{
			SessionID:       req.SessionID,
			ParentSessionID: req.ParentSessionID,
			Identity:        req.Identity,
			Memory:          item,
		})
		if err != nil {
			errs = append(errs, fmt.Errorf("write memory candidate %q: %w", item.Name, err))
		}
	}
	return errors.Join(errs...)
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

func scopeSet(scopes []Scope) map[Scope]struct{} {
	if len(scopes) == 0 {
		return nil
	}
	out := make(map[Scope]struct{}, len(scopes))
	for _, scope := range scopes {
		out[normalizeScope(scope)] = struct{}{}
	}
	return out
}
