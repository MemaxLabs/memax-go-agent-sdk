package workspacetools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/MemaxLabs/memax-go-agent-sdk/model"
	"github.com/MemaxLabs/memax-go-agent-sdk/tool"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/approvaltools"
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

// UnifiedDiffPatchStore is the workspace capability required by the
// unified-diff-only patch tool.
type UnifiedDiffPatchStore interface {
	Patcher
	UnifiedDiffPatcher
}

// AutoCheckpointPatchStore is the capability set required by the auto-
// checkpointing patch tool.
type AutoCheckpointPatchStore interface {
	Patcher
	Checkpointer
}

// AutoCheckpointUnifiedDiffPatchStore is the capability set required by the
// auto-checkpointing unified-diff-only patch tool.
type AutoCheckpointUnifiedDiffPatchStore interface {
	UnifiedDiffPatchStore
	Checkpointer
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
// model a chance to recover without mutating the workspace. Reviewer errors
// represent infrastructure or policy-service failures and block the patch.
type PatchReviewer interface {
	ReviewPatch(context.Context, PatchReviewRequest) (PatchReviewDecision, error)
}

// PatchReviewerFunc adapts a function into a PatchReviewer.
type PatchReviewerFunc func(context.Context, PatchReviewRequest) (PatchReviewDecision, error)

func (f PatchReviewerFunc) ReviewPatch(ctx context.Context, req PatchReviewRequest) (PatchReviewDecision, error) {
	if f == nil {
		return PatchReviewDecision{Allow: true}, nil
	}
	return f(ctx, req)
}

// ApprovalSummaryFromPatchInput returns a host-facing approval summary for a
// workspace_apply_patch tool input. It is intentionally input-only: it does not
// read workspace state or validate guards. Hosts that need exact pre/post
// content should use PatchReviewer or a dry-run preview before approval.
func ApprovalSummaryFromPatchInput(input []byte) (approvaltools.Summary, error) {
	var patch patchInput
	if len(input) > 0 {
		if err := json.Unmarshal(input, &patch); err != nil {
			return approvaltools.Summary{}, fmt.Errorf("workspacetools: decode patch input: %w", err)
		}
	}
	return approvalSummaryFromPatch(patch)
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

// NewUnifiedDiffApplyPatchTool returns a destructive tool for applying standard
// unified diffs to the configured workspace. Use this narrower patch surface
// for coding agents that perform better when there is exactly one patch input
// shape.
func NewUnifiedDiffApplyPatchTool(store UnifiedDiffPatchStore) tool.Tool {
	return NewUnifiedDiffApplyPatchToolWithReview(store, nil)
}

// NewAutoCheckpointApplyPatchToolWithReview returns a patch tool that creates
// a workspace checkpoint immediately before mutating patch application. Dry-run
// and reviewer-denied patches do not create checkpoints.
func NewAutoCheckpointApplyPatchToolWithReview(store AutoCheckpointPatchStore, reviewer PatchReviewer) tool.Tool {
	return newApplyPatchToolWithReview(store, reviewer, store)
}

// NewAutoCheckpointUnifiedDiffApplyPatchToolWithReview returns a
// unified-diff-only patch tool that creates a workspace checkpoint immediately
// before mutating patch application.
func NewAutoCheckpointUnifiedDiffApplyPatchToolWithReview(store AutoCheckpointUnifiedDiffPatchStore, reviewer PatchReviewer) tool.Tool {
	return newUnifiedDiffApplyPatchToolWithReview(store, reviewer, store)
}

// NewApplyPatchToolWithReview returns a destructive patch tool that validates
// and previews the requested change, passes the summary to reviewer, and only
// mutates the workspace when the reviewer allows it. Dry-run requests never
// mutate but still invoke the reviewer so hosts can audit proposed changes.
// The actual mutation is re-validated after review; production stores with
// external mutable state should treat review as advisory unless their adapter
// performs preview, review, and apply inside one transaction or lease.
func NewApplyPatchToolWithReview(store Patcher, reviewer PatchReviewer) tool.Tool {
	return newApplyPatchToolWithReview(store, reviewer, nil)
}

func newApplyPatchToolWithReview(store Patcher, reviewer PatchReviewer, checkpointer Checkpointer) tool.Tool {
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
		Normalizer: tool.InputNormalizerFunc(normalizeUnifiedDiffStringInput),
		Handler: func(ctx context.Context, call tool.Call) (model.ToolResult, error) {
			input, err := tool.DecodeInput[patchInput](call.Use)
			if err != nil {
				return model.ToolResult{}, err
			}
			result, err := applyPatchInput(ctx, store, call.Use, input, reviewer, checkpointer)
			if err != nil {
				return model.ToolResult{}, err
			}
			return patchToolResult(result), nil
		},
	}
}

// NewUnifiedDiffApplyPatchToolWithReview returns a destructive patch tool like
// NewApplyPatchToolWithReview, but its model-facing schema accepts only
// unified_diff plus dry_run. The tool name remains workspace_apply_patch so the
// surrounding coding policies, approval summaries, and event handling stay the
// same while hosts can choose the simpler patch input contract.
func NewUnifiedDiffApplyPatchToolWithReview(store UnifiedDiffPatchStore, reviewer PatchReviewer) tool.Tool {
	return newUnifiedDiffApplyPatchToolWithReview(store, reviewer, nil)
}

func newUnifiedDiffApplyPatchToolWithReview(store UnifiedDiffPatchStore, reviewer PatchReviewer, checkpointer Checkpointer) tool.Tool {
	return tool.Definition{
		ToolSpec: model.ToolSpec{
			Name:           ApplyPatchToolName,
			Description:    "Apply a standard unified diff to the configured workspace. Set dry_run to validate and preview without mutating files.",
			SearchHint:     "apply unified diff patch edit write workspace files source code",
			Destructive:    true,
			MaxResultBytes: 32 * 1024,
			InputSchema: map[string]any{
				"type":                 "object",
				"required":             []any{"unified_diff"},
				"additionalProperties": false,
				"properties": map[string]any{
					"unified_diff": map[string]any{
						"type":        "string",
						"description": "Standard unified diff with ---/+++ file headers and @@ hunks. Do not pass structured operations to this tool.",
						"minLength":   1,
					},
					"dry_run": map[string]any{
						"type":        "boolean",
						"description": "Validate and preview the patch without mutating workspace state.",
					},
				},
			},
		},
		Normalizer: tool.InputNormalizerFunc(normalizeUnifiedDiffStringInput),
		Handler: func(ctx context.Context, call tool.Call) (model.ToolResult, error) {
			input, err := tool.DecodeInput[unifiedDiffPatchInput](call.Use)
			if err != nil {
				return model.ToolResult{}, err
			}
			if strings.TrimSpace(input.UnifiedDiff) == "" {
				return model.ToolResult{}, fmt.Errorf("workspacetools: unified_diff is required")
			}
			result, err := applyPatchInput(ctx, store, call.Use, patchInput{
				UnifiedDiff: input.UnifiedDiff,
				DryRun:      input.DryRun,
			}, reviewer, checkpointer)
			if err != nil {
				return model.ToolResult{}, err
			}
			return patchToolResult(result), nil
		},
	}
}

func normalizeUnifiedDiffStringInput(_ context.Context, use model.ToolUse) (model.ToolUse, bool, error) {
	var unifiedDiff string
	if err := json.Unmarshal(use.Input, &unifiedDiff); err != nil {
		return use, false, nil
	}
	if strings.TrimSpace(unifiedDiff) == "" {
		return use, false, nil
	}
	input, err := json.Marshal(map[string]any{"unified_diff": unifiedDiff})
	if err != nil {
		return use, false, err
	}
	use.Input = input
	return use, true, nil
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

type unifiedDiffPatchInput struct {
	UnifiedDiff string `json:"unified_diff"`
	DryRun      bool   `json:"dry_run"`
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

func approvalSummaryFromPatch(input patchInput) (approvaltools.Summary, error) {
	hasOperations := len(input.Operations) > 0
	hasUnifiedDiff := strings.TrimSpace(input.UnifiedDiff) != ""
	switch {
	case hasOperations == hasUnifiedDiff:
		return approvaltools.Summary{}, fmt.Errorf("workspacetools: provide exactly one of operations or unified_diff")
	case hasUnifiedDiff:
		paths := pathsFromUnifiedDiff(input.UnifiedDiff)
		return approvaltools.Summary{
			Title:       "Review workspace patch",
			Description: "Apply a unified diff to the workspace.",
			Risk:        "workspace mutation",
			Changes:     maxInt(1, len(paths)),
			Paths:       paths,
		}, nil
	default:
		summary := approvaltools.Summary{
			Title:       "Review workspace patch",
			Description: "Apply guarded workspace file edits.",
			Risk:        "workspace mutation",
			Changes:     len(input.Operations),
			Paths:       make([]string, 0, len(input.Operations)),
		}
		seen := map[string]struct{}{}
		for _, op := range input.Operations {
			path := strings.TrimSpace(op.Path)
			if path != "" {
				if _, ok := seen[path]; !ok {
					seen[path] = struct{}{}
					summary.Paths = append(summary.Paths, path)
				}
			}
			switch {
			case op.Delete:
				summary.Deleted++
				if op.OldContent != nil {
					summary.ByteDelta -= len(*op.OldContent)
				}
			case op.OldContent == nil:
				summary.Added++
				if op.NewContent != nil {
					summary.ByteDelta += len(*op.NewContent)
				}
			default:
				summary.Modified++
				if op.NewContent != nil {
					summary.ByteDelta += len(*op.NewContent) - len(*op.OldContent)
				}
			}
		}
		return summary, nil
	}
}

func pathsFromUnifiedDiff(diff string) []string {
	var paths []string
	seen := map[string]struct{}{}
	var oldPath string
	for _, line := range strings.Split(diff, "\n") {
		switch {
		case strings.HasPrefix(line, "--- "):
			oldPath = cleanDiffPath(strings.TrimSpace(strings.TrimPrefix(line, "--- ")), "a/")
		case strings.HasPrefix(line, "+++ "):
			newPath := cleanDiffPath(strings.TrimSpace(strings.TrimPrefix(line, "+++ ")), "b/")
			path := newPath
			if path == "" {
				path = oldPath
			}
			if path == "" {
				oldPath = ""
				continue
			}
			if _, ok := seen[path]; ok {
				oldPath = ""
				continue
			}
			seen[path] = struct{}{}
			paths = append(paths, path)
			oldPath = ""
		default:
			continue
		}
	}
	return paths
}

func cleanDiffPath(path, prefix string) string {
	if path == "" || path == "/dev/null" {
		return ""
	}
	return strings.TrimPrefix(path, prefix)
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func applyPatchInput(ctx context.Context, store Patcher, use model.ToolUse, input patchInput, reviewer PatchReviewer, checkpointer Checkpointer) (workspace.PatchResult, error) {
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
		if input.DryRun {
			result, err := unified.ApplyUnifiedDiff(ctx, input.UnifiedDiff, workspace.PatchOptions{DryRun: input.DryRun})
			if err != nil || reviewer == nil {
				return result, err
			}
			return reviewPatch(ctx, reviewer, use, result, input.DryRun)
		}
		if reviewer != nil {
			preview, err := unified.ApplyUnifiedDiff(ctx, input.UnifiedDiff, workspace.PatchOptions{DryRun: true})
			if err != nil {
				return workspace.PatchResult{}, err
			}
			if _, err := reviewPatch(ctx, reviewer, use, preview, input.DryRun); err != nil {
				return workspace.PatchResult{}, err
			}
		}
		cp, err := autoCheckpoint(ctx, checkpointer, use)
		if err != nil {
			return workspace.PatchResult{}, err
		}
		result, err := unified.ApplyUnifiedDiff(ctx, input.UnifiedDiff, workspace.PatchOptions{})
		result.AutoCheckpointID = cp.ID
		return result, err
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
		cp, err := autoCheckpoint(ctx, checkpointer, use)
		if err != nil {
			return workspace.PatchResult{}, err
		}
		result, err := store.ApplyPatch(ctx, ops)
		result.AutoCheckpointID = cp.ID
		return result, err
	}
}

func autoCheckpoint(ctx context.Context, checkpointer Checkpointer, use model.ToolUse) (workspace.Checkpoint, error) {
	if checkpointer == nil {
		return workspace.Checkpoint{}, nil
	}
	cp, err := checkpointer.Checkpoint(ctx, workspace.CheckpointOptions{
		Label: "before " + use.Name,
		Metadata: map[string]any{
			"tool_use_id": use.ID,
			"tool_name":   use.Name,
			"automatic":   true,
		},
	})
	if err != nil {
		return workspace.Checkpoint{}, fmt.Errorf("workspacetools: create automatic checkpoint before patch: %w", err)
	}
	return cp, nil
}

func reviewPatch(ctx context.Context, reviewer PatchReviewer, use model.ToolUse, result workspace.PatchResult, dryRun bool) (workspace.PatchResult, error) {
	use.Input = append([]byte(nil), use.Input...)
	decision, err := reviewer.ReviewPatch(ctx, PatchReviewRequest{
		ToolUse: use,
		DryRun:  dryRun,
		Summary: workspace.SummarizeChanges(result.Changes),
		Changes: append([]workspace.Change(nil), result.Changes...),
	})
	if err != nil {
		return workspace.PatchResult{}, fmt.Errorf("workspacetools: review workspace patch: %w", err)
	}
	if decision.Allow {
		return result, nil
	}
	reason := strings.TrimSpace(decision.Reason)
	if reason == "" {
		reason = "workspace patch denied by reviewer"
	}
	return workspace.PatchResult{}, fmt.Errorf("workspacetools: %s", reason)
}

func patchToolResult(result workspace.PatchResult) model.ToolResult {
	summary := workspace.SummarizeChanges(result.Changes)
	out := model.ToolResult{
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
	}
	if result.AutoCheckpointID != "" {
		out.Metadata[model.MetadataWorkspaceCheckpointID] = result.AutoCheckpointID
		out.Metadata["auto_checkpoint"] = true
	}
	return out
}

func formatPatchResult(result workspace.PatchResult) string {
	content := formatChanges(result.Changes)
	if result.DryRun {
		return "dry run: " + content
	}
	if result.AutoCheckpointID != "" {
		return "auto checkpoint: " + result.AutoCheckpointID + "\n" + content
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
