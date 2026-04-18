// Package cloudmanaged provides an opinionated managed-agent stack over the
// neutral Memax runtime.
//
// The package assembles tenant-aware defaults and quota validation into one
// reusable server profile without changing the kernel. Tenant admission stays a
// normal host-owned validator on the explicit tenant seam; this package simply
// provides a batteries-included validator and preset for managed products.
//
// QuotaValidator uses a host-owned QuotaStore. Single-process managed hosts can
// use the reference MemoryQuotaStore directly; distributed deployments can
// attach a shared QuotaStore without reimplementing the validator logic.
//
// Quota reservations are admission-time accounting, not billing-accurate usage
// tracking: once reserved, quota is not automatically released if the run later
// aborts. Store errors are treated as denials so managed hosts fail closed by
// default; hosts that prefer degrade-to-allow semantics should wrap the store
// or validator explicitly.
package cloudmanaged

import (
	"context"
	"fmt"

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
	// QuotaStore overrides the reference in-memory quota backend used by the
	// managed validator. Nil keeps the default MemoryQuotaStore.
	QuotaStore QuotaStore
	Policies   Policies
	Audit      AuditConfig
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
	audit   AuditConfig
}

// New assembles a cloud-managed stack from the configured host-owned
// capabilities. Returned options are ready to pass to memaxagent.Query after
// the caller sets a model. Stack.Query and Stack.QueryAsync accept the
// tenant scope explicitly per run so one assembled stack can serve many
// tenants without rebuilding shared registries, hooks, or validators.
func New(config Config) (Stack, error) {
	opts := config.Base
	opts.Hooks = cloneHooks(opts.Hooks)
	opts.Tools = cloneRegistry(opts.Tools)
	if config.Sessions != nil {
		opts.Sessions = config.Sessions
	}
	if err := installTenantControls(&opts, config.Policies, config.QuotaStore); err != nil {
		return Stack{}, err
	}
	return Stack{options: opts, audit: config.Audit}, nil
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

func (s Stack) optionsForTenant(scope tenant.Scope) memaxagent.Options {
	opts := s.options
	opts.Tenant = scope.Clone()
	return opts
}

// Query runs the assembled cloud-managed stack and, when configured, mirrors
// the emitted event stream to the stack's audit sink. The tenant scope is
// explicit per run so managed hosts can reuse one stack across tenants.
func (s Stack) Query(ctx context.Context, prompt string, scope tenant.Scope) (<-chan memaxagent.Event, error) {
	opts := s.optionsForTenant(scope)
	ctx = memaxagent.WithEventObserver(ctx, s.audit)
	return memaxagent.Query(ctx, prompt, opts)
}

// QueryAsync runs the assembled cloud-managed stack through QueryAsync so
// startup denials also become auditable events. When configured, the stack's
// audit sink receives every emitted event in order. The tenant scope is
// explicit per run so managed hosts can reuse one stack across tenants.
func (s Stack) QueryAsync(ctx context.Context, prompt string, scope tenant.Scope) <-chan memaxagent.Event {
	return memaxagent.QueryAsync(memaxagent.WithEventObserver(ctx, s.audit), prompt, s.optionsForTenant(scope))
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
	store              QuotaStore
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

// NewQuotaValidator constructs a tenant validator for the given quota limits
// using the reference in-memory quota store.
func NewQuotaValidator(quota Quota, options ...QuotaValidatorOption) *QuotaValidator {
	return NewQuotaValidatorWithStore(nil, quota, options...)
}

// NewQuotaValidatorWithStore constructs a tenant validator for the given quota
// limits over store. Nil uses the reference in-memory quota store.
func NewQuotaValidatorWithStore(store QuotaStore, quota Quota, options ...QuotaValidatorOption) *QuotaValidator {
	if store == nil {
		store = NewMemoryQuotaStore()
	}
	v := &QuotaValidator{
		maxModelRequests: quota.MaxModelRequests,
		maxToolUses:      quota.MaxToolUses,
		store:            store,
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
		return v.ensureSession(ctx, req.Scope, req.SessionID)
	case tenant.BoundaryModelRequest:
		return v.recordModelRequest(ctx, req.Scope, req.SessionID)
	case tenant.BoundaryToolUse:
		return v.recordToolUse(ctx, req.Scope, req.SessionID)
	default:
		return nil
	}
}

// Reset forgets quota state for sessionID.
func (v *QuotaValidator) Reset(sessionID string) {
	if v == nil || sessionID == "" {
		return
	}
	_ = v.resetSession(context.Background(), tenant.Scope{}, sessionID)
}

// SessionEnded releases session-scoped quota state when a run ends.
func (v *QuotaValidator) SessionEnded(ctx context.Context, input hook.SessionEndedInput) error {
	return v.resetSession(ctx, input.Tenant, input.SessionID)
}

// Options returns hook options that keep session-scoped quota state bounded.
func (v *QuotaValidator) Options() []hook.Option {
	if v == nil {
		return nil
	}
	return []hook.Option{hook.WithSessionEnded(v.SessionEnded)}
}

func (v *QuotaValidator) ensureSession(ctx context.Context, scope tenant.Scope, sessionID string) error {
	if v == nil || sessionID == "" || v.store == nil {
		return nil
	}
	return v.store.EnsureSession(ctx, scope, sessionID)
}

func (v *QuotaValidator) resetSession(ctx context.Context, scope tenant.Scope, sessionID string) error {
	if v == nil || sessionID == "" || v.store == nil {
		return nil
	}
	return v.store.ResetSession(ctx, scope, sessionID)
}

func (v *QuotaValidator) recordModelRequest(ctx context.Context, scope tenant.Scope, sessionID string) error {
	if v.maxModelRequests <= 0 || sessionID == "" {
		return nil
	}
	_, granted, err := v.store.Reserve(ctx, scope, sessionID, QuotaCounterModelRequests, v.maxModelRequests)
	if err != nil {
		return fmt.Errorf("reserve model request quota: %w", err)
	}
	if !granted {
		return fmt.Errorf("tenant quota exceeded: max model requests reached (%d)", v.maxModelRequests)
	}
	return nil
}

func (v *QuotaValidator) recordToolUse(ctx context.Context, scope tenant.Scope, sessionID string) error {
	if v.maxToolUses <= 0 || sessionID == "" {
		return nil
	}
	_, granted, err := v.store.Reserve(ctx, scope, sessionID, QuotaCounterToolUses, v.maxToolUses)
	if err != nil {
		return fmt.Errorf("reserve tool quota: %w", err)
	}
	if !granted {
		return fmt.Errorf("tenant quota exceeded: max tool uses reached (%d)", v.maxToolUses)
	}
	return nil
}

func installTenantControls(opts *memaxagent.Options, policies Policies, store QuotaStore) error {
	validator := newManagedValidator(policies, store)
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

func newManagedValidator(policies Policies, store QuotaStore) *QuotaValidator {
	if !policies.RequireTenantScope && policies.Quota.IsZero() {
		return nil
	}
	var options []QuotaValidatorOption
	if policies.RequireTenantScope {
		options = append(options, WithRequiredTenantScope())
	}
	return NewQuotaValidatorWithStore(store, policies.Quota, options...)
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
