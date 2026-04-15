package contextwindow

import (
	"context"
	"fmt"
	"strings"
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

	start, err := newestSuffixStart(messages, p.MaxTokens, estimate)
	if err != nil {
		return nil, err
	}
	return cloneMessages(messages[start:]), nil
}

// Summarizer compacts older transcript messages into a model-visible summary.
type Summarizer interface {
	Summarize(context.Context, []model.Message) (string, error)
}

// SummarizerFunc adapts a function to the Summarizer interface.
type SummarizerFunc func(context.Context, []model.Message) (string, error)

// Summarize calls f(ctx, messages).
func (f SummarizerFunc) Summarize(ctx context.Context, messages []model.Message) (string, error) {
	return f(ctx, messages)
}

// SummarizingBudget summarizes older messages when the transcript exceeds
// MaxTokens, then prepends the summary to the newest structurally valid suffix.
type SummarizingBudget struct {
	MaxTokens        int
	MaxSummaryTokens int
	Estimate         Estimator
	Summarizer       Summarizer
	SummaryRole      model.Role
	SummaryPrefix    string
}

func (p SummarizingBudget) Apply(ctx context.Context, messages []model.Message) ([]model.Message, error) {
	if p.MaxTokens <= 0 {
		return nil, fmt.Errorf("contextwindow: MaxTokens must be positive")
	}
	estimate := p.Estimate
	if estimate == nil {
		estimate = EstimateByRunes
	}
	total, err := estimateMessages(messages, estimate)
	if err != nil {
		return nil, err
	}
	if total <= p.MaxTokens {
		return cloneMessages(messages), nil
	}
	if p.Summarizer == nil {
		return nil, fmt.Errorf("contextwindow: Summarizer is required")
	}

	summaryBudget := p.summaryBudget()
	recentBudget := p.MaxTokens - summaryBudget
	if recentBudget < 1 {
		recentBudget = 1
	}
	start, err := newestSuffixStart(messages, recentBudget, estimate)
	if err != nil {
		return nil, err
	}
	prefix := cloneMessages(messages[:start])
	recent := cloneMessages(messages[start:])
	summary, err := p.Summarizer.Summarize(ctx, prefix)
	if err != nil {
		return nil, fmt.Errorf("contextwindow: summarize compacted messages: %w", err)
	}
	summaryMessage, err := p.summaryMessage(summary, summaryBudget, estimate)
	if err != nil {
		return nil, err
	}

	out := make([]model.Message, 0, 1+len(recent))
	out = append(out, summaryMessage)
	out = append(out, recent...)
	return out, nil
}

func (p SummarizingBudget) summaryBudget() int {
	if p.MaxSummaryTokens > 0 {
		if p.MaxTokens <= 1 {
			return 1
		}
		if p.MaxSummaryTokens >= p.MaxTokens && p.MaxTokens > 1 {
			return p.MaxTokens - 1
		}
		return p.MaxSummaryTokens
	}
	budget := p.MaxTokens / 4
	if budget < 1 {
		return 1
	}
	return budget
}

func (p SummarizingBudget) summaryMessage(summary string, maxTokens int, estimate Estimator) (model.Message, error) {
	summary = strings.TrimSpace(summary)
	if summary == "" {
		return model.Message{}, fmt.Errorf("contextwindow: summarizer returned empty summary")
	}
	prefix := p.SummaryPrefix
	if prefix == "" {
		prefix = "Previous conversation summary:\n"
	}
	text := prefix + summary
	msg := model.Message{
		Role: p.summaryRole(),
		Content: []model.ContentBlock{
			{Type: model.ContentText, Text: text},
		},
	}
	count := estimate(msg)
	if count < 0 {
		return model.Message{}, fmt.Errorf("contextwindow: estimator returned negative token count")
	}
	if maxTokens <= 0 || count <= maxTokens {
		return msg, nil
	}
	return truncateTextMessage(msg, maxTokens, estimate)
}

func (p SummarizingBudget) summaryRole() model.Role {
	if p.SummaryRole != "" {
		return p.SummaryRole
	}
	return model.RoleUser
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

func newestSuffixStart(messages []model.Message, maxTokens int, estimate Estimator) (int, error) {
	start := len(messages)
	total := 0
	for start > 0 {
		next := estimate(messages[start-1])
		if next < 0 {
			return 0, fmt.Errorf("contextwindow: estimator returned negative token count")
		}
		if total > 0 && total+next > maxTokens {
			break
		}
		total += next
		start--
		if total >= maxTokens {
			break
		}
	}
	return dropLeadingToolResults(messages, start), nil
}

func estimateMessages(messages []model.Message, estimate Estimator) (int, error) {
	total := 0
	for _, msg := range messages {
		next := estimate(msg)
		if next < 0 {
			return 0, fmt.Errorf("contextwindow: estimator returned negative token count")
		}
		total += next
	}
	return total, nil
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

func truncateTextMessage(msg model.Message, maxTokens int, estimate Estimator) (model.Message, error) {
	if len(msg.Content) == 0 {
		return msg, nil
	}
	text := msg.Content[0].Text
	runes := []rune(text)
	low, high := 0, len(runes)
	best := -1
	for low <= high {
		mid := low + (high-low)/2
		candidate := msg
		candidate.Content = cloneBlocks(msg.Content)
		candidate.Content[0].Text = string(runes[:mid])
		count := estimate(candidate)
		if count < 0 {
			return model.Message{}, fmt.Errorf("contextwindow: estimator returned negative token count")
		}
		if count <= maxTokens {
			best = mid
			low = mid + 1
		} else {
			high = mid - 1
		}
	}
	if best < 0 {
		return model.Message{}, fmt.Errorf("contextwindow: summary cannot fit budget")
	}
	msg.Content = cloneBlocks(msg.Content)
	msg.Content[0].Text = string(runes[:best])
	if strings.TrimSpace(msg.Content[0].Text) == "" {
		return model.Message{}, fmt.Errorf("contextwindow: summary cannot fit budget")
	}
	return msg, nil
}

func cloneBlocks(blocks []model.ContentBlock) []model.ContentBlock {
	out := make([]model.ContentBlock, len(blocks))
	copy(out, blocks)
	return out
}
