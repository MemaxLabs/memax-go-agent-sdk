package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"strings"

	memaxagent "github.com/MemaxLabs/memax-go-agent-sdk"
	"github.com/MemaxLabs/memax-go-agent-sdk/messaging"
	"github.com/MemaxLabs/memax-go-agent-sdk/model"
	"github.com/MemaxLabs/memax-go-agent-sdk/stack/personal"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/approvaltools"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/messagetools"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/tasktools"
)

func main() {
	if err := runExample(context.Background(), os.Stdout); err != nil {
		log.Fatal(err)
	}
}

// runExample walks through a message-first personal_assistant flow. The
// scripted model searches thread metadata, reads one seeded thread, and only
// then sends an approved reply that reflects the recalled guidance.
func runExample(ctx context.Context, w io.Writer) error {
	messageStore := messaging.NewThreadStore([]messaging.Thread{{
		ID:      "thread-1",
		Subject: "Project kickoff follow-up",
		Summary: "Alex wants concise replies with owners and due dates.",
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
	tasks := tasktools.NewMemoryStore([]tasktools.Task{{
		ID:     "task-1",
		Title:  "Reply to kickoff follow-up",
		Status: tasktools.StatusInProgress,
		Notes:  "search message metadata first, then read the thread before sending an outbound reply",
	}})

	config := personal.PersonalAssistant()
	config.Messages = messagetools.Config{
		Searcher:     messageStore,
		Reader:       messageStore,
		Sender:       messageStore,
		DefaultLimit: 3,
	}
	config.Tasks = tasks
	config.Approval.Approver = approvaltools.StaticApprover{
		Decision: approvaltools.Decision{
			Approved: true,
			Reason:   "approved outbound project follow-up",
		},
	}

	stack, err := personal.New(config)
	if err != nil {
		return err
	}

	events, err := memaxagent.Query(ctx, "Reply to the kickoff follow-up carefully, but search message metadata first and read the thread before sending anything.", stack.WithModel(&personalMessagesModel{}))
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

type personalMessagesModel struct {
	turn      int
	sendInput map[string]any
}

func (m *personalMessagesModel) Stream(_ context.Context, req model.Request) (model.Stream, error) {
	m.turn++
	switch m.turn {
	case 1:
		return newStream(toolUse("search-1", messagetools.SearchToolName, map[string]any{
			"query": "kickoff follow-up owners due dates",
			"limit": 3,
		})), nil
	case 2:
		return newStream(toolUse("read-1", messagetools.ReadToolName, map[string]any{
			"thread_id": "thread-1",
		})), nil
	case 3:
		body := "Thanks. I will send a follow-up soon."
		if requestContains(req, "owners and due dates") {
			body = "Thanks. I'll keep the update concise and call out owners and due dates in the follow-up."
		}
		m.sendInput = map[string]any{
			"thread_id": "thread-1",
			"body":      body,
			"recipients": []map[string]any{
				{"name": "Alex", "address": "alex@example.com"},
			},
		}
		return newStream(toolUse("approval-1", approvaltools.ToolName, map[string]any{
			"action":     messagetools.SendToolName,
			"reason":     "sending an outbound project follow-up requires approval",
			"tool_input": m.sendInput,
		})), nil
	case 4:
		return newStream(toolUse("send-1", messagetools.SendToolName, m.sendInput)), nil
	case 5:
		return newStream(toolUse("search-2", messagetools.SearchToolName, map[string]any{
			"query": "kickoff follow-up concise owners due dates",
			"limit": 3,
		})), nil
	default:
		text := "Sent a project follow-up."
		if body, _ := m.sendInput["body"].(string); strings.Contains(body, "owners and due dates") {
			text = "Recalled the existing thread guidance, sent an approved reply, and confirmed the thread is still discoverable."
		}
		return newStream(model.StreamEvent{Kind: model.StreamText, Text: text}), nil
	}
}

func requestContains(req model.Request, needle string) bool {
	for _, msg := range req.Messages {
		if strings.Contains(msg.PlainText(), needle) {
			return true
		}
		if msg.ToolResult != nil && strings.Contains(msg.ToolResult.Content, needle) {
			return true
		}
	}
	return strings.Contains(req.SystemPrompt, needle)
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
