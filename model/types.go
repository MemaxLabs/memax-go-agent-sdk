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
	ContentText             ContentType = "text"
	ContentToolUse          ContentType = "tool_use"
	ContentProviderArtifact ContentType = "provider_artifact"
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

// PlainText concatenates text content only. Provider artifacts are
// intentionally excluded because they are opaque transcript state, not model
// text for prompts, summaries, or logs.
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
	Type             ContentType       `json:"type"`
	Text             string            `json:"text,omitempty"`
	ToolUse          *ToolUse          `json:"tool_use,omitempty"`
	ProviderArtifact *ProviderArtifact `json:"provider_artifact,omitempty"`
}

// ProviderArtifact carries opaque provider-native transcript state that must
// round-trip across turns but must not be exposed as normal assistant text.
// Provider adapters may replay artifacts for their own provider and must ignore
// artifacts produced by other providers.
type ProviderArtifact struct {
	Provider string          `json:"provider"`
	Type     string          `json:"type"`
	ID       string          `json:"id,omitempty"`
	Data     json.RawMessage `json:"data,omitempty"`
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

// NormalizeToolUse returns a copy of use whose Input is safe to marshal and
// execute. Providers sometimes represent an empty tool argument object as an
// empty string while streaming; the SDK contract exposes that as {} instead of
// an invalid json.RawMessage.
func NormalizeToolUse(use ToolUse) ToolUse {
	use.Input = NormalizeToolInput(use.Input)
	return use
}

// NormalizeToolInput returns a copy of input, defaulting empty or whitespace
// input to {} so ToolUse values remain valid JSON transcript entries.
func NormalizeToolInput(input json.RawMessage) json.RawMessage {
	if len(strings.TrimSpace(string(input))) == 0 {
		return json.RawMessage(`{}`)
	}
	return append(json.RawMessage(nil), input...)
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
	// MetadataApprovalOperation identifies approval tool results that should
	// produce approval lifecycle events.
	MetadataApprovalOperation = "approval_operation"
	// MetadataApprovalAction carries the action or tool name being approved.
	MetadataApprovalAction = "approval_action"
	// MetadataApprovalApproved records whether approval was granted.
	MetadataApprovalApproved = "approval_approved"
	// MetadataApprovalReason carries a host-visible approval or denial reason.
	MetadataApprovalReason = "approval_reason"
	// MetadataApprovalInputHash carries the canonical JSON hash of the proposed
	// tool input, when approval is bound to exact input.
	MetadataApprovalInputHash = "approval_input_hash"
	// MetadataApprovalSummaryTitle carries a short host-facing approval summary.
	MetadataApprovalSummaryTitle = "approval_summary_title"
	// MetadataApprovalSummaryDescription carries optional approval summary
	// details.
	MetadataApprovalSummaryDescription = "approval_summary_description"
	// MetadataApprovalSummaryRisk carries optional approval summary risk text.
	MetadataApprovalSummaryRisk = "approval_summary_risk"
	// MetadataApprovalSummaryPaths carries paths affected by the proposed
	// action.
	MetadataApprovalSummaryPaths = "approval_summary_paths"
	// MetadataApprovalSummaryChanges carries the number of file-level changes
	// in the proposed action.
	MetadataApprovalSummaryChanges = "approval_summary_changes"
	// MetadataApprovalSummaryAdded carries the number of added files in the
	// proposed action.
	MetadataApprovalSummaryAdded = "approval_summary_added"
	// MetadataApprovalSummaryModified carries the number of modified files in
	// the proposed action.
	MetadataApprovalSummaryModified = "approval_summary_modified"
	// MetadataApprovalSummaryDeleted carries the number of deleted files in the
	// proposed action.
	MetadataApprovalSummaryDeleted = "approval_summary_deleted"
	// MetadataApprovalSummaryByteDelta carries the estimated byte delta for the
	// proposed action.
	MetadataApprovalSummaryByteDelta = "approval_summary_byte_delta"
	// MetadataApprovalConsumed marks a later tool result whose execution used an
	// approval grant.
	MetadataApprovalConsumed = "approval_consumed"
	// MetadataApprovalSingleUse marks whether the consumed approval grant was
	// single-use.
	MetadataApprovalSingleUse = "approval_single_use"
	// MetadataTenantDenied marks a tool result as a tenant-policy denial.
	MetadataTenantDenied = "tenant_denied"
	// MetadataTenantDeniedBoundary carries the denied boundary name.
	MetadataTenantDeniedBoundary = "tenant_denied_boundary"
	// MetadataTenantDeniedReason carries the host-visible denial reason.
	MetadataTenantDeniedReason = "tenant_denied_reason"
	// MetadataCommandOperation identifies command tool results that should
	// produce command lifecycle events.
	MetadataCommandOperation = "command_operation"
	// MetadataCommandArgv carries the executed argv vector.
	MetadataCommandArgv = "command_argv"
	// MetadataCommandString carries the model-facing shell command string when
	// the command tool was invoked through a shell-style interface.
	MetadataCommandString = "command_string"
	// MetadataCommandCWD carries the command working directory.
	MetadataCommandCWD = "command_cwd"
	// MetadataCommandExitCode carries the process exit code. Runner
	// implementations may use -1 when no process exit code exists.
	MetadataCommandExitCode = "command_exit_code"
	// MetadataCommandTimedOut records whether the command exceeded its timeout.
	MetadataCommandTimedOut = "command_timed_out"
	// MetadataCommandDurationMS carries command runtime in milliseconds.
	MetadataCommandDurationMS = "command_duration_ms"
	// MetadataCommandStdoutBytes carries the uncapped stdout byte count when
	// known, or the retained stdout byte count otherwise.
	MetadataCommandStdoutBytes = "command_stdout_bytes"
	// MetadataCommandStderrBytes carries the uncapped stderr byte count when
	// known, or the retained stderr byte count otherwise.
	MetadataCommandStderrBytes = "command_stderr_bytes"
	// MetadataCommandOutputTruncated records whether stdout or stderr was
	// truncated before returning to the model.
	MetadataCommandOutputTruncated = "command_output_truncated"
	// MetadataCommandSessionID carries the managed command session identifier
	// returned by start_command.
	MetadataCommandSessionID = "command_session_id"
	// MetadataCommandStatus carries the managed command status such as running,
	// exited, or stopped.
	MetadataCommandStatus = "command_status"
	// MetadataCommandPID carries the managed command process identifier when the
	// host exposes one.
	MetadataCommandPID = "command_pid"
	// MetadataCommandTTY records whether the managed command session uses a PTY.
	MetadataCommandTTY = "command_tty"
	// MetadataCommandSignalsProcessTree records whether stop and timeout signals
	// target a process tree or group rather than only the top-level process.
	MetadataCommandSignalsProcessTree = "command_signals_process_tree"
	// MetadataCommandCols carries the terminal width for PTY-backed managed
	// command sessions.
	MetadataCommandCols = "command_cols"
	// MetadataCommandRows carries the terminal height for PTY-backed managed
	// command sessions.
	MetadataCommandRows = "command_rows"
	// MetadataCommandStartedAt carries the command session start time in
	// RFC3339Nano format.
	MetadataCommandStartedAt = "command_started_at"
	// MetadataCommandFinishedAt carries the command session finish time in
	// RFC3339Nano format when known.
	MetadataCommandFinishedAt = "command_finished_at"
	// MetadataCommandInputBytes carries the number of stdin bytes written by a
	// write_command_input result.
	MetadataCommandInputBytes = "command_input_bytes"
	// MetadataCommandNextSeq carries the next output sequence number visible to
	// a later read_command_output call.
	MetadataCommandNextSeq = "command_next_seq"
	// MetadataCommandResumeAfterSeq carries the sequence cursor callers should
	// pass as after_seq when continuing a managed command session.
	MetadataCommandResumeAfterSeq = "command_resume_after_seq"
	// MetadataCommandOutputChunks carries the number of output chunks returned by
	// a read_command_output result.
	MetadataCommandOutputChunks = "command_output_chunks"
	// MetadataCommandDroppedChunks carries the number of older output chunks
	// evicted from the manager buffer.
	MetadataCommandDroppedChunks = "command_dropped_chunks"
	// MetadataCommandDroppedBytes carries the number of older output bytes
	// evicted from the manager buffer.
	MetadataCommandDroppedBytes = "command_dropped_bytes"
	// MetadataWebOperation identifies web tool results that should produce web
	// lifecycle events or audit records.
	MetadataWebOperation = "web_operation"
	// MetadataWebQuery carries the web search query.
	MetadataWebQuery = "web_query"
	// MetadataWebDomains carries optional domain filters for a web search.
	MetadataWebDomains = "web_domains"
	// MetadataWebResultCount carries the number of web search results returned.
	MetadataWebResultCount = "web_result_count"
	// MetadataWebResultMetadata carries per-result web search metadata in result
	// order when the backend provides it.
	MetadataWebResultMetadata = "web_result_metadata"
	// MetadataWebURLs carries URLs returned by web search tools or touched by
	// web fetch tools.
	MetadataWebURLs = "web_urls"
	// MetadataWebURL carries the requested URL for a web fetch.
	MetadataWebURL = "web_url"
	// MetadataWebFinalURL carries the final URL after a web fetch redirect.
	MetadataWebFinalURL = "web_final_url"
	// MetadataWebStatusCode carries the HTTP status code observed by a web
	// fetch backend, when applicable.
	MetadataWebStatusCode = "web_status_code"
	// MetadataWebContentType carries the fetched content type.
	MetadataWebContentType = "web_content_type"
	// MetadataWebContentBytes carries the size of fetched content returned to
	// the model.
	MetadataWebContentBytes = "web_content_bytes"
	// MetadataWebFetchedAt carries the fetch timestamp in RFC3339Nano format.
	MetadataWebFetchedAt = "web_fetched_at"
	// MetadataTaskID identifies the task affected by a tool result or requested
	// operation.
	MetadataTaskID = "task_id"
	// MetadataTaskStatus carries the task status after a progress update.
	MetadataTaskStatus = "task_status"
	// MetadataTaskEvidence carries evidence attached to a task progress update.
	MetadataTaskEvidence = "task_evidence"
	// MetadataTaskProgressError carries a non-fatal task progress update error.
	MetadataTaskProgressError = "task_progress_error"
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
			use := NormalizeToolUse(*block.ToolUse)
			out[i].ToolUse = &use
		}
		if block.ProviderArtifact != nil {
			artifact := *block.ProviderArtifact
			if len(block.ProviderArtifact.Data) > 0 {
				artifact.Data = append(json.RawMessage(nil), block.ProviderArtifact.Data...)
			}
			out[i].ProviderArtifact = &artifact
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
