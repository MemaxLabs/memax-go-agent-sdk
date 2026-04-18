package scenarios

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	memaxagent "github.com/MemaxLabs/memax-go-agent-sdk"
	"github.com/MemaxLabs/memax-go-agent-sdk/agenteval"
	"github.com/MemaxLabs/memax-go-agent-sdk/memory"
	"github.com/MemaxLabs/memax-go-agent-sdk/messaging"
	"github.com/MemaxLabs/memax-go-agent-sdk/model"
	"github.com/MemaxLabs/memax-go-agent-sdk/notes"
	"github.com/MemaxLabs/memax-go-agent-sdk/session"
	"github.com/MemaxLabs/memax-go-agent-sdk/stack/personal"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/agentpolicy"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/approvaltools"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/memorytools"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/messagetools"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/notetools"
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
						"Search note, document, and message-thread metadata before loading full content or sending new replies.",
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
						"Search note, document, and message-thread metadata before loading full content or sending new replies.",
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
