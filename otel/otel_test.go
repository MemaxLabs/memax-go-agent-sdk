package otel

import (
	"context"
	"errors"
	"testing"

	"github.com/MemaxLabs/memax-go-agent-sdk/telemetry"
	"go.opentelemetry.io/otel/trace/noop"
)

func TestTracerAdaptsOpenTelemetryTracer(t *testing.T) {
	tracer := FromTraceTracer(noop.NewTracerProvider().Tracer("test"))
	_, span := tracer.Start(context.Background(), "test.span", telemetry.String("key", "value"))
	span.Set(telemetry.Int("count", 1), telemetry.Bool("ok", true))
	span.RecordError(errors.New("boom"))
	span.End()
}

func TestMeterAdaptsOpenTelemetryMeter(t *testing.T) {
	meter := NewMeter("test")
	meter.Add(context.Background(), "test.counter", 1, telemetry.String("key", "value"))
	meter.Record(context.Background(), "test.histogram", 1.5, telemetry.Float64("value", 1.5))
}
