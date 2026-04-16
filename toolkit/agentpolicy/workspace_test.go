package agentpolicy

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/MemaxLabs/memax-go-agent-sdk/hook"
	"github.com/MemaxLabs/memax-go-agent-sdk/model"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/workspacetools"
)

func TestCheckpointBeforePatchDeniesUntilCheckpoint(t *testing.T) {
	policy := RequireCheckpointBeforePatch()
	runner := hook.NewRunner(policy.Options()...)

	result, err := runner.BeforeToolUse(context.Background(), hook.BeforeToolUseInput{
		SessionID: "session-1",
		Use: model.ToolUse{
			Name:  workspacetools.ApplyPatchToolName,
			Input: json.RawMessage(`{"operations":[{"path":"README.md","new_content":"next"}]}`),
		},
	})
	if err != nil {
		t.Fatalf("BeforeToolUse returned error: %v", err)
	}
	if result.DenyReason != CheckpointBeforePatchReason() {
		t.Fatalf("DenyReason = %q, want checkpoint denial", result.DenyReason)
	}

	errs := runner.AfterToolUse(context.Background(), hook.AfterToolUseInput{
		SessionID: "session-1",
		Use:       model.ToolUse{Name: workspacetools.CheckpointToolName},
		Result: model.ToolResult{Metadata: map[string]any{
			model.MetadataWorkspaceOperation: "checkpoint",
		}},
	})
	if len(errs) > 0 {
		t.Fatalf("AfterToolUse returned errors: %v", errs)
	}

	result, err = runner.BeforeToolUse(context.Background(), hook.BeforeToolUseInput{
		SessionID: "session-1",
		Use: model.ToolUse{
			Name:  workspacetools.ApplyPatchToolName,
			Input: json.RawMessage(`{"operations":[{"path":"README.md","new_content":"next"}]}`),
		},
	})
	if err != nil {
		t.Fatalf("BeforeToolUse returned error: %v", err)
	}
	if result.DenyReason != "" {
		t.Fatalf("DenyReason = %q, want allowed after checkpoint", result.DenyReason)
	}
}

func TestCheckpointBeforePatchAllowsDryRun(t *testing.T) {
	policy := RequireCheckpointBeforePatch()
	result, err := policy.BeforeToolUse(context.Background(), hook.BeforeToolUseInput{
		SessionID: "session-1",
		Use: model.ToolUse{
			Name:  workspacetools.ApplyPatchToolName,
			Input: json.RawMessage(`{"dry_run":true,"operations":[{"path":"README.md","new_content":"next"}]}`),
		},
	})
	if err != nil {
		t.Fatalf("BeforeToolUse returned error: %v", err)
	}
	if result.DenyReason != "" {
		t.Fatalf("DenyReason = %q, want dry-run allowed", result.DenyReason)
	}
}

func TestCheckpointBeforePatchIsSessionScoped(t *testing.T) {
	policy := RequireCheckpointBeforePatch()
	if err := policy.AfterToolUse(context.Background(), hook.AfterToolUseInput{
		SessionID: "session-1",
		Use:       model.ToolUse{Name: workspacetools.CheckpointToolName},
		Result: model.ToolResult{Metadata: map[string]any{
			model.MetadataWorkspaceOperation: "checkpoint",
		}},
	}); err != nil {
		t.Fatalf("AfterToolUse returned error: %v", err)
	}

	result, err := policy.BeforeToolUse(context.Background(), hook.BeforeToolUseInput{
		SessionID: "session-2",
		Use: model.ToolUse{
			Name:  workspacetools.ApplyPatchToolName,
			Input: json.RawMessage(`{"operations":[{"path":"README.md","new_content":"next"}]}`),
		},
	})
	if err != nil {
		t.Fatalf("BeforeToolUse returned error: %v", err)
	}
	if result.DenyReason == "" {
		t.Fatalf("DenyReason empty, want session-scoped denial")
	}
}
