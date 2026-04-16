package scenarios

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	memaxagent "github.com/MemaxLabs/memax-go-agent-sdk"
	"github.com/MemaxLabs/memax-go-agent-sdk/agenteval"
	"github.com/MemaxLabs/memax-go-agent-sdk/model"
	"github.com/MemaxLabs/memax-go-agent-sdk/permission"
	"github.com/MemaxLabs/memax-go-agent-sdk/tool"
)

// StreamingSafeToolOverlap returns a single-use scenario where a read-only,
// concurrency-safe tool starts before trailing assistant text finishes.
func StreamingSafeToolOverlap() agenteval.Case {
	started := make(chan struct{})
	modelClient := &streamingGateModel{
		first: &gateStream{
			events: []model.StreamEvent{
				{Kind: model.StreamToolUseStart, ToolUse: model.ToolUse{ID: "tool-1", Name: "stream_read"}},
				{Kind: model.StreamToolUseDelta, ToolUse: model.ToolUse{ID: "tool-1", Name: "stream_read"}, ToolUseDelta: `{"path":"README.md"}`},
				{Kind: model.StreamToolUse, ToolUse: model.ToolUse{
					ID:    "tool-1",
					Name:  "stream_read",
					Input: json.RawMessage(`{"path":"README.md"}`),
				}},
				{Kind: model.StreamText, Text: " after tool start"},
			},
			beforeIndex: 3,
			waitFor:     started,
			waitErr:     "safe tool did not start before trailing assistant text",
		},
		second: []model.StreamEvent{{Kind: model.StreamText, Text: "streaming read completed"}},
	}

	return agenteval.Case{
		Name:   "streaming_safe_tool_overlap",
		Prompt: "Start a safe read while continuing to stream.",
		Options: memaxagent.Options{
			Model: modelClient,
			Tools: tool.NewRegistry(streamReadTool(started)),
		},
		Assertions: []agenteval.Assertion{
			agenteval.EventKindEmitted(memaxagent.EventToolUseStart),
			agenteval.EventKindEmitted(memaxagent.EventToolUseDelta),
			streamingEventDeltaContains("README.md"),
			agenteval.ToolUsed("stream_read"),
			agenteval.NoToolErrors(),
			agenteval.FinalEquals("streaming read completed"),
			requestCountEquals(modelClient, 2),
			toolResultContains("stream_read", false, "read README.md"),
		},
	}
}

// StreamingMutatingToolWaits returns a single-use scenario where a destructive
// tool is not started until the assistant stream has completed.
func StreamingMutatingToolWaits() agenteval.Case {
	started := make(chan struct{})
	var streamFinished atomic.Bool
	modelClient := &streamingGateModel{
		first: &gateStream{
			events: []model.StreamEvent{
				{Kind: model.StreamToolUseStart, ToolUse: model.ToolUse{ID: "tool-1", Name: "stream_write"}},
				{Kind: model.StreamToolUseDelta, ToolUse: model.ToolUse{ID: "tool-1", Name: "stream_write"}, ToolUseDelta: `{"path":"README.md","content":"updated"}`},
				{Kind: model.StreamToolUse, ToolUse: model.ToolUse{
					ID:    "tool-1",
					Name:  "stream_write",
					Input: json.RawMessage(`{"path":"README.md","content":"updated"}`),
				}},
				{Kind: model.StreamText, Text: " before mutation"},
			},
			beforeIndex: 3,
			mustNotSee:  started,
			mustNotErr:  "mutating tool started before assistant stream finished",
			onEOF:       func() { streamFinished.Store(true) },
		},
		second: []model.StreamEvent{{Kind: model.StreamText, Text: "mutation completed after stream"}},
	}

	return agenteval.Case{
		Name:   "streaming_mutating_tool_waits",
		Prompt: "Write after the assistant stream completes.",
		Options: memaxagent.Options{
			Model: modelClient,
			Tools: tool.NewRegistry(streamWriteTool(started, &streamFinished)),
		},
		Assertions: []agenteval.Assertion{
			agenteval.EventKindEmitted(memaxagent.EventToolUseStart),
			agenteval.EventKindEmitted(memaxagent.EventToolUseDelta),
			streamingEventDeltaContains("updated"),
			agenteval.ToolUsed("stream_write"),
			agenteval.NoToolErrors(),
			agenteval.FinalEquals("mutation completed after stream"),
			toolResultContains("stream_write", false, "wrote README.md"),
			requestCountEquals(modelClient, 2),
		},
	}
}

// StreamingPermissionDenialRecovery returns a single-use scenario where an
// early-eligible tool still passes through permissions and the model recovers
// from the model-visible denial.
func StreamingPermissionDenialRecovery() agenteval.Case {
	var runCount atomic.Int32
	modelClient := &streamingGateModel{
		first: &gateStream{
			events: []model.StreamEvent{
				{Kind: model.StreamToolUseStart, ToolUse: model.ToolUse{ID: "tool-1", Name: "stream_read"}},
				{Kind: model.StreamToolUseDelta, ToolUse: model.ToolUse{ID: "tool-1", Name: "stream_read"}, ToolUseDelta: `{"path":"secrets.md"}`},
				{Kind: model.StreamToolUse, ToolUse: model.ToolUse{
					ID:    "tool-1",
					Name:  "stream_read",
					Input: json.RawMessage(`{"path":"secrets.md"}`),
				}},
				{Kind: model.StreamText, Text: " after denied read"},
			},
		},
		second: []model.StreamEvent{{Kind: model.StreamText, Text: "recovered after streaming permission denial"}},
	}

	return agenteval.Case{
		Name:   "streaming_permission_denial_recovery",
		Prompt: "Read the secret, then recover if denied.",
		Options: memaxagent.Options{
			Model: modelClient,
			Tools: tool.NewRegistry(streamReadCountingTool(&runCount)),
			Permissions: permission.Func(func(context.Context, model.ToolUse, model.ToolSpec) tool.Decision {
				return tool.Decision{Allow: false, Reason: "streaming read denied by policy"}
			}),
		},
		Assertions: []agenteval.Assertion{
			agenteval.EventKindEmitted(memaxagent.EventToolUseStart),
			agenteval.EventKindEmitted(memaxagent.EventToolUseDelta),
			streamingEventDeltaContains("secrets.md"),
			agenteval.ToolUsed("stream_read"),
			toolResultContains("stream_read", true, "streaming read denied"),
			agenteval.FinalEquals("recovered after streaming permission denial"),
			requestCountEquals(modelClient, 2),
			{
				Name: "denied early tool handler did not run",
				Check: func(agenteval.Result) error {
					if got := runCount.Load(); got != 0 {
						return fmt.Errorf("handler ran %d times, want 0", got)
					}
					return nil
				},
			},
		},
	}
}

// StreamingFailureCancelsEarlyTool returns a single-use scenario where a model
// stream failure cancels an already-started early safe tool and emits a paired
// cancellation result before the run error.
func StreamingFailureCancelsEarlyTool() agenteval.Case {
	started := make(chan struct{})
	cancelled := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	modelClient := &streamingErrorModel{
		started: started,
		err:     errors.New("stream exploded"),
		events: []model.StreamEvent{
			{Kind: model.StreamToolUseStart, ToolUse: model.ToolUse{ID: "tool-1", Name: "stream_read"}},
			{Kind: model.StreamToolUseDelta, ToolUse: model.ToolUse{ID: "tool-1", Name: "stream_read"}, ToolUseDelta: `{"path":"README.md"}`},
			{Kind: model.StreamToolUse, ToolUse: model.ToolUse{
				ID:    "tool-1",
				Name:  "stream_read",
				Input: json.RawMessage(`{"path":"README.md"}`),
			}},
		},
	}

	return agenteval.Case{
		Name:       "streaming_failure_cancels_early_tool",
		Prompt:     "Start a read, then handle stream failure.",
		AllowError: true,
		Cleanup: func() {
			releaseOnce.Do(func() { close(release) })
		},
		Options: memaxagent.Options{
			Model: modelClient,
			Tools: tool.NewRegistry(streamBlockingReadTool(started, cancelled, release)),
		},
		Assertions: []agenteval.Assertion{
			agenteval.EventKindEmitted(memaxagent.EventToolUseStart),
			agenteval.EventKindEmitted(memaxagent.EventToolUseDelta),
			agenteval.ToolUsed("stream_read"),
			toolResultContains("stream_read", true, "model streaming stopped"),
			agenteval.RunErrorContains("stream exploded"),
			requestCountEquals(modelClient, 1),
			{
				Name: "early tool observed cancellation",
				Check: func(agenteval.Result) error {
					select {
					case <-cancelled:
						return nil
					case <-time.After(5 * time.Second):
						return fmt.Errorf("early tool did not observe cancellation")
					}
				},
			},
		},
	}
}

// StreamingCancellation returns a single-use scenario where cancellation while
// streaming stops the run cleanly instead of hanging.
func StreamingCancellation() agenteval.Case {
	modelClient := &cancelStreamingModel{}
	return agenteval.Case{
		Name:       "streaming_cancellation",
		Prompt:     "Start streaming and cancel.",
		Timeout:    100 * time.Millisecond,
		AllowError: true,
		Options: memaxagent.Options{
			Model: modelClient,
		},
		Assertions: []agenteval.Assertion{
			requestCountEquals(modelClient, 1),
			{
				Name: "stream observed cancellation",
				Check: func(agenteval.Result) error {
					if !modelClient.Cancelled() {
						return fmt.Errorf("stream did not observe context cancellation")
					}
					return nil
				},
			},
			{
				Name: "no final result emitted",
				Check: func(result agenteval.Result) error {
					if result.Final != "" {
						return fmt.Errorf("final = %q, want no final answer after cancellation", result.Final)
					}
					return nil
				},
			},
		},
	}
}

func streamReadTool(started chan<- struct{}) tool.Tool {
	return tool.Definition{
		ToolSpec: model.ToolSpec{
			Name:            "stream_read",
			Description:     "Read a file during streaming.",
			ReadOnly:        true,
			ConcurrencySafe: true,
			InputSchema:     stringFieldSchema("path"),
		},
		Handler: func(_ context.Context, call tool.Call) (model.ToolResult, error) {
			close(started)
			path, err := pathInput(call.Use)
			if err != nil {
				return model.ToolResult{}, err
			}
			return model.ToolResult{Content: "read " + path}, nil
		},
	}
}

func streamReadCountingTool(count *atomic.Int32) tool.Tool {
	return tool.Definition{
		ToolSpec: model.ToolSpec{
			Name:            "stream_read",
			Description:     "Read a file during streaming.",
			ReadOnly:        true,
			ConcurrencySafe: true,
			InputSchema:     stringFieldSchema("path"),
		},
		Handler: func(_ context.Context, call tool.Call) (model.ToolResult, error) {
			count.Add(1)
			path, err := pathInput(call.Use)
			if err != nil {
				return model.ToolResult{}, err
			}
			return model.ToolResult{Content: "read " + path}, nil
		},
	}
}

func streamBlockingReadTool(started chan<- struct{}, cancelled chan<- struct{}, release <-chan struct{}) tool.Tool {
	return tool.Definition{
		ToolSpec: model.ToolSpec{
			Name:            "stream_read",
			Description:     "Read a file during streaming until canceled.",
			ReadOnly:        true,
			ConcurrencySafe: true,
			InputSchema:     stringFieldSchema("path"),
		},
		Handler: func(ctx context.Context, call tool.Call) (model.ToolResult, error) {
			close(started)
			<-ctx.Done()
			close(cancelled)
			<-release
			path, err := pathInput(call.Use)
			if err != nil {
				return model.ToolResult{}, err
			}
			return model.ToolResult{Content: "canceled read " + path, IsError: true}, nil
		},
	}
}

func streamWriteTool(started chan<- struct{}, streamFinished *atomic.Bool) tool.Tool {
	return tool.Definition{
		ToolSpec: model.ToolSpec{
			Name:        "stream_write",
			Description: "Write a file after streaming.",
			Destructive: true,
			InputSchema: map[string]any{
				"type":                 "object",
				"required":             []any{"path", "content"},
				"additionalProperties": false,
				"properties": map[string]any{
					"path":    map[string]any{"type": "string"},
					"content": map[string]any{"type": "string"},
				},
			},
		},
		Handler: func(_ context.Context, call tool.Call) (model.ToolResult, error) {
			close(started)
			if !streamFinished.Load() {
				return model.ToolResult{Content: "write started before stream completed", IsError: true}, nil
			}
			path, err := pathInput(call.Use)
			if err != nil {
				return model.ToolResult{}, err
			}
			return model.ToolResult{Content: "wrote " + path}, nil
		},
	}
}

func pathInput(use model.ToolUse) (string, error) {
	var input struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(use.Input, &input); err != nil {
		return "", err
	}
	return input.Path, nil
}

type streamingGateModel struct {
	mu       sync.Mutex
	requests []model.Request
	calls    int
	first    *gateStream
	second   []model.StreamEvent
}

func (m *streamingGateModel) Stream(_ context.Context, req model.Request) (model.Stream, error) {
	m.mu.Lock()
	m.requests = append(m.requests, cloneRequest(req))
	m.calls++
	calls := m.calls
	m.mu.Unlock()
	if calls == 1 {
		return m.first, nil
	}
	return &sliceStream{events: m.second}, nil
}

func (m *streamingGateModel) RequestCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.requests)
}

// gateStream is a single-consumer stream test helper. Recv is intentionally
// not concurrency-safe because model streams are consumed sequentially.
type gateStream struct {
	events      []model.StreamEvent
	index       int
	beforeIndex int
	waitFor     <-chan struct{}
	waitErr     string
	mustNotSee  <-chan struct{}
	mustNotErr  string
	onEOF       func()
}

func (s *gateStream) Recv() (model.StreamEvent, error) {
	if s.index >= len(s.events) {
		if s.onEOF != nil {
			s.onEOF()
			s.onEOF = nil
		}
		return model.StreamEvent{}, model.ErrEndOfStream
	}
	if s.index == s.beforeIndex {
		if s.waitFor != nil {
			select {
			case <-s.waitFor:
			case <-time.After(5 * time.Second):
				return model.StreamEvent{}, errors.New(s.waitErr)
			}
		}
		if s.mustNotSee != nil {
			select {
			case <-s.mustNotSee:
				return model.StreamEvent{}, errors.New(s.mustNotErr)
			default:
			}
		}
	}
	event := s.events[s.index]
	s.index++
	return event, nil
}

func (s *gateStream) Close() error {
	return nil
}

type sliceStream struct {
	events []model.StreamEvent
	index  int
}

func (s *sliceStream) Recv() (model.StreamEvent, error) {
	if s.index >= len(s.events) {
		return model.StreamEvent{}, model.ErrEndOfStream
	}
	event := s.events[s.index]
	s.index++
	return event, nil
}

func (s *sliceStream) Close() error {
	return nil
}

type cancelStreamingModel struct {
	requests  atomic.Int32
	cancelled atomic.Bool
}

func (m *cancelStreamingModel) Stream(ctx context.Context, _ model.Request) (model.Stream, error) {
	m.requests.Add(1)
	return cancelStream{ctx: ctx, cancelled: &m.cancelled}, nil
}

func (m *cancelStreamingModel) RequestCount() int {
	return int(m.requests.Load())
}

func (m *cancelStreamingModel) Cancelled() bool {
	return m.cancelled.Load()
}

type cancelStream struct {
	ctx       context.Context
	cancelled *atomic.Bool
}

func (s cancelStream) Recv() (model.StreamEvent, error) {
	<-s.ctx.Done()
	s.cancelled.Store(true)
	return model.StreamEvent{}, s.ctx.Err()
}

func (s cancelStream) Close() error {
	return nil
}

type streamingErrorModel struct {
	requests atomic.Int32
	started  <-chan struct{}
	events   []model.StreamEvent
	err      error
}

func (m *streamingErrorModel) Stream(context.Context, model.Request) (model.Stream, error) {
	m.requests.Add(1)
	return &streamingErrorStream{events: m.events, started: m.started, err: m.err}, nil
}

func (m *streamingErrorModel) RequestCount() int {
	return int(m.requests.Load())
}

type streamingErrorStream struct {
	events  []model.StreamEvent
	index   int
	started <-chan struct{}
	err     error
}

func (s *streamingErrorStream) Recv() (model.StreamEvent, error) {
	if s.index >= len(s.events) {
		if s.started != nil {
			select {
			case <-s.started:
			case <-time.After(5 * time.Second):
				return model.StreamEvent{}, errors.New("early tool did not start before stream error")
			}
		}
		return model.StreamEvent{}, s.err
	}
	event := s.events[s.index]
	s.index++
	return event, nil
}

func (s *streamingErrorStream) Close() error {
	return nil
}

func streamingEventDeltaContains(substring string) agenteval.Assertion {
	return agenteval.Assertion{
		Name: "streaming tool delta contains",
		Check: func(result agenteval.Result) error {
			for _, event := range result.Events {
				if event.Kind == memaxagent.EventToolUseDelta && strings.Contains(event.ToolUseDelta, substring) {
					return nil
				}
			}
			return fmt.Errorf("missing streaming tool delta containing %q", substring)
		},
	}
}
