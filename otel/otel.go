package otel

import (
	"context"
	"fmt"

	"github.com/MemaxLabs/memax-go-agent-sdk/telemetry"
	gootel "go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

const defaultInstrumentationName = "github.com/MemaxLabs/memax-go-agent-sdk"

type Tracer struct {
	tracer trace.Tracer
}

// NewTracer creates an SDK telemetry tracer backed by the global OpenTelemetry
// tracer provider.
func NewTracer(name string) Tracer {
	if name == "" {
		name = defaultInstrumentationName
	}
	return Tracer{tracer: gootel.Tracer(name)}
}

// FromTraceTracer adapts an existing OpenTelemetry tracer.
func FromTraceTracer(tracer trace.Tracer) Tracer {
	if tracer == nil {
		return NewTracer("")
	}
	return Tracer{tracer: tracer}
}

// Start starts an OpenTelemetry span.
func (t Tracer) Start(ctx context.Context, name string, attrs ...telemetry.Attribute) (context.Context, telemetry.Span) {
	tracer := t.tracer
	if tracer == nil {
		tracer = gootel.Tracer(defaultInstrumentationName)
	}
	ctx, span := tracer.Start(ctx, name, trace.WithAttributes(convertAttributes(attrs)...))
	return ctx, Span{span: span}
}

type Span struct {
	span trace.Span
}

// Set records span attributes.
func (s Span) Set(attrs ...telemetry.Attribute) {
	if s.span == nil {
		return
	}
	s.span.SetAttributes(convertAttributes(attrs)...)
}

// RecordError records err and marks the span as failed.
func (s Span) RecordError(err error) {
	if s.span == nil || err == nil {
		return
	}
	s.span.RecordError(err)
	s.span.SetStatus(codes.Error, err.Error())
}

// End ends the span.
func (s Span) End() {
	if s.span != nil {
		s.span.End()
	}
}

func convertAttributes(attrs []telemetry.Attribute) []attribute.KeyValue {
	out := make([]attribute.KeyValue, 0, len(attrs))
	for _, attr := range attrs {
		if attr.Key == "" {
			continue
		}
		out = append(out, convertAttribute(attr))
	}
	return out
}

func convertAttribute(attr telemetry.Attribute) attribute.KeyValue {
	key := attribute.Key(attr.Key)
	switch value := attr.Value.(type) {
	case string:
		return key.String(value)
	case bool:
		return key.Bool(value)
	case int:
		return key.Int(value)
	case int64:
		return key.Int64(value)
	case float64:
		return key.Float64(value)
	default:
		return key.String(fmt.Sprint(value))
	}
}
