package personal

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	memaxagent "github.com/MemaxLabs/memax-go-agent-sdk"
	"github.com/MemaxLabs/memax-go-agent-sdk/memory"
	"github.com/MemaxLabs/memax-go-agent-sdk/model"
	"github.com/MemaxLabs/memax-go-agent-sdk/planner"
	"github.com/MemaxLabs/memax-go-agent-sdk/skill"
	"github.com/MemaxLabs/memax-go-agent-sdk/tool"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/agentpolicy"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/approvaltools"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/memorytools"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/subagents"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/tasktools"
)

func TestNewAssemblesPersonalRuntime(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	memories := memory.NewMemoryStore([]memory.Memory{{
		ID:      "memory-1",
		Name:    "note-style",
		Scope:   memory.ScopeUser,
		Content: "User prefers concise meeting notes.",
	}})
	tasks := tasktools.NewMemoryStore([]tasktools.Task{{
		ID:     "task-1",
		Title:  "prepare meeting brief",
		Status: tasktools.StatusInProgress,
	}})
	approver := approvaltools.StaticApprover{
		Decision: approvaltools.Decision{
			Approved: true,
			Reason:   "approved durable memory update",
		},
	}

	stack, err := New(Config{
		Memory: memorytools.Config{
			Source:  memories,
			Writer:  memories,
			Deleter: memories,
		},
		Tasks: tasks,
		SkillSource: skill.StaticSource{{
			Name:        "meeting-preferences",
			Description: "Track meeting style preferences.",
			Content:     "Use concise meeting summaries.",
		}},
		SkillResourceSource: skill.StaticResourceSource{{
			SkillName: "meeting-preferences",
			Name:      "meeting-checklist",
			Content:   "prepare agenda and outcomes",
		}},
		Approval: approvaltools.Config{
			Approver: approver,
		},
		Subagents: &subagents.Config{
			Agents: []subagents.Agent{{
				Name:        "researcher",
				Description: "Investigates focused personal questions.",
				Options:     memaxagent.Options{},
			}},
		},
		Policies: Policies{
			RequireMemoryApproval:     true,
			RequireDelegationApproval: true,
			SingleUseApprovals:        true,
			InputBoundApprovals:       true,
		},
		SkillDisclosure: skill.DisclosureProgressive,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	opts := stack.Options()
	if opts.Tools == nil {
		t.Fatal("stack options missing tool registry")
	}
	if opts.Hooks == nil {
		t.Fatal("stack options missing hook runner")
	}
	if opts.Planner == nil {
		t.Fatal("stack options missing planner")
	}
	if opts.MemorySource == nil {
		t.Fatal("stack options missing prompt memory source")
	}
	if opts.SkillSource == nil || opts.SkillResourceSource == nil {
		t.Fatal("stack options missing skill sources")
	}
	if opts.SkillDisclosure != skill.DisclosureProgressive {
		t.Fatalf("skill disclosure = %q, want %q", opts.SkillDisclosure, skill.DisclosureProgressive)
	}

	specNames := toolNames(opts.Tools)
	for _, want := range []string{
		memorytools.SearchToolName,
		memorytools.SaveToolName,
		memorytools.DeleteToolName,
		tasktools.ListToolName,
		tasktools.UpsertToolName,
		tasktools.DeleteToolName,
		approvaltools.ToolName,
		subagents.ToolName,
	} {
		if !contains(specNames, want) {
			t.Fatalf("assembled registry missing %q; got %v", want, specNames)
		}
	}

	exec := tool.Executor{
		Registry: opts.Tools,
		Hooks:    opts.Hooks,
		Runtime: tool.Runtime{
			SessionID: "session-1",
			Sessions:  opts.Sessions,
		},
	}

	saveInput := map[string]any{
		"name":    "meeting-summary-style",
		"scope":   "user",
		"content": "Keep personal meeting summaries short and action-oriented.",
	}
	result := runTool(t, exec, memorytools.SaveToolName, saveInput)
	if !result.IsError || !strings.Contains(result.Content, agentpolicy.ApprovalBeforeToolReason(memorytools.SaveToolName)) {
		t.Fatalf("expected approval denial, got error=%v content=%q", result.IsError, result.Content)
	}

	result = runTool(t, exec, subagents.ToolName, map[string]any{
		"agent":   "researcher",
		"prompt":  "prepare a short meeting brief",
		"task_id": "task-1",
	})
	if !result.IsError || !strings.Contains(result.Content, agentpolicy.ApprovalBeforeToolReason(subagents.ToolName)) {
		t.Fatalf("expected delegation approval denial, got error=%v content=%q", result.IsError, result.Content)
	}

	result = runTool(t, exec, approvaltools.ToolName, map[string]any{
		"action":     memorytools.SaveToolName,
		"reason":     "save durable meeting preference",
		"tool_input": saveInput,
	})
	if result.IsError {
		t.Fatalf("approval should succeed: %s", result.Content)
	}

	result = runTool(t, exec, memorytools.SaveToolName, saveInput)
	if result.IsError {
		t.Fatalf("approved save should succeed: %s", result.Content)
	}

	allMemories, err := memories.Memories(ctx, memory.Request{})
	if err != nil {
		t.Fatalf("Memories() error = %v", err)
	}
	if len(allMemories) < 2 {
		t.Fatalf("memory count = %d, want at least 2", len(allMemories))
	}

	plan, err := opts.Planner.Prepare(ctx, planner.Request{})
	if err != nil {
		t.Fatalf("Planner.Prepare() error = %v", err)
	}
	if len(plan.Steps) != 1 {
		t.Fatalf("plan steps = %d, want 1", len(plan.Steps))
	}
	step := plan.Steps[0]
	for _, want := range []string{
		memorytools.SearchToolName,
		memorytools.SaveToolName,
		memorytools.DeleteToolName,
		tasktools.ListToolName,
		approvaltools.ToolName,
		subagents.ToolName,
		skill.LoadToolName,
		skill.ResourceToolName,
	} {
		if !contains(step.ToolHints, want) {
			t.Fatalf("step tool hints = %v, want %q", step.ToolHints, want)
		}
	}
}

func TestPersonalAssistantBuildsWithoutSkills(t *testing.T) {
	t.Parallel()

	stack, err := New(PersonalAssistant())
	if err != nil {
		t.Fatalf("New(PersonalAssistant()) error = %v", err)
	}
	opts := stack.Options()
	if opts.SkillDisclosure != skill.DisclosureProgressive {
		t.Fatalf("skill disclosure = %q, want %q", opts.SkillDisclosure, skill.DisclosureProgressive)
	}
	for _, spec := range opts.Tools.Specs() {
		if spec.Name == skill.LoadToolName || spec.Name == skill.ResourceToolName {
			t.Fatalf("registry unexpectedly included skill load tools without a skill source: %v", opts.Tools.Specs())
		}
	}
}

func TestNewRejectsApprovalGatesWithoutApprover(t *testing.T) {
	t.Parallel()

	_, err := New(Config{
		Memory: memorytools.Config{
			Writer: memory.NewMemoryStore(nil),
		},
		Policies: Policies{
			RequireMemoryApproval: true,
		},
	})
	if err == nil || !strings.Contains(err.Error(), "memory approval requires approval approver") {
		t.Fatalf("New() error = %v, want memory approval validation", err)
	}

	_, err = New(Config{
		Subagents: &subagents.Config{
			Agents: []subagents.Agent{{Name: "researcher", Description: "Investigates questions."}},
		},
		Policies: Policies{
			RequireDelegationApproval: true,
		},
	})
	if err == nil || !strings.Contains(err.Error(), "delegation approval requires approval approver") {
		t.Fatalf("New() error = %v, want delegation approval validation", err)
	}
}

func TestPresetsAndDefaultPolicies(t *testing.T) {
	t.Parallel()

	if got := Presets(); len(got) != 2 || got[0] != PresetPersonalAssistant || got[1] != PresetResearchPartner {
		t.Fatalf("Presets() = %v, want stable personal presets", got)
	}
	policies := DefaultPolicies()
	if !policies.RequireMemoryApproval || !policies.SingleUseApprovals || !policies.InputBoundApprovals || policies.RequireDelegationApproval {
		t.Fatalf("DefaultPolicies() = %#v, want memory approval on and delegation approval off", policies)
	}

	if _, err := PresetPersonalAssistant.Config(); err != nil {
		t.Fatalf("PresetPersonalAssistant.Config() error = %v", err)
	}
	if _, err := PresetResearchPartner.Config(); err != nil {
		t.Fatalf("PresetResearchPartner.Config() error = %v", err)
	}
	if _, err := Preset("unknown").Config(); err == nil {
		t.Fatal("unknown preset returned nil error")
	}
}

func runTool(t *testing.T, exec tool.Executor, name string, input map[string]any) model.ToolResult {
	t.Helper()
	payload, err := json.Marshal(input)
	if err != nil {
		t.Fatalf("Marshal(%s) error = %v", name, err)
	}
	results := exec.Run(context.Background(), []model.ToolUse{{
		ID:    name + "-1",
		Name:  name,
		Input: payload,
	}})
	var result []model.ToolResult
	for item := range results {
		result = append(result, item)
	}
	if len(result) != 1 {
		t.Fatalf("Run(%s) results = %d, want 1", name, len(result))
	}
	return result[0]
}

func toolNames(registry *tool.Registry) []string {
	specs := registry.Specs()
	names := make([]string, 0, len(specs))
	for _, spec := range specs {
		names = append(names, spec.Name)
	}
	return names
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
