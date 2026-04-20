package main

import (
	"context"
	"database/sql"
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
	"github.com/MemaxLabs/memax-go-agent-sdk/scheduling"
	"github.com/MemaxLabs/memax-go-agent-sdk/stack/personal"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/memorytools"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/messagetools"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/scheduletools"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/tasktools"
	tasksqlitestore "github.com/MemaxLabs/memax-go-agent-sdk/toolkit/tasktools/sqlitestore"
	_ "modernc.org/sqlite"
)

func main() {
	if err := runExample(context.Background(), os.Stdout); err != nil {
		log.Fatal(err)
	}
}

// runExample walks through two personal-assistant invocations over one durable
// SQLite task ledger. The first run creates deterministic follow-up tasks from
// recalled context, inbox threads, and schedule events. The second run reopens
// the SQLite ledger with a fresh database handle, resumes the pending tasks
// before rediscovery, and updates only the completed follow-up.
func runExample(ctx context.Context, w io.Writer) error {
	file, err := os.CreateTemp("", "memax-personal-task-ledger-*.db")
	if err != nil {
		return fmt.Errorf("create task ledger db: %w", err)
	}
	dbPath := file.Name()
	if err := file.Close(); err != nil {
		_ = os.Remove(dbPath)
		return fmt.Errorf("close task ledger temp file: %w", err)
	}
	defer os.Remove(dbPath)

	modelClient := &taskLedgerModel{}
	db, taskStore, err := openTaskStore(ctx, dbPath)
	if err != nil {
		return err
	}
	if _, err := taskStore.Upsert(ctx, tasktools.Task{
		ID:     "task-1",
		Title:  "Assemble the week-ahead follow-up ledger",
		Status: tasktools.StatusInProgress,
		Notes:  "persist owner-visible follow-ups as durable tasks with deterministic IDs",
	}); err != nil {
		_ = db.Close()
		return err
	}

	stack, err := buildTaskLedgerStack(taskStore, modelClient)
	if err != nil {
		_ = db.Close()
		return err
	}
	if err := runQuery(ctx, w, "first run", stack, "Prepare my week-ahead follow-up ledger for 2026-04-20 through 2026-04-26. Search durable context, unread inbox metadata, and calendar metadata first; read only selected details, then persist each owner-visible follow-up as a task with a deterministic ID."); err != nil {
		_ = db.Close()
		return err
	}
	if err := db.Close(); err != nil {
		return fmt.Errorf("close first task ledger db: %w", err)
	}

	fmt.Fprintln(w, "reopened sqlite task ledger")
	db, taskStore, err = openTaskStore(ctx, dbPath)
	if err != nil {
		return err
	}
	defer db.Close()

	stack, err = buildTaskLedgerStack(taskStore, modelClient)
	if err != nil {
		return err
	}
	if err := runQuery(ctx, w, "second run", stack, "Resume the pending week-ahead follow-ups from the task ledger. Do not rediscover or duplicate tasks; list pending tasks and update only completed work."); err != nil {
		return err
	}

	tasks, err := taskStore.List(ctx)
	if err != nil {
		return err
	}
	for _, task := range tasks {
		fmt.Fprintf(w, "task: %s %s %s\n", task.ID, task.Status, task.Title)
	}
	return nil
}

func openTaskStore(ctx context.Context, path string) (*sql.DB, tasktools.Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, nil, fmt.Errorf("open task ledger db: %w", err)
	}
	store, err := tasksqlitestore.New(ctx, db)
	if err != nil {
		_ = db.Close()
		return nil, nil, err
	}
	return db, store, nil
}

func runQuery(ctx context.Context, w io.Writer, label string, stack personal.Stack, prompt string) error {
	fmt.Fprintf(w, "== %s ==\n", label)
	events, err := memaxagent.Query(ctx, prompt, stack.Options())
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

func buildTaskLedgerStack(tasks tasktools.Store, client model.Client) (personal.Stack, error) {
	memoryStore := memory.NewMemoryStore([]memory.Memory{{
		ID:      "memory-1",
		Name:    "task-ledger-style",
		Scope:   memory.ScopeUser,
		Content: "For week-ahead follow-ups, convert owner-visible commitments into durable task state with deterministic IDs.",
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
			ID:          "event-1",
			Title:       "Acme renewal meeting",
			Summary:     "Customer renewal meeting needs a named mitigation owner.",
			Location:    "Video",
			Organizer:   scheduling.Participant{Name: "Casey", Address: "casey@acme.example"},
			Start:       mondayAcme,
			End:         mondayAcme.Add(time.Hour),
			TimeZone:    "UTC",
			Description: "Bring checkout status, mitigation owner, and the Monday 14:00 UTC customer checkpoint plan.",
			Tags:        []string{"customer", "renewal", "acme"},
		},
		{
			ID:          "event-2",
			Title:       "Partner council demo",
			Summary:     "Demo Q2 launch readiness to the partner council.",
			Location:    "Boardroom",
			Organizer:   scheduling.Participant{Name: "Priya", Address: "priya@example.com"},
			Start:       thursdayCouncil,
			End:         thursdayCouncil.Add(time.Hour),
			TimeZone:    "UTC",
			Description: "Requires final demo slides and the Acme mitigation summary before the council.",
			Tags:        []string{"partner", "launch", "demo"},
		},
	})

	config := personal.PersonalAssistant()
	config.Base.Model = client
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
	config.Tasks = tasks
	return personal.New(config)
}

type taskLedgerModel struct {
	turn int
}

func (m *taskLedgerModel) Stream(_ context.Context, _ model.Request) (model.Stream, error) {
	m.turn++
	switch m.turn {
	case 1:
		return newStream(toolUse("search-memory-1", memorytools.SearchToolName, map[string]any{
			"query": "week ahead task ledger follow-ups",
			"limit": 3,
		})), nil
	case 2:
		return newStream(toolUse("search-message-1", messagetools.SearchToolName, map[string]any{
			"query":     "Acme owner partner council demo slides",
			"mailboxes": []string{"INBOX"},
			"unread":    true,
			"since":     "2026-04-19T00:00:00Z",
			"until":     "2026-04-27T00:00:00Z",
			"limit":     4,
		})), nil
	case 3:
		return newStream(toolUse("read-thread-1", messagetools.ReadToolName, map[string]any{
			"thread_id": "thread-1",
		})), nil
	case 4:
		return newStream(toolUse("read-thread-2", messagetools.ReadToolName, map[string]any{
			"thread_id": "thread-2",
		})), nil
	case 5:
		return newStream(toolUse("search-schedule-1", scheduletools.SearchToolName, map[string]any{
			"query": "Acme renewal partner council demo",
			"start": "2026-04-20T00:00:00Z",
			"end":   "2026-04-27T00:00:00Z",
			"limit": 5,
		})), nil
	case 6:
		return newStream(toolUse("read-event-1", scheduletools.ReadToolName, map[string]any{
			"id": "event-1",
		})), nil
	case 7:
		return newStream(toolUse("read-event-2", scheduletools.ReadToolName, map[string]any{
			"id": "event-2",
		})), nil
	case 8:
		return newStream(toolUse("upsert-task-1", tasktools.UpsertToolName, map[string]any{
			"id":       "week-2026-04-20-acme-owner",
			"title":    "Confirm Acme mitigation owner",
			"status":   "pending",
			"notes":    "Casey needs the mitigation owner before the Monday 14:00 UTC customer checkpoint.",
			"priority": 1,
			"evidence": []string{"thread-1", "event-1"},
		})), nil
	case 9:
		return newStream(toolUse("upsert-task-2", tasktools.UpsertToolName, map[string]any{
			"id":       "week-2026-04-20-demo-slides",
			"title":    "Deliver partner council demo slides",
			"status":   "pending",
			"notes":    "Priya needs final demo slides by Wednesday 17:00 UTC for Thursday partner council.",
			"priority": 2,
			"evidence": []string{"thread-2", "event-2"},
		})), nil
	case 10:
		return newStream(model.StreamEvent{
			Kind: model.StreamText,
			Text: "Week-ahead task ledger updated: created follow-up tasks for Acme mitigation owner and partner council demo slides.",
		}), nil
	case 11:
		return newStream(toolUse("list-tasks-1", tasktools.ListToolName, map[string]any{
			"status": "pending",
		})), nil
	case 12:
		return newStream(toolUse("complete-task-1", tasktools.UpsertToolName, map[string]any{
			"id":       "week-2026-04-20-acme-owner",
			"status":   "completed",
			"notes":    "Mitigation owner confirmed for the Monday 14:00 UTC customer checkpoint.",
			"evidence": []string{"thread-1", "event-1", "owner-confirmed"},
		})), nil
	default:
		return newStream(model.StreamEvent{
			Kind: model.StreamText,
			Text: "Resumed week-ahead task ledger: Acme owner follow-up is complete; partner council demo slides remain pending.",
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
