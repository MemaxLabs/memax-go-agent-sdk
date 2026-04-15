package agenteval

import (
	"context"
	"sync"

	"github.com/MemaxLabs/memax-go-agent-sdk/model"
)

// ScriptedModel is a deterministic model.Client for evals and tests. Each
// model call consumes the next scripted turn.
type ScriptedModel struct {
	mu       sync.Mutex
	turns    [][]model.StreamEvent
	requests []model.Request
}

// NewScriptedModel returns a model client that emits turns in order.
func NewScriptedModel(turns ...[]model.StreamEvent) *ScriptedModel {
	copied := make([][]model.StreamEvent, len(turns))
	for i, turn := range turns {
		copied[i] = append([]model.StreamEvent(nil), turn...)
	}
	return &ScriptedModel{turns: copied}
}

// Stream implements model.Client.
func (m *ScriptedModel) Stream(_ context.Context, req model.Request) (model.Stream, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.requests = append(m.requests, req)
	index := len(m.requests) - 1
	if index >= len(m.turns) {
		return &scriptedStream{}, nil
	}
	return &scriptedStream{events: append([]model.StreamEvent(nil), m.turns[index]...)}, nil
}

// Requests returns the model requests received so far.
func (m *ScriptedModel) Requests() []model.Request {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]model.Request(nil), m.requests...)
}

type scriptedStream struct {
	events []model.StreamEvent
	index  int
}

func (s *scriptedStream) Recv() (model.StreamEvent, error) {
	if s.index >= len(s.events) {
		return model.StreamEvent{}, model.ErrEndOfStream
	}
	event := s.events[s.index]
	s.index++
	return event, nil
}

func (s *scriptedStream) Close() error {
	return nil
}
