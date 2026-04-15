package memorytools

import (
	"context"
	"fmt"
	"strings"

	"github.com/MemaxLabs/memax-go-agent-sdk/memory"
	"github.com/MemaxLabs/memax-go-agent-sdk/model"
	"github.com/MemaxLabs/memax-go-agent-sdk/tool"
)

const (
	SearchToolName = "search_memories"
	SaveToolName   = "save_memory"
	DeleteToolName = "delete_memory"

	defaultSearchLimit          = 8
	defaultSearchToolMaxBytes   = 64 * 1024
	defaultMutationToolMaxBytes = 8 * 1024
)

// Config controls the memory tools exposed for one memory backend.
type Config struct {
	Source         memory.Source
	Writer         memory.Writer
	Deleter        memory.Deleter
	SearchName     string
	SaveName       string
	DeleteName     string
	DefaultLimit   int
	MaxResultBytes int
}

// NewTools returns tools for the capabilities configured in config.
func NewTools(config Config) ([]tool.Tool, error) {
	var tools []tool.Tool
	if config.Source != nil {
		search, err := NewSearchTool(config)
		if err != nil {
			return nil, err
		}
		tools = append(tools, search)
	}
	if config.Writer != nil {
		save, err := NewSaveTool(config)
		if err != nil {
			return nil, err
		}
		tools = append(tools, save)
	}
	if config.Deleter != nil {
		deleteTool, err := NewDeleteTool(config)
		if err != nil {
			return nil, err
		}
		tools = append(tools, deleteTool)
	}
	if len(tools) == 0 {
		return nil, fmt.Errorf("memorytools: at least one source, writer, or deleter is required")
	}
	return tools, nil
}

// NewSearchTool returns a read-only memory search tool.
func NewSearchTool(config Config) (tool.Tool, error) {
	if config.Source == nil {
		return nil, fmt.Errorf("memorytools: source is required")
	}
	name := config.SearchName
	if name == "" {
		name = SearchToolName
	}
	limit := config.DefaultLimit
	if limit <= 0 {
		limit = defaultSearchLimit
	}
	maxResultBytes := config.MaxResultBytes
	if maxResultBytes <= 0 {
		maxResultBytes = defaultSearchToolMaxBytes
	}
	return tool.Definition{
		ToolSpec: model.ToolSpec{
			Name:            name,
			Description:     "Search durable host memories for relevant project, user, session, or organization context.",
			SearchHint:      "search recall memory project user session organization context preference rule",
			ReadOnly:        true,
			ConcurrencySafe: true,
			AlwaysLoad:      true,
			MaxResultBytes:  maxResultBytes,
			InputSchema:     searchInputSchema(),
		},
		Handler: func(ctx context.Context, call tool.Call) (model.ToolResult, error) {
			input, err := tool.DecodeInput[searchInput](call.Use)
			if err != nil {
				return model.ToolResult{}, err
			}
			if err := ctx.Err(); err != nil {
				return model.ToolResult{}, err
			}
			selectedLimit := input.Limit
			if selectedLimit <= 0 {
				selectedLimit = limit
			}
			items, err := config.Source.Memories(ctx, memory.Request{
				SessionID:       call.Runtime.SessionID,
				ParentSessionID: call.Runtime.ParentSessionID,
				Query:           input.Query,
			})
			if err != nil {
				return model.ToolResult{}, err
			}
			selected := (memory.Selector{MaxMemories: selectedLimit}).Select(items, input.Query)
			return model.ToolResult{
				Content: formatMemories(selected),
				Metadata: map[string]any{
					"query":   input.Query,
					"matches": len(selected),
				},
			}, nil
		},
	}, nil
}

// NewSaveTool returns a tool that lets an agent request durable memory writes.
func NewSaveTool(config Config) (tool.Tool, error) {
	if config.Writer == nil {
		return nil, fmt.Errorf("memorytools: writer is required")
	}
	name := config.SaveName
	if name == "" {
		name = SaveToolName
	}
	maxResultBytes := config.MaxResultBytes
	if maxResultBytes <= 0 {
		maxResultBytes = defaultMutationToolMaxBytes
	}
	return tool.Definition{
		ToolSpec: model.ToolSpec{
			Name:           name,
			Description:    "Save durable host memory for future agent runs when the user or host policy allows it.",
			SearchHint:     "save remember durable memory preference project user session organization",
			Destructive:    true,
			MaxResultBytes: maxResultBytes,
			InputSchema:    saveInputSchema(),
		},
		Handler: func(ctx context.Context, call tool.Call) (model.ToolResult, error) {
			input, err := tool.DecodeInput[saveInput](call.Use)
			if err != nil {
				return model.ToolResult{}, err
			}
			item, err := config.Writer.PutMemory(ctx, memory.PutRequest{
				SessionID:       call.Runtime.SessionID,
				ParentSessionID: call.Runtime.ParentSessionID,
				Memory: memory.Memory{
					ID:          strings.TrimSpace(input.ID),
					Name:        strings.TrimSpace(input.Name),
					Scope:       parseScope(input.Scope),
					Description: strings.TrimSpace(input.Description),
					Content:     strings.TrimSpace(input.Content),
					Priority:    input.Priority,
					AlwaysOn:    input.AlwaysOn,
					Tags:        input.Tags,
					Metadata:    input.Metadata,
				},
			})
			if err != nil {
				return model.ToolResult{}, err
			}
			return model.ToolResult{
				Content:  fmt.Sprintf("saved memory %s", memoryLabel(item)),
				Metadata: memoryMetadata(item),
			}, nil
		},
	}, nil
}

// NewDeleteTool returns a tool that lets an agent request durable memory deletion.
func NewDeleteTool(config Config) (tool.Tool, error) {
	if config.Deleter == nil {
		return nil, fmt.Errorf("memorytools: deleter is required")
	}
	name := config.DeleteName
	if name == "" {
		name = DeleteToolName
	}
	maxResultBytes := config.MaxResultBytes
	if maxResultBytes <= 0 {
		maxResultBytes = defaultMutationToolMaxBytes
	}
	return tool.Definition{
		ToolSpec: model.ToolSpec{
			Name:           name,
			Description:    "Delete durable host memory by ID or by scope and name when the user or host policy allows it.",
			SearchHint:     "delete forget remove durable memory",
			Destructive:    true,
			MaxResultBytes: maxResultBytes,
			InputSchema:    deleteInputSchema(),
		},
		Handler: func(ctx context.Context, call tool.Call) (model.ToolResult, error) {
			input, err := tool.DecodeInput[deleteInput](call.Use)
			if err != nil {
				return model.ToolResult{}, err
			}
			req := memory.DeleteRequest{
				SessionID:       call.Runtime.SessionID,
				ParentSessionID: call.Runtime.ParentSessionID,
				ID:              strings.TrimSpace(input.ID),
				Name:            strings.TrimSpace(input.Name),
				Scope:           memory.Scope(strings.TrimSpace(input.Scope)),
			}
			if req.ID == "" && req.Name == "" {
				return model.ToolResult{}, fmt.Errorf("delete_memory requires id or name")
			}
			if err := config.Deleter.DeleteMemory(ctx, req); err != nil {
				return model.ToolResult{}, err
			}
			label := req.ID
			if label == "" {
				label = req.Name
				if req.Scope != "" {
					label = string(req.Scope) + "/" + req.Name
				}
			}
			return model.ToolResult{Content: "deleted memory " + label}, nil
		},
	}, nil
}

type searchInput struct {
	Query string `json:"query"`
	Limit int    `json:"limit"`
}

type saveInput struct {
	ID          string         `json:"id"`
	Name        string         `json:"name"`
	Scope       string         `json:"scope"`
	Description string         `json:"description"`
	Content     string         `json:"content"`
	Priority    int            `json:"priority"`
	AlwaysOn    bool           `json:"always_on"`
	Tags        []string       `json:"tags"`
	Metadata    map[string]any `json:"metadata"`
}

type deleteInput struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Scope string `json:"scope"`
}

func searchInputSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"query": map[string]any{
				"type":        "string",
				"description": "Memory search query.",
			},
			"limit": map[string]any{
				"type":        "integer",
				"minimum":     1,
				"maximum":     50,
				"description": "Maximum memories to return.",
			},
		},
	}
}

func saveInputSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"required":             []any{"content"},
		"additionalProperties": false,
		"properties": map[string]any{
			"id":          map[string]any{"type": "string", "description": "Optional existing memory ID to replace."},
			"name":        map[string]any{"type": "string", "description": "Stable memory name for deduplication."},
			"scope":       scopeSchema(),
			"description": map[string]any{"type": "string", "description": "Short explanation of when this memory matters."},
			"content":     map[string]any{"type": "string", "minLength": 1, "description": "Durable context to remember."},
			"priority":    map[string]any{"type": "integer", "minimum": 0, "description": "Lower positive numbers are selected first when relevance ties."},
			"always_on":   map[string]any{"type": "boolean", "description": "Whether this memory should always be injected when available."},
			"tags":        map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"metadata":    map[string]any{"type": "object", "additionalProperties": true},
		},
	}
}

func deleteInputSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"id":    map[string]any{"type": "string", "description": "Memory ID to delete."},
			"name":  map[string]any{"type": "string", "description": "Memory name to delete when ID is not known."},
			"scope": scopeSchema(),
		},
	}
}

func scopeSchema() map[string]any {
	return map[string]any{
		"type":        "string",
		"description": "Memory ownership scope.",
		"enum": []any{
			string(memory.ScopeProject),
			string(memory.ScopeUser),
			string(memory.ScopeSession),
			string(memory.ScopeOrganization),
			string(memory.ScopeCustom),
		},
	}
}

func parseScope(value string) memory.Scope {
	value = strings.TrimSpace(value)
	if value == "" {
		return memory.ScopeCustom
	}
	return memory.Scope(value)
}

func formatMemories(memories []memory.Memory) string {
	if len(memories) == 0 {
		return "No matching memories."
	}
	var b strings.Builder
	for _, item := range memories {
		fmt.Fprintf(&b, "- %s", memoryLabel(item))
		if item.Description != "" {
			fmt.Fprintf(&b, ": %s", item.Description)
		}
		if item.Content != "" {
			fmt.Fprintf(&b, "\n  Content: %s", item.Content)
		}
		if len(item.Tags) > 0 {
			fmt.Fprintf(&b, "\n  Tags: %s", strings.Join(item.Tags, ", "))
		}
		b.WriteByte('\n')
	}
	return strings.TrimSpace(b.String())
}

func memoryLabel(item memory.Memory) string {
	if item.Name != "" {
		return fmt.Sprintf("%s/%s", item.Scope, item.Name)
	}
	if item.ID != "" {
		return item.ID
	}
	return string(item.Scope)
}

func memoryMetadata(item memory.Memory) map[string]any {
	return map[string]any{
		"id":        item.ID,
		"name":      item.Name,
		"scope":     string(item.Scope),
		"priority":  item.Priority,
		"always_on": item.AlwaysOn,
		"tags":      append([]string(nil), item.Tags...),
	}
}
