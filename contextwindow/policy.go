package contextwindow

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/MemaxLabs/memax-go-agent-sdk/internal/metadatavalues"
	"github.com/MemaxLabs/memax-go-agent-sdk/model"
)

type Policy interface {
	Apply(context.Context, []model.Message) ([]model.Message, error)
}

// PolicyResult is the rich result returned by policies that can report
// context-management provenance in addition to transformed messages.
type PolicyResult struct {
	Messages   []model.Message
	Compaction *CompactionRecord
}

// PolicyWithResult is implemented by policies that expose provenance for the
// transformation they applied. Callers should fall back to Policy.Apply for
// policies that do not implement it.
type PolicyWithResult interface {
	ApplyWithResult(context.Context, []model.Message) (PolicyResult, error)
}

type CompactionReason string

const (
	CompactionReasonBudget CompactionReason = "budget"
)

const (
	MetadataContextSummary     = "context_summary"
	MetadataContextSummaryHash = "context_summary_hash"
	MetadataContextPolicy      = "context_policy"
)

// CompactionRecord describes a model-visible context compaction. It is emitted
// on EventContextCompacted and can be persisted by hosts for debugging or
// resume inspection.
type CompactionRecord struct {
	Policy             string
	Reason             CompactionReason
	OriginalMessages   int
	SentMessages       int
	SummarizedMessages int
	// RetainedMessages is the number of messages prepended by a retention
	// wrapper after the inner policy ran, not the total number of important
	// messages present in the final prompt.
	RetainedMessages  int
	ReplacedSummaries int
	SummaryHash       string
	SummaryPreview    string
}

type RecentMessages struct {
	MaxMessages int
}

func (p RecentMessages) Apply(_ context.Context, messages []model.Message) ([]model.Message, error) {
	if p.MaxMessages <= 0 {
		return nil, fmt.Errorf("contextwindow: MaxMessages must be positive")
	}
	if len(messages) <= p.MaxMessages {
		return model.CloneMessages(messages), nil
	}
	start := dropLeadingToolResults(messages, len(messages)-p.MaxMessages)
	return model.CloneMessages(messages[start:]), nil
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
	return model.CloneMessages(messages[start:]), nil
}

// PreserveImportant wraps another policy and prepends explicitly retained
// transcript groups that the wrapped policy dropped. It is useful for context
// retry and long-running sessions where aggressive trimming must not discard
// loaded skills, stored result handles, or recent tool errors.
//
// Retained tool results are kept with the assistant tool-use message that
// produced them when that message is available, so the provider transcript
// remains structurally valid. Because retained groups are added after the inner
// policy runs, the result may exceed the inner policy's message or token budget.
type PreserveImportant struct {
	Policy Policy
	// MaxMessages limits the number of retained important messages prepended to
	// the wrapped policy output. Zero uses a conservative default. Tool-use
	// groups may exceed this limit by one message to avoid orphan tool results.
	MaxMessages int
}

func (p PreserveImportant) Apply(ctx context.Context, messages []model.Message) ([]model.Message, error) {
	result, err := p.ApplyWithResult(ctx, messages)
	if err != nil {
		return nil, err
	}
	return result.Messages, nil
}

func (p PreserveImportant) ApplyWithResult(ctx context.Context, messages []model.Message) (PolicyResult, error) {
	selected := model.CloneMessages(messages)
	var record *CompactionRecord
	var err error
	if p.Policy != nil {
		if richer, ok := p.Policy.(PolicyWithResult); ok {
			result, err := richer.ApplyWithResult(ctx, messages)
			if err != nil {
				return PolicyResult{}, err
			}
			selected = result.Messages
			record = result.Compaction
		} else {
			selected, err = p.Policy.Apply(ctx, messages)
			if err != nil {
				return PolicyResult{}, err
			}
		}
	}
	out := PreserveImportantMessages(messages, selected, p.MaxMessages)
	if record != nil {
		record.SentMessages = len(out)
		record.RetainedMessages = len(out) - len(selected)
	}
	return PolicyResult{Messages: out, Compaction: record}, nil
}

// PreserveImportantMessages prepends important messages from original that are
// absent from selected. It returns a cloned slice and does not mutate either
// input.
func PreserveImportantMessages(original, selected []model.Message, maxMessages int) []model.Message {
	if len(original) == 0 {
		return model.CloneMessages(selected)
	}
	if maxMessages == 0 {
		maxMessages = 8
	}
	if maxMessages < 0 {
		return model.CloneMessages(selected)
	}
	groups := importantGroups(original, maxMessages)
	if len(groups) == 0 {
		return model.CloneMessages(selected)
	}
	out := make([]model.Message, 0, len(selected)+maxMessages)
	for _, group := range groups {
		if messageGroupContained(group, selected) {
			continue
		}
		out = append(out, model.CloneMessages(group)...)
	}
	out = append(out, model.CloneMessages(selected)...)
	return out
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
	result, err := p.ApplyWithResult(ctx, messages)
	if err != nil {
		return nil, err
	}
	return result.Messages, nil
}

func (p SummarizingBudget) ApplyWithResult(ctx context.Context, messages []model.Message) (PolicyResult, error) {
	if p.MaxTokens <= 0 {
		return PolicyResult{}, fmt.Errorf("contextwindow: MaxTokens must be positive")
	}
	estimate := p.Estimate
	if estimate == nil {
		estimate = EstimateByRunes
	}
	total, err := estimateMessages(messages, estimate)
	if err != nil {
		return PolicyResult{}, err
	}
	if total <= p.MaxTokens {
		return PolicyResult{Messages: model.CloneMessages(messages)}, nil
	}
	if p.Summarizer == nil {
		return PolicyResult{}, fmt.Errorf("contextwindow: Summarizer is required")
	}

	summaryBudget := p.summaryBudget()
	recentBudget := p.MaxTokens - summaryBudget
	if recentBudget < 1 {
		recentBudget = 1
	}
	start, err := newestSuffixStart(messages, recentBudget, estimate)
	if err != nil {
		return PolicyResult{}, err
	}
	replacedSummaries := countSummaryMessages(messages)
	recent, recentSummaries := splitSummaryMessages(messages[start:])
	prefix := model.CloneMessages(messages[:start])
	prefix = append(prefix, recentSummaries...)
	summary, err := p.Summarizer.Summarize(ctx, prefix)
	if err != nil {
		return PolicyResult{}, fmt.Errorf("contextwindow: summarize compacted messages: %w", err)
	}
	summaryMessage, err := p.summaryMessage(summary, summaryBudget, estimate)
	if err != nil {
		return PolicyResult{}, err
	}

	out := make([]model.Message, 0, 1+len(recent))
	out = append(out, summaryMessage)
	out = append(out, recent...)
	record := &CompactionRecord{
		Policy:             "SummarizingBudget",
		Reason:             CompactionReasonBudget,
		OriginalMessages:   len(messages),
		SentMessages:       len(out),
		SummarizedMessages: len(prefix),
		ReplacedSummaries:  replacedSummaries,
		SummaryHash:        metadatavalues.String(summaryMessage.Metadata, MetadataContextSummaryHash),
		SummaryPreview:     previewText(summaryMessage.PlainText(), 200),
	}
	return PolicyResult{Messages: out, Compaction: record}, nil
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
		Metadata: map[string]any{
			MetadataContextSummary:     true,
			MetadataContextSummaryHash: hashText(text),
			MetadataContextPolicy:      "SummarizingBudget",
		},
	}
	count := estimate(msg)
	if count < 0 {
		return model.Message{}, fmt.Errorf("contextwindow: estimator returned negative token count")
	}
	if maxTokens <= 0 || count <= maxTokens {
		return msg, nil
	}
	msg, err := truncateTextMessage(msg, maxTokens, estimate)
	if err != nil {
		return model.Message{}, err
	}
	msg.Metadata = model.CloneMetadata(msg.Metadata)
	msg.Metadata[MetadataContextSummaryHash] = hashText(msg.PlainText())
	return msg, nil
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

func IsSummaryMessage(msg model.Message) bool {
	return metadatavalues.Bool(msg.Metadata, MetadataContextSummary)
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

func countSummaryMessages(messages []model.Message) int {
	count := 0
	for _, msg := range messages {
		if IsSummaryMessage(msg) {
			count++
		}
	}
	return count
}

func splitSummaryMessages(messages []model.Message) ([]model.Message, []model.Message) {
	var kept []model.Message
	var summaries []model.Message
	for _, msg := range messages {
		if IsSummaryMessage(msg) {
			summaries = append(summaries, model.CloneMessage(msg))
			continue
		}
		kept = append(kept, model.CloneMessage(msg))
	}
	return kept, summaries
}

func importantGroups(messages []model.Message, maxMessages int) [][]model.Message {
	type group struct {
		start int
		end   int
	}
	groups := make([]group, 0)
	for i, msg := range messages {
		if !importantMessage(msg) {
			continue
		}
		start := i
		if msg.ToolResult != nil {
			if assistant := matchingToolUseMessage(messages, i, msg.ToolResult.ToolUseID); assistant >= 0 {
				start = assistant
			}
		}
		groups = append(groups, group{start: start, end: i + 1})
	}
	if len(groups) == 0 {
		return nil
	}

	selected := make([]group, 0, len(groups))
	count := 0
	for i := len(groups) - 1; i >= 0; i-- {
		g := groups[i]
		size := g.end - g.start
		if maxMessages > 0 && count > 0 && count+size > maxMessages {
			break
		}
		selected = append(selected, g)
		count += size
		if maxMessages > 0 && count >= maxMessages {
			break
		}
	}
	slices.Reverse(selected)

	out := make([][]model.Message, 0, len(selected))
	for _, g := range selected {
		out = append(out, model.CloneMessages(messages[g.start:g.end]))
	}
	return out
}

func importantMessage(msg model.Message) bool {
	if msg.ToolResult == nil {
		return false
	}
	result := msg.ToolResult
	if result.IsError {
		return true
	}
	if metadatavalues.String(result.Metadata, model.MetadataContextRetention) == model.RetentionImportant {
		return true
	}
	if metadatavalues.Bool(result.Metadata, model.MetadataLoadedSkill) {
		return true
	}
	if metadatavalues.String(result.Metadata, "stored_result_id") != "" {
		return true
	}
	if metadatavalues.String(result.Metadata, "stored_result_uri") != "" {
		return true
	}
	return false
}

func matchingToolUseMessage(messages []model.Message, before int, toolUseID string) int {
	if toolUseID == "" {
		return -1
	}
	for i := before - 1; i >= 0; i-- {
		if messages[i].Role == model.RoleUser {
			return -1
		}
		if messages[i].Role != model.RoleAssistant {
			continue
		}
		if messageHasToolUse(messages[i], toolUseID) {
			return i
		}
	}
	return -1
}

func messageHasToolUse(msg model.Message, id string) bool {
	for _, block := range msg.Content {
		if block.ToolUse != nil && block.ToolUse.ID == id {
			return true
		}
	}
	return false
}

func messageSignature(msg model.Message) string {
	if msg.ID != "" {
		return "id:" + msg.ID
	}
	if msg.ToolResult != nil {
		return "tool:" + msg.ToolResult.ToolUseID + ":" + msg.ToolResult.Name
	}
	toolUseIDs := make([]string, 0)
	for _, block := range msg.Content {
		if block.ToolUse != nil {
			toolUseIDs = append(toolUseIDs, block.ToolUse.ID+":"+block.ToolUse.Name)
		}
	}
	if len(toolUseIDs) > 0 {
		return "assistant_tool_use:" + strings.Join(toolUseIDs, ",")
	}
	return string(msg.Role) + ":" + msg.PlainText()
}

func messageGroupContained(group, messages []model.Message) bool {
	if len(group) == 0 {
		return true
	}
	if len(group) > len(messages) {
		return false
	}
	for start := 0; start <= len(messages)-len(group); start++ {
		matches := true
		for i := range group {
			if messageSignature(group[i]) != messageSignature(messages[start+i]) {
				matches = false
				break
			}
		}
		if matches {
			return true
		}
	}
	return false
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
		candidate.Content = model.CloneContentBlocks(msg.Content)
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
	msg.Content = model.CloneContentBlocks(msg.Content)
	msg.Content[0].Text = string(runes[:best])
	if strings.TrimSpace(msg.Content[0].Text) == "" {
		return model.Message{}, fmt.Errorf("contextwindow: summary cannot fit budget")
	}
	return msg, nil
}

func hashText(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

func previewText(text string, maxRunes int) string {
	if maxRunes <= 0 {
		return ""
	}
	text = strings.TrimSpace(text)
	runes := []rune(text)
	if len(runes) <= maxRunes {
		return text
	}
	return string(runes[:maxRunes])
}
