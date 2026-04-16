package workspacetools

import (
	"context"
	"fmt"
	"strings"

	"github.com/MemaxLabs/memax-go-agent-sdk/model"
	"github.com/MemaxLabs/memax-go-agent-sdk/tool"
	"github.com/MemaxLabs/memax-go-agent-sdk/workspace"
)

const (
	ReadToolName       = "workspace_read_file"
	ListToolName       = "workspace_list_files"
	ApplyPatchToolName = "workspace_apply_patch"
	DiffToolName       = "workspace_diff"
	CheckpointToolName = "workspace_checkpoint"
	RestoreToolName    = "workspace_restore"
)

// Reader is the workspace read capability required by NewReadTool.
type Reader interface {
	ReadFile(context.Context, string) (string, error)
}

// Lister is the workspace list capability required by NewListTool.
type Lister interface {
	ListFiles(context.Context, string) ([]string, error)
}

// Patcher is the guarded patch capability required by NewApplyPatchTool.
type Patcher interface {
	ApplyPatch(context.Context, []workspace.PatchOperation) (workspace.PatchResult, error)
}

// PatchPreviewer is an optional extension that validates guarded operations
// without mutating workspace state.
type PatchPreviewer interface {
	PreviewPatch(context.Context, []workspace.PatchOperation) (workspace.PatchResult, error)
}

// UnifiedDiffPatcher is an optional extension for applying standard unified
// diffs. Stores that do not implement it can still use structured operations.
type UnifiedDiffPatcher interface {
	ApplyUnifiedDiff(context.Context, string, workspace.PatchOptions) (workspace.PatchResult, error)
}

// PatchReviewRequest is sent to a reviewer after a patch has been validated
// and previewed but before mutation.
type PatchReviewRequest struct {
	ToolUse model.ToolUse
	DryRun  bool
	Summary workspace.PatchSummary
	Changes []workspace.Change
}

// PatchReviewDecision controls whether a reviewed patch may be applied.
type PatchReviewDecision struct {
	Allow  bool
	Reason string
}

// PatchReviewer optionally gates workspace patch mutation with structured
// file-change context. A denied review returns a normal tool error, giving the
// model a chance to recover without mutating the workspace.
type PatchReviewer interface {
	ReviewPatch(context.Context, PatchReviewRequest) PatchReviewDecision
}

// PatchReviewerFunc adapts a function into a PatchReviewer.
type PatchReviewerFunc func(context.Context, PatchReviewRequest) PatchReviewDecision

func (f PatchReviewerFunc) ReviewPatch(ctx context.Context, req PatchReviewRequest) PatchReviewDecision {
	if f == nil {
		return PatchReviewDecision{Allow: true}
	}
	return f(ctx, req)
}

// Differ is the diff capability required by NewDiffTool.
type Differ interface {
	Diff(context.Context, string) (workspace.Diff, error)
}

// Checkpointer is the checkpoint creation capability required by
// NewCheckpointTool.
type Checkpointer interface {
	Checkpoint(context.Context, workspace.CheckpointOptions) (workspace.Checkpoint, error)
}

// Restorer is the checkpoint restore capability required by NewRestoreTool.
type Restorer interface {
	Restore(context.Context, string) (workspace.Checkpoint, error)
}

// NewTools returns the standard workspace tool set over store. Use the
// individual constructors when a host wants to expose only a subset of
// workspace capabilities, such as read/list without patch or restore.
func NewTools(store workspace.Store) ([]tool.Tool, error) {
	if store == nil {
		return nil, fmt.Errorf("workspacetools: store is required")
	}
	return []tool.Tool{
		NewReadTool(store),
		NewListTool(store),
		NewApplyPatchTool(store),
		NewDiffTool(store),
		NewCheckpointTool(store),
		NewRestoreTool(store),
	}, nil
}

// NewReadTool returns a read-only tool for reading workspace files.
func NewReadTool(store Reader) tool.Tool {
	return tool.Definition{
		ToolSpec: model.ToolSpec{
			Name:            ReadToolName,
			Description:     "Read a text file from the configured workspace.",
			SearchHint:      "read workspace file source code",
			ReadOnly:        true,
			ConcurrencySafe: true,
			MaxResultBytes:  64 * 1024,
			InputSchema:     pathInputSchema("Workspace-relative file path."),
		},
		Handler: func(ctx context.Context, call tool.Call) (model.ToolResult, error) {
			input, err := tool.DecodeInput[pathInput](call.Use)
			if err != nil {
				return model.ToolResult{}, err
			}
			content, err := store.ReadFile(ctx, input.Path)
			if err != nil {
				return model.ToolResult{}, err
			}
			return model.ToolResult{Content: content}, nil
		},
	}
}

// NewListTool returns a read-only tool for listing workspace files.
func NewListTool(store Lister) tool.Tool {
	return tool.Definition{
		ToolSpec: model.ToolSpec{
			Name:            ListToolName,
			Description:     "List files in the configured workspace.",
			SearchHint:      "list workspace files source code",
			ReadOnly:        true,
			ConcurrencySafe: true,
			MaxResultBytes:  32 * 1024,
			InputSchema: map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"properties": map[string]any{
					"prefix": map[string]any{
						"type":        "string",
						"description": "Optional workspace-relative prefix.",
					},
				},
			},
		},
		Handler: func(ctx context.Context, call tool.Call) (model.ToolResult, error) {
			input, err := tool.DecodeInput[listInput](call.Use)
			if err != nil {
				return model.ToolResult{}, err
			}
			files, err := store.ListFiles(ctx, input.Prefix)
			if err != nil {
				return model.ToolResult{}, err
			}
			return model.ToolResult{Content: strings.Join(files, "\n"), Metadata: map[string]any{"count": len(files)}}, nil
		},
	}
}

// NewApplyPatchTool returns a destructive tool for applying guarded atomic
// workspace patches. The tool accepts either structured operations or a
// standard unified diff. Dry-run requests require a store that implements
// PatchPreviewer for structured operations or UnifiedDiffPatcher for diffs.
func NewApplyPatchTool(store Patcher) tool.Tool {
	return NewApplyPatchToolWithReview(store, nil)
}

// NewApplyPatchToolWithReview returns a destructive patch tool that validates
// and previews the requested change, passes the summary to reviewer, and only
// mutates the workspace when the reviewer allows it. Dry-run requests never
// mutate but still invoke the reviewer so hosts can audit proposed changes.
func NewApplyPatchToolWithReview(store Patcher, reviewer PatchReviewer) tool.Tool {
	return tool.Definition{
		ToolSpec: model.ToolSpec{
			Name:           ApplyPatchToolName,
			Description:    "Apply guarded file edits or a standard unified diff to the configured workspace. Set dry_run to validate and preview without mutating files.",
			SearchHint:     "apply patch edit write workspace files source code",
			Destructive:    true,
			MaxResultBytes: 32 * 1024,
			InputSchema: map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"properties": map[string]any{
					"operations": map[string]any{
						"type":        "array",
						"minItems":    1,
						"description": "Atomic file operations. If any old_content guard fails, no operation is applied.",
						"items": map[string]any{
							"type":                 "object",
							"required":             []any{"path"},
							"additionalProperties": false,
							"properties": map[string]any{
								"path": map[string]any{
									"type":        "string",
									"description": "Workspace-relative file path.",
									"minLength":   1,
								},
								"old_content": map[string]any{
									"type":        "string",
									"description": "Optional guard requiring the current file content to match exactly.",
								},
								"new_content": map[string]any{
									"type":        "string",
									"description": "New file content. Omit when delete is true.",
								},
								"delete": map[string]any{
									"type":        "boolean",
									"description": "Delete the file instead of writing new_content.",
								},
							},
						},
					},
					"unified_diff": map[string]any{
						"type":        "string",
						"description": "Standard unified diff with ---/+++ file headers and @@ hunks.",
						"minLength":   1,
					},
					"dry_run": map[string]any{
						"type":        "boolean",
						"description": "Validate and preview the patch without mutating workspace state.",
					},
				},
			},
		},
		Handler: func(ctx context.Context, call tool.Call) (model.ToolResult, error) {
			input, err := tool.DecodeInput[patchInput](call.Use)
			if err != nil {
				return model.ToolResult{}, err
			}
			result, err := applyPatchInput(ctx, store, call.Use, input, reviewer)
			if err != nil {
				return model.ToolResult{}, err
			}
			summary := workspace.SummarizeChanges(result.Changes)
			return model.ToolResult{
				Content: formatPatchResult(result),
				Metadata: map[string]any{
					model.MetadataWorkspaceOperation: "patch",
					model.MetadataWorkspaceChanges:   summary.Files,
					model.MetadataWorkspaceAdded:     summary.Added,
					model.MetadataWorkspaceModified:  summary.Modified,
					model.MetadataWorkspaceDeleted:   summary.Deleted,
					model.MetadataWorkspaceByteDelta: summary.ByteDelta,
					model.MetadataWorkspacePaths:     summary.Paths,
					"dry_run":                        result.DryRun,
				},
			}, nil
		},
	}
}

// NewDiffTool returns a read-only tool for showing changes since a checkpoint.
func NewDiffTool(store Differ) tool.Tool {
	return tool.Definition{
		ToolSpec: model.ToolSpec{
			Name:            DiffToolName,
			Description:     "Show workspace changes since a checkpoint.",
			SearchHint:      "diff workspace changes files patch",
			ReadOnly:        true,
			ConcurrencySafe: true,
			MaxResultBytes:  64 * 1024,
			InputSchema: map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"properties": map[string]any{
					"base_id": map[string]any{
						"type":        "string",
						"description": "Optional checkpoint ID. Defaults to the initial checkpoint.",
					},
				},
			},
		},
		Handler: func(ctx context.Context, call tool.Call) (model.ToolResult, error) {
			input, err := tool.DecodeInput[diffInput](call.Use)
			if err != nil {
				return model.ToolResult{}, err
			}
			diff, err := store.Diff(ctx, input.BaseID)
			if err != nil {
				return model.ToolResult{}, err
			}
			summary := workspace.SummarizeChanges(diff.Changes)
			return model.ToolResult{
				Content: formatChanges(diff.Changes),
				Metadata: map[string]any{
					model.MetadataWorkspaceOperation:    "diff",
					model.MetadataWorkspaceBaseID:       diff.BaseID,
					model.MetadataWorkspaceCheckpointID: diff.BaseID,
					model.MetadataWorkspaceChanges:      summary.Files,
					model.MetadataWorkspaceAdded:        summary.Added,
					model.MetadataWorkspaceModified:     summary.Modified,
					model.MetadataWorkspaceDeleted:      summary.Deleted,
					model.MetadataWorkspaceByteDelta:    summary.ByteDelta,
					model.MetadataWorkspacePaths:        summary.Paths,
				},
			}, nil
		},
	}
}

// NewCheckpointTool returns a destructive tool for creating workspace
// checkpoints. It is destructive because checkpoint creation changes host-owned
// workspace state even though file contents are unchanged.
func NewCheckpointTool(store Checkpointer) tool.Tool {
	return tool.Definition{
		ToolSpec: model.ToolSpec{
			Name:           CheckpointToolName,
			Description:    "Create a restorable checkpoint of current workspace state.",
			SearchHint:     "checkpoint snapshot save workspace state",
			Destructive:    true,
			MaxResultBytes: 8 * 1024,
			InputSchema: map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"properties": map[string]any{
					"label": map[string]any{
						"type":        "string",
						"description": "Short checkpoint label.",
					},
					"metadata": map[string]any{
						"type":        "object",
						"description": "Optional checkpoint metadata.",
					},
				},
			},
		},
		Handler: func(ctx context.Context, call tool.Call) (model.ToolResult, error) {
			input, err := tool.DecodeInput[checkpointInput](call.Use)
			if err != nil {
				return model.ToolResult{}, err
			}
			cp, err := store.Checkpoint(ctx, workspace.CheckpointOptions{
				Label:    input.Label,
				Metadata: input.Metadata,
			})
			if err != nil {
				return model.ToolResult{}, err
			}
			return model.ToolResult{
				Content:  "created workspace checkpoint " + cp.ID,
				Metadata: checkpointMetadata(cp),
			}, nil
		},
	}
}

// NewRestoreTool returns a destructive tool for restoring workspace files from
// a checkpoint.
func NewRestoreTool(store Restorer) tool.Tool {
	return tool.Definition{
		ToolSpec: model.ToolSpec{
			Name:           RestoreToolName,
			Description:    "Restore workspace files from a checkpoint.",
			SearchHint:     "restore rollback checkpoint workspace state",
			Destructive:    true,
			MaxResultBytes: 8 * 1024,
			InputSchema: map[string]any{
				"type":                 "object",
				"required":             []any{"id"},
				"additionalProperties": false,
				"properties": map[string]any{
					"id": map[string]any{
						"type":        "string",
						"description": "Checkpoint ID to restore.",
						"minLength":   1,
					},
				},
			},
		},
		Handler: func(ctx context.Context, call tool.Call) (model.ToolResult, error) {
			input, err := tool.DecodeInput[idInput](call.Use)
			if err != nil {
				return model.ToolResult{}, err
			}
			cp, err := store.Restore(ctx, input.ID)
			if err != nil {
				return model.ToolResult{}, err
			}
			return model.ToolResult{
				Content:  "restored workspace checkpoint " + cp.ID,
				Metadata: checkpointMetadata(cp, "restore"),
			}, nil
		},
	}
}

type pathInput struct {
	Path string `json:"path"`
}

type listInput struct {
	Prefix string `json:"prefix"`
}

type diffInput struct {
	BaseID string `json:"base_id"`
}

type idInput struct {
	ID string `json:"id"`
}

type checkpointInput struct {
	Label    string         `json:"label"`
	Metadata map[string]any `json:"metadata"`
}

type patchInput struct {
	Operations  []patchOperationInput `json:"operations"`
	UnifiedDiff string                `json:"unified_diff"`
	DryRun      bool                  `json:"dry_run"`
}

type patchOperationInput struct {
	Path       string  `json:"path"`
	OldContent *string `json:"old_content"`
	NewContent *string `json:"new_content"`
	Delete     bool    `json:"delete"`
}

func patchOperations(input []patchOperationInput) ([]workspace.PatchOperation, error) {
	if len(input) == 0 {
		return nil, fmt.Errorf("workspacetools: patch requires at least one operation")
	}
	out := make([]workspace.PatchOperation, len(input))
	for i, op := range input {
		if op.Delete && op.NewContent != nil {
			return nil, fmt.Errorf("workspacetools: operation %d cannot set delete and new_content", i)
		}
		if !op.Delete && op.NewContent == nil {
			return nil, fmt.Errorf("workspacetools: operation %d requires new_content unless delete is true", i)
		}
		out[i] = workspace.PatchOperation{
			Path:       op.Path,
			OldContent: op.OldContent,
			NewContent: op.NewContent,
		}
	}
	return out, nil
}

func applyPatchInput(ctx context.Context, store Patcher, use model.ToolUse, input patchInput, reviewer PatchReviewer) (workspace.PatchResult, error) {
	hasOperations := len(input.Operations) > 0
	hasUnifiedDiff := strings.TrimSpace(input.UnifiedDiff) != ""
	switch {
	case hasOperations == hasUnifiedDiff:
		return workspace.PatchResult{}, fmt.Errorf("workspacetools: provide exactly one of operations or unified_diff")
	case hasUnifiedDiff:
		unified, ok := store.(UnifiedDiffPatcher)
		if !ok {
			return workspace.PatchResult{}, fmt.Errorf("workspacetools: store does not support unified diff patches")
		}
		if input.DryRun || reviewer == nil {
			result, err := unified.ApplyUnifiedDiff(ctx, input.UnifiedDiff, workspace.PatchOptions{DryRun: input.DryRun})
			if err != nil || reviewer == nil {
				return result, err
			}
			return reviewPatch(ctx, reviewer, use, result, input.DryRun)
		}
		preview, err := unified.ApplyUnifiedDiff(ctx, input.UnifiedDiff, workspace.PatchOptions{DryRun: true})
		if err != nil {
			return workspace.PatchResult{}, err
		}
		if _, err := reviewPatch(ctx, reviewer, use, preview, input.DryRun); err != nil {
			return workspace.PatchResult{}, err
		}
		return unified.ApplyUnifiedDiff(ctx, input.UnifiedDiff, workspace.PatchOptions{})
	default:
		ops, err := patchOperations(input.Operations)
		if err != nil {
			return workspace.PatchResult{}, err
		}
		if input.DryRun {
			previewer, ok := store.(PatchPreviewer)
			if !ok {
				return workspace.PatchResult{}, fmt.Errorf("workspacetools: store does not support dry-run structured patches")
			}
			result, err := previewer.PreviewPatch(ctx, ops)
			if err != nil || reviewer == nil {
				return result, err
			}
			return reviewPatch(ctx, reviewer, use, result, input.DryRun)
		}
		if reviewer != nil {
			previewer, ok := store.(PatchPreviewer)
			if !ok {
				return workspace.PatchResult{}, fmt.Errorf("workspacetools: store does not support reviewed structured patches")
			}
			preview, err := previewer.PreviewPatch(ctx, ops)
			if err != nil {
				return workspace.PatchResult{}, err
			}
			if _, err := reviewPatch(ctx, reviewer, use, preview, input.DryRun); err != nil {
				return workspace.PatchResult{}, err
			}
		}
		return store.ApplyPatch(ctx, ops)
	}
}

func reviewPatch(ctx context.Context, reviewer PatchReviewer, use model.ToolUse, result workspace.PatchResult, dryRun bool) (workspace.PatchResult, error) {
	use.Input = append([]byte(nil), use.Input...)
	decision := reviewer.ReviewPatch(ctx, PatchReviewRequest{
		ToolUse: use,
		DryRun:  dryRun,
		Summary: workspace.SummarizeChanges(result.Changes),
		Changes: append([]workspace.Change(nil), result.Changes...),
	})
	if decision.Allow {
		return result, nil
	}
	reason := strings.TrimSpace(decision.Reason)
	if reason == "" {
		reason = "workspace patch denied by reviewer"
	}
	return workspace.PatchResult{}, fmt.Errorf("workspacetools: %s", reason)
}

func formatPatchResult(result workspace.PatchResult) string {
	content := formatChanges(result.Changes)
	if result.DryRun {
		return "dry run: " + content
	}
	return content
}

func formatChanges(changes []workspace.Change) string {
	if len(changes) == 0 {
		return "no changes"
	}
	var b strings.Builder
	for i, change := range changes {
		if i > 0 {
			b.WriteByte('\n')
		}
		fmt.Fprintf(&b, "- %s %s", change.Kind, change.Path)
		switch change.Kind {
		case workspace.ChangeAdded:
			fmt.Fprintf(&b, " (%d bytes)", len(change.After))
		case workspace.ChangeModified:
			fmt.Fprintf(&b, " (%d -> %d bytes)", len(change.Before), len(change.After))
		case workspace.ChangeDeleted:
			fmt.Fprintf(&b, " (%d bytes)", len(change.Before))
		}
	}
	return b.String()
}

func checkpointMetadata(cp workspace.Checkpoint, operation ...string) map[string]any {
	op := "checkpoint"
	if len(operation) > 0 && operation[0] != "" {
		op = operation[0]
	}
	out := map[string]any{
		"id":                                cp.ID,
		model.MetadataWorkspaceOperation:    op,
		model.MetadataWorkspaceCheckpointID: cp.ID,
		"label":                             cp.Label,
		"created_at":                        cp.CreatedAt.Format("2006-01-02T15:04:05Z"),
		"files":                             cp.Files,
	}
	if len(cp.Metadata) > 0 {
		out["metadata"] = cp.Metadata
	}
	return out
}

func changePaths(changes []workspace.Change) []string {
	if len(changes) == 0 {
		return nil
	}
	paths := make([]string, len(changes))
	for i, change := range changes {
		paths[i] = change.Path
	}
	return paths
}

func pathInputSchema(description string) map[string]any {
	return map[string]any{
		"type":                 "object",
		"required":             []any{"path"},
		"additionalProperties": false,
		"properties": map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": description,
				"minLength":   1,
			},
		},
	}
}
