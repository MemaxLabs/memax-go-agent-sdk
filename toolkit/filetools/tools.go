package filetools

import (
	"context"
	"strings"

	"github.com/MemaxLabs/memax-go-agent-sdk/model"
	"github.com/MemaxLabs/memax-go-agent-sdk/tool"
)

const (
	ReadToolName  = "read_file"
	WriteToolName = "write_file"
	ListToolName  = "list_files"
)

func NewReadTool(fs FileSystem) tool.Tool {
	return tool.Definition{
		ToolSpec: model.ToolSpec{
			Name:            ReadToolName,
			Description:     "Read a text file from the configured workspace.",
			SearchHint:      "read workspace file",
			ReadOnly:        true,
			ConcurrencySafe: true,
			MaxResultBytes:  64 * 1024,
			InputSchema: map[string]any{
				"type":                 "object",
				"required":             []any{"path"},
				"additionalProperties": false,
				"properties": map[string]any{
					"path": map[string]any{
						"type":        "string",
						"description": "Workspace-relative file path.",
						"minLength":   1,
					},
				},
			},
		},
		Handler: func(ctx context.Context, call tool.Call) (model.ToolResult, error) {
			input, err := tool.DecodeInput[readInput](call.Use)
			if err != nil {
				return model.ToolResult{}, err
			}
			content, err := fs.ReadFile(ctx, input.Path)
			if err != nil {
				return model.ToolResult{}, err
			}
			return model.ToolResult{Content: content}, nil
		},
	}
}

func NewWriteTool(fs FileSystem) tool.Tool {
	return tool.Definition{
		ToolSpec: model.ToolSpec{
			Name:           WriteToolName,
			Description:    "Write a text file to the configured workspace.",
			SearchHint:     "write workspace file",
			Destructive:    true,
			MaxResultBytes: 8 * 1024,
			InputSchema: map[string]any{
				"type":                 "object",
				"required":             []any{"path", "content"},
				"additionalProperties": false,
				"properties": map[string]any{
					"path": map[string]any{
						"type":        "string",
						"description": "Workspace-relative file path.",
						"minLength":   1,
					},
					"content": map[string]any{
						"type":        "string",
						"description": "Complete file content to write.",
					},
				},
			},
		},
		Handler: func(ctx context.Context, call tool.Call) (model.ToolResult, error) {
			input, err := tool.DecodeInput[writeInput](call.Use)
			if err != nil {
				return model.ToolResult{}, err
			}
			if err := fs.WriteFile(ctx, input.Path, input.Content); err != nil {
				return model.ToolResult{}, err
			}
			return model.ToolResult{Content: "wrote " + cleanPath(input.Path)}, nil
		},
	}
}

func NewListTool(fs FileSystem) tool.Tool {
	return tool.Definition{
		ToolSpec: model.ToolSpec{
			Name:            ListToolName,
			Description:     "List files in the configured workspace.",
			SearchHint:      "list workspace files",
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
			files, err := fs.ListFiles(ctx, input.Prefix)
			if err != nil {
				return model.ToolResult{}, err
			}
			return model.ToolResult{Content: strings.Join(files, "\n")}, nil
		},
	}
}

type readInput struct {
	Path string `json:"path"`
}

type writeInput struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

type listInput struct {
	Prefix string `json:"prefix"`
}
