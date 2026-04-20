package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	memaxagent "github.com/MemaxLabs/memax-go-agent-sdk"
	"github.com/MemaxLabs/memax-go-agent-sdk/model"
	"github.com/MemaxLabs/memax-go-agent-sdk/stack/personal"
	personalsqlitestore "github.com/MemaxLabs/memax-go-agent-sdk/stack/personal/sqlitestore"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/tasktools"
	tasksqlitestore "github.com/MemaxLabs/memax-go-agent-sdk/toolkit/tasktools/sqlitestore"
	_ "modernc.org/sqlite"
)

func main() {
	if err := runExample(context.Background(), os.Stdout); err != nil {
		log.Fatal(err)
	}
}

// runExample walks through one proactive scheduled task-ledger maintenance
// occurrence. The trigger fires from a deterministic schedule, the agent lists
// persisted pending tasks before mutating them, and the scheduled-run store
// deduplicates a second fire for the same occurrence.
func runExample(ctx context.Context, w io.Writer) error {
	now := time.Date(2026, 4, 20, 8, 5, 0, 0, time.UTC)
	stack, runStore, taskStore, trigger, cleanup, err := buildExample(ctx, now)
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

	watchCtx, cancel := context.WithCancel(memaxagent.WithEventObserver(ctx, observer))
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- stack.WatchScheduledTriggers(watchCtx, runStore, personal.TriggerWatcherOptions{
			Interval: time.Millisecond,
			Now: func() time.Time {
				return now
			},
		}, trigger)
	}()

	runID := "task-ledger-maintenance:2026-04-20T08:00:00Z"
	finalRun, err := waitForScheduledRun(runStore, runID, func(record personal.ScheduledRunRecord) bool { return record.Terminal() })
	if err != nil {
		cancel()
		<-errCh
		return err
	}
	intent, due := trigger.IntentAt(now)
	if !due {
		cancel()
		<-errCh
		return fmt.Errorf("periodic trigger did not fire for %s", now.Format(time.RFC3339))
	}
	duplicateRun, created, err := stack.StartScheduledRun(memaxagent.WithEventObserver(ctx, observer), runStore, intent)
	if err != nil {
		cancel()
		<-errCh
		return err
	}

	cancel()
	if err := <-errCh; err != nil && !errors.Is(err, context.Canceled) {
		return err
	}

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

	tasks, err := taskStore.List(ctx)
	if err != nil {
		return err
	}
	for _, task := range tasks {
		fmt.Fprintf(w, "task: %s %s %s\n", task.ID, task.Status, task.Title)
	}
	fmt.Fprintf(w, "scheduled run: %s %s\n", finalRun.ID, finalRun.Status)
	fmt.Fprintf(w, "scheduled session: %s\n", finalRun.SessionID)
	fmt.Fprintf(w, "duplicate fire reused run: %s created=%t\n", duplicateRun.ID, created)
	return nil
}

func buildExample(ctx context.Context, now time.Time) (personal.Stack, personal.ScheduledRunStore, tasktools.Store, personal.PeriodicTrigger, func(), error) {
	runDB, runPath, err := openTempSQLite("memax-personal-scheduled-task-runs")
	if err != nil {
		return personal.Stack{}, nil, nil, personal.PeriodicTrigger{}, nil, err
	}
	taskDB, taskPath, err := openTempSQLite("memax-personal-scheduled-task-ledger")
	if err != nil {
		_ = runDB.Close()
		_ = os.Remove(runPath)
		return personal.Stack{}, nil, nil, personal.PeriodicTrigger{}, nil, err
	}
	cleanup := func() {
		_ = runDB.Close()
		_ = taskDB.Close()
		_ = os.Remove(runPath)
		_ = os.Remove(taskPath)
	}

	runStore, err := personalsqlitestore.New(ctx, runDB)
	if err != nil {
		cleanup()
		return personal.Stack{}, nil, nil, personal.PeriodicTrigger{}, nil, err
	}
	taskStore, err := tasksqlitestore.New(ctx, taskDB)
	if err != nil {
		cleanup()
		return personal.Stack{}, nil, nil, personal.PeriodicTrigger{}, nil, err
	}
	for _, task := range seedTasks() {
		if _, err := taskStore.Upsert(ctx, task); err != nil {
			cleanup()
			return personal.Stack{}, nil, nil, personal.PeriodicTrigger{}, nil, err
		}
	}

	config := personal.PersonalAssistant()
	config.Base.Model = &scheduledTaskLedgerModel{}
	config.Tasks = taskStore

	stack, err := personal.New(config)
	if err != nil {
		cleanup()
		return personal.Stack{}, nil, nil, personal.PeriodicTrigger{}, nil, err
	}
	trigger := personal.PeriodicTrigger{
		Name:   "task-ledger-maintenance",
		Prompt: "Run scheduled task-ledger maintenance for 2026-04-20. List persisted pending tasks first; complete confirmed work, mark blocked work explicitly, and do not create duplicate task IDs.",
		Every:  24 * time.Hour,
		Anchor: time.Date(now.Year(), now.Month(), now.Day(), 8, 0, 0, 0, time.UTC),
	}
	return stack, runStore, taskStore, trigger, cleanup, nil
}

func openTempSQLite(prefix string) (*sql.DB, string, error) {
	file, err := os.CreateTemp("", prefix+"-*.db")
	if err != nil {
		return nil, "", fmt.Errorf("create sqlite temp file: %w", err)
	}
	path := file.Name()
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return nil, "", fmt.Errorf("close sqlite temp file: %w", err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		_ = os.Remove(path)
		return nil, "", fmt.Errorf("open sqlite temp db: %w", err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(context.Background(), "PRAGMA busy_timeout = 5000"); err != nil {
		_ = db.Close()
		_ = os.Remove(path)
		return nil, "", fmt.Errorf("configure sqlite busy timeout: %w", err)
	}
	if _, err := db.ExecContext(context.Background(), "PRAGMA journal_mode = WAL"); err != nil {
		_ = db.Close()
		_ = os.Remove(path)
		return nil, "", fmt.Errorf("configure sqlite WAL mode: %w", err)
	}
	return db, path, nil
}

func seedTasks() []tasktools.Task {
	return []tasktools.Task{
		{
			ID:       "week-2026-04-20-acme-owner",
			Title:    "Confirm Acme mitigation owner",
			Status:   tasktools.StatusPending,
			Notes:    "Owner was confirmed after the customer checkpoint prep.",
			Priority: 1,
			Evidence: []string{"thread-1", "event-1"},
		},
		{
			ID:       "week-2026-04-20-demo-slides",
			Title:    "Deliver partner council demo slides",
			Status:   tasktools.StatusPending,
			Notes:    "Slides still need final product screenshots before partner review.",
			Priority: 2,
			Evidence: []string{"thread-2", "event-2"},
		},
	}
}

type scheduledTaskLedgerModel struct {
	turn int
}

func (m *scheduledTaskLedgerModel) Stream(_ context.Context, req model.Request) (model.Stream, error) {
	m.turn++
	switch m.turn {
	case 1:
		return newStream(toolUse("list-tasks-1", tasktools.ListToolName, map[string]any{
			"status": "pending",
		})), nil
	case 2:
		if !requestHasTaskResult(req, "week-2026-04-20-acme-owner", tasktools.StatusPending) {
			return newStream(model.StreamEvent{
				Kind: model.StreamText,
				Text: "Scheduled task-ledger maintenance found no persisted Acme owner task to update.",
			}), nil
		}
		return newStream(toolUse("complete-task-1", tasktools.UpsertToolName, map[string]any{
			"id":       "week-2026-04-20-acme-owner",
			"status":   "completed",
			"notes":    "Mitigation owner confirmed for the Monday 14:00 UTC customer checkpoint.",
			"evidence": []string{"thread-1", "event-1", "owner-confirmed"},
		})), nil
	case 3:
		if !requestHasToolResult(req, "complete-task-1", "upserted week-2026-04-20-acme-owner") {
			return newStream(model.StreamEvent{
				Kind: model.StreamText,
				Text: "Scheduled task-ledger maintenance could not verify the completed Acme owner update.",
			}), nil
		}
		return newStream(toolUse("block-task-1", tasktools.UpsertToolName, map[string]any{
			"id":       "week-2026-04-20-demo-slides",
			"status":   "blocked",
			"notes":    "Blocked until final product screenshots are available for the partner council package.",
			"evidence": []string{"thread-2", "event-2", "waiting-on-screenshots"},
		})), nil
	default:
		if !requestHasToolResult(req, "block-task-1", "upserted week-2026-04-20-demo-slides") {
			return newStream(model.StreamEvent{
				Kind: model.StreamText,
				Text: "Scheduled task-ledger maintenance could not verify the blocked demo slides update.",
			}), nil
		}
		return newStream(model.StreamEvent{
			Kind: model.StreamText,
			Text: "Scheduled task-ledger maintenance complete: Acme mitigation owner is confirmed; partner council demo slides remain blocked.",
		}), nil
	}
}

func requestHasTaskResult(req model.Request, taskID string, status tasktools.Status) bool {
	needle := "- [" + string(status) + "] " + taskID
	for _, msg := range req.Messages {
		if msg.ToolResult == nil || msg.ToolResult.Name != tasktools.ListToolName {
			continue
		}
		if strings.Contains(msg.ToolResult.Content, needle) {
			return true
		}
	}
	return false
}

func requestHasToolResult(req model.Request, toolUseID string, substrings ...string) bool {
	for _, msg := range req.Messages {
		if msg.ToolResult == nil || msg.ToolResult.ToolUseID != toolUseID {
			continue
		}
		matched := true
		for _, substring := range substrings {
			if !strings.Contains(msg.ToolResult.Content, substring) {
				matched = false
				break
			}
		}
		if matched {
			return true
		}
	}
	return false
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
