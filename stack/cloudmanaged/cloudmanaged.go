// Package cloudmanaged provides an opinionated managed-agent stack over the
// neutral Memax runtime.
//
// The package assembles tenant-aware defaults and quota validation into one
// reusable server profile without changing the kernel. Tenant admission stays a
// normal host-owned validator on the explicit tenant seam; this package simply
// provides a batteries-included validator and preset for managed products.
//
// QuotaValidator keeps per-session state in memory. Single-process managed
// hosts can use it directly; distributed deployments need either session-affine
// routing or a host-owned distributed tenant.Validator backed by shared state.
package cloudmanaged

import (
	"context"
	"fmt"
	"sync"

	memaxagent "github.com/MemaxLabs/memax-go-agent-sdk"
	"github.com/MemaxLabs/memax-go-agent-sdk/hook"
	"github.com/MemaxLabs/memax-go-agent-sdk/model"
	"github.com/MemaxLabs/memax-go-agent-sdk/session"
	"github.com/MemaxLabs/memax-go-agent-sdk/tenant"
	"github.com/MemaxLabs/memax-go-agent-sdk/tool"
)

// Config assembles a cloud-managed stack from explicit host-owned components.
//
// Base carries neutral memaxagent.Options that should remain in force. Base
// tool registries are cloned before the stack is returned, so the caller's
// registry is never mutated. Base hooks are cloned before quota cleanup hooks
// are added, so callers can safely reuse a base option bundle across multiple
// stacks.
type Config struct {
	Base     memaxagent.Options
	Sessions session.Store
	Policies Policies
}

// Policies configures optional tenant-aware governance for the cloud-managed
// stack. The zero value is intentionally conservative about hidden behavior:
// policies are disabled until the host opts in or uses a preset.
type Policies struct {
	RequireTenantScope bool
	Quota              Quota
}

// Quota configures per-session managed-service limits enforced through the
// tenant admission seam. Zero values disable the corresponding limit.
type Quota struct {
	MaxModelRequests int
	MaxToolUses      int
}

// IsZero reports whether q has no active limits.
func (q Quota) IsZero() bool {
	return q.MaxModelRequests <= 0 && q.MaxToolUses <= 0
}

// DefaultPolicies returns a practical default cloud-managed governance preset.
//
// The default requires explicit tenant scope and applies a bounded per-session
// envelope for model requests and tool uses. This is intentionally narrower
// than a full tenant billing system: it gives managed hosts a concrete,
// deterministic admission layer without hard-coding ledger, quota-window, or
// policy-service semantics into the kernel.
func DefaultPolicies() Policies {
	return Policies{
		RequireTenantScope: true,
		Quota: Quota{
			MaxModelRequests: 16,
			MaxToolUses:      32,
		},
	}
}

// Stack is one assembled cloud-managed runtime profile.
type Stack struct {
	options memaxagent.Options
}

// New assembles a cloud-managed stack from the configured host-owned
// capabilities. Returned options are ready to pass to memaxagent.Query after
// the caller sets a model and the run's tenant scope.
func New(config Config) (Stack, error) {
	opts := config.Base
	opts.Hooks = cloneHooks(opts.Hooks)
	opts.Tools = cloneRegistry(opts.Tools)
	if config.Sessions != nil {
		opts.Sessions = config.Sessions
	}
	if err := installTenantControls(&opts, config.Policies); err != nil {
		return Stack{}, err
	}
	return Stack{options: opts}, nil
}

// Options returns the assembled agent options. The returned value is a copy of
// the stack-level option struct; referenced registries and hook runners remain
// shared.
func (s Stack) Options() memaxagent.Options {
	return s.options
}

// WithModel returns assembled options with client installed as the model.
func (s Stack) WithModel(client model.Client) memaxagent.Options {
	opts := s.options
	opts.Model = client
	return opts
}

// Registry returns the assembled tool registry.
func (s Stack) Registry() *tool.Registry {
	return s.options.Tools
}

// Hooks returns the assembled hook runner.
func (s Stack) Hooks() *hook.Runner {
	return s.options.Hooks
}

// QuotaValidator enforces tenant-aware per-session model/tool quotas through
// the kernel's explicit tenant admission seam.
//
// Limits are tracked per session ID. Delegated child agents inherit the same
// validator but consume their own session envelope because subagent runs create
// child sessions with distinct IDs.
type QuotaValidator struct {
	requireTenantScope bool
	maxModelRequests   int
	maxToolUses        int

	mu       sync.RWMutex
	sessions map[string]quotaState
}

type quotaState struct {
	ModelRequests int
	ToolUses      int
}

// QuotaValidatorOption configures a QuotaValidator.
type QuotaValidatorOption func(*QuotaValidator)

// WithRequiredTenantScope requires non-zero tenant scope on every validated
// boundary.
func WithRequiredTenantScope() QuotaValidatorOption {
	return func(v *QuotaValidator) {
		if v != nil {
			v.requireTenantScope = true
		}
	}
}

// NewQuotaValidator constructs a tenant validator for the given quota limits.
func NewQuotaValidator(quota Quota, options ...QuotaValidatorOption) *QuotaValidator {
	v := &QuotaValidator{
		maxModelRequests: quota.MaxModelRequests,
		maxToolUses:      quota.MaxToolUses,
	}
	for _, option := range options {
		if option != nil {
			option(v)
		}
	}
	return v
}

// Validate implements tenant.Validator.
func (v *QuotaValidator) Validate(ctx context.Context, req tenant.Request) error {
	if v == nil {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if v.requireTenantScope && req.Scope.IsZero() {
		return fmt.Errorf("tenant scope required for cloud-managed run")
	}
	if v.maxModelRequests <= 0 && v.maxToolUses <= 0 {
		return nil
	}
	switch req.Boundary {
	case tenant.BoundarySessionStart:
		v.ensureSession(req.SessionID)
		return nil
	case tenant.BoundaryModelRequest:
		return v.recordModelRequest(req.SessionID)
	case tenant.BoundaryToolUse:
		return v.recordToolUse(req.SessionID)
	default:
		return nil
	}
}

// Reset forgets quota state for sessionID.
func (v *QuotaValidator) Reset(sessionID string) {
	if v == nil || sessionID == "" {
		return
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	delete(v.sessions, sessionID)
}

// SessionEnded releases session-scoped quota state when a run ends.
func (v *QuotaValidator) SessionEnded(_ context.Context, input hook.SessionEndedInput) error {
	v.Reset(input.SessionID)
	return nil
}

// Options returns hook options that keep session-scoped quota state bounded.
func (v *QuotaValidator) Options() []hook.Option {
	if v == nil {
		return nil
	}
	return []hook.Option{hook.WithSessionEnded(v.SessionEnded)}
}

func (v *QuotaValidator) ensureSession(sessionID string) {
	if sessionID == "" {
		return
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.sessions == nil {
		v.sessions = make(map[string]quotaState)
	}
	if _, ok := v.sessions[sessionID]; !ok {
		v.sessions[sessionID] = quotaState{}
	}
}

func (v *QuotaValidator) recordModelRequest(sessionID string) error {
	if v.maxModelRequests <= 0 || sessionID == "" {
		return nil
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.sessions == nil {
		v.sessions = make(map[string]quotaState)
	}
	state := v.sessions[sessionID]
	if state.ModelRequests >= v.maxModelRequests {
		return fmt.Errorf("tenant quota exceeded: max model requests reached (%d)", v.maxModelRequests)
	}
	state.ModelRequests++
	v.sessions[sessionID] = state
	return nil
}

func (v *QuotaValidator) recordToolUse(sessionID string) error {
	if v.maxToolUses <= 0 || sessionID == "" {
		return nil
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.sessions == nil {
		v.sessions = make(map[string]quotaState)
	}
	state := v.sessions[sessionID]
	if state.ToolUses >= v.maxToolUses {
		return fmt.Errorf("tenant quota exceeded: max tool uses reached (%d)", v.maxToolUses)
	}
	state.ToolUses++
	v.sessions[sessionID] = state
	return nil
}

func installTenantControls(opts *memaxagent.Options, policies Policies) error {
	validator := newManagedValidator(policies)
	if validator != nil {
		opts.TenantValidator = combineValidators(opts.TenantValidator, validator)
		for _, option := range validator.Options() {
			if option == nil {
				continue
			}
			if opts.Hooks == nil {
				opts.Hooks = hook.NewRunner()
			}
			option(opts.Hooks)
		}
	}
	return nil
}

func newManagedValidator(policies Policies) *QuotaValidator {
	if !policies.RequireTenantScope && policies.Quota.IsZero() {
		return nil
	}
	var options []QuotaValidatorOption
	if policies.RequireTenantScope {
		options = append(options, WithRequiredTenantScope())
	}
	return NewQuotaValidator(policies.Quota, options...)
}

func combineValidators(validators ...tenant.Validator) tenant.Validator {
	filtered := make([]tenant.Validator, 0, len(validators))
	for _, validator := range validators {
		if validator != nil {
			filtered = append(filtered, validator)
		}
	}
	switch len(filtered) {
	case 0:
		return nil
	case 1:
		return filtered[0]
	default:
		return tenant.ValidatorFunc(func(ctx context.Context, req tenant.Request) error {
			for _, validator := range filtered {
				if err := validator.Validate(ctx, req); err != nil {
					return err
				}
			}
			return nil
		})
	}
}

func cloneRegistry(registry *tool.Registry) *tool.Registry {
	if registry == nil {
		return tool.NewRegistry()
	}
	return registry.Clone()
}

func cloneHooks(runner *hook.Runner) *hook.Runner {
	if runner == nil {
		return nil
	}
	return runner.Clone()
}
