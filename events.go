package memaxagent

import (
	"time"

	"github.com/MemaxLabs/memax-go-agent-sdk/model"
)

type EventKind string

const (
	EventSessionStarted EventKind = "session_started"
	EventModelRequest   EventKind = "model_request"
	EventAssistant      EventKind = "assistant"
	EventToolUse        EventKind = "tool_use"
	EventToolResult     EventKind = "tool_result"
	EventError          EventKind = "error"
	EventResult         EventKind = "result"
)

// Event is emitted by Query as the orchestration loop progresses.
type Event struct {
	Kind      EventKind
	SessionID string
	Turn      int
	Time      time.Time

	Message    *model.Message
	ToolUse    *model.ToolUse
	ToolResult *model.ToolResult
	Result     string
	Err        error
}

func newEvent(kind EventKind, sessionID string, turn int) Event {
	return Event{
		Kind:      kind,
		SessionID: sessionID,
		Turn:      turn,
		Time:      time.Now().UTC(),
	}
}
