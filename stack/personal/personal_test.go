package personal

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	memaxagent "github.com/MemaxLabs/memax-go-agent-sdk"
	"github.com/MemaxLabs/memax-go-agent-sdk/memory"
	"github.com/MemaxLabs/memax-go-agent-sdk/messaging"
	"github.com/MemaxLabs/memax-go-agent-sdk/model"
	"github.com/MemaxLabs/memax-go-agent-sdk/notes"
	"github.com/MemaxLabs/memax-go-agent-sdk/planner"
	"github.com/MemaxLabs/memax-go-agent-sdk/scheduling"
	"github.com/MemaxLabs/memax-go-agent-sdk/skill"
	"github.com/MemaxLabs/memax-go-agent-sdk/tool"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/agentpolicy"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/approvaltools"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/memorytools"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/messagetools"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/notetools"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/scheduletools"
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
	noteStore := notes.NewNoteStore([]notes.Note{{
		ID:      "note-1",
		Title:   "meeting brief style",
		Kind:    "brief",
		Summary: "Template for concise meeting briefs",
		Content: "Use one short summary paragraph followed by owner and due-date bullets.",
	}})
	messageStore := messaging.NewThreadStore([]messaging.Thread{{
		ID:      "thread-1",
		Subject: "Project kickoff follow-up",
		Summary: "Alex asked for concise replies with owners and due dates.",
		Participants: []messaging.Participant{
			{Name: "Alex", Address: "alex@example.com"},
		},
		Messages: []messaging.Message{{
			ID:        "thread-1-msg-1",
			ThreadID:  "thread-1",
			Subject:   "Project kickoff follow-up",
			Summary:   "Keep replies concise.",
			Body:      "Please keep replies concise and include owners and due dates.",
			Direction: messaging.DirectionInbound,
			Sender:    messaging.Participant{Name: "Alex", Address: "alex@example.com"},
		}},
	}})
	start := time.Date(2026, 4, 20, 15, 0, 0, 0, time.UTC)
	scheduleStore := scheduling.NewEventStore([]scheduling.Event{{
		ID:       "event-1",
		Title:    "Project kickoff",
		Summary:  "Weekly kickoff with owners and due dates",
		Location: "Zoom",
		Organizer: scheduling.Participant{
			Name:    "Alex",
			Address: "alex@example.com",
		},
		Start:       start,
		End:         start.Add(time.Hour),
		TimeZone:    "UTC",
		Description: "Keep the meeting concise and end with owners and due dates.",
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
		Notes: notetools.Config{
			Searcher: noteStore,
			Reader:   noteStore,
			Writer:   noteStore,
			Deleter:  noteStore,
		},
		Messages: messagetools.Config{
			Searcher: messageStore,
			Reader:   messageStore,
			Sender:   messageStore,
		},
		Schedule: scheduletools.Config{
			Searcher:    scheduleStore,
			Reader:      scheduleStore,
			Creator:     scheduleStore,
			Rescheduler: scheduleStore,
			Canceller:   scheduleStore,
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
			RequireMemoryApproval:             true,
			RequireNoteApproval:               true,
			RequireMessageApproval:            true,
			RequireScheduleCreateApproval:     true,
			RequireScheduleRescheduleApproval: true,
			RequireScheduleCancelApproval:     true,
			RequireDelegationApproval:         true,
			SingleUseApprovals:                true,
			InputBoundApprovals:               true,
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
		notetools.SearchToolName,
		notetools.ReadToolName,
		notetools.SaveToolName,
		notetools.DeleteToolName,
		messagetools.SearchToolName,
		messagetools.ReadToolName,
		messagetools.SendToolName,
		scheduletools.SearchToolName,
		scheduletools.ReadToolName,
		scheduletools.CreateToolName,
		scheduletools.RescheduleToolName,
		scheduletools.CancelToolName,
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

	noteSaveInput := map[string]any{
		"title":   "meeting follow-up template",
		"kind":    "template",
		"summary": "Template for action-oriented follow-up notes",
		"content": "List owners and due dates in an action-oriented format.",
	}
	result = runTool(t, exec, notetools.SaveToolName, noteSaveInput)
	if !result.IsError || !strings.Contains(result.Content, agentpolicy.ApprovalBeforeToolReason(notetools.SaveToolName)) {
		t.Fatalf("expected note approval denial, got error=%v content=%q", result.IsError, result.Content)
	}

	result = runTool(t, exec, approvaltools.ToolName, map[string]any{
		"action":     notetools.SaveToolName,
		"reason":     "save reusable meeting note template",
		"tool_input": noteSaveInput,
	})
	if result.IsError {
		t.Fatalf("note approval should succeed: %s", result.Content)
	}

	result = runTool(t, exec, notetools.SaveToolName, noteSaveInput)
	if result.IsError {
		t.Fatalf("approved note save should succeed: %s", result.Content)
	}

	sendInput := map[string]any{
		"thread_id": "thread-1",
		"body":      "Thanks. I will keep replies concise and call out owners and due dates.",
		"recipients": []map[string]any{
			{"name": "Alex", "address": "alex@example.com"},
		},
	}
	result = runTool(t, exec, messagetools.SendToolName, sendInput)
	if !result.IsError || !strings.Contains(result.Content, agentpolicy.ApprovalBeforeToolReason(messagetools.SendToolName)) {
		t.Fatalf("expected message approval denial, got error=%v content=%q", result.IsError, result.Content)
	}

	result = runTool(t, exec, approvaltools.ToolName, map[string]any{
		"action":     messagetools.SendToolName,
		"reason":     "send a concise follow-up through the personal messaging backend",
		"tool_input": sendInput,
	})
	if result.IsError {
		t.Fatalf("message approval should succeed: %s", result.Content)
	}

	result = runTool(t, exec, messagetools.SendToolName, sendInput)
	if result.IsError {
		t.Fatalf("approved message send should succeed: %s", result.Content)
	}

	createScheduleInput := map[string]any{
		"title":   "Weekly sync",
		"summary": "Keep the sync concise",
		"organizer": map[string]any{
			"name":    "Alex",
			"address": "alex@example.com",
		},
		"start":     start.Add(24 * time.Hour).Format(time.RFC3339),
		"end":       start.Add(25 * time.Hour).Format(time.RFC3339),
		"time_zone": "UTC",
	}
	result = runTool(t, exec, scheduletools.CreateToolName, createScheduleInput)
	if !result.IsError || !strings.Contains(result.Content, agentpolicy.ApprovalBeforeToolReason(scheduletools.CreateToolName)) {
		t.Fatalf("expected schedule create approval denial, got error=%v content=%q", result.IsError, result.Content)
	}

	result = runTool(t, exec, approvaltools.ToolName, map[string]any{
		"action":     scheduletools.CreateToolName,
		"reason":     "create a follow-up event in the personal schedule backend",
		"tool_input": createScheduleInput,
	})
	if result.IsError {
		t.Fatalf("schedule create approval should succeed: %s", result.Content)
	}

	result = runTool(t, exec, scheduletools.CreateToolName, createScheduleInput)
	if result.IsError {
		t.Fatalf("approved schedule create should succeed: %s", result.Content)
	}

	rescheduleInput := map[string]any{
		"id":        "event-1",
		"start":     start.Add(2 * time.Hour).Format(time.RFC3339),
		"end":       start.Add(3 * time.Hour).Format(time.RFC3339),
		"time_zone": "America/Los_Angeles",
	}
	result = runTool(t, exec, scheduletools.RescheduleToolName, rescheduleInput)
	if !result.IsError || !strings.Contains(result.Content, agentpolicy.ApprovalBeforeToolReason(scheduletools.RescheduleToolName)) {
		t.Fatalf("expected schedule reschedule approval denial, got error=%v content=%q", result.IsError, result.Content)
	}

	result = runTool(t, exec, approvaltools.ToolName, map[string]any{
		"action":     scheduletools.RescheduleToolName,
		"reason":     "move the kickoff event in the personal schedule backend",
		"tool_input": rescheduleInput,
	})
	if result.IsError {
		t.Fatalf("schedule reschedule approval should succeed: %s", result.Content)
	}

	result = runTool(t, exec, scheduletools.RescheduleToolName, rescheduleInput)
	if result.IsError {
		t.Fatalf("approved schedule reschedule should succeed: %s", result.Content)
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
		notetools.SearchToolName,
		notetools.ReadToolName,
		notetools.SaveToolName,
		notetools.DeleteToolName,
		messagetools.SearchToolName,
		messagetools.ReadToolName,
		messagetools.SendToolName,
		scheduletools.SearchToolName,
		scheduletools.ReadToolName,
		scheduletools.CreateToolName,
		scheduletools.RescheduleToolName,
		scheduletools.CancelToolName,
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

func TestConfiguredSubagentsInheritReadOnlyNotesOnly(t *testing.T) {
	t.Parallel()

	noteStore := notes.NewNoteStore([]notes.Note{{
		ID:      "note-1",
		Title:   "meeting brief style",
		Content: "Use one short summary paragraph followed by owner and due-date bullets.",
	}})

	cfg, err := configuredSubagents(Config{
		Base: memaxagent.Options{},
		Notes: notetools.Config{
			Searcher: noteStore,
			Reader:   noteStore,
			Writer:   noteStore,
			Deleter:  noteStore,
		},
		Subagents: &subagents.Config{
			Agents: []subagents.Agent{{
				Name:        "researcher",
				Description: "Investigates questions.",
			}},
		},
	})
	if err != nil {
		t.Fatalf("configuredSubagents() error = %v", err)
	}
	if cfg.DefaultOptions.Tools == nil {
		t.Fatal("configuredSubagents() missing inherited child registry")
	}
	names := toolNames(cfg.DefaultOptions.Tools)
	for _, want := range []string{notetools.SearchToolName, notetools.ReadToolName} {
		if !contains(names, want) {
			t.Fatalf("child registry missing %q; got %v", want, names)
		}
	}
	for _, forbidden := range []string{notetools.SaveToolName, notetools.DeleteToolName} {
		if contains(names, forbidden) {
			t.Fatalf("child registry unexpectedly inherited %q; got %v", forbidden, names)
		}
	}
}

func TestConfiguredSubagentsInheritReadOnlyMessagesOnly(t *testing.T) {
	t.Parallel()

	messageStore := messaging.NewThreadStore([]messaging.Thread{{
		ID:      "thread-1",
		Subject: "Project kickoff follow-up",
		Summary: "Alex wants concise replies.",
		Participants: []messaging.Participant{
			{Name: "Alex", Address: "alex@example.com"},
		},
		Messages: []messaging.Message{{
			ID:        "thread-1-msg-1",
			ThreadID:  "thread-1",
			Subject:   "Project kickoff follow-up",
			Summary:   "Keep replies concise.",
			Body:      "Please keep replies concise and include owners.",
			Direction: messaging.DirectionInbound,
			Sender:    messaging.Participant{Name: "Alex", Address: "alex@example.com"},
		}},
	}})

	cfg, err := configuredSubagents(Config{
		Base: memaxagent.Options{},
		Messages: messagetools.Config{
			Searcher: messageStore,
			Reader:   messageStore,
			Sender:   messageStore,
		},
		Subagents: &subagents.Config{
			Agents: []subagents.Agent{{
				Name:        "researcher",
				Description: "Investigates questions.",
			}},
		},
	})
	if err != nil {
		t.Fatalf("configuredSubagents() error = %v", err)
	}
	if cfg.DefaultOptions.Tools == nil {
		t.Fatal("configuredSubagents() missing inherited child registry")
	}
	names := toolNames(cfg.DefaultOptions.Tools)
	for _, want := range []string{messagetools.SearchToolName, messagetools.ReadToolName} {
		if !contains(names, want) {
			t.Fatalf("child registry missing %q; got %v", want, names)
		}
	}
	if contains(names, messagetools.SendToolName) {
		t.Fatalf("child registry unexpectedly inherited %q; got %v", messagetools.SendToolName, names)
	}
}

func TestConfiguredSubagentsInheritReadOnlyScheduleOnly(t *testing.T) {
	t.Parallel()

	start := time.Date(2026, 4, 20, 15, 0, 0, 0, time.UTC)
	scheduleStore := scheduling.NewEventStore([]scheduling.Event{{
		ID:       "event-1",
		Title:    "Project kickoff",
		Summary:  "Weekly kickoff with owners and due dates",
		Location: "Zoom",
		Organizer: scheduling.Participant{
			Name:    "Alex",
			Address: "alex@example.com",
		},
		Start:       start,
		End:         start.Add(time.Hour),
		TimeZone:    "UTC",
		Description: "Keep the meeting concise and end with owners and due dates.",
	}})

	cfg, err := configuredSubagents(Config{
		Base: memaxagent.Options{},
		Schedule: scheduletools.Config{
			Searcher:    scheduleStore,
			Reader:      scheduleStore,
			Creator:     scheduleStore,
			Rescheduler: scheduleStore,
			Canceller:   scheduleStore,
		},
		Subagents: &subagents.Config{
			Agents: []subagents.Agent{{
				Name:        "researcher",
				Description: "Investigates questions.",
			}},
		},
	})
	if err != nil {
		t.Fatalf("configuredSubagents() error = %v", err)
	}
	if cfg.DefaultOptions.Tools == nil {
		t.Fatal("configuredSubagents() missing inherited child registry")
	}
	names := toolNames(cfg.DefaultOptions.Tools)
	for _, want := range []string{scheduletools.SearchToolName, scheduletools.ReadToolName} {
		if !contains(names, want) {
			t.Fatalf("child registry missing %q; got %v", want, names)
		}
	}
	for _, forbidden := range []string{
		scheduletools.CreateToolName,
		scheduletools.RescheduleToolName,
		scheduletools.CancelToolName,
	} {
		if contains(names, forbidden) {
			t.Fatalf("child registry unexpectedly inherited %q; got %v", forbidden, names)
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
		Notes: notetools.Config{
			Writer: notes.NewNoteStore(nil),
		},
		Policies: Policies{
			RequireNoteApproval: true,
		},
	})
	if err == nil || !strings.Contains(err.Error(), "note approval requires approval approver") {
		t.Fatalf("New() error = %v, want note approval validation", err)
	}

	_, err = New(Config{
		Messages: messagetools.Config{
			Sender: messaging.NewThreadStore(nil),
		},
		Policies: Policies{
			RequireMessageApproval: true,
		},
	})
	if err == nil || !strings.Contains(err.Error(), "message approval requires approval approver") {
		t.Fatalf("New() error = %v, want message approval validation", err)
	}

	_, err = New(Config{
		Schedule: scheduletools.Config{
			Creator: scheduling.NewEventStore(nil),
		},
		Policies: Policies{
			RequireScheduleCreateApproval: true,
		},
	})
	if err == nil || !strings.Contains(err.Error(), "schedule approval requires approval approver") {
		t.Fatalf("New() error = %v, want schedule approval validation", err)
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
	if !policies.RequireMemoryApproval ||
		!policies.RequireNoteApproval ||
		!policies.RequireMessageApproval ||
		!policies.RequireScheduleCreateApproval ||
		!policies.RequireScheduleRescheduleApproval ||
		!policies.RequireScheduleCancelApproval ||
		!policies.SingleUseApprovals ||
		!policies.InputBoundApprovals ||
		policies.RequireDelegationApproval {
		t.Fatalf("DefaultPolicies() = %#v, want durable-state approvals on and delegation approval off", policies)
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
