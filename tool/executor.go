package tool

import (
	"context"
	"fmt"
	"sync"

	"github.com/MemaxLabs/memax-go-agent-sdk/hook"
	"github.com/MemaxLabs/memax-go-agent-sdk/model"
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
	Runtime        Runtime
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
	t, ok := e.Registry.Get(use.Name)
	if !ok {
		return errorResult(use, fmt.Errorf("no such tool: %s", use.Name))
	}
	schema, _ := e.Registry.InputSchema(use.Name)
	if err := validateInput(use, schema); err != nil {
		return errorResult(use, err)
	}

	spec := t.Spec()
	if e.Hooks != nil {
		result, err := e.Hooks.BeforeToolUse(ctx, hook.BeforeToolUseInput{
			SessionID: e.Runtime.SessionID,
			Use:       use,
			Spec:      spec,
		})
		if err != nil {
			return errorResult(use, fmt.Errorf("before tool hook failed: %w", err))
		}
		if result.DenyReason != "" {
			return errorResult(use, fmt.Errorf("%s", result.DenyReason))
		}
	}

	if e.Permissions != nil {
		decision := e.Permissions.Check(ctx, use, spec)
		if !decision.Allow {
			if decision.Reason == "" {
				decision.Reason = "permission denied"
			}
			return errorResult(use, fmt.Errorf("%s", decision.Reason))
		}
	}

	result, err := t.Execute(ctx, Call{Use: use, Runtime: e.Runtime})
	if err != nil {
		return errorResult(use, err)
	}
	result.ToolUseID = use.ID
	result.Name = use.Name
	result = enforceResultLimit(result, spec.MaxResultBytes)
	if e.Hooks != nil {
		errs := e.Hooks.AfterToolUse(ctx, hook.AfterToolUseInput{
			SessionID: e.Runtime.SessionID,
			Use:       use,
			Spec:      spec,
			Result:    result,
		})
		if len(errs) > 0 {
			result = withHookErrors(result, errs)
		}
	}
	return result
}

func enforceResultLimit(result model.ToolResult, limit int) model.ToolResult {
	if limit <= 0 || len(result.Content) <= limit {
		return result
	}
	originalBytes := len(result.Content)
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
