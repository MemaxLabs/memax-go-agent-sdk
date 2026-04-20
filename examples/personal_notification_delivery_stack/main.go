package main

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"log"
	"os"
	"sort"
	"sync"
	"time"

	memaxagent "github.com/MemaxLabs/memax-go-agent-sdk"
	"github.com/MemaxLabs/memax-go-agent-sdk/model"
	"github.com/MemaxLabs/memax-go-agent-sdk/stack/personal"
	personalsqlitestore "github.com/MemaxLabs/memax-go-agent-sdk/stack/personal/sqlitestore"
	"github.com/MemaxLabs/memax-go-agent-sdk/telemetry"
	_ "modernc.org/sqlite"
)

const notificationWorkerID = "push-worker-1"

func main() {
	if err := runExample(context.Background(), os.Stdout); err != nil {
		log.Fatal(err)
	}
}

// runExample walks through a host-owned scheduled-run notification delivery
// worker. The SDK persists notification outbox, claim, retry, and ack state;
// the actual delivery channel remains ordinary host code.
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
	meter := &exampleMetricMeter{}
	metrics := personal.NewScheduledRunNotificationMetrics(meter)

	runCtx := memaxagent.WithEventObserver(ctx, capture)
	runCtx = memaxagent.WithEventObserver(runCtx, notifier)
	runCtx = memaxagent.WithEventObserver(runCtx, metrics)
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

	channel := &flakyNotificationChannel{}
	deliveryCtx := memaxagent.WithEventObserver(ctx, metrics)
	// Scheduled occurrences use deterministic timestamps; delivery uses the
	// outbox record's own durable clock because notifications are created from
	// accepted lifecycle transitions.
	deliveryNow := notification.DeliverAfter
	firstResult, err := personal.DrainScheduledRunNotifications(
		deliveryCtx,
		store,
		notificationWorkerID,
		personal.ScheduledRunNotificationDeliveryHandlerFunc(func(_ context.Context, record personal.ScheduledRunNotificationRecord) error {
			return channel.Deliver(record)
		}),
		personal.WithScheduledRunNotificationDrainNow(deliveryNow),
		personal.WithScheduledRunNotificationDrainLeaseDuration(time.Minute),
		personal.WithScheduledRunNotificationRetryBackoff(func(personal.ScheduledRunNotificationRecord, error, time.Time) time.Time {
			return deliveryNow.Add(2 * time.Minute)
		}),
	)
	if err != nil {
		return err
	}
	if len(firstResult.Claimed) != 1 || len(firstResult.Failed) != 1 {
		return fmt.Errorf("first drain = %#v, want one claimed retryable failure", firstResult)
	}
	firstClaim := firstResult.Claimed[0]
	firstFailure := firstResult.Failed[0].Record
	fmt.Fprintf(w, "claim 1: %s attempts=%d status=%s\n", firstClaim.ID, firstClaim.DeliveryAttempts, firstClaim.DeliveryStatus)
	fmt.Fprintf(w, "delivery failed: %s retry_after=2m status=%s\n", firstFailure.DeliveryError, firstFailure.DeliveryStatus)

	// This direct empty-claim probe intentionally skips deliveryCtx because no
	// notification delivery transition should be emitted.
	notYet, err := store.ClaimScheduledRunNotifications(ctx, personal.ClaimScheduledRunNotificationsRequest{
		WorkerID:      notificationWorkerID,
		Limit:         1,
		Now:           deliveryNow.Add(time.Minute),
		LeaseDuration: time.Minute,
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "claim before retry: %d\n", len(notYet))

	secondResult, err := personal.DrainScheduledRunNotifications(
		deliveryCtx,
		store,
		notificationWorkerID,
		personal.ScheduledRunNotificationDeliveryHandlerFunc(func(_ context.Context, record personal.ScheduledRunNotificationRecord) error {
			return channel.Deliver(record)
		}),
		personal.WithScheduledRunNotificationDrainNow(deliveryNow.Add(2*time.Minute)),
		personal.WithScheduledRunNotificationDrainLeaseDuration(time.Minute),
	)
	if err != nil {
		return err
	}
	if len(secondResult.Claimed) != 1 || len(secondResult.Delivered) != 1 || len(secondResult.Failed) != 0 {
		return fmt.Errorf("second drain = %#v, want one delivered retry", secondResult)
	}
	secondClaim := secondResult.Claimed[0]
	delivered := secondResult.Delivered[0]
	fmt.Fprintf(w, "claim 2: %s attempts=%d status=%s\n", secondClaim.ID, secondClaim.DeliveryAttempts, secondClaim.DeliveryStatus)
	fmt.Fprintf(w, "delivery sent to host channel: %s\n", channel.Last())
	fmt.Fprintf(w, "delivered: %s status=%s attempts=%d\n", delivered.ID, delivered.DeliveryStatus, delivered.DeliveryAttempts)

	deliveredNotifications, err := store.ListScheduledRunNotifications(ctx, personal.ScheduledRunNotificationFilter{
		DeliveryStatus: personal.ScheduledRunNotificationDeliveryDelivered,
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "final delivered notifications: %d\n", len(deliveredNotifications))
	stats, err := personal.GetScheduledRunNotificationStats(ctx, store, deliveryNow.Add(2*time.Minute))
	if err != nil {
		return err
	}
	personal.RecordScheduledRunNotificationStats(ctx, meter, stats, telemetry.String("example", "notification_delivery"))
	for _, line := range meter.Lines() {
		fmt.Fprintf(w, "%s\n", line)
	}
	return nil
}

func buildExample(ctx context.Context, now time.Time) (personal.Stack, *personalsqlitestore.Store, personal.PeriodicTrigger, func(), error) {
	db, path, err := openTempSQLite("memax-personal-notification-delivery")
	if err != nil {
		return personal.Stack{}, nil, personal.PeriodicTrigger{}, nil, err
	}
	cleanup := func() {
		_ = db.Close()
		_ = os.Remove(path)
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
		Name:   "delivery-check",
		Prompt: "Finish the scheduled delivery check and summarize what changed.",
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
		time.Sleep(time.Millisecond)
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
		time.Sleep(time.Millisecond)
	}
	record, err := store.GetScheduledRun(context.Background(), id)
	if err != nil {
		return personal.ScheduledRunRecord{}, err
	}
	return personal.ScheduledRunRecord{}, fmt.Errorf("scheduled run %q did not finish: %#v", id, record)
}

type notificationModel struct{}

func (m *notificationModel) Stream(context.Context, model.Request) (model.Stream, error) {
	return newStream(model.StreamEvent{
		Kind: model.StreamText,
		Text: "Scheduled delivery check complete: the owner update is ready for notification.",
	}), nil
}

type flakyNotificationChannel struct {
	attempts int
	sent     []string
}

func (c *flakyNotificationChannel) Deliver(record personal.ScheduledRunNotificationRecord) error {
	c.attempts++
	if c.attempts == 1 {
		return fmt.Errorf("push gateway unavailable")
	}
	message := fmt.Sprintf("%s -> %s", record.TriggerName, record.Result)
	c.sent = append(c.sent, message)
	return nil
}

func (c *flakyNotificationChannel) Last() string {
	if len(c.sent) == 0 {
		return ""
	}
	return c.sent[len(c.sent)-1]
}

type exampleMetric struct {
	kind  string
	name  string
	value string
	attrs []telemetry.Attribute
	seq   int
}

// exampleMetricMeter records raw metric deltas and measurements so the example
// can print exactly what a host-owned telemetry adapter receives.
type exampleMetricMeter struct {
	mu      sync.Mutex
	metrics []exampleMetric
}

func (m *exampleMetricMeter) Add(_ context.Context, name string, value int64, attrs ...telemetry.Attribute) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.metrics = append(m.metrics, exampleMetric{
		kind:  "counter",
		name:  name,
		value: fmt.Sprintf("%d", value),
		attrs: append([]telemetry.Attribute(nil), attrs...),
		seq:   len(m.metrics),
	})
}

func (m *exampleMetricMeter) Record(_ context.Context, name string, value float64, attrs ...telemetry.Attribute) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.metrics = append(m.metrics, exampleMetric{
		kind:  "record",
		name:  name,
		value: formatMetricValue(value),
		attrs: append([]telemetry.Attribute(nil), attrs...),
		seq:   len(m.metrics),
	})
}

func (m *exampleMetricMeter) Lines() []string {
	m.mu.Lock()
	metrics := append([]exampleMetric(nil), m.metrics...)
	m.mu.Unlock()

	sort.SliceStable(metrics, func(i, j int) bool {
		if metrics[i].name != metrics[j].name {
			return metrics[i].name < metrics[j].name
		}
		iAttrs := formatMetricAttrs(metrics[i].attrs)
		jAttrs := formatMetricAttrs(metrics[j].attrs)
		if iAttrs != jAttrs {
			return iAttrs < jAttrs
		}
		return metrics[i].seq < metrics[j].seq
	})
	lines := make([]string, 0, len(metrics))
	for _, metric := range metrics {
		lines = append(lines, fmt.Sprintf("metric %s: %s=%s%s", metric.kind, metric.name, metric.value, formatMetricAttrs(metric.attrs)))
	}
	return lines
}

func formatMetricValue(value float64) string {
	if value == float64(int64(value)) {
		return fmt.Sprintf("%.0f", value)
	}
	return fmt.Sprintf("%.3f", value)
}

func formatMetricAttrs(attrs []telemetry.Attribute) string {
	if len(attrs) == 0 {
		return ""
	}
	copied := append([]telemetry.Attribute(nil), attrs...)
	sort.Slice(copied, func(i, j int) bool {
		return copied[i].Key < copied[j].Key
	})
	out := ""
	for _, attr := range copied {
		out += fmt.Sprintf(" %s=%v", attr.Key, attr.Value)
	}
	return out
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
