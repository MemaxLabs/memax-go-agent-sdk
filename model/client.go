package model

import (
	"context"
	"errors"
)

var ErrEndOfStream = errors.New("model stream ended")

type Client interface {
	Stream(context.Context, Request) (Stream, error)
}

type Request struct {
	SessionID          string
	ParentSessionID    string
	Messages           []Message
	Tools              []ToolSpec
	SystemPrompt       string
	AppendSystemPrompt string
}

type Stream interface {
	Recv() (StreamEvent, error)
	Close() error
}

type StreamEventKind string

const (
	StreamText    StreamEventKind = "text"
	StreamToolUse StreamEventKind = "tool_use"
)

type StreamEvent struct {
	Kind    StreamEventKind
	Text    string
	ToolUse ToolUse
}
