package contextwindow

import (
	"context"
	"fmt"

	"github.com/MemaxLabs/memax-go-agent-sdk/model"
)

type Policy interface {
	Apply(context.Context, []model.Message) ([]model.Message, error)
}

type RecentMessages struct {
	MaxMessages int
}

func (p RecentMessages) Apply(_ context.Context, messages []model.Message) ([]model.Message, error) {
	if p.MaxMessages <= 0 {
		return nil, fmt.Errorf("contextwindow: MaxMessages must be positive")
	}
	if len(messages) <= p.MaxMessages {
		return cloneMessages(messages), nil
	}
	start := len(messages) - p.MaxMessages
	for start < len(messages) && messages[start].Role == model.RoleTool {
		start++
	}
	return cloneMessages(messages[start:]), nil
}

func cloneMessages(messages []model.Message) []model.Message {
	out := make([]model.Message, len(messages))
	copy(out, messages)
	return out
}
