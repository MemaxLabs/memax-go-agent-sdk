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
	EventToolUseDelta   EventKind = "tool_use_delta"
	EventToolUse        EventKind = "tool_use"
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
	EventError                       EventKind = "error"
	EventResult                      EventKind = "result"
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

func newEvent(kind EventKind, sessionID string, turn int) Event {
	return Event{
		Kind:      kind,
		SessionID: sessionID,
		Turn:      turn,
		Time:      time.Now().UTC(),
	}
}
