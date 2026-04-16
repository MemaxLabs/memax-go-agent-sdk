// Package agentpolicy provides composable host-owned policy presets for common
// agent workflows. Policies are implemented as hooks and helpers rather than
// hidden core behavior, so applications can combine them with their own
// permission, review, and telemetry layers.
package agentpolicy

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/MemaxLabs/memax-go-agent-sdk/hook"
	"github.com/MemaxLabs/memax-go-agent-sdk/model"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/verifytools"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/workspacetools"
)

const checkpointBeforePatchReason = "create a workspace checkpoint before applying patches"

const (
	// MetadataRollbackRecommended marks verification results where rollback
	// guidance was attached by RollbackOnFailedVerification.
	MetadataRollbackRecommended = "rollback_recommended"
	// MetadataRollbackCheckpointID carries the checkpoint ID recommended for
	// rollback after failed verification.
	MetadataRollbackCheckpointID = "rollback_checkpoint_id"
)

// CheckpointBeforePatch denies mutating workspace patches until the current
// session has successfully created a workspace checkpoint. Dry-run patch
// previews are allowed because they do not mutate workspace state.
type CheckpointBeforePatch struct {
	mu           sync.RWMutex
	checkpointed map[string]bool
}

// RequireCheckpointBeforePatch returns a policy that can be installed into a
// hook runner with hook.NewRunner(policy.Options()...).
func RequireCheckpointBeforePatch() *CheckpointBeforePatch {
	return &CheckpointBeforePatch{checkpointed: map[string]bool{}}
}

// Options returns hook options for this policy.
func (p *CheckpointBeforePatch) Options() []hook.Option {
	return []hook.Option{
		hook.WithBeforeToolUse(p.BeforeToolUse),
		hook.WithAfterToolUse(p.AfterToolUse),
		hook.WithSessionEnded(p.SessionEnded),
	}
}

// BeforeToolUse denies workspace patches until a checkpoint has been observed
// in the same session.
func (p *CheckpointBeforePatch) BeforeToolUse(ctx context.Context, input hook.BeforeToolUseInput) (hook.BeforeToolUseResult, error) {
	if err := ctx.Err(); err != nil {
		return hook.BeforeToolUseResult{}, err
	}
	if input.Use.Name != workspacetools.ApplyPatchToolName {
		return hook.BeforeToolUseResult{}, nil
	}
	if patchDryRun(input.Use.Input) {
		return hook.BeforeToolUseResult{}, nil
	}
	if p.hasCheckpoint(input.SessionID) {
		return hook.BeforeToolUseResult{}, nil
	}
	return hook.BeforeToolUseResult{DenyReason: checkpointBeforePatchReason}, nil
}

// AfterToolUse records successful checkpoint creation.
func (p *CheckpointBeforePatch) AfterToolUse(ctx context.Context, input hook.AfterToolUseInput) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if input.Result.IsError || input.Use.Name != workspacetools.CheckpointToolName {
		return nil
	}
	if operation, _ := input.Result.Metadata[model.MetadataWorkspaceOperation].(string); operation != "checkpoint" {
		return nil
	}
	if input.SessionID == "" {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.checkpointed == nil {
		p.checkpointed = map[string]bool{}
	}
	p.checkpointed[input.SessionID] = true
	return nil
}

// SessionEnded removes checkpoint state for the completed session.
func (p *CheckpointBeforePatch) SessionEnded(_ context.Context, input hook.SessionEndedInput) error {
	p.Reset(input.SessionID)
	return nil
}

// Reset removes checkpoint state for sessionID. It is safe to call on a nil
// policy or with an empty session ID.
func (p *CheckpointBeforePatch) Reset(sessionID string) {
	if p == nil || sessionID == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.checkpointed, sessionID)
}

func (p *CheckpointBeforePatch) hasCheckpoint(sessionID string) bool {
	if sessionID == "" {
		return false
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.checkpointed[sessionID]
}

func patchDryRun(input json.RawMessage) bool {
	var value struct {
		DryRun bool `json:"dry_run"`
	}
	if err := json.Unmarshal(input, &value); err != nil {
		return false
	}
	return value.DryRun
}

// CheckpointBeforePatchReason returns the model-visible denial reason used by
// RequireCheckpointBeforePatch. It is exported for tests and host UI copy.
func CheckpointBeforePatchReason() string {
	return checkpointBeforePatchReason
}

// RollbackOnFailedVerification records the latest workspace checkpoint for each
// session and wraps a verifier so failed checks include model-visible rollback
// guidance. The policy does not restore automatically; the model must call the
// normal workspace restore tool, keeping rollback explicit and observable.
type RollbackOnFailedVerification struct {
	mu          sync.RWMutex
	checkpoints map[string]string
}

// RecommendRollbackOnFailedVerification returns a rollback recommendation
// policy. Install Options() into the hook runner and wrap the verification
// tool's verifier with WrapVerifier.
func RecommendRollbackOnFailedVerification() *RollbackOnFailedVerification {
	return &RollbackOnFailedVerification{checkpoints: map[string]string{}}
}

// Options returns hook options for recording checkpoint lifecycle state.
func (p *RollbackOnFailedVerification) Options() []hook.Option {
	return []hook.Option{
		hook.WithAfterToolUse(p.AfterToolUse),
		hook.WithSessionEnded(p.SessionEnded),
	}
}

// WrapVerifier returns a verifier that appends rollback guidance to failed
// verification results when this policy has observed a checkpoint for the
// verification request's session.
func (p *RollbackOnFailedVerification) WrapVerifier(verifier verifytools.Verifier) verifytools.Verifier {
	return verifytools.VerifierFunc(func(ctx context.Context, req verifytools.Request) (verifytools.Result, error) {
		if verifier == nil {
			return verifytools.Result{}, fmt.Errorf("agentpolicy: verifier is required")
		}
		result, err := verifier.Verify(ctx, req)
		if err != nil || result.Passed {
			return result, err
		}
		checkpointID := p.latestCheckpoint(req.SessionID)
		if checkpointID == "" {
			return result, nil
		}
		metadata := model.CloneMetadata(result.Metadata)
		if metadata == nil {
			metadata = map[string]any{}
		}
		metadata[MetadataRollbackRecommended] = true
		metadata[MetadataRollbackCheckpointID] = checkpointID
		result.Metadata = metadata
		instruction := fmt.Sprintf("Rollback policy: restore workspace checkpoint %s before continuing, then repair and verify again.", checkpointID)
		if result.Output == "" {
			result.Output = instruction
		} else {
			result.Output += "\n" + instruction
		}
		return result, nil
	})
}

// AfterToolUse records successful workspace checkpoints.
func (p *RollbackOnFailedVerification) AfterToolUse(ctx context.Context, input hook.AfterToolUseInput) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if input.Result.IsError || input.Use.Name != workspacetools.CheckpointToolName {
		return nil
	}
	if operation, _ := input.Result.Metadata[model.MetadataWorkspaceOperation].(string); operation != "checkpoint" {
		return nil
	}
	checkpointID, _ := input.Result.Metadata[model.MetadataWorkspaceCheckpointID].(string)
	if input.SessionID == "" || checkpointID == "" {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.checkpoints == nil {
		p.checkpoints = map[string]string{}
	}
	p.checkpoints[input.SessionID] = checkpointID
	return nil
}

// SessionEnded removes checkpoint state for the completed session.
func (p *RollbackOnFailedVerification) SessionEnded(_ context.Context, input hook.SessionEndedInput) error {
	p.Reset(input.SessionID)
	return nil
}

// Reset removes rollback checkpoint state for sessionID. It is safe to call on
// a nil policy or with an empty session ID.
func (p *RollbackOnFailedVerification) Reset(sessionID string) {
	if p == nil || sessionID == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.checkpoints, sessionID)
}

func (p *RollbackOnFailedVerification) latestCheckpoint(sessionID string) string {
	if p == nil || sessionID == "" {
		return ""
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.checkpoints[sessionID]
}
