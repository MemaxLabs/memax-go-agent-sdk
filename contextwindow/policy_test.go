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
