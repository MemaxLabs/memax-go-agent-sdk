package telemetry

import (
	"context"
	"reflect"
)

// Attribute is a key/value attribute used by SDK tracing and metrics.
type Attribute struct {
	Key   string
	Value any
}

// String creates a string telemetry attribute.
func String(key string, value string) Attribute {
	return Attribute{Key: key, Value: value}
}

// Int creates an integer telemetry attribute.
func Int(key string, value int) Attribute {
	return Attribute{Key: key, Value: value}
}

// Bool creates a boolean telemetry attribute.
func Bool(key string, value bool) Attribute {
	return Attribute{Key: key, Value: value}
}

// Int64 creates an int64 telemetry attribute.
func Int64(key string, value int64) Attribute {
	return Attribute{Key: key, Value: value}
}

// Float64 creates a float64 telemetry attribute.
func Float64(key string, value float64) Attribute {
	return Attribute{Key: key, Value: value}
}

// Tracer starts SDK spans.
type Tracer interface {
	Start(context.Context, string, ...Attribute) (context.Context, Span)
}

// Span records SDK span attributes, errors, and completion.
type Span interface {
	Set(...Attribute)
	RecordError(error)
	End()
}

// NoopTracer drops all spans.
type NoopTracer struct{}

// Start returns a no-op span.
func (NoopTracer) Start(ctx context.Context, _ string, _ ...Attribute) (context.Context, Span) {
	return ctx, NoopSpan{}
}

// NoopSpan drops all span operations.
type NoopSpan struct{}

// Set drops attributes.
func (NoopSpan) Set(...Attribute) {}

// RecordError drops errors.
func (NoopSpan) RecordError(error) {}

// End completes the no-op span.
func (NoopSpan) End() {}

// Meter records SDK counters and value measurements.
type Meter interface {
	Add(context.Context, string, int64, ...Attribute)
	Record(context.Context, string, float64, ...Attribute)
}

// NoopMeter drops all measurements.
type NoopMeter struct{}

// Add drops counter increments.
func (NoopMeter) Add(context.Context, string, int64, ...Attribute) {}

// Record drops value measurements.
func (NoopMeter) Record(context.Context, string, float64, ...Attribute) {}

// FanoutMeter forwards measurements to multiple meters.
//
// Calls are synchronous: a slow downstream meter slows the caller, and panics
// from downstream meters are not recovered. Use this when hosts need the same
// SDK measurements delivered to more than one provider-neutral sink, such as a
// local recorder plus an OpenTelemetry exporter. Nil meters are ignored.
type FanoutMeter struct {
	meters []Meter
}

// NewFanoutMeter returns a meter that forwards every measurement to meters.
// The returned zero-downstream meter is a no-op.
func NewFanoutMeter(meters ...Meter) FanoutMeter {
	out := FanoutMeter{}
	for _, meter := range meters {
		if !meterIsNil(meter) {
			out.meters = append(out.meters, meter)
		}
	}
	return out
}

// Add forwards a counter increment to every downstream meter.
func (m FanoutMeter) Add(ctx context.Context, name string, value int64, attrs ...Attribute) {
	for _, meter := range m.meters {
		// Clone per downstream meter so one meter cannot mutate the attribute
		// slice seen by the next. Attribute values are shallow-copied; callers
		// should avoid mutable reference values.
		meter.Add(ctx, name, value, cloneAttributes(attrs)...)
	}
}

// Record forwards a value measurement to every downstream meter.
func (m FanoutMeter) Record(ctx context.Context, name string, value float64, attrs ...Attribute) {
	for _, meter := range m.meters {
		// Clone per downstream meter so one meter cannot mutate the attribute
		// slice seen by the next. Attribute values are shallow-copied; callers
		// should avoid mutable reference values.
		meter.Record(ctx, name, value, cloneAttributes(attrs)...)
	}
}

func meterIsNil(meter Meter) bool {
	if meter == nil {
		return true
	}
	value := reflect.ValueOf(meter)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

func cloneAttributes(attrs []Attribute) []Attribute {
	if len(attrs) == 0 {
		return nil
	}
	return append([]Attribute(nil), attrs...)
}
