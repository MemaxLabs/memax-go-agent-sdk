package toolsearch

import (
	"context"
	"fmt"
	"strings"

	"github.com/MemaxLabs/memax-go-agent-sdk/model"
	"github.com/MemaxLabs/memax-go-agent-sdk/tool"
)

const (
	ToolName     = "search_tools"
	defaultLimit = 8
)

type Config struct {
	Name           string
	Description    string
	Registry       *tool.Registry
	Limit          int
	MaxResultBytes int
}

type searchTool struct {
	spec     model.ToolSpec
	registry *tool.Registry
	limit    int
}

type input struct {
	Query string `json:"query"`
	Limit int    `json:"limit"`
}

func NewTool(config Config) (tool.Tool, error) {
	if config.Registry == nil {
		return nil, fmt.Errorf("toolsearch: registry is required")
	}
	name := config.Name
	if name == "" {
		name = ToolName
	}
	description := config.Description
	if description == "" {
		description = "Search available deferred tools by name, description, and search hint."
	}
	limit := config.Limit
	if limit <= 0 {
		limit = defaultLimit
	}
	return &searchTool{
		spec: model.ToolSpec{
			Name:            name,
			Description:     description,
			SearchHint:      "search find discover available deferred tools",
			ReadOnly:        true,
			ConcurrencySafe: true,
			AlwaysLoad:      true,
			MaxResultBytes:  config.MaxResultBytes,
			InputSchema: map[string]any{
				"type":                 "object",
				"required":             []any{"query"},
				"additionalProperties": false,
				"properties": map[string]any{
					"query": map[string]any{
						"type":        "string",
						"description": "Capability, tool name, or task to search for.",
						"minLength":   1,
					},
					"limit": map[string]any{
						"type":        "integer",
						"description": "Maximum tools to return.",
						"minimum":     1,
					},
				},
			},
		},
		registry: config.Registry,
		limit:    limit,
	}, nil
}

func (t *searchTool) Spec() model.ToolSpec {
	return t.spec
}

func (t *searchTool) CanRunConcurrently(model.ToolUse) bool {
	return true
}

func (t *searchTool) Execute(_ context.Context, call tool.Call) (model.ToolResult, error) {
	in, err := tool.DecodeInput[input](call.Use)
	if err != nil {
		return model.ToolResult{}, err
	}
	in.Query = strings.TrimSpace(in.Query)
	if in.Query == "" {
		return model.ToolResult{}, fmt.Errorf("toolsearch: query is required")
	}
	limit := t.limit
	if in.Limit > 0 && in.Limit < limit {
		limit = in.Limit
	}
	specs := removeTool(t.registry.Specs(), t.spec.Name)
	specs = tool.SearchSpecs(specs, in.Query, limit)
	if len(specs) == 0 {
		return model.ToolResult{Content: "no matching tools"}, nil
	}
	return model.ToolResult{
		Content:  formatSpecs(specs),
		Metadata: map[string]any{"count": len(specs)},
	}, nil
}

func removeTool(specs []model.ToolSpec, name string) []model.ToolSpec {
	out := specs[:0]
	for _, spec := range specs {
		if spec.Name == name {
			continue
		}
		out = append(out, spec)
	}
	return out
}

func formatSpecs(specs []model.ToolSpec) string {
	var b strings.Builder
	for i, spec := range specs {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString("- ")
		b.WriteString(spec.Name)
		if spec.Description != "" {
			b.WriteString(": ")
			b.WriteString(spec.Description)
		}
		var flags []string
		if spec.ReadOnly {
			flags = append(flags, "read_only")
		}
		if spec.Destructive {
			flags = append(flags, "destructive")
		}
		if spec.ConcurrencySafe {
			flags = append(flags, "concurrency_safe")
		}
		if len(flags) > 0 {
			b.WriteString(" [")
			b.WriteString(strings.Join(flags, ","))
			b.WriteByte(']')
		}
	}
	return b.String()
}
