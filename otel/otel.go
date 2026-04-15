package otel

import (
	"context"
	"fmt"
	"sync"

	"github.com/MemaxLabs/memax-go-agent-sdk/telemetry"
	gootel "go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
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

type Meter struct {
	mu         sync.Mutex
	meter      metric.Meter
	counters   map[string]metric.Int64Counter
	histograms map[string]metric.Float64Histogram
}

// NewMeter creates an SDK telemetry meter backed by the global OpenTelemetry
// meter provider.
func NewMeter(name string) *Meter {
	if name == "" {
		name = defaultInstrumentationName
	}
	return FromMetricMeter(gootel.Meter(name))
}

// FromMetricMeter adapts an existing OpenTelemetry meter.
func FromMetricMeter(meter metric.Meter) *Meter {
	if meter == nil {
		meter = gootel.Meter(defaultInstrumentationName)
	}
	return &Meter{
		meter:      meter,
		counters:   make(map[string]metric.Int64Counter),
		histograms: make(map[string]metric.Float64Histogram),
	}
}

// Add records a counter increment.
func (m *Meter) Add(ctx context.Context, name string, value int64, attrs ...telemetry.Attribute) {
	if m == nil || name == "" {
		return
	}
	counter, err := m.counter(name)
	if err != nil {
		return
	}
	counter.Add(ctx, value, metric.WithAttributes(convertAttributes(attrs)...))
}

// Record records a value measurement.
func (m *Meter) Record(ctx context.Context, name string, value float64, attrs ...telemetry.Attribute) {
	if m == nil || name == "" {
		return
	}
	histogram, err := m.histogram(name)
	if err != nil {
		return
	}
	histogram.Record(ctx, value, metric.WithAttributes(convertAttributes(attrs)...))
}

func (m *Meter) counter(name string) (metric.Int64Counter, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	counter, ok := m.counters[name]
	if ok {
		return counter, nil
	}
	counter, err := m.meter.Int64Counter(name)
	if err != nil {
		return nil, err
	}
	m.counters[name] = counter
	return counter, nil
}

func (m *Meter) histogram(name string) (metric.Float64Histogram, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	histogram, ok := m.histograms[name]
	if ok {
		return histogram, nil
	}
	histogram, err := m.meter.Float64Histogram(name)
	if err != nil {
		return nil, err
	}
	m.histograms[name] = histogram
	return histogram, nil
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
