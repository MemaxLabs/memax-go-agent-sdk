package tasktools

import (
	"context"

	"github.com/MemaxLabs/memax-go-agent-sdk/planner"
)

// Planner adapts a task Store into a planner.Policy. The policy reads the
// store on each model turn, so task mutations made through task tools are
// reflected in the next prompt.
func Planner(store Store, opts ...planner.TaskSourceOption) planner.Policy {
	return planner.FromTaskSource(taskSource{store: store}, opts...)
}

type taskSource struct {
	store Store
}

func (s taskSource) Tasks(ctx context.Context, _ planner.Request) ([]planner.Task, error) {
	if s.store == nil {
		return nil, nil
	}
	tasks, err := s.store.List(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]planner.Task, 0, len(tasks))
	for _, task := range tasks {
		// tasktools.Task intentionally carries only operational task state.
		// Planner evidence, tool hints, and verification hints come from
		// planner options or custom planner.TaskSource implementations.
		out = append(out, planner.Task{
			ID:       task.ID,
			Title:    task.Title,
			Status:   planner.Status(task.Status),
			Notes:    task.Notes,
			Priority: task.Priority,
		})
	}
	return out, nil
}
