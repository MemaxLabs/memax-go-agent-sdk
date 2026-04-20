package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"time"

	memaxagent "github.com/MemaxLabs/memax-go-agent-sdk"
	"github.com/MemaxLabs/memax-go-agent-sdk/model"
	"github.com/MemaxLabs/memax-go-agent-sdk/stack/personal"
	personalsqlitestore "github.com/MemaxLabs/memax-go-agent-sdk/stack/personal/sqlitestore"
	"github.com/MemaxLabs/memax-go-agent-sdk/stack/personal/webhook"
	_ "modernc.org/sqlite"
)

const notificationWorkerID = "webhook-worker-1"

func main() {
	if err := runExample(context.Background(), os.Stdout); err != nil {
		log.Fatal(err)
	}
}

// runExample demonstrates the host-owned webhook delivery path for scheduled
// personal notifications. The example uses an in-process httptest endpoint so
// `go run ./examples/personal_webhook_notification_stack` works without any
// external service. Production hosts pass their real webhook URL and signing
// secret to webhook.New and keep the same claim/ack worker shape.
func runExample(ctx context.Context, w io.Writer) error {
	now := time.Date(2026, 4, 20, 9, 5, 0, 0, time.UTC)
	stack, store, trigger, cleanup, err := buildExample(ctx, now)
	if err != nil {
		return err
	}
	defer cleanup()

	notifier, err := personal.NewScheduledRunNotifier(store)
	if err != nil {
		return err
	}
	var (
		mu     sync.Mutex
		events []memaxagent.Event
	)
	capture := memaxagent.EventObserverFunc(func(_ context.Context, event memaxagent.Event) {
		mu.Lock()
		events = append(events, event)
		mu.Unlock()
	})

	runCtx := memaxagent.WithEventObserver(memaxagent.WithEventObserver(ctx, capture), notifier)
	results, err := stack.FireScheduledTriggers(runCtx, store, now, trigger)
	if err != nil {
		return err
	}
	if len(results) != 1 || !results[0].Created {
		return fmt.Errorf("scheduled trigger fire = %#v, want one created notification run", results)
	}
	runID := results[0].Record.ID
	finalRun, err := waitForScheduledRun(store, runID, func(record personal.ScheduledRunRecord) bool {
		return record.Terminal()
	})
	if err != nil {
		return err
	}

	notifications, err := waitForNotifications(store, runID, personal.ScheduledRunNotificationDeliveryPending, 1)
	if err != nil {
		return err
	}
	notification := notifications[0]

	mu.Lock()
	captured := append([]memaxagent.Event(nil), events...)
	mu.Unlock()
	for _, event := range captured {
		if event.Kind == memaxagent.EventRunStateChanged && event.Run != nil {
			fmt.Fprintf(w, "scheduled lifecycle: %s %s\n", event.Run.RunID, event.Run.Status)
		}
		if event.Kind == memaxagent.EventError {
			return event.Err
		}
	}
	fmt.Fprintf(w, "scheduled run: %s %s\n", finalRun.ID, finalRun.Status)
	fmt.Fprintf(w, "notification recorded: %s run=%s delivery=%s result=%q\n", notification.ID, notification.RunID, notification.DeliveryStatus, notification.Result)

	secret := []byte("example-webhook-secret")
	receiver := &webhookReceiver{secret: secret}
	server := httptest.NewServer(receiver)
	defer server.Close()

	deliveryNow := notification.DeliverAfter
	handler, err := webhook.New(
		server.URL,
		webhook.WithHTTPClient(server.Client()),
		webhook.WithHMACSecret(secret),
		webhook.WithClock(func() time.Time { return deliveryNow }),
		webhook.WithHeader("X-Host-Route", "personal-notifications"),
	)
	if err != nil {
		return err
	}
	result, err := personal.DrainScheduledRunNotifications(
		ctx,
		store,
		notificationWorkerID,
		handler,
		personal.WithScheduledRunNotificationDrainNow(deliveryNow),
		personal.WithScheduledRunNotificationDrainLeaseDuration(time.Minute),
	)
	if err != nil {
		return err
	}
	if len(result.Claimed) != 1 || len(result.Delivered) != 1 || len(result.Failed) != 0 {
		return fmt.Errorf("webhook drain = %#v, want one delivered notification", result)
	}
	receipt, err := receiver.Single()
	if err != nil {
		return err
	}
	if !receipt.SignatureOK {
		return fmt.Errorf("webhook signature verification failed")
	}
	delivered := result.Delivered[0]
	fmt.Fprintf(w, "webhook received: method=%s idempotency=%s route=%s signature=%t event=%s\n", receipt.Method, receipt.IdempotencyKey, receipt.Route, receipt.SignatureOK, receipt.Payload.Type)
	fmt.Fprintf(w, "webhook payload: run=%s status=%s result=%q\n", receipt.Payload.Data.RunID, receipt.Payload.Data.Status, receipt.Payload.Data.Result)
	fmt.Fprintf(w, "delivered: %s status=%s attempts=%d\n", delivered.ID, delivered.DeliveryStatus, delivered.DeliveryAttempts)

	deliveredNotifications, err := store.ListScheduledRunNotifications(ctx, personal.ScheduledRunNotificationFilter{
		DeliveryStatus: personal.ScheduledRunNotificationDeliveryDelivered,
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "final delivered notifications: %d\n", len(deliveredNotifications))
	return nil
}

func buildExample(ctx context.Context, now time.Time) (personal.Stack, *personalsqlitestore.Store, personal.PeriodicTrigger, func(), error) {
	db, path, err := openTempSQLite("memax-personal-webhook-notification")
	if err != nil {
		return personal.Stack{}, nil, personal.PeriodicTrigger{}, nil, err
	}
	cleanup := func() {
		_ = db.Close()
		removeSQLiteFiles(path)
	}
	store, err := personalsqlitestore.New(ctx, db)
	if err != nil {
		cleanup()
		return personal.Stack{}, nil, personal.PeriodicTrigger{}, nil, err
	}

	config := personal.PersonalAssistant()
	config.Base.Model = &notificationModel{}
	stack, err := personal.New(config)
	if err != nil {
		cleanup()
		return personal.Stack{}, nil, personal.PeriodicTrigger{}, nil, err
	}
	trigger := personal.PeriodicTrigger{
		Name:   "webhook-check",
		Prompt: "Finish the scheduled webhook check and summarize what changed.",
		Every:  24 * time.Hour,
		Anchor: time.Date(now.Year(), now.Month(), now.Day(), 9, 0, 0, 0, time.UTC),
	}
	return stack, store, trigger, cleanup, nil
}

func openTempSQLite(prefix string) (*sql.DB, string, error) {
	file, err := os.CreateTemp("", prefix+"-*.db")
	if err != nil {
		return nil, "", fmt.Errorf("create sqlite temp file: %w", err)
	}
	path := file.Name()
	if err := file.Close(); err != nil {
		removeSQLiteFiles(path)
		return nil, "", fmt.Errorf("close sqlite temp file: %w", err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		removeSQLiteFiles(path)
		return nil, "", fmt.Errorf("open sqlite temp db: %w", err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(context.Background(), "PRAGMA busy_timeout = 5000"); err != nil {
		_ = db.Close()
		removeSQLiteFiles(path)
		return nil, "", fmt.Errorf("configure sqlite busy timeout: %w", err)
	}
	if _, err := db.ExecContext(context.Background(), "PRAGMA journal_mode = WAL"); err != nil {
		_ = db.Close()
		removeSQLiteFiles(path)
		return nil, "", fmt.Errorf("configure sqlite WAL mode: %w", err)
	}
	return db, path, nil
}

func removeSQLiteFiles(path string) {
	_ = os.Remove(path)
	_ = os.Remove(path + "-wal")
	_ = os.Remove(path + "-shm")
}

func waitForNotifications(store personal.ScheduledRunNotificationStore, runID string, status personal.ScheduledRunNotificationDeliveryStatus, count int) ([]personal.ScheduledRunNotificationRecord, error) {
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		notifications, err := store.ListScheduledRunNotifications(context.Background(), personal.ScheduledRunNotificationFilter{
			RunID:          runID,
			DeliveryStatus: status,
		})
		if err == nil && len(notifications) >= count {
			return notifications, nil
		}
		time.Sleep(5 * time.Millisecond)
	}
	notifications, err := store.ListScheduledRunNotifications(context.Background(), personal.ScheduledRunNotificationFilter{
		RunID:          runID,
		DeliveryStatus: status,
	})
	if err != nil {
		return nil, err
	}
	return nil, fmt.Errorf("notifications for %q with delivery status %s = %d, want at least %d", runID, status, len(notifications), count)
}

func waitForScheduledRun(store personal.ScheduledRunStore, id string, done func(personal.ScheduledRunRecord) bool) (personal.ScheduledRunRecord, error) {
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		record, err := store.GetScheduledRun(context.Background(), id)
		if err == nil && done(record) {
			return record, nil
		}
		time.Sleep(5 * time.Millisecond)
	}
	record, err := store.GetScheduledRun(context.Background(), id)
	if err != nil {
		return personal.ScheduledRunRecord{}, err
	}
	return personal.ScheduledRunRecord{}, fmt.Errorf("scheduled run %q did not finish: %#v", id, record)
}

type webhookReceipt struct {
	Method         string
	IdempotencyKey string
	Route          string
	SignatureOK    bool
	Payload        webhook.Payload
}

type webhookReceiver struct {
	secret   []byte
	mu       sync.Mutex
	receipts []webhookReceipt
}

func (r *webhookReceiver) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	body, err := io.ReadAll(req.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var payload webhook.Payload
	if err := json.Unmarshal(body, &payload); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	signature := req.Header.Get("webhook-signature")
	expectedSignature := standardSignature(
		r.secret,
		req.Header.Get("webhook-id"),
		req.Header.Get("webhook-timestamp"),
		string(body),
	)
	signatureOK := hmac.Equal([]byte(signature), []byte(expectedSignature))
	r.mu.Lock()
	r.receipts = append(r.receipts, webhookReceipt{
		Method:         req.Method,
		IdempotencyKey: req.Header.Get("Idempotency-Key"),
		Route:          req.Header.Get("X-Host-Route"),
		SignatureOK:    signatureOK,
		Payload:        payload,
	})
	r.mu.Unlock()
	w.WriteHeader(http.StatusAccepted)
}

func (r *webhookReceiver) Single() (webhookReceipt, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.receipts) != 1 {
		return webhookReceipt{}, fmt.Errorf("webhook receipts = %d, want one", len(r.receipts))
	}
	return r.receipts[0], nil
}

func standardSignature(secret []byte, id, timestamp, body string) string {
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte(id + "." + timestamp + "." + body))
	return "v1," + base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

type notificationModel struct{}

func (m *notificationModel) Stream(context.Context, model.Request) (model.Stream, error) {
	return newStream(model.StreamEvent{
		Kind: model.StreamText,
		Text: "Scheduled webhook notice complete: the owner update is ready for signed delivery.",
	}), nil
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
