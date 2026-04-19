package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"time"

	memaxagent "github.com/MemaxLabs/memax-go-agent-sdk"
	"github.com/MemaxLabs/memax-go-agent-sdk/messaging"
	"github.com/MemaxLabs/memax-go-agent-sdk/model"
	"github.com/MemaxLabs/memax-go-agent-sdk/scheduling"
	"github.com/MemaxLabs/memax-go-agent-sdk/stack/personal"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/approvaltools"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/messagetools"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/scheduletools"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/tasktools"
)

func main() {
	if err := runExample(context.Background(), os.Stdout); err != nil {
		log.Fatal(err)
	}
}

// runExample walks through an inbox-triage personal_assistant flow. The
// scripted model triages an unread inbox thread from metadata first, reads the
// full thread only before drafting, recovers through approval to send the
// reply, and then creates an approval-gated follow-up reminder.
func runExample(ctx context.Context, w io.Writer) error {
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
	tasks := tasktools.NewMemoryStore([]tasktools.Task{{
		ID:     "task-1",
		Title:  "Triage the urgent Acme inbox thread",
		Status: tasktools.StatusInProgress,
		Notes:  "search unread inbox metadata first, then read the thread before replying, and create a follow-up reminder after the approved reply",
	}})

	config := personal.PersonalAssistant()
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
	config.Tasks = tasks
	config.Approval.Approver = approvaltools.StaticApprover{
		Decision: approvaltools.Decision{
			Approved: true,
			Reason:   "approved urgent customer workflow",
		},
	}

	stack, err := personal.New(config)
	if err != nil {
		return err
	}

	events, err := memaxagent.Query(ctx, "Triage urgent unread inbox threads carefully, only read a thread before drafting a reply, and create a follow-up reminder after the approved reply if the thread needs one.", stack.WithModel(&personalInboxModel{}))
	if err != nil {
		return err
	}

	for event := range events {
		switch event.Kind {
		case memaxagent.EventToolUse:
			fmt.Fprintf(w, "tool use: %s\n", event.ToolUse.Name)
		case memaxagent.EventApprovalRequested:
			fmt.Fprintf(w, "approval requested: %s\n", event.Approval.Action)
		case memaxagent.EventApprovalGranted:
			fmt.Fprintf(w, "approval granted: %s\n", event.Approval.Action)
		case memaxagent.EventApprovalConsumed:
			fmt.Fprintf(w, "approval consumed: %s\n", event.Approval.Action)
		case memaxagent.EventToolResult:
			fmt.Fprintf(w, "tool result: %s\n", event.ToolResult.Content)
		case memaxagent.EventResult:
			fmt.Fprintf(w, "result: %s\n", event.Result)
		case memaxagent.EventError:
			return event.Err
		}
	}
	return nil
}

type personalInboxModel struct {
	turn int
}

func (m *personalInboxModel) Stream(_ context.Context, _ model.Request) (model.Stream, error) {
	m.turn++
	switch m.turn {
	case 1:
		return newStream(toolUse("search-1", messagetools.SearchToolName, map[string]any{
			"query":     "urgent renewal blocker same-day update",
			"mailboxes": []string{"INBOX"},
			"from":      []string{"casey@acme.example"},
			"unread":    true,
			"limit":     3,
		})), nil
	case 2:
		return newStream(toolUse("read-1", messagetools.ReadToolName, map[string]any{
			"thread_id": "thread-1",
		})), nil
	case 3:
		return newStream(toolUse("send-1", messagetools.SendToolName, sendInput())), nil
	case 4:
		return newStream(toolUse("approval-1", approvaltools.ToolName, map[string]any{
			"action":     messagetools.SendToolName,
			"reason":     "sending an urgent customer update requires approval",
			"tool_input": sendInput(),
		})), nil
	case 5:
		return newStream(toolUse("send-2", messagetools.SendToolName, sendInput())), nil
	case 6:
		return newStream(toolUse("approval-2", approvaltools.ToolName, map[string]any{
			"action":     scheduletools.CreateToolName,
			"reason":     "creating a customer follow-up reminder requires approval",
			"tool_input": createInput(),
		})), nil
	case 7:
		return newStream(toolUse("create-1", scheduletools.CreateToolName, createInput())), nil
	default:
		return newStream(model.StreamEvent{
			Kind: model.StreamText,
			Text: "Triaged the urgent Acme inbox thread from metadata, recovered through approval to send the reply, and created a same-day follow-up reminder.",
		}), nil
	}
}

func sendInput() map[string]any {
	return map[string]any{
		"thread_id": "thread-1",
		"body":      "Thanks, Casey. We are treating this as urgent and I will send you the next update by 14:00 UTC today.",
		"recipients": []map[string]any{
			{"name": "Casey", "address": "casey@acme.example"},
		},
	}
}

func createInput() map[string]any {
	return map[string]any{
		"title":       "Acme blocker follow-up",
		"summary":     "Send Casey the promised same-day checkout update.",
		"description": "Follow up on the Acme renewal blocker and send the 14:00 UTC status update.",
		"start":       "2026-04-19T13:45:00Z",
		"end":         "2026-04-19T14:00:00Z",
		"time_zone":   "UTC",
		"attendees": []map[string]any{
			{"name": "Casey", "address": "casey@acme.example"},
		},
		"tags": []string{"follow-up", "customer", "urgent"},
	}
}

func toolUse(id string, name string, input map[string]any) model.StreamEvent {
	return model.StreamEvent{
		Kind: model.StreamToolUse,
		ToolUse: model.ToolUse{
			ID:    id,
			Name:  name,
			Input: mustJSON(input),
		},
	}
}

func mustJSON(v any) json.RawMessage {
	data, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return data
}

type stream struct {
	events []model.StreamEvent
	index  int
}

func newStream(events ...model.StreamEvent) *stream {
	return &stream{events: events}
}

func (s *stream) Recv() (model.StreamEvent, error) {
	if s.index >= len(s.events) {
		return model.StreamEvent{}, model.ErrEndOfStream
	}
	event := s.events[s.index]
	s.index++
	return event, nil
}

func (s *stream) Close() error {
	return nil
}
