package scenarios

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	memaxagent "github.com/MemaxLabs/memax-go-agent-sdk"
	"github.com/MemaxLabs/memax-go-agent-sdk/agenteval"
	"github.com/MemaxLabs/memax-go-agent-sdk/memory"
	"github.com/MemaxLabs/memax-go-agent-sdk/messaging"
	"github.com/MemaxLabs/memax-go-agent-sdk/model"
	"github.com/MemaxLabs/memax-go-agent-sdk/notes"
	"github.com/MemaxLabs/memax-go-agent-sdk/scheduling"
	"github.com/MemaxLabs/memax-go-agent-sdk/session"
	"github.com/MemaxLabs/memax-go-agent-sdk/stack/personal"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/agentpolicy"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/approvaltools"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/memorytools"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/messagetools"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/notetools"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/scheduletools"
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

// PersonalPresetAssistantDailyBriefing returns a single-use scenario where the
// personal_assistant preset assembles a morning briefing by searching note,
// message, and schedule metadata first, then selectively reading the matching
// full items before synthesizing the final brief.
func PersonalPresetAssistantDailyBriefing() agenteval.Case {
	memoryStore := memory.NewMemoryStore([]memory.Memory{{
		ID:      "memory-1",
		Name:    "briefing-style",
		Scope:   memory.ScopeUser,
		Content: "Morning briefings should start with urgent changes and explicit times.",
	}})
	noteStore := notes.NewNoteStore([]notes.Note{{
		ID:        "note-1",
		Title:     "Morning briefing template",
		Kind:      "brief",
		Summary:   "Template for daily executive briefings",
		Content:   "Lead with urgent changes, then list the next meeting and any travel prep.",
		Tags:      []string{"briefing", "template"},
		CreatedAt: time.Date(2026, 4, 19, 6, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 4, 19, 6, 0, 0, 0, time.UTC),
	}})
	messageStore := messaging.NewThreadStore([]messaging.Thread{{
		ID:      "thread-1",
		Subject: "Travel update for today",
		Summary: "Jordan says the flight moved to 3:30 PM and asks you to bring your passport.",
		Participants: []messaging.Participant{
			{Name: "Jordan", Address: "jordan@example.com"},
		},
		Messages: []messaging.Message{{
			ID:        "thread-1-msg-1",
			ThreadID:  "thread-1",
			Subject:   "Travel update for today",
			Summary:   "Flight moved and passport reminder.",
			Body:      "The flight moved to 3:30 PM. Please bring your passport to the airport.",
			Direction: messaging.DirectionInbound,
			Sender:    messaging.Participant{Name: "Jordan", Address: "jordan@example.com"},
			SentAt:    time.Date(2026, 4, 19, 7, 0, 0, 0, time.UTC),
		}},
	}})
	eventStart := time.Date(2026, 4, 19, 9, 0, 0, 0, time.UTC)
	scheduleStore := scheduling.NewEventStore([]scheduling.Event{{
		ID:       "event-1",
		Title:    "Design review",
		Summary:  "Review the Q2 launch design with Taylor.",
		Location: "Room 5A",
		Organizer: scheduling.Participant{
			Name:    "Taylor",
			Address: "taylor@example.com",
		},
		Start:       eventStart,
		End:         eventStart.Add(45 * time.Minute),
		TimeZone:    "UTC",
		Description: "Bring the revised vendor budget and decision log.",
	}})
	taskStore := tasktools.NewMemoryStore([]tasktools.Task{{
		ID:     "task-1",
		Title:  "Prepare the morning briefing",
		Status: tasktools.StatusInProgress,
		Notes:  "search note, message, and schedule metadata before reading the full items you need for the briefing",
	}})

	modelClient := agenteval.NewScriptedModel(
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:    "search-note-1",
				Name:  notetools.SearchToolName,
				Input: json.RawMessage(`{"query":"morning briefing urgent changes travel prep","limit":3}`),
			},
		}},
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:    "search-thread-1",
				Name:  messagetools.SearchToolName,
				Input: json.RawMessage(`{"query":"travel update passport flight","limit":3}`),
			},
		}},
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:    "search-event-1",
				Name:  scheduletools.SearchToolName,
				Input: json.RawMessage(`{"query":"design review vendor budget","limit":3}`),
			},
		}},
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:    "read-note-1",
				Name:  notetools.ReadToolName,
				Input: json.RawMessage(`{"id":"note-1"}`),
			},
		}},
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:    "read-thread-1",
				Name:  messagetools.ReadToolName,
				Input: json.RawMessage(`{"thread_id":"thread-1"}`),
			},
		}},
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:    "read-event-1",
				Name:  scheduletools.ReadToolName,
				Input: json.RawMessage(`{"id":"event-1"}`),
			},
		}},
		[]model.StreamEvent{{Kind: model.StreamText, Text: "Morning briefing: urgent change first, your design review is at 09:00 UTC in Room 5A, and Jordan says the flight moved to 3:30 PM so bring your passport."}},
	)

	config, configErr := personal.PresetPersonalAssistant.Config()
	config.Memory = memorytools.Config{
		Source:       memoryStore,
		DefaultLimit: 3,
	}
	config.Notes = notetools.Config{
		Searcher:     noteStore,
		Reader:       noteStore,
		DefaultLimit: 3,
	}
	config.Messages = messagetools.Config{
		Searcher:     messageStore,
		Reader:       messageStore,
		DefaultLimit: 3,
	}
	config.Schedule = scheduletools.Config{
		Searcher:     scheduleStore,
		Reader:       scheduleStore,
		DefaultLimit: 3,
	}
	config.Tasks = taskStore
	stack, stackErr := personal.New(config)

	return agenteval.Case{
		Name:    "personal_preset_personal_assistant_daily_briefing",
		Prompt:  "Prepare this morning's briefing. Search note, message, and schedule metadata first, then read only the items you need.",
		Options: stack.WithModel(modelClient),
		Assertions: []agenteval.Assertion{
			toolConstructionSucceeded(configErr, stackErr),
			agenteval.NoToolErrors(),
			agenteval.FinalEquals("Morning briefing: urgent change first, your design review is at 09:00 UTC in Room 5A, and Jordan says the flight moved to 3:30 PM so bring your passport."),
			requestCountEquals(modelClient, 7),
			{
				Name: "briefing tool order stays metadata-first",
				Check: func(result agenteval.Result) error {
					uses := result.ToolUses()
					// This scripted case proves the stack supports a search-then-read
					// briefing flow; real models may validly interleave search and read.
					want := []string{
						notetools.SearchToolName,
						messagetools.SearchToolName,
						scheduletools.SearchToolName,
						notetools.ReadToolName,
						messagetools.ReadToolName,
						scheduletools.ReadToolName,
					}
					if len(uses) != len(want) {
						return fmt.Errorf("tool uses = %#v, want %v", uses, want)
					}
					for i, name := range want {
						if uses[i].Name != name {
							return fmt.Errorf("tool use order = %#v, want %v", uses, want)
						}
					}
					return nil
				},
			},
			{
				Name: "briefing prompt includes durable style and active task",
				Check: func(result agenteval.Result) error {
					requests := modelClient.Requests()
					if len(requests) != 7 {
						return fmt.Errorf("requests = %d, want 7", len(requests))
					}
					initialPrompt := requests[0].SystemPrompt
					for _, want := range []string{
						"Morning briefings should start with urgent changes and explicit times.",
						"[in_progress] task-1",
						"Prepare the morning briefing",
						notetools.SearchToolName,
						messagetools.SearchToolName,
						scheduletools.SearchToolName,
					} {
						if !strings.Contains(initialPrompt, want) {
							return fmt.Errorf("initial prompt missing %q:\n%s", want, initialPrompt)
						}
					}
					return nil
				},
			},
			{
				Name: "metadata searches do not leak full content before reads",
				Check: func(result agenteval.Result) error {
					toolResults := result.ToolResults()
					if len(toolResults) != 6 {
						return fmt.Errorf("tool results = %#v, want 6 search/read results", toolResults)
					}
					if strings.Contains(toolResults[0].Content, "Lead with urgent changes") {
						return fmt.Errorf("note search leaked full note content: %q", toolResults[0].Content)
					}
					if strings.Contains(toolResults[1].Content, "bring your passport to the airport") {
						return fmt.Errorf("message search leaked full thread body: %q", toolResults[1].Content)
					}
					if strings.Contains(toolResults[2].Content, "revised vendor budget and decision log") {
						return fmt.Errorf("schedule search leaked full event description: %q", toolResults[2].Content)
					}
					if !strings.Contains(toolResults[3].Content, "Lead with urgent changes, then list the next meeting and any travel prep.") {
						return fmt.Errorf("note read content = %q, want full note content", toolResults[3].Content)
					}
					if !strings.Contains(toolResults[4].Content, "Please bring your passport to the airport.") {
						return fmt.Errorf("message read content = %q, want full thread content", toolResults[4].Content)
					}
					if !strings.Contains(toolResults[5].Content, "Bring the revised vendor budget and decision log.") {
						return fmt.Errorf("schedule read content = %q, want full event description", toolResults[5].Content)
					}
					return nil
				},
			},
		},
	}
}

// PersonalPresetAssistantNoteRecall returns a single-use scenario where the
// personal_assistant preset searches note metadata, reads one seeded note, uses
// the recalled content to save a matching reusable note, and confirms the new
// note is discoverable afterward.
func PersonalPresetAssistantNoteRecall() agenteval.Case {
	noteStore := notes.NewNoteStore([]notes.Note{{
		ID:      "note-1",
		Title:   "meeting brief style",
		Kind:    "brief",
		Summary: "Reusable style for concise meeting briefs",
		Content: "Use one short summary paragraph followed by owner and due-date bullets.",
		Tags:    []string{"meeting", "brief"},
	}})
	taskStore := tasktools.NewMemoryStore([]tasktools.Task{{
		ID:     "task-1",
		Title:  "capture reusable meeting template",
		Status: tasktools.StatusInProgress,
		Notes:  "search notes first, then load the relevant note before saving a reusable template",
	}})
	modelClient := agenteval.NewScriptedModel(
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:    "search-1",
				Name:  notetools.SearchToolName,
				Input: json.RawMessage(`{"query":"meeting brief style owner due date","limit":3}`),
			},
		}},
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:    "read-1",
				Name:  notetools.ReadToolName,
				Input: json.RawMessage(`{"id":"note-1"}`),
			},
		}},
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:   "approval-1",
				Name: approvaltools.ToolName,
				Input: json.RawMessage(`{
					"action":"save_note",
					"reason":"saving a reusable personal note template requires approval",
					"tool_input":{
						"title":"meeting follow-up template",
						"kind":"template",
						"summary":"Reusable action-oriented follow-up template",
						"content":"Use one short summary paragraph followed by owner and due-date bullets for every follow-up."
					}
				}`),
			},
		}},
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:   "save-1",
				Name: notetools.SaveToolName,
				Input: json.RawMessage(`{
					"title":"meeting follow-up template",
					"kind":"template",
					"summary":"Reusable action-oriented follow-up template",
					"content":"Use one short summary paragraph followed by owner and due-date bullets for every follow-up."
				}`),
			},
		}},
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:    "search-2",
				Name:  notetools.SearchToolName,
				Input: json.RawMessage(`{"query":"meeting follow-up template owner due-date bullets","limit":3}`),
			},
		}},
		[]model.StreamEvent{{Kind: model.StreamText, Text: "Personal assistant recalled the note style and saved a matching reusable template."}},
	)

	config, configErr := personal.PresetPersonalAssistant.Config()
	config.Notes = notetools.Config{
		Searcher:     noteStore,
		Reader:       noteStore,
		Writer:       noteStore,
		DefaultLimit: 3,
	}
	config.Tasks = taskStore
	config.Approval.Approver = approvaltools.StaticApprover{
		Decision: approvaltools.Decision{
			Approved: true,
			Reason:   "approved reusable note template",
		},
	}
	stack, stackErr := personal.New(config)

	return agenteval.Case{
		Name:    "personal_preset_personal_assistant_note_recall",
		Prompt:  "Capture a reusable meeting follow-up template, but search notes first and reuse the existing style before saving anything.",
		Options: stack.WithModel(modelClient),
		Assertions: []agenteval.Assertion{
			toolConstructionSucceeded(configErr, stackErr),
			agenteval.ToolUsed(notetools.SearchToolName),
			agenteval.ToolUsed(notetools.ReadToolName),
			agenteval.ToolUsed(approvaltools.ToolName),
			agenteval.ToolUsed(notetools.SaveToolName),
			agenteval.EventKindEmitted(memaxagent.EventApprovalRequested),
			agenteval.EventKindEmitted(memaxagent.EventApprovalGranted),
			agenteval.EventKindEmitted(memaxagent.EventApprovalConsumed),
			agenteval.NoToolErrors(),
			agenteval.FinalEquals("Personal assistant recalled the note style and saved a matching reusable template."),
			requestCountEquals(modelClient, 6),
			{
				Name: "assistant preset prompt includes note workflow guidance",
				Check: func(agenteval.Result) error {
					requests := modelClient.Requests()
					if len(requests) != 6 {
						return fmt.Errorf("requests = %d, want 6", len(requests))
					}
					initialPrompt := requests[0].SystemPrompt
					for _, want := range []string{
						"Search note, document, message-thread, and schedule metadata before loading full content or changing calendar state.",
						"[in_progress] task-1",
						notetools.SearchToolName,
						notetools.ReadToolName,
						notetools.SaveToolName,
					} {
						if !strings.Contains(initialPrompt, want) {
							return fmt.Errorf("initial prompt missing %q:\n%s", want, initialPrompt)
						}
					}
					return nil
				},
			},
			{
				Name: "note recall changes the saved template",
				Check: func(result agenteval.Result) error {
					toolResults := result.ToolResults()
					if len(toolResults) != 5 {
						return fmt.Errorf("tool results = %#v, want exactly search read approval save search", toolResults)
					}
					if strings.Contains(toolResults[0].Content, "owner and due-date bullets for every follow-up") {
						return fmt.Errorf("metadata search leaked full template content: %#v", toolResults[0])
					}
					if toolResults[1].IsError || !strings.Contains(toolResults[1].Content, "owner and due-date bullets") {
						return fmt.Errorf("read result = %#v, want recalled note content", toolResults[1])
					}
					if toolResults[2].IsError || toolResults[2].Metadata[approvaltools.MetadataApprovalApproved] != true {
						return fmt.Errorf("approval result = %#v, want granted approval", toolResults[2])
					}
					if toolResults[3].IsError || !strings.Contains(toolResults[3].Content, "meeting follow-up template") {
						return fmt.Errorf("save result = %#v, want note save success", toolResults[3])
					}
					if toolResults[4].IsError || !strings.Contains(toolResults[4].Content, "meeting follow-up template") {
						return fmt.Errorf("search result = %#v, want saved note metadata", toolResults[4])
					}
					item, err := noteStore.ReadNote(context.Background(), notes.ReadRequest{Title: "meeting follow-up template"})
					if err != nil {
						return err
					}
					if !strings.Contains(item.Content, "owner and due-date bullets") {
						return fmt.Errorf("saved note content = %q, want recalled style", item.Content)
					}
					return nil
				},
			},
		},
	}
}

// PersonalPresetAssistantMessageRecall returns a single-use scenario where the
// personal_assistant preset searches message-thread metadata, reads one seeded
// thread, requests approval, sends a reply that reflects the recalled thread
// guidance, and confirms the updated thread remains discoverable.
func PersonalPresetAssistantMessageRecall() agenteval.Case {
	messageStore := messaging.NewThreadStore([]messaging.Thread{{
		ID:      "thread-1",
		Subject: "Project kickoff follow-up",
		Summary: "Alex wants concise replies with owners and due dates.",
		Participants: []messaging.Participant{
			{Name: "Alex", Address: "alex@example.com"},
		},
		Tags: []string{"project", "follow-up"},
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
	taskStore := tasktools.NewMemoryStore([]tasktools.Task{{
		ID:     "task-1",
		Title:  "reply to kickoff follow-up",
		Status: tasktools.StatusInProgress,
		Notes:  "search message thread metadata first, then read the thread before sending a reply",
	}})
	sendInput := `{
		"thread_id":"thread-1",
		"body":"Thanks. I'll keep the update concise and call out owners and due dates in the follow-up.",
		"recipients":[{"name":"Alex","address":"alex@example.com"}]
	}`
	modelClient := agenteval.NewScriptedModel(
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:    "search-1",
				Name:  messagetools.SearchToolName,
				Input: json.RawMessage(`{"query":"kickoff follow-up owners due dates","limit":3}`),
			},
		}},
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:    "read-1",
				Name:  messagetools.ReadToolName,
				Input: json.RawMessage(`{"thread_id":"thread-1"}`),
			},
		}},
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:   "approval-1",
				Name: approvaltools.ToolName,
				Input: json.RawMessage(`{
					"action":"send_message",
					"reason":"sending an outbound project follow-up requires approval",
					"tool_input":` + sendInput + `
				}`),
			},
		}},
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:    "send-1",
				Name:  messagetools.SendToolName,
				Input: json.RawMessage(sendInput),
			},
		}},
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:    "search-2",
				Name:  messagetools.SearchToolName,
				Input: json.RawMessage(`{"query":"kickoff follow-up concise owners due dates","limit":3}`),
			},
		}},
		[]model.StreamEvent{{Kind: model.StreamText, Text: "Personal assistant recalled the thread guidance, sent an approved reply, and confirmed the thread remains discoverable."}},
	)

	config, configErr := personal.PresetPersonalAssistant.Config()
	config.Messages = messagetools.Config{
		Searcher:     messageStore,
		Reader:       messageStore,
		Sender:       messageStore,
		DefaultLimit: 3,
	}
	config.Tasks = taskStore
	config.Approval.Approver = approvaltools.StaticApprover{
		Decision: approvaltools.Decision{
			Approved: true,
			Reason:   "approved outbound project follow-up",
		},
	}
	stack, stackErr := personal.New(config)

	return agenteval.Case{
		Name:    "personal_preset_personal_assistant_message_recall",
		Prompt:  "Reply to the kickoff follow-up carefully, but search message metadata first and read the thread before sending anything.",
		Options: stack.WithModel(modelClient),
		Assertions: []agenteval.Assertion{
			toolConstructionSucceeded(configErr, stackErr),
			agenteval.ToolUsed(messagetools.SearchToolName),
			agenteval.ToolUsed(messagetools.ReadToolName),
			agenteval.ToolUsed(approvaltools.ToolName),
			agenteval.ToolUsed(messagetools.SendToolName),
			agenteval.EventKindEmitted(memaxagent.EventApprovalRequested),
			agenteval.EventKindEmitted(memaxagent.EventApprovalGranted),
			agenteval.EventKindEmitted(memaxagent.EventApprovalConsumed),
			agenteval.NoToolErrors(),
			agenteval.FinalEquals("Personal assistant recalled the thread guidance, sent an approved reply, and confirmed the thread remains discoverable."),
			requestCountEquals(modelClient, 6),
			{
				Name: "assistant preset prompt includes message workflow guidance",
				Check: func(agenteval.Result) error {
					requests := modelClient.Requests()
					if len(requests) != 6 {
						return fmt.Errorf("requests = %d, want 6", len(requests))
					}
					initialPrompt := requests[0].SystemPrompt
					for _, want := range []string{
						"Search note, document, message-thread, and schedule metadata before loading full content or changing calendar state.",
						"[in_progress] task-1",
						messagetools.SearchToolName,
						messagetools.ReadToolName,
						messagetools.SendToolName,
					} {
						if !strings.Contains(initialPrompt, want) {
							return fmt.Errorf("initial prompt missing %q:\n%s", want, initialPrompt)
						}
					}
					return nil
				},
			},
			{
				Name: "message recall changes the approved outbound reply",
				Check: func(result agenteval.Result) error {
					toolResults := result.ToolResults()
					if len(toolResults) != 5 {
						return fmt.Errorf("tool results = %#v, want exactly search read approval send search", toolResults)
					}
					if strings.Contains(toolResults[0].Content, "Please keep replies concise and include owners and due dates.") {
						return fmt.Errorf("metadata search leaked full thread content: %#v", toolResults[0])
					}
					if toolResults[1].IsError || !strings.Contains(toolResults[1].Content, "Please keep replies concise and include owners and due dates.") {
						return fmt.Errorf("read result = %#v, want recalled thread content", toolResults[1])
					}
					if toolResults[2].IsError || toolResults[2].Metadata[approvaltools.MetadataApprovalApproved] != true {
						return fmt.Errorf("approval result = %#v, want granted approval", toolResults[2])
					}
					if toolResults[3].IsError || !strings.Contains(toolResults[3].Content, "sent message Project kickoff follow-up") {
						return fmt.Errorf("send result = %#v, want send success", toolResults[3])
					}
					if toolResults[3].Metadata["created_thread"] != false {
						return fmt.Errorf("send result metadata = %#v, want reply to existing thread", toolResults[3].Metadata)
					}
					if toolResults[4].IsError || !strings.Contains(toolResults[4].Content, "Project kickoff follow-up") {
						return fmt.Errorf("search result = %#v, want thread metadata after send", toolResults[4])
					}
					thread, err := messageStore.ReadThread(context.Background(), messaging.ReadRequest{ThreadID: "thread-1"})
					if err != nil {
						return err
					}
					if len(thread.Messages) != 2 {
						return fmt.Errorf("thread messages = %d, want 2", len(thread.Messages))
					}
					last := thread.Messages[len(thread.Messages)-1]
					if !strings.Contains(last.Body, "owners and due dates") {
						return fmt.Errorf("outbound message body = %q, want recalled style", last.Body)
					}
					return nil
				},
			},
		},
	}
}

// PersonalPresetAssistantMessageApprovalRecovery returns a single-use scenario
// where the personal_assistant preset denies an outbound reply until the model
// requests approval and retries the send against the same thread.
func PersonalPresetAssistantMessageApprovalRecovery() agenteval.Case {
	messageStore := messaging.NewThreadStore([]messaging.Thread{{
		ID:      "thread-1",
		Subject: "Project kickoff follow-up",
		Summary: "Alex wants concise replies with owners and due dates.",
		Participants: []messaging.Participant{
			{Name: "Alex", Address: "alex@example.com"},
		},
		Tags: []string{"project", "follow-up"},
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
	taskStore := tasktools.NewMemoryStore([]tasktools.Task{{
		ID:     "task-1",
		Title:  "reply to kickoff follow-up",
		Status: tasktools.StatusInProgress,
		Notes:  "search message thread metadata first, then read the thread before sending a reply",
	}})
	sendInput := `{
		"thread_id":"thread-1",
		"body":"Thanks. I'll keep the update concise and call out owners and due dates in the follow-up.",
		"recipients":[{"name":"Alex","address":"alex@example.com"}]
	}`
	modelClient := agenteval.NewScriptedModel(
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:    "search-1",
				Name:  messagetools.SearchToolName,
				Input: json.RawMessage(`{"query":"kickoff follow-up owners due dates","limit":3}`),
			},
		}},
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:    "read-1",
				Name:  messagetools.ReadToolName,
				Input: json.RawMessage(`{"thread_id":"thread-1"}`),
			},
		}},
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:    "send-1",
				Name:  messagetools.SendToolName,
				Input: json.RawMessage(sendInput),
			},
		}},
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:   "approval-1",
				Name: approvaltools.ToolName,
				Input: json.RawMessage(`{
					"action":"send_message",
					"reason":"sending an outbound project follow-up requires approval",
					"tool_input":` + sendInput + `
				}`),
			},
		}},
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:    "send-2",
				Name:  messagetools.SendToolName,
				Input: json.RawMessage(sendInput),
			},
		}},
		[]model.StreamEvent{{Kind: model.StreamText, Text: "Personal assistant recovered after message approval and sent the requested reply."}},
	)

	config, configErr := personal.PresetPersonalAssistant.Config()
	config.Messages = messagetools.Config{
		Searcher:     messageStore,
		Reader:       messageStore,
		Sender:       messageStore,
		DefaultLimit: 3,
	}
	config.Tasks = taskStore
	config.Approval.Approver = approvaltools.StaticApprover{
		Decision: approvaltools.Decision{
			Approved: true,
			Reason:   "approved outbound project follow-up",
		},
	}
	stack, stackErr := personal.New(config)

	return agenteval.Case{
		Name:    "personal_preset_personal_assistant_message_approval_recovery",
		Prompt:  "Reply to the kickoff follow-up carefully and recover through the required approval flow if the first send is denied.",
		Options: stack.WithModel(modelClient),
		Assertions: []agenteval.Assertion{
			toolConstructionSucceeded(configErr, stackErr),
			agenteval.ToolUsed(messagetools.SearchToolName),
			agenteval.ToolUsed(messagetools.ReadToolName),
			agenteval.ToolUsed(messagetools.SendToolName),
			agenteval.ToolUsed(approvaltools.ToolName),
			agenteval.EventKindEmitted(memaxagent.EventApprovalRequested),
			agenteval.EventKindEmitted(memaxagent.EventApprovalGranted),
			agenteval.EventKindEmitted(memaxagent.EventApprovalConsumed),
			agenteval.FinalEquals("Personal assistant recovered after message approval and sent the requested reply."),
			requestCountEquals(modelClient, 6),
			{
				Name: "message approval recovery drives the outbound reply",
				Check: func(result agenteval.Result) error {
					toolResults := result.ToolResults()
					if len(toolResults) != 5 {
						return fmt.Errorf("tool results = %#v, want exactly search read denied send approval send", toolResults)
					}
					if strings.Contains(toolResults[0].Content, "Please keep replies concise and include owners and due dates.") {
						return fmt.Errorf("metadata search leaked full thread content: %#v", toolResults[0])
					}
					if toolResults[1].IsError || !strings.Contains(toolResults[1].Content, "Please keep replies concise and include owners and due dates.") {
						return fmt.Errorf("read result = %#v, want recalled thread content", toolResults[1])
					}
					if !toolResults[2].IsError || !strings.Contains(toolResults[2].Content, agentpolicy.ApprovalBeforeToolReason(messagetools.SendToolName)) {
						return fmt.Errorf("first send result = %#v, want approval denial", toolResults[2])
					}
					if toolResults[3].IsError || toolResults[3].Metadata[approvaltools.MetadataApprovalApproved] != true {
						return fmt.Errorf("approval result = %#v, want granted approval", toolResults[3])
					}
					if toolResults[4].IsError || !strings.Contains(toolResults[4].Content, "sent message Project kickoff follow-up") {
						return fmt.Errorf("second send result = %#v, want send success", toolResults[4])
					}
					if toolResults[4].Metadata["created_thread"] != false {
						return fmt.Errorf("second send metadata = %#v, want reply to existing thread", toolResults[4].Metadata)
					}
					thread, err := messageStore.ReadThread(context.Background(), messaging.ReadRequest{ThreadID: "thread-1"})
					if err != nil {
						return err
					}
					if len(thread.Messages) != 2 {
						return fmt.Errorf("thread messages = %d, want 2", len(thread.Messages))
					}
					last := thread.Messages[len(thread.Messages)-1]
					if !strings.Contains(last.Body, "owners and due dates") {
						return fmt.Errorf("outbound message body = %q, want recalled style", last.Body)
					}
					return nil
				},
			},
		},
	}
}

// PersonalPresetAssistantInboxTriageReplyFollowup returns a single-use
// scenario where the personal_assistant preset triages an unread inbox thread
// from metadata first, loads the full thread only before drafting, recovers
// through message approval, and then creates an approval-gated follow-up
// reminder through the schedule backend.
func PersonalPresetAssistantInboxTriageReplyFollowup() agenteval.Case {
	messageStore := messaging.NewThreadStore([]messaging.Thread{{
		ID:      "thread-1",
		Subject: "Urgent: Acme renewal blocker",
		Summary: "Casey says checkout is blocked before Monday's renewal deadline and needs a same-day update.",
		Participants: []messaging.Participant{
			{Name: "Casey", Address: "casey@acme.example", Role: "from"},
		},
		Tags:          []string{"INBOX", "urgent", "customer"},
		LastMessageAt: time.Date(2026, 4, 19, 8, 15, 0, 0, time.UTC),
		Metadata:      map[string]any{"unread": true},
		Messages: []messaging.Message{{
			ID:        "thread-1-msg-1",
			ThreadID:  "thread-1",
			Subject:   "Urgent: Acme renewal blocker",
			Summary:   "Checkout blocked before the renewal deadline.",
			Body:      "Checkout is blocked for Acme before Monday's renewal deadline. Please send me a same-day update and tell me when I should expect the next checkpoint.",
			Direction: messaging.DirectionInbound,
			Sender:    messaging.Participant{Name: "Casey", Address: "casey@acme.example", Role: "from"},
			SentAt:    time.Date(2026, 4, 19, 8, 15, 0, 0, time.UTC),
		}},
	}})
	scheduleStore := scheduling.NewEventStore(nil)
	taskStore := tasktools.NewMemoryStore([]tasktools.Task{{
		ID:     "task-1",
		Title:  "triage the urgent Acme inbox thread",
		Status: tasktools.StatusInProgress,
		Notes:  "search unread inbox metadata first, then read the thread before replying, and create a follow-up reminder after the approved reply",
	}})

	searchInput := `{
		"query":"urgent renewal blocker same-day update",
		"mailboxes":["INBOX"],
		"from":["casey@acme.example"],
		"unread":true,
		"limit":3
	}`
	sendInput := `{
		"thread_id":"thread-1",
		"body":"Thanks, Casey. We are treating this as urgent and I will send you the next update by 14:00 UTC today.",
		"recipients":[{"name":"Casey","address":"casey@acme.example"}]
	}`
	createInput := `{
		"title":"Acme blocker follow-up",
		"summary":"Send Casey the promised same-day checkout update.",
		"description":"Follow up on the Acme renewal blocker and send the 14:00 UTC status update.",
		"start":"2026-04-19T13:45:00Z",
		"end":"2026-04-19T14:00:00Z",
		"time_zone":"UTC",
		"attendees":[{"name":"Casey","address":"casey@acme.example"}],
		"tags":["follow-up","customer","urgent"]
	}`
	modelClient := agenteval.NewScriptedModel(
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:    "search-1",
				Name:  messagetools.SearchToolName,
				Input: json.RawMessage(searchInput),
			},
		}},
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:    "read-1",
				Name:  messagetools.ReadToolName,
				Input: json.RawMessage(`{"thread_id":"thread-1"}`),
			},
		}},
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:    "send-1",
				Name:  messagetools.SendToolName,
				Input: json.RawMessage(sendInput),
			},
		}},
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:   "approval-1",
				Name: approvaltools.ToolName,
				Input: json.RawMessage(`{
					"action":"send_message",
					"reason":"sending an urgent customer update requires approval",
					"tool_input":` + sendInput + `
				}`),
			},
		}},
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:    "send-2",
				Name:  messagetools.SendToolName,
				Input: json.RawMessage(sendInput),
			},
		}},
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:   "approval-2",
				Name: approvaltools.ToolName,
				Input: json.RawMessage(`{
					"action":"create_schedule_event",
					"reason":"creating a customer follow-up reminder requires approval",
					"tool_input":` + createInput + `
				}`),
			},
		}},
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:    "create-1",
				Name:  scheduletools.CreateToolName,
				Input: json.RawMessage(createInput),
			},
		}},
		[]model.StreamEvent{{Kind: model.StreamText, Text: "Personal assistant triaged the urgent Acme inbox thread from metadata, recovered through approval to send the reply, and created a same-day follow-up reminder."}},
	)

	config, configErr := personal.PresetPersonalAssistant.Config()
	config.Messages = messagetools.Config{
		Searcher:     messageStore,
		Reader:       messageStore,
		Sender:       messageStore,
		DefaultLimit: 3,
	}
	config.Schedule = scheduletools.Config{
		Creator:      scheduleStore,
		DefaultLimit: 3,
	}
	config.Tasks = taskStore
	config.Approval.Approver = approvaltools.StaticApprover{
		Decision: approvaltools.Decision{
			Approved: true,
			Reason:   "approved urgent customer workflow",
		},
	}
	stack, stackErr := personal.New(config)

	return agenteval.Case{
		Name:    "personal_preset_personal_assistant_inbox_triage_reply_followup",
		Prompt:  "Triage urgent unread inbox threads carefully, only read a thread before drafting a reply, and create a follow-up reminder after the approved reply if the thread needs one.",
		Options: stack.WithModel(modelClient),
		Assertions: []agenteval.Assertion{
			toolConstructionSucceeded(configErr, stackErr),
			agenteval.ToolUsed(messagetools.SearchToolName),
			agenteval.ToolUsed(messagetools.ReadToolName),
			agenteval.ToolUsed(messagetools.SendToolName),
			agenteval.ToolUsed(approvaltools.ToolName),
			agenteval.ToolUsed(scheduletools.CreateToolName),
			agenteval.EventKindEmitted(memaxagent.EventApprovalRequested),
			agenteval.EventKindEmitted(memaxagent.EventApprovalGranted),
			agenteval.EventKindEmitted(memaxagent.EventApprovalConsumed),
			agenteval.FinalEquals("Personal assistant triaged the urgent Acme inbox thread from metadata, recovered through approval to send the reply, and created a same-day follow-up reminder."),
			requestCountEquals(modelClient, 8),
			{
				Name: "assistant preset prompt includes inbox triage workflow guidance",
				Check: func(agenteval.Result) error {
					requests := modelClient.Requests()
					if len(requests) != 8 {
						return fmt.Errorf("requests = %d, want 8", len(requests))
					}
					initialPrompt := requests[0].SystemPrompt
					for _, want := range []string{
						"Search note, document, message-thread, and schedule metadata before loading full content or changing calendar state.",
						"[in_progress] task-1",
						messagetools.SearchToolName,
						messagetools.ReadToolName,
						messagetools.SendToolName,
						scheduletools.CreateToolName,
					} {
						if !strings.Contains(initialPrompt, want) {
							return fmt.Errorf("initial prompt missing %q:\n%s", want, initialPrompt)
						}
					}
					return nil
				},
			},
			{
				Name: "urgent triage stays metadata-first before the thread read",
				Check: func(result agenteval.Result) error {
					uses := result.ToolUses()
					want := []string{
						messagetools.SearchToolName,
						messagetools.ReadToolName,
						messagetools.SendToolName,
						approvaltools.ToolName,
						messagetools.SendToolName,
						approvaltools.ToolName,
						scheduletools.CreateToolName,
					}
					if len(uses) != len(want) {
						return fmt.Errorf("tool uses = %#v, want %v", uses, want)
					}
					for i, name := range want {
						if uses[i].Name != name {
							return fmt.Errorf("tool use order = %#v, want %v", uses, want)
						}
					}
					toolResults := result.ToolResults()
					if len(toolResults) != 7 {
						return fmt.Errorf("tool results = %#v, want exactly search read denied send approval send approval create", toolResults)
					}
					if !strings.Contains(toolResults[0].Content, "Urgent: Acme renewal blocker") || !strings.Contains(toolResults[0].Content, "same-day update") {
						return fmt.Errorf("search result = %#v, want urgent metadata", toolResults[0])
					}
					if strings.Contains(toolResults[0].Content, "Please send me a same-day update and tell me when I should expect the next checkpoint.") {
						return fmt.Errorf("search result leaked full thread body: %#v", toolResults[0])
					}
					if toolResults[0].Metadata["matches"] != 1 {
						return fmt.Errorf("search metadata = %#v, want one urgent inbox match", toolResults[0].Metadata)
					}
					return nil
				},
			},
			{
				Name: "reply recovery and follow-up creation both persist",
				Check: func(result agenteval.Result) error {
					toolResults := result.ToolResults()
					if toolResults[1].IsError || !strings.Contains(toolResults[1].Content, "Please send me a same-day update") {
						return fmt.Errorf("read result = %#v, want full thread before draft", toolResults[1])
					}
					if !toolResults[2].IsError || !strings.Contains(toolResults[2].Content, agentpolicy.ApprovalBeforeToolReason(messagetools.SendToolName)) {
						return fmt.Errorf("first send result = %#v, want approval denial", toolResults[2])
					}
					if toolResults[3].IsError || toolResults[3].Metadata[approvaltools.MetadataApprovalApproved] != true {
						return fmt.Errorf("message approval result = %#v, want granted approval", toolResults[3])
					}
					if toolResults[4].IsError || !strings.Contains(toolResults[4].Content, "sent message Urgent: Acme renewal blocker") {
						return fmt.Errorf("second send result = %#v, want send success", toolResults[4])
					}
					if toolResults[5].IsError || toolResults[5].Metadata[approvaltools.MetadataApprovalApproved] != true {
						return fmt.Errorf("schedule approval result = %#v, want granted approval", toolResults[5])
					}
					if toolResults[6].IsError || !strings.Contains(toolResults[6].Content, "created schedule event Acme blocker follow-up") {
						return fmt.Errorf("create result = %#v, want follow-up creation success", toolResults[6])
					}
					thread, err := messageStore.ReadThread(context.Background(), messaging.ReadRequest{ThreadID: "thread-1"})
					if err != nil {
						return err
					}
					if len(thread.Messages) != 2 {
						return fmt.Errorf("thread messages = %d, want 2", len(thread.Messages))
					}
					last := thread.Messages[len(thread.Messages)-1]
					if !strings.Contains(last.Body, "14:00 UTC today") {
						return fmt.Errorf("outbound reply body = %q, want promised checkpoint time", last.Body)
					}
					created, err := scheduleStore.ReadEvent(context.Background(), scheduling.ReadRequest{Title: "Acme blocker follow-up"})
					if err != nil {
						return err
					}
					if created.TimeZone != "UTC" {
						return fmt.Errorf("follow-up timezone = %q, want UTC", created.TimeZone)
					}
					if created.End.Sub(created.Start) != 15*time.Minute {
						return fmt.Errorf("follow-up duration = %s, want 15m", created.End.Sub(created.Start))
					}
					if !strings.Contains(created.Description, "14:00 UTC status update") {
						return fmt.Errorf("follow-up description = %q, want promised update", created.Description)
					}
					return nil
				},
			},
		},
	}
}

// PersonalPresetAssistantScheduleRecall returns a single-use scenario where
// the personal_assistant preset searches schedule metadata, reads one seeded
// event, requests approval, reschedules the event using the recalled
// constraints, and confirms the updated event remains discoverable.
func PersonalPresetAssistantScheduleRecall() agenteval.Case {
	start := time.Date(2026, 4, 20, 22, 0, 0, 0, time.UTC)
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
		Description: "Keep this kickoff to 45 minutes and do not move it after 4 PM Pacific.",
		Tags:        []string{"project", "kickoff"},
	}})
	taskStore := tasktools.NewMemoryStore([]tasktools.Task{{
		ID:     "task-1",
		Title:  "adjust the kickoff event",
		Status: tasktools.StatusInProgress,
		Notes:  "search schedule metadata first, then read the event before changing calendar state",
	}})
	rescheduleInput := `{
		"id":"event-1",
		"start":"2026-04-20T15:15:00-07:00",
		"end":"2026-04-20T16:00:00-07:00",
		"time_zone":"America/Los_Angeles"
	}`
	modelClient := agenteval.NewScriptedModel(
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:    "search-1",
				Name:  scheduletools.SearchToolName,
				Input: json.RawMessage(`{"query":"kickoff owners due dates","limit":3}`),
			},
		}},
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:    "read-1",
				Name:  scheduletools.ReadToolName,
				Input: json.RawMessage(`{"id":"event-1"}`),
			},
		}},
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:   "approval-1",
				Name: approvaltools.ToolName,
				Input: json.RawMessage(`{
					"action":"reschedule_schedule_event",
					"reason":"rescheduling a calendar event requires approval",
					"tool_input":` + rescheduleInput + `
				}`),
			},
		}},
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:    "reschedule-1",
				Name:  scheduletools.RescheduleToolName,
				Input: json.RawMessage(rescheduleInput),
			},
		}},
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:    "search-2",
				Name:  scheduletools.SearchToolName,
				Input: json.RawMessage(`{"query":"kickoff america/los_angeles","limit":3}`),
			},
		}},
		[]model.StreamEvent{{Kind: model.StreamText, Text: "Personal assistant recalled the event constraints, rescheduled the kickoff, and confirmed the updated event metadata."}},
	)

	config, configErr := personal.PresetPersonalAssistant.Config()
	config.Schedule = scheduletools.Config{
		Searcher:     scheduleStore,
		Reader:       scheduleStore,
		Rescheduler:  scheduleStore,
		DefaultLimit: 3,
	}
	config.Tasks = taskStore
	config.Approval.Approver = approvaltools.StaticApprover{
		Decision: approvaltools.Decision{
			Approved: true,
			Reason:   "approved kickoff reschedule",
		},
	}
	stack, stackErr := personal.New(config)

	return agenteval.Case{
		Name:    "personal_preset_personal_assistant_schedule_recall",
		Prompt:  "Adjust the kickoff event carefully, but search schedule metadata first and read the event before changing calendar state.",
		Options: stack.WithModel(modelClient),
		Assertions: []agenteval.Assertion{
			toolConstructionSucceeded(configErr, stackErr),
			agenteval.ToolUsed(scheduletools.SearchToolName),
			agenteval.ToolUsed(scheduletools.ReadToolName),
			agenteval.ToolUsed(approvaltools.ToolName),
			agenteval.ToolUsed(scheduletools.RescheduleToolName),
			agenteval.EventKindEmitted(memaxagent.EventApprovalRequested),
			agenteval.EventKindEmitted(memaxagent.EventApprovalGranted),
			agenteval.EventKindEmitted(memaxagent.EventApprovalConsumed),
			agenteval.NoToolErrors(),
			agenteval.FinalEquals("Personal assistant recalled the event constraints, rescheduled the kickoff, and confirmed the updated event metadata."),
			requestCountEquals(modelClient, 6),
			{
				Name: "assistant preset prompt includes schedule workflow guidance",
				Check: func(agenteval.Result) error {
					requests := modelClient.Requests()
					if len(requests) != 6 {
						return fmt.Errorf("requests = %d, want 6", len(requests))
					}
					initialPrompt := requests[0].SystemPrompt
					for _, want := range []string{
						"Search note, document, message-thread, and schedule metadata before loading full content or changing calendar state.",
						"[in_progress] task-1",
						scheduletools.SearchToolName,
						scheduletools.ReadToolName,
						scheduletools.RescheduleToolName,
					} {
						if !strings.Contains(initialPrompt, want) {
							return fmt.Errorf("initial prompt missing %q:\n%s", want, initialPrompt)
						}
					}
					return nil
				},
			},
			{
				Name: "schedule recall changes the approved reschedule",
				Check: func(result agenteval.Result) error {
					toolResults := result.ToolResults()
					if len(toolResults) != 5 {
						return fmt.Errorf("tool results = %#v, want exactly search read approval reschedule search", toolResults)
					}
					if strings.Contains(toolResults[0].Content, "Keep this kickoff to 45 minutes") {
						return fmt.Errorf("metadata search leaked full event description: %#v", toolResults[0])
					}
					if toolResults[1].IsError || !strings.Contains(toolResults[1].Content, "do not move it after 4 PM Pacific") {
						return fmt.Errorf("read result = %#v, want recalled event description", toolResults[1])
					}
					if toolResults[2].IsError || toolResults[2].Metadata[approvaltools.MetadataApprovalApproved] != true {
						return fmt.Errorf("approval result = %#v, want granted approval", toolResults[2])
					}
					if toolResults[3].IsError || !strings.Contains(toolResults[3].Content, "rescheduled schedule event Project kickoff") {
						return fmt.Errorf("reschedule result = %#v, want reschedule success", toolResults[3])
					}
					if toolResults[3].Metadata["event_time_zone"] != "America/Los_Angeles" {
						return fmt.Errorf("reschedule metadata = %#v, want updated time zone", toolResults[3].Metadata)
					}
					if toolResults[4].IsError || !strings.Contains(toolResults[4].Content, "America/Los_Angeles") {
						return fmt.Errorf("search result = %#v, want updated schedule metadata", toolResults[4])
					}
					item, err := scheduleStore.ReadEvent(context.Background(), scheduling.ReadRequest{ID: "event-1"})
					if err != nil {
						return err
					}
					if item.TimeZone != "America/Los_Angeles" {
						return fmt.Errorf("event timezone = %q, want America/Los_Angeles", item.TimeZone)
					}
					if item.End.Sub(item.Start) != 45*time.Minute {
						return fmt.Errorf("event duration = %s, want 45m", item.End.Sub(item.Start))
					}
					return nil
				},
			},
		},
	}
}

// PersonalPresetAssistantScheduleApprovalRecovery returns a single-use scenario
// where the personal_assistant preset denies a reschedule until the model
// requests approval and retries the event change.
func PersonalPresetAssistantScheduleApprovalRecovery() agenteval.Case {
	start := time.Date(2026, 4, 20, 22, 0, 0, 0, time.UTC)
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
		Description: "Keep this kickoff to 45 minutes and do not move it after 4 PM Pacific.",
		Tags:        []string{"project", "kickoff"},
	}})
	taskStore := tasktools.NewMemoryStore([]tasktools.Task{{
		ID:     "task-1",
		Title:  "adjust the kickoff event",
		Status: tasktools.StatusInProgress,
		Notes:  "search schedule metadata first, then read the event before changing calendar state",
	}})
	rescheduleInput := `{
		"id":"event-1",
		"start":"2026-04-20T15:15:00-07:00",
		"end":"2026-04-20T16:00:00-07:00",
		"time_zone":"America/Los_Angeles"
	}`
	modelClient := agenteval.NewScriptedModel(
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:    "search-1",
				Name:  scheduletools.SearchToolName,
				Input: json.RawMessage(`{"query":"kickoff owners due dates","limit":3}`),
			},
		}},
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:    "read-1",
				Name:  scheduletools.ReadToolName,
				Input: json.RawMessage(`{"id":"event-1"}`),
			},
		}},
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:    "reschedule-1",
				Name:  scheduletools.RescheduleToolName,
				Input: json.RawMessage(rescheduleInput),
			},
		}},
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:   "approval-1",
				Name: approvaltools.ToolName,
				Input: json.RawMessage(`{
					"action":"reschedule_schedule_event",
					"reason":"rescheduling a calendar event requires approval",
					"tool_input":` + rescheduleInput + `
				}`),
			},
		}},
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:    "reschedule-2",
				Name:  scheduletools.RescheduleToolName,
				Input: json.RawMessage(rescheduleInput),
			},
		}},
		[]model.StreamEvent{{Kind: model.StreamText, Text: "Personal assistant recovered after schedule approval and rescheduled the kickoff."}},
	)

	config, configErr := personal.PresetPersonalAssistant.Config()
	config.Schedule = scheduletools.Config{
		Searcher:     scheduleStore,
		Reader:       scheduleStore,
		Rescheduler:  scheduleStore,
		DefaultLimit: 3,
	}
	config.Tasks = taskStore
	config.Approval.Approver = approvaltools.StaticApprover{
		Decision: approvaltools.Decision{
			Approved: true,
			Reason:   "approved kickoff reschedule",
		},
	}
	stack, stackErr := personal.New(config)

	return agenteval.Case{
		Name:    "personal_preset_personal_assistant_schedule_approval_recovery",
		Prompt:  "Adjust the kickoff event carefully and recover through approval if the first calendar change is denied.",
		Options: stack.WithModel(modelClient),
		Assertions: []agenteval.Assertion{
			toolConstructionSucceeded(configErr, stackErr),
			agenteval.ToolUsed(scheduletools.SearchToolName),
			agenteval.ToolUsed(scheduletools.ReadToolName),
			agenteval.ToolUsed(scheduletools.RescheduleToolName),
			agenteval.ToolUsed(approvaltools.ToolName),
			agenteval.EventKindEmitted(memaxagent.EventApprovalRequested),
			agenteval.EventKindEmitted(memaxagent.EventApprovalGranted),
			agenteval.EventKindEmitted(memaxagent.EventApprovalConsumed),
			agenteval.FinalEquals("Personal assistant recovered after schedule approval and rescheduled the kickoff."),
			requestCountEquals(modelClient, 6),
			{
				Name: "schedule approval recovery drives the event change",
				Check: func(result agenteval.Result) error {
					toolResults := result.ToolResults()
					if len(toolResults) != 5 {
						return fmt.Errorf("tool results = %#v, want exactly search read denied reschedule approval reschedule", toolResults)
					}
					if strings.Contains(toolResults[0].Content, "Keep this kickoff to 45 minutes") {
						return fmt.Errorf("metadata search leaked full event description: %#v", toolResults[0])
					}
					if toolResults[1].IsError || !strings.Contains(toolResults[1].Content, "do not move it after 4 PM Pacific") {
						return fmt.Errorf("read result = %#v, want recalled event description", toolResults[1])
					}
					if !toolResults[2].IsError || !strings.Contains(toolResults[2].Content, agentpolicy.ApprovalBeforeToolReason(scheduletools.RescheduleToolName)) {
						return fmt.Errorf("first reschedule result = %#v, want approval denial", toolResults[2])
					}
					if toolResults[3].IsError || toolResults[3].Metadata[approvaltools.MetadataApprovalApproved] != true {
						return fmt.Errorf("approval result = %#v, want granted approval", toolResults[3])
					}
					if toolResults[4].IsError || !strings.Contains(toolResults[4].Content, "rescheduled schedule event Project kickoff") {
						return fmt.Errorf("second reschedule result = %#v, want reschedule success", toolResults[4])
					}
					item, err := scheduleStore.ReadEvent(context.Background(), scheduling.ReadRequest{ID: "event-1"})
					if err != nil {
						return err
					}
					if item.TimeZone != "America/Los_Angeles" {
						return fmt.Errorf("event timezone = %q, want America/Los_Angeles", item.TimeZone)
					}
					if item.End.Sub(item.Start) != 45*time.Minute {
						return fmt.Errorf("event duration = %s, want 45m", item.End.Sub(item.Start))
					}
					return nil
				},
			},
		},
	}
}

// PersonalPresetAssistantScheduleConflictRecovery returns a single-use scenario
// where the personal_assistant preset approves a schedule change, receives a
// recoverable conflict from the host-owned schedule backend, rereads the event,
// re-requests approval, retries the reschedule, and marks the task completed
// with evidence.
func PersonalPresetAssistantScheduleConflictRecovery() agenteval.Case {
	originalStart := time.Date(2026, 4, 20, 22, 0, 0, 0, time.UTC)
	original := scheduling.Event{
		ID:       "event-1",
		Title:    "Project kickoff",
		Summary:  "Weekly kickoff with owners and due dates",
		Location: "Zoom",
		Organizer: scheduling.Participant{
			Name:    "Alex",
			Address: "alex@example.com",
		},
		Start:       originalStart,
		End:         originalStart.Add(time.Hour),
		TimeZone:    "UTC",
		Description: "Keep this kickoff to 45 minutes and do not move it after 4 PM Pacific.",
		Tags:        []string{"project", "kickoff"},
	}
	conflicting := cloneScheduleEvent(original)
	conflicting.Start = time.Date(2026, 4, 20, 22, 30, 0, 0, time.UTC)
	conflicting.End = conflicting.Start.Add(50 * time.Minute)
	conflicting.TimeZone = "America/Los_Angeles"
	conflicting.Description = original.Description + " Another editor already moved it, so reread before retrying."
	scheduleStore := newConflictScheduleStore(original, conflicting)
	taskStore := tasktools.NewMemoryStore([]tasktools.Task{{
		ID:     "task-1",
		Title:  "adjust the kickoff event",
		Status: tasktools.StatusInProgress,
		Notes:  "search schedule metadata first, then read the event before changing calendar state",
	}})
	rescheduleInput := `{
		"id":"event-1",
		"start":"2026-04-20T15:00:00-07:00",
		"end":"2026-04-20T15:45:00-07:00",
		"time_zone":"America/Los_Angeles"
	}`
	modelClient := agenteval.NewScriptedModel(
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:    "search-1",
				Name:  scheduletools.SearchToolName,
				Input: json.RawMessage(`{"query":"kickoff owners due dates","limit":3}`),
			},
		}},
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:    "read-1",
				Name:  scheduletools.ReadToolName,
				Input: json.RawMessage(`{"id":"event-1"}`),
			},
		}},
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:   "approval-1",
				Name: approvaltools.ToolName,
				Input: json.RawMessage(`{
					"action":"reschedule_schedule_event",
					"reason":"rescheduling a calendar event requires approval",
					"tool_input":` + rescheduleInput + `
				}`),
			},
		}},
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:    "reschedule-1",
				Name:  scheduletools.RescheduleToolName,
				Input: json.RawMessage(rescheduleInput),
			},
		}},
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:    "read-2",
				Name:  scheduletools.ReadToolName,
				Input: json.RawMessage(`{"id":"event-1"}`),
			},
		}},
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:   "approval-2",
				Name: approvaltools.ToolName,
				Input: json.RawMessage(`{
					"action":"reschedule_schedule_event",
					"reason":"retrying a calendar event reschedule after a conflict requires approval",
					"tool_input":` + rescheduleInput + `
				}`),
			},
		}},
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:    "reschedule-2",
				Name:  scheduletools.RescheduleToolName,
				Input: json.RawMessage(rescheduleInput),
			},
		}},
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:   "task-1",
				Name: tasktools.UpsertToolName,
				Input: json.RawMessage(`{
					"id":"task-1",
					"status":"completed",
					"notes":"Recovered from a schedule conflict and applied the final kickoff time.",
					"evidence":["schedule conflict recovered","event-1"]
				}`),
			},
		}},
		[]model.StreamEvent{{Kind: model.StreamText, Text: "Personal assistant recovered from a schedule conflict, retried the kickoff update, and recorded task evidence."}},
	)

	config, configErr := personal.PresetPersonalAssistant.Config()
	config.Schedule = scheduletools.Config{
		Searcher:     scheduleStore,
		Reader:       scheduleStore,
		Rescheduler:  scheduleStore,
		DefaultLimit: 3,
	}
	config.Tasks = taskStore
	config.Approval.Approver = approvaltools.StaticApprover{
		Decision: approvaltools.Decision{
			Approved: true,
			Reason:   "approved kickoff reschedule retry",
		},
	}
	stack, stackErr := personal.New(config)

	conflictingStart := conflicting.Start.Format(time.RFC3339Nano)
	finalStart := time.Date(2026, 4, 20, 22, 0, 0, 0, time.UTC).Format(time.RFC3339Nano)

	return agenteval.Case{
		Name:    "personal_preset_personal_assistant_schedule_conflict_recovery",
		Prompt:  "Adjust the kickoff event carefully, recover from schedule conflicts, and keep task progress current.",
		Options: stack.WithModel(modelClient),
		Assertions: []agenteval.Assertion{
			toolConstructionSucceeded(configErr, stackErr),
			agenteval.ToolUsed(scheduletools.SearchToolName),
			agenteval.ToolUsed(scheduletools.ReadToolName),
			agenteval.ToolUsed(approvaltools.ToolName),
			agenteval.ToolUsed(scheduletools.RescheduleToolName),
			agenteval.ToolUsed(tasktools.UpsertToolName),
			agenteval.EventKindEmitted(memaxagent.EventApprovalRequested),
			agenteval.EventKindEmitted(memaxagent.EventApprovalGranted),
			agenteval.EventKindEmitted(memaxagent.EventApprovalConsumed),
			agenteval.FinalEquals("Personal assistant recovered from a schedule conflict, retried the kickoff update, and recorded task evidence."),
			requestCountEquals(modelClient, 9),
			{
				Name: "conflict recovery updates task guidance in the final prompt",
				Check: func(agenteval.Result) error {
					requests := modelClient.Requests()
					if len(requests) != 9 {
						return fmt.Errorf("requests = %d, want 9", len(requests))
					}
					finalPrompt := requests[len(requests)-1].SystemPrompt
					for _, want := range []string{
						"[completed] task-1",
						"schedule conflict recovered",
						scheduletools.RescheduleToolName,
						tasktools.UpsertToolName,
					} {
						if !strings.Contains(finalPrompt, want) {
							return fmt.Errorf("final prompt missing %q:\n%s", want, finalPrompt)
						}
					}
					return nil
				},
			},
			{
				Name: "schedule conflict recovery rereads and retries against the updated state",
				Check: func(result agenteval.Result) error {
					toolResults := result.ToolResults()
					if len(toolResults) != 8 {
						return fmt.Errorf("tool results = %#v, want search read approval reschedule conflict read approval reschedule upsert", toolResults)
					}
					if strings.Contains(toolResults[0].Content, "Keep this kickoff to 45 minutes") {
						return fmt.Errorf("metadata search leaked full event description: %#v", toolResults[0])
					}
					if toolResults[1].IsError || !strings.Contains(toolResults[1].Content, "do not move it after 4 PM Pacific") {
						return fmt.Errorf("first read result = %#v, want original event description", toolResults[1])
					}
					if toolResults[2].IsError || toolResults[2].Metadata[approvaltools.MetadataApprovalApproved] != true {
						return fmt.Errorf("first approval result = %#v, want granted approval", toolResults[2])
					}
					if !toolResults[3].IsError || !strings.Contains(toolResults[3].Content, "schedule event changed during reschedule") {
						return fmt.Errorf("first reschedule result = %#v, want recoverable conflict", toolResults[3])
					}
					if toolResults[4].IsError || toolResults[4].Metadata["event_start"] != conflictingStart {
						return fmt.Errorf("second read result = %#v, want conflicted event state", toolResults[4])
					}
					if toolResults[5].IsError || toolResults[5].Metadata[approvaltools.MetadataApprovalApproved] != true {
						return fmt.Errorf("second approval result = %#v, want granted retry approval", toolResults[5])
					}
					if toolResults[6].IsError || !strings.Contains(toolResults[6].Content, "rescheduled schedule event Project kickoff") {
						return fmt.Errorf("second reschedule result = %#v, want successful retry", toolResults[6])
					}
					if toolResults[6].Metadata["previous_event_start"] != conflictingStart {
						return fmt.Errorf("retry metadata = %#v, want previous conflicted start %q", toolResults[6].Metadata, conflictingStart)
					}
					if toolResults[6].Metadata["event_start"] != finalStart {
						return fmt.Errorf("retry metadata = %#v, want final start %q", toolResults[6].Metadata, finalStart)
					}
					if toolResults[7].IsError || toolResults[7].Metadata["status"] != string(tasktools.StatusCompleted) {
						return fmt.Errorf("task result = %#v, want completed task", toolResults[7])
					}
					tasks, err := taskStore.List(context.Background())
					if err != nil {
						return err
					}
					if len(tasks) != 1 || tasks[0].Status != tasktools.StatusCompleted {
						return fmt.Errorf("tasks = %#v, want one completed task", tasks)
					}
					if len(tasks[0].Evidence) != 2 || tasks[0].Evidence[0] != "schedule conflict recovered" {
						return fmt.Errorf("task evidence = %#v, want conflict evidence", tasks[0].Evidence)
					}
					item, err := scheduleStore.ReadEvent(context.Background(), scheduling.ReadRequest{ID: "event-1"})
					if err != nil {
						return err
					}
					if item.TimeZone != "America/Los_Angeles" {
						return fmt.Errorf("event timezone = %q, want America/Los_Angeles", item.TimeZone)
					}
					if item.End.Sub(item.Start) != 45*time.Minute {
						return fmt.Errorf("event duration = %s, want 45m", item.End.Sub(item.Start))
					}
					return nil
				},
			},
		},
	}
}

type conflictScheduleStore struct {
	mu               sync.Mutex
	event            scheduling.Event
	conflictingEvent scheduling.Event
	conflictInjected bool
}

func newConflictScheduleStore(initial scheduling.Event, conflicting scheduling.Event) *conflictScheduleStore {
	return &conflictScheduleStore{
		event:            cloneScheduleEvent(initial),
		conflictingEvent: cloneScheduleEvent(conflicting),
	}
}

func (s *conflictScheduleStore) SearchEvents(ctx context.Context, req scheduling.SearchRequest) ([]scheduling.Event, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s == nil {
		return nil, fmt.Errorf("nil conflict schedule store")
	}
	s.mu.Lock()
	current := cloneScheduleEvent(s.event)
	s.mu.Unlock()
	return (scheduling.Selector{MaxEvents: req.Limit}).Select([]scheduling.Event{current}, req.Query, req.WindowStart, req.WindowEnd), nil
}

func (s *conflictScheduleStore) ReadEvent(ctx context.Context, req scheduling.ReadRequest) (scheduling.Event, error) {
	if err := ctx.Err(); err != nil {
		return scheduling.Event{}, err
	}
	if s == nil {
		return scheduling.Event{}, fmt.Errorf("nil conflict schedule store")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !matchesScheduleLookup(s.event, req.ID, req.Title) {
		return scheduling.Event{}, fmt.Errorf("schedule event not found")
	}
	return cloneScheduleEvent(s.event), nil
}

func (s *conflictScheduleStore) RescheduleEvent(ctx context.Context, req scheduling.RescheduleRequest) (scheduling.RescheduleResult, error) {
	if err := ctx.Err(); err != nil {
		return scheduling.RescheduleResult{}, err
	}
	if s == nil {
		return scheduling.RescheduleResult{}, fmt.Errorf("nil conflict schedule store")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !matchesScheduleLookup(s.event, req.ID, req.Title) {
		return scheduling.RescheduleResult{}, fmt.Errorf("schedule event not found")
	}
	if !s.conflictInjected {
		s.event = cloneScheduleEvent(s.conflictingEvent)
		s.conflictInjected = true
		return scheduling.RescheduleResult{}, fmt.Errorf("schedule event changed during reschedule; read the latest event and retry")
	}
	previous := cloneScheduleEvent(s.event)
	updated := cloneScheduleEvent(s.event)
	updated.Start = req.Start.UTC()
	updated.End = req.End.UTC()
	if strings.TrimSpace(req.TimeZone) != "" {
		updated.TimeZone = strings.TrimSpace(req.TimeZone)
	}
	if len(req.Metadata) > 0 {
		if updated.Metadata == nil {
			updated.Metadata = make(map[string]any, len(req.Metadata))
		}
		for key, value := range req.Metadata {
			updated.Metadata[key] = value
		}
	}
	s.event = updated
	return scheduling.RescheduleResult{
		Event:       cloneScheduleEvent(updated),
		Previous:    previous,
		Rescheduled: true,
	}, nil
}

func matchesScheduleLookup(item scheduling.Event, id string, title string) bool {
	id = strings.TrimSpace(id)
	title = strings.TrimSpace(title)
	switch {
	case id != "":
		return item.ID == id
	case title != "":
		return item.Title == title
	default:
		return false
	}
}

func cloneScheduleEvent(item scheduling.Event) scheduling.Event {
	out := item
	out.Attendees = append([]scheduling.Participant(nil), item.Attendees...)
	out.Tags = append([]string(nil), item.Tags...)
	out.Metadata = model.CloneMetadata(item.Metadata)
	return out
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
