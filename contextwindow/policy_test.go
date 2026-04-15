package contextwindow

import (
	"context"
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
		},
	}
	if got, want := EstimateByRunes(msg), 17; got != want {
		t.Fatalf("EstimateByRunes = %d, want %d", got, want)
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
