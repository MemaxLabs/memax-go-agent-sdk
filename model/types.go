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
	// Metadata carries SDK-owned message annotations such as context
	// compaction provenance. Session stores may persist it, but provider
	// adapters must intentionally omit it from provider request payloads unless
	// a provider has an explicit compatible field.
	Metadata map[string]any `json:"metadata,omitempty"`
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
	// MetadataLoadedSkillResource marks a tool result as a loaded skill
	// supporting resource.
	MetadataLoadedSkillResource = "loaded_skill_resource"
	// MetadataSkillSearch marks a tool result as skill catalog search output.
	MetadataSkillSearch = "skill_search"
	// MetadataWorkspaceOperation identifies workspace tool results that should
	// produce workspace lifecycle events.
	MetadataWorkspaceOperation = "workspace_operation"
	// MetadataWorkspacePaths carries the workspace-relative paths affected by a
	// workspace operation.
	MetadataWorkspacePaths = "workspace_paths"
	// MetadataWorkspaceChanges carries the number of file-level changes
	// reported by a workspace operation.
	MetadataWorkspaceChanges = "workspace_changes"
	// MetadataWorkspaceAdded carries the number of added files in a workspace
	// patch or diff summary.
	MetadataWorkspaceAdded = "workspace_added"
	// MetadataWorkspaceModified carries the number of modified files in a
	// workspace patch or diff summary.
	MetadataWorkspaceModified = "workspace_modified"
	// MetadataWorkspaceDeleted carries the number of deleted files in a
	// workspace patch or diff summary.
	MetadataWorkspaceDeleted = "workspace_deleted"
	// MetadataWorkspaceByteDelta carries the net byte delta for a workspace
	// patch or diff summary.
	MetadataWorkspaceByteDelta = "workspace_byte_delta"
	// MetadataWorkspaceCheckpointID carries the checkpoint ID created, diffed,
	// or restored by a workspace operation.
	MetadataWorkspaceCheckpointID = "workspace_checkpoint_id"
	// MetadataWorkspaceBaseID carries the checkpoint ID used as a diff base.
	MetadataWorkspaceBaseID = "workspace_base_id"
	// MetadataVerificationOperation identifies verification tool results that
	// should produce verification lifecycle events.
	MetadataVerificationOperation = "verification_operation"
	// MetadataVerificationName carries the host-defined verification check name.
	MetadataVerificationName = "verification_name"
	// MetadataVerificationPassed records whether a verification check passed.
	MetadataVerificationPassed = "verification_passed"
	// MetadataVerificationDiagnostics carries the number of diagnostics reported
	// by a verification check.
	MetadataVerificationDiagnostics = "verification_diagnostics"
	// MetadataVerificationPaths carries workspace-relative paths mentioned by
	// verification diagnostics.
	MetadataVerificationPaths = "verification_paths"
)

// CloneMessages returns a deep copy of messages.
func CloneMessages(messages []Message) []Message {
	if len(messages) == 0 {
		return nil
	}
	out := make([]Message, len(messages))
	for i, msg := range messages {
		out[i] = CloneMessage(msg)
	}
	return out
}

// CloneMessage returns a deep copy of msg.
func CloneMessage(msg Message) Message {
	if len(msg.Content) > 0 {
		msg.Content = CloneContentBlocks(msg.Content)
	}
	if len(msg.Metadata) > 0 {
		msg.Metadata = CloneMetadata(msg.Metadata)
	}
	if msg.ToolResult != nil {
		result := *msg.ToolResult
		result.Metadata = CloneMetadata(result.Metadata)
		msg.ToolResult = &result
	}
	return msg
}

// CloneContentBlocks returns a deep copy of content blocks.
func CloneContentBlocks(blocks []ContentBlock) []ContentBlock {
	if len(blocks) == 0 {
		return nil
	}
	out := make([]ContentBlock, len(blocks))
	for i, block := range blocks {
		out[i] = block
		if block.ToolUse != nil {
			use := *block.ToolUse
			use.Input = append([]byte(nil), block.ToolUse.Input...)
			out[i].ToolUse = &use
		}
	}
	return out
}

// CloneMetadata returns a shallow copy of metadata values.
func CloneMetadata(metadata map[string]any) map[string]any {
	if len(metadata) == 0 {
		return nil
	}
	out := make(map[string]any, len(metadata))
	for key, value := range metadata {
		out[key] = value
	}
	return out
}
