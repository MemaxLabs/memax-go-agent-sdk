// Package planner defines source-neutral planning policies for agent runs.
package planner

import (
	"context"
	"strings"

	"github.com/MemaxLabs/memax-go-agent-sdk/identity"
	"github.com/MemaxLabs/memax-go-agent-sdk/model"
)

// Policy prepares host-owned plan context for an agent turn.
//
// Query calls Prepare for each model turn so planners can reflect evolving
// host state, such as task progress, approvals, or external planning systems.
// Implementations that call remote services should cache, prefetch, or apply
// their own timeout policy when per-turn freshness is not required.
type Policy interface {
	Prepare(context.Context, Request) (Plan, error)
}

// PolicyFunc adapts a function to Policy.
type PolicyFunc func(context.Context, Request) (Plan, error)

// Prepare calls f(ctx, req). A nil PolicyFunc returns an empty plan.
func (f PolicyFunc) Prepare(ctx context.Context, req Request) (Plan, error) {
	if f == nil {
		return Plan{}, nil
	}
	return f(ctx, req)
}

// Request is the input passed to a planning policy.
type Request struct {
	SessionID       string
	ParentSessionID string
	Identity        identity.Identity
	Messages        []model.Message
	Query           string
}

// Plan is host-provided task strategy and progress state for the next model
// request.
type Plan struct {
	Goal        string
	Steps       []Step
	Constraints []string
	State       State
}

// Empty reports whether plan contains no promptable planning context.
func (p Plan) Empty() bool {
	return strings.TrimSpace(p.Goal) == "" &&
		len(p.Steps) == 0 &&
		len(p.Constraints) == 0 &&
		p.State == ""
}

// Step is one planned unit of work.
type Step struct {
	ID        string
	Title     string
	Status    Status
	Evidence  []string
	ToolHints []string
}

// Status is the progress state for one plan step.
type Status string

const (
	StatusPending    Status = "pending"
	StatusInProgress Status = "in_progress"
	StatusCompleted  Status = "completed"
	StatusBlocked    Status = "blocked"
	StatusCanceled   Status = "canceled"
)

// State is the overall plan state.
type State string

const (
	StateActive    State = "active"
	StateCompleted State = "completed"
	StateBlocked   State = "blocked"
	StateCanceled  State = "canceled"
)

// Static returns a policy that always provides plan.
func Static(plan Plan) Policy {
	return staticPolicy{plan: clonePlan(plan)}
}

type staticPolicy struct {
	plan Plan
}

func (p staticPolicy) Prepare(ctx context.Context, _ Request) (Plan, error) {
	if err := ctx.Err(); err != nil {
		return Plan{}, err
	}
	return clonePlan(p.plan), nil
}

func clonePlan(plan Plan) Plan {
	plan.Constraints = append([]string(nil), plan.Constraints...)
	plan.Steps = cloneSteps(plan.Steps)
	return plan
}

func cloneSteps(steps []Step) []Step {
	if len(steps) == 0 {
		return nil
	}
	out := make([]Step, len(steps))
	for i, step := range steps {
		out[i] = step
		out[i].Evidence = append([]string(nil), step.Evidence...)
		out[i].ToolHints = append([]string(nil), step.ToolHints...)
	}
	return out
}
