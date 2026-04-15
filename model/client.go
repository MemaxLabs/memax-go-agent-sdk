package model

import (
	"context"
	"errors"
)

var ErrEndOfStream = errors.New("model stream ended")
var ErrContextWindowExceeded = errors.New("model context window exceeded")

// IsContextWindowExceeded reports whether err means the provider rejected the
// request because the prompt/context window was too large.
func IsContextWindowExceeded(err error) bool {
	return errors.Is(err, ErrContextWindowExceeded)
}

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
	StreamUsage   StreamEventKind = "usage"
)

type StreamEvent struct {
	Kind    StreamEventKind
	Text    string
	ToolUse ToolUse
	Usage   *Usage
}

// Usage is provider-neutral model token accounting for one model stream event
// or an aggregate run total. Zero fields mean the provider did not report that
// value.
type Usage struct {
	Provider     string         `json:"provider,omitempty"`
	Model        string         `json:"model,omitempty"`
	InputTokens  int            `json:"input_tokens,omitempty"`
	OutputTokens int            `json:"output_tokens,omitempty"`
	TotalTokens  int            `json:"total_tokens,omitempty"`
	Metadata     map[string]any `json:"metadata,omitempty"`
}

// Add returns a copy of u with next's token counts added. Provider, model, and
// metadata are kept from the first non-empty value.
func (u Usage) Add(next Usage) Usage {
	if u.Provider == "" {
		u.Provider = next.Provider
	}
	if u.Model == "" {
		u.Model = next.Model
	}
	u.InputTokens += next.InputTokens
	u.OutputTokens += next.OutputTokens
	u.TotalTokens += next.TotalTokens
	if len(u.Metadata) == 0 && len(next.Metadata) > 0 {
		u.Metadata = cloneUsageMetadata(next.Metadata)
	}
	return u
}

func cloneUsageMetadata(metadata map[string]any) map[string]any {
	if len(metadata) == 0 {
		return nil
	}
	out := make(map[string]any, len(metadata))
	for key, value := range metadata {
		out[key] = value
	}
	return out
}
