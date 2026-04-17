// Package approvaltools provides explicit host approval tools. Approval is
// modeled as a normal tool result so the request, decision, and reason are
// visible in the transcript and can be combined with policy hooks.
package approvaltools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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
	MetadataApprovalOperation = model.MetadataApprovalOperation
	// MetadataApprovalAction carries the action or tool name being approved.
	MetadataApprovalAction = model.MetadataApprovalAction
	// MetadataApprovalApproved records whether approval was granted.
	MetadataApprovalApproved = model.MetadataApprovalApproved
	// MetadataApprovalReason carries a host-visible approval or denial reason.
	MetadataApprovalReason = model.MetadataApprovalReason
	// MetadataApprovalInputHash carries the canonical JSON hash of the proposed
	// tool input, when the approval request includes tool_input.
	MetadataApprovalInputHash = model.MetadataApprovalInputHash
	// MetadataApprovalSummaryTitle carries a short host-facing approval summary.
	MetadataApprovalSummaryTitle = model.MetadataApprovalSummaryTitle
	// MetadataApprovalSummaryDescription carries optional approval summary details.
	MetadataApprovalSummaryDescription = model.MetadataApprovalSummaryDescription
	// MetadataApprovalSummaryRisk carries optional approval summary risk text.
	MetadataApprovalSummaryRisk = model.MetadataApprovalSummaryRisk
	// MetadataApprovalSummaryPaths carries affected paths for the proposed action.
	MetadataApprovalSummaryPaths = model.MetadataApprovalSummaryPaths
	// MetadataApprovalSummaryChanges carries the number of proposed changes.
	MetadataApprovalSummaryChanges = model.MetadataApprovalSummaryChanges
	// MetadataApprovalSummaryAdded carries the number of proposed added files.
	MetadataApprovalSummaryAdded = model.MetadataApprovalSummaryAdded
	// MetadataApprovalSummaryModified carries the number of proposed modified files.
	MetadataApprovalSummaryModified = model.MetadataApprovalSummaryModified
	// MetadataApprovalSummaryDeleted carries the number of proposed deleted files.
	MetadataApprovalSummaryDeleted = model.MetadataApprovalSummaryDeleted
	// MetadataApprovalSummaryByteDelta carries the proposed byte delta.
	MetadataApprovalSummaryByteDelta = model.MetadataApprovalSummaryByteDelta
)

// Summary is a compact host-facing description of a requested approval. It is
// optional, but approval UIs and audit logs should prefer it over free-form
// Details when present.
type Summary struct {
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Risk        string   `json:"risk"`
	Paths       []string `json:"paths"`
	Changes     int      `json:"changes"`
	Added       int      `json:"added"`
	Modified    int      `json:"modified"`
	Deleted     int      `json:"deleted"`
	ByteDelta   int      `json:"byte_delta"`
}

// Request describes one approval request from the model to the host.
type Request struct {
	SessionID       string
	ParentSessionID string
	Identity        identity.Identity
	Action          string
	Reason          string
	Details         string
	Risk            string
	Summary         Summary
	ToolInput       json.RawMessage
	ToolInputHash   string
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
					"summary": map[string]any{
						"type":        "object",
						"description": "Optional structured host-facing summary of the approval request.",
						"properties": map[string]any{
							"title": map[string]any{
								"type":        "string",
								"description": "Short approval title.",
							},
							"description": map[string]any{
								"type":        "string",
								"description": "Concrete description of the requested action.",
							},
							"risk": map[string]any{
								"type":        "string",
								"description": "Risk level or risk explanation.",
							},
							"paths": map[string]any{
								"type":        "array",
								"description": "Affected paths, if any.",
								"items": map[string]any{
									"type": "string",
								},
							},
							"changes":    map[string]any{"type": "integer"},
							"added":      map[string]any{"type": "integer"},
							"modified":   map[string]any{"type": "integer"},
							"deleted":    map[string]any{"type": "integer"},
							"byte_delta": map[string]any{"type": "integer"},
						},
					},
					"tool_input": map[string]any{
						"type":        "object",
						"description": "Optional exact proposed input for the approved tool call. Policies can bind approval to its canonical hash.",
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
			toolInputHash, err := hashRawJSON(input.ToolInput)
			if err != nil {
				return model.ToolResult{}, fmt.Errorf("approvaltools: tool_input: %w", err)
			}
			decision, err := config.Approver.RequestApproval(ctx, Request{
				SessionID:       call.Runtime.SessionID,
				ParentSessionID: call.Runtime.ParentSessionID,
				Identity:        call.Runtime.Identity,
				Action:          action,
				Reason:          reason,
				Details:         strings.TrimSpace(input.Details),
				Risk:            strings.TrimSpace(input.Risk),
				Summary:         normalizeSummary(input.Summary),
				ToolInput:       cloneRawMessage(input.ToolInput),
				ToolInputHash:   toolInputHash,
				Metadata:        model.CloneMetadata(input.Metadata),
			})
			if err != nil {
				return model.ToolResult{}, err
			}
			return approvalResult(action, toolInputHash, normalizeSummary(input.Summary), decision), nil
		},
	}
}

type input struct {
	Action    string          `json:"action"`
	Reason    string          `json:"reason"`
	Details   string          `json:"details"`
	Risk      string          `json:"risk"`
	Summary   Summary         `json:"summary"`
	ToolInput json.RawMessage `json:"tool_input"`
	Metadata  map[string]any  `json:"metadata"`
}

func approvalResult(action, toolInputHash string, summary Summary, decision Decision) model.ToolResult {
	metadata := model.CloneMetadata(decision.Metadata)
	if metadata == nil {
		metadata = map[string]any{}
	}
	reason := strings.TrimSpace(decision.Reason)
	metadata[MetadataApprovalOperation] = "request"
	metadata[MetadataApprovalAction] = action
	metadata[MetadataApprovalApproved] = decision.Approved
	if toolInputHash != "" {
		metadata[MetadataApprovalInputHash] = toolInputHash
	}
	addSummaryMetadata(metadata, summary)
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

func normalizeSummary(summary Summary) Summary {
	summary.Title = strings.TrimSpace(summary.Title)
	summary.Description = strings.TrimSpace(summary.Description)
	summary.Risk = strings.TrimSpace(summary.Risk)
	if len(summary.Paths) > 0 {
		paths := make([]string, 0, len(summary.Paths))
		seen := map[string]struct{}{}
		for _, path := range summary.Paths {
			path = strings.TrimSpace(path)
			if path == "" {
				continue
			}
			if _, ok := seen[path]; ok {
				continue
			}
			seen[path] = struct{}{}
			paths = append(paths, path)
		}
		summary.Paths = paths
	}
	return summary
}

func addSummaryMetadata(metadata map[string]any, summary Summary) {
	if summary.Title != "" {
		metadata[MetadataApprovalSummaryTitle] = summary.Title
	}
	if summary.Description != "" {
		metadata[MetadataApprovalSummaryDescription] = summary.Description
	}
	if summary.Risk != "" {
		metadata[MetadataApprovalSummaryRisk] = summary.Risk
	}
	if len(summary.Paths) > 0 {
		metadata[MetadataApprovalSummaryPaths] = append([]string(nil), summary.Paths...)
	}
	if summary.Changes != 0 {
		metadata[MetadataApprovalSummaryChanges] = summary.Changes
	}
	if summary.Added != 0 {
		metadata[MetadataApprovalSummaryAdded] = summary.Added
	}
	if summary.Modified != 0 {
		metadata[MetadataApprovalSummaryModified] = summary.Modified
	}
	if summary.Deleted != 0 {
		metadata[MetadataApprovalSummaryDeleted] = summary.Deleted
	}
	if summary.ByteDelta != 0 {
		metadata[MetadataApprovalSummaryByteDelta] = summary.ByteDelta
	}
}

func hashRawJSON(input json.RawMessage) (string, error) {
	if len(input) == 0 {
		return "", nil
	}
	var value any
	if err := json.Unmarshal(input, &value); err != nil {
		return "", err
	}
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func cloneRawMessage(input json.RawMessage) json.RawMessage {
	if input == nil {
		return nil
	}
	return append(json.RawMessage(nil), input...)
}
