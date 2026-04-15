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
		copied[i] = cloneStreamEvents(turn)
	}
	return &ScriptedModel{turns: copied}
}

// Stream implements model.Client.
func (m *ScriptedModel) Stream(_ context.Context, req model.Request) (model.Stream, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.requests = append(m.requests, cloneRequest(req))
	index := len(m.requests) - 1
	if index >= len(m.turns) {
		return &scriptedStream{}, nil
	}
	return &scriptedStream{events: cloneStreamEvents(m.turns[index])}, nil
}

// Requests returns the model requests received so far.
func (m *ScriptedModel) Requests() []model.Request {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]model.Request, len(m.requests))
	for i, req := range m.requests {
		out[i] = cloneRequest(req)
	}
	return out
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

func cloneRequest(req model.Request) model.Request {
	req.Messages = cloneMessages(req.Messages)
	req.Tools = cloneToolSpecs(req.Tools)
	return req
}

func cloneMessages(messages []model.Message) []model.Message {
	if len(messages) == 0 {
		return nil
	}
	out := make([]model.Message, len(messages))
	for i, msg := range messages {
		out[i] = msg
		out[i].Content = cloneContentBlocks(msg.Content)
		if msg.ToolResult != nil {
			toolResult := *msg.ToolResult
			toolResult.Metadata = cloneMetadata(toolResult.Metadata)
			out[i].ToolResult = &toolResult
		}
	}
	return out
}

func cloneContentBlocks(blocks []model.ContentBlock) []model.ContentBlock {
	if len(blocks) == 0 {
		return nil
	}
	out := make([]model.ContentBlock, len(blocks))
	for i, block := range blocks {
		out[i] = block
		if block.ToolUse != nil {
			toolUse := *block.ToolUse
			toolUse.Input = append([]byte(nil), block.ToolUse.Input...)
			out[i].ToolUse = &toolUse
		}
	}
	return out
}

func cloneToolSpecs(specs []model.ToolSpec) []model.ToolSpec {
	if len(specs) == 0 {
		return nil
	}
	out := make([]model.ToolSpec, len(specs))
	for i, spec := range specs {
		out[i] = spec
		out[i].InputSchema = cloneSchemaMap(spec.InputSchema)
	}
	return out
}

func cloneStreamEvents(events []model.StreamEvent) []model.StreamEvent {
	if len(events) == 0 {
		return nil
	}
	out := make([]model.StreamEvent, len(events))
	for i, event := range events {
		out[i] = event
		out[i].ToolUse.Input = append([]byte(nil), event.ToolUse.Input...)
		if event.Usage != nil {
			usage := *event.Usage
			usage.Metadata = cloneMetadata(usage.Metadata)
			out[i].Usage = &usage
		}
	}
	return out
}

func cloneSchemaMap(value map[string]any) map[string]any {
	if len(value) == 0 {
		return nil
	}
	out := make(map[string]any, len(value))
	for key, item := range value {
		out[key] = cloneSchemaValue(item)
	}
	return out
}

func cloneSchemaValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return cloneSchemaMap(typed)
	case []any:
		out := make([]any, len(typed))
		for i, item := range typed {
			out[i] = cloneSchemaValue(item)
		}
		return out
	default:
		return typed
	}
}

func cloneMetadata(metadata map[string]any) map[string]any {
	if len(metadata) == 0 {
		return nil
	}
	out := make(map[string]any, len(metadata))
	for key, value := range metadata {
		out[key] = value
	}
	return out
}
