package cloudmanaged

import (
	"context"
	"strings"
	"time"

	memaxagent "github.com/MemaxLabs/memax-go-agent-sdk"
	"github.com/MemaxLabs/memax-go-agent-sdk/telemetry"
)

const (
	metricCloudManagedRunLifecycleEvents = "memax.cloudmanaged.run.lifecycle.events"
	metricCloudManagedRunQueueLatencyMS  = "memax.cloudmanaged.run.queue_latency_ms"
	metricCloudManagedRunDurationMS      = "memax.cloudmanaged.run.duration_ms"
	metricCloudManagedRunTotalDurationMS = "memax.cloudmanaged.run.total_duration_ms"
	metricCloudManagedTenantDenials      = "memax.cloudmanaged.tenant.denials"
	metricCloudManagedWorkerClaims       = "memax.cloudmanaged.worker.claims"
	metricCloudManagedWorkerHeartbeats   = "memax.cloudmanaged.worker.heartbeats"
	metricCloudManagedWorkerHeartbeatErr = "memax.cloudmanaged.worker.heartbeat_errors"
	metricCloudManagedWorkerStaleFailed  = "memax.cloudmanaged.worker.stale_failures"
)

// MetricsObserver records cloud-managed lifecycle and denial metrics from a
// Memax event stream. Stack methods record worker-side claim, heartbeat, and
// stale-failure metrics directly because those operations may not emit
// transcript-visible events. The emitted metric attributes are intentionally
// low-cardinality; use audit events when you need tenant, run, or worker IDs.
type MetricsObserver struct {
	meter telemetry.Meter
}

// NewMetricsObserver returns an event observer for cloud-managed runtime
// metrics. A nil meter uses the no-op meter. Attach the returned observer with
// memaxagent.WithEventObserver when hosts want event-derived metrics outside
// the assembled Stack helpers.
func NewMetricsObserver(meter telemetry.Meter) MetricsObserver {
	return MetricsObserver{meter: normalizeMeter(meter)}
}

// ObserveEvent implements memaxagent.EventObserver.
func (m MetricsObserver) ObserveEvent(ctx context.Context, event memaxagent.Event) {
	RecordEventMetrics(ctx, m.meter, event)
}

// RecordEventMetrics records metrics derived from cloud-managed event payloads.
// Non-cloudmanaged events are ignored. Tenant-denial events are stack-neutral,
// so hosts that attach this observer outside cloudmanaged will still get the
// cloudmanaged tenant-denial counter for compatible denial events.
func RecordEventMetrics(ctx context.Context, meter telemetry.Meter, event memaxagent.Event) {
	meter = normalizeMeter(meter)
	switch event.Kind {
	case memaxagent.EventTenantDenied:
		recordTenantDeniedMetrics(ctx, meter, event)
	case memaxagent.EventRunStateChanged:
		recordRunEventMetrics(ctx, meter, event)
	}
}

// RecordRunStateMetrics records metrics for one durable managed-run snapshot.
func RecordRunStateMetrics(ctx context.Context, meter telemetry.Meter, record RunRecord, attrs ...telemetry.Attribute) {
	if record.ID == "" || record.Status == "" {
		return
	}
	meter = normalizeMeter(meter)
	runAttrs := appendRunMetricAttrs(nil, record)
	runAttrs = append(runAttrs, attrs...)
	meter.Add(ctx, metricCloudManagedRunLifecycleEvents, 1, runAttrs...)
	if record.Status == RunStatusRunning && !record.CreatedAt.IsZero() && !record.StartedAt.IsZero() {
		meter.Record(ctx, metricCloudManagedRunQueueLatencyMS, durationMilliseconds(record.StartedAt.Sub(record.CreatedAt)), runAttrs...)
	}
	if record.Terminal() && !record.StartedAt.IsZero() && !record.CompletedAt.IsZero() {
		meter.Record(ctx, metricCloudManagedRunDurationMS, durationMilliseconds(record.CompletedAt.Sub(record.StartedAt)), runAttrs...)
	}
	if record.Terminal() && !record.CreatedAt.IsZero() && !record.CompletedAt.IsZero() {
		meter.Record(ctx, metricCloudManagedRunTotalDurationMS, durationMilliseconds(record.CompletedAt.Sub(record.CreatedAt)), runAttrs...)
	}
}

func recordRunEventMetrics(ctx context.Context, meter telemetry.Meter, event memaxagent.Event) {
	if event.Run == nil || event.Run.RunID == "" || event.Run.Status == "" {
		return
	}
	attrs := []telemetry.Attribute{
		telemetry.String("run_status", event.Run.Status),
		telemetry.Bool("run_terminal", runStatusTerminal(event.Run.Status)),
	}
	if event.Run.Error != "" {
		attrs = append(attrs, telemetry.String("failure_kind", classifyRunFailure(event.Run.Error)))
	}
	meter.Add(ctx, metricCloudManagedRunLifecycleEvents, 1, attrs...)
}

func recordTenantDeniedMetrics(ctx context.Context, meter telemetry.Meter, event memaxagent.Event) {
	if event.Tenant == nil {
		return
	}
	attrs := []telemetry.Attribute{
		telemetry.String("tenant_boundary", event.Tenant.Boundary),
	}
	meter.Add(ctx, metricCloudManagedTenantDenials, 1, attrs...)
}

func recordWorkerClaimMetrics(ctx context.Context, meter telemetry.Meter) {
	meter = normalizeMeter(meter)
	meter.Add(ctx, metricCloudManagedWorkerClaims, 1)
}

func recordWorkerHeartbeatMetrics(ctx context.Context, meter telemetry.Meter) {
	meter = normalizeMeter(meter)
	meter.Add(ctx, metricCloudManagedWorkerHeartbeats, 1)
}

func recordWorkerHeartbeatErrorMetrics(ctx context.Context, meter telemetry.Meter, err error) {
	meter = normalizeMeter(meter)
	attrs := []telemetry.Attribute{telemetry.String("error_kind", classifyError(err))}
	meter.Add(ctx, metricCloudManagedWorkerHeartbeatErr, 1, attrs...)
}

func recordWorkerStaleFailureMetrics(ctx context.Context, meter telemetry.Meter, count int64, reason string) {
	if count <= 0 {
		return
	}
	meter = normalizeMeter(meter)
	meter.Add(ctx, metricCloudManagedWorkerStaleFailed, count,
		telemetry.String("failure_kind", classifyRunFailure(reason)),
	)
}

func appendRunMetricAttrs(attrs []telemetry.Attribute, record RunRecord) []telemetry.Attribute {
	attrs = append(attrs,
		telemetry.String("run_status", string(record.Status)),
		telemetry.Bool("run_terminal", record.Terminal()),
	)
	if record.Error != "" {
		attrs = append(attrs, telemetry.String("failure_kind", classifyRunFailure(record.Error)))
	}
	return attrs
}

func runStatusTerminal(status string) bool {
	switch RunStatus(status) {
	case RunStatusSucceeded, RunStatusFailed, RunStatusCanceled:
		return true
	default:
		return false
	}
}

func classifyRunFailure(reason string) string {
	reason = strings.ToLower(reason)
	switch {
	case reason == "":
		return "none"
	case reason == staleRunFailureReason:
		return "heartbeat_timeout"
	case strings.Contains(reason, "tenant denied"), strings.Contains(reason, "tenant quota exceeded"):
		return "tenant_denied"
	case strings.Contains(reason, context.Canceled.Error()):
		return "canceled"
	case strings.Contains(reason, context.DeadlineExceeded.Error()):
		return "deadline_exceeded"
	default:
		return "error"
	}
}

func classifyError(err error) string {
	if err == nil {
		return "none"
	}
	return classifyRunFailure(err.Error())
}

func durationMilliseconds(duration time.Duration) float64 {
	if duration < 0 {
		return 0
	}
	return float64(duration) / float64(time.Millisecond)
}
