package tool

import (
	"context"
	"fmt"
	"sync"

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

	spec := t.Spec()
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
	return result
}

func sendResult(ctx context.Context, out chan<- model.ToolResult, result model.ToolResult) bool {
	select {
	case <-ctx.Done():
		return false
	case out <- result:
		return true
	}
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
