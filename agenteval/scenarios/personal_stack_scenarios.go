package scenarios

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"time"

	memaxagent "github.com/MemaxLabs/memax-go-agent-sdk"
	"github.com/MemaxLabs/memax-go-agent-sdk/agenteval"
	"github.com/MemaxLabs/memax-go-agent-sdk/memory"
	"github.com/MemaxLabs/memax-go-agent-sdk/messaging"
	"github.com/MemaxLabs/memax-go-agent-sdk/messaging/jmapclient"
	"github.com/MemaxLabs/memax-go-agent-sdk/messaging/jmapstore"
	"github.com/MemaxLabs/memax-go-agent-sdk/model"
	"github.com/MemaxLabs/memax-go-agent-sdk/notes"
	"github.com/MemaxLabs/memax-go-agent-sdk/scheduling"
	"github.com/MemaxLabs/memax-go-agent-sdk/session"
	"github.com/MemaxLabs/memax-go-agent-sdk/stack/personal"
	personalsqlitestore "github.com/MemaxLabs/memax-go-agent-sdk/stack/personal/sqlitestore"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/agentpolicy"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/approvaltools"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/memorytools"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/messagetools"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/notetools"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/scheduletools"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/subagents"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/tasktools"
	tasksqlitestore "github.com/MemaxLabs/memax-go-agent-sdk/toolkit/tasktools/sqlitestore"
	_ "modernc.org/sqlite"
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

// PersonalPresetAssistantWeekAheadPlanning returns a single-use scenario where
// the personal_assistant preset builds a week-ahead plan by composing durable
// memory, notes, unread inbox metadata, and calendar metadata before reading
// only the selected source details needed to synthesize conflicts,
// commitments, and prep work.
func PersonalPresetAssistantWeekAheadPlanning() agenteval.Case {
	memoryStore := memory.NewMemoryStore([]memory.Memory{{
		ID:      "memory-1",
		Name:    "week-planning-style",
		Scope:   memory.ScopeUser,
		Content: "For week-ahead plans, lead with hard conflicts, then commitments, prep blocks, and owner-visible follow-ups. Use explicit UTC times.",
		Tags:    []string{"planning", "weekly"},
	}})
	noteStore := notes.NewNoteStore([]notes.Note{{
		ID:        "note-1",
		Title:     "Q2 launch planning brief",
		Kind:      "brief",
		Summary:   "Launch brief covering Acme renewal, pricing review, and partner council readiness.",
		Content:   "The Q2 launch depends on unblocking the Acme renewal, finishing pricing review by Wednesday, and preparing the partner council demo before Thursday.",
		Tags:      []string{"planning", "q2-launch", "acme"},
		CreatedAt: time.Date(2026, 4, 18, 16, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 4, 18, 16, 30, 0, 0, time.UTC),
	}})
	messageStore := messaging.NewThreadStore([]messaging.Thread{
		{
			ID:      "thread-1",
			Subject: "Acme renewal blocker",
			Summary: "Casey needs a Monday 14:00 UTC checkout-blocker checkpoint before the renewal meeting.",
			Participants: []messaging.Participant{
				{Name: "Casey", Address: "casey@acme.example", Role: "from"},
			},
			Tags:          []string{"INBOX", "customer", "urgent"},
			LastMessageAt: time.Date(2026, 4, 19, 12, 0, 0, 0, time.UTC),
			Metadata:      map[string]any{"unread": true},
			Messages: []messaging.Message{{
				ID:        "thread-1-msg-1",
				ThreadID:  "thread-1",
				Subject:   "Acme renewal blocker",
				Summary:   "Checkout blocker needs an explicit Monday checkpoint.",
				Body:      "Checkout is still blocked. Please send the Monday 14:00 UTC checkpoint with the mitigation owner and the next customer-visible update.",
				Direction: messaging.DirectionInbound,
				Sender:    messaging.Participant{Name: "Casey", Address: "casey@acme.example", Role: "from"},
				SentAt:    time.Date(2026, 4, 19, 12, 0, 0, 0, time.UTC),
			}},
		},
		{
			ID:      "thread-2",
			Subject: "Partner council demo slides",
			Summary: "Priya needs final demo slides by Wednesday 17:00 UTC for Thursday's council.",
			Participants: []messaging.Participant{
				{Name: "Priya", Address: "priya@example.com", Role: "from"},
			},
			Tags:          []string{"INBOX", "launch", "prep"},
			LastMessageAt: time.Date(2026, 4, 19, 13, 0, 0, 0, time.UTC),
			Metadata:      map[string]any{"unread": true},
			Messages: []messaging.Message{{
				ID:        "thread-2-msg-1",
				ThreadID:  "thread-2",
				Subject:   "Partner council demo slides",
				Summary:   "Demo slides due Wednesday for partner council.",
				Body:      "Please have the final demo slides ready by Wednesday 17:00 UTC so I can package them for Thursday's partner council.",
				Direction: messaging.DirectionInbound,
				Sender:    messaging.Participant{Name: "Priya", Address: "priya@example.com", Role: "from"},
				SentAt:    time.Date(2026, 4, 19, 13, 0, 0, 0, time.UTC),
			}},
		},
	})
	mondayAcme := time.Date(2026, 4, 20, 13, 30, 0, 0, time.UTC)
	mondayRisk := time.Date(2026, 4, 20, 14, 0, 0, 0, time.UTC)
	thursdayCouncil := time.Date(2026, 4, 23, 16, 0, 0, 0, time.UTC)
	scheduleStore := scheduling.NewEventStore([]scheduling.Event{
		{
			ID:       "event-1",
			Title:    "Acme renewal meeting",
			Summary:  "Discuss checkout blocker and renewal status with Casey.",
			Location: "Video",
			Organizer: scheduling.Participant{
				Name:    "Casey",
				Address: "casey@acme.example",
			},
			Start:       mondayAcme,
			End:         mondayAcme.Add(time.Hour),
			TimeZone:    "UTC",
			Description: "Bring checkout status, mitigation owner, and the Monday 14:00 UTC customer checkpoint plan.",
			Tags:        []string{"customer", "renewal", "acme"},
		},
		{
			ID:       "event-2",
			Title:    "Internal launch risk review",
			Summary:  "Review launch risks and pricing readiness before the Q2 checkpoint.",
			Location: "Room 3B",
			Organizer: scheduling.Participant{
				Name:    "Taylor",
				Address: "taylor@example.com",
			},
			Start:       mondayRisk,
			End:         mondayRisk.Add(30 * time.Minute),
			TimeZone:    "UTC",
			Description: "This overlaps the customer checkpoint; bring the risk register and decide whether to move the internal review.",
			Tags:        []string{"launch", "risk"},
		},
		{
			ID:       "event-3",
			Title:    "Partner council demo",
			Summary:  "Demo Q2 launch readiness to the partner council.",
			Location: "Boardroom",
			Organizer: scheduling.Participant{
				Name:    "Priya",
				Address: "priya@example.com",
			},
			Start:       thursdayCouncil,
			End:         thursdayCouncil.Add(time.Hour),
			TimeZone:    "UTC",
			Description: "Requires final demo slides, pricing review packet, and the Acme mitigation summary before the council.",
			Tags:        []string{"partner", "launch", "demo"},
		},
	})
	taskStore := tasktools.NewMemoryStore([]tasktools.Task{{
		ID:     "task-1",
		Title:  "assemble the week-ahead plan",
		Status: tasktools.StatusInProgress,
		Notes:  "search memory, notes, unread inbox, and calendar metadata first; read only selected details before synthesizing conflicts, commitments, prep blocks, and follow-ups",
	}})

	messageSearchInput := `{
		"query":"Acme renewal blocker partner council demo slides",
		"mailboxes":["INBOX"],
		"unread":true,
		"since":"2026-04-19T00:00:00Z",
		"until":"2026-04-27T00:00:00Z",
		"limit":4
	}`
	scheduleSearchInput := `{
		"query":"Acme renewal launch risk review partner council demo",
		"start":"2026-04-20T00:00:00Z",
		"end":"2026-04-27T00:00:00Z",
		"limit":5
	}`
	finalText := "Week-ahead plan: Conflict first: Monday 13:30-14:30 UTC Acme renewal meeting overlaps the 14:00-14:30 UTC internal launch risk review, so protect the customer checkpoint and move or shorten the internal review. Commitments: send Casey the 14:00 UTC blocker checkpoint and deliver Priya's partner council demo slides by Wednesday 17:00 UTC. Prep: use the Q2 launch brief, checkout mitigation owner, pricing review packet, and Acme mitigation summary before Thursday 16:00 UTC partner council. Follow-ups: confirm the mitigation owner with Casey and give Priya the final demo-slide package."
	modelClient := agenteval.NewScriptedModel(
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:    "search-memory-1",
				Name:  memorytools.SearchToolName,
				Input: json.RawMessage(`{"query":"week ahead planning conflicts commitments prep follow-ups","limit":3}`),
			},
		}},
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:    "search-note-1",
				Name:  notetools.SearchToolName,
				Input: json.RawMessage(`{"query":"Q2 launch planning Acme renewal partner council demo","limit":3}`),
			},
		}},
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:    "search-message-1",
				Name:  messagetools.SearchToolName,
				Input: json.RawMessage(messageSearchInput),
			},
		}},
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:    "search-schedule-1",
				Name:  scheduletools.SearchToolName,
				Input: json.RawMessage(scheduleSearchInput),
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
				ID:    "read-thread-2",
				Name:  messagetools.ReadToolName,
				Input: json.RawMessage(`{"thread_id":"thread-2"}`),
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
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:    "read-event-2",
				Name:  scheduletools.ReadToolName,
				Input: json.RawMessage(`{"id":"event-2"}`),
			},
		}},
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:    "read-event-3",
				Name:  scheduletools.ReadToolName,
				Input: json.RawMessage(`{"id":"event-3"}`),
			},
		}},
		[]model.StreamEvent{{Kind: model.StreamText, Text: finalText}},
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
		DefaultLimit: 4,
	}
	config.Schedule = scheduletools.Config{
		Searcher:     scheduleStore,
		Reader:       scheduleStore,
		DefaultLimit: 5,
	}
	config.Tasks = taskStore
	stack, stackErr := personal.New(config)

	return agenteval.Case{
		Name:    "personal_preset_personal_assistant_week_ahead_planning",
		Prompt:  "Prepare my week-ahead plan for 2026-04-20 through 2026-04-26. Search durable context, notes, unread inbox metadata, and calendar metadata first; read only what you need to produce conflicts, commitments, prep blocks, and follow-ups.",
		Options: stack.WithModel(modelClient),
		Assertions: []agenteval.Assertion{
			toolConstructionSucceeded(configErr, stackErr),
			agenteval.NoToolErrors(),
			agenteval.ToolUsed(memorytools.SearchToolName),
			agenteval.ToolUsed(notetools.SearchToolName),
			agenteval.ToolUsed(messagetools.SearchToolName),
			agenteval.ToolUsed(scheduletools.SearchToolName),
			agenteval.FinalEquals(finalText),
			requestCountEquals(modelClient, 11),
			{
				Name: "week planning prompt carries durable style and active task",
				Check: func(result agenteval.Result) error {
					requests := modelClient.Requests()
					if len(requests) != 11 {
						return fmt.Errorf("requests = %d, want 11", len(requests))
					}
					initialPrompt := requests[0].SystemPrompt
					for _, want := range []string{
						"For week-ahead plans, lead with hard conflicts",
						"[in_progress] task-1",
						"assemble the week-ahead plan",
						memorytools.SearchToolName,
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
				Name: "week planning uses portable inbox and calendar window filters",
				Check: func(result agenteval.Result) error {
					uses := result.ToolUses()
					want := []string{
						memorytools.SearchToolName,
						notetools.SearchToolName,
						messagetools.SearchToolName,
						scheduletools.SearchToolName,
						notetools.ReadToolName,
						messagetools.ReadToolName,
						messagetools.ReadToolName,
						scheduletools.ReadToolName,
						scheduletools.ReadToolName,
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
					messageInput := string(uses[2].Input)
					for _, want := range []string{`"mailboxes":["INBOX"]`, `"unread":true`, `"since":"2026-04-19T00:00:00Z"`, `"until":"2026-04-27T00:00:00Z"`} {
						if !strings.Contains(messageInput, want) {
							return fmt.Errorf("message search input = %s, missing %s", messageInput, want)
						}
					}
					scheduleInput := string(uses[3].Input)
					for _, want := range []string{`"start":"2026-04-20T00:00:00Z"`, `"end":"2026-04-27T00:00:00Z"`} {
						if !strings.Contains(scheduleInput, want) {
							return fmt.Errorf("schedule search input = %s, missing %s", scheduleInput, want)
						}
					}
					return nil
				},
			},
			{
				Name: "week planning keeps note message and schedule bodies behind reads",
				Check: func(result agenteval.Result) error {
					toolResults := result.ToolResults()
					if len(toolResults) != 10 {
						return fmt.Errorf("tool results = %#v, want 10 search/read results", toolResults)
					}
					if strings.Contains(toolResults[1].Content, "finishing pricing review by Wednesday") {
						return fmt.Errorf("note search leaked full note content: %q", toolResults[1].Content)
					}
					if strings.Contains(toolResults[2].Content, "mitigation owner and the next customer-visible update") {
						return fmt.Errorf("message search leaked full Acme body: %q", toolResults[2].Content)
					}
					if strings.Contains(toolResults[2].Content, "package them for Thursday's partner council") {
						return fmt.Errorf("message search leaked full partner body: %q", toolResults[2].Content)
					}
					if strings.Contains(toolResults[3].Content, "Bring checkout status") || strings.Contains(toolResults[3].Content, "Requires final demo slides") {
						return fmt.Errorf("schedule search leaked full event description: %q", toolResults[3].Content)
					}
					if !strings.Contains(toolResults[4].Content, "finishing pricing review by Wednesday") {
						return fmt.Errorf("note read content = %q, want full launch brief", toolResults[4].Content)
					}
					if !strings.Contains(toolResults[5].Content, "mitigation owner and the next customer-visible update") {
						return fmt.Errorf("Acme read content = %q, want full thread body", toolResults[5].Content)
					}
					if !strings.Contains(toolResults[6].Content, "package them for Thursday's partner council") {
						return fmt.Errorf("partner read content = %q, want full thread body", toolResults[6].Content)
					}
					if !strings.Contains(toolResults[7].Content, "Bring checkout status") || !strings.Contains(toolResults[8].Content, "overlaps the customer checkpoint") || !strings.Contains(toolResults[9].Content, "Requires final demo slides") {
						return fmt.Errorf("event reads = %#v, want full event descriptions", toolResults[7:10])
					}
					return nil
				},
			},
			{
				Name: "week plan synthesizes conflict commitments and prep across sources",
				Check: func(result agenteval.Result) error {
					final := result.Final
					for _, want := range []string{
						"Monday 13:30-14:30 UTC Acme renewal meeting overlaps",
						"send Casey the 14:00 UTC blocker checkpoint",
						"Wednesday 17:00 UTC",
						"Thursday 16:00 UTC partner council",
						"pricing review packet",
						"Follow-ups: confirm the mitigation owner with Casey",
					} {
						if !strings.Contains(final, want) {
							return fmt.Errorf("final = %q, missing %q", final, want)
						}
					}
					return nil
				},
			},
		},
	}
}

type weekAheadTaskLedgerStore struct {
	Open func(context.Context) (tasktools.Store, func(context.Context) (tasktools.Store, error), func(), error)
}

func newMemoryWeekAheadTaskLedgerStore() weekAheadTaskLedgerStore {
	return weekAheadTaskLedgerStore{
		Open: func(ctx context.Context) (tasktools.Store, func(context.Context) (tasktools.Store, error), func(), error) {
			if err := ctx.Err(); err != nil {
				return nil, nil, nil, err
			}
			store := tasktools.NewMemoryStore(nil)
			reopen := func(ctx context.Context) (tasktools.Store, error) {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
				return store, nil
			}
			return store, reopen, nil, nil
		},
	}
}

func newSQLiteWeekAheadTaskLedgerStore() weekAheadTaskLedgerStore {
	return weekAheadTaskLedgerStore{
		Open: func(ctx context.Context) (tasktools.Store, func(context.Context) (tasktools.Store, error), func(), error) {
			if err := ctx.Err(); err != nil {
				return nil, nil, nil, err
			}
			file, err := os.CreateTemp("", "memax-week-ahead-task-ledger-*.db")
			if err != nil {
				return nil, nil, nil, fmt.Errorf("create sqlite task ledger temp file: %w", err)
			}
			path := file.Name()
			_ = file.Close()

			var activeDB *sql.DB
			cleanup := func() {
				if activeDB != nil {
					_ = activeDB.Close()
				}
				_ = os.Remove(path)
			}
			openStore := func(ctx context.Context) (tasktools.Store, error) {
				if activeDB != nil {
					db := activeDB
					activeDB = nil
					if err := db.Close(); err != nil {
						return nil, fmt.Errorf("close sqlite task ledger db: %w", err)
					}
				}
				db, err := sql.Open("sqlite", path)
				if err != nil {
					return nil, fmt.Errorf("open sqlite task ledger db: %w", err)
				}
				store, err := tasksqlitestore.New(ctx, db)
				if err != nil {
					_ = db.Close()
					return nil, err
				}
				activeDB = db
				return store, nil
			}
			store, err := openStore(ctx)
			if err != nil {
				cleanup()
				return nil, nil, nil, err
			}
			return store, openStore, cleanup, nil
		},
	}
}

// PersonalPresetAssistantWeekAheadTaskLedger returns a two-run scenario where
// week-ahead planning persists follow-up work as durable tasks, and a later run
// resumes from that task ledger instead of rediscovering or duplicating work.
func PersonalPresetAssistantWeekAheadTaskLedger() agenteval.Case {
	return personalPresetAssistantWeekAheadTaskLedgerCase(
		"personal_preset_personal_assistant_week_ahead_task_ledger",
		newMemoryWeekAheadTaskLedgerStore(),
	)
}

// PersonalPresetAssistantWeekAheadTaskLedgerSQLite returns the same two-run
// task-ledger workflow backed by a SQLite store that is reopened before resume.
func PersonalPresetAssistantWeekAheadTaskLedgerSQLite() agenteval.Case {
	return personalPresetAssistantWeekAheadTaskLedgerCase(
		"personal_preset_personal_assistant_week_ahead_task_ledger_sqlite",
		newSQLiteWeekAheadTaskLedgerStore(),
	)
}

func personalPresetAssistantWeekAheadTaskLedgerCase(name string, ledger weekAheadTaskLedgerStore) agenteval.Case {
	memoryStore := memory.NewMemoryStore([]memory.Memory{{
		ID:      "memory-1",
		Name:    "week-planning-style",
		Scope:   memory.ScopeUser,
		Content: "For week-ahead plans, convert owner-visible follow-ups into explicit task state with deterministic IDs.",
		Tags:    []string{"planning", "tasks"},
	}})
	messageStore := messaging.NewThreadStore([]messaging.Thread{
		{
			ID:      "thread-1",
			Subject: "Acme mitigation owner",
			Summary: "Casey needs the mitigation owner confirmed before the Monday checkpoint.",
			Participants: []messaging.Participant{
				{Name: "Casey", Address: "casey@acme.example", Role: "from"},
			},
			Tags:          []string{"INBOX", "customer", "urgent"},
			LastMessageAt: time.Date(2026, 4, 19, 12, 0, 0, 0, time.UTC),
			Metadata:      map[string]any{"unread": true},
			Messages: []messaging.Message{{
				ID:        "thread-1-msg-1",
				ThreadID:  "thread-1",
				Subject:   "Acme mitigation owner",
				Summary:   "Mitigation owner needed before Monday checkpoint.",
				Body:      "Please confirm the Acme checkout mitigation owner before the Monday 14:00 UTC customer-visible checkpoint.",
				Direction: messaging.DirectionInbound,
				Sender:    messaging.Participant{Name: "Casey", Address: "casey@acme.example", Role: "from"},
				SentAt:    time.Date(2026, 4, 19, 12, 0, 0, 0, time.UTC),
			}},
		},
		{
			ID:      "thread-2",
			Subject: "Partner council demo slides",
			Summary: "Priya needs final demo slides by Wednesday 17:00 UTC.",
			Participants: []messaging.Participant{
				{Name: "Priya", Address: "priya@example.com", Role: "from"},
			},
			Tags:          []string{"INBOX", "launch", "prep"},
			LastMessageAt: time.Date(2026, 4, 19, 13, 0, 0, 0, time.UTC),
			Metadata:      map[string]any{"unread": true},
			Messages: []messaging.Message{{
				ID:        "thread-2-msg-1",
				ThreadID:  "thread-2",
				Subject:   "Partner council demo slides",
				Summary:   "Demo slides due Wednesday for partner council.",
				Body:      "Please send the final partner council demo slides by Wednesday 17:00 UTC so I can package them for Thursday.",
				Direction: messaging.DirectionInbound,
				Sender:    messaging.Participant{Name: "Priya", Address: "priya@example.com", Role: "from"},
				SentAt:    time.Date(2026, 4, 19, 13, 0, 0, 0, time.UTC),
			}},
		},
	})
	mondayAcme := time.Date(2026, 4, 20, 13, 30, 0, 0, time.UTC)
	thursdayCouncil := time.Date(2026, 4, 23, 16, 0, 0, 0, time.UTC)
	scheduleStore := scheduling.NewEventStore([]scheduling.Event{
		{
			ID:       "event-1",
			Title:    "Acme renewal meeting",
			Summary:  "Customer renewal meeting needs a named mitigation owner.",
			Location: "Video",
			Organizer: scheduling.Participant{
				Name:    "Casey",
				Address: "casey@acme.example",
			},
			Start:       mondayAcme,
			End:         mondayAcme.Add(time.Hour),
			TimeZone:    "UTC",
			Description: "Bring checkout status, mitigation owner, and the Monday 14:00 UTC customer checkpoint plan.",
			Tags:        []string{"customer", "renewal", "acme"},
		},
		{
			ID:       "event-2",
			Title:    "Partner council demo",
			Summary:  "Demo Q2 launch readiness to the partner council.",
			Location: "Boardroom",
			Organizer: scheduling.Participant{
				Name:    "Priya",
				Address: "priya@example.com",
			},
			Start:       thursdayCouncil,
			End:         thursdayCouncil.Add(time.Hour),
			TimeZone:    "UTC",
			Description: "Requires final demo slides and the Acme mitigation summary before the council.",
			Tags:        []string{"partner", "launch", "demo"},
		},
	})
	initialTask := tasktools.Task{
		ID:     "task-1",
		Title:  "assemble the week-ahead plan",
		Status: tasktools.StatusInProgress,
		Notes:  "persist owner-visible follow-ups as durable tasks with deterministic IDs",
	}
	var finalTaskStore tasktools.Store
	firstFinal := "Week-ahead task ledger updated: created follow-up tasks for Acme mitigation owner and partner council demo slides."
	secondFinal := "Resumed week-ahead task ledger: Acme owner follow-up is complete; partner council demo slides remain pending."
	modelClient := agenteval.NewScriptedModel(
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:    "search-memory-1",
				Name:  memorytools.SearchToolName,
				Input: json.RawMessage(`{"query":"week ahead task ledger follow-ups","limit":3}`),
			},
		}},
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:    "search-message-1",
				Name:  messagetools.SearchToolName,
				Input: json.RawMessage(`{"query":"Acme owner partner council demo slides","mailboxes":["INBOX"],"unread":true,"since":"2026-04-19T00:00:00Z","until":"2026-04-27T00:00:00Z","limit":4}`),
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
				ID:    "read-thread-2",
				Name:  messagetools.ReadToolName,
				Input: json.RawMessage(`{"thread_id":"thread-2"}`),
			},
		}},
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:    "search-schedule-1",
				Name:  scheduletools.SearchToolName,
				Input: json.RawMessage(`{"query":"Acme renewal partner council demo","start":"2026-04-20T00:00:00Z","end":"2026-04-27T00:00:00Z","limit":5}`),
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
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:    "read-event-2",
				Name:  scheduletools.ReadToolName,
				Input: json.RawMessage(`{"id":"event-2"}`),
			},
		}},
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:   "upsert-task-1",
				Name: tasktools.UpsertToolName,
				Input: json.RawMessage(`{
					"id":"week-2026-04-20-acme-owner",
					"title":"Confirm Acme mitigation owner",
					"status":"pending",
					"notes":"Casey needs the mitigation owner before the Monday 14:00 UTC customer checkpoint.",
					"priority":1,
					"evidence":["thread-1","event-1"]
				}`),
			},
		}},
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:   "upsert-task-2",
				Name: tasktools.UpsertToolName,
				Input: json.RawMessage(`{
					"id":"week-2026-04-20-demo-slides",
					"title":"Deliver partner council demo slides",
					"status":"pending",
					"notes":"Priya needs final demo slides by Wednesday 17:00 UTC for Thursday partner council.",
					"priority":2,
					"evidence":["thread-2","event-2"]
				}`),
			},
		}},
		[]model.StreamEvent{{Kind: model.StreamText, Text: firstFinal}},
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:    "list-tasks-1",
				Name:  tasktools.ListToolName,
				Input: json.RawMessage(`{"status":"pending"}`),
			},
		}},
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:   "complete-task-1",
				Name: tasktools.UpsertToolName,
				Input: json.RawMessage(`{
					"id":"week-2026-04-20-acme-owner",
					"status":"completed",
					"notes":"Mitigation owner confirmed for the Monday 14:00 UTC customer checkpoint.",
					"evidence":["thread-1","event-1","owner-confirmed"]
				}`),
			},
		}},
		[]model.StreamEvent{{Kind: model.StreamText, Text: secondFinal}},
	)

	config, configErr := personal.PresetPersonalAssistant.Config()
	config.Memory = memorytools.Config{
		Source:       memoryStore,
		DefaultLimit: 3,
	}
	config.Messages = messagetools.Config{
		Searcher:     messageStore,
		Reader:       messageStore,
		DefaultLimit: 4,
	}
	config.Schedule = scheduletools.Config{
		Searcher:     scheduleStore,
		Reader:       scheduleStore,
		DefaultLimit: 5,
	}
	prompts := []string{
		"Prepare my week-ahead follow-up ledger for 2026-04-20 through 2026-04-26. Search durable context, unread inbox metadata, and calendar metadata first; read only selected details, then persist each owner-visible follow-up as a task with a deterministic ID.",
		"Resume the pending week-ahead follow-ups from the task ledger. Do not rediscover or duplicate tasks; list pending tasks and update only completed work.",
	}
	var cleanup func()

	return agenteval.Case{
		Name:   name,
		Prompt: strings.Join(prompts, " / "),
		Cleanup: func() {
			if cleanup != nil {
				cleanup()
			}
		},
		Run: func(ctx context.Context) (<-chan memaxagent.Event, error) {
			if configErr != nil {
				return nil, configErr
			}
			if ledger.Open == nil {
				return nil, fmt.Errorf("task ledger opener is nil")
			}
			taskStore, reopen, closeLedger, err := ledger.Open(ctx)
			if err != nil {
				return nil, err
			}
			cleanup = closeLedger
			if taskStore == nil {
				return nil, fmt.Errorf("task ledger store is nil")
			}
			if reopen == nil {
				return nil, fmt.Errorf("task ledger reopen is nil")
			}
			if _, err := taskStore.Upsert(ctx, initialTask); err != nil {
				return nil, err
			}
			runConfig := config
			runConfig.Tasks = taskStore
			stack, stackErr := personal.New(runConfig)
			if stackErr != nil {
				return nil, stackErr
			}
			finalTaskStore = taskStore
			out := make(chan memaxagent.Event)
			go func() {
				defer close(out)
				runStack := stack
				for i, prompt := range prompts {
					if i == 1 {
						resumeStore, err := reopen(ctx)
						if err != nil {
							select {
							case out <- memaxagent.Event{Kind: memaxagent.EventError, Err: err, Time: time.Now().UTC()}:
							case <-ctx.Done():
							}
							return
						}
						resumeConfig := config
						resumeConfig.Tasks = resumeStore
						resumeStack, err := personal.New(resumeConfig)
						if err != nil {
							select {
							case out <- memaxagent.Event{Kind: memaxagent.EventError, Err: err, Time: time.Now().UTC()}:
							case <-ctx.Done():
							}
							return
						}
						finalTaskStore = resumeStore
						runStack = resumeStack
					}
					events, err := memaxagent.Query(ctx, prompt, runStack.WithModel(modelClient))
					if err != nil {
						select {
						case out <- memaxagent.Event{Kind: memaxagent.EventError, Err: err, Time: time.Now().UTC()}:
						case <-ctx.Done():
						}
						return
					}
					for event := range events {
						select {
						case out <- event:
						case <-ctx.Done():
							return
						}
					}
				}
			}()
			return out, nil
		},
		Assertions: []agenteval.Assertion{
			toolConstructionSucceeded(configErr),
			agenteval.NoToolErrors(),
			agenteval.ToolUsed(tasktools.UpsertToolName),
			agenteval.ToolUsed(tasktools.ListToolName),
			agenteval.FinalEquals(secondFinal),
			requestCountEquals(modelClient, 13),
			{
				Name: "task mutations carry standard task metadata",
				Check: func(result agenteval.Result) error {
					results := result.ToolResults()
					if len(results) != 11 {
						return fmt.Errorf("tool results = %#v, want 11 results", results)
					}
					byID := make(map[string]model.ToolResult, len(results))
					for _, toolResult := range results {
						byID[toolResult.ToolUseID] = toolResult
					}
					firstTask := byID["upsert-task-1"]
					if firstTask.Metadata[model.MetadataTaskID] != "week-2026-04-20-acme-owner" || firstTask.Metadata[model.MetadataTaskStatus] != string(tasktools.StatusPending) {
						return fmt.Errorf("first task metadata = %#v, want pending Acme follow-up", firstTask.Metadata)
					}
					secondTask := byID["upsert-task-2"]
					if secondTask.Metadata[model.MetadataTaskID] != "week-2026-04-20-demo-slides" || secondTask.Metadata[model.MetadataTaskStatus] != string(tasktools.StatusPending) {
						return fmt.Errorf("second task metadata = %#v, want pending demo follow-up", secondTask.Metadata)
					}
					completedTask := byID["complete-task-1"]
					if completedTask.Metadata[model.MetadataTaskID] != "week-2026-04-20-acme-owner" || completedTask.Metadata[model.MetadataTaskStatus] != string(tasktools.StatusCompleted) {
						return fmt.Errorf("completed task metadata = %#v, want completed Acme follow-up", completedTask.Metadata)
					}
					return nil
				},
			},
			{
				Name: "second run resumes from pending task state before listing",
				Check: func(result agenteval.Result) error {
					requests := modelClient.Requests()
					if len(requests) != 13 {
						return fmt.Errorf("requests = %d, want 13", len(requests))
					}
					secondInitial := requests[10].SystemPrompt
					for _, want := range []string{
						"[pending] week-2026-04-20-acme-owner",
						"Confirm Acme mitigation owner",
						"[pending] week-2026-04-20-demo-slides",
						"Deliver partner council demo slides",
					} {
						if !strings.Contains(secondInitial, want) {
							return fmt.Errorf("second run prompt missing %q:\n%s", want, secondInitial)
						}
					}
					return nil
				},
			},
			{
				Name: "ledger persists follow-ups without duplicates",
				Check: func(result agenteval.Result) error {
					if finalTaskStore == nil {
						return fmt.Errorf("final task ledger store is nil")
					}
					tasks, err := finalTaskStore.List(context.Background())
					if err != nil {
						return err
					}
					byID := make(map[string]tasktools.Task, len(tasks))
					for _, task := range tasks {
						if _, exists := byID[task.ID]; exists {
							return fmt.Errorf("duplicate task id %q in %#v", task.ID, tasks)
						}
						byID[task.ID] = task
					}
					if len(tasks) != 3 {
						return fmt.Errorf("tasks = %#v, want initial plan task plus two follow-ups", tasks)
					}
					if byID["week-2026-04-20-acme-owner"].Status != tasktools.StatusCompleted {
						return fmt.Errorf("Acme follow-up = %#v, want completed", byID["week-2026-04-20-acme-owner"])
					}
					if byID["week-2026-04-20-demo-slides"].Status != tasktools.StatusPending {
						return fmt.Errorf("demo follow-up = %#v, want pending", byID["week-2026-04-20-demo-slides"])
					}
					if !strings.Contains(strings.Join(byID["week-2026-04-20-acme-owner"].Evidence, ","), "owner-confirmed") {
						return fmt.Errorf("Acme evidence = %#v, want owner-confirmed evidence", byID["week-2026-04-20-acme-owner"].Evidence)
					}
					return nil
				},
			},
			{
				Name: "resume lists pending tasks before completing one",
				Check: func(result agenteval.Result) error {
					results := result.ToolResults()
					if len(results) < 10 {
						return fmt.Errorf("tool results = %#v, want list result", results)
					}
					list := results[9].Content
					for _, want := range []string{"week-2026-04-20-acme-owner", "week-2026-04-20-demo-slides"} {
						if !strings.Contains(list, want) {
							return fmt.Errorf("pending list = %q, missing %q", list, want)
						}
					}
					return nil
				},
			},
		},
	}
}

// PersonalPresetAssistantScheduledDailyBriefing returns a single-use scenario
// where the personal_assistant preset runs a proactive daily briefing trigger
// once for its deterministic occurrence, persists the run, and treats a second
// fire for the same occurrence as a no-op.
func PersonalPresetAssistantScheduledDailyBriefing() agenteval.Case {
	modelClient := agenteval.NewScriptedModel(
		[]model.StreamEvent{{Kind: model.StreamText, Text: "Morning briefing ready for 2026-04-19."}},
	)

	config, configErr := personal.PresetPersonalAssistant.Config()
	if configErr == nil {
		config.Base.Model = modelClient
	}
	stack, stackErr := personal.New(config)
	store := personal.NewMemoryScheduledRunStore()
	now := time.Date(2026, 4, 19, 7, 1, 0, 0, time.UTC)
	trigger := personal.PeriodicTrigger{
		Name:   "daily-brief",
		Prompt: "Prepare the morning briefing for 2026-04-19.",
		Every:  24 * time.Hour,
		Anchor: time.Date(2026, 4, 19, 7, 0, 0, 0, time.UTC),
	}

	var (
		finalRun       personal.ScheduledRunRecord
		duplicateRun   personal.ScheduledRunRecord
		duplicateStart bool
		fireErr        error
	)

	return agenteval.Case{
		Name:   "personal_preset_personal_assistant_scheduled_daily_briefing",
		Prompt: "Run the daily briefing trigger proactively for the current occurrence.",
		Run: func(ctx context.Context) (<-chan memaxagent.Event, error) {
			if stackErr != nil {
				return nil, stackErr
			}
			out := make(chan memaxagent.Event, 32)
			observer := memaxagent.EventObserverFunc(func(_ context.Context, event memaxagent.Event) {
				select {
				case out <- event:
				case <-ctx.Done():
				}
			})
			go func() {
				defer close(out)
				finalRun, duplicateRun, duplicateStart, fireErr = fireScheduledTriggerOnce(memaxagent.WithEventObserver(ctx, observer), stack, store, now, trigger)
			}()
			return out, nil
		},
		Assertions: []agenteval.Assertion{
			toolConstructionSucceeded(configErr, stackErr),
			agenteval.FinalEquals("Morning briefing ready for 2026-04-19."),
			requestCountEquals(modelClient, 1),
			{
				Name: "scheduled run persists deterministic occurrence and deduplicates reruns",
				Check: func(result agenteval.Result) error {
					if fireErr != nil {
						return fireErr
					}
					if finalRun.ID != "daily-brief:2026-04-19T07:00:00Z" {
						return fmt.Errorf("final run id = %q, want deterministic occurrence id", finalRun.ID)
					}
					if finalRun.Status != personal.ScheduledRunSucceeded || finalRun.Result != "Morning briefing ready for 2026-04-19." {
						return fmt.Errorf("final run = %#v, want succeeded proactive briefing", finalRun)
					}
					if finalRun.SessionID == "" || finalRun.CompletedAt.IsZero() {
						return fmt.Errorf("final run = %#v, want session and completion time", finalRun)
					}
					if duplicateStart {
						return fmt.Errorf("duplicate start = true, want no-op for same occurrence")
					}
					if duplicateRun.ID != finalRun.ID {
						return fmt.Errorf("duplicate run = %#v, want existing occurrence record", duplicateRun)
					}
					return nil
				},
			},
		},
	}
}

// PersonalPresetAssistantScheduledTaskLedgerMaintenance returns a single-use
// scenario where a proactive scheduled trigger maintains durable task state by
// listing persisted pending work before applying deterministic updates.
func PersonalPresetAssistantScheduledTaskLedgerMaintenance() agenteval.Case {
	modelClient := &scheduledTaskLedgerMaintenanceModel{}
	taskDB, taskDBErr := sql.Open("sqlite", "file:scheduled-task-ledger-maintenance-tasks?mode=memory&cache=shared")
	var (
		taskStore    tasktools.Store
		taskStoreErr error
	)
	if taskDBErr == nil {
		taskStore, taskStoreErr = tasksqlitestore.New(context.Background(), taskDB)
	}
	if taskStoreErr == nil && taskStore != nil {
		_, taskStoreErr = taskStore.Upsert(context.Background(), tasktools.Task{
			ID:       "week-2026-04-20-acme-owner",
			Title:    "Confirm Acme mitigation owner",
			Status:   tasktools.StatusPending,
			Notes:    "Casey needs the mitigation owner before the Monday 14:00 UTC customer checkpoint.",
			Priority: 1,
			Evidence: []string{"thread-1", "event-1"},
		})
	}
	if taskStoreErr == nil && taskStore != nil {
		_, taskStoreErr = taskStore.Upsert(context.Background(), tasktools.Task{
			ID:       "week-2026-04-20-demo-slides",
			Title:    "Deliver partner council demo slides",
			Status:   tasktools.StatusPending,
			Notes:    "Priya needs final demo slides by Wednesday 17:00 UTC for Thursday partner council.",
			Priority: 2,
			Evidence: []string{"thread-2", "event-2"},
		})
	}

	runDB, runDBErr := sql.Open("sqlite", "file:scheduled-task-ledger-maintenance-runs?mode=memory&cache=shared")
	var (
		runStore    personal.ScheduledRunStore
		runStoreErr error
	)
	if runDBErr == nil {
		runStore, runStoreErr = personalsqlitestore.New(context.Background(), runDB)
	}

	config, configErr := personal.PresetPersonalAssistant.Config()
	if configErr == nil {
		config.Base.Model = modelClient
		config.Tasks = taskStore
	}
	stack, stackErr := personal.New(config)
	now := time.Date(2026, 4, 20, 8, 5, 0, 0, time.UTC)
	trigger := personal.PeriodicTrigger{
		Name:   "task-ledger-maintenance",
		Prompt: "Run scheduled task-ledger maintenance for 2026-04-20. List persisted pending tasks first; complete confirmed work, mark blocked work explicitly, and do not create duplicate task IDs.",
		Every:  24 * time.Hour,
		Anchor: time.Date(2026, 4, 20, 8, 0, 0, 0, time.UTC),
	}
	registry, registryErr := personal.NewMemoryScheduledWorkflowRegistry(personal.ScheduledWorkflow{
		Name:        "task-ledger-maintenance",
		Description: "Maintain the durable task ledger for the current planning window.",
		Tags:        []string{"tasks", "maintenance"},
		Trigger:     trigger,
	})

	var (
		finalRun       personal.ScheduledRunRecord
		duplicateRun   personal.ScheduledRunRecord
		duplicateStart bool
		duplicateErr   error
	)

	return agenteval.Case{
		Name:   "personal_preset_personal_assistant_scheduled_task_ledger_maintenance",
		Prompt: "Run the scheduled task-ledger maintenance trigger for the current occurrence.",
		Run: func(ctx context.Context) (<-chan memaxagent.Event, error) {
			if configErr != nil {
				return nil, configErr
			}
			if taskDBErr != nil {
				return nil, taskDBErr
			}
			if taskStoreErr != nil {
				return nil, taskStoreErr
			}
			if runDBErr != nil {
				return nil, runDBErr
			}
			if runStoreErr != nil {
				return nil, runStoreErr
			}
			if stackErr != nil {
				return nil, stackErr
			}
			if registryErr != nil {
				return nil, registryErr
			}
			out := make(chan memaxagent.Event, 32)
			observer := memaxagent.EventObserverFunc(func(_ context.Context, event memaxagent.Event) {
				select {
				case out <- event:
				case <-ctx.Done():
				}
			})
			go func() {
				defer close(out)
				finalRun, duplicateRun, duplicateStart, duplicateErr = fireScheduledWorkflowOnce(memaxagent.WithEventObserver(ctx, observer), stack, runStore, registry, now, "task-ledger-maintenance")
			}()
			return out, nil
		},
		Cleanup: func() {
			if taskDB != nil {
				_ = taskDB.Close()
			}
			if runDB != nil {
				_ = runDB.Close()
			}
		},
		Assertions: []agenteval.Assertion{
			toolConstructionSucceeded(configErr, taskDBErr, taskStoreErr, runDBErr, runStoreErr, stackErr, registryErr),
			agenteval.NoToolErrors(),
			agenteval.ToolUsed(tasktools.ListToolName),
			agenteval.ToolUsed(tasktools.UpsertToolName),
			agenteval.FinalEquals(scheduledTaskLedgerMaintenanceFinal),
			requestCountEquals(modelClient, 4),
			{
				Name: "scheduled maintenance lists pending tasks before updating",
				Check: func(result agenteval.Result) error {
					uses := result.ToolUses()
					want := []string{tasktools.ListToolName, tasktools.UpsertToolName, tasktools.UpsertToolName}
					if len(uses) != len(want) {
						return fmt.Errorf("tool uses = %#v, want %v", uses, want)
					}
					for i, name := range want {
						if uses[i].Name != name {
							return fmt.Errorf("tool use order = %#v, want %v", uses, want)
						}
					}
					results := result.ToolResults()
					if len(results) != 3 {
						return fmt.Errorf("tool results = %#v, want list and two upserts", results)
					}
					for _, want := range []string{
						"[pending] week-2026-04-20-acme-owner",
						"[pending] week-2026-04-20-demo-slides",
					} {
						if !strings.Contains(results[0].Content, want) {
							return fmt.Errorf("pending list = %q, missing %q", results[0].Content, want)
						}
					}
					if results[1].Metadata[model.MetadataTaskID] != "week-2026-04-20-acme-owner" || results[1].Metadata[model.MetadataTaskStatus] != string(tasktools.StatusCompleted) {
						return fmt.Errorf("first update metadata = %#v, want completed Acme task", results[1].Metadata)
					}
					if results[2].Metadata[model.MetadataTaskID] != "week-2026-04-20-demo-slides" || results[2].Metadata[model.MetadataTaskStatus] != string(tasktools.StatusBlocked) {
						return fmt.Errorf("second update metadata = %#v, want blocked demo task", results[2].Metadata)
					}
					return nil
				},
			},
			{
				Name: "task ledger context is loaded before the first model request",
				Check: func(result agenteval.Result) error {
					requests := modelClient.Requests()
					if len(requests) != 4 {
						return fmt.Errorf("requests = %d, want 4", len(requests))
					}
					firstPrompt := requests[0].SystemPrompt
					for _, want := range []string{
						"[pending] week-2026-04-20-acme-owner",
						"Confirm Acme mitigation owner",
						"[pending] week-2026-04-20-demo-slides",
						"Deliver partner council demo slides",
					} {
						if !strings.Contains(firstPrompt, want) {
							return fmt.Errorf("first model prompt missing %q:\n%s", want, firstPrompt)
						}
					}
					if !scheduledTaskRequestHasToolResult(requests[1], "list-1", "week-2026-04-20-acme-owner", "week-2026-04-20-demo-slides") {
						return fmt.Errorf("second model request did not include pending list result")
					}
					return nil
				},
			},
			{
				Name: "scheduled sqlite occurrence deduplicates reruns and persists task updates",
				Check: func(result agenteval.Result) error {
					if duplicateErr != nil {
						return duplicateErr
					}
					if finalRun.ID != "task-ledger-maintenance:2026-04-20T08:00:00Z" {
						return fmt.Errorf("final run id = %q, want deterministic maintenance occurrence", finalRun.ID)
					}
					if finalRun.Status != personal.ScheduledRunSucceeded || finalRun.Result != scheduledTaskLedgerMaintenanceFinal {
						return fmt.Errorf("final run = %#v, want succeeded task maintenance", finalRun)
					}
					if finalRun.SessionID == "" || finalRun.CompletedAt.IsZero() {
						return fmt.Errorf("final run = %#v, want session and completion time", finalRun)
					}
					if duplicateStart {
						return fmt.Errorf("duplicate start = true, want no-op for same occurrence")
					}
					if duplicateRun.ID != finalRun.ID {
						return fmt.Errorf("duplicate run = %#v, want existing occurrence record", duplicateRun)
					}
					tasks, err := taskStore.List(context.Background())
					if err != nil {
						return err
					}
					if len(tasks) != 2 {
						return fmt.Errorf("tasks = %#v, want two persisted maintenance tasks", tasks)
					}
					byID := make(map[string]tasktools.Task, len(tasks))
					for _, task := range tasks {
						if _, ok := byID[task.ID]; ok {
							return fmt.Errorf("duplicate task id %q in %#v", task.ID, tasks)
						}
						byID[task.ID] = task
					}
					if byID["week-2026-04-20-acme-owner"].Status != tasktools.StatusCompleted {
						return fmt.Errorf("Acme task = %#v, want completed", byID["week-2026-04-20-acme-owner"])
					}
					if byID["week-2026-04-20-demo-slides"].Status != tasktools.StatusBlocked {
						return fmt.Errorf("demo task = %#v, want blocked", byID["week-2026-04-20-demo-slides"])
					}
					if !strings.Contains(strings.Join(byID["week-2026-04-20-demo-slides"].Evidence, ","), "slides-missing") {
						return fmt.Errorf("demo task evidence = %#v, want slides-missing", byID["week-2026-04-20-demo-slides"].Evidence)
					}
					return nil
				},
			},
		},
	}
}

const scheduledTaskLedgerMaintenanceFinal = "Scheduled task-ledger maintenance complete: Acme mitigation owner is confirmed; partner council demo slides remain blocked."

type scheduledTaskLedgerMaintenanceModel struct {
	mu       sync.Mutex
	requests []model.Request
}

func (m *scheduledTaskLedgerMaintenanceModel) Stream(ctx context.Context, req model.Request) (model.Stream, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	request := req
	request.Messages = model.CloneMessages(req.Messages)
	request.Tools = append([]model.ToolSpec(nil), req.Tools...)
	m.requests = append(m.requests, request)
	switch len(m.requests) {
	case 1:
		return &scheduledTaskLedgerMaintenanceStream{events: []model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:    "list-1",
				Name:  tasktools.ListToolName,
				Input: json.RawMessage(`{"status":"pending"}`),
			},
		}}}, nil
	case 2:
		if !scheduledTaskRequestHasToolResult(req, "list-1", "week-2026-04-20-acme-owner", "week-2026-04-20-demo-slides") {
			return &scheduledTaskLedgerMaintenanceStream{events: []model.StreamEvent{{Kind: model.StreamText, Text: "No pending task ledger entries were available to maintain."}}}, nil
		}
		return &scheduledTaskLedgerMaintenanceStream{events: []model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:   "complete-1",
				Name: tasktools.UpsertToolName,
				Input: json.RawMessage(`{
					"id":"week-2026-04-20-acme-owner",
					"status":"completed",
					"notes":"Mitigation owner confirmed for the Monday 14:00 UTC customer checkpoint.",
					"evidence":["thread-1","event-1","owner-confirmed"]
				}`),
			},
		}}}, nil
	case 3:
		if !scheduledTaskRequestHasToolResult(req, "complete-1", "upserted week-2026-04-20-acme-owner") {
			return &scheduledTaskLedgerMaintenanceStream{events: []model.StreamEvent{{Kind: model.StreamText, Text: "Could not verify the Acme task update before continuing maintenance."}}}, nil
		}
		return &scheduledTaskLedgerMaintenanceStream{events: []model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:   "block-1",
				Name: tasktools.UpsertToolName,
				Input: json.RawMessage(`{
					"id":"week-2026-04-20-demo-slides",
					"status":"blocked",
					"notes":"Still waiting on final demo slides needed by Wednesday 17:00 UTC.",
					"evidence":["thread-2","event-2","slides-missing"]
				}`),
			},
		}}}, nil
	case 4:
		if !scheduledTaskRequestHasToolResult(req, "block-1", "upserted week-2026-04-20-demo-slides") {
			return &scheduledTaskLedgerMaintenanceStream{events: []model.StreamEvent{{Kind: model.StreamText, Text: "Could not verify the demo-slide blocker before closing maintenance."}}}, nil
		}
		return &scheduledTaskLedgerMaintenanceStream{events: []model.StreamEvent{{Kind: model.StreamText, Text: scheduledTaskLedgerMaintenanceFinal}}}, nil
	default:
		return &scheduledTaskLedgerMaintenanceStream{}, nil
	}
}

func (m *scheduledTaskLedgerMaintenanceModel) RequestCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.requests)
}

func (m *scheduledTaskLedgerMaintenanceModel) Requests() []model.Request {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]model.Request, len(m.requests))
	for i, req := range m.requests {
		out[i] = req
		out[i].Messages = model.CloneMessages(req.Messages)
		out[i].Tools = append([]model.ToolSpec(nil), req.Tools...)
	}
	return out
}

type scheduledTaskLedgerMaintenanceStream struct {
	events []model.StreamEvent
	index  int
}

func (s *scheduledTaskLedgerMaintenanceStream) Recv() (model.StreamEvent, error) {
	if s.index >= len(s.events) {
		return model.StreamEvent{}, model.ErrEndOfStream
	}
	event := s.events[s.index]
	s.index++
	return event, nil
}

func (s *scheduledTaskLedgerMaintenanceStream) Close() error {
	return nil
}

func scheduledTaskRequestHasToolResult(req model.Request, toolUseID string, substrings ...string) bool {
	for _, msg := range req.Messages {
		if msg.ToolResult == nil || msg.ToolResult.ToolUseID != toolUseID {
			continue
		}
		foundAll := true
		for _, substring := range substrings {
			if !strings.Contains(msg.ToolResult.Content, substring) {
				foundAll = false
				break
			}
		}
		if foundAll {
			return true
		}
	}
	return false
}

// PersonalPresetAssistantScheduledInboxTriage returns a single-use scenario
// where the personal_assistant preset runs one hourly unread-inbox triage
// trigger, classifies the urgent thread from metadata first, reads the full
// thread only before drafting, sends the approved reply, and treats a second
// fire for the same occurrence as a no-op backed by durable SQLite state.
func PersonalPresetAssistantScheduledInboxTriage() agenteval.Case {
	messageStore := messaging.NewThreadStore([]messaging.Thread{{
		ID:      "thread-1",
		Subject: "Urgent: Acme renewal blocker",
		Summary: "Casey says checkout is blocked before Monday's renewal deadline and needs a same-day update.",
		Participants: []messaging.Participant{
			{Name: "Casey", Address: "casey@acme.example", Role: "from"},
		},
		Tags:          []string{"INBOX", "urgent", "customer"},
		LastMessageAt: time.Date(2026, 4, 19, 9, 0, 0, 0, time.UTC),
		Metadata:      map[string]any{"unread": true},
		Messages: []messaging.Message{{
			ID:        "thread-1-msg-1",
			ThreadID:  "thread-1",
			Subject:   "Urgent: Acme renewal blocker",
			Summary:   "Checkout blocked before the renewal deadline.",
			Body:      "Checkout is blocked for Acme before Monday's renewal deadline. Please send me a same-day update and tell me when I should expect the next checkpoint.",
			Direction: messaging.DirectionInbound,
			Sender:    messaging.Participant{Name: "Casey", Address: "casey@acme.example", Role: "from"},
			SentAt:    time.Date(2026, 4, 19, 9, 0, 0, 0, time.UTC),
		}},
	}})
	taskStore := tasktools.NewMemoryStore([]tasktools.Task{{
		ID:     "task-1",
		Title:  "triage unread inbox threads",
		Status: tasktools.StatusInProgress,
		Notes:  "run the hourly unread inbox triage proactively, classify from metadata first, then read the selected thread before drafting the approved reply",
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
				ID:    "send-1",
				Name:  messagetools.SendToolName,
				Input: json.RawMessage(sendInput),
			},
		}},
		[]model.StreamEvent{{Kind: model.StreamText, Text: "Scheduled inbox triage sent the urgent Acme reply and recorded the occurrence so the same hourly trigger does not run twice."}},
	)

	config, configErr := personal.PresetPersonalAssistant.Config()
	if configErr == nil {
		config.Base.Model = modelClient
	}
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
			Reason:   "approved scheduled urgent triage reply",
		},
	}
	stack, stackErr := personal.New(config)

	db, dbErr := sql.Open("sqlite", "file:scheduled-inbox-triage?mode=memory&cache=shared")
	var (
		store    personal.ScheduledRunStore
		storeErr error
	)
	if dbErr == nil {
		store, storeErr = personalsqlitestore.New(context.Background(), db)
	}
	now := time.Date(2026, 4, 19, 9, 5, 0, 0, time.UTC)
	trigger := personal.PeriodicTrigger{
		Name:   "inbox-triage",
		Prompt: "Run the hourly unread inbox triage. Search unread inbox metadata first, read only the selected thread, then send the approved reply.",
		Every:  time.Hour,
		Anchor: time.Date(2026, 4, 19, 9, 0, 0, 0, time.UTC),
	}

	var (
		finalRun       personal.ScheduledRunRecord
		duplicateRun   personal.ScheduledRunRecord
		duplicateStart bool
		fireErr        error
	)

	return agenteval.Case{
		Name:   "personal_preset_personal_assistant_scheduled_inbox_triage",
		Prompt: "Run the hourly unread inbox triage proactively for the current occurrence.",
		Run: func(ctx context.Context) (<-chan memaxagent.Event, error) {
			if stackErr != nil {
				return nil, stackErr
			}
			if dbErr != nil {
				return nil, dbErr
			}
			if storeErr != nil {
				return nil, storeErr
			}
			out := make(chan memaxagent.Event, 32)
			observer := memaxagent.EventObserverFunc(func(_ context.Context, event memaxagent.Event) {
				select {
				case out <- event:
				case <-ctx.Done():
				}
			})
			go func() {
				defer close(out)
				finalRun, duplicateRun, duplicateStart, fireErr = fireScheduledTriggerOnce(memaxagent.WithEventObserver(ctx, observer), stack, store, now, trigger)
			}()
			return out, nil
		},
		Cleanup: func() {
			if db != nil {
				_ = db.Close()
			}
		},
		Assertions: []agenteval.Assertion{
			toolConstructionSucceeded(configErr, stackErr, dbErr, storeErr),
			agenteval.ToolUsed(messagetools.SearchToolName),
			agenteval.ToolUsed(messagetools.ReadToolName),
			agenteval.ToolUsed(approvaltools.ToolName),
			agenteval.ToolUsed(messagetools.SendToolName),
			agenteval.EventKindEmitted(memaxagent.EventApprovalRequested),
			agenteval.EventKindEmitted(memaxagent.EventApprovalGranted),
			agenteval.EventKindEmitted(memaxagent.EventApprovalConsumed),
			agenteval.FinalEquals("Scheduled inbox triage sent the urgent Acme reply and recorded the occurrence so the same hourly trigger does not run twice."),
			requestCountEquals(modelClient, 5),
			{
				Name: "scheduled triage stays metadata-first and uses unread inbox filters",
				Check: func(result agenteval.Result) error {
					uses := result.ToolUses()
					want := []string{
						messagetools.SearchToolName,
						messagetools.ReadToolName,
						approvaltools.ToolName,
						messagetools.SendToolName,
					}
					if len(uses) != len(want) {
						return fmt.Errorf("tool uses = %#v, want %v", uses, want)
					}
					for i, name := range want {
						if uses[i].Name != name {
							return fmt.Errorf("tool use order = %#v, want %v", uses, want)
						}
					}
					if !strings.Contains(string(uses[0].Input), `"mailboxes":["INBOX"]`) || !strings.Contains(string(uses[0].Input), `"unread":true`) {
						return fmt.Errorf("search input = %s, want unread inbox filters", uses[0].Input)
					}
					toolResults := result.ToolResults()
					if len(toolResults) != 4 {
						return fmt.Errorf("tool results = %#v, want search read approval send", toolResults)
					}
					if !strings.Contains(toolResults[0].Content, "Urgent: Acme renewal blocker") || !strings.Contains(toolResults[0].Content, "same-day update") {
						return fmt.Errorf("search result = %#v, want urgent metadata", toolResults[0])
					}
					if strings.Contains(toolResults[0].Content, "Please send me a same-day update and tell me when I should expect the next checkpoint.") {
						return fmt.Errorf("search result leaked full thread body: %#v", toolResults[0])
					}
					if toolResults[0].Metadata["matches"] != 1 {
						return fmt.Errorf("search metadata = %#v, want one unread inbox match", toolResults[0].Metadata)
					}
					if toolResults[1].IsError || !strings.Contains(toolResults[1].Content, "Please send me a same-day update") {
						return fmt.Errorf("read result = %#v, want full thread before draft", toolResults[1])
					}
					return nil
				},
			},
			{
				Name: "scheduled sqlite occurrence deduplicates reruns and persists output",
				Check: func(result agenteval.Result) error {
					if fireErr != nil {
						return fireErr
					}
					toolResults := result.ToolResults()
					if toolResults[2].IsError || toolResults[2].Metadata[approvaltools.MetadataApprovalApproved] != true {
						return fmt.Errorf("approval result = %#v, want granted approval", toolResults[2])
					}
					if toolResults[3].IsError || !strings.Contains(toolResults[3].Content, "sent message Urgent: Acme renewal blocker") {
						return fmt.Errorf("send result = %#v, want send success", toolResults[3])
					}
					if finalRun.ID != "inbox-triage:2026-04-19T09:00:00Z" {
						return fmt.Errorf("final run id = %q, want deterministic occurrence id", finalRun.ID)
					}
					if finalRun.Status != personal.ScheduledRunSucceeded || !strings.Contains(finalRun.Result, "recorded the occurrence") {
						return fmt.Errorf("final run = %#v, want succeeded scheduled triage", finalRun)
					}
					if finalRun.SessionID == "" || finalRun.CompletedAt.IsZero() {
						return fmt.Errorf("final run = %#v, want session and completion time", finalRun)
					}
					if duplicateStart {
						return fmt.Errorf("duplicate start = true, want no-op for same occurrence")
					}
					if duplicateRun.ID != finalRun.ID {
						return fmt.Errorf("duplicate run = %#v, want existing occurrence record", duplicateRun)
					}
					thread, err := messageStore.ReadThread(context.Background(), messaging.ReadRequest{ThreadID: "thread-1"})
					if err != nil {
						return err
					}
					if len(thread.Messages) != 2 {
						return fmt.Errorf("thread messages = %d, want 2", len(thread.Messages))
					}
					if !strings.Contains(thread.Messages[len(thread.Messages)-1].Body, "14:00 UTC today") {
						return fmt.Errorf("outbound reply body = %q, want promised checkpoint time", thread.Messages[len(thread.Messages)-1].Body)
					}
					return nil
				},
			},
		},
	}
}

// PersonalPresetAssistantScheduledInboxTriageJMAP returns a single-use
// scenario where the same scheduled unread-inbox triage workflow runs over the
// real JMAP adapter seam rather than the in-memory messaging store.
func PersonalPresetAssistantScheduledInboxTriageJMAP() agenteval.Case {
	var (
		serverMu      sync.Mutex
		methods       []string
		submittedMail bool
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var envelope struct {
			MethodCalls [][]json.RawMessage `json:"methodCalls"`
		}
		if err := json.NewDecoder(r.Body).Decode(&envelope); err != nil {
			panic(fmt.Sprintf("decode request: %v", err))
		}
		if len(envelope.MethodCalls) != 1 {
			panic(fmt.Sprintf("method calls = %d, want 1", len(envelope.MethodCalls)))
		}
		var method string
		if err := json.Unmarshal(envelope.MethodCalls[0][0], &method); err != nil {
			panic(fmt.Sprintf("decode method: %v", err))
		}
		serverMu.Lock()
		methods = append(methods, method)
		serverMu.Unlock()

		switch method {
		case "Email/query":
			_, _ = w.Write([]byte(`{"methodResponses":[["Email/query",{"accountId":"acc","ids":["email-1"],"total":1},"0"]]}`))
		case "Email/get":
			var args map[string]any
			if err := json.Unmarshal(envelope.MethodCalls[0][1], &args); err != nil {
				panic(fmt.Sprintf("decode Email/get args: %v", err))
			}
			ids, _ := args["ids"].([]any)
			if len(ids) == 1 && ids[0] == "email-1" {
				if fetch, _ := args["fetchTextBodyValues"].(bool); fetch {
					_, _ = w.Write([]byte(`{"methodResponses":[["Email/get",{"list":[{"id":"email-1","threadId":"thread-1","subject":"Urgent: Acme renewal blocker","preview":"Please send me a same-day update and tell me when I should expect the next checkpoint.","receivedAt":"2026-04-19T09:00:00Z","mailboxIds":{"INBOX":true},"keywords":{"$seen":false},"from":[{"name":"Casey","email":"casey@acme.example"}],"to":[{"name":"Me","email":"me@example.com"}],"textBody":[{"partId":"1"}],"bodyValues":{"1":{"value":"Checkout is blocked for Acme before Monday's renewal deadline. Please send me a same-day update and tell me when I should expect the next checkpoint.","isTruncated":false}}}]},"0"]]}`))
					return
				}
				_, _ = w.Write([]byte(`{"methodResponses":[["Email/get",{"list":[{"id":"email-1","threadId":"thread-1","subject":"Urgent: Acme renewal blocker","preview":"same-day update requested","receivedAt":"2026-04-19T09:00:00Z","mailboxIds":{"INBOX":true},"keywords":{"$seen":false},"from":[{"name":"Casey","email":"casey@acme.example"}],"to":[{"name":"Me","email":"me@example.com"}]}]},"0"]]}`))
				return
			}
			if len(ids) == 1 && ids[0] == "email-2" {
				_, _ = w.Write([]byte(`{"methodResponses":[["Email/get",{"list":[{"id":"email-2","threadId":"thread-1","subject":"Urgent: Acme renewal blocker","preview":"Thanks, Casey. We are treating this as urgent and I will send you the next update by 14:00 UTC today.","receivedAt":"2026-04-19T09:05:00Z","mailboxIds":{"sent":true},"keywords":{"$sent":true},"from":[{"name":"Memax","email":"me@example.com"}],"to":[{"name":"Casey","email":"casey@acme.example"}],"textBody":[{"partId":"1"}],"bodyValues":{"1":{"value":"Thanks, Casey. We are treating this as urgent and I will send you the next update by 14:00 UTC today.","isTruncated":false}}}]},"0"]]}`))
				return
			}
			if len(ids) == 2 {
				_, _ = w.Write([]byte(`{"methodResponses":[["Email/get",{"list":[{"id":"email-1","threadId":"thread-1","subject":"Urgent: Acme renewal blocker","preview":"Please send me a same-day update and tell me when I should expect the next checkpoint.","receivedAt":"2026-04-19T09:00:00Z","mailboxIds":{"INBOX":true},"keywords":{"$seen":false},"from":[{"name":"Casey","email":"casey@acme.example"}],"to":[{"name":"Me","email":"me@example.com"}],"textBody":[{"partId":"1"}],"bodyValues":{"1":{"value":"Checkout is blocked for Acme before Monday's renewal deadline. Please send me a same-day update and tell me when I should expect the next checkpoint.","isTruncated":false}}},{"id":"email-2","threadId":"thread-1","subject":"Urgent: Acme renewal blocker","preview":"Thanks, Casey. We are treating this as urgent and I will send you the next update by 14:00 UTC today.","receivedAt":"2026-04-19T09:05:00Z","mailboxIds":{"sent":true},"keywords":{"$sent":true},"from":[{"name":"Memax","email":"me@example.com"}],"to":[{"name":"Casey","email":"casey@acme.example"}],"textBody":[{"partId":"1"}],"bodyValues":{"1":{"value":"Thanks, Casey. We are treating this as urgent and I will send you the next update by 14:00 UTC today.","isTruncated":false}}}]},"0"]]}`))
				return
			}
			panic(fmt.Sprintf("unexpected Email/get ids %#v", ids))
		case "Thread/get":
			serverMu.Lock()
			created := submittedMail
			serverMu.Unlock()
			if created {
				_, _ = w.Write([]byte(`{"methodResponses":[["Thread/get",{"list":[{"id":"thread-1","emailIds":["email-1","email-2"]}]},"0"]]}`))
				return
			}
			_, _ = w.Write([]byte(`{"methodResponses":[["Thread/get",{"list":[{"id":"thread-1","emailIds":["email-1"]}]},"0"]]}`))
		case "Email/set":
			_, _ = w.Write([]byte(`{"methodResponses":[["Email/set",{"created":{"email":{"id":"email-2"}}},"0"]]}`))
		case "EmailSubmission/set":
			serverMu.Lock()
			submittedMail = true
			serverMu.Unlock()
			_, _ = w.Write([]byte(`{"methodResponses":[["EmailSubmission/set",{"created":{"submission":{"id":"submission-1","emailId":"email-2"}}},"0"]]}`))
		default:
			panic(fmt.Sprintf("unexpected method %q", method))
		}
	}))

	client, clientErr := jmapclient.New(server.URL, "acc")
	messageStore, storeErr := jmapstore.New(client,
		jmapstore.WithDefaultIdentity("identity-1"),
		jmapstore.WithDefaultSender("Memax", "me@example.com"),
		jmapstore.WithDraftMailbox("drafts"),
	)
	taskStore := tasktools.NewMemoryStore([]tasktools.Task{{
		ID:     "task-1",
		Title:  "triage unread inbox threads",
		Status: tasktools.StatusInProgress,
		Notes:  "run the hourly unread inbox triage proactively, classify from metadata first, then read the selected thread before drafting the approved reply over the attached JMAP inbox backend",
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
				ID:    "send-1",
				Name:  messagetools.SendToolName,
				Input: json.RawMessage(sendInput),
			},
		}},
		[]model.StreamEvent{{Kind: model.StreamText, Text: "Scheduled inbox triage sent the urgent Acme reply through the attached JMAP inbox backend and recorded the occurrence so the same hourly trigger does not run twice."}},
	)

	config, configErr := personal.PresetPersonalAssistant.Config()
	if configErr == nil {
		config.Base.Model = modelClient
	}
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
			Reason:   "approved scheduled urgent triage reply",
		},
	}
	stack, stackErr := personal.New(config)

	db, dbErr := sql.Open("sqlite", "file:scheduled-inbox-triage-jmap?mode=memory&cache=shared")
	var (
		store  personal.ScheduledRunStore
		sqlErr error
	)
	if dbErr == nil {
		store, sqlErr = personalsqlitestore.New(context.Background(), db)
	}
	now := time.Date(2026, 4, 19, 9, 5, 0, 0, time.UTC)
	trigger := personal.PeriodicTrigger{
		Name:   "inbox-triage",
		Prompt: "Run the hourly unread inbox triage. Search unread inbox metadata first, read only the selected thread, then send the approved reply.",
		Every:  time.Hour,
		Anchor: time.Date(2026, 4, 19, 9, 0, 0, 0, time.UTC),
	}

	var (
		finalRun       personal.ScheduledRunRecord
		duplicateRun   personal.ScheduledRunRecord
		duplicateStart bool
		fireErr        error
	)

	return agenteval.Case{
		Name:   "personal_preset_personal_assistant_scheduled_inbox_triage_jmap",
		Prompt: "Run the hourly unread inbox triage proactively for the current occurrence through the attached JMAP inbox backend.",
		Run: func(ctx context.Context) (<-chan memaxagent.Event, error) {
			if stackErr != nil {
				return nil, stackErr
			}
			if clientErr != nil {
				return nil, clientErr
			}
			if storeErr != nil {
				return nil, storeErr
			}
			if dbErr != nil {
				return nil, dbErr
			}
			if sqlErr != nil {
				return nil, sqlErr
			}
			out := make(chan memaxagent.Event, 32)
			observer := memaxagent.EventObserverFunc(func(_ context.Context, event memaxagent.Event) {
				select {
				case out <- event:
				case <-ctx.Done():
				}
			})
			go func() {
				defer close(out)
				finalRun, duplicateRun, duplicateStart, fireErr = fireScheduledTriggerOnce(memaxagent.WithEventObserver(ctx, observer), stack, store, now, trigger)
			}()
			return out, nil
		},
		Cleanup: func() {
			server.Close()
			if db != nil {
				_ = db.Close()
			}
		},
		Assertions: []agenteval.Assertion{
			toolConstructionSucceeded(clientErr, storeErr, configErr, stackErr, dbErr, sqlErr),
			agenteval.ToolUsed(messagetools.SearchToolName),
			agenteval.ToolUsed(messagetools.ReadToolName),
			agenteval.ToolUsed(approvaltools.ToolName),
			agenteval.ToolUsed(messagetools.SendToolName),
			agenteval.EventKindEmitted(memaxagent.EventApprovalRequested),
			agenteval.EventKindEmitted(memaxagent.EventApprovalGranted),
			agenteval.EventKindEmitted(memaxagent.EventApprovalConsumed),
			agenteval.FinalEquals("Scheduled inbox triage sent the urgent Acme reply through the attached JMAP inbox backend and recorded the occurrence so the same hourly trigger does not run twice."),
			requestCountEquals(modelClient, 5),
			{
				Name: "scheduled JMAP triage keeps the same portable unread inbox contract",
				Check: func(result agenteval.Result) error {
					uses := result.ToolUses()
					want := []string{
						messagetools.SearchToolName,
						messagetools.ReadToolName,
						approvaltools.ToolName,
						messagetools.SendToolName,
					}
					if len(uses) != len(want) {
						return fmt.Errorf("tool uses = %#v, want %v", uses, want)
					}
					for i, name := range want {
						if uses[i].Name != name {
							return fmt.Errorf("tool use order = %#v, want %v", uses, want)
						}
					}
					if !strings.Contains(string(uses[0].Input), `"mailboxes":["INBOX"]`) || !strings.Contains(string(uses[0].Input), `"unread":true`) {
						return fmt.Errorf("search input = %s, want unread inbox filters", uses[0].Input)
					}
					toolResults := result.ToolResults()
					if len(toolResults) != 4 {
						return fmt.Errorf("tool results = %#v, want search read approval send", toolResults)
					}
					if toolResults[0].IsError || !strings.Contains(toolResults[0].Content, "Urgent: Acme renewal blocker") {
						return fmt.Errorf("search result = %#v, want urgent inbox metadata", toolResults[0])
					}
					if strings.Contains(toolResults[0].Content, "Please send me a same-day update and tell me when I should expect the next checkpoint.") {
						return fmt.Errorf("search result leaked full body: %#v", toolResults[0])
					}
					if toolResults[0].Metadata["matches"] != 1 {
						return fmt.Errorf("search metadata = %#v, want one unread inbox match", toolResults[0].Metadata)
					}
					if toolResults[1].IsError || !strings.Contains(toolResults[1].Content, "Please send me a same-day update") {
						return fmt.Errorf("read result = %#v, want full thread body", toolResults[1])
					}
					return nil
				},
			},
			{
				Name: "scheduled JMAP triage preserves durable idempotency and adapter sequence",
				Check: func(result agenteval.Result) error {
					if fireErr != nil {
						return fireErr
					}
					toolResults := result.ToolResults()
					if toolResults[2].IsError || toolResults[2].Metadata[approvaltools.MetadataApprovalApproved] != true {
						return fmt.Errorf("approval result = %#v, want granted approval", toolResults[2])
					}
					if toolResults[3].IsError || !strings.Contains(toolResults[3].Content, "sent message Urgent: Acme renewal blocker") {
						return fmt.Errorf("send result = %#v, want persisted send success", toolResults[3])
					}
					if finalRun.ID != "inbox-triage:2026-04-19T09:00:00Z" {
						return fmt.Errorf("final run id = %q, want deterministic occurrence id", finalRun.ID)
					}
					if finalRun.Status != personal.ScheduledRunSucceeded || !strings.Contains(finalRun.Result, "recorded the occurrence") {
						return fmt.Errorf("final run = %#v, want succeeded scheduled JMAP triage", finalRun)
					}
					if finalRun.SessionID == "" || finalRun.CompletedAt.IsZero() {
						return fmt.Errorf("final run = %#v, want session and completion time", finalRun)
					}
					if duplicateStart {
						return fmt.Errorf("duplicate start = true, want no-op for same occurrence")
					}
					if duplicateRun.ID != finalRun.ID {
						return fmt.Errorf("duplicate run = %#v, want existing occurrence record", duplicateRun)
					}
					serverMu.Lock()
					defer serverMu.Unlock()
					want := []string{
						"Email/query",
						"Email/get",
						"Thread/get",
						"Email/get",
						"Thread/get",
						"Email/get",
						"Email/set",
						"EmailSubmission/set",
						"Email/get",
						"Thread/get",
						"Email/get",
					}
					if len(methods) != len(want) {
						return fmt.Errorf("jmap methods = %#v, want %v", methods, want)
					}
					for i, method := range want {
						if methods[i] != method {
							return fmt.Errorf("jmap methods = %#v, want %v", methods, want)
						}
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

// PersonalPresetAssistantInboxSendBackendFailure returns a single-use scenario
// where the personal_assistant preset triages an urgent inbox thread, gets
// approval for the outbound reply, then surfaces a backend send failure to the
// model without auto-retrying or hiding the transport error.
func PersonalPresetAssistantInboxSendBackendFailure() agenteval.Case {
	messageStore := messaging.NewThreadStore([]messaging.Thread{{
		ID:      "thread-1",
		Subject: "Urgent: Acme renewal blocker",
		Summary: "Customer requests a same-day renewal update and explicit checkpoint time.",
		Snippet: "Please send me a same-day update and tell me when I should expect the next checkpoint.",
		Participants: []messaging.Participant{
			{Name: "Jordan", Address: "jordan@acme.example", Role: "from"},
			{Name: "Me", Address: "me@example.com", Role: "to"},
		},
		Tags:          []string{"INBOX", "urgent", "unread"},
		LastMessageAt: time.Date(2026, 4, 19, 11, 0, 0, 0, time.UTC),
		Metadata: map[string]any{
			"mailboxes": []string{"INBOX"},
			"unread":    true,
		},
		Messages: []messaging.Message{{
			ID:        "message-1",
			ThreadID:  "thread-1",
			Subject:   "Urgent: Acme renewal blocker",
			Summary:   "Customer requests a same-day renewal update and explicit checkpoint time.",
			Body:      "Please send me a same-day update and tell me when I should expect the next checkpoint.",
			Direction: messaging.DirectionInbound,
			Sender:    messaging.Participant{Name: "Jordan", Address: "jordan@acme.example", Role: "from"},
			Recipients: []messaging.Participant{
				{Name: "Me", Address: "me@example.com", Role: "to"},
			},
			SentAt: time.Date(2026, 4, 19, 11, 0, 0, 0, time.UTC),
		}},
	}})
	taskStore := tasktools.NewMemoryStore([]tasktools.Task{{
		ID:     "task-1",
		Title:  "triage the urgent renewal blocker thread",
		Status: tasktools.StatusInProgress,
		Notes:  "search inbox metadata first, read the thread before drafting, and surface backend delivery failures explicitly if send fails",
	}})

	var (
		sendMu  sync.Mutex
		sendReq messaging.SendRequest
	)
	failingSender := messaging.SenderFunc(func(_ context.Context, req messaging.SendRequest) (messaging.SendResult, error) {
		sendMu.Lock()
		sendReq = req
		sendMu.Unlock()
		return messaging.SendResult{}, fmt.Errorf("jmap submission temporarily unavailable")
	})

	modelClient := agenteval.NewScriptedModel(
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:    "search-1",
				Name:  messagetools.SearchToolName,
				Input: json.RawMessage(`{"query":"urgent renewal same-day checkpoint","mailboxes":["INBOX"],"unread":true,"limit":3}`),
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
					"reason":"sending an external customer reply requires approval",
					"tool_input":{
						"thread_id":"thread-1",
						"subject":"Urgent: Acme renewal blocker",
						"body":"I can send the status update by 14:00 UTC today.",
						"recipients":[{"name":"Jordan","address":"jordan@acme.example","role":"to"}]
					}
				}`),
			},
		}},
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:   "send-1",
				Name: messagetools.SendToolName,
				Input: json.RawMessage(`{
					"thread_id":"thread-1",
					"subject":"Urgent: Acme renewal blocker",
					"body":"I can send the status update by 14:00 UTC today.",
					"recipients":[{"name":"Jordan","address":"jordan@acme.example","role":"to"}]
				}`),
			},
		}},
		[]model.StreamEvent{{Kind: model.StreamText, Text: "Personal assistant surfaced the approved reply failure and asked the user to retry once the mail backend recovers."}},
	)

	config, configErr := personal.PresetPersonalAssistant.Config()
	config.Messages = messagetools.Config{
		Searcher:     messageStore,
		Reader:       messageStore,
		Sender:       failingSender,
		DefaultLimit: 3,
	}
	config.Tasks = taskStore
	config.Approval.Approver = approvaltools.StaticApprover{
		Decision: approvaltools.Decision{
			Approved: true,
			Reason:   "approved urgent customer reply",
		},
	}
	stack, stackErr := personal.New(config)

	return agenteval.Case{
		Name:    "personal_preset_personal_assistant_inbox_send_backend_failure",
		Prompt:  "Triage urgent unread inbox threads carefully, read before drafting, and surface transport failures clearly if an approved outbound reply still fails.",
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
			agenteval.FinalEquals("Personal assistant surfaced the approved reply failure and asked the user to retry once the mail backend recovers."),
			requestCountEquals(modelClient, 5),
			{
				Name: "backend send failure stays model-visible after approval",
				Check: func(result agenteval.Result) error {
					uses := result.ToolUses()
					want := []string{
						messagetools.SearchToolName,
						messagetools.ReadToolName,
						approvaltools.ToolName,
						messagetools.SendToolName,
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
					if len(toolResults) != 4 {
						return fmt.Errorf("tool results = %#v, want search read approval send", toolResults)
					}
					if toolResults[0].IsError || !strings.Contains(toolResults[0].Content, "Urgent: Acme renewal blocker") {
						return fmt.Errorf("search result = %#v, want urgent inbox metadata", toolResults[0])
					}
					if strings.Contains(toolResults[0].Content, "Please send me a same-day update and tell me when I should expect the next checkpoint.") {
						return fmt.Errorf("search result leaked full thread body: %#v", toolResults[0])
					}
					if toolResults[1].IsError || !strings.Contains(toolResults[1].Content, "Please send me a same-day update") {
						return fmt.Errorf("read result = %#v, want full thread before draft", toolResults[1])
					}
					if toolResults[2].IsError || toolResults[2].Metadata[approvaltools.MetadataApprovalApproved] != true {
						return fmt.Errorf("approval result = %#v, want granted approval", toolResults[2])
					}
					if !toolResults[3].IsError || !strings.Contains(toolResults[3].Content, "jmap submission temporarily unavailable") {
						return fmt.Errorf("send result = %#v, want backend transport error", toolResults[3])
					}
					sendMu.Lock()
					defer sendMu.Unlock()
					if got := strings.TrimSpace(sendReq.Body); got != "I can send the status update by 14:00 UTC today." {
						return fmt.Errorf("send request body = %q, want approved reply content", got)
					}
					thread, err := messageStore.ReadThread(context.Background(), messaging.ReadRequest{ThreadID: "thread-1"})
					if err != nil {
						return err
					}
					if len(thread.Messages) != 1 {
						return fmt.Errorf("thread messages = %d, want 1 after failed send", len(thread.Messages))
					}
					return nil
				},
			},
		},
	}
}

// PersonalPresetAssistantJMAPInboxReply returns a single-use scenario where
// the personal_assistant preset runs the inbox workflow over the real JMAP
// adapter seam rather than the in-memory messaging store.
func PersonalPresetAssistantJMAPInboxReply() agenteval.Case {
	var (
		serverMu      sync.Mutex
		methods       []string
		submittedMail bool
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var envelope struct {
			MethodCalls [][]json.RawMessage `json:"methodCalls"`
		}
		if err := json.NewDecoder(r.Body).Decode(&envelope); err != nil {
			panic(fmt.Sprintf("decode request: %v", err))
		}
		if len(envelope.MethodCalls) != 1 {
			panic(fmt.Sprintf("method calls = %d, want 1", len(envelope.MethodCalls)))
		}
		var method string
		if err := json.Unmarshal(envelope.MethodCalls[0][0], &method); err != nil {
			panic(fmt.Sprintf("decode method: %v", err))
		}
		serverMu.Lock()
		methods = append(methods, method)
		serverMu.Unlock()

		switch method {
		case "Email/query":
			_, _ = w.Write([]byte(`{"methodResponses":[["Email/query",{"accountId":"acc","ids":["email-1"],"total":1},"0"]]}`))
		case "Email/get":
			var args map[string]any
			if err := json.Unmarshal(envelope.MethodCalls[0][1], &args); err != nil {
				panic(fmt.Sprintf("decode Email/get args: %v", err))
			}
			ids, _ := args["ids"].([]any)
			if len(ids) == 1 && ids[0] == "email-1" {
				if fetch, _ := args["fetchTextBodyValues"].(bool); fetch {
					_, _ = w.Write([]byte(`{"methodResponses":[["Email/get",{"list":[{"id":"email-1","threadId":"thread-1","subject":"Urgent: Acme renewal blocker","preview":"Please send me a same-day update and tell me when I should expect the next checkpoint.","receivedAt":"2026-04-19T11:00:00Z","mailboxIds":{"INBOX":true},"keywords":{"$seen":false},"from":[{"name":"Jordan","email":"jordan@acme.example"}],"to":[{"name":"Me","email":"me@example.com"}],"textBody":[{"partId":"1"}],"bodyValues":{"1":{"value":"Please send me a same-day update and tell me when I should expect the next checkpoint.","isTruncated":false}}}]},"0"]]}`))
					return
				}
				_, _ = w.Write([]byte(`{"methodResponses":[["Email/get",{"list":[{"id":"email-1","threadId":"thread-1","subject":"Urgent: Acme renewal blocker","preview":"same-day update requested","receivedAt":"2026-04-19T11:00:00Z","mailboxIds":{"INBOX":true},"keywords":{"$seen":false},"from":[{"name":"Jordan","email":"jordan@acme.example"}],"to":[{"name":"Me","email":"me@example.com"}]}]},"0"]]}`))
				return
			}
			if len(ids) == 1 && ids[0] == "email-2" {
				_, _ = w.Write([]byte(`{"methodResponses":[["Email/get",{"list":[{"id":"email-2","threadId":"thread-1","subject":"Urgent: Acme renewal blocker","preview":"I can send the status update by 14:00 UTC today.","receivedAt":"2026-04-19T12:00:00Z","mailboxIds":{"sent":true},"keywords":{"$sent":true},"from":[{"name":"Memax","email":"me@example.com"}],"to":[{"name":"Jordan","email":"jordan@acme.example"}],"textBody":[{"partId":"1"}],"bodyValues":{"1":{"value":"I can send the status update by 14:00 UTC today.","isTruncated":false}}}]},"0"]]}`))
				return
			}
			if len(ids) == 2 {
				_, _ = w.Write([]byte(`{"methodResponses":[["Email/get",{"list":[{"id":"email-1","threadId":"thread-1","subject":"Urgent: Acme renewal blocker","preview":"Please send me a same-day update and tell me when I should expect the next checkpoint.","receivedAt":"2026-04-19T11:00:00Z","mailboxIds":{"INBOX":true},"keywords":{"$seen":false},"from":[{"name":"Jordan","email":"jordan@acme.example"}],"to":[{"name":"Me","email":"me@example.com"}],"textBody":[{"partId":"1"}],"bodyValues":{"1":{"value":"Please send me a same-day update and tell me when I should expect the next checkpoint.","isTruncated":false}}},{"id":"email-2","threadId":"thread-1","subject":"Urgent: Acme renewal blocker","preview":"I can send the status update by 14:00 UTC today.","receivedAt":"2026-04-19T12:00:00Z","mailboxIds":{"sent":true},"keywords":{"$sent":true},"from":[{"name":"Memax","email":"me@example.com"}],"to":[{"name":"Jordan","email":"jordan@acme.example"}],"textBody":[{"partId":"1"}],"bodyValues":{"1":{"value":"I can send the status update by 14:00 UTC today.","isTruncated":false}}}]},"0"]]}`))
				return
			}
			panic(fmt.Sprintf("unexpected Email/get ids %#v", ids))
		case "Thread/get":
			serverMu.Lock()
			created := submittedMail
			serverMu.Unlock()
			if created {
				_, _ = w.Write([]byte(`{"methodResponses":[["Thread/get",{"list":[{"id":"thread-1","emailIds":["email-1","email-2"]}]},"0"]]}`))
				return
			}
			_, _ = w.Write([]byte(`{"methodResponses":[["Thread/get",{"list":[{"id":"thread-1","emailIds":["email-1"]}]},"0"]]}`))
		case "Email/set":
			_, _ = w.Write([]byte(`{"methodResponses":[["Email/set",{"created":{"email":{"id":"email-2"}}},"0"]]}`))
		case "EmailSubmission/set":
			serverMu.Lock()
			submittedMail = true
			serverMu.Unlock()
			_, _ = w.Write([]byte(`{"methodResponses":[["EmailSubmission/set",{"created":{"submission":{"id":"submission-1","emailId":"email-2"}}},"0"]]}`))
		default:
			panic(fmt.Sprintf("unexpected method %q", method))
		}
	}))

	client, clientErr := jmapclient.New(server.URL, "acc")
	messageStore, storeErr := jmapstore.New(client,
		jmapstore.WithDefaultIdentity("identity-1"),
		jmapstore.WithDefaultSender("Memax", "me@example.com"),
		jmapstore.WithDraftMailbox("drafts"),
	)
	taskStore := tasktools.NewMemoryStore([]tasktools.Task{{
		ID:     "task-1",
		Title:  "triage the urgent JMAP inbox thread",
		Status: tasktools.StatusInProgress,
		Notes:  "search inbox metadata first, read the thread before drafting, and use the attached JMAP inbox backend for the approved reply",
	}})
	modelClient := agenteval.NewScriptedModel(
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:    "search-1",
				Name:  messagetools.SearchToolName,
				Input: json.RawMessage(`{"query":"urgent renewal same-day checkpoint","mailboxes":["INBOX"],"unread":true,"limit":3}`),
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
					"reason":"sending an external customer reply requires approval",
					"tool_input":{
						"thread_id":"thread-1",
						"subject":"Urgent: Acme renewal blocker",
						"body":"I can send the status update by 14:00 UTC today.",
						"recipients":[{"name":"Jordan","address":"jordan@acme.example","role":"to"}]
					}
				}`),
			},
		}},
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:   "send-1",
				Name: messagetools.SendToolName,
				Input: json.RawMessage(`{
					"thread_id":"thread-1",
					"subject":"Urgent: Acme renewal blocker",
					"body":"I can send the status update by 14:00 UTC today.",
					"recipients":[{"name":"Jordan","address":"jordan@acme.example","role":"to"}]
				}`),
			},
		}},
		[]model.StreamEvent{{Kind: model.StreamText, Text: "Personal assistant triaged the JMAP inbox thread from metadata and sent the approved reply through the attached inbox backend."}},
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
			Reason:   "approved urgent customer reply",
		},
	}
	stack, stackErr := personal.New(config)

	return agenteval.Case{
		Name:    "personal_preset_personal_assistant_jmap_inbox_reply",
		Prompt:  "Triage urgent unread inbox threads carefully, read before drafting, and send the approved reply through the attached JMAP inbox backend.",
		Options: stack.WithModel(modelClient),
		Assertions: []agenteval.Assertion{
			toolConstructionSucceeded(clientErr, storeErr, configErr, stackErr),
			agenteval.ToolUsed(messagetools.SearchToolName),
			agenteval.ToolUsed(messagetools.ReadToolName),
			agenteval.ToolUsed(approvaltools.ToolName),
			agenteval.ToolUsed(messagetools.SendToolName),
			agenteval.EventKindEmitted(memaxagent.EventApprovalRequested),
			agenteval.EventKindEmitted(memaxagent.EventApprovalGranted),
			agenteval.EventKindEmitted(memaxagent.EventApprovalConsumed),
			agenteval.FinalEquals("Personal assistant triaged the JMAP inbox thread from metadata and sent the approved reply through the attached inbox backend."),
			requestCountEquals(modelClient, 5),
			{
				Name: "workflow stays metadata-first and drives the JMAP adapter end to end",
				Check: func(result agenteval.Result) error {
					toolResults := result.ToolResults()
					if len(toolResults) != 4 {
						return fmt.Errorf("tool results = %#v, want search read approval send", toolResults)
					}
					if toolResults[0].IsError || !strings.Contains(toolResults[0].Content, "Urgent: Acme renewal blocker") {
						return fmt.Errorf("search result = %#v, want urgent inbox metadata", toolResults[0])
					}
					if strings.Contains(toolResults[0].Content, "Please send me a same-day update and tell me when I should expect the next checkpoint.") {
						return fmt.Errorf("search result leaked full body: %#v", toolResults[0])
					}
					if toolResults[1].IsError || !strings.Contains(toolResults[1].Content, "Please send me a same-day update") {
						return fmt.Errorf("read result = %#v, want full thread body", toolResults[1])
					}
					if toolResults[2].IsError || toolResults[2].Metadata[approvaltools.MetadataApprovalApproved] != true {
						return fmt.Errorf("approval result = %#v, want granted approval", toolResults[2])
					}
					if toolResults[3].IsError || !strings.Contains(toolResults[3].Content, "sent message Urgent: Acme renewal blocker") {
						return fmt.Errorf("send result = %#v, want persisted send success", toolResults[3])
					}
					serverMu.Lock()
					defer serverMu.Unlock()
					want := []string{
						"Email/query",
						"Email/get",
						"Thread/get",
						"Email/get",
						"Email/set",
						"EmailSubmission/set",
						"Email/get",
						"Thread/get",
						"Email/get",
					}
					if len(methods) != len(want) {
						return fmt.Errorf("jmap methods = %#v, want %v", methods, want)
					}
					for i, method := range want {
						if methods[i] != method {
							return fmt.Errorf("jmap methods = %#v, want %v", methods, want)
						}
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

func fireScheduledTriggerOnce(ctx context.Context, stack personal.Stack, store personal.ScheduledRunStore, now time.Time, trigger personal.ScheduledTrigger) (personal.ScheduledRunRecord, personal.ScheduledRunRecord, bool, error) {
	results, err := stack.FireScheduledTriggers(ctx, store, now, trigger)
	if err != nil {
		return personal.ScheduledRunRecord{}, personal.ScheduledRunRecord{}, false, err
	}
	if len(results) != 1 || !results[0].Created {
		return personal.ScheduledRunRecord{}, personal.ScheduledRunRecord{}, false, fmt.Errorf("scheduled trigger fire = %#v, want one created run", results)
	}
	finalRun := waitForScheduledRun(store, results[0].Record.ID)
	duplicateResults, err := stack.FireScheduledTriggers(ctx, store, now, trigger)
	if err != nil {
		return finalRun, personal.ScheduledRunRecord{}, false, err
	}
	if len(duplicateResults) != 1 {
		return finalRun, personal.ScheduledRunRecord{}, false, fmt.Errorf("duplicate scheduled trigger fire = %#v, want existing run", duplicateResults)
	}
	return finalRun, duplicateResults[0].Record, duplicateResults[0].Created, nil
}

func fireScheduledWorkflowOnce(ctx context.Context, stack personal.Stack, store personal.ScheduledRunStore, registry personal.ScheduledWorkflowRegistry, now time.Time, name string) (personal.ScheduledRunRecord, personal.ScheduledRunRecord, bool, error) {
	results, err := stack.FireScheduledWorkflows(ctx, store, registry, now, name)
	if err != nil {
		return personal.ScheduledRunRecord{}, personal.ScheduledRunRecord{}, false, err
	}
	if len(results) != 1 || results[0].Workflow.Name != name || !results[0].Fire.Created {
		return personal.ScheduledRunRecord{}, personal.ScheduledRunRecord{}, false, fmt.Errorf("scheduled workflow fire = %#v, want one created run for %q", results, name)
	}
	finalRun := waitForScheduledRun(store, results[0].Fire.Record.ID)
	duplicateResults, err := stack.FireScheduledWorkflows(ctx, store, registry, now, name)
	if err != nil {
		return finalRun, personal.ScheduledRunRecord{}, false, err
	}
	if len(duplicateResults) != 1 || duplicateResults[0].Workflow.Name != name {
		return finalRun, personal.ScheduledRunRecord{}, false, fmt.Errorf("duplicate scheduled workflow fire = %#v, want existing run for %q", duplicateResults, name)
	}
	return finalRun, duplicateResults[0].Fire.Record, duplicateResults[0].Fire.Created, nil
}

func waitForScheduledRun(store personal.ScheduledRunStore, id string) personal.ScheduledRunRecord {
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		record, err := store.GetScheduledRun(context.Background(), id)
		if err == nil && record.Terminal() {
			return record
		}
		time.Sleep(10 * time.Millisecond)
	}
	record, _ := store.GetScheduledRun(context.Background(), id)
	return record
}
