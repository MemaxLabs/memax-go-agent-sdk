// Package planner defines source-neutral planning policies for agent runs.
package planner

import (
	"context"
	"fmt"
	"sort"
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
	Notes     string
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

// Task is source-neutral task state that can be converted into a Plan.
type Task struct {
	ID     string
	Title  string
	Status Status
	Notes  string
	// Priority orders task-derived steps. Lower positive values appear first;
	// zero means no priority preference and sorts after positive priorities.
	Priority  int
	Evidence  []string
	ToolHints []string
}

// TaskSource loads task state for one agent turn.
type TaskSource interface {
	Tasks(context.Context, Request) ([]Task, error)
}

// TaskSourceFunc adapts a function to TaskSource.
type TaskSourceFunc func(context.Context, Request) ([]Task, error)

// Tasks calls f(ctx, req). A nil TaskSourceFunc returns no tasks.
func (f TaskSourceFunc) Tasks(ctx context.Context, req Request) ([]Task, error) {
	if f == nil {
		return nil, nil
	}
	return f(ctx, req)
}

// TaskSourceOption configures FromTaskSource.
type TaskSourceOption func(*taskSourcePolicy)

// WithTaskGoal sets the plan goal for task-derived plans.
func WithTaskGoal(goal string) TaskSourceOption {
	return func(p *taskSourcePolicy) {
		p.goal = goal
	}
}

// WithTaskConstraints adds plan constraints for task-derived plans.
func WithTaskConstraints(constraints ...string) TaskSourceOption {
	return func(p *taskSourcePolicy) {
		p.constraints = append([]string(nil), constraints...)
	}
}

// WithTaskState sets the plan state for task-derived plans. When unset,
// FromTaskSource infers a state from task statuses.
func WithTaskState(state State) TaskSourceOption {
	return func(p *taskSourcePolicy) {
		p.state = state
	}
}

// WithTaskToolHints adds tool hints to every task-derived plan step.
func WithTaskToolHints(hints ...string) TaskSourceOption {
	return func(p *taskSourcePolicy) {
		p.toolHints = append([]string(nil), hints...)
	}
}

// FromTaskSource converts source-neutral task state into per-turn plan context.
func FromTaskSource(source TaskSource, opts ...TaskSourceOption) Policy {
	policy := taskSourcePolicy{source: source}
	for _, opt := range opts {
		if opt != nil {
			opt(&policy)
		}
	}
	return policy
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

type taskSourcePolicy struct {
	source      TaskSource
	goal        string
	constraints []string
	state       State
	toolHints   []string
}

func (p taskSourcePolicy) Prepare(ctx context.Context, req Request) (Plan, error) {
	if err := ctx.Err(); err != nil {
		return Plan{}, err
	}
	if p.source == nil {
		return Plan{}, nil
	}
	tasks, err := p.source.Tasks(ctx, req)
	if err != nil {
		return Plan{}, fmt.Errorf("load planner tasks: %w", err)
	}
	tasks = sortTasks(tasks)
	steps := make([]Step, 0, len(tasks))
	for _, task := range tasks {
		title := strings.TrimSpace(task.Title)
		id := strings.TrimSpace(task.ID)
		if title == "" && id == "" {
			continue
		}
		hints := append([]string(nil), p.toolHints...)
		hints = append(hints, task.ToolHints...)
		steps = append(steps, Step{
			ID:        id,
			Title:     title,
			Status:    normalizeStatus(task.Status),
			Notes:     strings.TrimSpace(task.Notes),
			Evidence:  append([]string(nil), task.Evidence...),
			ToolHints: hints,
		})
	}
	if len(steps) == 0 && strings.TrimSpace(p.goal) == "" && len(p.constraints) == 0 && p.state == "" {
		return Plan{}, nil
	}
	state := p.state
	if state == "" {
		state = inferState(tasks)
	}
	return Plan{
		Goal:        strings.TrimSpace(p.goal),
		Steps:       steps,
		Constraints: append([]string(nil), p.constraints...),
		State:       state,
	}, nil
}

func clonePlan(plan Plan) Plan {
	plan.Constraints = append([]string(nil), plan.Constraints...)
	plan.Steps = cloneSteps(plan.Steps)
	return plan
}

func sortTasks(tasks []Task) []Task {
	out := append([]Task(nil), tasks...)
	sort.SliceStable(out, func(i int, j int) bool {
		left := out[i]
		right := out[j]
		if left.Priority != right.Priority {
			if left.Priority == 0 {
				return false
			}
			if right.Priority == 0 {
				return true
			}
			return left.Priority < right.Priority
		}
		return left.ID < right.ID
	})
	return out
}

func normalizeStatus(status Status) Status {
	switch status {
	case StatusPending, StatusInProgress, StatusCompleted, StatusBlocked, StatusCanceled:
		return status
	default:
		return StatusPending
	}
}

func inferState(tasks []Task) State {
	if len(tasks) == 0 {
		return ""
	}
	allCompleted := true
	allCanceled := true
	for _, task := range tasks {
		switch normalizeStatus(task.Status) {
		case StatusBlocked:
			return StateBlocked
		case StatusCanceled:
			allCompleted = false
		case StatusCompleted:
			allCanceled = false
		default:
			allCompleted = false
			allCanceled = false
		}
	}
	if allCompleted {
		return StateCompleted
	}
	if allCanceled {
		return StateCanceled
	}
	return StateActive
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
