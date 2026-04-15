package memaxagent

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/MemaxLabs/memax-go-agent-sdk/model"
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

	sess, err := opts.Sessions.Create(ctx)
	if err != nil {
		return nil, fmt.Errorf("create session: %w", err)
	}
	if err := opts.Sessions.Append(ctx, sess.ID, model.Message{
		Role:    model.RoleUser,
		Content: []model.ContentBlock{{Type: model.ContentText, Text: prompt}},
	}); err != nil {
		return nil, fmt.Errorf("append user prompt: %w", err)
	}

	events := make(chan Event)
	go func() {
		defer close(events)
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
		messages, err := opts.Sessions.Messages(ctx, sessionID)
		if err != nil {
			emitError(ctx, emit, sessionID, turn, fmt.Errorf("load session messages: %w", err))
			return
		}
		if opts.Context != nil {
			originalCount := len(messages)
			messages, err = opts.Context.Apply(ctx, messages)
			if err != nil {
				emitError(ctx, emit, sessionID, turn, fmt.Errorf("apply context policy: %w", err))
				return
			}
			if len(messages) != originalCount {
				event := newEvent(EventContextApplied, sessionID, turn)
				event.Context = &ContextEvent{
					OriginalMessages: originalCount,
					SentMessages:     len(messages),
				}
				if !emit(event) {
					return
				}
			}
		}

		if !emit(newEvent(EventModelRequest, sessionID, turn)) {
			return
		}

		stream, err := opts.Model.Stream(ctx, model.Request{
			SessionID:          sessionID,
			Messages:           messages,
			Tools:              opts.Tools.Specs(),
			SystemPrompt:       opts.SystemPrompt,
			AppendSystemPrompt: opts.AppendSystemPrompt,
		})
		if err != nil {
			emitError(ctx, emit, sessionID, turn, fmt.Errorf("stream model: %w", err))
			return
		}

		assistant, uses, err := collectAssistant(ctx, emit, stream, sessionID, turn)
		if closeErr := stream.Close(); closeErr != nil && err == nil {
			err = closeErr
		}
		if err != nil {
			emitError(ctx, emit, sessionID, turn, err)
			return
		}
		if len(assistant.Content) > 0 {
			if err := opts.Sessions.Append(ctx, sessionID, assistant); err != nil {
				emitError(ctx, emit, sessionID, turn, fmt.Errorf("append assistant message: %w", err))
				return
			}
		}
		if len(uses) == 0 {
			result := assistant.PlainText()
			event := newEvent(EventResult, sessionID, turn)
			event.Result = result
			emit(event)
			return
		}

		results := executor.Run(ctx, uses)
		for result := range results {
			event := newEvent(EventToolResult, sessionID, turn)
			event.ToolResult = &result
			if !emit(event) {
				return
			}
			if err := opts.Sessions.Append(ctx, sessionID, model.Message{
				Role: model.RoleTool,
				ToolResult: &model.ToolResult{
					ToolUseID: result.ToolUseID,
					Name:      result.Name,
					Content:   result.Content,
					IsError:   result.IsError,
					Metadata:  result.Metadata,
				},
			}); err != nil {
				emitError(ctx, emit, sessionID, turn, fmt.Errorf("append tool result: %w", err))
				return
			}
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
