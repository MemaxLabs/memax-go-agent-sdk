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
			Input: json.RawMessage(`{"action":"workspace_apply_patch","reason":"edit README","details":"README.md","risk":"low","summary":{"title":"Review README patch","description":"Update README status","risk":"low","paths":["README.md","README.md"],"changes":1,"modified":1,"byte_delta":4},"tool_input":{"operations":[{"path":"README.md","new_content":"next"}]},"metadata":{"path":"README.md"}}`),
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
	if got.SessionID != "session-1" || got.ParentSessionID != "parent-1" || got.Identity.Name != "Zoe" || got.Action != "workspace_apply_patch" || got.Reason != "edit README" || got.Details != "README.md" || got.Risk != "low" || got.ToolInputHash == "" {
		t.Fatalf("request = %#v, want runtime and input context", got)
	}
	if got.Summary.Title != "Review README patch" || got.Summary.Description != "Update README status" || got.Summary.Risk != "low" || len(got.Summary.Paths) != 1 || got.Summary.Paths[0] != "README.md" || got.Summary.Changes != 1 || got.Summary.Modified != 1 || got.Summary.ByteDelta != 4 {
		t.Fatalf("summary = %#v, want normalized structured summary", got.Summary)
	}
	if result.Metadata[MetadataApprovalOperation] != "request" ||
		result.Metadata[MetadataApprovalAction] != "workspace_apply_patch" ||
		result.Metadata[MetadataApprovalApproved] != true ||
		result.Metadata[MetadataApprovalReason] != "approved for tests" ||
		result.Metadata[MetadataApprovalInputHash] != got.ToolInputHash ||
		result.Metadata[MetadataApprovalSummaryTitle] != "Review README patch" ||
		result.Metadata[MetadataApprovalSummaryPaths].([]string)[0] != "README.md" ||
		result.Metadata[MetadataApprovalSummaryChanges] != 1 ||
		result.Metadata[MetadataApprovalSummaryModified] != 1 ||
		result.Metadata["ticket"] != "A-1" {
		t.Fatalf("metadata = %#v, want approval metadata", result.Metadata)
	}
}

func TestApprovalToolHashesToolInputCanonically(t *testing.T) {
	var hashes []string
	approvalTool := NewTool(Config{
		Approver: ApproverFunc(func(_ context.Context, req Request) (Decision, error) {
			hashes = append(hashes, req.ToolInputHash)
			return Decision{Approved: true}, nil
		}),
	})
	for _, raw := range []string{
		`{"action":"tool","reason":"x","tool_input":{"b":2,"a":1}}`,
		`{"action":"tool","reason":"x","tool_input":{"a":1,"b":2}}`,
	} {
		if _, err := approvalTool.Execute(context.Background(), tool.Call{Use: model.ToolUse{Name: ToolName, Input: json.RawMessage(raw)}}); err != nil {
			t.Fatalf("Execute returned error: %v", err)
		}
	}
	if len(hashes) != 2 || hashes[0] == "" || hashes[0] != hashes[1] {
		t.Fatalf("hashes = %#v, want equal canonical input hashes", hashes)
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
