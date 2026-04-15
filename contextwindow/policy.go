package contextwindow

import (
	"context"
	"fmt"
	"unicode/utf8"

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
	start := dropLeadingToolResults(messages, len(messages)-p.MaxMessages)
	return cloneMessages(messages[start:]), nil
}

type Estimator func(model.Message) int

type TokenBudget struct {
	MaxTokens int
	Estimate  Estimator
}

func (p TokenBudget) Apply(_ context.Context, messages []model.Message) ([]model.Message, error) {
	if p.MaxTokens <= 0 {
		return nil, fmt.Errorf("contextwindow: MaxTokens must be positive")
	}
	estimate := p.Estimate
	if estimate == nil {
		estimate = EstimateByRunes
	}

	start := len(messages)
	total := 0
	for start > 0 {
		next := estimate(messages[start-1])
		if next < 0 {
			return nil, fmt.Errorf("contextwindow: estimator returned negative token count")
		}
		if total > 0 && total+next > p.MaxTokens {
			break
		}
		total += next
		start--
		if total >= p.MaxTokens {
			break
		}
	}
	start = dropLeadingToolResults(messages, start)
	return cloneMessages(messages[start:]), nil
}

func EstimateByRunes(msg model.Message) int {
	total := 0
	for _, block := range msg.Content {
		total += utf8.RuneCountInString(block.Text)
		if block.ToolUse != nil {
			total += utf8.RuneCountInString(block.ToolUse.Name)
			total += len(block.ToolUse.Input)
		}
	}
	if msg.ToolResult != nil {
		total += utf8.RuneCountInString(msg.ToolResult.Name)
		total += utf8.RuneCountInString(msg.ToolResult.Content)
	}
	return total
}

func dropLeadingToolResults(messages []model.Message, start int) int {
	for start < len(messages) && messages[start].Role == model.RoleTool {
		start++
	}
	return start
}

func cloneMessages(messages []model.Message) []model.Message {
	out := make([]model.Message, len(messages))
	copy(out, messages)
	return out
}
