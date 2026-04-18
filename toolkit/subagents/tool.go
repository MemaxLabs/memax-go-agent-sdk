package subagents

import (
	"context"
	"fmt"
	"time"

	memaxagent "github.com/MemaxLabs/memax-go-agent-sdk"
	"github.com/MemaxLabs/memax-go-agent-sdk/model"
	"github.com/MemaxLabs/memax-go-agent-sdk/planner"
	"github.com/MemaxLabs/memax-go-agent-sdk/tool"
)

const (
	// ToolName is the default tool name used for bounded child-agent runs.
	ToolName              = "run_subagent"
	defaultMaxPromptBytes = 64 * 1024
	defaultMaxTurns       = 8
	defaultRunDuration    = 2 * time.Minute
)

type Agent struct {
	Name        string
	Description string
	Options     memaxagent.Options
}

type Config struct {
	Name           string
	Description    string
	Agents         []Agent
	DefaultOptions memaxagent.Options
	PlanSource     PlanSource
	ResultHandler  ResultHandler
	MaxPromptBytes int
	MaxResultBytes int
}

// PlanRequest describes the scoped work item a child agent is about to run.
type PlanRequest struct {
	Agent           string
	TaskID          string
	Prompt          string
	ParentSessionID string
}

// PlanSource prepares scoped child-agent plan context. Hosts can use this to
// give a subagent only the task step, evidence, and verification hints relevant
// to its delegated work.
type PlanSource interface {
	SubagentPlan(context.Context, PlanRequest) (planner.Plan, error)
}

// PlanSourceFunc adapts a function to PlanSource.
type PlanSourceFunc func(context.Context, PlanRequest) (planner.Plan, error)

// SubagentPlan calls f(ctx, req). A nil PlanSourceFunc returns an empty plan.
func (f PlanSourceFunc) SubagentPlan(ctx context.Context, req PlanRequest) (planner.Plan, error) {
	if f == nil {
		return planner.Plan{}, nil
	}
	return f(ctx, req)
}

// ResultRequest describes the completed child-agent run passed to an optional
// ResultHandler.
type ResultRequest struct {
	Agent           string
	TaskID          string
	Prompt          string
	ParentSessionID string
	ChildSessionID  string
	Result          string
	IsError         bool
}

// ResultHandler can attach metadata or update host-owned progress state from a
// child-agent result. Handler errors are reported in tool result metadata, not
// returned as tool execution errors.
type ResultHandler interface {
	HandleSubagentResult(context.Context, ResultRequest) (map[string]any, error)
}

// ResultHandlerFunc adapts a function to ResultHandler.
type ResultHandlerFunc func(context.Context, ResultRequest) (map[string]any, error)

// HandleSubagentResult calls f(ctx, req). A nil ResultHandlerFunc is a no-op.
func (f ResultHandlerFunc) HandleSubagentResult(ctx context.Context, req ResultRequest) (map[string]any, error) {
	if f == nil {
		return nil, nil
	}
	return f(ctx, req)
}

type subagentTool struct {
	spec           model.ToolSpec
	agents         map[string]Agent
	order          []string
	defaultOptions memaxagent.Options
	planSource     PlanSource
	resultHandler  ResultHandler
	maxPromptBytes int
}

type input struct {
	Agent  string `json:"agent"`
	Prompt string `json:"prompt"`
	TaskID string `json:"task_id"`
}

func NewTool(config Config) (tool.Tool, error) {
	if len(config.Agents) == 0 {
		return nil, fmt.Errorf("subagents: at least one agent is required")
	}

	agents := make(map[string]Agent, len(config.Agents))
	order := make([]string, 0, len(config.Agents))
	for _, agent := range config.Agents {
		if agent.Name == "" {
			return nil, fmt.Errorf("subagents: agent name is required")
		}
		if _, ok := agents[agent.Name]; ok {
			return nil, fmt.Errorf("subagents: duplicate agent %q", agent.Name)
		}
		agents[agent.Name] = agent
		order = append(order, agent.Name)
	}

	name := config.Name
	if name == "" {
		name = ToolName
	}
	description := config.Description
	if description == "" {
		description = "Run a bounded worker agent in a child session."
	}
	maxPromptBytes := config.MaxPromptBytes
	if maxPromptBytes <= 0 {
		maxPromptBytes = defaultMaxPromptBytes
	}

	return &subagentTool{
		spec: model.ToolSpec{
			Name:            name,
			Description:     description,
			InputSchema:     inputSchema(order),
			ConcurrencySafe: true,
			Destructive:     true,
			MaxResultBytes:  config.MaxResultBytes,
		},
		agents:         agents,
		order:          order,
		defaultOptions: config.DefaultOptions,
		planSource:     config.PlanSource,
		resultHandler:  config.ResultHandler,
		maxPromptBytes: maxPromptBytes,
	}, nil
}

func (t *subagentTool) Spec() model.ToolSpec {
	return t.spec
}

func (t *subagentTool) CanRunConcurrently(model.ToolUse) bool {
	return true
}

func (t *subagentTool) Execute(ctx context.Context, call tool.Call) (model.ToolResult, error) {
	in, err := tool.DecodeInput[input](call.Use)
	if err != nil {
		return model.ToolResult{}, err
	}
	if in.Prompt == "" {
		return model.ToolResult{}, fmt.Errorf("subagents: prompt is required")
	}
	if len(in.Prompt) > t.maxPromptBytes {
		return model.ToolResult{}, fmt.Errorf("subagents: prompt is %d bytes, max is %d", len(in.Prompt), t.maxPromptBytes)
	}

	agent, err := t.agent(in.Agent)
	if err != nil {
		return model.ToolResult{}, err
	}

	opts := t.defaultOptions.Merge(agent.Options)
	if t.planSource != nil && in.TaskID != "" {
		plan, err := t.planSource.SubagentPlan(ctx, PlanRequest{
			Agent:           agent.Name,
			TaskID:          in.TaskID,
			Prompt:          in.Prompt,
			ParentSessionID: call.Runtime.SessionID,
		})
		if err != nil {
			return model.ToolResult{
				Content:  fmt.Sprintf("subagent %q failed to prepare scoped plan: %v", agent.Name, err),
				IsError:  true,
				Metadata: t.metadata(agent.Name, call.Runtime.SessionID, "", in.TaskID),
			}, nil
		}
		if !plan.Empty() {
			opts.Planner = planner.Static(plan)
		}
	}
	if opts.Sessions == nil {
		opts.Sessions = call.Runtime.Sessions
	}
	opts.ParentSessionID = call.Runtime.SessionID
	if opts.MaxTurns <= 0 {
		opts.MaxTurns = defaultMaxTurns
	}
	if opts.MaxRunDuration <= 0 {
		opts.MaxRunDuration = defaultRunDuration
	}

	events, err := memaxagent.Query(ctx, in.Prompt, opts)
	if err != nil {
		return model.ToolResult{
			Content:  fmt.Sprintf("subagent %q failed to start: %v", agent.Name, err),
			IsError:  true,
			Metadata: t.metadata(agent.Name, call.Runtime.SessionID, "", in.TaskID),
		}, nil
	}

	var childSessionID string
	for event := range events {
		if childSessionID == "" && event.SessionID != "" {
			childSessionID = event.SessionID
		}
		switch event.Kind {
		case memaxagent.EventResult:
			metadata := t.metadata(agent.Name, call.Runtime.SessionID, childSessionID, in.TaskID)
			t.handleResult(ctx, metadata, ResultRequest{
				Agent:           agent.Name,
				TaskID:          in.TaskID,
				Prompt:          in.Prompt,
				ParentSessionID: call.Runtime.SessionID,
				ChildSessionID:  childSessionID,
				Result:          event.Result,
			})
			return model.ToolResult{
				Content:  event.Result,
				Metadata: metadata,
			}, nil
		case memaxagent.EventError:
			metadata := t.metadata(agent.Name, call.Runtime.SessionID, childSessionID, in.TaskID)
			t.handleResult(ctx, metadata, ResultRequest{
				Agent:           agent.Name,
				TaskID:          in.TaskID,
				Prompt:          in.Prompt,
				ParentSessionID: call.Runtime.SessionID,
				ChildSessionID:  childSessionID,
				Result:          fmt.Sprint(event.Err),
				IsError:         true,
			})
			return model.ToolResult{
				Content:  fmt.Sprintf("subagent %q failed: %v", agent.Name, event.Err),
				IsError:  true,
				Metadata: metadata,
			}, nil
		}
	}

	metadata := t.metadata(agent.Name, call.Runtime.SessionID, childSessionID, in.TaskID)
	t.handleResult(ctx, metadata, ResultRequest{
		Agent:           agent.Name,
		TaskID:          in.TaskID,
		Prompt:          in.Prompt,
		ParentSessionID: call.Runtime.SessionID,
		ChildSessionID:  childSessionID,
		Result:          "ended without a result",
		IsError:         true,
	})
	return model.ToolResult{
		Content:  fmt.Sprintf("subagent %q ended without a result", agent.Name),
		IsError:  true,
		Metadata: metadata,
	}, nil
}

func (t *subagentTool) agent(name string) (Agent, error) {
	if name == "" && len(t.order) == 1 {
		return t.agents[t.order[0]], nil
	}
	agent, ok := t.agents[name]
	if !ok {
		return Agent{}, fmt.Errorf("subagents: unknown agent %q", name)
	}
	return agent, nil
}

func (t *subagentTool) metadata(agent string, parentSessionID string, childSessionID string, taskID string) map[string]any {
	out := map[string]any{"agent": agent}
	if parentSessionID != "" {
		out["parent_session_id"] = parentSessionID
	}
	if childSessionID != "" {
		out["child_session_id"] = childSessionID
	}
	if taskID != "" {
		out[model.MetadataTaskID] = taskID
	}
	return out
}

func (t *subagentTool) handleResult(ctx context.Context, metadata map[string]any, req ResultRequest) {
	if t.resultHandler == nil || req.TaskID == "" {
		return
	}
	extra, err := t.resultHandler.HandleSubagentResult(ctx, req)
	for key, value := range extra {
		metadata[key] = value
	}
	if err != nil {
		metadata[model.MetadataTaskProgressError] = err.Error()
	}
}

func inputSchema(agentNames []string) map[string]any {
	agentValues := make([]any, 0, len(agentNames))
	for _, name := range agentNames {
		agentValues = append(agentValues, name)
	}
	required := []any{"prompt"}
	if len(agentNames) > 1 {
		required = append(required, "agent")
	}
	agentProperty := map[string]any{
		"type":        "string",
		"description": "Worker agent to run.",
	}
	if len(agentValues) > 0 {
		agentProperty["enum"] = agentValues
	}
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             required,
		"properties": map[string]any{
			"agent":   agentProperty,
			"prompt":  map[string]any{"type": "string", "minLength": 1},
			"task_id": map[string]any{"type": "string", "description": "Optional host task ID this subagent run should scope and report progress against."},
		},
	}
}
