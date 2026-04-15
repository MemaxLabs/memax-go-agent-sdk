package telemetry

import "context"

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
