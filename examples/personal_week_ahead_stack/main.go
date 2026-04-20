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
	"github.com/MemaxLabs/memax-go-agent-sdk/memory"
	"github.com/MemaxLabs/memax-go-agent-sdk/messaging"
	"github.com/MemaxLabs/memax-go-agent-sdk/model"
	"github.com/MemaxLabs/memax-go-agent-sdk/notes"
	"github.com/MemaxLabs/memax-go-agent-sdk/scheduling"
	"github.com/MemaxLabs/memax-go-agent-sdk/stack/personal"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/memorytools"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/messagetools"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/notetools"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/scheduletools"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/tasktools"
)

func main() {
	if err := runExample(context.Background(), os.Stdout); err != nil {
		log.Fatal(err)
	}
}

// runExample walks through a week-ahead planning workflow. The scripted model
// recalls planning preferences, searches note, inbox, and schedule metadata,
// reads only the selected source details, and synthesizes conflicts,
// commitments, prep blocks, and follow-ups for the week.
func runExample(ctx context.Context, w io.Writer) error {
	stack, err := buildWeekAheadStack()
	if err != nil {
		return err
	}

	events, err := memaxagent.Query(ctx, "Prepare my week-ahead plan for 2026-04-20 through 2026-04-26. Search durable context, notes, unread inbox metadata, and calendar metadata first; read only what you need to produce conflicts, commitments, prep blocks, and follow-ups.", stack.WithModel(&weekAheadModel{}))
	if err != nil {
		return err
	}

	for event := range events {
		switch event.Kind {
		case memaxagent.EventToolUse:
			fmt.Fprintf(w, "tool use: %s\n", event.ToolUse.Name)
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

func buildWeekAheadStack() (personal.Stack, error) {
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
	tasks := tasktools.NewMemoryStore([]tasktools.Task{{
		ID:     "task-1",
		Title:  "Assemble the week-ahead plan",
		Status: tasktools.StatusInProgress,
		Notes:  "search memory, notes, unread inbox, and calendar metadata first; read only selected details before synthesizing conflicts, commitments, prep blocks, and follow-ups",
	}})

	config := personal.PersonalAssistant()
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
	config.Tasks = tasks

	return personal.New(config)
}

type weekAheadModel struct {
	turn int
}

func (m *weekAheadModel) Stream(_ context.Context, _ model.Request) (model.Stream, error) {
	m.turn++
	switch m.turn {
	case 1:
		return newStream(toolUse("search-memory-1", memorytools.SearchToolName, map[string]any{
			"query": "week ahead planning conflicts commitments prep follow-ups",
			"limit": 3,
		})), nil
	case 2:
		return newStream(toolUse("search-note-1", notetools.SearchToolName, map[string]any{
			"query": "Q2 launch planning Acme renewal partner council demo",
			"limit": 3,
		})), nil
	case 3:
		return newStream(toolUse("search-message-1", messagetools.SearchToolName, map[string]any{
			"query":     "Acme renewal blocker partner council demo slides",
			"mailboxes": []string{"INBOX"},
			"unread":    true,
			"since":     "2026-04-19T00:00:00Z",
			"until":     "2026-04-27T00:00:00Z",
			"limit":     4,
		})), nil
	case 4:
		return newStream(toolUse("search-schedule-1", scheduletools.SearchToolName, map[string]any{
			"query": "Acme renewal launch risk review partner council demo",
			"start": "2026-04-20T00:00:00Z",
			"end":   "2026-04-27T00:00:00Z",
			"limit": 5,
		})), nil
	case 5:
		return newStream(toolUse("read-note-1", notetools.ReadToolName, map[string]any{
			"id": "note-1",
		})), nil
	case 6:
		return newStream(toolUse("read-thread-1", messagetools.ReadToolName, map[string]any{
			"thread_id": "thread-1",
		})), nil
	case 7:
		return newStream(toolUse("read-thread-2", messagetools.ReadToolName, map[string]any{
			"thread_id": "thread-2",
		})), nil
	case 8:
		return newStream(toolUse("read-event-1", scheduletools.ReadToolName, map[string]any{
			"id": "event-1",
		})), nil
	case 9:
		return newStream(toolUse("read-event-2", scheduletools.ReadToolName, map[string]any{
			"id": "event-2",
		})), nil
	case 10:
		return newStream(toolUse("read-event-3", scheduletools.ReadToolName, map[string]any{
			"id": "event-3",
		})), nil
	default:
		return newStream(model.StreamEvent{
			Kind: model.StreamText,
			Text: "Week-ahead plan: Conflict first: Monday 13:30-14:30 UTC Acme renewal meeting overlaps the 14:00-14:30 UTC internal launch risk review, so protect the customer checkpoint and move or shorten the internal review. Commitments: send Casey the 14:00 UTC blocker checkpoint and deliver Priya's partner council demo slides by Wednesday 17:00 UTC. Prep: use the Q2 launch brief, checkout mitigation owner, pricing review packet, and Acme mitigation summary before Thursday 16:00 UTC partner council. Follow-ups: confirm the mitigation owner with Casey and give Priya the final demo-slide package.",
		}), nil
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
