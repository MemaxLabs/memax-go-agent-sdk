// Package agentpolicy provides composable host-owned policy presets for common
// agent workflows. Policies are implemented as hooks and helpers rather than
// hidden core behavior, so applications can combine them with their own
// permission, review, and telemetry layers.
package agentpolicy

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/MemaxLabs/memax-go-agent-sdk/hook"
	"github.com/MemaxLabs/memax-go-agent-sdk/model"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/workspacetools"
)

const checkpointBeforePatchReason = "create a workspace checkpoint before applying patches"

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
