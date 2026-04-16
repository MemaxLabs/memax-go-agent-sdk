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

// NewTools returns the standard workspace tool set over store.
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
// workspace patches.
func NewApplyPatchTool(store Patcher) tool.Tool {
	return tool.Definition{
		ToolSpec: model.ToolSpec{
			Name:           ApplyPatchToolName,
			Description:    "Apply guarded file edits to the configured workspace.",
			SearchHint:     "apply patch edit write workspace files source code",
			Destructive:    true,
			MaxResultBytes: 32 * 1024,
			InputSchema: map[string]any{
				"type":                 "object",
				"required":             []any{"operations"},
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
				},
			},
		},
		Handler: func(ctx context.Context, call tool.Call) (model.ToolResult, error) {
			input, err := tool.DecodeInput[patchInput](call.Use)
			if err != nil {
				return model.ToolResult{}, err
			}
			ops, err := patchOperations(input.Operations)
			if err != nil {
				return model.ToolResult{}, err
			}
			result, err := store.ApplyPatch(ctx, ops)
			if err != nil {
				return model.ToolResult{}, err
			}
			return model.ToolResult{
				Content:  formatChanges(result.Changes),
				Metadata: map[string]any{"changes": len(result.Changes)},
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
			return model.ToolResult{
				Content: formatChanges(diff.Changes),
				Metadata: map[string]any{
					"base_id": diff.BaseID,
					"changes": len(diff.Changes),
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
				Metadata: checkpointMetadata(cp),
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
	Operations []patchOperationInput `json:"operations"`
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

func checkpointMetadata(cp workspace.Checkpoint) map[string]any {
	out := map[string]any{
		"id":         cp.ID,
		"label":      cp.Label,
		"created_at": cp.CreatedAt.Format("2006-01-02T15:04:05Z"),
		"files":      cp.Files,
	}
	if len(cp.Metadata) > 0 {
		out["metadata"] = cp.Metadata
	}
	return out
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
