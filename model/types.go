package model

import (
	"encoding/json"
	"strings"
)

type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

type ContentType string

const (
	ContentText    ContentType = "text"
	ContentToolUse ContentType = "tool_use"
)

type Message struct {
	ID         string         `json:"id,omitempty"`
	Role       Role           `json:"role"`
	Content    []ContentBlock `json:"content,omitempty"`
	ToolResult *ToolResult    `json:"tool_result,omitempty"`
}

func (m Message) PlainText() string {
	var b strings.Builder
	for _, block := range m.Content {
		if block.Type == ContentText {
			b.WriteString(block.Text)
		}
	}
	return b.String()
}

type ContentBlock struct {
	Type    ContentType `json:"type"`
	Text    string      `json:"text,omitempty"`
	ToolUse *ToolUse    `json:"tool_use,omitempty"`
}

type ToolSpec struct {
	Name            string         `json:"name"`
	Description     string         `json:"description"`
	InputSchema     map[string]any `json:"input_schema,omitempty"`
	SearchHint      string         `json:"search_hint,omitempty"`
	ReadOnly        bool           `json:"read_only,omitempty"`
	ConcurrencySafe bool           `json:"concurrency_safe,omitempty"`
	Destructive     bool           `json:"destructive,omitempty"`
	AlwaysLoad      bool           `json:"always_load,omitempty"`
	ShouldDefer     bool           `json:"should_defer,omitempty"`
	// MaxResultBytes bounds the result content returned to the model. Zero means
	// unbounded. This is SDK-side execution policy, not model-facing metadata.
	MaxResultBytes int `json:"-"`
}

type ToolUse struct {
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

type ToolResult struct {
	ToolUseID string         `json:"tool_use_id"`
	Name      string         `json:"name"`
	Content   string         `json:"content"`
	IsError   bool           `json:"is_error,omitempty"`
	Metadata  map[string]any `json:"metadata,omitempty"`
}

const (
	// MetadataContextRetention marks ToolResult metadata with an explicit
	// context-window retention hint.
	MetadataContextRetention = "context_retention"
	// RetentionImportant is the MetadataContextRetention value for tool results
	// that should survive aggressive context trimming.
	RetentionImportant = "important"
	// MetadataLoadedSkill marks a tool result as loaded skill instructions.
	MetadataLoadedSkill = "loaded_skill"
)
