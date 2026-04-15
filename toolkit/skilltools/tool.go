package skilltools

import (
	"context"
	"fmt"
	"strings"

	"github.com/MemaxLabs/memax-go-agent-sdk/model"
	"github.com/MemaxLabs/memax-go-agent-sdk/skill"
	"github.com/MemaxLabs/memax-go-agent-sdk/tool"
)

const (
	defaultSearchToolName     = "search_skills"
	defaultSearchToolMaxBytes = 64 * 1024
	defaultSearchLimit        = 8
)

type Config struct {
	Name           string
	Description    string
	Source         skill.Source
	MaxResultBytes int
	DefaultLimit   int
}

type searchTool struct {
	spec         model.ToolSpec
	source       skill.Source
	defaultLimit int
}

type searchInput struct {
	Query string `json:"query"`
	Limit int    `json:"limit"`
}

// NewSearchTool returns a read-only skill discovery tool.
func NewSearchTool(config Config) (tool.Tool, error) {
	if config.Source == nil {
		return nil, fmt.Errorf("skilltools: source is required")
	}
	name := config.Name
	if name == "" {
		name = defaultSearchToolName
	}
	description := config.Description
	if description == "" {
		description = "Search available skills and return relevant instructions."
	}
	maxResultBytes := config.MaxResultBytes
	if maxResultBytes <= 0 {
		maxResultBytes = defaultSearchToolMaxBytes
	}
	defaultLimit := config.DefaultLimit
	if defaultLimit <= 0 {
		defaultLimit = defaultSearchLimit
	}
	return &searchTool{
		spec: model.ToolSpec{
			Name:            name,
			Description:     description,
			InputSchema:     searchInputSchema(),
			SearchHint:      "search discover skill instruction prompt guidance",
			ReadOnly:        true,
			ConcurrencySafe: true,
			AlwaysLoad:      true,
			MaxResultBytes:  maxResultBytes,
		},
		source:       config.Source,
		defaultLimit: defaultLimit,
	}, nil
}

func (t *searchTool) Spec() model.ToolSpec {
	return t.spec
}

func (t *searchTool) CanRunConcurrently(model.ToolUse) bool {
	return true
}

func (t *searchTool) Execute(ctx context.Context, use tool.Call) (model.ToolResult, error) {
	input, err := tool.DecodeInput[searchInput](use.Use)
	if err != nil {
		return model.ToolResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return model.ToolResult{}, err
	}
	limit := input.Limit
	if limit <= 0 {
		limit = t.defaultLimit
	}
	skills, err := t.source.Skills(ctx)
	if err != nil {
		return model.ToolResult{}, err
	}
	selected := (skill.Selector{MaxSkills: limit}).Select(skills, input.Query)
	return model.ToolResult{
		Content: formatSkills(selected),
		Metadata: map[string]any{
			"query":   input.Query,
			"matches": len(selected),
		},
	}, nil
}

func searchInputSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"query": map[string]any{
				"type":        "string",
				"description": "Skill search query.",
			},
			"limit": map[string]any{
				"type":        "integer",
				"minimum":     1,
				"maximum":     50,
				"description": "Maximum skills to return.",
			},
		},
	}
}

func formatSkills(skills []skill.Skill) string {
	if len(skills) == 0 {
		return "No matching skills."
	}
	var b strings.Builder
	for _, item := range skills {
		fmt.Fprintf(&b, "- %s", item.Name)
		if item.Description != "" {
			fmt.Fprintf(&b, ": %s", item.Description)
		}
		if item.WhenToUse != "" {
			fmt.Fprintf(&b, "\n  Use when: %s", item.WhenToUse)
		}
		if len(item.Tags) > 0 {
			fmt.Fprintf(&b, "\n  Tags: %s", strings.Join(item.Tags, ", "))
		}
		if item.Content != "" {
			fmt.Fprintf(&b, "\n  Instructions: %s", item.Content)
		}
		b.WriteByte('\n')
	}
	return strings.TrimSpace(b.String())
}
