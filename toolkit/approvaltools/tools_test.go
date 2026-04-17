package approvaltools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/MemaxLabs/memax-go-agent-sdk/identity"
	"github.com/MemaxLabs/memax-go-agent-sdk/model"
	"github.com/MemaxLabs/memax-go-agent-sdk/tool"
)

func TestApprovalToolGranted(t *testing.T) {
	var got Request
	approvalTool := NewTool(Config{
		Approver: ApproverFunc(func(_ context.Context, req Request) (Decision, error) {
			got = req
			return Decision{Approved: true, Reason: "approved for tests", Metadata: map[string]any{"ticket": "A-1"}}, nil
		}),
	})
	result, err := approvalTool.Execute(context.Background(), tool.Call{
		Use: model.ToolUse{
			ID:    "approval-1",
			Name:  ToolName,
			Input: json.RawMessage(`{"action":"workspace_apply_patch","reason":"edit README","details":"README.md","risk":"low","metadata":{"path":"README.md"}}`),
		},
		Runtime: tool.Runtime{
			SessionID:       "session-1",
			ParentSessionID: "parent-1",
			Identity:        identity.Identity{Name: "Zoe"},
		},
	})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if result.IsError || !strings.Contains(result.Content, "approval approved for workspace_apply_patch") {
		t.Fatalf("result = %#v, want approved result", result)
	}
	if got.SessionID != "session-1" || got.ParentSessionID != "parent-1" || got.Identity.Name != "Zoe" || got.Action != "workspace_apply_patch" || got.Reason != "edit README" || got.Details != "README.md" || got.Risk != "low" {
		t.Fatalf("request = %#v, want runtime and input context", got)
	}
	if result.Metadata[MetadataApprovalOperation] != "request" ||
		result.Metadata[MetadataApprovalAction] != "workspace_apply_patch" ||
		result.Metadata[MetadataApprovalApproved] != true ||
		result.Metadata[MetadataApprovalReason] != "approved for tests" ||
		result.Metadata["ticket"] != "A-1" {
		t.Fatalf("metadata = %#v, want approval metadata", result.Metadata)
	}
}

func TestApprovalToolDeniedIsToolError(t *testing.T) {
	approvalTool := NewTool(Config{
		Approver: StaticApprover{Decision: Decision{Approved: false, Reason: "too risky"}},
	})
	result, err := approvalTool.Execute(context.Background(), tool.Call{Use: model.ToolUse{
		ID:    "approval-1",
		Name:  ToolName,
		Input: json.RawMessage(`{"action":"deploy","reason":"needs production access"}`),
	}})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if !result.IsError || !strings.Contains(result.Content, "approval denied for deploy: too risky") {
		t.Fatalf("result = %#v, want denied tool error", result)
	}
	if result.Metadata[MetadataApprovalApproved] != false {
		t.Fatalf("metadata = %#v, want denied metadata", result.Metadata)
	}
}

func TestApprovalToolRequiresActionAndReason(t *testing.T) {
	approvalTool := NewTool(Config{Approver: StaticApprover{Decision: Decision{Approved: true}}})
	if _, err := approvalTool.Execute(context.Background(), tool.Call{Use: model.ToolUse{Name: ToolName, Input: json.RawMessage(`{"action":"","reason":"x"}`)}}); err == nil || !strings.Contains(err.Error(), "action is required") {
		t.Fatalf("missing action error = %v", err)
	}
	if _, err := approvalTool.Execute(context.Background(), tool.Call{Use: model.ToolUse{Name: ToolName, Input: json.RawMessage(`{"action":"x","reason":""}`)}}); err == nil || !strings.Contains(err.Error(), "reason is required") {
		t.Fatalf("missing reason error = %v", err)
	}
}

func TestStaticApproverReturnsDefensiveCopy(t *testing.T) {
	approver := StaticApprover{Decision: Decision{Approved: true, Metadata: map[string]any{"k": "v"}}}
	first, err := approver.RequestApproval(context.Background(), Request{})
	if err != nil {
		t.Fatalf("RequestApproval returned error: %v", err)
	}
	first.Metadata["k"] = "changed"
	second, err := approver.RequestApproval(context.Background(), Request{})
	if err != nil {
		t.Fatalf("RequestApproval second returned error: %v", err)
	}
	if second.Metadata["k"] != "v" {
		t.Fatalf("second metadata = %#v, want defensive copy", second.Metadata)
	}
}
