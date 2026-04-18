package tenant

import "context"

// Scope identifies the tenant and subject associated with one agent run.
//
// The runtime treats this as opaque host-owned data. Hosts can use it to
// enforce tenancy, billing, or admission rules without coupling the kernel to
// any specific account model. Attributes are intentionally string-typed:
// tenant scope is a stable identity record, not a free-form metadata bag.
type Scope struct {
	ID         string
	SubjectID  string
	Attributes map[string]string
}

// IsZero reports whether s carries no tenant information.
func (s Scope) IsZero() bool {
	return s.ID == "" && s.SubjectID == "" && len(s.Attributes) == 0
}

// Clone returns a copy of s with its attribute map detached.
func (s Scope) Clone() Scope {
	if len(s.Attributes) == 0 {
		s.Attributes = nil
		return s
	}
	out := s
	out.Attributes = make(map[string]string, len(s.Attributes))
	for key, value := range s.Attributes {
		out.Attributes[key] = value
	}
	return out
}

// Boundary identifies the runtime seam a tenant validation request came from.
type Boundary string

const (
	// BoundarySessionStart validates session creation or resume.
	BoundarySessionStart Boundary = "session_start"
	// BoundaryModelRequest validates one outbound model request.
	BoundaryModelRequest Boundary = "model_request"
	// BoundaryToolUse validates one tool-use attempt after schema validation and
	// before hooks, permissions, or tool execution.
	BoundaryToolUse Boundary = "tool_use"
)

// Request is the host-visible admission payload for one tenant validation.
type Request struct {
	Boundary            Boundary
	Scope               Scope
	SessionID           string
	ParentSessionID     string
	ToolUseID           string
	ToolName            string
	ToolReadOnly        bool
	ToolDestructive     bool
	ToolConcurrencySafe bool
}

// Validator decides whether a tenant-scoped runtime action may proceed.
type Validator interface {
	Validate(context.Context, Request) error
}

// ValidatorFunc adapts a function into a Validator.
type ValidatorFunc func(context.Context, Request) error

// Validate implements Validator.
func (f ValidatorFunc) Validate(ctx context.Context, req Request) error {
	if f == nil {
		return nil
	}
	return f(ctx, req)
}

// AllowAll permits every tenant-scoped action.
type AllowAll struct{}

// Validate implements Validator.
func (AllowAll) Validate(context.Context, Request) error { return nil }

// Check validates req with validator when one is configured.
func Check(ctx context.Context, validator Validator, req Request) error {
	if validator == nil {
		return nil
	}
	req.Scope = req.Scope.Clone()
	if err := validator.Validate(ctx, req); err != nil {
		return &DeniedError{Request: req, Err: err}
	}
	return nil
}

// DeniedError wraps a tenant validator denial with the request that triggered
// it so callers can emit structured observability without parsing error text.
type DeniedError struct {
	Request Request
	Err     error
}

// Error implements error.
func (e *DeniedError) Error() string {
	if e == nil || e.Err == nil {
		return "tenant denied"
	}
	return e.Err.Error()
}

// Unwrap implements errors.Wrapper.
func (e *DeniedError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}
