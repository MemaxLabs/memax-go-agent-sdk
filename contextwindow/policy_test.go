package contextwindow

import (
	"context"
	"strings"
	"testing"

	"github.com/MemaxLabs/memax-go-agent-sdk/model"
)

func TestRecentMessagesKeepsRecentSuffix(t *testing.T) {
	messages := []model.Message{
		{Role: model.RoleUser},
		{Role: model.RoleAssistant},
		{Role: model.RoleUser},
	}

	got, err := (RecentMessages{MaxMessages: 2}).Apply(context.Background(), messages)
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}
	if len(got) != 2 || got[0].Role != model.RoleAssistant || got[1].Role != model.RoleUser {
		t.Fatalf("messages = %#v, want last two", got)
	}
}

func TestRecentMessagesDropsLeadingOrphanToolResults(t *testing.T) {
	messages := []model.Message{
		{Role: model.RoleUser},
		{Role: model.RoleAssistant},
		{Role: model.RoleTool},
		{Role: model.RoleUser},
	}

	got, err := (RecentMessages{MaxMessages: 2}).Apply(context.Background(), messages)
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}
	if len(got) != 1 || got[0].Role != model.RoleUser {
		t.Fatalf("messages = %#v, want orphan tool result dropped", got)
	}
}

func TestRecentMessagesRejectsInvalidLimit(t *testing.T) {
	_, err := (RecentMessages{}).Apply(context.Background(), nil)
	if err == nil {
		t.Fatal("Apply returned nil, want invalid limit error")
	}
}

func TestTokenBudgetKeepsNewestMessagesWithinBudget(t *testing.T) {
	messages := []model.Message{
		textMessage(model.RoleUser, "aaaaa"),
		textMessage(model.RoleAssistant, "bbbbb"),
		textMessage(model.RoleUser, "cc"),
	}

	got, err := (TokenBudget{MaxTokens: 7}).Apply(context.Background(), messages)
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}
	if len(got) != 2 || got[0].PlainText() != "bbbbb" || got[1].PlainText() != "cc" {
		t.Fatalf("messages = %#v, want last two within budget", got)
	}
}

func TestTokenBudgetKeepsOversizedNewestMessage(t *testing.T) {
	messages := []model.Message{
		textMessage(model.RoleUser, "small"),
		textMessage(model.RoleAssistant, "this is oversized"),
	}

	got, err := (TokenBudget{MaxTokens: 2}).Apply(context.Background(), messages)
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}
	if len(got) != 1 || got[0].PlainText() != "this is oversized" {
		t.Fatalf("messages = %#v, want newest oversized message", got)
	}
}

func TestTokenBudgetDropsLeadingOrphanToolResults(t *testing.T) {
	messages := []model.Message{
		textMessage(model.RoleAssistant, "tool call"),
		{Role: model.RoleTool, ToolResult: &model.ToolResult{Name: "read", Content: "large result"}},
		textMessage(model.RoleUser, "next"),
	}

	got, err := (TokenBudget{MaxTokens: 16}).Apply(context.Background(), messages)
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}
	if len(got) != 1 || got[0].Role != model.RoleUser {
		t.Fatalf("messages = %#v, want orphan tool result dropped", got)
	}
}

func TestTokenBudgetRejectsInvalidEstimator(t *testing.T) {
	_, err := (TokenBudget{
		MaxTokens: 10,
		Estimate: func(model.Message) int {
			return -1
		},
	}).Apply(context.Background(), []model.Message{textMessage(model.RoleUser, "x")})
	if err == nil {
		t.Fatal("Apply returned nil, want estimator error")
	}
}

func TestEstimateByRunesIncludesToolPayloads(t *testing.T) {
	msg := model.Message{
		Role: model.RoleAssistant,
		Content: []model.ContentBlock{
			{Type: model.ContentText, Text: "é"},
			{Type: model.ContentToolUse, ToolUse: &model.ToolUse{Name: "read", Input: []byte(`{"path":"x"}`)}},
			{
				Type: model.ContentProviderArtifact,
				ProviderArtifact: &model.ProviderArtifact{
					Provider: "openai",
					Type:     "reasoning",
					ID:       "rs_1",
					Data:     []byte(`{"encrypted_content":"opaque"}`),
				},
			},
		},
	}
	if got, want := EstimateByRunes(msg), 66; got != want {
		t.Fatalf("EstimateByRunes = %d, want %d", got, want)
	}
}

func TestSummarizingBudgetPrependsSummaryForCompactedPrefix(t *testing.T) {
	var summarized []model.Message
	result, err := (SummarizingBudget{
		MaxTokens:        16,
		MaxSummaryTokens: 10,
		SummaryPrefix:    "S:",
		Summarizer: SummarizerFunc(func(_ context.Context, messages []model.Message) (string, error) {
			summarized = model.CloneMessages(messages)
			return "summary", nil
		}),
	}).ApplyWithResult(context.Background(), []model.Message{
		textMessage(model.RoleUser, "old-old"),
		textMessage(model.RoleAssistant, "middle!"),
		textMessage(model.RoleUser, "recent"),
	})
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}
	if len(summarized) != 2 {
		t.Fatalf("summarized len = %d, want 2", len(summarized))
	}
	got := result.Messages
	if len(got) != 2 {
		t.Fatalf("messages len = %d, want 2", len(got))
	}
	if got[0].Role != model.RoleUser || got[0].PlainText() != "S:summary" {
		t.Fatalf("summary message = %#v", got[0])
	}
	if got[1].PlainText() != "recent" {
		t.Fatalf("recent message = %#v", got[1])
	}
	if !IsSummaryMessage(got[0]) {
		t.Fatalf("summary metadata = %#v, want context summary marker", got[0].Metadata)
	}
	if result.Compaction == nil {
		t.Fatal("compaction record is nil")
	}
	if result.Compaction.OriginalMessages != 3 || result.Compaction.SentMessages != 2 || result.Compaction.SummarizedMessages != 2 {
		t.Fatalf("compaction record = %#v, want 3 -> 2 with 2 summarized", result.Compaction)
	}
	if result.Compaction.SummaryHash == "" {
		t.Fatalf("summary hash is empty: %#v", result.Compaction)
	}
	if result.Compaction.SummaryPreview != "S:summary" {
		t.Fatalf("summary preview = %q, want S:summary", result.Compaction.SummaryPreview)
	}
}

func TestSummarizingBudgetSkipsSummarizerWhenMessagesFit(t *testing.T) {
	called := false
	got, err := (SummarizingBudget{
		MaxTokens: 100,
		Summarizer: SummarizerFunc(func(_ context.Context, _ []model.Message) (string, error) {
			called = true
			return "summary", nil
		}),
	}).Apply(context.Background(), []model.Message{
		textMessage(model.RoleUser, "small"),
	})
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}
	if called {
		t.Fatal("summarizer was called for messages that fit")
	}
	if len(got) != 1 || got[0].PlainText() != "small" {
		t.Fatalf("messages = %#v", got)
	}
}

func TestSummarizingBudgetUsesTriggerTokensForHysteresis(t *testing.T) {
	calls := 0
	policy := SummarizingBudget{
		MaxTokens:        10,
		TriggerTokens:    20,
		MaxSummaryTokens: 4,
		SummaryPrefix:    "S:",
		Estimate:         EstimateByRunes,
		Summarizer: SummarizerFunc(func(_ context.Context, _ []model.Message) (string, error) {
			calls++
			return "s", nil
		}),
	}

	got, err := policy.Apply(context.Background(), []model.Message{
		textMessage(model.RoleUser, "123456789012345"),
	})
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}
	if calls != 0 {
		t.Fatal("summarizer was called before trigger threshold")
	}
	if len(got) != 1 || got[0].PlainText() != "123456789012345" {
		t.Fatalf("messages = %#v", got)
	}

	result, err := policy.ApplyWithResult(context.Background(), []model.Message{
		textMessage(model.RoleUser, "1234567890"),
		textMessage(model.RoleAssistant, "abcdefghi"),
		textMessage(model.RoleUser, "recent"),
	})
	if err != nil {
		t.Fatalf("ApplyWithResult returned error: %v", err)
	}
	if calls != 1 {
		t.Fatalf("summarizer calls = %d, want 1 after trigger threshold", calls)
	}
	total, err := estimateMessages(result.Messages, EstimateByRunes)
	if err != nil {
		t.Fatalf("estimateMessages returned error: %v", err)
	}
	if total > policy.MaxTokens {
		t.Fatalf("compacted total = %d, want <= %d; messages = %#v", total, policy.MaxTokens, result.Messages)
	}
	if result.Compaction == nil {
		t.Fatal("compaction record is nil after trigger threshold")
	}
}

func TestSummarizingBudgetRejectsMissingSummarizer(t *testing.T) {
	_, err := (SummarizingBudget{MaxTokens: 1}).Apply(context.Background(), []model.Message{
		textMessage(model.RoleUser, "too large"),
	})
	if err == nil {
		t.Fatal("Apply returned nil, want missing summarizer error")
	}
}

func TestSummarizingBudgetDropsLeadingOrphanToolResults(t *testing.T) {
	got, err := (SummarizingBudget{
		MaxTokens:        10,
		MaxSummaryTokens: 5,
		SummaryPrefix:    "S:",
		Summarizer: SummarizerFunc(func(_ context.Context, messages []model.Message) (string, error) {
			if len(messages) != 2 || messages[1].Role != model.RoleTool {
				t.Fatalf("summarized messages = %#v, want assistant plus tool result", messages)
			}
			return "sum", nil
		}),
	}).Apply(context.Background(), []model.Message{
		textMessage(model.RoleAssistant, "tool call"),
		{Role: model.RoleTool, ToolResult: &model.ToolResult{Name: "read", Content: "large result"}},
		textMessage(model.RoleUser, "next"),
	})
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}
	if len(got) != 2 || got[1].Role != model.RoleUser || got[1].PlainText() != "next" {
		t.Fatalf("messages = %#v, want summary plus user message", got)
	}
}

func TestSummarizingBudgetTruncatesSummaryToBudget(t *testing.T) {
	got, err := (SummarizingBudget{
		MaxTokens:        10,
		MaxSummaryTokens: 4,
		SummaryPrefix:    "S:",
		Summarizer: SummarizerFunc(func(_ context.Context, _ []model.Message) (string, error) {
			return "abcdef", nil
		}),
	}).Apply(context.Background(), []model.Message{
		textMessage(model.RoleUser, "old-old"),
		textMessage(model.RoleUser, "recent"),
	})
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}
	if got[0].PlainText() != "S:ab" {
		t.Fatalf("summary = %q, want truncated text", got[0].PlainText())
	}
	if got[0].Metadata[MetadataContextSummaryHash] != hashText("S:ab") {
		t.Fatalf("summary metadata = %#v, want hash of truncated text", got[0].Metadata)
	}
}

func TestSummarizingBudgetReplacesPriorSummaries(t *testing.T) {
	prior := model.Message{
		Role:     model.RoleUser,
		Content:  []model.ContentBlock{{Type: model.ContentText, Text: "S:old summary"}},
		Metadata: map[string]any{MetadataContextSummary: true},
	}
	var summarized []model.Message
	result, err := (SummarizingBudget{
		MaxTokens:        18,
		MaxSummaryTokens: 12,
		SummaryPrefix:    "S:",
		Summarizer: SummarizerFunc(func(_ context.Context, messages []model.Message) (string, error) {
			summarized = model.CloneMessages(messages)
			return "new summary", nil
		}),
	}).ApplyWithResult(context.Background(), []model.Message{
		prior,
		textMessage(model.RoleUser, "old work"),
		textMessage(model.RoleUser, "recent"),
	})
	if err != nil {
		t.Fatalf("ApplyWithResult returned error: %v", err)
	}
	if len(summarized) != 2 || !IsSummaryMessage(summarized[0]) {
		t.Fatalf("summarized messages = %#v, want prior summary plus old work", summarized)
	}
	summaryCount := 0
	for _, msg := range result.Messages {
		if IsSummaryMessage(msg) {
			summaryCount++
		}
		if msg.PlainText() == "S:old summary" {
			t.Fatalf("old summary remained active in messages: %#v", result.Messages)
		}
	}
	if summaryCount != 1 {
		t.Fatalf("summary count = %d, want one active summary: %#v", summaryCount, result.Messages)
	}
	if result.Compaction == nil || result.Compaction.ReplacedSummaries != 1 {
		t.Fatalf("compaction record = %#v, want one replaced summary", result.Compaction)
	}
}

func TestSummarizingBudgetCarriesRecentSummaryIntoNextSummary(t *testing.T) {
	prior := model.Message{
		Role:     model.RoleUser,
		Content:  []model.ContentBlock{{Type: model.ContentText, Text: "S:old"}},
		Metadata: map[string]any{MetadataContextSummary: true},
	}
	var summarized []model.Message
	result, err := (SummarizingBudget{
		MaxTokens:        22,
		MaxSummaryTokens: 10,
		SummaryPrefix:    "S:",
		Summarizer: SummarizerFunc(func(_ context.Context, messages []model.Message) (string, error) {
			summarized = model.CloneMessages(messages)
			return "new", nil
		}),
	}).ApplyWithResult(context.Background(), []model.Message{
		textMessage(model.RoleUser, "old old old old"),
		prior,
		textMessage(model.RoleUser, "recent"),
	})
	if err != nil {
		t.Fatalf("ApplyWithResult returned error: %v", err)
	}
	foundPrior := false
	for _, msg := range summarized {
		if msg.PlainText() == "S:old" {
			foundPrior = true
		}
	}
	if !foundPrior {
		t.Fatalf("summarized messages = %#v, want old summary included", summarized)
	}
	summaryCount := 0
	for _, msg := range result.Messages {
		if IsSummaryMessage(msg) {
			summaryCount++
		}
		if msg.PlainText() == "S:old" {
			t.Fatalf("old summary remained active in messages: %#v", result.Messages)
		}
	}
	if summaryCount != 1 {
		t.Fatalf("summary count = %d, want one active summary: %#v", summaryCount, result.Messages)
	}
}

func TestModelSummarizerStreamsSummary(t *testing.T) {
	client := &summaryClient{
		events: []model.StreamEvent{
			{Kind: model.StreamText, Text: "first "},
			{Kind: model.StreamText, Text: "second"},
		},
	}
	summary, err := (ModelSummarizer{Model: client}).Summarize(context.Background(), []model.Message{
		textMessage(model.RoleUser, "original request"),
		{
			Role: model.RoleTool,
			ToolResult: &model.ToolResult{
				ToolUseID: "toolu_1",
				Name:      "read_file",
				Content:   "contents",
			},
		},
	})
	if err != nil {
		t.Fatalf("Summarize returned error: %v", err)
	}
	if summary != "first second" {
		t.Fatalf("summary = %q", summary)
	}
	if len(client.request.Messages) != 1 || !strings.Contains(client.request.Messages[0].PlainText(), "tool_result toolu_1 read_file: contents") {
		t.Fatalf("request transcript = %#v", client.request.Messages)
	}
}

func TestPreserveImportantKeepsLoadedSkillWithToolUse(t *testing.T) {
	messages := []model.Message{
		textMessage(model.RoleUser, "use the database skill"),
		toolUseMessage("skill-1", "load_skill"),
		toolResultMessage("skill-1", "load_skill", "skill instructions", map[string]any{
			model.MetadataLoadedSkill:      true,
			model.MetadataContextRetention: model.RetentionImportant,
		}, false),
		textMessage(model.RoleUser, "now answer"),
	}

	got, err := (PreserveImportant{Policy: RecentMessages{MaxMessages: 1}}).Apply(context.Background(), messages)
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("messages len = %d, want retained tool-use pair plus selected suffix: %#v", len(got), got)
	}
	if got[0].Role != model.RoleAssistant || !messageHasToolUse(got[0], "skill-1") {
		t.Fatalf("first message = %#v, want assistant tool use", got[0])
	}
	if got[1].ToolResult == nil || got[1].ToolResult.Name != "load_skill" {
		t.Fatalf("second message = %#v, want load_skill result", got[1])
	}
	if got[2].PlainText() != "now answer" {
		t.Fatalf("last message = %#v, want selected suffix", got[2])
	}
}

func TestPreserveImportantKeepsStoredResultHandle(t *testing.T) {
	messages := []model.Message{
		textMessage(model.RoleUser, "read report"),
		toolUseMessage("tool-1", "read_large_report"),
		toolResultMessage("tool-1", "read_large_report", "preview", map[string]any{
			"stored_result_id":             "result-1",
			model.MetadataContextRetention: model.RetentionImportant,
		}, false),
		textMessage(model.RoleUser, "continue"),
	}

	got, err := (PreserveImportant{Policy: RecentMessages{MaxMessages: 1}}).Apply(context.Background(), messages)
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}
	if len(got) != 3 || got[1].ToolResult == nil || got[1].ToolResult.Metadata["stored_result_id"] != "result-1" {
		t.Fatalf("messages = %#v, want stored result handle preserved", got)
	}
}

func TestPreserveImportantKeepsToolErrors(t *testing.T) {
	messages := []model.Message{
		textMessage(model.RoleUser, "write file"),
		toolUseMessage("tool-1", "write_file"),
		toolResultMessage("tool-1", "write_file", "permission denied", nil, true),
		textMessage(model.RoleUser, "recover"),
	}

	got, err := (PreserveImportant{Policy: RecentMessages{MaxMessages: 1}}).Apply(context.Background(), messages)
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}
	if len(got) != 3 || got[1].ToolResult == nil || !got[1].ToolResult.IsError {
		t.Fatalf("messages = %#v, want tool error group preserved", got)
	}
}

func TestPreserveImportantAvoidsDuplicates(t *testing.T) {
	messages := []model.Message{
		textMessage(model.RoleUser, "read"),
		toolUseMessage("tool-1", "read_large_report"),
		toolResultMessage("tool-1", "read_large_report", "preview", map[string]any{
			"stored_result_id": "result-1",
		}, false),
	}

	got, err := (PreserveImportant{Policy: RecentMessages{MaxMessages: 2}}).Apply(context.Background(), messages)
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("messages len = %d, want no duplicate retained group: %#v", len(got), got)
	}
}

func TestPreserveImportantDoesNotCreatePartialGroups(t *testing.T) {
	messages := []model.Message{
		textMessage(model.RoleUser, "read"),
		toolUseMessage("tool-1", "read_large_report"),
		toolResultMessage("tool-1", "read_large_report", "preview", map[string]any{
			"stored_result_id": "result-1",
		}, false),
	}
	selected := []model.Message{toolUseMessage("tool-1", "read_large_report")}

	got := PreserveImportantMessages(messages, selected, 0)
	if len(got) != 3 {
		t.Fatalf("messages len = %d, want full retained group plus selected partial group: %#v", len(got), got)
	}
	if !messageHasToolUse(got[0], "tool-1") || got[1].ToolResult == nil {
		t.Fatalf("messages = %#v, want retained assistant/result group first", got)
	}
}

func TestPreserveImportantReturnsDefensiveCopies(t *testing.T) {
	messages := []model.Message{
		toolUseMessage("tool-1", "read_large_report"),
		toolResultMessage("tool-1", "read_large_report", "preview", map[string]any{
			"stored_result_id": "result-1",
		}, false),
	}

	got := PreserveImportantMessages(messages, nil, 0)
	got[0].Content[0].ToolUse.Name = "mutated"
	got[1].ToolResult.Metadata["stored_result_id"] = "mutated"

	if messages[0].Content[0].ToolUse.Name != "read_large_report" {
		t.Fatalf("original tool use mutated: %#v", messages[0])
	}
	if messages[1].ToolResult.Metadata["stored_result_id"] != "result-1" {
		t.Fatalf("original metadata mutated: %#v", messages[1].ToolResult.Metadata)
	}
}

func textMessage(role model.Role, text string) model.Message {
	return model.Message{
		Role: role,
		Content: []model.ContentBlock{
			{Type: model.ContentText, Text: text},
		},
	}
}

func toolUseMessage(id, name string) model.Message {
	return model.Message{
		Role: model.RoleAssistant,
		Content: []model.ContentBlock{
			{Type: model.ContentToolUse, ToolUse: &model.ToolUse{ID: id, Name: name}},
		},
	}
}

func toolResultMessage(id, name, content string, metadata map[string]any, isError bool) model.Message {
	return model.Message{
		Role: model.RoleTool,
		ToolResult: &model.ToolResult{
			ToolUseID: id,
			Name:      name,
			Content:   content,
			IsError:   isError,
			Metadata:  metadata,
		},
	}
}

type summaryClient struct {
	request model.Request
	events  []model.StreamEvent
}

func (c *summaryClient) Stream(_ context.Context, req model.Request) (model.Stream, error) {
	c.request = req
	return &summaryStream{events: c.events}, nil
}

type summaryStream struct {
	events []model.StreamEvent
	index  int
}

func (s *summaryStream) Recv() (model.StreamEvent, error) {
	if s.index >= len(s.events) {
		return model.StreamEvent{}, model.ErrEndOfStream
	}
	event := s.events[s.index]
	s.index++
	return event, nil
}

func (s *summaryStream) Close() error {
	return nil
}
