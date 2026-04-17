// Package approvaltools provides explicit host approval tools. Approval is
// modeled as a normal tool result so the request, decision, and reason are
// visible in the transcript and can be combined with policy hooks.
package approvaltools

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/MemaxLabs/memax-go-agent-sdk/identity"
	"github.com/MemaxLabs/memax-go-agent-sdk/model"
	"github.com/MemaxLabs/memax-go-agent-sdk/tool"
)

const (
	// ToolName is the default approval request tool name.
	ToolName = "request_approval"

	// MetadataApprovalOperation identifies approval tool results.
	MetadataApprovalOperation = "approval_operation"
	// MetadataApprovalAction carries the action or tool name being approved.
	MetadataApprovalAction = "approval_action"
	// MetadataApprovalApproved records whether approval was granted.
	MetadataApprovalApproved = "approval_approved"
	// MetadataApprovalReason carries a host-visible approval or denial reason.
	MetadataApprovalReason = "approval_reason"
)

// Request describes one approval request from the model to the host.
type Request struct {
	SessionID       string
	ParentSessionID string
	Identity        identity.Identity
	Action          string
	Reason          string
	Details         string
	Risk            string
	Metadata        map[string]any
}

// Decision is returned by an Approver. Denied decisions are surfaced as tool
// error results so the model can recover or choose a safer path.
type Decision struct {
	Approved bool
	Reason   string
	Metadata map[string]any
}

// Approver is implemented by hosts that can approve or deny requested actions.
// Implementations may call a human, consult policy, enqueue review, or return a
// deterministic decision in tests.
type Approver interface {
	RequestApproval(context.Context, Request) (Decision, error)
}

// ApproverFunc adapts a function into an Approver.
type ApproverFunc func(context.Context, Request) (Decision, error)

func (f ApproverFunc) RequestApproval(ctx context.Context, req Request) (Decision, error) {
	if f == nil {
		return Decision{}, fmt.Errorf("approvaltools: approver is required")
	}
	return f(ctx, req)
}

// StaticApprover returns the same decision for every request.
type StaticApprover struct {
	Decision Decision
}

func (a StaticApprover) RequestApproval(ctx context.Context, _ Request) (Decision, error) {
	if err := ctx.Err(); err != nil {
		return Decision{}, err
	}
	return cloneDecision(a.Decision), nil
}

// Config configures NewTool.
type Config struct {
	Approver        Approver
	Name            string
	Description     string
	SearchHint      string
	ConcurrencySafe bool
	MaxResultBytes  int
	Timeout         time.Duration
}

// NewTool returns a tool that asks the configured host approver for a decision.
func NewTool(config Config) tool.Tool {
	name := strings.TrimSpace(config.Name)
	if name == "" {
		name = ToolName
	}
	description := strings.TrimSpace(config.Description)
	if description == "" {
		description = "Request explicit host approval before performing a sensitive or policy-gated action."
	}
	searchHint := strings.TrimSpace(config.SearchHint)
	if searchHint == "" {
		searchHint = "request approval ask user permission policy sensitive action"
	}
	maxResultBytes := config.MaxResultBytes
	if maxResultBytes <= 0 {
		maxResultBytes = 8 * 1024
	}
	return tool.Definition{
		ToolSpec: model.ToolSpec{
			Name:            name,
			Description:     description,
			SearchHint:      searchHint,
			ReadOnly:        true,
			ConcurrencySafe: config.ConcurrencySafe,
			AlwaysLoad:      true,
			MaxResultBytes:  maxResultBytes,
			InputSchema: map[string]any{
				"type":                 "object",
				"required":             []any{"action", "reason"},
				"additionalProperties": false,
				"properties": map[string]any{
					"action": map[string]any{
						"type":        "string",
						"description": "Action or tool name that needs approval, for example workspace_apply_patch.",
						"minLength":   1,
					},
					"reason": map[string]any{
						"type":        "string",
						"description": "Short explanation of why approval is needed.",
						"minLength":   1,
					},
					"details": map[string]any{
						"type":        "string",
						"description": "Optional concrete details, affected paths, commands, or proposed change summary.",
					},
					"risk": map[string]any{
						"type":        "string",
						"description": "Optional risk level or risk description.",
					},
					"metadata": map[string]any{
						"type":        "object",
						"description": "Optional host-defined metadata for the approval request.",
					},
				},
			},
		},
		Handler: func(ctx context.Context, call tool.Call) (model.ToolResult, error) {
			if config.Approver == nil {
				return model.ToolResult{}, fmt.Errorf("approvaltools: approver is required")
			}
			input, err := tool.DecodeInput[input](call.Use)
			if err != nil {
				return model.ToolResult{}, err
			}
			action := strings.TrimSpace(input.Action)
			reason := strings.TrimSpace(input.Reason)
			if action == "" {
				return model.ToolResult{}, fmt.Errorf("approvaltools: action is required")
			}
			if reason == "" {
				return model.ToolResult{}, fmt.Errorf("approvaltools: reason is required")
			}
			if config.Timeout > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, config.Timeout)
				defer cancel()
			}
			decision, err := config.Approver.RequestApproval(ctx, Request{
				SessionID:       call.Runtime.SessionID,
				ParentSessionID: call.Runtime.ParentSessionID,
				Identity:        call.Runtime.Identity,
				Action:          action,
				Reason:          reason,
				Details:         strings.TrimSpace(input.Details),
				Risk:            strings.TrimSpace(input.Risk),
				Metadata:        model.CloneMetadata(input.Metadata),
			})
			if err != nil {
				return model.ToolResult{}, err
			}
			return approvalResult(action, decision), nil
		},
	}
}

type input struct {
	Action   string         `json:"action"`
	Reason   string         `json:"reason"`
	Details  string         `json:"details"`
	Risk     string         `json:"risk"`
	Metadata map[string]any `json:"metadata"`
}

func approvalResult(action string, decision Decision) model.ToolResult {
	metadata := model.CloneMetadata(decision.Metadata)
	if metadata == nil {
		metadata = map[string]any{}
	}
	reason := strings.TrimSpace(decision.Reason)
	metadata[MetadataApprovalOperation] = "request"
	metadata[MetadataApprovalAction] = action
	metadata[MetadataApprovalApproved] = decision.Approved
	if reason != "" {
		metadata[MetadataApprovalReason] = reason
	}
	status := "denied"
	if decision.Approved {
		status = "approved"
	}
	content := fmt.Sprintf("approval %s for %s", status, action)
	if reason != "" {
		content += ": " + reason
	}
	return model.ToolResult{
		Content:  content,
		IsError:  !decision.Approved,
		Metadata: metadata,
	}
}

func cloneDecision(decision Decision) Decision {
	decision.Metadata = model.CloneMetadata(decision.Metadata)
	return decision
}
