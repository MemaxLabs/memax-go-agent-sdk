package hook

import (
	"context"
	"sync"

	"github.com/MemaxLabs/memax-go-agent-sdk/model"
	"github.com/MemaxLabs/memax-go-agent-sdk/tenant"
)

type BeforeToolUseInput struct {
	SessionID string
	Tenant    tenant.Scope
	Use       model.ToolUse
	Spec      model.ToolSpec
}

// BeforeToolUseResult controls whether a tool call may continue.
type BeforeToolUseResult struct {
	// DenyReason blocks execution when non-empty. The reason is returned to the
	// model as a tool-result error so the agent can recover in a later turn.
	DenyReason string
	// Metadata is attached to the tool result when execution is allowed. Gate
	// hooks can use it to expose policy decisions, such as an approval grant
	// consumed for this attempt, without changing the tool handler contract.
	Metadata map[string]any
}

// BeforeToolUseFunc runs after schema validation and before permission checks.
type BeforeToolUseFunc func(context.Context, BeforeToolUseInput) (BeforeToolUseResult, error)

type AfterToolUseInput struct {
	SessionID string
	Tenant    tenant.Scope
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

type BeforeFinalInput struct {
	SessionID string
	Tenant    tenant.Scope
	Turn      int
	Answer    string
}

// BeforeFinalResult controls whether a final answer may complete the run.
type BeforeFinalResult struct {
	// DenyReason blocks finalization when non-empty. The reason is appended to
	// the transcript as a user repair prompt so the agent can recover with
	// normal tool calls in a later turn.
	DenyReason string
}

// BeforeFinalFunc runs after the model produces a no-tool assistant response
// and before output validation, memory distillation, and EventResult.
type BeforeFinalFunc func(context.Context, BeforeFinalInput) (BeforeFinalResult, error)

type SessionStartedInput struct {
	SessionID string
	Tenant    tenant.Scope
}

type SessionStartedFunc func(context.Context, SessionStartedInput) error

type SessionEndedInput struct {
	SessionID string
	Tenant    tenant.Scope
	Reason    StopReason
	Err       error
}

type SessionEndedFunc func(context.Context, SessionEndedInput) error

type UserPromptInput struct {
	SessionID string
	Tenant    tenant.Scope
	Prompt    string
}

type UserPromptResult struct {
	Prompt     string
	DenyReason string
}

type UserPromptFunc func(context.Context, UserPromptInput) (UserPromptResult, error)

type StopReason string

const (
	StopReasonResult   StopReason = "result"
	StopReasonError    StopReason = "error"
	StopReasonMaxTurns StopReason = "max_turns"
	StopReasonBudget   StopReason = "budget"
	StopReasonPolicy   StopReason = "policy"
	StopReasonCanceled StopReason = "canceled"
)

type StopInput struct {
	SessionID string
	Tenant    tenant.Scope
	Turn      int
	Reason    StopReason
	Err       error
}

type StopFunc func(context.Context, StopInput) error

type ContextAppliedInput struct {
	SessionID        string
	Tenant           tenant.Scope
	Turn             int
	OriginalMessages int
	SentMessages     int
}

type ContextAppliedFunc func(context.Context, ContextAppliedInput) error

// Runner executes lifecycle hooks in registration order.
type Runner struct {
	mu             sync.RWMutex
	before         []BeforeToolUseFunc
	after          []AfterToolUseFunc
	beforeFinal    []BeforeFinalFunc
	sessionStarted []SessionStartedFunc
	sessionEnded   []SessionEndedFunc
	userPrompt     []UserPromptFunc
	stop           []StopFunc
	contextApplied []ContextAppliedFunc
}

// NewRunner creates a hook runner.
func NewRunner(options ...Option) *Runner {
	r := &Runner{}
	for _, option := range options {
		option(r)
	}
	return r
}

// Clone returns a snapshot copy of r. Hook functions are shared; the runner's
// internal slices are copied so later Add* calls on either runner do not mutate
// the other.
func (r *Runner) Clone() *Runner {
	if r == nil {
		return NewRunner()
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return &Runner{
		before:         append([]BeforeToolUseFunc(nil), r.before...),
		after:          append([]AfterToolUseFunc(nil), r.after...),
		beforeFinal:    append([]BeforeFinalFunc(nil), r.beforeFinal...),
		sessionStarted: append([]SessionStartedFunc(nil), r.sessionStarted...),
		sessionEnded:   append([]SessionEndedFunc(nil), r.sessionEnded...),
		userPrompt:     append([]UserPromptFunc(nil), r.userPrompt...),
		stop:           append([]StopFunc(nil), r.stop...),
		contextApplied: append([]ContextAppliedFunc(nil), r.contextApplied...),
	}
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

// WithBeforeFinal registers a before-final hook.
func WithBeforeFinal(fn BeforeFinalFunc) Option {
	return func(r *Runner) {
		r.AddBeforeFinal(fn)
	}
}

// WithSessionStarted registers a session-start hook.
func WithSessionStarted(fn SessionStartedFunc) Option {
	return func(r *Runner) {
		r.AddSessionStarted(fn)
	}
}

// WithSessionEnded registers a session-end hook.
func WithSessionEnded(fn SessionEndedFunc) Option {
	return func(r *Runner) {
		r.AddSessionEnded(fn)
	}
}

// WithUserPrompt registers a user-prompt hook.
func WithUserPrompt(fn UserPromptFunc) Option {
	return func(r *Runner) {
		r.AddUserPrompt(fn)
	}
}

// WithStop registers a stop hook.
func WithStop(fn StopFunc) Option {
	return func(r *Runner) {
		r.AddStop(fn)
	}
}

// WithContextApplied registers a context-applied hook.
func WithContextApplied(fn ContextAppliedFunc) Option {
	return func(r *Runner) {
		r.AddContextApplied(fn)
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

// AddBeforeFinal appends a before-final hook.
func (r *Runner) AddBeforeFinal(fn BeforeFinalFunc) {
	if r == nil || fn == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.beforeFinal = append(r.beforeFinal, fn)
}

// AddSessionStarted appends a session-start hook.
func (r *Runner) AddSessionStarted(fn SessionStartedFunc) {
	if r == nil || fn == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sessionStarted = append(r.sessionStarted, fn)
}

// AddSessionEnded appends a session-end hook.
func (r *Runner) AddSessionEnded(fn SessionEndedFunc) {
	if r == nil || fn == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sessionEnded = append(r.sessionEnded, fn)
}

// AddUserPrompt appends a user-prompt hook.
func (r *Runner) AddUserPrompt(fn UserPromptFunc) {
	if r == nil || fn == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.userPrompt = append(r.userPrompt, fn)
}

// AddStop appends a stop hook.
func (r *Runner) AddStop(fn StopFunc) {
	if r == nil || fn == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stop = append(r.stop, fn)
}

// AddContextApplied appends a context-applied hook.
func (r *Runner) AddContextApplied(fn ContextAppliedFunc) {
	if r == nil || fn == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.contextApplied = append(r.contextApplied, fn)
}

// BeforeToolUse runs before-tool hooks until one denies or fails.
func (r *Runner) BeforeToolUse(ctx context.Context, input BeforeToolUseInput) (BeforeToolUseResult, error) {
	var out BeforeToolUseResult
	for _, fn := range r.beforeSnapshot() {
		result, err := fn(ctx, input)
		if err != nil {
			return BeforeToolUseResult{}, err
		}
		if len(result.Metadata) > 0 {
			if out.Metadata == nil {
				out.Metadata = map[string]any{}
			}
			for key, value := range result.Metadata {
				out.Metadata[key] = value
			}
		}
		if result.DenyReason != "" {
			result.Metadata = model.CloneMetadata(out.Metadata)
			return result, nil
		}
	}
	return out, nil
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

// BeforeFinal runs before-final hooks until one denies or fails.
func (r *Runner) BeforeFinal(ctx context.Context, input BeforeFinalInput) (BeforeFinalResult, error) {
	for _, fn := range r.beforeFinalSnapshot() {
		result, err := fn(ctx, input)
		if err != nil {
			return BeforeFinalResult{}, err
		}
		if result.DenyReason != "" {
			return result, nil
		}
	}
	return BeforeFinalResult{}, nil
}

// SessionStarted runs session-start hooks and returns observer errors.
func (r *Runner) SessionStarted(ctx context.Context, input SessionStartedInput) []error {
	var errs []error
	for _, fn := range r.sessionStartedSnapshot() {
		if err := fn(ctx, input); err != nil {
			errs = append(errs, err)
		}
	}
	return errs
}

// SessionEnded runs session-end hooks and returns observer errors.
func (r *Runner) SessionEnded(ctx context.Context, input SessionEndedInput) []error {
	var errs []error
	for _, fn := range r.sessionEndedSnapshot() {
		if err := fn(ctx, input); err != nil {
			errs = append(errs, err)
		}
	}
	return errs
}

// UserPrompt runs user-prompt hooks in order. Hooks can deny or rewrite the
// prompt before it is persisted.
func (r *Runner) UserPrompt(ctx context.Context, input UserPromptInput) (UserPromptResult, error) {
	result := UserPromptResult{Prompt: input.Prompt}
	for _, fn := range r.userPromptSnapshot() {
		next, err := fn(ctx, UserPromptInput{SessionID: input.SessionID, Prompt: result.Prompt})
		if err != nil {
			return UserPromptResult{}, err
		}
		if next.DenyReason != "" {
			return next, nil
		}
		if next.Prompt != "" {
			result.Prompt = next.Prompt
		}
	}
	return result, nil
}

// Stop runs stop hooks and returns observer errors.
func (r *Runner) Stop(ctx context.Context, input StopInput) []error {
	var errs []error
	for _, fn := range r.stopSnapshot() {
		if err := fn(ctx, input); err != nil {
			errs = append(errs, err)
		}
	}
	return errs
}

// ContextApplied runs context-applied hooks and returns observer errors.
func (r *Runner) ContextApplied(ctx context.Context, input ContextAppliedInput) []error {
	var errs []error
	for _, fn := range r.contextAppliedSnapshot() {
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

func (r *Runner) beforeFinalSnapshot() []BeforeFinalFunc {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return append([]BeforeFinalFunc(nil), r.beforeFinal...)
}

func (r *Runner) sessionStartedSnapshot() []SessionStartedFunc {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return append([]SessionStartedFunc(nil), r.sessionStarted...)
}

func (r *Runner) sessionEndedSnapshot() []SessionEndedFunc {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return append([]SessionEndedFunc(nil), r.sessionEnded...)
}

func (r *Runner) userPromptSnapshot() []UserPromptFunc {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return append([]UserPromptFunc(nil), r.userPrompt...)
}

func (r *Runner) stopSnapshot() []StopFunc {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return append([]StopFunc(nil), r.stop...)
}

func (r *Runner) contextAppliedSnapshot() []ContextAppliedFunc {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return append([]ContextAppliedFunc(nil), r.contextApplied...)
}
