package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"sync"
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

// runExample walks through one named host-owned proactive daily-brief workflow.
// The workflow fires once for its deterministic occurrence, the agent searches
// note, message, and schedule metadata before reading the matched items, and a
// second fire for the same occurrence is deduplicated by the scheduled-run
// store.
func runExample(ctx context.Context, w io.Writer) error {
	now := time.Date(2026, 4, 19, 7, 1, 0, 0, time.UTC)
	stack, store, registry, err := buildProactiveExample(now)
	if err != nil {
		return err
	}

	var (
		mu     sync.Mutex
		events []memaxagent.Event
	)
	observer := memaxagent.EventObserverFunc(func(_ context.Context, event memaxagent.Event) {
		mu.Lock()
		events = append(events, event)
		mu.Unlock()
	})

	runCtx := memaxagent.WithEventObserver(ctx, observer)
	results, err := stack.FireScheduledWorkflows(runCtx, store, registry, now, "daily-brief")
	if err != nil {
		return err
	}
	if len(results) != 1 || results[0].Workflow.Name != "daily-brief" || !results[0].Fire.Created {
		return fmt.Errorf("scheduled workflow fire = %#v, want one created daily brief run", results)
	}
	runID := results[0].Fire.Record.ID
	finalRun, err := waitForScheduledRun(store, runID, func(record personal.ScheduledRunRecord) bool { return record.Terminal() })
	if err != nil {
		return err
	}
	duplicateResults, err := stack.FireScheduledWorkflows(runCtx, store, registry, now, "daily-brief")
	if err != nil {
		return err
	}
	if len(duplicateResults) != 1 || duplicateResults[0].Workflow.Name != "daily-brief" {
		return fmt.Errorf("duplicate workflow fire = %#v, want existing daily brief run", duplicateResults)
	}
	duplicateRun := duplicateResults[0].Fire.Record
	created := duplicateResults[0].Fire.Created

	mu.Lock()
	captured := append([]memaxagent.Event(nil), events...)
	mu.Unlock()

	for _, event := range captured {
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

	fmt.Fprintf(w, "scheduled run: %s %s\n", finalRun.ID, finalRun.Status)
	fmt.Fprintf(w, "scheduled workflow: %s\n", results[0].Workflow.Name)
	fmt.Fprintf(w, "scheduled session: %s\n", finalRun.SessionID)
	fmt.Fprintf(w, "duplicate fire reused run: %s created=%t\n", duplicateRun.ID, created)
	return nil
}

func buildProactiveExample(now time.Time) (personal.Stack, personal.ScheduledRunStore, personal.ScheduledWorkflowRegistry, error) {
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
	tasks := tasktools.NewMemoryStore([]tasktools.Task{{
		ID:     "task-1",
		Title:  "Prepare the morning briefing",
		Status: tasktools.StatusInProgress,
		Notes:  "search note, message, and schedule metadata before reading the full items you need for the briefing",
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
		DefaultLimit: 3,
	}
	config.Schedule = scheduletools.Config{
		Searcher:     scheduleStore,
		Reader:       scheduleStore,
		DefaultLimit: 3,
	}
	config.Tasks = tasks
	config.Base.Model = &proactiveBriefingModel{}

	stack, err := personal.New(config)
	if err != nil {
		return personal.Stack{}, nil, nil, err
	}
	store := personal.NewMemoryScheduledRunStore()
	trigger := personal.PeriodicTrigger{
		Name:   "daily-brief",
		Prompt: "Prepare this morning's briefing. Search note, message, and schedule metadata first, then read only the items you need.",
		Every:  24 * time.Hour,
		Anchor: time.Date(now.Year(), now.Month(), now.Day(), 7, 0, 0, 0, time.UTC),
	}
	registry, err := personal.NewMemoryScheduledWorkflowRegistry(personal.ScheduledWorkflow{
		Name:        "daily-brief",
		Description: "Prepare a metadata-first daily briefing.",
		Tags:        []string{"briefing", "morning"},
		Trigger:     trigger,
	})
	if err != nil {
		return personal.Stack{}, nil, nil, err
	}
	return stack, store, registry, nil
}

type proactiveBriefingModel struct {
	turn int
}

func (m *proactiveBriefingModel) Stream(_ context.Context, _ model.Request) (model.Stream, error) {
	m.turn++
	switch m.turn {
	case 1:
		return newStream(toolUse("search-note-1", notetools.SearchToolName, map[string]any{
			"query": "morning briefing urgent changes travel prep",
			"limit": 3,
		})), nil
	case 2:
		return newStream(toolUse("search-thread-1", messagetools.SearchToolName, map[string]any{
			"query": "travel update passport flight",
			"limit": 3,
		})), nil
	case 3:
		return newStream(toolUse("search-event-1", scheduletools.SearchToolName, map[string]any{
			"query": "design review vendor budget",
			"limit": 3,
		})), nil
	case 4:
		return newStream(toolUse("read-note-1", notetools.ReadToolName, map[string]any{
			"id": "note-1",
		})), nil
	case 5:
		return newStream(toolUse("read-thread-1", messagetools.ReadToolName, map[string]any{
			"thread_id": "thread-1",
		})), nil
	case 6:
		return newStream(toolUse("read-event-1", scheduletools.ReadToolName, map[string]any{
			"id": "event-1",
		})), nil
	default:
		return newStream(model.StreamEvent{
			Kind: model.StreamText,
			Text: "Morning briefing: urgent change first, your design review is at 09:00 UTC in Room 5A, and Jordan says the flight moved to 3:30 PM so bring your passport.",
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

func waitForScheduledRun(store personal.ScheduledRunStore, id string, done func(personal.ScheduledRunRecord) bool) (personal.ScheduledRunRecord, error) {
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		record, err := store.GetScheduledRun(context.Background(), id)
		if err == nil && done(record) {
			return record, nil
		}
		time.Sleep(time.Millisecond)
	}
	record, err := store.GetScheduledRun(context.Background(), id)
	if err != nil {
		return personal.ScheduledRunRecord{}, err
	}
	return personal.ScheduledRunRecord{}, fmt.Errorf("scheduled run %q did not finish: %#v", id, record)
}
