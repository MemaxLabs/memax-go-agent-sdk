package hook

import (
	"context"
	"sync"

	"github.com/MemaxLabs/memax-go-agent-sdk/model"
)

type BeforeToolUseInput struct {
	SessionID string
	Use       model.ToolUse
	Spec      model.ToolSpec
}

// BeforeToolUseResult controls whether a tool call may continue.
type BeforeToolUseResult struct {
	// DenyReason blocks execution when non-empty. The reason is returned to the
	// model as a tool-result error so the agent can recover in a later turn.
	DenyReason string
}

// BeforeToolUseFunc runs after schema validation and before permission checks.
type BeforeToolUseFunc func(context.Context, BeforeToolUseInput) (BeforeToolUseResult, error)

type AfterToolUseInput struct {
	SessionID string
	Use       model.ToolUse
	Spec      model.ToolSpec
	Result    model.ToolResult
}

// AfterToolUseFunc observes a completed tool result.
//
// Errors from after-tool hooks are attached to result metadata and do not turn a
// successful tool result into a failure, because mutating tools may already have
// changed external state.
type AfterToolUseFunc func(context.Context, AfterToolUseInput) error

// Runner executes lifecycle hooks in registration order.
type Runner struct {
	mu     sync.RWMutex
	before []BeforeToolUseFunc
	after  []AfterToolUseFunc
}

// NewRunner creates a hook runner.
func NewRunner(options ...Option) *Runner {
	r := &Runner{}
	for _, option := range options {
		option(r)
	}
	return r
}

type Option func(*Runner)

// WithBeforeToolUse registers a before-tool hook.
func WithBeforeToolUse(fn BeforeToolUseFunc) Option {
	return func(r *Runner) {
		r.AddBeforeToolUse(fn)
	}
}

// WithAfterToolUse registers an after-tool hook.
func WithAfterToolUse(fn AfterToolUseFunc) Option {
	return func(r *Runner) {
		r.AddAfterToolUse(fn)
	}
}

// AddBeforeToolUse appends a before-tool hook.
func (r *Runner) AddBeforeToolUse(fn BeforeToolUseFunc) {
	if r == nil || fn == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.before = append(r.before, fn)
}

// AddAfterToolUse appends an after-tool hook.
func (r *Runner) AddAfterToolUse(fn AfterToolUseFunc) {
	if r == nil || fn == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.after = append(r.after, fn)
}

// BeforeToolUse runs before-tool hooks until one denies or fails.
func (r *Runner) BeforeToolUse(ctx context.Context, input BeforeToolUseInput) (BeforeToolUseResult, error) {
	for _, fn := range r.beforeSnapshot() {
		result, err := fn(ctx, input)
		if err != nil {
			return BeforeToolUseResult{}, err
		}
		if result.DenyReason != "" {
			return result, nil
		}
	}
	return BeforeToolUseResult{}, nil
}

// AfterToolUse runs all after-tool hooks and returns observer errors.
func (r *Runner) AfterToolUse(ctx context.Context, input AfterToolUseInput) []error {
	var errs []error
	for _, fn := range r.afterSnapshot() {
		if err := fn(ctx, input); err != nil {
			errs = append(errs, err)
		}
	}
	return errs
}

func (r *Runner) beforeSnapshot() []BeforeToolUseFunc {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return append([]BeforeToolUseFunc(nil), r.before...)
}

func (r *Runner) afterSnapshot() []AfterToolUseFunc {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return append([]AfterToolUseFunc(nil), r.after...)
}
