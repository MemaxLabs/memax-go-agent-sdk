package tool

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/MemaxLabs/memax-go-agent-sdk/hook"
	"github.com/MemaxLabs/memax-go-agent-sdk/model"
	"github.com/MemaxLabs/memax-go-agent-sdk/resultstore"
	"github.com/MemaxLabs/memax-go-agent-sdk/telemetry"
)

const DefaultMaxConcurrency = 10

type PermissionChecker interface {
	Check(context.Context, model.ToolUse, model.ToolSpec) Decision
}

type Decision struct {
	Allow  bool
	Reason string
}

type Executor struct {
	Registry       *Registry
	Permissions    PermissionChecker
	Hooks          *hook.Runner
	MaxConcurrency int
	ResultStore    resultstore.Store
	Runtime        Runtime
	Tracer         telemetry.Tracer
	Meter          telemetry.Meter
}

func (e Executor) Run(ctx context.Context, uses []model.ToolUse) <-chan model.ToolResult {
	out := make(chan model.ToolResult)
	go func() {
		defer close(out)
		for _, batch := range e.partition(uses) {
			if batch.concurrent {
				e.runConcurrent(ctx, batch.uses, out)
				continue
			}
			for _, use := range batch.uses {
				if !sendResult(ctx, out, e.runOne(ctx, use)) {
					return
				}
			}
		}
	}()
	return out
}

type batch struct {
	concurrent bool
	uses       []model.ToolUse
}

func (e Executor) partition(uses []model.ToolUse) []batch {
	var batches []batch
	for _, use := range uses {
		t, ok := e.Registry.Get(use.Name)
		concurrent := ok && t.CanRunConcurrently(use)
		if concurrent && len(batches) > 0 && batches[len(batches)-1].concurrent {
			batches[len(batches)-1].uses = append(batches[len(batches)-1].uses, use)
			continue
		}
		batches = append(batches, batch{concurrent: concurrent, uses: []model.ToolUse{use}})
	}
	return batches
}

func (e Executor) runConcurrent(ctx context.Context, uses []model.ToolUse, out chan<- model.ToolResult) {
	limit := e.MaxConcurrency
	if limit <= 0 {
		limit = DefaultMaxConcurrency
	}
	sem := make(chan struct{}, limit)
	results := make([]model.ToolResult, len(uses))
	var wg sync.WaitGroup
	for i, use := range uses {
		i, use := i, use
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case <-ctx.Done():
				results[i] = errorResult(use, ctx.Err())
				return
			case sem <- struct{}{}:
			}
			defer func() { <-sem }()
			results[i] = e.runOne(ctx, use)
		}()
	}
	wg.Wait()

	for _, result := range results {
		if !sendResult(ctx, out, result) {
			return
		}
	}
}

func (e Executor) runOne(ctx context.Context, use model.ToolUse) model.ToolResult {
	tracer := e.Tracer
	if tracer == nil {
		tracer = telemetry.NoopTracer{}
	}
	meter := e.Meter
	if meter == nil {
		meter = telemetry.NoopMeter{}
	}
	started := time.Now()
	ctx, span := tracer.Start(ctx, "memaxagent.tool.execute",
		telemetry.String("memax.session_id", e.Runtime.SessionID),
		telemetry.String("memax.tool.id", use.ID),
		telemetry.String("memax.tool.name", use.Name),
		telemetry.Int("memax.tool.input_bytes", len(use.Input)),
	)
	defer span.End()
	finish := func(result model.ToolResult) model.ToolResult {
		attrs := []telemetry.Attribute{
			telemetry.String("memax.session_id", e.Runtime.SessionID),
			telemetry.String("memax.tool.id", use.ID),
			telemetry.String("memax.tool.name", use.Name),
			telemetry.Bool("memax.tool.error", result.IsError),
		}
		meter.Add(ctx, "memax.tool.executions", 1, attrs...)
		meter.Record(ctx, "memax.tool.duration_ms", durationMilliseconds(time.Since(started)), attrs...)
		return result
	}
	fail := func(err error) model.ToolResult {
		span.RecordError(err)
		span.Set(telemetry.Bool("memax.tool.error", true))
		return finish(errorResult(use, err))
	}

	t, ok := e.Registry.Get(use.Name)
	if !ok {
		return fail(fmt.Errorf("no such tool: %s", use.Name))
	}
	schema, _ := e.Registry.InputSchema(use.Name)
	if err := validateInput(use, schema); err != nil {
		return fail(err)
	}

	spec := t.Spec()
	span.Set(
		telemetry.Bool("memax.tool.read_only", spec.ReadOnly),
		telemetry.Bool("memax.tool.destructive", spec.Destructive),
		telemetry.Bool("memax.tool.concurrency_safe", spec.ConcurrencySafe),
	)
	if e.Hooks != nil {
		result, err := e.Hooks.BeforeToolUse(ctx, hook.BeforeToolUseInput{
			SessionID: e.Runtime.SessionID,
			Use:       use,
			Spec:      spec,
		})
		if err != nil {
			meter.Add(ctx, "memax.hook.errors", 1,
				telemetry.String("memax.session_id", e.Runtime.SessionID),
				telemetry.String("memax.hook", "before_tool_use"),
				telemetry.String("memax.tool.name", use.Name),
			)
			return fail(fmt.Errorf("before tool hook failed: %w", err))
		}
		if result.DenyReason != "" {
			return fail(fmt.Errorf("%s", result.DenyReason))
		}
	}

	if e.Permissions != nil {
		decision := e.Permissions.Check(ctx, use, spec)
		if !decision.Allow {
			if decision.Reason == "" {
				decision.Reason = "permission denied"
			}
			return fail(fmt.Errorf("%s", decision.Reason))
		}
	}

	result, err := t.Execute(ctx, Call{Use: use, Runtime: e.Runtime})
	if err != nil {
		return fail(err)
	}
	result.ToolUseID = use.ID
	result.Name = use.Name
	result = e.enforceResultLimit(ctx, result, use, spec.MaxResultBytes)
	span.Set(
		telemetry.Bool("memax.tool.error", result.IsError),
		telemetry.Int("memax.tool.result_bytes", len(result.Content)),
	)
	if result.IsError {
		span.RecordError(fmt.Errorf("%s", result.Content))
	}
	if e.Hooks != nil {
		errs := e.Hooks.AfterToolUse(ctx, hook.AfterToolUseInput{
			SessionID: e.Runtime.SessionID,
			Use:       use,
			Spec:      spec,
			Result:    result,
		})
		if len(errs) > 0 {
			meter.Add(ctx, "memax.hook.errors", int64(len(errs)),
				telemetry.String("memax.session_id", e.Runtime.SessionID),
				telemetry.String("memax.hook", "after_tool_use"),
				telemetry.String("memax.tool.name", use.Name),
			)
			result = withHookErrors(result, errs)
		}
	}
	return finish(result)
}

func (e Executor) enforceResultLimit(ctx context.Context, result model.ToolResult, use model.ToolUse, limit int) model.ToolResult {
	if limit <= 0 || len(result.Content) <= limit {
		return result
	}
	originalBytes := len(result.Content)
	if e.ResultStore != nil {
		handle, err := e.ResultStore.Put(ctx, resultstore.PutRequest{
			SessionID:       e.Runtime.SessionID,
			ParentSessionID: e.Runtime.ParentSessionID,
			ToolUseID:       use.ID,
			ToolName:        use.Name,
			Content:         result.Content,
			Metadata:        result.Metadata,
		})
		result.Metadata = cloneMetadata(result.Metadata)
		if err != nil {
			result.Metadata["stored_result_error"] = err.Error()
		} else {
			if handle.ID != "" {
				result.Metadata["stored_result_id"] = handle.ID
			}
			if handle.URI != "" {
				result.Metadata["stored_result_uri"] = handle.URI
			}
			storedBytes := handle.Bytes
			if storedBytes <= 0 {
				storedBytes = originalBytes
			}
			result.Metadata["stored_result_bytes"] = storedBytes
			if !handle.CreatedAt.IsZero() {
				result.Metadata["stored_result_created_at"] = handle.CreatedAt.Format(time.RFC3339Nano)
			}
		}
	}
	result.Content = truncateUTF8(result.Content, limit)
	result.Metadata = cloneMetadata(result.Metadata)
	result.Metadata["truncated"] = true
	result.Metadata["original_bytes"] = originalBytes
	result.Metadata["returned_bytes"] = len(result.Content)
	return result
}

func truncateUTF8(s string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if len(s) <= limit {
		return s
	}
	end := 0
	for i := range s {
		if i > limit {
			break
		}
		end = i
	}
	return s[:end]
}

func cloneMetadata(metadata map[string]any) map[string]any {
	out := make(map[string]any, len(metadata)+3)
	for k, v := range metadata {
		out[k] = v
	}
	return out
}

func sendResult(ctx context.Context, out chan<- model.ToolResult, result model.ToolResult) bool {
	select {
	case <-ctx.Done():
		return false
	case out <- result:
		return true
	}
}

func withHookErrors(result model.ToolResult, errs []error) model.ToolResult {
	if len(errs) == 0 {
		return result
	}
	if result.Metadata == nil {
		result.Metadata = make(map[string]any)
	}
	messages := make([]string, 0, len(errs))
	for _, err := range errs {
		if err == nil {
			continue
		}
		messages = append(messages, err.Error())
	}
	if len(messages) == 0 {
		return result
	}
	result.Metadata["hook_errors"] = messages
	return result
}

func errorResult(use model.ToolUse, err error) model.ToolResult {
	msg := "unknown error"
	if err != nil {
		msg = err.Error()
	}
	return model.ToolResult{
		ToolUseID: use.ID,
		Name:      use.Name,
		Content:   msg,
		IsError:   true,
	}
}

func durationMilliseconds(d time.Duration) float64 {
	return float64(d) / float64(time.Millisecond)
}
