package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"sync"
	"time"

	memaxagent "github.com/MemaxLabs/memax-go-agent-sdk"
	"github.com/MemaxLabs/memax-go-agent-sdk/messaging"
	"github.com/MemaxLabs/memax-go-agent-sdk/model"
	"github.com/MemaxLabs/memax-go-agent-sdk/stack/personal"
	personalsqlitestore "github.com/MemaxLabs/memax-go-agent-sdk/stack/personal/sqlitestore"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/approvaltools"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/messagetools"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/tasktools"
	_ "modernc.org/sqlite"
)

func main() {
	if err := runExample(context.Background(), os.Stdout); err != nil {
		log.Fatal(err)
	}
}

// runExample walks through one host-owned scheduled inbox triage trigger backed
// by the durable SQLite scheduled-run store. The trigger fires once for the
// current hourly occurrence, triages the unread inbox thread from metadata
// first, reads the selected thread before drafting, sends the approved reply,
// and treats a second fire for the same occurrence as a no-op.
func runExample(ctx context.Context, w io.Writer) error {
	now := time.Date(2026, 4, 19, 9, 5, 0, 0, time.UTC)
	stack, store, trigger, cleanup, err := buildExample(now)
	if err != nil {
		return err
	}
	defer cleanup()

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
	results, err := stack.FireScheduledTriggers(runCtx, store, now, trigger)
	if err != nil {
		return err
	}
	if len(results) != 1 || !results[0].Created {
		return fmt.Errorf("scheduled trigger fire = %#v, want one created inbox triage run", results)
	}
	runID := results[0].Record.ID
	finalRun, err := waitForScheduledRun(store, runID, func(record personal.ScheduledRunRecord) bool { return record.Terminal() })
	if err != nil {
		return err
	}
	duplicateResults, err := stack.FireScheduledTriggers(runCtx, store, now, trigger)
	if err != nil {
		return err
	}
	if len(duplicateResults) != 1 {
		return fmt.Errorf("duplicate trigger fire = %#v, want existing inbox triage run", duplicateResults)
	}
	duplicateRun := duplicateResults[0].Record
	created := duplicateResults[0].Created

	mu.Lock()
	captured := append([]memaxagent.Event(nil), events...)
	mu.Unlock()

	for _, event := range captured {
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

	fmt.Fprintf(w, "scheduled run: %s %s\n", finalRun.ID, finalRun.Status)
	fmt.Fprintf(w, "scheduled session: %s\n", finalRun.SessionID)
	fmt.Fprintf(w, "duplicate fire reused run: %s created=%t\n", duplicateRun.ID, created)
	return nil
}

func buildExample(now time.Time) (personal.Stack, personal.ScheduledRunStore, personal.PeriodicTrigger, func(), error) {
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
	tasks := tasktools.NewMemoryStore([]tasktools.Task{{
		ID:     "task-1",
		Title:  "Triage unread inbox threads",
		Status: tasktools.StatusInProgress,
		Notes:  "run the hourly unread inbox triage proactively, classify from metadata first, then read the selected thread before drafting the approved reply",
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
			Reason:   "approved scheduled urgent triage reply",
		},
	}
	config.Base.Model = &scheduledInboxModel{}

	stack, err := personal.New(config)
	if err != nil {
		return personal.Stack{}, nil, personal.PeriodicTrigger{}, nil, err
	}

	db, err := sql.Open("sqlite", "file:personal-scheduled-inbox?mode=memory&cache=shared")
	if err != nil {
		return personal.Stack{}, nil, personal.PeriodicTrigger{}, nil, err
	}
	store, err := personalsqlitestore.New(context.Background(), db)
	if err != nil {
		_ = db.Close()
		return personal.Stack{}, nil, personal.PeriodicTrigger{}, nil, err
	}
	trigger := personal.PeriodicTrigger{
		Name:   "inbox-triage",
		Prompt: "Run the hourly unread inbox triage. Search unread inbox metadata first, read only the selected thread, then send the approved reply.",
		Every:  time.Hour,
		Anchor: time.Date(now.Year(), now.Month(), now.Day(), 9, 0, 0, 0, time.UTC),
	}
	cleanup := func() {
		_ = db.Close()
	}
	return stack, store, trigger, cleanup, nil
}

type scheduledInboxModel struct {
	turn int
}

func (m *scheduledInboxModel) Stream(_ context.Context, _ model.Request) (model.Stream, error) {
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
		return newStream(toolUse("approval-1", approvaltools.ToolName, map[string]any{
			"action":     messagetools.SendToolName,
			"reason":     "sending an urgent customer update requires approval",
			"tool_input": sendInput(),
		})), nil
	case 4:
		return newStream(toolUse("send-1", messagetools.SendToolName, sendInput())), nil
	default:
		return newStream(model.StreamEvent{
			Kind: model.StreamText,
			Text: "Scheduled inbox triage sent the urgent Acme reply and recorded the occurrence so the same hourly trigger does not run twice.",
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
