package checkpointtools

import (
	"context"
	"fmt"
	"strings"

	"github.com/MemaxLabs/memax-go-agent-sdk/checkpoint"
	"github.com/MemaxLabs/memax-go-agent-sdk/model"
	"github.com/MemaxLabs/memax-go-agent-sdk/tool"
)

const (
	CreateToolName  = "create_checkpoint"
	ListToolName    = "list_checkpoints"
	RestoreToolName = "restore_checkpoint"
	DeleteToolName  = "delete_checkpoint"
)

func NewCreateTool(manager checkpoint.Manager) tool.Tool {
	return tool.Definition{
		ToolSpec: model.ToolSpec{
			Name:           CreateToolName,
			Description:    "Create a checkpoint for the current workspace state.",
			SearchHint:     "create save checkpoint snapshot workspace state",
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
			input, err := tool.DecodeInput[createInput](call.Use)
			if err != nil {
				return model.ToolResult{}, err
			}
			cp, err := manager.Create(ctx, checkpoint.CreateOptions{
				SessionID: call.Runtime.SessionID,
				ParentID:  call.Runtime.ParentSessionID,
				Label:     input.Label,
				Metadata:  input.Metadata,
			})
			if err != nil {
				return model.ToolResult{}, err
			}
			return model.ToolResult{
				Content:  "created checkpoint " + cp.ID,
				Metadata: checkpointMetadata(cp),
			}, nil
		},
	}
}

func NewListTool(manager checkpoint.Manager) tool.Tool {
	return tool.Definition{
		ToolSpec: model.ToolSpec{
			Name:            ListToolName,
			Description:     "List checkpoints for a session or parent run.",
			SearchHint:      "list checkpoints snapshots workspace state",
			ReadOnly:        true,
			ConcurrencySafe: true,
			MaxResultBytes:  32 * 1024,
			InputSchema: map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"properties": map[string]any{
					"session_id": map[string]any{
						"type":        "string",
						"description": "Optional session ID filter. Defaults to the current session.",
					},
					"parent_id": map[string]any{
						"type":        "string",
						"description": "Optional parent session ID filter.",
					},
					"all": map[string]any{
						"type":        "boolean",
						"description": "When true, do not default to the current session.",
					},
				},
			},
		},
		Handler: func(ctx context.Context, call tool.Call) (model.ToolResult, error) {
			input, err := tool.DecodeInput[listInput](call.Use)
			if err != nil {
				return model.ToolResult{}, err
			}
			sessionID := strings.TrimSpace(input.SessionID)
			if sessionID == "" && !input.All {
				sessionID = call.Runtime.SessionID
			}
			checkpoints, err := manager.List(ctx, checkpoint.ListOptions{
				SessionID: sessionID,
				ParentID:  input.ParentID,
			})
			if err != nil {
				return model.ToolResult{}, err
			}
			return model.ToolResult{
				Content:  formatCheckpoints(checkpoints),
				Metadata: map[string]any{"count": len(checkpoints)},
			}, nil
		},
	}
}

func NewRestoreTool(manager checkpoint.Manager) tool.Tool {
	return tool.Definition{
		ToolSpec: model.ToolSpec{
			Name:           RestoreToolName,
			Description:    "Restore workspace state from a checkpoint.",
			SearchHint:     "restore rollback checkpoint snapshot workspace state",
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
			cp, err := manager.Restore(ctx, input.ID)
			if err != nil {
				return model.ToolResult{}, err
			}
			return model.ToolResult{
				Content:  "restored checkpoint " + cp.ID,
				Metadata: checkpointMetadata(cp),
			}, nil
		},
	}
}

func NewDeleteTool(manager checkpoint.Manager) tool.Tool {
	return tool.Definition{
		ToolSpec: model.ToolSpec{
			Name:           DeleteToolName,
			Description:    "Delete a checkpoint.",
			SearchHint:     "delete checkpoint snapshot workspace state",
			Destructive:    true,
			MaxResultBytes: 8 * 1024,
			InputSchema: map[string]any{
				"type":                 "object",
				"required":             []any{"id"},
				"additionalProperties": false,
				"properties": map[string]any{
					"id": map[string]any{
						"type":        "string",
						"description": "Checkpoint ID to delete.",
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
			if err := manager.Delete(ctx, input.ID); err != nil {
				return model.ToolResult{}, err
			}
			return model.ToolResult{Content: "deleted checkpoint " + strings.TrimSpace(input.ID)}, nil
		},
	}
}

type createInput struct {
	Label    string         `json:"label"`
	Metadata map[string]any `json:"metadata"`
}

type listInput struct {
	SessionID string `json:"session_id"`
	ParentID  string `json:"parent_id"`
	All       bool   `json:"all"`
}

type idInput struct {
	ID string `json:"id"`
}

func formatCheckpoints(checkpoints []checkpoint.Checkpoint) string {
	if len(checkpoints) == 0 {
		return "no checkpoints"
	}
	var b strings.Builder
	for i, cp := range checkpoints {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString("- ")
		b.WriteString(cp.ID)
		if cp.Label != "" {
			b.WriteString(": ")
			b.WriteString(cp.Label)
		}
		if cp.SessionID != "" {
			b.WriteString(" session=")
			b.WriteString(cp.SessionID)
		}
		if cp.ParentID != "" {
			b.WriteString(" parent=")
			b.WriteString(cp.ParentID)
		}
		if !cp.CreatedAt.IsZero() {
			fmt.Fprintf(&b, " created=%s", cp.CreatedAt.Format("2006-01-02T15:04:05Z"))
		}
	}
	return b.String()
}

func checkpointMetadata(cp checkpoint.Checkpoint) map[string]any {
	out := map[string]any{
		"id":         cp.ID,
		"session_id": cp.SessionID,
		"parent_id":  cp.ParentID,
		"label":      cp.Label,
		"created_at": cp.CreatedAt.Format("2006-01-02T15:04:05Z"),
	}
	if len(cp.Metadata) > 0 {
		out["metadata"] = cp.Metadata
	}
	return out
}
