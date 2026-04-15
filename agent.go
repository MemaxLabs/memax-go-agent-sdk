package memaxagent

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/MemaxLabs/memax-go-agent-sdk/hook"
	"github.com/MemaxLabs/memax-go-agent-sdk/memory"
	"github.com/MemaxLabs/memax-go-agent-sdk/model"
	"github.com/MemaxLabs/memax-go-agent-sdk/output"
	promptpkg "github.com/MemaxLabs/memax-go-agent-sdk/prompt"
	"github.com/MemaxLabs/memax-go-agent-sdk/session"
	skillpkg "github.com/MemaxLabs/memax-go-agent-sdk/skill"
	"github.com/MemaxLabs/memax-go-agent-sdk/telemetry"
	"github.com/MemaxLabs/memax-go-agent-sdk/tool"
)

var ErrMissingModelClient = errors.New("memaxagent: model client is required")

const (
	maxMemoryQueryMessages = 3
	maxMemoryQueryBytes    = 4096
)

// Query runs an autonomous agent loop for a single prompt and streams events.
func Query(ctx context.Context, prompt string, opts Options) (<-chan Event, error) {
	opts = opts.withDefaults()
	if opts.Model == nil {
		return nil, ErrMissingModelClient
	}
	outputValidator, err := opts.Output.Compile(ctx)
	if err != nil {
		return nil, fmt.Errorf("compile output contract: %w", err)
	}
	var cancel context.CancelFunc
	if opts.MaxRunDuration > 0 {
		ctx, cancel = context.WithTimeout(ctx, opts.MaxRunDuration)
	}
	ctx, querySpan := opts.Tracer.Start(ctx, "memaxagent.query",
		telemetry.Int("memax.max_turns", opts.MaxTurns),
		telemetry.Int("memax.max_tool_concurrency", opts.MaxToolConcurrency),
	)
	opts.Meter.Add(ctx, "memax.query.started", 1)

	sess, err := startSession(ctx, opts)
	if err != nil {
		if cancel != nil {
			cancel()
		}
		err = fmt.Errorf("start session: %w", err)
		querySpan.RecordError(err)
		querySpan.End()
		return nil, err
	}
	if opts.ParentSessionID == "" {
		opts.ParentSessionID = sess.ParentID
	}
	querySpan.Set(telemetry.String("memax.session_id", sess.ID))
	if errs := opts.Hooks.SessionStarted(ctx, hook.SessionStartedInput{SessionID: sess.ID}); len(errs) > 0 {
		if cancel != nil {
			cancel()
		}
		err = fmt.Errorf("session started hook failed: %w", errors.Join(errs...))
		querySpan.RecordError(err)
		querySpan.End()
		return nil, err
	}
	promptResult, err := opts.Hooks.UserPrompt(ctx, hook.UserPromptInput{
		SessionID: sess.ID,
		Prompt:    prompt,
	})
	if err != nil {
		if cancel != nil {
			cancel()
		}
		err = fmt.Errorf("user prompt hook failed: %w", err)
		querySpan.RecordError(err)
		querySpan.End()
		return nil, err
	}
	if promptResult.DenyReason != "" {
		if cancel != nil {
			cancel()
		}
		err = fmt.Errorf("%s", promptResult.DenyReason)
		querySpan.RecordError(err)
		querySpan.End()
		return nil, err
	}
	if promptResult.Prompt != "" {
		prompt = promptResult.Prompt
	}
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
		runLoop(ctx, events, sess.ID, opts, outputValidator)
	}()

	return events, nil
}

// QueryAsync starts Query in a goroutine and reports startup failures as
// EventError values instead of returning them synchronously.
//
// This is useful in server handlers that need SDK startup work, such as session
// store access or user-prompt hooks, to run outside the caller goroutine.
func QueryAsync(ctx context.Context, prompt string, opts Options) <-chan Event {
	out := make(chan Event)
	go func() {
		defer close(out)
		events, err := Query(ctx, prompt, opts)
		if err != nil {
			event := newEvent(EventError, "", 0)
			event.Err = err
			select {
			case <-ctx.Done():
			case out <- event:
			}
			return
		}
		for event := range events {
			select {
			case <-ctx.Done():
				return
			case out <- event:
			}
		}
	}()
	return out
}

func startSession(ctx context.Context, opts Options) (session.Session, error) {
	if opts.SessionID != "" {
		return session.Get(ctx, opts.Sessions, opts.SessionID)
	}
	return session.Create(ctx, opts.Sessions, session.CreateOptions{ParentID: opts.ParentSessionID})
}

func runLoop(ctx context.Context, events chan<- Event, sessionID string, opts Options, outputValidator output.Validator) {
	memories := memoryLoader{opts: opts, sessionID: sessionID}
	executor := tool.Executor{
		Registry:       opts.Tools,
		Permissions:    opts.Permissions,
		Hooks:          opts.Hooks,
		MaxConcurrency: opts.MaxToolConcurrency,
		ResultStore:    opts.ResultStore,
		Runtime: tool.Runtime{
			SessionID:       sessionID,
			ParentSessionID: opts.ParentSessionID,
			Sessions:        opts.Sessions,
		},
		Tracer: opts.Tracer,
		Meter:  opts.Meter,
	}

	emit := func(event Event) bool {
		if event.ParentSessionID == "" {
			event.ParentSessionID = opts.ParentSessionID
		}
		select {
		case <-ctx.Done():
			return false
		case events <- event:
			return true
		}
	}

	finish := func(turn int, reason hook.StopReason, err error) error {
		finalErr := err
		if errs := opts.Hooks.Stop(ctx, hook.StopInput{
			SessionID: sessionID,
			Turn:      turn,
			Reason:    reason,
			Err:       err,
		}); len(errs) > 0 && finalErr == nil {
			opts.Meter.Add(ctx, "memax.hook.errors", int64(len(errs)),
				telemetry.String("memax.session_id", sessionID),
				telemetry.String("memax.hook", "stop"),
			)
			finalErr = fmt.Errorf("stop hook failed: %w", errors.Join(errs...))
		}
		if errs := opts.Hooks.SessionEnded(ctx, hook.SessionEndedInput{
			SessionID: sessionID,
			Reason:    reason,
			Err:       err,
		}); len(errs) > 0 && finalErr == nil {
			opts.Meter.Add(ctx, "memax.hook.errors", int64(len(errs)),
				telemetry.String("memax.session_id", sessionID),
				telemetry.String("memax.hook", "session_ended"),
			)
			finalErr = fmt.Errorf("session ended hook failed: %w", errors.Join(errs...))
		}
		attrs := []telemetry.Attribute{
			telemetry.String("memax.session_id", sessionID),
			telemetry.Int("memax.turn", turn),
			telemetry.String("memax.stop_reason", string(reason)),
			telemetry.Bool("memax.error", finalErr != nil),
		}
		opts.Meter.Add(ctx, "memax.query.completed", 1, attrs...)
		if finalErr != nil {
			opts.Meter.Add(ctx, "memax.query.errors", 1, attrs...)
		}
		return finalErr
	}

	if !emit(newEvent(EventSessionStarted, sessionID, 0)) {
		_ = finish(0, hook.StopReasonCanceled, ctx.Err())
		return
	}

	// Output repair attempts are scoped to the whole Query run so a contract
	// cannot consume unbounded turns by repeatedly producing invalid finals.
	outputRetries := 0
	var totalUsage model.Usage
	for turn := 1; turn <= opts.MaxTurns; turn++ {
		turnStarted := time.Now()
		turnCtx, turnSpan := opts.Tracer.Start(ctx, "memaxagent.turn",
			telemetry.String("memax.session_id", sessionID),
			telemetry.Int("memax.turn", turn),
		)
		opts.Meter.Add(turnCtx, "memax.turn.started", 1,
			telemetry.String("memax.session_id", sessionID),
			telemetry.Int("memax.turn", turn),
		)
		shouldStop := false
		func() {
			defer func() {
				opts.Meter.Record(turnCtx, "memax.turn.duration_ms", durationMilliseconds(time.Since(turnStarted)),
					telemetry.String("memax.session_id", sessionID),
					telemetry.Int("memax.turn", turn),
				)
				turnSpan.End()
			}()

			messages, err := opts.Sessions.Messages(turnCtx, sessionID)
			if err != nil {
				err = fmt.Errorf("load session messages: %w", err)
				turnSpan.RecordError(err)
				emitError(turnCtx, emit, sessionID, turn, err)
				_ = finish(turn, hook.StopReasonError, err)
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
					_ = finish(turn, hook.StopReasonError, err)
					shouldStop = true
					return
				}
				contextSpan.Set(telemetry.Int("memax.context.sent_messages", len(messages)))
				contextSpan.End()
				if !reflect.DeepEqual(messages, originalMessages) {
					if ok, applyErr := emitContextApplied(turnCtx, emit, opts, sessionID, turn, originalCount, len(messages)); applyErr != nil {
						err = applyErr
						turnSpan.RecordError(err)
						emitError(turnCtx, emit, sessionID, turn, err)
						_ = finish(turn, hook.StopReasonError, err)
						shouldStop = true
						return
					} else if !ok {
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

			toolSpecs, err := selectedToolSpecs(turnCtx, opts, messages)
			if err != nil {
				err = fmt.Errorf("select tools: %w", err)
				turnSpan.RecordError(err)
				emitError(turnCtx, emit, sessionID, turn, err)
				_ = finish(turn, hook.StopReasonError, err)
				shouldStop = true
				return
			}
			turnSpan.Set(
				telemetry.Int("memax.tools.available", len(opts.Tools.Specs())),
				telemetry.Int("memax.tools.selected", len(toolSpecs)),
			)
			promptResult, err := buildPrompt(turnCtx, opts, &memories, messages, toolSpecs)
			if err != nil {
				err = fmt.Errorf("build prompt: %w", err)
				turnSpan.RecordError(err)
				emitError(turnCtx, emit, sessionID, turn, err)
				_ = finish(turn, hook.StopReasonError, err)
				shouldStop = true
				return
			}
			if promptResult.Hash != "" {
				turnSpan.Set(
					telemetry.String("memax.prompt.hash", promptResult.Hash),
					telemetry.Int("memax.prompt.parts", len(promptResult.Parts)),
				)
			}
			modelCtx, modelSpan := opts.Tracer.Start(turnCtx, "memaxagent.model.stream",
				telemetry.String("memax.session_id", sessionID),
				telemetry.Int("memax.turn", turn),
				telemetry.Int("memax.model.messages", len(messages)),
				telemetry.Int("memax.model.tools", len(toolSpecs)),
			)
			modelStarted := time.Now()
			opts.Meter.Add(modelCtx, "memax.model.stream.started", 1,
				telemetry.String("memax.session_id", sessionID),
				telemetry.Int("memax.turn", turn),
				telemetry.Int("memax.model.messages", len(messages)),
				telemetry.Int("memax.model.tools", len(toolSpecs)),
			)
			stream, err := opts.Model.Stream(modelCtx, model.Request{
				SessionID:          sessionID,
				Messages:           messages,
				Tools:              toolSpecs,
				SystemPrompt:       promptResult.SystemPrompt,
				AppendSystemPrompt: promptResult.AppendSystemPrompt,
				ParentSessionID:    opts.ParentSessionID,
			})
			if err != nil {
				if opts.ContextRetry != nil && model.IsContextWindowExceeded(err) {
					retryMessages, retryTools, retryPrompt, retryErr := retryContextWindow(modelCtx, opts, &memories, messages)
					if retryErr == nil {
						if !reflect.DeepEqual(retryMessages, messages) {
							if ok, applyErr := emitContextApplied(turnCtx, emit, opts, sessionID, turn, len(messages), len(retryMessages)); applyErr != nil {
								err = applyErr
								modelSpan.RecordError(err)
								modelSpan.End()
								turnSpan.RecordError(err)
								emitError(turnCtx, emit, sessionID, turn, err)
								_ = finish(turn, hook.StopReasonError, err)
								shouldStop = true
								return
							} else if !ok {
								modelSpan.End()
								shouldStop = true
								return
							}
						}
						messages = retryMessages
						toolSpecs = retryTools
						promptResult = retryPrompt
						modelSpan.Set(telemetry.Int("memax.model.retry_tools", len(toolSpecs)))
						stream, err = opts.Model.Stream(modelCtx, model.Request{
							SessionID:          sessionID,
							Messages:           messages,
							Tools:              toolSpecs,
							SystemPrompt:       promptResult.SystemPrompt,
							AppendSystemPrompt: promptResult.AppendSystemPrompt,
							ParentSessionID:    opts.ParentSessionID,
						})
					}
					if retryErr != nil {
						err = errors.Join(err, fmt.Errorf("context retry failed: %w", retryErr))
					}
				}
			}
			if err != nil {
				err = fmt.Errorf("stream model: %w", err)
				modelSpan.RecordError(err)
				modelSpan.End()
				opts.Meter.Add(modelCtx, "memax.model.stream.errors", 1,
					telemetry.String("memax.session_id", sessionID),
					telemetry.Int("memax.turn", turn),
				)
				opts.Meter.Record(modelCtx, "memax.model.stream.duration_ms", durationMilliseconds(time.Since(modelStarted)),
					telemetry.String("memax.session_id", sessionID),
					telemetry.Int("memax.turn", turn),
				)
				turnSpan.RecordError(err)
				emitError(turnCtx, emit, sessionID, turn, err)
				_ = finish(turn, hook.StopReasonError, err)
				shouldStop = true
				return
			}

			assistant, uses, usage, err := collectAssistant(modelCtx, emit, stream, sessionID, turn, opts.Meter)
			totalUsage = totalUsage.Add(usage)
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
				opts.Meter.Add(modelCtx, "memax.model.stream.errors", 1,
					telemetry.String("memax.session_id", sessionID),
					telemetry.Int("memax.turn", turn),
				)
				opts.Meter.Record(modelCtx, "memax.model.stream.duration_ms", durationMilliseconds(time.Since(modelStarted)),
					telemetry.String("memax.session_id", sessionID),
					telemetry.Int("memax.turn", turn),
				)
				turnSpan.RecordError(err)
				emitError(turnCtx, emit, sessionID, turn, err)
				_ = finish(turn, hook.StopReasonError, err)
				shouldStop = true
				return
			}
			modelSpan.End()
			opts.Meter.Record(modelCtx, "memax.model.stream.duration_ms", durationMilliseconds(time.Since(modelStarted)),
				telemetry.String("memax.session_id", sessionID),
				telemetry.Int("memax.turn", turn),
				telemetry.Int("memax.model.tool_uses", len(uses)),
				telemetry.Int("memax.model.assistant_blocks", len(assistant.Content)),
			)
			if len(assistant.Content) > 0 {
				if err := opts.Sessions.Append(turnCtx, sessionID, assistant); err != nil {
					err = fmt.Errorf("append assistant message: %w", err)
					turnSpan.RecordError(err)
					emitError(turnCtx, emit, sessionID, turn, err)
					_ = finish(turn, hook.StopReasonError, err)
					shouldStop = true
					return
				}
			}
			if len(uses) == 0 {
				result := assistant.PlainText()
				if err := outputValidator.Validate(turnCtx, result); err != nil {
					if outputRetries < outputValidator.RetryLimit() {
						outputRetries++
						if err := appendOutputRetryPrompt(turnCtx, opts.Sessions, sessionID, err); err != nil {
							err = fmt.Errorf("append output validation retry prompt: %w", err)
							turnSpan.RecordError(err)
							emitError(turnCtx, emit, sessionID, turn, err)
							_ = finish(turn, hook.StopReasonError, err)
							shouldStop = true
							return
						}
						opts.Meter.Add(turnCtx, "memax.output.validation_retries", 1,
							telemetry.String("memax.session_id", sessionID),
							telemetry.Int("memax.turn", turn),
						)
						return
					}
					err = fmt.Errorf("validate structured output: %w", err)
					turnSpan.RecordError(err)
					emitError(turnCtx, emit, sessionID, turn, err)
					_ = finish(turn, hook.StopReasonError, err)
					shouldStop = true
					return
				}
				if err := finish(turn, hook.StopReasonResult, nil); err != nil {
					turnSpan.RecordError(err)
					emitError(turnCtx, emit, sessionID, turn, err)
					shouldStop = true
					return
				}
				event := newEvent(EventResult, sessionID, turn)
				event.Result = result
				if hasUsage(totalUsage) {
					usage := totalUsage
					event.Usage = &usage
				}
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
					_ = finish(turn, hook.StopReasonError, err)
					shouldStop = true
					return
				}
			}
		}()
		if shouldStop {
			return
		}
	}

	err := fmt.Errorf("max turns exceeded: %d", opts.MaxTurns)
	emitError(ctx, emit, sessionID, opts.MaxTurns, err)
	_ = finish(opts.MaxTurns, hook.StopReasonMaxTurns, err)
}

func selectedToolSpecs(ctx context.Context, opts Options, messages []model.Message) ([]model.ToolSpec, error) {
	if opts.ToolSelector == nil {
		return opts.Tools.Specs(), nil
	}
	return opts.ToolSelector.Select(ctx, opts.Tools, tool.SelectRequest{Messages: messages})
}

func appendOutputRetryPrompt(ctx context.Context, store session.Store, sessionID string, err error) error {
	message := outputRetryPrompt(err)
	return store.Append(ctx, sessionID, model.Message{
		Role:    model.RoleUser,
		Content: []model.ContentBlock{{Type: model.ContentText, Text: message}},
	})
}

func outputRetryPrompt(err error) string {
	detail := "unknown validation error"
	if err != nil {
		detail = err.Error()
	}
	return "Your previous final answer did not satisfy the required structured output contract: " + detail + "\nReturn only valid JSON that satisfies the schema. Do not include markdown fences, prose, or any text outside the JSON value."
}

type builtPrompt struct {
	SystemPrompt       string
	AppendSystemPrompt string
	Hash               string
	Parts              []promptpkg.Part
}

func buildPrompt(ctx context.Context, opts Options, memories *memoryLoader, messages []model.Message, tools []model.ToolSpec) (builtPrompt, error) {
	if opts.PromptBuilder == nil && opts.PromptProfile == "" && opts.Identity.IsZero() && !opts.Output.Enabled() && opts.MemorySource == nil && len(opts.Memories) == 0 && opts.SkillSource == nil && len(opts.Skills) == 0 {
		return builtPrompt{
			SystemPrompt:       opts.SystemPrompt,
			AppendSystemPrompt: opts.AppendSystemPrompt,
		}, nil
	}
	builder := opts.PromptBuilder
	if builder == nil {
		builder = promptpkg.DefaultBuilder{Profile: opts.PromptProfile}
	}
	loadedMemories, err := memories.Load(ctx, messages)
	if err != nil {
		return builtPrompt{}, err
	}
	skills := append([]skillpkg.Skill(nil), opts.Skills...)
	if opts.SkillSource != nil {
		loaded, err := opts.SkillSource.Skills(ctx)
		if err != nil {
			return builtPrompt{}, err
		}
		skills = append(skills, loaded...)
	}
	result, err := builder.Build(ctx, promptpkg.Request{
		Identity:           opts.Identity,
		SystemPrompt:       opts.SystemPrompt,
		AppendSystemPrompt: opts.AppendSystemPrompt,
		Messages:           messages,
		Tools:              tools,
		Memories:           loadedMemories,
		Skills:             skills,
		OutputSchema:       opts.Output.Schema,
	})
	if err != nil {
		return builtPrompt{}, err
	}
	return builtPrompt{
		SystemPrompt: result.SystemPrompt,
		Hash:         result.Hash,
		Parts:        result.Parts,
	}, nil
}

type memoryLoader struct {
	opts      Options
	sessionID string
	loaded    bool
	memories  []memory.Memory
}

func (l *memoryLoader) Load(ctx context.Context, messages []model.Message) ([]memory.Memory, error) {
	if l == nil || l.opts.MemorySource == nil && len(l.opts.Memories) == 0 {
		return nil, nil
	}
	if l.loaded {
		return memory.StaticSource(l.memories).Memories(ctx, memory.Request{})
	}
	opts := l.opts
	sources := make(memory.MultiSource, 0, 2)
	if len(opts.Memories) > 0 {
		sources = append(sources, memory.StaticSource(opts.Memories))
	}
	if opts.MemorySource != nil {
		sources = append(sources, opts.MemorySource)
	}
	loaded, err := sources.Memories(ctx, memory.Request{
		SessionID:       l.sessionID,
		ParentSessionID: opts.ParentSessionID,
		Identity:        opts.Identity,
		Messages:        messages,
		Query:           memoryQuery(messages),
	})
	if err != nil {
		return nil, err
	}
	l.memories = loaded
	l.loaded = true
	return memory.StaticSource(l.memories).Memories(ctx, memory.Request{})
}

func retryContextWindow(ctx context.Context, opts Options, memories *memoryLoader, messages []model.Message) ([]model.Message, []model.ToolSpec, builtPrompt, error) {
	retryMessages, err := opts.ContextRetry.Apply(ctx, messages)
	if err != nil {
		return nil, nil, builtPrompt{}, err
	}
	retryTools, err := selectedToolSpecs(ctx, opts, retryMessages)
	if err != nil {
		return nil, nil, builtPrompt{}, err
	}
	retryPrompt, err := buildPrompt(ctx, opts, memories, retryMessages, retryTools)
	if err != nil {
		return nil, nil, builtPrompt{}, err
	}
	return retryMessages, retryTools, retryPrompt, nil
}

func memoryQuery(messages []model.Message) string {
	if len(messages) == 0 {
		return ""
	}
	selected := make([]string, 0, maxMemoryQueryMessages)
	for i := len(messages) - 1; i >= 0 && len(selected) < maxMemoryQueryMessages; i-- {
		if messages[i].Role != model.RoleUser {
			continue
		}
		if text := strings.TrimSpace(messages[i].PlainText()); text != "" {
			selected = append(selected, text)
		}
	}
	var b strings.Builder
	for i := len(selected) - 1; i >= 0; i-- {
		if b.Len() > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(selected[i])
	}
	return limitStringBytes(b.String(), maxMemoryQueryBytes)
}

func limitStringBytes(value string, max int) string {
	if max <= 0 || len(value) <= max {
		return value
	}
	var b strings.Builder
	for _, r := range value {
		if b.Len()+len(string(r)) > max {
			break
		}
		b.WriteRune(r)
	}
	return b.String()
}

func emitContextApplied(
	ctx context.Context,
	emit func(Event) bool,
	opts Options,
	sessionID string,
	turn int,
	originalCount int,
	sentCount int,
) (bool, error) {
	event := newEvent(EventContextApplied, sessionID, turn)
	event.Context = &ContextEvent{
		OriginalMessages: originalCount,
		SentMessages:     sentCount,
	}
	if errs := opts.Hooks.ContextApplied(ctx, hook.ContextAppliedInput{
		SessionID:        sessionID,
		Turn:             turn,
		OriginalMessages: originalCount,
		SentMessages:     sentCount,
	}); len(errs) > 0 {
		opts.Meter.Add(ctx, "memax.hook.errors", int64(len(errs)),
			telemetry.String("memax.session_id", sessionID),
			telemetry.String("memax.hook", "context_applied"),
		)
		return true, fmt.Errorf("context applied hook failed: %w", errors.Join(errs...))
	}
	if !emit(event) {
		return false, nil
	}
	opts.Meter.Add(ctx, "memax.context.applied", 1,
		telemetry.String("memax.session_id", sessionID),
		telemetry.Int("memax.turn", turn),
		telemetry.Int("memax.context.original_messages", originalCount),
		telemetry.Int("memax.context.sent_messages", sentCount),
	)
	return true, nil
}

func collectAssistant(
	ctx context.Context,
	emit func(Event) bool,
	stream model.Stream,
	sessionID string,
	turn int,
	meter telemetry.Meter,
) (model.Message, []model.ToolUse, model.Usage, error) {
	var blocks []model.ContentBlock
	var uses []model.ToolUse
	var usage model.Usage

	for {
		event, err := stream.Recv()
		if errors.Is(err, model.ErrEndOfStream) {
			return model.Message{Role: model.RoleAssistant, Content: blocks}, uses, usage, nil
		}
		if err != nil {
			return model.Message{}, nil, model.Usage{}, fmt.Errorf("receive model event: %w", err)
		}

		switch event.Kind {
		case model.StreamText:
			if strings.TrimSpace(event.Text) != "" {
				block := model.ContentBlock{Type: model.ContentText, Text: event.Text}
				blocks = append(blocks, block)
				out := newEvent(EventAssistant, sessionID, turn)
				out.Message = &model.Message{Role: model.RoleAssistant, Content: []model.ContentBlock{block}}
				if !emit(out) {
					return model.Message{}, nil, model.Usage{}, ctx.Err()
				}
			}
		case model.StreamToolUse:
			uses = append(uses, event.ToolUse)
			blocks = append(blocks, model.ContentBlock{Type: model.ContentToolUse, ToolUse: &event.ToolUse})
			out := newEvent(EventToolUse, sessionID, turn)
			out.ToolUse = &event.ToolUse
			if !emit(out) {
				return model.Message{}, nil, model.Usage{}, ctx.Err()
			}
		case model.StreamUsage:
			if event.Usage == nil {
				continue
			}
			usage = usage.Add(*event.Usage)
			recordUsage(ctx, meter, sessionID, turn, *event.Usage)
			out := newEvent(EventUsage, sessionID, turn)
			current := *event.Usage
			out.Usage = &current
			if !emit(out) {
				return model.Message{}, nil, model.Usage{}, ctx.Err()
			}
		}
	}
}

func hasUsage(usage model.Usage) bool {
	return usage.InputTokens != 0 || usage.OutputTokens != 0 || usage.TotalTokens != 0 || usage.Provider != "" || usage.Model != "" || len(usage.Metadata) > 0
}

func recordUsage(ctx context.Context, meter telemetry.Meter, sessionID string, turn int, usage model.Usage) {
	if meter == nil {
		meter = telemetry.NoopMeter{}
	}
	attrs := []telemetry.Attribute{
		telemetry.String("memax.session_id", sessionID),
		telemetry.Int("memax.turn", turn),
	}
	if usage.Provider != "" {
		attrs = append(attrs, telemetry.String("memax.model.provider", usage.Provider))
	}
	if usage.Model != "" {
		attrs = append(attrs, telemetry.String("memax.model.name", usage.Model))
	}
	if usage.InputTokens != 0 {
		meter.Add(ctx, "memax.model.input_tokens", int64(usage.InputTokens), attrs...)
	}
	if usage.OutputTokens != 0 {
		meter.Add(ctx, "memax.model.output_tokens", int64(usage.OutputTokens), attrs...)
	}
	if usage.TotalTokens != 0 {
		meter.Add(ctx, "memax.model.total_tokens", int64(usage.TotalTokens), attrs...)
	}
}

func emitError(_ context.Context, emit func(Event) bool, sessionID string, turn int, err error) {
	event := newEvent(EventError, sessionID, turn)
	event.Err = err
	emit(event)
}

func durationMilliseconds(d time.Duration) float64 {
	return float64(d) / float64(time.Millisecond)
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
