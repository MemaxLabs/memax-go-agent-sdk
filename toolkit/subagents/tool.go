package subagents

import (
	"context"
	"fmt"
	"time"

	memaxagent "github.com/MemaxLabs/memax-go-agent-sdk"
	"github.com/MemaxLabs/memax-go-agent-sdk/memory"
	"github.com/MemaxLabs/memax-go-agent-sdk/model"
	"github.com/MemaxLabs/memax-go-agent-sdk/skill"
	"github.com/MemaxLabs/memax-go-agent-sdk/tool"
)

const (
	defaultToolName       = "run_subagent"
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
	MaxPromptBytes int
	MaxResultBytes int
}

type subagentTool struct {
	spec           model.ToolSpec
	agents         map[string]Agent
	order          []string
	defaultOptions memaxagent.Options
	maxPromptBytes int
}

type input struct {
	Agent  string `json:"agent"`
	Prompt string `json:"prompt"`
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
		name = defaultToolName
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

	opts := mergeOptions(t.defaultOptions, agent.Options)
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
			Metadata: metadata(agent.Name, call.Runtime.SessionID, ""),
		}, nil
	}

	var childSessionID string
	for event := range events {
		if childSessionID == "" && event.SessionID != "" {
			childSessionID = event.SessionID
		}
		switch event.Kind {
		case memaxagent.EventResult:
			return model.ToolResult{
				Content:  event.Result,
				Metadata: metadata(agent.Name, call.Runtime.SessionID, childSessionID),
			}, nil
		case memaxagent.EventError:
			return model.ToolResult{
				Content:  fmt.Sprintf("subagent %q failed: %v", agent.Name, event.Err),
				IsError:  true,
				Metadata: metadata(agent.Name, call.Runtime.SessionID, childSessionID),
			}, nil
		}
	}

	return model.ToolResult{
		Content:  fmt.Sprintf("subagent %q ended without a result", agent.Name),
		IsError:  true,
		Metadata: metadata(agent.Name, call.Runtime.SessionID, childSessionID),
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

func mergeOptions(base memaxagent.Options, override memaxagent.Options) memaxagent.Options {
	out := base
	if override.Model != nil {
		out.Model = override.Model
	}
	if override.Tools != nil {
		out.Tools = override.Tools
	}
	if override.Permissions != nil {
		out.Permissions = override.Permissions
	}
	if override.Sessions != nil {
		out.Sessions = override.Sessions
	}
	if override.Hooks != nil {
		out.Hooks = override.Hooks
	}
	if override.Context != nil {
		out.Context = override.Context
	}
	if override.ContextRetry != nil {
		out.ContextRetry = override.ContextRetry
	}
	if override.ToolSelector != nil {
		out.ToolSelector = override.ToolSelector
	}
	if override.ResultStore != nil {
		out.ResultStore = override.ResultStore
	}
	if override.Tracer != nil {
		out.Tracer = override.Tracer
	}
	if override.Meter != nil {
		out.Meter = override.Meter
	}
	if override.PromptBuilder != nil {
		out.PromptBuilder = override.PromptBuilder
	}
	if override.PromptProfile != "" {
		out.PromptProfile = override.PromptProfile
	}
	if !override.Identity.IsZero() {
		out.Identity = override.Identity
	}
	if override.MemorySource != nil {
		out.MemorySource = override.MemorySource
	}
	if len(override.Memories) != 0 {
		out.Memories = append([]memory.Memory(nil), override.Memories...)
	}
	if override.SkillSource != nil {
		out.SkillSource = override.SkillSource
	}
	if len(override.Skills) != 0 {
		out.Skills = append([]skill.Skill(nil), override.Skills...)
	}
	if override.SystemPrompt != "" {
		out.SystemPrompt = override.SystemPrompt
	}
	if override.AppendSystemPrompt != "" {
		out.AppendSystemPrompt = override.AppendSystemPrompt
	}
	if override.SessionID != "" {
		out.SessionID = override.SessionID
	}
	if override.ParentSessionID != "" {
		out.ParentSessionID = override.ParentSessionID
	}
	if override.MaxTurns != 0 {
		out.MaxTurns = override.MaxTurns
	}
	if override.MaxToolConcurrency != 0 {
		out.MaxToolConcurrency = override.MaxToolConcurrency
	}
	if override.MaxRunDuration != 0 {
		out.MaxRunDuration = override.MaxRunDuration
	}
	return out
}

func metadata(agent string, parentSessionID string, childSessionID string) map[string]any {
	out := map[string]any{"agent": agent}
	if parentSessionID != "" {
		out["parent_session_id"] = parentSessionID
	}
	if childSessionID != "" {
		out["child_session_id"] = childSessionID
	}
	return out
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
			"agent":  agentProperty,
			"prompt": map[string]any{"type": "string", "minLength": 1},
		},
	}
}
