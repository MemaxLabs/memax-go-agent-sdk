package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"time"

	memaxagent "github.com/MemaxLabs/memax-go-agent-sdk"
	"github.com/MemaxLabs/memax-go-agent-sdk/model"
	"github.com/MemaxLabs/memax-go-agent-sdk/scheduling"
	"github.com/MemaxLabs/memax-go-agent-sdk/stack/personal"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/approvaltools"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/scheduletools"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/tasktools"
)

func main() {
	if err := runExample(context.Background(), os.Stdout); err != nil {
		log.Fatal(err)
	}
}

// runExample walks through a schedule-first personal_assistant flow. The
// scripted model searches event metadata, reads one seeded event, and only
// then reschedules it through the approval gate using the recalled event
// constraints.
func runExample(ctx context.Context, w io.Writer) error {
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
	tasks := tasktools.NewMemoryStore([]tasktools.Task{{
		ID:     "task-1",
		Title:  "Adjust the kickoff event",
		Status: tasktools.StatusInProgress,
		Notes:  "search schedule metadata first, load the event, then reschedule it through approval",
	}})

	config := personal.PersonalAssistant()
	config.Schedule = scheduletools.Config{
		Searcher:     scheduleStore,
		Reader:       scheduleStore,
		Rescheduler:  scheduleStore,
		DefaultLimit: 3,
	}
	config.Tasks = tasks
	config.Approval.Approver = approvaltools.StaticApprover{
		Decision: approvaltools.Decision{
			Approved: true,
			Reason:   "approved kickoff reschedule",
		},
	}

	stack, err := personal.New(config)
	if err != nil {
		return err
	}

	events, err := memaxagent.Query(ctx, "Adjust the kickoff event carefully, but search schedule metadata first and read the event before changing calendar state.", stack.WithModel(&personalScheduleModel{}))
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

type personalScheduleModel struct {
	turn            int
	rescheduleInput map[string]any
}

func (m *personalScheduleModel) Stream(_ context.Context, req model.Request) (model.Stream, error) {
	m.turn++
	switch m.turn {
	case 1:
		return newStream(toolUse("search-1", scheduletools.SearchToolName, map[string]any{
			"query": "kickoff owners due dates",
			"limit": 3,
		})), nil
	case 2:
		return newStream(toolUse("read-1", scheduletools.ReadToolName, map[string]any{
			"id": "event-1",
		})), nil
	case 3:
		start := "2026-04-20T17:00:00-07:00"
		end := "2026-04-20T18:00:00-07:00"
		if requestContains(req, "45 minutes") && requestContains(req, "4 PM Pacific") {
			start = "2026-04-20T15:15:00-07:00"
			end = "2026-04-20T16:00:00-07:00"
		}
		m.rescheduleInput = map[string]any{
			"id":        "event-1",
			"start":     start,
			"end":       end,
			"time_zone": "America/Los_Angeles",
		}
		return newStream(toolUse("approval-1", approvaltools.ToolName, map[string]any{
			"action":     scheduletools.RescheduleToolName,
			"reason":     "rescheduling a calendar event requires approval",
			"tool_input": m.rescheduleInput,
		})), nil
	case 4:
		return newStream(toolUse("reschedule-1", scheduletools.RescheduleToolName, m.rescheduleInput)), nil
	case 5:
		return newStream(toolUse("search-2", scheduletools.SearchToolName, map[string]any{
			"query": "kickoff america/los_angeles",
			"limit": 3,
		})), nil
	default:
		text := "Rescheduled the kickoff event."
		if start, _ := m.rescheduleInput["start"].(string); strings.Contains(start, "15:15:00-07:00") {
			text = "Recalled the existing event constraints, rescheduled the kickoff, and confirmed the updated event metadata."
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
