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
	// IncludeContent makes search results include full skill instructions. The
	// default is metadata-only so search remains a discovery surface; use
	// load_skill for progressive instruction loading.
	IncludeContent bool
}

type searchTool struct {
	spec           model.ToolSpec
	source         skill.Source
	defaultLimit   int
	includeContent bool
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
		description = "Search available skills and return relevant metadata. Use load_skill to load full instructions."
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
		source:         config.Source,
		defaultLimit:   defaultLimit,
		includeContent: config.IncludeContent,
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
		Content: formatSkills(selected, t.includeContent),
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

func formatSkills(skills []skill.Skill, includeContent bool) string {
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
		if len(item.Resources) > 0 {
			b.WriteString("\n  Resources:")
			for _, ref := range item.Resources {
				fmt.Fprintf(&b, "\n    - %s", firstNonEmpty(ref.Name, ref.Path))
				if ref.Description != "" {
					fmt.Fprintf(&b, ": %s", ref.Description)
				}
				if ref.Path != "" && ref.Path != ref.Name {
					fmt.Fprintf(&b, " (path: %s)", ref.Path)
				}
				if ref.MIMEType != "" {
					fmt.Fprintf(&b, " [%s]", ref.MIMEType)
				}
				if ref.Bytes > 0 {
					fmt.Fprintf(&b, " [%d bytes]", ref.Bytes)
				}
			}
		}
		if includeContent && item.Content != "" {
			fmt.Fprintf(&b, "\n  Instructions: %s", item.Content)
		}
		b.WriteByte('\n')
	}
	return strings.TrimSpace(b.String())
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
