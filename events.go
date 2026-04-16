package memaxagent

import (
	"time"

	"github.com/MemaxLabs/memax-go-agent-sdk/contextwindow"
	"github.com/MemaxLabs/memax-go-agent-sdk/memory"
	"github.com/MemaxLabs/memax-go-agent-sdk/model"
)

type EventKind string

const (
	EventSessionStarted EventKind = "session_started"
	EventModelRequest   EventKind = "model_request"
	EventAssistant      EventKind = "assistant"
	// EventToolUseStart is emitted when a provider starts streaming a tool-use
	// block before the full JSON input is complete.
	EventToolUseStart EventKind = "tool_use_start"
	// EventToolUseDelta is emitted for incremental provider tool-use input
	// chunks. The complete, executable call is still emitted as EventToolUse.
	EventToolUseDelta EventKind = "tool_use_delta"
	EventToolUse      EventKind = "tool_use"
	// EventToolResult is emitted for tool execution results. If streaming fails
	// after an early safe tool has started, a cancellation result may be emitted
	// before EventError so observers do not see an orphaned tool-use event.
	EventToolResult     EventKind = "tool_result"
	EventUsage          EventKind = "usage"
	EventContextApplied EventKind = "context_applied"
	// EventContextCompacted is emitted when a context policy produces a
	// compaction record, such as replacing older transcript messages with a
	// summary.
	EventContextCompacted EventKind = "context_compacted"
	// EventMemoryCandidates is emitted after a valid final answer has been
	// distilled and before EventResult. Candidates are proposals only; the SDK
	// does not persist them unless Options.MemoryCandidateHandler is configured.
	EventMemoryCandidates EventKind = "memory_candidates"
	// EventMemoryCandidateHandlerError is emitted when an optional memory
	// candidate handler fails. It is non-terminal; EventResult is still emitted.
	EventMemoryCandidateHandlerError EventKind = "memory_candidate_handler_error"
	// EventSkillDiscovery is emitted when the prompt exposes progressive skill
	// metadata for a model request. It is emitted per prompt build, so a context
	// retry can produce more than one discovery event for the same turn.
	EventSkillDiscovery EventKind = "skill_discovery"
	// EventSkillSearch is emitted when a skill catalog search tool returns.
	EventSkillSearch EventKind = "skill_search"
	// EventSkillLoaded is emitted when load_skill returns full instructions.
	EventSkillLoaded EventKind = "skill_loaded"
	// EventSkillResourceLoaded is emitted when read_skill_resource returns a
	// supporting resource.
	EventSkillResourceLoaded EventKind = "skill_resource_loaded"
	// EventWorkspacePatch is emitted when a workspace patch tool applies file
	// changes.
	EventWorkspacePatch EventKind = "workspace_patch"
	// EventWorkspaceDiff is emitted when a workspace diff tool reports changes.
	EventWorkspaceDiff EventKind = "workspace_diff"
	// EventWorkspaceCheckpoint is emitted when a workspace checkpoint is
	// created.
	EventWorkspaceCheckpoint EventKind = "workspace_checkpoint"
	// EventWorkspaceRestore is emitted when a workspace checkpoint is restored.
	EventWorkspaceRestore EventKind = "workspace_restore"
	// EventVerification is emitted when a verification tool reports pass/fail
	// status for a host-owned check.
	EventVerification EventKind = "verification"
	EventError        EventKind = "error"
	EventResult       EventKind = "result"
)

// Event is emitted by Query as the orchestration loop progresses.
type Event struct {
	Kind            EventKind
	SessionID       string
	ParentSessionID string
	Turn            int
	Time            time.Time

	Message      *model.Message
	ToolUse      *model.ToolUse
	ToolUseDelta string
	ToolResult   *model.ToolResult
	Usage        *model.Usage
	Context      *ContextEvent
	Compaction   *contextwindow.CompactionRecord
	Memory       *MemoryCandidatesEvent
	Skill        *SkillEvent
	Workspace    *WorkspaceEvent
	Verification *VerificationEvent
	Result       string
	Err          error
}

type ContextEvent struct {
	OriginalMessages int
	SentMessages     int
}

type MemoryCandidatesEvent struct {
	Candidates []memory.Candidate
}

// SkillEvent describes one skill lifecycle observation. Fields are populated
// according to Action: "discovery" uses SelectedSkills, Selected, Omitted,
// PromptBytes, and MetadataOnly; "search" uses Query, Matches, and MetadataOnly;
// "load" uses SkillName; "resource_load" uses SkillName and ResourceName.
type SkillEvent struct {
	Action         string
	SkillName      string
	ResourceName   string
	Query          string
	SelectedSkills []string
	Selected       int
	Omitted        int
	Matches        int
	PromptBytes    int
	MetadataOnly   bool
}

// WorkspaceEvent describes one workspace lifecycle observation derived from
// tool result metadata. Operation is one of "patch", "diff", "checkpoint", or
// "restore". Patch and diff events use Paths, Changes, Added, Modified,
// Deleted, and ByteDelta; checkpoint and restore events use CheckpointID. Diff
// events may also set BaseID.
type WorkspaceEvent struct {
	Operation    string
	Paths        []string
	Changes      int
	Added        int
	Modified     int
	Deleted      int
	ByteDelta    int
	CheckpointID string
	BaseID       string
}

// VerificationEvent describes one host-owned verification check, such as a
// test, typecheck, lint, or custom policy validator. Failed checks are expected
// to arrive as tool error results so the model can repair and retry.
type VerificationEvent struct {
	Operation   string
	Name        string
	Passed      bool
	Diagnostics int
	Paths       []string
}

func newEvent(kind EventKind, sessionID string, turn int) Event {
	return Event{
		Kind:      kind,
		SessionID: sessionID,
		Turn:      turn,
		Time:      time.Now().UTC(),
	}
}
