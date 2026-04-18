package scenarios

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	memaxagent "github.com/MemaxLabs/memax-go-agent-sdk"
	"github.com/MemaxLabs/memax-go-agent-sdk/agenteval"
	"github.com/MemaxLabs/memax-go-agent-sdk/memory"
	"github.com/MemaxLabs/memax-go-agent-sdk/model"
	"github.com/MemaxLabs/memax-go-agent-sdk/session"
	"github.com/MemaxLabs/memax-go-agent-sdk/stack/personal"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/agentpolicy"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/approvaltools"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/memorytools"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/subagents"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/tasktools"
)

// PersonalPresetAssistant returns a single-use scenario that exercises the
// personal_assistant preset through a durable-memory workflow: recall existing
// context, request approval proactively, save a new durable memory, and confirm
// it is discoverable for later turns.
func PersonalPresetAssistant() agenteval.Case {
	memoryStore := memory.NewMemoryStore([]memory.Memory{{
		ID:      "memory-1",
		Name:    "note-style",
		Scope:   memory.ScopeUser,
		Content: "User prefers concise meeting notes.",
	}})
	taskStore := tasktools.NewMemoryStore([]tasktools.Task{{
		ID:     "task-1",
		Title:  "capture durable meeting preferences",
		Status: tasktools.StatusInProgress,
		Notes:  "review existing memory before saving a new long-lived preference",
	}})
	modelClient := agenteval.NewScriptedModel(
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:   "approval-1",
				Name: approvaltools.ToolName,
				Input: json.RawMessage(`{
					"action":"save_memory",
					"reason":"saving a durable meeting preference requires approval",
					"tool_input":{
						"name":"meeting-outcomes",
						"scope":"user",
						"content":"Meeting outcomes should stay short, action-oriented, and easy to skim."
					}
				}`),
			},
		}},
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:   "save-2",
				Name: memorytools.SaveToolName,
				Input: json.RawMessage(`{
					"name":"meeting-outcomes",
					"scope":"user",
					"content":"Meeting outcomes should stay short, action-oriented, and easy to skim."
				}`),
			},
		}},
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:    "search-1",
				Name:  memorytools.SearchToolName,
				Input: json.RawMessage(`{"query":"action-oriented meeting outcomes","limit":3}`),
			},
		}},
		[]model.StreamEvent{{Kind: model.StreamText, Text: "Personal assistant saved and recalled the durable meeting preference."}},
	)

	config, configErr := personal.PresetPersonalAssistant.Config()
	config.Memory = memorytools.Config{
		Source:       memoryStore,
		Writer:       memoryStore,
		DefaultLimit: 3,
	}
	config.Tasks = taskStore
	config.Approval.Approver = approvaltools.StaticApprover{
		Decision: approvaltools.Decision{
			Approved: true,
			Reason:   "approved durable meeting preference",
		},
	}
	stack, stackErr := personal.New(config)

	return agenteval.Case{
		Name:    "personal_preset_personal_assistant",
		Prompt:  "Capture the user's durable meeting preference carefully and only after the required approval flow.",
		Options: stack.WithModel(modelClient),
		Assertions: []agenteval.Assertion{
			toolConstructionSucceeded(configErr, stackErr),
			agenteval.ToolUsed(approvaltools.ToolName),
			agenteval.ToolUsed(memorytools.SaveToolName),
			agenteval.ToolUsed(memorytools.SearchToolName),
			agenteval.EventKindEmitted(memaxagent.EventApprovalRequested),
			agenteval.EventKindEmitted(memaxagent.EventApprovalGranted),
			agenteval.EventKindEmitted(memaxagent.EventApprovalConsumed),
			agenteval.NoToolErrors(),
			agenteval.FinalEquals("Personal assistant saved and recalled the durable meeting preference."),
			requestCountEquals(modelClient, 4),
			{
				Name: "assistant preset guidance and seeded memory are visible",
				Check: func(agenteval.Result) error {
					requests := modelClient.Requests()
					if len(requests) != 4 {
						return fmt.Errorf("requests = %d, want 4", len(requests))
					}
					initialPrompt := requests[0].SystemPrompt
					for _, want := range []string{
						"Recall durable user and project context before writing new memory.",
						"User prefers concise meeting notes.",
						"[in_progress] task-1",
						memorytools.SaveToolName,
						approvaltools.ToolName,
					} {
						if !strings.Contains(initialPrompt, want) {
							return fmt.Errorf("initial prompt missing %q:\n%s", want, initialPrompt)
						}
					}
					return nil
				},
			},
			{
				Name: "approved memory save persists and is discoverable",
				Check: func(result agenteval.Result) error {
					toolResults := result.ToolResults()
					if len(toolResults) < 3 {
						return fmt.Errorf("tool results = %#v, want approval save search", toolResults)
					}
					if toolResults[0].IsError || toolResults[0].Metadata[approvaltools.MetadataApprovalApproved] != true {
						return fmt.Errorf("approval result = %#v, want granted approval", toolResults[0])
					}
					if toolResults[1].IsError || !strings.Contains(toolResults[1].Content, "meeting-outcomes") {
						return fmt.Errorf("save result = %#v, want durable save success", toolResults[1])
					}
					if toolResults[2].IsError || !strings.Contains(toolResults[2].Content, "meeting-outcomes") {
						return fmt.Errorf("search result = %#v, want recalled memory", toolResults[2])
					}
					items, err := memoryStore.Memories(context.Background(), memory.Request{})
					if err != nil {
						return err
					}
					if len(items) != 2 {
						return fmt.Errorf("memory count = %d, want 2", len(items))
					}
					return nil
				},
			},
		},
	}
}

// PersonalPresetAssistantMemoryApprovalRecovery returns a single-use scenario
// where the personal_assistant preset denies a durable memory write until the
// model requests approval and retries the save.
func PersonalPresetAssistantMemoryApprovalRecovery() agenteval.Case {
	memoryStore := memory.NewMemoryStore([]memory.Memory{{
		ID:      "memory-1",
		Name:    "note-style",
		Scope:   memory.ScopeUser,
		Content: "User prefers concise meeting notes.",
	}})
	taskStore := tasktools.NewMemoryStore([]tasktools.Task{{
		ID:     "task-1",
		Title:  "capture durable meeting preferences",
		Status: tasktools.StatusInProgress,
		Notes:  "review existing memory before saving a new long-lived preference",
	}})
	modelClient := agenteval.NewScriptedModel(
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:   "save-1",
				Name: memorytools.SaveToolName,
				Input: json.RawMessage(`{
					"name":"meeting-outcomes",
					"scope":"user",
					"content":"Meeting outcomes should stay short, action-oriented, and easy to skim."
				}`),
			},
		}},
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:   "approval-1",
				Name: approvaltools.ToolName,
				Input: json.RawMessage(`{
					"action":"save_memory",
					"reason":"saving a durable meeting preference requires approval",
					"tool_input":{
						"name":"meeting-outcomes",
						"scope":"user",
						"content":"Meeting outcomes should stay short, action-oriented, and easy to skim."
					}
				}`),
			},
		}},
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:   "save-2",
				Name: memorytools.SaveToolName,
				Input: json.RawMessage(`{
					"name":"meeting-outcomes",
					"scope":"user",
					"content":"Meeting outcomes should stay short, action-oriented, and easy to skim."
				}`),
			},
		}},
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:    "search-1",
				Name:  memorytools.SearchToolName,
				Input: json.RawMessage(`{"query":"action-oriented meeting outcomes","limit":3}`),
			},
		}},
		[]model.StreamEvent{{Kind: model.StreamText, Text: "Personal assistant recovered after memory approval and saved the durable preference."}},
	)

	config, configErr := personal.PresetPersonalAssistant.Config()
	config.Memory = memorytools.Config{
		Source:       memoryStore,
		Writer:       memoryStore,
		DefaultLimit: 3,
	}
	config.Tasks = taskStore
	config.Approval.Approver = approvaltools.StaticApprover{
		Decision: approvaltools.Decision{
			Approved: true,
			Reason:   "approved durable meeting preference",
		},
	}
	stack, stackErr := personal.New(config)

	return agenteval.Case{
		Name:    "personal_preset_personal_assistant_memory_approval_recovery",
		Prompt:  "Capture the user's durable meeting preference carefully and recover through the required approval flow if the first save is denied.",
		Options: stack.WithModel(modelClient),
		Assertions: []agenteval.Assertion{
			toolConstructionSucceeded(configErr, stackErr),
			agenteval.ToolUsed(memorytools.SaveToolName),
			agenteval.ToolUsed(approvaltools.ToolName),
			agenteval.ToolUsed(memorytools.SearchToolName),
			agenteval.EventKindEmitted(memaxagent.EventApprovalRequested),
			agenteval.EventKindEmitted(memaxagent.EventApprovalGranted),
			agenteval.EventKindEmitted(memaxagent.EventApprovalConsumed),
			agenteval.FinalEquals("Personal assistant recovered after memory approval and saved the durable preference."),
			requestCountEquals(modelClient, 5),
			{
				Name: "memory approval recovery drives durable save",
				Check: func(result agenteval.Result) error {
					toolResults := result.ToolResults()
					if len(toolResults) < 4 {
						return fmt.Errorf("tool results = %#v, want denied save approval save search", toolResults)
					}
					if !toolResults[0].IsError || !strings.Contains(toolResults[0].Content, agentpolicy.ApprovalBeforeToolReason(memorytools.SaveToolName)) {
						return fmt.Errorf("first save result = %#v, want approval denial", toolResults[0])
					}
					if toolResults[1].IsError || toolResults[1].Metadata[approvaltools.MetadataApprovalApproved] != true {
						return fmt.Errorf("approval result = %#v, want granted approval", toolResults[1])
					}
					if toolResults[2].IsError || !strings.Contains(toolResults[2].Content, "meeting-outcomes") {
						return fmt.Errorf("second save result = %#v, want durable save success", toolResults[2])
					}
					if toolResults[3].IsError || !strings.Contains(toolResults[3].Content, "meeting-outcomes") {
						return fmt.Errorf("search result = %#v, want recalled memory", toolResults[3])
					}
					return nil
				},
			},
		},
	}
}

// PersonalPresetResearchPartner returns a single-use scenario that exercises
// the research_partner preset through scoped delegation: the child agent
// inherits the stack's prompt posture and memory context, sees only the scoped
// task, and the delegated result updates parent-visible task progress.
func PersonalPresetResearchPartner() agenteval.Case {
	store := session.NewMemoryStore()
	memoryStore := memory.NewMemoryStore([]memory.Memory{{
		ID:      "memory-1",
		Name:    "research-style",
		Scope:   memory.ScopeUser,
		Content: "Always keep research conclusions traceable to the gathered evidence.",
	}})
	taskStore := tasktools.NewMemoryStore([]tasktools.Task{
		{ID: "task-1", Title: "investigate travel options", Status: tasktools.StatusInProgress, Evidence: []string{"trip.md"}},
		{ID: "task-2", Title: "unrelated grocery reminders", Status: tasktools.StatusPending},
	})
	childModel := agenteval.NewScriptedModel(
		[]model.StreamEvent{{Kind: model.StreamText, Text: "child research summary complete"}},
	)
	parentModel := agenteval.NewScriptedModel(
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:   "delegate-1",
				Name: subagents.ToolName,
				Input: json.RawMessage(`{
					"agent":"researcher",
					"prompt":"investigate the best train options for the trip task",
					"task_id":"task-1"
				}`),
			},
		}},
		[]model.StreamEvent{{Kind: model.StreamText, Text: "Research partner completed the delegated travel investigation."}},
	)

	config, configErr := personal.PresetResearchPartner.Config()
	config.Memory = memorytools.Config{Source: memoryStore}
	config.Tasks = taskStore
	config.Subagents = &subagents.Config{
		Agents: []subagents.Agent{{
			Name:        "researcher",
			Description: "Investigates focused personal research tasks.",
			Options: memaxagent.Options{
				Model:    childModel,
				Sessions: store,
			},
		}},
		DefaultOptions: memaxagent.Options{Sessions: store},
	}
	stack, stackErr := personal.New(config)

	return agenteval.Case{
		Name:    "personal_preset_research_partner",
		Prompt:  "Delegate the travel research task and finish only after the scoped progress is reflected.",
		Options: stack.WithModel(parentModel),
		Assertions: []agenteval.Assertion{
			toolConstructionSucceeded(configErr, stackErr),
			agenteval.ToolUsed(subagents.ToolName),
			agenteval.NoToolErrors(),
			agenteval.FinalEquals("Research partner completed the delegated travel investigation."),
			requestCountEquals(parentModel, 2),
			{
				Name: "child sees inherited posture and scoped task only",
				Check: func(agenteval.Result) error {
					requests := childModel.Requests()
					if len(requests) != 1 {
						return fmt.Errorf("child requests = %d, want 1", len(requests))
					}
					prompt := requests[0].SystemPrompt
					for _, want := range []string{
						"Use scoped delegation for independent research threads when it helps.",
						"Always keep research conclusions traceable to the gathered evidence.",
						"complete delegated task task-1",
						"task-1",
						"trip.md",
						subagents.ToolName,
					} {
						if !strings.Contains(prompt, want) {
							return fmt.Errorf("child prompt missing %q:\n%s", want, prompt)
						}
					}
					if strings.Contains(prompt, "task-2") || strings.Contains(prompt, "grocery reminders") {
						return fmt.Errorf("child prompt leaked unrelated task:\n%s", prompt)
					}
					return nil
				},
			},
			{
				Name: "delegation updates parent task progress",
				Check: func(agenteval.Result) error {
					requests := parentModel.Requests()
					if len(requests) != 2 {
						return fmt.Errorf("parent requests = %d, want 2", len(requests))
					}
					finalPrompt := requests[1].SystemPrompt
					for _, want := range []string{"[completed] task-1", "subagent:researcher"} {
						if !strings.Contains(finalPrompt, want) {
							return fmt.Errorf("final prompt missing %q:\n%s", want, finalPrompt)
						}
					}
					tasks, err := taskStore.List(context.Background())
					if err != nil {
						return err
					}
					if len(tasks) != 2 || tasks[0].Status != tasktools.StatusCompleted {
						return fmt.Errorf("tasks = %#v, want completed delegated task", tasks)
					}
					return nil
				},
			},
		},
	}
}
