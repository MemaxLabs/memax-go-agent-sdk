package agentpolicy

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/MemaxLabs/memax-go-agent-sdk/hook"
	"github.com/MemaxLabs/memax-go-agent-sdk/model"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/verifytools"
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

func TestCheckpointBeforePatchSessionEndedResetsAllStopReasons(t *testing.T) {
	reasons := []hook.StopReason{
		hook.StopReasonResult,
		hook.StopReasonError,
		hook.StopReasonMaxTurns,
		hook.StopReasonBudget,
		hook.StopReasonCanceled,
	}
	for _, reason := range reasons {
		t.Run(string(reason), func(t *testing.T) {
			policy := RequireCheckpointBeforePatch()
			sessionID := "session-" + string(reason)
			if err := policy.AfterToolUse(context.Background(), checkpointInput(sessionID, "checkpoint-1")); err != nil {
				t.Fatalf("AfterToolUse returned error: %v", err)
			}
			allowed, err := policy.BeforeToolUse(context.Background(), patchInput(sessionID))
			if err != nil {
				t.Fatalf("BeforeToolUse returned error: %v", err)
			}
			if allowed.DenyReason != "" {
				t.Fatalf("DenyReason = %q, want allowed before cleanup", allowed.DenyReason)
			}
			if err := policy.SessionEnded(context.Background(), hook.SessionEndedInput{SessionID: sessionID, Reason: reason}); err != nil {
				t.Fatalf("SessionEnded returned error: %v", err)
			}
			denied, err := policy.BeforeToolUse(context.Background(), patchInput(sessionID))
			if err != nil {
				t.Fatalf("BeforeToolUse after cleanup returned error: %v", err)
			}
			if denied.DenyReason != CheckpointBeforePatchReason() {
				t.Fatalf("DenyReason = %q, want checkpoint denial after cleanup", denied.DenyReason)
			}
		})
	}
}

func TestCheckpointBeforePatchReset(t *testing.T) {
	policy := RequireCheckpointBeforePatch()
	if err := policy.AfterToolUse(context.Background(), checkpointInput("session-1", "checkpoint-1")); err != nil {
		t.Fatalf("AfterToolUse returned error: %v", err)
	}
	policy.Reset("session-1")
	result, err := policy.BeforeToolUse(context.Background(), patchInput("session-1"))
	if err != nil {
		t.Fatalf("BeforeToolUse returned error: %v", err)
	}
	if result.DenyReason == "" {
		t.Fatalf("DenyReason empty, want denial after reset")
	}
}

func TestRollbackOnFailedVerificationAddsGuidance(t *testing.T) {
	policy := RecommendRollbackOnFailedVerification()
	if err := policy.AfterToolUse(context.Background(), checkpointInput("session-1", "checkpoint-9")); err != nil {
		t.Fatalf("AfterToolUse returned error: %v", err)
	}
	verifier := policy.WrapVerifier(verifytools.VerifierFunc(func(context.Context, verifytools.Request) (verifytools.Result, error) {
		return verifytools.Result{
			Name:   "test",
			Passed: false,
			Output: "verification failed",
		}, nil
	}))

	result, err := verifier.Verify(context.Background(), verifytools.Request{SessionID: "session-1", Name: "test"})
	if err != nil {
		t.Fatalf("Verify returned error: %v", err)
	}
	if result.Metadata[MetadataRollbackRecommended] != true {
		t.Fatalf("Metadata = %#v, want rollback recommendation", result.Metadata)
	}
	if result.Metadata[MetadataRollbackCheckpointID] != "checkpoint-9" {
		t.Fatalf("Metadata = %#v, want checkpoint ID", result.Metadata)
	}
	if !strings.Contains(result.Output, "restore workspace checkpoint checkpoint-9") {
		t.Fatalf("Output = %q, want rollback guidance", result.Output)
	}
}

func TestRollbackOnFailedVerificationSkipsPassingResults(t *testing.T) {
	policy := RecommendRollbackOnFailedVerification()
	if err := policy.AfterToolUse(context.Background(), checkpointInput("session-1", "checkpoint-1")); err != nil {
		t.Fatalf("AfterToolUse returned error: %v", err)
	}
	verifier := policy.WrapVerifier(verifytools.VerifierFunc(func(context.Context, verifytools.Request) (verifytools.Result, error) {
		return verifytools.Result{Name: "test", Passed: true, Output: "ok"}, nil
	}))

	result, err := verifier.Verify(context.Background(), verifytools.Request{SessionID: "session-1", Name: "test"})
	if err != nil {
		t.Fatalf("Verify returned error: %v", err)
	}
	if result.Metadata[MetadataRollbackRecommended] != nil || strings.Contains(result.Output, "Rollback policy") {
		t.Fatalf("result = %#v, want no rollback guidance for passing verification", result)
	}
}

func TestRollbackOnFailedVerificationSessionEndedResetsState(t *testing.T) {
	policy := RecommendRollbackOnFailedVerification()
	if err := policy.AfterToolUse(context.Background(), checkpointInput("session-1", "checkpoint-1")); err != nil {
		t.Fatalf("AfterToolUse returned error: %v", err)
	}
	if err := policy.SessionEnded(context.Background(), hook.SessionEndedInput{SessionID: "session-1", Reason: hook.StopReasonResult}); err != nil {
		t.Fatalf("SessionEnded returned error: %v", err)
	}
	verifier := policy.WrapVerifier(verifytools.VerifierFunc(func(context.Context, verifytools.Request) (verifytools.Result, error) {
		return verifytools.Result{Name: "test", Passed: false, Output: "failed"}, nil
	}))

	result, err := verifier.Verify(context.Background(), verifytools.Request{SessionID: "session-1", Name: "test"})
	if err != nil {
		t.Fatalf("Verify returned error: %v", err)
	}
	if result.Metadata[MetadataRollbackRecommended] != nil || strings.Contains(result.Output, "Rollback policy") {
		t.Fatalf("result = %#v, want no rollback guidance after cleanup", result)
	}
}

func checkpointInput(sessionID, checkpointID string) hook.AfterToolUseInput {
	return hook.AfterToolUseInput{
		SessionID: sessionID,
		Use:       model.ToolUse{Name: workspacetools.CheckpointToolName},
		Result: model.ToolResult{Metadata: map[string]any{
			model.MetadataWorkspaceOperation:    "checkpoint",
			model.MetadataWorkspaceCheckpointID: checkpointID,
		}},
	}
}

func patchInput(sessionID string) hook.BeforeToolUseInput {
	return hook.BeforeToolUseInput{
		SessionID: sessionID,
		Use: model.ToolUse{
			Name:  workspacetools.ApplyPatchToolName,
			Input: json.RawMessage(`{"operations":[{"path":"README.md","new_content":"next"}]}`),
		},
	}
}
