package memaxagent

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/MemaxLabs/memax-go-agent-sdk/budget"
	"github.com/MemaxLabs/memax-go-agent-sdk/contextwindow"
	"github.com/MemaxLabs/memax-go-agent-sdk/hook"
	"github.com/MemaxLabs/memax-go-agent-sdk/internal/metadatavalues"
	"github.com/MemaxLabs/memax-go-agent-sdk/memory"
	"github.com/MemaxLabs/memax-go-agent-sdk/model"
	"github.com/MemaxLabs/memax-go-agent-sdk/output"
	"github.com/MemaxLabs/memax-go-agent-sdk/planner"
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
	skills := skillLoader{opts: opts}
	if opts.SkillDisclosure == skillpkg.DisclosureProgressive && (opts.SkillSource != nil || len(opts.Skills) > 0) {
		registry, err := registryWithSkillTools(opts.Tools, opts, &skills)
		if err != nil {
			emitError(ctx, func(event Event) bool {
				select {
				case <-ctx.Done():
					return false
				case events <- event:
					return true
				}
			}, sessionID, 0, fmt.Errorf("configure skill disclosure: %w", err))
			return
		}
		opts.Tools = registry
	}
	executor := tool.Executor{
		Registry:       opts.Tools,
		Permissions:    opts.Permissions,
		Hooks:          opts.Hooks,
		MaxConcurrency: opts.MaxToolConcurrency,
		ResultStore:    opts.ResultStore,
		Runtime: tool.Runtime{
			SessionID:       sessionID,
			ParentSessionID: opts.ParentSessionID,
			Identity:        opts.Identity,
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
	finalDenials := 0
	var totalUsage model.Usage
	budgetState := budgetTracker{startedAt: time.Now().UTC()}
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
		budgetState.turns = turn
		shouldStop := false
		func() {
			defer func() {
				opts.Meter.Record(turnCtx, "memax.turn.duration_ms", durationMilliseconds(time.Since(turnStarted)),
					telemetry.String("memax.session_id", sessionID),
					telemetry.Int("memax.turn", turn),
				)
				turnSpan.End()
			}()

			if err := checkBudget(turnCtx, opts, sessionID, turn, budgetState.snapshot()); err != nil {
				turnSpan.RecordError(err)
				emitError(turnCtx, emit, sessionID, turn, err)
				_ = finish(turn, hook.StopReasonBudget, err)
				shouldStop = true
				return
			}

			messages, err := opts.Sessions.Messages(turnCtx, sessionID)
			if err != nil {
				err = fmt.Errorf("load session messages: %w", err)
				turnSpan.RecordError(err)
				emitError(turnCtx, emit, sessionID, turn, err)
				_ = finish(turn, hook.StopReasonError, err)
				shouldStop = true
				return
			}
			durableMessages := model.CloneMessages(messages)
			if opts.Context != nil {
				originalMessages := messages
				originalCount := len(messages)
				contextCtx, contextSpan := opts.Tracer.Start(turnCtx, "memaxagent.context.apply",
					telemetry.String("memax.session_id", sessionID),
					telemetry.Int("memax.turn", turn),
					telemetry.Int("memax.context.original_messages", originalCount),
				)
				contextResult, err := applyContextPolicy(contextCtx, opts.Context, messages)
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
				messages = contextResult.Messages
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
				if contextResult.Compaction != nil {
					if !emitContextCompacted(turnCtx, emit, opts, sessionID, turn, contextResult.Compaction) {
						shouldStop = true
						return
					}
				}
			}
			turnSpan.Set(telemetry.Int("memax.model.messages", len(messages)))

			if err := checkBudget(turnCtx, opts, sessionID, turn, budgetState.withModelCalls(1)); err != nil {
				turnSpan.RecordError(err)
				emitError(turnCtx, emit, sessionID, turn, err)
				_ = finish(turn, hook.StopReasonBudget, err)
				shouldStop = true
				return
			}
			budgetState.modelCalls++
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
			promptResult, err := buildPrompt(turnCtx, opts, &memories, &skills, sessionID, messages, toolSpecs)
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
			if !emitSkillDiscovery(turnCtx, emit, opts, sessionID, turn, promptResult.SkillDiscovery) {
				shouldStop = true
				return
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
					retryMessages, retryTools, retryPrompt, retryCompaction, retryErr := retryContextWindow(modelCtx, opts, &memories, &skills, sessionID, messages)
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
						if retryCompaction != nil {
							if !emitContextCompacted(turnCtx, emit, opts, sessionID, turn, retryCompaction) {
								modelSpan.End()
								shouldStop = true
								return
							}
						}
						modelSpan.Set(telemetry.Int("memax.model.retry_tools", len(toolSpecs)))
						if !emitSkillDiscovery(turnCtx, emit, opts, sessionID, turn, promptResult.SkillDiscovery) {
							modelSpan.End()
							shouldStop = true
							return
						}
						if budgetErr := checkBudget(modelCtx, opts, sessionID, turn, budgetState.withModelCalls(1)); budgetErr != nil {
							err = budgetErr
							modelSpan.RecordError(err)
							modelSpan.End()
							turnSpan.RecordError(err)
							emitError(turnCtx, emit, sessionID, turn, err)
							_ = finish(turn, hook.StopReasonBudget, err)
							shouldStop = true
							return
						}
						budgetState.modelCalls++
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

			toolCtx, cancelEarlyTools := context.WithCancel(turnCtx)
			defer cancelEarlyTools()
			assistant, uses, earlyResults, usage, err := collectAssistant(modelCtx, emit, stream, sessionID, turn, opts.Meter, func(use model.ToolUse) (<-chan model.ToolResult, bool, error) {
				if !executor.CanRunConcurrently(use) {
					return nil, false, nil
				}
				// Early tool execution crosses the same budget boundary as the
				// post-stream batch below. Accounting happens here because the
				// tool may already be running while the model continues streaming.
				if err := checkBudget(turnCtx, opts, sessionID, turn, budgetState.withToolCalls(1)); err != nil {
					return nil, false, err
				}
				budgetState.toolCalls++
				return bufferedToolResults(executor.Run(toolCtx, []model.ToolUse{use})), true, nil
			})
			totalUsage = totalUsage.Add(usage)
			budgetState.usage = totalUsage
			if closeErr := stream.Close(); closeErr != nil && err == nil {
				err = closeErr
			}
			modelSpan.Set(
				telemetry.Int("memax.model.tool_uses", len(uses)),
				telemetry.Int("memax.model.assistant_blocks", len(assistant.Content)),
			)
			if err != nil {
				if len(uses) > 0 && len(assistant.Content) > 0 {
					if appendErr := opts.Sessions.Append(turnCtx, sessionID, assistant); appendErr != nil {
						appendErr = fmt.Errorf("append partial assistant message after stream error: %w", appendErr)
						modelSpan.RecordError(appendErr)
						modelSpan.End()
						turnSpan.RecordError(appendErr)
						emitError(turnCtx, emit, sessionID, turn, appendErr)
						_ = finish(turn, hook.StopReasonError, appendErr)
						shouldStop = true
						return
					}
				}
				cancelEarlyTools()
				if cleanupErr := emitCanceledEarlyToolResults(turnCtx, emit, opts.Sessions, sessionID, turn, uses, earlyResults, err); cleanupErr != nil {
					turnSpan.RecordError(cleanupErr)
					emitError(turnCtx, emit, sessionID, turn, cleanupErr)
					modelSpan.End()
					shouldStop = true
					return
				}
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
			if err := checkBudget(turnCtx, opts, sessionID, turn, budgetState.snapshot()); err != nil {
				turnSpan.RecordError(err)
				emitError(turnCtx, emit, sessionID, turn, err)
				_ = finish(turn, hook.StopReasonBudget, err)
				shouldStop = true
				return
			}
			if len(assistant.Content) > 0 {
				if err := opts.Sessions.Append(turnCtx, sessionID, assistant); err != nil {
					err = fmt.Errorf("append assistant message: %w", err)
					turnSpan.RecordError(err)
					emitError(turnCtx, emit, sessionID, turn, err)
					_ = finish(turn, hook.StopReasonError, err)
					shouldStop = true
					return
				}
				durableMessages = append(durableMessages, model.CloneMessage(assistant))
			}
			if len(uses) == 0 {
				cancelEarlyTools()
				result := assistant.PlainText()
				finalGate, err := opts.Hooks.BeforeFinal(turnCtx, hook.BeforeFinalInput{
					SessionID: sessionID,
					Turn:      turn,
					Answer:    result,
				})
				if err != nil {
					err = fmt.Errorf("before final hook: %w", err)
					turnSpan.RecordError(err)
					emitError(turnCtx, emit, sessionID, turn, err)
					_ = finish(turn, hook.StopReasonError, err)
					shouldStop = true
					return
				}
				if finalGate.DenyReason != "" {
					opts.Meter.Add(turnCtx, "memax.final.denials", 1,
						telemetry.String("memax.session_id", sessionID),
						telemetry.Int("memax.turn", turn),
					)
					if finalDenials >= finalDenialLimit(opts.MaxFinalDenials) {
						err := fmt.Errorf("finalization denied after %d retries: %s", finalDenials, strings.TrimSpace(finalGate.DenyReason))
						turnSpan.RecordError(err)
						emitError(turnCtx, emit, sessionID, turn, err)
						_ = finish(turn, hook.StopReasonPolicy, err)
						shouldStop = true
						return
					}
					finalDenials++
					if err := appendFinalRetryPrompt(turnCtx, opts.Sessions, sessionID, finalGate.DenyReason); err != nil {
						err = fmt.Errorf("append finalization retry prompt: %w", err)
						turnSpan.RecordError(err)
						emitError(turnCtx, emit, sessionID, turn, err)
						_ = finish(turn, hook.StopReasonError, err)
						shouldStop = true
						return
					}
					return
				}
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
				candidates, err := distillMemories(turnCtx, opts, sessionID, durableMessages, promptResult.Plan, result)
				if err != nil {
					err = fmt.Errorf("distill memories: %w", err)
					turnSpan.RecordError(err)
					emitError(turnCtx, emit, sessionID, turn, err)
					_ = finish(turn, hook.StopReasonError, err)
					shouldStop = true
					return
				}
				if len(candidates) > 0 {
					event := newEvent(EventMemoryCandidates, sessionID, turn)
					event.Memory = &MemoryCandidatesEvent{Candidates: candidates}
					if !emit(event) {
						shouldStop = true
						return
					}
					opts.Meter.Add(turnCtx, "memax.memory.candidates", int64(len(candidates)),
						telemetry.String("memax.session_id", sessionID),
						telemetry.Int("memax.turn", turn),
					)
					if err := handleMemoryCandidates(turnCtx, opts, sessionID, durableMessages, promptResult.Plan, result, candidates); err != nil {
						err = fmt.Errorf("handle memory candidates: %w", err)
						opts.Meter.Add(turnCtx, "memax.memory.candidate_handler.errors", 1,
							telemetry.String("memax.session_id", sessionID),
							telemetry.Int("memax.turn", turn),
						)
						turnSpan.RecordError(err)
						event := newEvent(EventMemoryCandidateHandlerError, sessionID, turn)
						event.Err = err
						if !emit(event) {
							shouldStop = true
							return
						}
					}
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

			remainingToolCalls := len(uses) - len(earlyResults)
			if err := checkBudget(turnCtx, opts, sessionID, turn, budgetState.withToolCalls(remainingToolCalls)); err != nil {
				cancelEarlyTools()
				turnSpan.RecordError(err)
				emitError(turnCtx, emit, sessionID, turn, err)
				_ = finish(turn, hook.StopReasonBudget, err)
				shouldStop = true
				return
			}
			budgetState.toolCalls += remainingToolCalls
			handleToolResult := func(result model.ToolResult) bool {
				event := newEvent(EventToolResult, sessionID, turn)
				event.ToolResult = &result
				if !emit(event) {
					shouldStop = true
					return false
				}
				if !emitSkillToolEvent(turnCtx, emit, opts, sessionID, turn, result) {
					shouldStop = true
					return false
				}
				if !emitWorkspaceToolEvent(turnCtx, emit, opts, sessionID, turn, result) {
					shouldStop = true
					return false
				}
				if !emitVerificationToolEvent(turnCtx, emit, opts, sessionID, turn, result) {
					shouldStop = true
					return false
				}
				if !emitApprovalToolEvent(turnCtx, emit, opts, sessionID, turn, result) {
					shouldStop = true
					return false
				}
				if !emitCommandToolEvent(turnCtx, emit, opts, sessionID, turn, result) {
					shouldStop = true
					return false
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
					return false
				}
				return true
			}
			for i := 0; i < len(uses); {
				if results, ok := earlyResults[i]; ok {
					for result := range results {
						if !handleToolResult(result) {
							cancelEarlyTools()
							return
						}
					}
					i++
					continue
				}
				start := i
				for i < len(uses) {
					if _, ok := earlyResults[i]; ok {
						break
					}
					i++
				}
				for result := range executor.Run(turnCtx, uses[start:i]) {
					if !handleToolResult(result) {
						cancelEarlyTools()
						return
					}
				}
			}
			cancelEarlyTools()
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

func appendFinalRetryPrompt(ctx context.Context, store session.Store, sessionID string, reason string) error {
	return store.Append(ctx, sessionID, model.Message{
		Role: model.RoleUser,
		Content: []model.ContentBlock{{
			Type: model.ContentText,
			Text: finalRetryPrompt(reason),
		}},
	})
}

func finalRetryPrompt(reason string) string {
	detail := strings.TrimSpace(reason)
	if detail == "" {
		detail = "the final answer is not ready"
	}
	return "Your previous final answer cannot be accepted yet: " + detail + "\nUse the available tools to satisfy this requirement, then provide the final answer."
}

func finalDenialLimit(maxFinalDenials int) int {
	if maxFinalDenials < 0 {
		return 0
	}
	if maxFinalDenials == 0 {
		return defaultMaxFinalDenials
	}
	return maxFinalDenials
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
	Plan               planner.Plan
	SkillDiscovery     *promptpkg.SkillDiscovery
}

type skillLoader struct {
	opts    Options
	mu      sync.Mutex
	loaded  bool
	skills  []skillpkg.Skill
	byName  map[string]skillpkg.Skill
	aliases map[string]skillpkg.Skill
	used    map[string]struct{}
}

func (l *skillLoader) Load(ctx context.Context) ([]skillpkg.Skill, error) {
	if l == nil || l.opts.SkillSource == nil && len(l.opts.Skills) == 0 {
		return nil, nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.loaded {
		return skillpkg.StaticSource(l.skills).Skills(ctx)
	}
	opts := l.opts
	sources := make(skillpkg.MultiSource, 0, 2)
	if len(opts.Skills) > 0 {
		sources = append(sources, skillpkg.StaticSource(opts.Skills))
	}
	if opts.SkillSource != nil {
		sources = append(sources, opts.SkillSource)
	}
	loaded, err := sources.Skills(ctx)
	if err != nil {
		return nil, err
	}
	l.skills = loaded
	l.byName, l.aliases = indexSkills(loaded)
	if l.used == nil {
		l.used = map[string]struct{}{}
	}
	l.loaded = true
	return skillpkg.StaticSource(l.skills).Skills(ctx)
}

func (l *skillLoader) Lookup(ctx context.Context, name string) (skillpkg.Skill, bool, error) {
	if _, err := l.Load(ctx); err != nil {
		return skillpkg.Skill{}, false, err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if item, ok := l.byName[name]; ok {
		return item, true, nil
	}
	if item, ok := l.aliases[name]; ok {
		return item, true, nil
	}
	return skillpkg.Skill{}, false, nil
}

func (l *skillLoader) MarkLoaded(name string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.used == nil {
		l.used = map[string]struct{}{}
	}
	l.used[name] = struct{}{}
}

func (l *skillLoader) Loaded(name string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	_, ok := l.used[name]
	return ok
}

func indexSkills(skills []skillpkg.Skill) (map[string]skillpkg.Skill, map[string]skillpkg.Skill) {
	byName := make(map[string]skillpkg.Skill, len(skills))
	aliases := make(map[string]skillpkg.Skill)
	for _, item := range skills {
		if item.Name == "" {
			continue
		}
		byName[item.Name] = item
		aliases["/"+item.Name] = item
	}
	return byName, aliases
}

func registryWithSkillTools(registry *tool.Registry, opts Options, loader *skillLoader) (*tool.Registry, error) {
	// load_skill closes over per-run state, so keep it on a per-run registry
	// snapshot instead of mutating a caller-owned registry that may be reused.
	out := registry.Clone()
	if _, ok := out.Get(skillpkg.LoadToolName); !ok {
		if err := out.Register(loadSkillTool(loader, opts.SkillResourceSource != nil)); err != nil {
			return nil, err
		}
	}
	if opts.SkillResourceSource != nil {
		if _, ok := out.Get(skillpkg.ResourceToolName); !ok {
			if err := out.Register(readSkillResourceTool(loader, opts.SkillResourceSource)); err != nil {
				return nil, err
			}
		}
	}
	return out, nil
}

type loadSkillInput struct {
	Name string `json:"name"`
}

func loadSkillTool(loader *skillLoader, resourcesAvailable bool) tool.Tool {
	return tool.Definition{
		ToolSpec: model.ToolSpec{
			Name:            skillpkg.LoadToolName,
			Description:     "Load full instructions for a named skill after deciding it is relevant.",
			InputSchema:     loadSkillInputSchema(),
			SearchHint:      "load skill instructions full content progressive disclosure",
			ReadOnly:        true,
			ConcurrencySafe: true,
			AlwaysLoad:      true,
			MaxResultBytes:  100 * 1024,
		},
		Handler: func(ctx context.Context, call tool.Call) (model.ToolResult, error) {
			input, err := tool.DecodeInput[loadSkillInput](call.Use)
			if err != nil {
				return model.ToolResult{}, err
			}
			name := strings.TrimSpace(input.Name)
			if name == "" {
				return model.ToolResult{}, fmt.Errorf("skill name is required")
			}
			item, ok, err := loader.Lookup(ctx, name)
			if err != nil {
				return model.ToolResult{}, err
			}
			if !ok {
				return model.ToolResult{}, fmt.Errorf("unknown skill: %s", name)
			}
			loader.MarkLoaded(item.Name)
			return model.ToolResult{
				Content: formatLoadedSkill(item, resourcesAvailable),
				Metadata: map[string]any{
					model.MetadataLoadedSkill:      true,
					model.MetadataContextRetention: model.RetentionImportant,
					"skill_name":                   item.Name,
					"source":                       item.Source,
					"path":                         item.Path,
					"tags":                         append([]string(nil), item.Tags...),
				},
			}, nil
		},
	}
}

func loadSkillInputSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []any{"name"},
		"properties": map[string]any{
			"name": map[string]any{
				"type":        "string",
				"description": "Exact skill name from the prompt metadata.",
			},
		},
	}
}

type readSkillResourceInput struct {
	SkillName string `json:"skill_name"`
	Resource  string `json:"resource"`
}

func readSkillResourceTool(loader *skillLoader, source skillpkg.ResourceSource) tool.Tool {
	return tool.Definition{
		ToolSpec: model.ToolSpec{
			Name:            skillpkg.ResourceToolName,
			Description:     "Load a supporting resource for a named skill after loading the skill instructions.",
			InputSchema:     readSkillResourceInputSchema(),
			SearchHint:      "load read skill supporting resource example checklist template",
			ReadOnly:        true,
			ConcurrencySafe: true,
			AlwaysLoad:      true,
			MaxResultBytes:  100 * 1024,
		},
		Handler: func(ctx context.Context, call tool.Call) (model.ToolResult, error) {
			input, err := tool.DecodeInput[readSkillResourceInput](call.Use)
			if err != nil {
				return model.ToolResult{}, err
			}
			skillName := strings.TrimSpace(input.SkillName)
			resourceName := strings.TrimSpace(input.Resource)
			if skillName == "" {
				return model.ToolResult{}, fmt.Errorf("skill_name is required")
			}
			if resourceName == "" {
				return model.ToolResult{}, fmt.Errorf("resource is required")
			}
			item, ok, err := loader.Lookup(ctx, skillName)
			if err != nil {
				return model.ToolResult{}, err
			}
			if !ok {
				return model.ToolResult{}, fmt.Errorf("unknown skill: %s", skillName)
			}
			skillLoaded := loader.Loaded(item.Name)
			ref, ok := findSkillResource(item, resourceName)
			if !ok {
				return model.ToolResult{}, fmt.Errorf("unknown resource %q for skill %s", resourceName, item.Name)
			}
			resource, err := source.SkillResource(ctx, skillpkg.ResourceRequest{
				SkillName: item.Name,
				Name:      ref.Name,
				Path:      ref.Path,
			})
			if err != nil {
				return model.ToolResult{}, err
			}
			resource = completeResource(resource, item, ref)
			metadata := model.CloneMetadata(resource.Metadata)
			if metadata == nil {
				metadata = map[string]any{}
			}
			metadata[model.MetadataLoadedSkillResource] = true
			metadata[model.MetadataContextRetention] = model.RetentionImportant
			metadata["skill_name"] = item.Name
			metadata["resource"] = resource.Name
			metadata["path"] = resource.Path
			metadata["mime_type"] = resource.MIMEType
			metadata["skill_loaded"] = skillLoaded
			return model.ToolResult{
				Content:  formatLoadedSkillResource(resource),
				Metadata: metadata,
			}, nil
		},
	}
}

func readSkillResourceInputSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []any{"skill_name", "resource"},
		"properties": map[string]any{
			"skill_name": map[string]any{
				"type":        "string",
				"description": "Exact skill name from the prompt metadata.",
			},
			"resource": map[string]any{
				"type":        "string",
				"description": "Exact resource name or path from the skill metadata.",
			},
		},
	}
}

func findSkillResource(item skillpkg.Skill, name string) (skillpkg.ResourceRef, bool) {
	for _, ref := range item.Resources {
		if ref.Name == name || ref.Path == name {
			return ref, true
		}
	}
	return skillpkg.ResourceRef{}, false
}

func completeResource(resource skillpkg.Resource, item skillpkg.Skill, ref skillpkg.ResourceRef) skillpkg.Resource {
	if resource.SkillName == "" {
		resource.SkillName = item.Name
	}
	if resource.Name == "" {
		resource.Name = ref.Name
	}
	if resource.Description == "" {
		resource.Description = ref.Description
	}
	if resource.Path == "" {
		resource.Path = ref.Path
	}
	if resource.MIMEType == "" {
		resource.MIMEType = ref.MIMEType
	}
	if resource.Bytes <= 0 {
		if ref.Bytes > 0 {
			resource.Bytes = ref.Bytes
		} else {
			resource.Bytes = len(resource.Content)
		}
	}
	return resource
}

func formatLoadedSkill(item skillpkg.Skill, resourcesAvailable bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Skill: %s", item.Name)
	if item.Description != "" {
		fmt.Fprintf(&b, "\nDescription: %s", item.Description)
	}
	if item.WhenToUse != "" {
		fmt.Fprintf(&b, "\nUse when: %s", item.WhenToUse)
	}
	if len(item.Tags) > 0 {
		fmt.Fprintf(&b, "\nTags: %s", strings.Join(item.Tags, ", "))
	}
	if item.Source != "" {
		fmt.Fprintf(&b, "\nSource: %s", item.Source)
	}
	if item.Path != "" {
		fmt.Fprintf(&b, "\nPath: %s", item.Path)
	}
	if resourcesAvailable && len(item.Resources) > 0 {
		fmt.Fprintf(&b, "\nResources: use `%s` with skill_name %q and the resource name or path when supporting material is needed.", skillpkg.ResourceToolName, item.Name)
		formatSkillResourceRefs(&b, item.Resources, "")
	}
	if item.Content != "" {
		fmt.Fprintf(&b, "\n\nInstructions:\n%s", item.Content)
	}
	return b.String()
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func formatSkillResourceRefs(b *strings.Builder, refs []skillpkg.ResourceRef, prefix string) {
	for _, ref := range refs {
		fmt.Fprintf(b, "\n%s- %s", prefix, firstNonEmptyString(ref.Name, ref.Path))
		if ref.Description != "" {
			fmt.Fprintf(b, ": %s", ref.Description)
		}
		if ref.Path != "" && ref.Path != ref.Name {
			fmt.Fprintf(b, " (path: %s)", ref.Path)
		}
		if ref.MIMEType != "" {
			fmt.Fprintf(b, " [%s]", ref.MIMEType)
		}
		if ref.Bytes > 0 {
			fmt.Fprintf(b, " [%d bytes]", ref.Bytes)
		}
		if len(ref.Tags) > 0 {
			fmt.Fprintf(b, "\n%s  Tags: %s", prefix, strings.Join(ref.Tags, ", "))
		}
	}
}

func formatLoadedSkillResource(resource skillpkg.Resource) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Skill resource: %s", resource.Name)
	if resource.SkillName != "" {
		fmt.Fprintf(&b, "\nSkill: %s", resource.SkillName)
	}
	if resource.Description != "" {
		fmt.Fprintf(&b, "\nDescription: %s", resource.Description)
	}
	if resource.Path != "" {
		fmt.Fprintf(&b, "\nPath: %s", resource.Path)
	}
	if resource.MIMEType != "" {
		fmt.Fprintf(&b, "\nMIME type: %s", resource.MIMEType)
	}
	if resource.Bytes > 0 {
		fmt.Fprintf(&b, "\nBytes: %d", resource.Bytes)
	}
	if resource.Content != "" {
		fmt.Fprintf(&b, "\n\nContent:\n%s", resource.Content)
	}
	return b.String()
}

func buildPrompt(ctx context.Context, opts Options, memories *memoryLoader, skills *skillLoader, sessionID string, messages []model.Message, tools []model.ToolSpec) (builtPrompt, error) {
	if opts.PromptBuilder == nil && opts.PromptProfile == "" && opts.Identity.IsZero() && opts.Planner == nil && !opts.Output.Enabled() && opts.MemorySource == nil && len(opts.Memories) == 0 && opts.SkillSource == nil && len(opts.Skills) == 0 {
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
	loadedSkills, err := skills.Load(ctx)
	if err != nil {
		return builtPrompt{}, err
	}
	plan, err := loadPlan(ctx, opts, sessionID, messages)
	if err != nil {
		return builtPrompt{}, err
	}
	result, err := builder.Build(ctx, promptpkg.Request{
		Identity:           opts.Identity,
		SystemPrompt:       opts.SystemPrompt,
		AppendSystemPrompt: opts.AppendSystemPrompt,
		Messages:           messages,
		Tools:              tools,
		Plan:               plan,
		Memories:           loadedMemories,
		Skills:             loadedSkills,
		SkillDisclosure:    opts.SkillDisclosure,
		SkillResources:     opts.SkillResourceSource != nil,
		OutputSchema:       opts.Output.Schema,
	})
	if err != nil {
		return builtPrompt{}, err
	}
	return builtPrompt{
		SystemPrompt:   result.SystemPrompt,
		Hash:           result.Hash,
		Parts:          result.Parts,
		Plan:           plan,
		SkillDiscovery: clonePromptSkillDiscovery(result.SkillDiscovery),
	}, nil
}

func clonePromptSkillDiscovery(in *promptpkg.SkillDiscovery) *promptpkg.SkillDiscovery {
	if in == nil {
		return nil
	}
	out := *in
	out.SelectedSkills = append([]string(nil), in.SelectedSkills...)
	return &out
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

func retryContextWindow(ctx context.Context, opts Options, memories *memoryLoader, skills *skillLoader, sessionID string, messages []model.Message) ([]model.Message, []model.ToolSpec, builtPrompt, *contextwindow.CompactionRecord, error) {
	result, err := applyContextPolicy(ctx, opts.ContextRetry, messages)
	if err != nil {
		return nil, nil, builtPrompt{}, nil, err
	}
	retryMessages := result.Messages
	retryTools, err := selectedToolSpecs(ctx, opts, retryMessages)
	if err != nil {
		return nil, nil, builtPrompt{}, nil, err
	}
	retryPrompt, err := buildPrompt(ctx, opts, memories, skills, sessionID, retryMessages, retryTools)
	if err != nil {
		return nil, nil, builtPrompt{}, nil, err
	}
	return retryMessages, retryTools, retryPrompt, result.Compaction, nil
}

func applyContextPolicy(ctx context.Context, policy contextwindow.Policy, messages []model.Message) (contextwindow.PolicyResult, error) {
	if policy == nil {
		return contextwindow.PolicyResult{Messages: model.CloneMessages(messages)}, nil
	}
	if richer, ok := policy.(contextwindow.PolicyWithResult); ok {
		return richer.ApplyWithResult(ctx, messages)
	}
	out, err := policy.Apply(ctx, messages)
	if err != nil {
		return contextwindow.PolicyResult{}, err
	}
	return contextwindow.PolicyResult{Messages: out}, nil
}

func loadPlan(ctx context.Context, opts Options, sessionID string, messages []model.Message) (planner.Plan, error) {
	if opts.Planner == nil {
		return planner.Plan{}, nil
	}
	return opts.Planner.Prepare(ctx, planner.Request{
		SessionID:       sessionID,
		ParentSessionID: opts.ParentSessionID,
		Identity:        opts.Identity,
		Messages:        messages,
		Query:           memoryQuery(messages),
	})
}

func distillMemories(ctx context.Context, opts Options, sessionID string, messages []model.Message, plan planner.Plan, result string) ([]memory.Candidate, error) {
	if opts.MemoryDistiller == nil {
		return nil, nil
	}
	candidates, err := opts.MemoryDistiller.Distill(ctx, memory.DistillRequest{
		SessionID:       sessionID,
		ParentSessionID: opts.ParentSessionID,
		Identity:        opts.Identity,
		Messages:        messages,
		Plan:            plan,
		Result:          result,
	})
	if err != nil {
		return nil, err
	}
	return memory.CloneCandidates(candidates), nil
}

func handleMemoryCandidates(ctx context.Context, opts Options, sessionID string, messages []model.Message, plan planner.Plan, result string, candidates []memory.Candidate) error {
	if opts.MemoryCandidateHandler == nil || len(candidates) == 0 {
		return nil
	}
	return opts.MemoryCandidateHandler.HandleCandidates(ctx, memory.CandidateRequest{
		SessionID:       sessionID,
		ParentSessionID: opts.ParentSessionID,
		Identity:        opts.Identity,
		Messages:        model.CloneMessages(messages),
		Plan:            plan,
		Result:          result,
		Candidates:      memory.CloneCandidates(candidates),
	})
}

func bufferedToolResults(results <-chan model.ToolResult) <-chan model.ToolResult {
	buffered := make(chan model.ToolResult, 1)
	go func() {
		defer close(buffered)
		for result := range results {
			buffered <- result
		}
	}()
	return buffered
}

func emitCanceledEarlyToolResults(
	ctx context.Context,
	emit func(Event) bool,
	store session.Store,
	sessionID string,
	turn int,
	uses []model.ToolUse,
	earlyResults map[int]<-chan model.ToolResult,
	reason error,
) error {
	if len(earlyResults) == 0 {
		return nil
	}
	indices := make([]int, 0, len(earlyResults))
	for index := range earlyResults {
		indices = append(indices, index)
	}
	sort.Ints(indices)
	for _, index := range indices {
		results := earlyResults[index]
		if index < 0 || index >= len(uses) {
			continue
		}
		use := uses[index]
		result := model.ToolResult{
			ToolUseID: use.ID,
			Name:      use.Name,
			Content:   "tool execution canceled because model streaming stopped",
			IsError:   true,
			Metadata: map[string]any{
				"streaming_status": "canceled",
				"streaming_reason": reason.Error(),
			},
		}
		select {
		case actual, ok := <-results:
			if ok {
				result = actual
			}
		case <-time.After(10 * time.Millisecond):
		}
		event := newEvent(EventToolResult, sessionID, turn)
		event.ToolResult = &result
		if !emit(event) {
			return ctx.Err()
		}
		if store != nil {
			if err := store.Append(ctx, sessionID, model.Message{
				Role: model.RoleTool,
				ToolResult: &model.ToolResult{
					ToolUseID: result.ToolUseID,
					Name:      result.Name,
					Content:   result.Content,
					IsError:   result.IsError,
					Metadata:  result.Metadata,
				},
			}); err != nil {
				return fmt.Errorf("append canceled early tool result: %w", err)
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
	}
	return nil
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

type budgetTracker struct {
	startedAt  time.Time
	turns      int
	modelCalls int
	toolCalls  int
	usage      model.Usage
}

func (t budgetTracker) snapshot() budget.Snapshot {
	now := time.Now().UTC()
	return budget.Snapshot{
		StartedAt:  t.startedAt,
		Now:        now,
		Elapsed:    now.Sub(t.startedAt),
		Turns:      t.turns,
		ModelCalls: t.modelCalls,
		ToolCalls:  t.toolCalls,
		Usage:      t.usage,
	}
}

func (t budgetTracker) withModelCalls(delta int) budget.Snapshot {
	t.modelCalls += delta
	return t.snapshot()
}

func (t budgetTracker) withToolCalls(delta int) budget.Snapshot {
	t.toolCalls += delta
	return t.snapshot()
}

func checkBudget(ctx context.Context, opts Options, sessionID string, turn int, snapshot budget.Snapshot) error {
	if opts.Budget == nil {
		return nil
	}
	decision := opts.Budget.Check(ctx, snapshot)
	if decision.Allow {
		return nil
	}
	reason := strings.TrimSpace(decision.Reason)
	if reason == "" {
		reason = "budget exceeded"
	}
	opts.Meter.Add(ctx, "memax.budget.exceeded", 1,
		telemetry.String("memax.session_id", sessionID),
		telemetry.Int("memax.turn", turn),
		telemetry.Int("memax.budget.turns", snapshot.Turns),
		telemetry.Int("memax.budget.model_calls", snapshot.ModelCalls),
		telemetry.Int("memax.budget.tool_calls", snapshot.ToolCalls),
		telemetry.Int("memax.budget.input_tokens", snapshot.Usage.InputTokens),
		telemetry.Int("memax.budget.output_tokens", snapshot.Usage.OutputTokens),
		telemetry.Int("memax.budget.total_tokens", snapshot.Usage.TotalTokens),
	)
	return errors.New(reason)
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

func emitContextCompacted(
	ctx context.Context,
	emit func(Event) bool,
	opts Options,
	sessionID string,
	turn int,
	record *contextwindow.CompactionRecord,
) bool {
	if record == nil {
		return true
	}
	event := newEvent(EventContextCompacted, sessionID, turn)
	copied := *record
	event.Compaction = &copied
	if !emit(event) {
		return false
	}
	opts.Meter.Add(ctx, "memax.context.compacted", 1,
		telemetry.String("memax.session_id", sessionID),
		telemetry.Int("memax.turn", turn),
		telemetry.String("memax.context.policy", record.Policy),
		telemetry.String("memax.context.reason", string(record.Reason)),
		telemetry.Int("memax.context.original_messages", record.OriginalMessages),
		telemetry.Int("memax.context.sent_messages", record.SentMessages),
		telemetry.Int("memax.context.summarized_messages", record.SummarizedMessages),
		telemetry.Int("memax.context.retained_messages", record.RetainedMessages),
	)
	return true
}

func emitSkillDiscovery(ctx context.Context, emit func(Event) bool, opts Options, sessionID string, turn int, discovery *promptpkg.SkillDiscovery) bool {
	if discovery == nil {
		return true
	}
	event := newEvent(EventSkillDiscovery, sessionID, turn)
	event.Skill = &SkillEvent{
		Action:         "discovery",
		SelectedSkills: append([]string(nil), discovery.SelectedSkills...),
		Selected:       discovery.Selected,
		Omitted:        discovery.Omitted,
		PromptBytes:    discovery.PromptBytes,
		MetadataOnly:   true,
	}
	if !emit(event) {
		return false
	}
	opts.Meter.Add(ctx, "memax.skill.discovery", 1,
		telemetry.String("memax.session_id", sessionID),
		telemetry.Int("memax.turn", turn),
		telemetry.Int("memax.skill.selected", discovery.Selected),
		telemetry.Int("memax.skill.omitted", discovery.Omitted),
		telemetry.Int("memax.skill.discovery_bytes", discovery.PromptBytes),
	)
	return true
}

func emitSkillToolEvent(ctx context.Context, emit func(Event) bool, opts Options, sessionID string, turn int, result model.ToolResult) bool {
	if result.Metadata == nil {
		return true
	}
	switch {
	case metadatavalues.Bool(result.Metadata, model.MetadataSkillSearch):
		event := newEvent(EventSkillSearch, sessionID, turn)
		event.Skill = &SkillEvent{
			Action:       "search",
			Query:        metadatavalues.String(result.Metadata, "query"),
			Matches:      metadatavalues.Int(result.Metadata, "matches"),
			MetadataOnly: metadatavalues.Bool(result.Metadata, "metadata_only"),
		}
		if !emit(event) {
			return false
		}
		opts.Meter.Add(ctx, "memax.skill.search", 1,
			telemetry.String("memax.session_id", sessionID),
			telemetry.Int("memax.turn", turn),
			telemetry.Int("memax.skill.matches", event.Skill.Matches),
		)
	case metadatavalues.Bool(result.Metadata, model.MetadataLoadedSkill):
		event := newEvent(EventSkillLoaded, sessionID, turn)
		event.Skill = &SkillEvent{
			Action:    "load",
			SkillName: metadatavalues.String(result.Metadata, "skill_name"),
		}
		if !emit(event) {
			return false
		}
		opts.Meter.Add(ctx, "memax.skill.loaded", 1,
			telemetry.String("memax.session_id", sessionID),
			telemetry.Int("memax.turn", turn),
			telemetry.String("memax.skill.name", event.Skill.SkillName),
		)
	case metadatavalues.Bool(result.Metadata, model.MetadataLoadedSkillResource):
		event := newEvent(EventSkillResourceLoaded, sessionID, turn)
		event.Skill = &SkillEvent{
			Action:       "resource_load",
			SkillName:    metadatavalues.String(result.Metadata, "skill_name"),
			ResourceName: metadatavalues.String(result.Metadata, "resource"),
		}
		if !emit(event) {
			return false
		}
		opts.Meter.Add(ctx, "memax.skill.resource_loaded", 1,
			telemetry.String("memax.session_id", sessionID),
			telemetry.Int("memax.turn", turn),
			telemetry.String("memax.skill.name", event.Skill.SkillName),
			telemetry.String("memax.skill.resource", event.Skill.ResourceName),
		)
	}
	return true
}

func emitWorkspaceToolEvent(ctx context.Context, emit func(Event) bool, opts Options, sessionID string, turn int, result model.ToolResult) bool {
	operation := metadatavalues.String(result.Metadata, model.MetadataWorkspaceOperation)
	if operation == "" {
		return true
	}
	workspaceEvent := &WorkspaceEvent{
		Operation:    operation,
		Paths:        metadataStrings(result.Metadata, model.MetadataWorkspacePaths),
		Changes:      metadatavalues.Int(result.Metadata, model.MetadataWorkspaceChanges),
		Added:        metadatavalues.Int(result.Metadata, model.MetadataWorkspaceAdded),
		Modified:     metadatavalues.Int(result.Metadata, model.MetadataWorkspaceModified),
		Deleted:      metadatavalues.Int(result.Metadata, model.MetadataWorkspaceDeleted),
		ByteDelta:    metadatavalues.Int(result.Metadata, model.MetadataWorkspaceByteDelta),
		CheckpointID: metadatavalues.String(result.Metadata, model.MetadataWorkspaceCheckpointID),
		BaseID:       metadatavalues.String(result.Metadata, model.MetadataWorkspaceBaseID),
	}
	var kind EventKind
	var meterName string
	switch operation {
	case "patch":
		kind = EventWorkspacePatch
		meterName = "memax.workspace.patch"
	case "diff":
		kind = EventWorkspaceDiff
		meterName = "memax.workspace.diff"
	case "checkpoint":
		kind = EventWorkspaceCheckpoint
		meterName = "memax.workspace.checkpoint"
	case "restore":
		kind = EventWorkspaceRestore
		meterName = "memax.workspace.restore"
	default:
		return true
	}
	event := newEvent(kind, sessionID, turn)
	event.Workspace = workspaceEvent
	if !emit(event) {
		return false
	}
	opts.Meter.Add(ctx, meterName, 1,
		telemetry.String("memax.session_id", sessionID),
		telemetry.Int("memax.turn", turn),
		telemetry.Int("memax.workspace.changes", workspaceEvent.Changes),
		telemetry.Int("memax.workspace.added", workspaceEvent.Added),
		telemetry.Int("memax.workspace.modified", workspaceEvent.Modified),
		telemetry.Int("memax.workspace.deleted", workspaceEvent.Deleted),
		telemetry.Int("memax.workspace.byte_delta", workspaceEvent.ByteDelta),
		telemetry.String("memax.workspace.checkpoint_id", workspaceEvent.CheckpointID),
	)
	return true
}

func emitVerificationToolEvent(ctx context.Context, emit func(Event) bool, opts Options, sessionID string, turn int, result model.ToolResult) bool {
	operation := metadatavalues.String(result.Metadata, model.MetadataVerificationOperation)
	if operation == "" {
		return true
	}
	verificationEvent := &VerificationEvent{
		Operation:   operation,
		Name:        metadatavalues.String(result.Metadata, model.MetadataVerificationName),
		Passed:      metadatavalues.Bool(result.Metadata, model.MetadataVerificationPassed),
		Diagnostics: metadatavalues.Int(result.Metadata, model.MetadataVerificationDiagnostics),
		Paths:       metadataStrings(result.Metadata, model.MetadataVerificationPaths),
	}
	switch operation {
	case "verify":
	default:
		return true
	}
	event := newEvent(EventVerification, sessionID, turn)
	event.Verification = verificationEvent
	if !emit(event) {
		return false
	}
	opts.Meter.Add(ctx, "memax.verification.run", 1,
		telemetry.String("memax.session_id", sessionID),
		telemetry.Int("memax.turn", turn),
		telemetry.String("memax.verification.name", verificationEvent.Name),
		telemetry.Bool("memax.verification.passed", verificationEvent.Passed),
		telemetry.Int("memax.verification.diagnostics", verificationEvent.Diagnostics),
	)
	return true
}

func emitApprovalToolEvent(ctx context.Context, emit func(Event) bool, opts Options, sessionID string, turn int, result model.ToolResult) bool {
	if result.Metadata == nil {
		return true
	}
	inputHash := metadatavalues.String(result.Metadata, model.MetadataApprovalInputHash)
	inputBound := inputHash != ""
	// Consumed-grant metadata is normally attached to the later approved tool
	// result, while request metadata is attached to the approval tool result.
	// Keep the checks independent so custom tools can emit both if needed.
	if metadatavalues.Bool(result.Metadata, model.MetadataApprovalConsumed) {
		approvalEvent := &ApprovalEvent{
			Action:     metadatavalues.String(result.Metadata, model.MetadataApprovalAction),
			InputHash:  inputHash,
			Consumed:   true,
			SingleUse:  metadatavalues.Bool(result.Metadata, model.MetadataApprovalSingleUse),
			InputBound: inputBound,
		}
		event := newEvent(EventApprovalConsumed, sessionID, turn)
		event.Approval = approvalEvent
		if !emit(event) {
			return false
		}
		opts.Meter.Add(ctx, "memax.approval.consumed", 1,
			telemetry.String("memax.session_id", sessionID),
			telemetry.Int("memax.turn", turn),
			telemetry.String("memax.approval.action", approvalEvent.Action),
			telemetry.Bool("memax.approval.single_use", approvalEvent.SingleUse),
			telemetry.Bool("memax.approval.input_bound", inputBound),
		)
	}
	if metadatavalues.String(result.Metadata, model.MetadataApprovalOperation) != "request" {
		return true
	}
	approvalEvent := ApprovalEvent{
		Action:     metadatavalues.String(result.Metadata, model.MetadataApprovalAction),
		Reason:     metadatavalues.String(result.Metadata, model.MetadataApprovalReason),
		InputHash:  inputHash,
		Summary:    approvalSummaryFromMetadata(result.Metadata),
		Approved:   metadatavalues.Bool(result.Metadata, model.MetadataApprovalApproved),
		InputBound: inputBound,
	}
	requested := newEvent(EventApprovalRequested, sessionID, turn)
	requestedPayload := approvalEvent
	requestedPayload.Requested = true
	requested.Approval = &requestedPayload
	if !emit(requested) {
		return false
	}
	opts.Meter.Add(ctx, "memax.approval.requests", 1,
		telemetry.String("memax.session_id", sessionID),
		telemetry.Int("memax.turn", turn),
		telemetry.String("memax.approval.action", approvalEvent.Action),
		telemetry.Bool("memax.approval.input_bound", inputBound),
	)
	kind := EventApprovalDenied
	meterName := "memax.approval.denials"
	if approvalEvent.Approved {
		kind = EventApprovalGranted
		meterName = "memax.approval.grants"
	}
	decision := newEvent(kind, sessionID, turn)
	decisionPayload := approvalEvent
	decisionPayload.Approved = approvalEvent.Approved
	decision.Approval = &decisionPayload
	if !emit(decision) {
		return false
	}
	opts.Meter.Add(ctx, meterName, 1,
		telemetry.String("memax.session_id", sessionID),
		telemetry.Int("memax.turn", turn),
		telemetry.String("memax.approval.action", approvalEvent.Action),
		telemetry.Bool("memax.approval.input_bound", inputBound),
	)
	return true
}

func approvalSummaryFromMetadata(metadata map[string]any) ApprovalSummaryEvent {
	return ApprovalSummaryEvent{
		Title:       metadatavalues.String(metadata, model.MetadataApprovalSummaryTitle),
		Description: metadatavalues.String(metadata, model.MetadataApprovalSummaryDescription),
		Risk:        metadatavalues.String(metadata, model.MetadataApprovalSummaryRisk),
		Paths:       metadataStrings(metadata, model.MetadataApprovalSummaryPaths),
		Changes:     metadatavalues.Int(metadata, model.MetadataApprovalSummaryChanges),
		Added:       metadatavalues.Int(metadata, model.MetadataApprovalSummaryAdded),
		Modified:    metadatavalues.Int(metadata, model.MetadataApprovalSummaryModified),
		Deleted:     metadatavalues.Int(metadata, model.MetadataApprovalSummaryDeleted),
		ByteDelta:   metadatavalues.Int(metadata, model.MetadataApprovalSummaryByteDelta),
	}
}

func emitCommandToolEvent(ctx context.Context, emit func(Event) bool, opts Options, sessionID string, turn int, result model.ToolResult) bool {
	operation := metadatavalues.String(result.Metadata, model.MetadataCommandOperation)
	if operation == "" {
		return true
	}
	commandEvent := &CommandEvent{
		Operation:       operation,
		CommandID:       metadatavalues.String(result.Metadata, model.MetadataCommandSessionID),
		Argv:            metadataStrings(result.Metadata, model.MetadataCommandArgv),
		CWD:             metadatavalues.String(result.Metadata, model.MetadataCommandCWD),
		Status:          metadatavalues.String(result.Metadata, model.MetadataCommandStatus),
		PID:             metadatavalues.Int(result.Metadata, model.MetadataCommandPID),
		TTY:             metadatavalues.Bool(result.Metadata, model.MetadataCommandTTY),
		Cols:            metadatavalues.Int(result.Metadata, model.MetadataCommandCols),
		Rows:            metadatavalues.Int(result.Metadata, model.MetadataCommandRows),
		InputBytes:      metadatavalues.Int(result.Metadata, model.MetadataCommandInputBytes),
		ExitCode:        metadatavalues.Int(result.Metadata, model.MetadataCommandExitCode),
		TimedOut:        metadatavalues.Bool(result.Metadata, model.MetadataCommandTimedOut),
		DurationMS:      metadatavalues.Int(result.Metadata, model.MetadataCommandDurationMS),
		StdoutBytes:     metadatavalues.Int(result.Metadata, model.MetadataCommandStdoutBytes),
		StderrBytes:     metadatavalues.Int(result.Metadata, model.MetadataCommandStderrBytes),
		OutputTruncated: metadatavalues.Bool(result.Metadata, model.MetadataCommandOutputTruncated),
		NextSeq:         metadatavalues.Int(result.Metadata, model.MetadataCommandNextSeq),
		OutputChunks:    metadatavalues.Int(result.Metadata, model.MetadataCommandOutputChunks),
		DroppedChunks:   metadatavalues.Int(result.Metadata, model.MetadataCommandDroppedChunks),
		DroppedBytes:    metadatavalues.Int(result.Metadata, model.MetadataCommandDroppedBytes),
	}
	kind := EventCommandFinished
	meterName := "memax.command.finished"
	switch operation {
	case "run":
	case "start":
		kind = EventCommandStarted
		meterName = "memax.command.started"
	case "write":
		kind = EventCommandInput
		meterName = "memax.command.input"
	case "read":
		kind = EventCommandOutput
		meterName = "memax.command.output"
	case "stop":
		kind = EventCommandStopped
		meterName = "memax.command.stopped"
	case "resize":
		kind = EventCommandResized
		meterName = "memax.command.resized"
	default:
		return true
	}
	event := newEvent(kind, sessionID, turn)
	event.Command = commandEvent
	if !emit(event) {
		return false
	}
	opts.Meter.Add(ctx, meterName, 1,
		telemetry.String("memax.session_id", sessionID),
		telemetry.Int("memax.turn", turn),
		telemetry.String("memax.command.operation", commandEvent.Operation),
		telemetry.Int("memax.command.exit_code", commandEvent.ExitCode),
		telemetry.Bool("memax.command.timed_out", commandEvent.TimedOut),
		telemetry.Bool("memax.command.output_truncated", commandEvent.OutputTruncated),
	)
	if operation == "run" {
		opts.Meter.Record(ctx, "memax.command.duration_ms", float64(commandEvent.DurationMS),
			telemetry.String("memax.session_id", sessionID),
			telemetry.Int("memax.turn", turn),
			telemetry.Int("memax.command.exit_code", commandEvent.ExitCode),
			telemetry.Bool("memax.command.timed_out", commandEvent.TimedOut),
		)
	}
	return true
}

func metadataStrings(metadata map[string]any, key string) []string {
	switch values := metadata[key].(type) {
	case []string:
		return append([]string(nil), values...)
	case []any:
		out := make([]string, 0, len(values))
		for _, value := range values {
			if str, ok := value.(string); ok {
				out = append(out, str)
			}
		}
		return out
	default:
		return nil
	}
}

// collectAssistant consumes a provider stream and returns the assistant message
// plus any complete tool uses. On receive or emit errors it returns the partial
// assistant state accumulated so far so callers can preserve tool-use/result
// pairing and clean up any early-started tool executions.
func collectAssistant(
	ctx context.Context,
	emit func(Event) bool,
	stream model.Stream,
	sessionID string,
	turn int,
	meter telemetry.Meter,
	startEarlyTool func(model.ToolUse) (<-chan model.ToolResult, bool, error),
) (model.Message, []model.ToolUse, map[int]<-chan model.ToolResult, model.Usage, error) {
	var blocks []model.ContentBlock
	var uses []model.ToolUse
	earlyResults := make(map[int]<-chan model.ToolResult)
	var usage model.Usage

	for {
		event, err := stream.Recv()
		if errors.Is(err, model.ErrEndOfStream) {
			return model.Message{Role: model.RoleAssistant, Content: blocks}, uses, earlyResults, usage, nil
		}
		if err != nil {
			return model.Message{Role: model.RoleAssistant, Content: blocks}, uses, earlyResults, usage, fmt.Errorf("receive model event: %w", err)
		}

		switch event.Kind {
		case model.StreamText:
			if strings.TrimSpace(event.Text) != "" {
				block := model.ContentBlock{Type: model.ContentText, Text: event.Text}
				blocks = append(blocks, block)
				out := newEvent(EventAssistant, sessionID, turn)
				out.Message = &model.Message{Role: model.RoleAssistant, Content: []model.ContentBlock{block}}
				if !emit(out) {
					return model.Message{Role: model.RoleAssistant, Content: blocks}, uses, earlyResults, usage, ctx.Err()
				}
			}
		case model.StreamToolUseStart:
			out := newEvent(EventToolUseStart, sessionID, turn)
			use := event.ToolUse
			out.ToolUse = &use
			if !emit(out) {
				return model.Message{Role: model.RoleAssistant, Content: blocks}, uses, earlyResults, usage, ctx.Err()
			}
		case model.StreamToolUseDelta:
			out := newEvent(EventToolUseDelta, sessionID, turn)
			use := event.ToolUse
			out.ToolUse = &use
			out.ToolUseDelta = event.ToolUseDelta
			if !emit(out) {
				return model.Message{Role: model.RoleAssistant, Content: blocks}, uses, earlyResults, usage, ctx.Err()
			}
		case model.StreamToolUse:
			index := len(uses)
			uses = append(uses, event.ToolUse)
			blocks = append(blocks, model.ContentBlock{Type: model.ContentToolUse, ToolUse: &event.ToolUse})
			out := newEvent(EventToolUse, sessionID, turn)
			out.ToolUse = &event.ToolUse
			if !emit(out) {
				return model.Message{Role: model.RoleAssistant, Content: blocks}, uses, earlyResults, usage, ctx.Err()
			}
			if startEarlyTool != nil {
				results, started, err := startEarlyTool(event.ToolUse)
				if err != nil {
					return model.Message{Role: model.RoleAssistant, Content: blocks}, uses, earlyResults, usage, err
				}
				if started && results != nil {
					earlyResults[index] = results
				}
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
				return model.Message{Role: model.RoleAssistant, Content: blocks}, uses, earlyResults, usage, ctx.Err()
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
