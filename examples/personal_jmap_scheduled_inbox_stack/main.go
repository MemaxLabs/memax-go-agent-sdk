package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"time"

	memaxagent "github.com/MemaxLabs/memax-go-agent-sdk"
	"github.com/MemaxLabs/memax-go-agent-sdk/messaging/jmapclient"
	"github.com/MemaxLabs/memax-go-agent-sdk/messaging/jmapstore"
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

// runExample walks through one host-owned scheduled inbox triage trigger over
// the real JMAP adapter seam. The example uses an in-process httptest JMAP
// fixture so `go run ./examples/personal_jmap_scheduled_inbox_stack` works
// without OAuth or a remote inbox. Production deployments should pass a real
// JMAP base URL and bearer token to jmapclient.New instead.
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

	watchCtx, cancel := context.WithCancel(memaxagent.WithEventObserver(ctx, observer))
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- stack.WatchScheduledTriggers(watchCtx, store, personal.TriggerWatcherOptions{
			Interval: time.Millisecond,
			Now: func() time.Time {
				return now
			},
		}, trigger)
	}()

	runID := "inbox-triage:2026-04-19T09:00:00Z"
	finalRun, err := waitForScheduledRun(store, runID, func(record personal.ScheduledRunRecord) bool { return record.Terminal() })
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
	duplicateRun, created, err := stack.StartScheduledRun(memaxagent.WithEventObserver(ctx, observer), store, intent)
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
	var (
		serverMu      sync.Mutex
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

	client, err := jmapclient.New(server.URL, "acc")
	if err != nil {
		server.Close()
		return personal.Stack{}, nil, personal.PeriodicTrigger{}, nil, err
	}
	messageStore, err := jmapstore.New(client,
		jmapstore.WithDefaultIdentity("identity-1"),
		jmapstore.WithDefaultSender("Memax", "me@example.com"),
		jmapstore.WithDraftMailbox("drafts"),
	)
	if err != nil {
		server.Close()
		return personal.Stack{}, nil, personal.PeriodicTrigger{}, nil, err
	}
	tasks := tasktools.NewMemoryStore([]tasktools.Task{{
		ID:     "task-1",
		Title:  "Triage unread inbox threads",
		Status: tasktools.StatusInProgress,
		Notes:  "run the hourly unread inbox triage proactively, classify from metadata first, then read the selected thread before drafting the approved reply over the attached JMAP inbox backend",
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
	config.Base.Model = &scheduledJMAPInboxModel{}

	stack, err := personal.New(config)
	if err != nil {
		server.Close()
		return personal.Stack{}, nil, personal.PeriodicTrigger{}, nil, err
	}

	db, err := sql.Open("sqlite", "file:personal-scheduled-jmap-inbox?mode=memory&cache=shared")
	if err != nil {
		server.Close()
		return personal.Stack{}, nil, personal.PeriodicTrigger{}, nil, err
	}
	store, err := personalsqlitestore.New(context.Background(), db)
	if err != nil {
		server.Close()
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
		server.Close()
		_ = db.Close()
	}
	return stack, store, trigger, cleanup, nil
}

type scheduledJMAPInboxModel struct {
	turn int
}

func (m *scheduledJMAPInboxModel) Stream(_ context.Context, _ model.Request) (model.Stream, error) {
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
			Text: "Scheduled inbox triage sent the urgent Acme reply through the attached JMAP inbox backend and recorded the occurrence so the same hourly trigger does not run twice.",
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
