package tasktools

import (
	"context"
	"fmt"
	"strings"

	"github.com/MemaxLabs/memax-go-agent-sdk/model"
	"github.com/MemaxLabs/memax-go-agent-sdk/planner"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/subagents"
)

// SubagentPlanner returns a subagent plan source that scopes child-agent plan
// context to the task ID supplied in the subagent tool call. Task evidence is
// included in the scoped plan; global tool and verification hints can be added
// with planner.TaskSourceOption values.
func SubagentPlanner(store ProgressStore, opts ...planner.TaskSourceOption) subagents.PlanSource {
	return subagentPlanner{store: store, opts: append([]planner.TaskSourceOption(nil), opts...)}
}

type subagentPlanner struct {
	store ProgressStore
	opts  []planner.TaskSourceOption
}

func (p subagentPlanner) SubagentPlan(ctx context.Context, req subagents.PlanRequest) (planner.Plan, error) {
	taskID := strings.TrimSpace(req.TaskID)
	if taskID == "" || p.store == nil {
		return planner.Plan{}, nil
	}
	task, ok, err := findTask(ctx, p.store, taskID)
	if err != nil {
		return planner.Plan{}, err
	}
	if !ok {
		return planner.Plan{}, fmt.Errorf("task not found: %s", taskID)
	}
	source := planner.TaskSourceFunc(func(context.Context, planner.Request) ([]planner.Task, error) {
		return []planner.Task{{
			ID:       task.ID,
			Title:    task.Title,
			Status:   planner.Status(task.Status),
			Notes:    task.Notes,
			Priority: task.Priority,
			Evidence: append([]string(nil), task.Evidence...),
		}}, nil
	})
	opts := append([]planner.TaskSourceOption{
		planner.WithTaskGoal("complete delegated task " + task.ID),
	}, p.opts...)
	return planner.FromTaskSource(source, opts...).Prepare(ctx, planner.Request{Query: req.Prompt})
}

// SubagentProgressOption configures NewSubagentProgressHandler.
type SubagentProgressOption func(*subagentProgressConfig)

// WithSubagentSuccessStatus sets the task status used when a child agent
// returns a successful result. The default is completed. An empty status
// disables success updates.
func WithSubagentSuccessStatus(status Status) SubagentProgressOption {
	return func(c *subagentProgressConfig) {
		c.successStatus = status
	}
}

// WithSubagentFailureStatus sets the task status used when a child agent
// returns an error result. The default is blocked. An empty status disables
// failure updates.
func WithSubagentFailureStatus(status Status) SubagentProgressOption {
	return func(c *subagentProgressConfig) {
		c.failureStatus = status
	}
}

// NewSubagentProgressHandler returns a subagent result handler that updates
// task state for delegated runs with a task_id. Handler errors are surfaced by
// the subagent tool as model.MetadataTaskProgressError metadata.
func NewSubagentProgressHandler(store ProgressStore, opts ...SubagentProgressOption) subagents.ResultHandler {
	config := subagentProgressConfig{
		store:         store,
		successStatus: StatusCompleted,
		failureStatus: StatusBlocked,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(&config)
		}
	}
	return subagentProgressHandler{config: config}
}

type subagentProgressConfig struct {
	store         ProgressStore
	successStatus Status
	failureStatus Status
}

type subagentProgressHandler struct {
	config subagentProgressConfig
}

func (h subagentProgressHandler) HandleSubagentResult(ctx context.Context, req subagents.ResultRequest) (map[string]any, error) {
	taskID := strings.TrimSpace(req.TaskID)
	if taskID == "" || h.config.store == nil {
		return nil, nil
	}
	status := h.config.successStatus
	if req.IsError {
		status = h.config.failureStatus
	}
	if status == "" {
		return nil, nil
	}
	if !isValidStatus(status) {
		return nil, fmt.Errorf("invalid subagent progress status: %s", status)
	}
	if err := updateTaskFromSubagent(ctx, h.config.store, taskID, status, req); err != nil {
		return nil, err
	}
	evidence := subagentEvidence(req)
	return map[string]any{
		model.MetadataTaskID:       taskID,
		model.MetadataTaskStatus:   string(status),
		model.MetadataTaskEvidence: evidence,
	}, nil
}

func updateTaskFromSubagent(ctx context.Context, store ProgressStore, taskID string, status Status, req subagents.ResultRequest) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	task, ok, err := findTask(ctx, store, taskID)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("task not found: %s", taskID)
	}
	task.Status = status
	task.Notes = appendTaskNote(task.Notes, subagentNote(req))
	task.Evidence = mergeStrings(task.Evidence, subagentEvidence(req))
	if _, err := store.Upsert(ctx, task); err != nil {
		return fmt.Errorf("update task progress: %w", err)
	}
	return nil
}

func subagentNote(req subagents.ResultRequest) string {
	status := "completed"
	if req.IsError {
		status = "failed"
	}
	name := strings.TrimSpace(req.Agent)
	if name == "" {
		name = "subagent"
	}
	return fmt.Sprintf("subagent %s %s", name, status)
}

func subagentEvidence(req subagents.ResultRequest) []string {
	values := []string{}
	if agent := strings.TrimSpace(req.Agent); agent != "" {
		values = append(values, "subagent:"+agent)
	}
	if child := strings.TrimSpace(req.ChildSessionID); child != "" {
		values = append(values, "child_session:"+child)
	}
	return mergeStrings(nil, values)
}
