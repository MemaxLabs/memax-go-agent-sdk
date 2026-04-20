package personal

import (
	"context"

	memaxagent "github.com/MemaxLabs/memax-go-agent-sdk"
	"github.com/MemaxLabs/memax-go-agent-sdk/telemetry"
)

const (
	metricScheduledRunNotificationDeliveryEvents   = "memax.personal.notification.delivery.events"
	metricScheduledRunNotificationDeliveryAttempts = "memax.personal.notification.delivery.attempts"
	metricScheduledRunNotificationOutboxTotal      = "memax.personal.notification.outbox.total"
	metricScheduledRunNotificationOutboxRecords    = "memax.personal.notification.outbox.records"
	metricScheduledRunNotificationOutboxClaimable  = "memax.personal.notification.outbox.claimable"
	metricScheduledRunNotificationOutboxLeased     = "memax.personal.notification.outbox.leased"
	metricScheduledRunNotificationOutboxAttempts   = "memax.personal.notification.outbox.delivery_attempts"
	metricScheduledRunNotificationOldestAgeMS      = "memax.personal.notification.outbox.oldest_undelivered_age_ms"
)

// ScheduledRunNotificationMetrics observes scheduled-run notification delivery
// events and records aggregate counters through a host-owned telemetry meter.
// It is intentionally independent from notification stores: events provide
// ordered history, while RecordScheduledRunNotificationStats records current
// outbox health snapshots when hosts choose to poll them.
type ScheduledRunNotificationMetrics struct {
	meter telemetry.Meter
}

// NewScheduledRunNotificationMetrics returns an event observer that records
// scheduled-run notification delivery lifecycle metrics. A nil meter uses the
// no-op meter. Attach the returned observer with memaxagent.WithEventObserver
// to record delivery events from the root event stream.
func NewScheduledRunNotificationMetrics(meter telemetry.Meter) ScheduledRunNotificationMetrics {
	if meter == nil {
		meter = telemetry.NoopMeter{}
	}
	return ScheduledRunNotificationMetrics{meter: meter}
}

// ObserveEvent implements memaxagent.EventObserver.
func (m ScheduledRunNotificationMetrics) ObserveEvent(ctx context.Context, event memaxagent.Event) {
	RecordScheduledRunNotificationDeliveryMetrics(ctx, m.meter, event)
}

// RecordScheduledRunNotificationDeliveryMetrics records metrics for one
// scheduled-run notification delivery event. Non-notification events are
// ignored, so callers can attach the returned observer to the general Memax
// event stream. The delivery-attempts measurement records the current attempt
// count on every delivery event kind; consumers can split it by event_kind when
// they need claim-time and terminal-attempt distributions separately.
func RecordScheduledRunNotificationDeliveryMetrics(ctx context.Context, meter telemetry.Meter, event memaxagent.Event) {
	if event.Notification == nil || !isScheduledRunNotificationDeliveryEvent(event.Kind) {
		return
	}
	if meter == nil {
		meter = telemetry.NoopMeter{}
	}
	attrs := scheduledRunNotificationMetricAttrs(event)
	meter.Add(ctx, metricScheduledRunNotificationDeliveryEvents, 1, attrs...)
	if event.Notification.Attempts > 0 {
		meter.Record(ctx, metricScheduledRunNotificationDeliveryAttempts, float64(event.Notification.Attempts), attrs...)
	}
}

// RecordScheduledRunNotificationStats records current outbox health as
// gauge-like value measurements. Hosts typically call this after
// GetScheduledRunNotificationStats in a periodic monitor loop.
func RecordScheduledRunNotificationStats(ctx context.Context, meter telemetry.Meter, stats ScheduledRunNotificationStats, attrs ...telemetry.Attribute) {
	if meter == nil {
		meter = telemetry.NoopMeter{}
	}
	meter.Record(ctx, metricScheduledRunNotificationOutboxTotal, float64(stats.TotalCount), attrs...)
	recordNotificationStatusCount(ctx, meter, stats.PendingCount, ScheduledRunNotificationDeliveryPending, attrs...)
	recordNotificationStatusCount(ctx, meter, stats.DeliveringCount, ScheduledRunNotificationDeliveryDelivering, attrs...)
	recordNotificationStatusCount(ctx, meter, stats.DeliveredCount, ScheduledRunNotificationDeliveryDelivered, attrs...)
	recordNotificationStatusCount(ctx, meter, stats.FailedCount, ScheduledRunNotificationDeliveryFailed, attrs...)
	recordNotificationStatusCount(ctx, meter, stats.DeadLetteredCount, ScheduledRunNotificationDeliveryDeadLettered, attrs...)
	meter.Record(ctx, metricScheduledRunNotificationOutboxClaimable, float64(stats.ClaimableCount), attrs...)
	meter.Record(ctx, metricScheduledRunNotificationOutboxLeased, float64(stats.LeasedCount), attrs...)
	meter.Record(ctx, metricScheduledRunNotificationOutboxAttempts, float64(stats.DeliveryAttemptsTotal), attrs...)
	oldestAgeMS := stats.OldestUndeliveredAge.Milliseconds()
	if oldestAgeMS < 0 {
		oldestAgeMS = 0
	}
	meter.Record(ctx, metricScheduledRunNotificationOldestAgeMS, float64(oldestAgeMS), attrs...)
}

func recordNotificationStatusCount(ctx context.Context, meter telemetry.Meter, count int, status ScheduledRunNotificationDeliveryStatus, attrs ...telemetry.Attribute) {
	statusAttrs := make([]telemetry.Attribute, 0, len(attrs)+1)
	statusAttrs = append(statusAttrs, attrs...)
	statusAttrs = append(statusAttrs, telemetry.String("delivery_status", string(status)))
	meter.Record(ctx, metricScheduledRunNotificationOutboxRecords, float64(count), statusAttrs...)
}

func scheduledRunNotificationMetricAttrs(event memaxagent.Event) []telemetry.Attribute {
	notification := event.Notification
	attrs := []telemetry.Attribute{
		telemetry.String("event_kind", string(event.Kind)),
		telemetry.String("scheduled_run_status", notification.Status),
		telemetry.String("delivery_status", notification.DeliveryStatus),
	}
	if notification.TriggerName != "" {
		attrs = append(attrs, telemetry.String("trigger_name", notification.TriggerName))
	}
	return attrs
}

func isScheduledRunNotificationDeliveryEvent(kind memaxagent.EventKind) bool {
	switch kind {
	case memaxagent.EventScheduledRunNotificationClaimed,
		memaxagent.EventScheduledRunNotificationDelivered,
		memaxagent.EventScheduledRunNotificationFailed,
		memaxagent.EventScheduledRunNotificationDeadLettered,
		memaxagent.EventScheduledRunNotificationRequeued:
		return true
	default:
		return false
	}
}
