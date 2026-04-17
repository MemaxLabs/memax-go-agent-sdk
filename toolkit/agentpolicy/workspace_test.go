package agentpolicy

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/MemaxLabs/memax-go-agent-sdk/hook"
	"github.com/MemaxLabs/memax-go-agent-sdk/model"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/approvaltools"
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

func TestApprovalBeforeToolDeniesUntilApprovalGranted(t *testing.T) {
	policy := RequireApprovalBeforeTools(workspacetools.ApplyPatchToolName)
	denied, err := policy.BeforeToolUse(context.Background(), patchInput("session-1"))
	if err != nil {
		t.Fatalf("BeforeToolUse returned error: %v", err)
	}
	if denied.DenyReason != ApprovalBeforeToolReason(workspacetools.ApplyPatchToolName) {
		t.Fatalf("DenyReason = %q, want approval denial", denied.DenyReason)
	}
	if err := policy.AfterToolUse(context.Background(), approvalInput("session-1", workspacetools.ApplyPatchToolName, true)); err != nil {
		t.Fatalf("AfterToolUse returned error: %v", err)
	}
	allowed, err := policy.BeforeToolUse(context.Background(), patchInput("session-1"))
	if err != nil {
		t.Fatalf("BeforeToolUse after approval returned error: %v", err)
	}
	if allowed.DenyReason != "" {
		t.Fatalf("DenyReason = %q, want allowed after approval", allowed.DenyReason)
	}
}

func TestApprovalBeforeToolIgnoresDeniedApprovalAndOtherActions(t *testing.T) {
	policy := RequireApprovalBeforeTools(workspacetools.ApplyPatchToolName)
	for _, input := range []hook.AfterToolUseInput{
		approvalInput("session-1", workspacetools.ApplyPatchToolName, false),
		approvalInput("session-1", workspacetools.ReadToolName, true),
	} {
		if err := policy.AfterToolUse(context.Background(), input); err != nil {
			t.Fatalf("AfterToolUse returned error: %v", err)
		}
	}
	result, err := policy.BeforeToolUse(context.Background(), patchInput("session-1"))
	if err != nil {
		t.Fatalf("BeforeToolUse returned error: %v", err)
	}
	if result.DenyReason == "" {
		t.Fatalf("DenyReason empty, want denied approval and other action ignored")
	}
}

func TestApprovalBeforeToolSessionEndedResetsState(t *testing.T) {
	policy := RequireApprovalBeforeTools(workspacetools.ApplyPatchToolName)
	if err := policy.AfterToolUse(context.Background(), approvalInput("session-1", workspacetools.ApplyPatchToolName, true)); err != nil {
		t.Fatalf("AfterToolUse returned error: %v", err)
	}
	if err := policy.SessionEnded(context.Background(), hook.SessionEndedInput{SessionID: "session-1", Reason: hook.StopReasonResult}); err != nil {
		t.Fatalf("SessionEnded returned error: %v", err)
	}
	result, err := policy.BeforeToolUse(context.Background(), patchInput("session-1"))
	if err != nil {
		t.Fatalf("BeforeToolUse returned error: %v", err)
	}
	if result.DenyReason == "" {
		t.Fatalf("DenyReason empty, want approval state cleaned up")
	}
}

func TestApprovalBeforeToolSingleUseConsumesGrant(t *testing.T) {
	policy := RequireApprovalBeforeToolsWithOptions(
		[]string{workspacetools.ApplyPatchToolName},
		WithSingleUseApprovals(),
	)
	if err := policy.AfterToolUse(context.Background(), approvalInput("session-1", workspacetools.ApplyPatchToolName, true)); err != nil {
		t.Fatalf("AfterToolUse returned error: %v", err)
	}
	first, err := policy.BeforeToolUse(context.Background(), patchInput("session-1"))
	if err != nil {
		t.Fatalf("BeforeToolUse first returned error: %v", err)
	}
	if first.DenyReason != "" {
		t.Fatalf("DenyReason = %q, want first use allowed", first.DenyReason)
	}
	second, err := policy.BeforeToolUse(context.Background(), patchInput("session-1"))
	if err != nil {
		t.Fatalf("BeforeToolUse second returned error: %v", err)
	}
	if second.DenyReason == "" {
		t.Fatalf("DenyReason empty, want single-use approval consumed")
	}
}

func TestApprovalBeforeToolInputBoundApproval(t *testing.T) {
	policy := RequireApprovalBeforeToolsWithOptions(
		[]string{workspacetools.ApplyPatchToolName},
		WithInputBoundApprovals(),
	)
	approvedUse := model.ToolUse{
		Name:  workspacetools.ApplyPatchToolName,
		Input: json.RawMessage(`{"operations":[{"path":"README.md","new_content":"approved"}]}`),
	}
	approvedHash, err := hashToolInput(approvedUse.Input)
	if err != nil {
		t.Fatalf("hashToolInput returned error: %v", err)
	}
	if err := policy.AfterToolUse(context.Background(), approvalInputWithHash("session-1", workspacetools.ApplyPatchToolName, true, approvedHash)); err != nil {
		t.Fatalf("AfterToolUse returned error: %v", err)
	}
	denied, err := policy.BeforeToolUse(context.Background(), hook.BeforeToolUseInput{
		SessionID: "session-1",
		Use: model.ToolUse{
			Name:  workspacetools.ApplyPatchToolName,
			Input: json.RawMessage(`{"operations":[{"path":"README.md","new_content":"different"}]}`),
		},
	})
	if err != nil {
		t.Fatalf("BeforeToolUse denied returned error: %v", err)
	}
	if denied.DenyReason == "" {
		t.Fatalf("DenyReason empty, want mismatched input denied")
	}
	allowed, err := policy.BeforeToolUse(context.Background(), hook.BeforeToolUseInput{
		SessionID: "session-1",
		Use:       approvedUse,
	})
	if err != nil {
		t.Fatalf("BeforeToolUse allowed returned error: %v", err)
	}
	if allowed.DenyReason != "" {
		t.Fatalf("DenyReason = %q, want approved input allowed", allowed.DenyReason)
	}
}

func TestApprovalBeforeToolInputBoundApprovalWithoutToolInputDoesNotAuthorize(t *testing.T) {
	policy := RequireApprovalBeforeToolsWithOptions(
		[]string{workspacetools.ApplyPatchToolName},
		WithInputBoundApprovals(),
	)
	if err := policy.AfterToolUse(context.Background(), approvalInput("session-1", workspacetools.ApplyPatchToolName, true)); err != nil {
		t.Fatalf("AfterToolUse returned error: %v", err)
	}
	denied, err := policy.BeforeToolUse(context.Background(), patchInput("session-1"))
	if err != nil {
		t.Fatalf("BeforeToolUse returned error: %v", err)
	}
	if denied.DenyReason == "" {
		t.Fatalf("DenyReason empty, want input-bound approval without tool input to be unusable")
	}
}

func TestVerifyBeforeFinalDeniesUntilVerificationPasses(t *testing.T) {
	policy := RequireVerificationBeforeFinal()
	if err := policy.AfterToolUse(context.Background(), workspacePatchResult("session-1", false)); err != nil {
		t.Fatalf("AfterToolUse patch returned error: %v", err)
	}
	denied, err := policy.BeforeFinal(context.Background(), hook.BeforeFinalInput{
		SessionID: "session-1",
		Turn:      2,
		Answer:    "done",
	})
	if err != nil {
		t.Fatalf("BeforeFinal returned error: %v", err)
	}
	if denied.DenyReason != VerifyBeforeFinalReason() {
		t.Fatalf("DenyReason = %q, want verify-before-final denial", denied.DenyReason)
	}
	if err := policy.AfterToolUse(context.Background(), verificationResult("session-1", true)); err != nil {
		t.Fatalf("AfterToolUse verify returned error: %v", err)
	}
	allowed, err := policy.BeforeFinal(context.Background(), hook.BeforeFinalInput{
		SessionID: "session-1",
		Turn:      3,
		Answer:    "done",
	})
	if err != nil {
		t.Fatalf("BeforeFinal after verify returned error: %v", err)
	}
	if allowed.DenyReason != "" {
		t.Fatalf("DenyReason = %q, want final allowed after verification", allowed.DenyReason)
	}
}

func TestVerifyBeforeFinalIgnoresDryRunAndFailedTools(t *testing.T) {
	policy := RequireVerificationBeforeFinal()
	if err := policy.AfterToolUse(context.Background(), workspacePatchResult("session-1", true)); err != nil {
		t.Fatalf("AfterToolUse dry-run returned error: %v", err)
	}
	if err := policy.AfterToolUse(context.Background(), hook.AfterToolUseInput{
		SessionID: "session-1",
		Use:       model.ToolUse{Name: workspacetools.ApplyPatchToolName},
		Result: model.ToolResult{
			IsError: true,
			Metadata: map[string]any{
				model.MetadataWorkspaceOperation: "patch",
			},
		},
	}); err != nil {
		t.Fatalf("AfterToolUse failed patch returned error: %v", err)
	}
	result, err := policy.BeforeFinal(context.Background(), hook.BeforeFinalInput{SessionID: "session-1"})
	if err != nil {
		t.Fatalf("BeforeFinal returned error: %v", err)
	}
	if result.DenyReason != "" {
		t.Fatalf("DenyReason = %q, want dry-run and failed tool ignored", result.DenyReason)
	}
}

func TestVerifyBeforeFinalSessionEndedResetsState(t *testing.T) {
	policy := RequireVerificationBeforeFinal()
	if err := policy.AfterToolUse(context.Background(), workspacePatchResult("session-1", false)); err != nil {
		t.Fatalf("AfterToolUse patch returned error: %v", err)
	}
	if err := policy.SessionEnded(context.Background(), hook.SessionEndedInput{SessionID: "session-1", Reason: hook.StopReasonCanceled}); err != nil {
		t.Fatalf("SessionEnded returned error: %v", err)
	}
	result, err := policy.BeforeFinal(context.Background(), hook.BeforeFinalInput{SessionID: "session-1"})
	if err != nil {
		t.Fatalf("BeforeFinal returned error: %v", err)
	}
	if result.DenyReason != "" {
		t.Fatalf("DenyReason = %q, want cleanup to allow unrelated future final", result.DenyReason)
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

func workspacePatchResult(sessionID string, dryRun bool) hook.AfterToolUseInput {
	return hook.AfterToolUseInput{
		SessionID: sessionID,
		Use:       model.ToolUse{Name: workspacetools.ApplyPatchToolName},
		Result: model.ToolResult{Metadata: map[string]any{
			model.MetadataWorkspaceOperation: "patch",
			"dry_run":                        dryRun,
		}},
	}
}

func verificationResult(sessionID string, passed bool) hook.AfterToolUseInput {
	return hook.AfterToolUseInput{
		SessionID: sessionID,
		Use:       model.ToolUse{Name: verifytools.ToolName},
		Result: model.ToolResult{Metadata: map[string]any{
			model.MetadataVerificationOperation: "verify",
			model.MetadataVerificationPassed:    passed,
		}},
	}
}

func approvalInput(sessionID, action string, approved bool) hook.AfterToolUseInput {
	return approvalInputWithHash(sessionID, action, approved, "")
}

func approvalInputWithHash(sessionID, action string, approved bool, inputHash string) hook.AfterToolUseInput {
	metadata := map[string]any{
		approvaltools.MetadataApprovalOperation: "request",
		approvaltools.MetadataApprovalAction:    action,
		approvaltools.MetadataApprovalApproved:  approved,
	}
	if inputHash != "" {
		metadata[approvaltools.MetadataApprovalInputHash] = inputHash
	}
	return hook.AfterToolUseInput{
		SessionID: sessionID,
		Use:       model.ToolUse{Name: approvaltools.ToolName},
		Result: model.ToolResult{
			IsError:  !approved,
			Metadata: metadata,
		},
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
