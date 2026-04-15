package telemetry

import "context"

// Attribute is a tracing attribute used by the SDK telemetry interface.
type Attribute struct {
	Key   string
	Value any
}

// String creates a string tracing attribute.
func String(key string, value string) Attribute {
	return Attribute{Key: key, Value: value}
}

// Int creates an integer tracing attribute.
func Int(key string, value int) Attribute {
	return Attribute{Key: key, Value: value}
}

// Bool creates a boolean tracing attribute.
func Bool(key string, value bool) Attribute {
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
