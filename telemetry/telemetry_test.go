package telemetry

import (
	"context"
	"testing"
)

func TestFanoutMeterForwardsMeasurements(t *testing.T) {
	first := &recordingMeter{}
	second := &recordingMeter{}
	var typedNil *recordingMeter
	meter := NewFanoutMeter(nil, typedNil, first, second)

	meter.Add(context.Background(), "memax.test.counter", 2, String("key", "value"))
	meter.Record(context.Background(), "memax.test.histogram", 3.5, Bool("ok", true))

	for name, recorder := range map[string]*recordingMeter{
		"first":  first,
		"second": second,
	} {
		if len(recorder.adds) != 1 {
			t.Fatalf("%s adds = %#v, want one add", name, recorder.adds)
		}
		if recorder.adds[0].name != "memax.test.counter" || recorder.adds[0].value != 2 {
			t.Fatalf("%s add = %#v, want counter increment", name, recorder.adds[0])
		}
		if len(recorder.records) != 1 {
			t.Fatalf("%s records = %#v, want one record", name, recorder.records)
		}
		if recorder.records[0].name != "memax.test.histogram" || recorder.records[0].value != 3.5 {
			t.Fatalf("%s record = %#v, want histogram record", name, recorder.records[0])
		}
	}
}

func TestFanoutMeterProtectsDownstreamAttributeMutation(t *testing.T) {
	mutating := mutatingMeter{}
	recording := &recordingMeter{}
	meter := NewFanoutMeter(mutating, recording)

	meter.Add(context.Background(), "memax.test.counter", 1, String("key", "original"))

	if len(recording.adds) != 1 {
		t.Fatalf("adds = %#v, want one add", recording.adds)
	}
	if got := recording.adds[0].attrs[0].Value; got != "original" {
		t.Fatalf("recorded attr value = %v, want original", got)
	}
}

func TestFanoutMeterZeroValueNoops(t *testing.T) {
	var meter FanoutMeter
	meter.Add(context.Background(), "memax.test.counter", 1)
	meter.Record(context.Background(), "memax.test.histogram", 1)
}

type metricAdd struct {
	name  string
	value int64
	attrs []Attribute
}

type metricRecord struct {
	name  string
	value float64
	attrs []Attribute
}

type recordingMeter struct {
	adds    []metricAdd
	records []metricRecord
}

func (m *recordingMeter) Add(_ context.Context, name string, value int64, attrs ...Attribute) {
	m.adds = append(m.adds, metricAdd{name: name, value: value, attrs: append([]Attribute(nil), attrs...)})
}

func (m *recordingMeter) Record(_ context.Context, name string, value float64, attrs ...Attribute) {
	m.records = append(m.records, metricRecord{name: name, value: value, attrs: append([]Attribute(nil), attrs...)})
}

type mutatingMeter struct{}

func (mutatingMeter) Add(_ context.Context, _ string, _ int64, attrs ...Attribute) {
	if len(attrs) > 0 {
		attrs[0].Value = "mutated"
	}
}

func (mutatingMeter) Record(context.Context, string, float64, ...Attribute) {}
