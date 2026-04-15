package memory

import (
	"context"
	"testing"

	"github.com/MemaxLabs/memax-go-agent-sdk/model"
)

func TestStaticSourceReturnsDefensiveCopy(t *testing.T) {
	source := StaticSource{{
		Name:     "rules",
		Scope:    ScopeProject,
		Content:  "inspect before edit",
		Tags:     []string{"project"},
		Metadata: map[string]any{"owner": "host"},
	}}

	first, err := source.Memories(context.Background(), Request{})
	if err != nil {
		t.Fatalf("Memories returned error: %v", err)
	}
	first[0].Tags[0] = "mutated"
	first[0].Metadata["owner"] = "mutated"

	second, err := source.Memories(context.Background(), Request{})
	if err != nil {
		t.Fatalf("Memories returned error: %v", err)
	}
	if second[0].Tags[0] != "project" || second[0].Metadata["owner"] != "host" {
		t.Fatalf("source was mutated: %#v", second[0])
	}
}

func TestMultiSourceDeduplicatesByScopeAndName(t *testing.T) {
	source := MultiSource{
		StaticSource{
			{Name: "style", Scope: ScopeProject, Content: "first"},
			{Name: "style", Scope: ScopeUser, Content: "user"},
		},
		StaticSource{
			{Name: "style", Scope: ScopeProject, Content: "second"},
			{Name: "", Content: "anonymous"},
		},
	}

	got, err := source.Memories(context.Background(), Request{})
	if err != nil {
		t.Fatalf("Memories returned error: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("memories = %#v, want 3 entries", got)
	}
	if got[0].Content != "first" || got[1].Content != "user" || got[2].Content != "anonymous" {
		t.Fatalf("unexpected memory order/content: %#v", got)
	}
}

func TestSourceFuncReceivesRequest(t *testing.T) {
	source := SourceFunc(func(_ context.Context, req Request) ([]Memory, error) {
		if req.SessionID != "session-1" {
			t.Fatalf("SessionID = %q, want session-1", req.SessionID)
		}
		if len(req.Messages) != 1 || req.Messages[0].PlainText() != "migration" {
			t.Fatalf("Messages = %#v, want migration", req.Messages)
		}
		return []Memory{{Name: "seen"}}, nil
	})

	got, err := source.Memories(context.Background(), Request{
		SessionID: "session-1",
		Messages: []model.Message{{
			Role:    model.RoleUser,
			Content: []model.ContentBlock{{Type: model.ContentText, Text: "migration"}},
		}},
	})
	if err != nil {
		t.Fatalf("Memories returned error: %v", err)
	}
	if len(got) != 1 || got[0].Name != "seen" {
		t.Fatalf("memories = %#v, want seen", got)
	}
}

func TestSelectorRanksAndLimitsMemories(t *testing.T) {
	memories := []Memory{
		{Name: "always", Content: "unrelated", AlwaysOn: true},
		{Name: "database", Content: "migration rollback", Priority: 2},
		{Name: "database-priority", Content: "migration rollback", Priority: 1},
		{Name: "frontend", Content: "button style"},
	}

	got := (Selector{MaxMemories: 2}).Select(memories, "migration rollback")
	if len(got) != 2 {
		t.Fatalf("selected = %#v, want 2", got)
	}
	if got[0].Name != "always" {
		t.Fatalf("first memory = %q, want always-on preserved", got[0].Name)
	}
	if got[1].Name != "database-priority" {
		t.Fatalf("second memory = %q, want priority tie-break", got[1].Name)
	}
}
