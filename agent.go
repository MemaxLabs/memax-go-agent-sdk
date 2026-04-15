package memaxagent

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/MemaxLabs/memax-go-agent-sdk/model"
	"github.com/MemaxLabs/memax-go-agent-sdk/telemetry"
	"github.com/MemaxLabs/memax-go-agent-sdk/tool"
)

var ErrMissingModelClient = errors.New("memaxagent: model client is required")

// Query runs an autonomous agent loop for a single prompt and streams events.
func Query(ctx context.Context, prompt string, opts Options) (<-chan Event, error) {
	opts = opts.withDefaults()
	if opts.Model == nil {
		return nil, ErrMissingModelClient
	}
	var cancel context.CancelFunc
	if opts.MaxRunDuration > 0 {
		ctx, cancel = context.WithTimeout(ctx, opts.MaxRunDuration)
	}
	ctx, querySpan := opts.Tracer.Start(ctx, "memaxagent.query",
		telemetry.Int("memax.max_turns", opts.MaxTurns),
		telemetry.Int("memax.max_tool_concurrency", opts.MaxToolConcurrency),
	)

	sess, err := opts.Sessions.Create(ctx)
	if err != nil {
		if cancel != nil {
			cancel()
		}
		err = fmt.Errorf("create session: %w", err)
		querySpan.RecordError(err)
		querySpan.End()
		return nil, err
	}
	querySpan.Set(telemetry.String("memax.session_id", sess.ID))
	if err := opts.Sessions.Append(ctx, sess.ID, model.Message{
		Role:    model.RoleUser,
		Content: []model.ContentBlock{{Type: model.ContentText, Text: prompt}},
	}); err != nil {
		if cancel != nil {
			cancel()
		}
		err = fmt.Errorf("append user prompt: %w", err)
		querySpan.RecordError(err)
		querySpan.End()
		return nil, err
	}

	events := make(chan Event)
	go func() {
		defer close(events)
		defer querySpan.End()
		if cancel != nil {
			defer cancel()
		}
		runLoop(ctx, events, sess.ID, opts)
	}()

	return events, nil
}

func runLoop(ctx context.Context, events chan<- Event, sessionID string, opts Options) {
	executor := tool.Executor{
		Registry:       opts.Tools,
		Permissions:    opts.Permissions,
		Hooks:          opts.Hooks,
		MaxConcurrency: opts.MaxToolConcurrency,
		Runtime:        tool.Runtime{SessionID: sessionID},
		Tracer:         opts.Tracer,
	}

	emit := func(event Event) bool {
		select {
		case <-ctx.Done():
			return false
		case events <- event:
			return true
		}
	}

	if !emit(newEvent(EventSessionStarted, sessionID, 0)) {
		return
	}

	for turn := 1; turn <= opts.MaxTurns; turn++ {
		turnCtx, turnSpan := opts.Tracer.Start(ctx, "memaxagent.turn",
			telemetry.String("memax.session_id", sessionID),
			telemetry.Int("memax.turn", turn),
		)
		shouldStop := false
		func() {
			defer turnSpan.End()

			messages, err := opts.Sessions.Messages(turnCtx, sessionID)
			if err != nil {
				err = fmt.Errorf("load session messages: %w", err)
				turnSpan.RecordError(err)
				emitError(turnCtx, emit, sessionID, turn, err)
				shouldStop = true
				return
			}
			if opts.Context != nil {
				originalMessages := messages
				originalCount := len(messages)
				contextCtx, contextSpan := opts.Tracer.Start(turnCtx, "memaxagent.context.apply",
					telemetry.String("memax.session_id", sessionID),
					telemetry.Int("memax.turn", turn),
					telemetry.Int("memax.context.original_messages", originalCount),
				)
				messages, err = opts.Context.Apply(contextCtx, messages)
				if err != nil {
					err = fmt.Errorf("apply context policy: %w", err)
					contextSpan.RecordError(err)
					contextSpan.End()
					turnSpan.RecordError(err)
					emitError(turnCtx, emit, sessionID, turn, err)
					shouldStop = true
					return
				}
				contextSpan.Set(telemetry.Int("memax.context.sent_messages", len(messages)))
				contextSpan.End()
				if !reflect.DeepEqual(messages, originalMessages) {
					event := newEvent(EventContextApplied, sessionID, turn)
					event.Context = &ContextEvent{
						OriginalMessages: originalCount,
						SentMessages:     len(messages),
					}
					if !emit(event) {
						shouldStop = true
						return
					}
				}
			}
			turnSpan.Set(telemetry.Int("memax.model.messages", len(messages)))

			if !emit(newEvent(EventModelRequest, sessionID, turn)) {
				shouldStop = true
				return
			}

			toolSpecs := opts.Tools.Specs()
			modelCtx, modelSpan := opts.Tracer.Start(turnCtx, "memaxagent.model.stream",
				telemetry.String("memax.session_id", sessionID),
				telemetry.Int("memax.turn", turn),
				telemetry.Int("memax.model.messages", len(messages)),
				telemetry.Int("memax.model.tools", len(toolSpecs)),
			)
			stream, err := opts.Model.Stream(modelCtx, model.Request{
				SessionID:          sessionID,
				Messages:           messages,
				Tools:              toolSpecs,
				SystemPrompt:       opts.SystemPrompt,
				AppendSystemPrompt: opts.AppendSystemPrompt,
			})
			if err != nil {
				err = fmt.Errorf("stream model: %w", err)
				modelSpan.RecordError(err)
				modelSpan.End()
				turnSpan.RecordError(err)
				emitError(turnCtx, emit, sessionID, turn, err)
				shouldStop = true
				return
			}

			assistant, uses, err := collectAssistant(modelCtx, emit, stream, sessionID, turn)
			if closeErr := stream.Close(); closeErr != nil && err == nil {
				err = closeErr
			}
			modelSpan.Set(
				telemetry.Int("memax.model.tool_uses", len(uses)),
				telemetry.Int("memax.model.assistant_blocks", len(assistant.Content)),
			)
			if err != nil {
				modelSpan.RecordError(err)
				modelSpan.End()
				turnSpan.RecordError(err)
				emitError(turnCtx, emit, sessionID, turn, err)
				shouldStop = true
				return
			}
			modelSpan.End()
			if len(assistant.Content) > 0 {
				if err := opts.Sessions.Append(turnCtx, sessionID, assistant); err != nil {
					err = fmt.Errorf("append assistant message: %w", err)
					turnSpan.RecordError(err)
					emitError(turnCtx, emit, sessionID, turn, err)
					shouldStop = true
					return
				}
			}
			if len(uses) == 0 {
				result := assistant.PlainText()
				event := newEvent(EventResult, sessionID, turn)
				event.Result = result
				emit(event)
				shouldStop = true
				return
			}

			results := executor.Run(turnCtx, uses)
			for result := range results {
				event := newEvent(EventToolResult, sessionID, turn)
				event.ToolResult = &result
				if !emit(event) {
					shouldStop = true
					return
				}
				if err := opts.Sessions.Append(turnCtx, sessionID, model.Message{
					Role: model.RoleTool,
					ToolResult: &model.ToolResult{
						ToolUseID: result.ToolUseID,
						Name:      result.Name,
						Content:   result.Content,
						IsError:   result.IsError,
						Metadata:  result.Metadata,
					},
				}); err != nil {
					err = fmt.Errorf("append tool result: %w", err)
					turnSpan.RecordError(err)
					emitError(turnCtx, emit, sessionID, turn, err)
					shouldStop = true
					return
				}
			}
		}()
		if shouldStop {
			return
		}
	}

	emitError(ctx, emit, sessionID, opts.MaxTurns, fmt.Errorf("max turns exceeded: %d", opts.MaxTurns))
}

func collectAssistant(
	ctx context.Context,
	emit func(Event) bool,
	stream model.Stream,
	sessionID string,
	turn int,
) (model.Message, []model.ToolUse, error) {
	var blocks []model.ContentBlock
	var uses []model.ToolUse

	for {
		event, err := stream.Recv()
		if errors.Is(err, model.ErrEndOfStream) {
			return model.Message{Role: model.RoleAssistant, Content: blocks}, uses, nil
		}
		if err != nil {
			return model.Message{}, nil, fmt.Errorf("receive model event: %w", err)
		}

		switch event.Kind {
		case model.StreamText:
			if strings.TrimSpace(event.Text) != "" {
				block := model.ContentBlock{Type: model.ContentText, Text: event.Text}
				blocks = append(blocks, block)
				out := newEvent(EventAssistant, sessionID, turn)
				out.Message = &model.Message{Role: model.RoleAssistant, Content: []model.ContentBlock{block}}
				if !emit(out) {
					return model.Message{}, nil, ctx.Err()
				}
			}
		case model.StreamToolUse:
			uses = append(uses, event.ToolUse)
			blocks = append(blocks, model.ContentBlock{Type: model.ContentToolUse, ToolUse: &event.ToolUse})
			out := newEvent(EventToolUse, sessionID, turn)
			out.ToolUse = &event.ToolUse
			if !emit(out) {
				return model.Message{}, nil, ctx.Err()
			}
		}
	}
}

func emitError(_ context.Context, emit func(Event) bool, sessionID string, turn int, err error) {
	event := newEvent(EventError, sessionID, turn)
	event.Err = err
	emit(event)
}

// Drain consumes a query event stream and returns the final result or error.
func Drain(events <-chan Event) (string, error) {
	for event := range events {
		switch event.Kind {
		case EventResult:
			return event.Result, nil
		case EventError:
			return "", event.Err
		}
	}
	return "", nil
}
