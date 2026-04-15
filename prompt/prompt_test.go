package prompt

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/MemaxLabs/memax-go-agent-sdk/identity"
	"github.com/MemaxLabs/memax-go-agent-sdk/memory"
	"github.com/MemaxLabs/memax-go-agent-sdk/model"
	"github.com/MemaxLabs/memax-go-agent-sdk/skill"
)

func TestDefaultBuilderIncludesIdentityToolsSkillsAndHostPrompt(t *testing.T) {
	result, err := (DefaultBuilder{}).Build(context.Background(), Request{
		Identity:     identity.Identity{Name: "reviewer", Mission: "find risks"},
		SystemPrompt: "host rules",
		Tools:        []model.ToolSpec{{Name: "read_file"}},
		Skills: []skill.Skill{{
			Name:        "code-review",
			Description: "review diffs",
			AlwaysOn:    true,
			Content:     "Find bugs first.",
		}},
	})
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}
	for _, want := range []string{"Memax Agent SDK", "reviewer", "find risks", "Available tool count: 1", "code-review", "Find bugs first.", "host rules"} {
		if !strings.Contains(result.SystemPrompt, want) {
			t.Fatalf("system prompt missing %q:\n%s", want, result.SystemPrompt)
		}
	}
	if result.Hash == "" || len(result.Parts) == 0 {
		t.Fatalf("result metadata = %#v", result)
	}
}

func TestDefaultBuilderIncludesSelectedMemories(t *testing.T) {
	result, err := (DefaultBuilder{MemorySelector: memory.Selector{MaxMemories: 1}}).Build(context.Background(), Request{
		Messages: []model.Message{{
			Role:    model.RoleUser,
			Content: []model.ContentBlock{{Type: model.ContentText, Text: "review billing flow"}},
		}},
		Memories: []memory.Memory{
			{Name: "billing", Scope: memory.ScopeProject, Description: "billing context", Content: "Invoices require audit logs."},
			{Name: "frontend", Scope: memory.ScopeProject, Content: "Use accessible controls."},
		},
	})
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}
	if !strings.Contains(result.SystemPrompt, "Durable host context") || !strings.Contains(result.SystemPrompt, "Invoices require audit logs.") {
		t.Fatalf("system prompt missing selected memory:\n%s", result.SystemPrompt)
	}
	if strings.Contains(result.SystemPrompt, "Use accessible controls.") {
		t.Fatalf("system prompt included irrelevant memory:\n%s", result.SystemPrompt)
	}
}

func TestDefaultBuilderSelectorQueryUsesRecentUserMessages(t *testing.T) {
	result, err := (DefaultBuilder{}).Build(context.Background(), Request{
		Messages: []model.Message{
			{Role: model.RoleUser, Content: []model.ContentBlock{{Type: model.ContentText, Text: "old frontend task"}}},
			{Role: model.RoleTool, ToolResult: &model.ToolResult{Name: "lookup", Content: "payments noise"}},
			{Role: model.RoleUser, Content: []model.ContentBlock{{Type: model.ContentText, Text: "review billing flow"}}},
		},
		Memories: []memory.Memory{
			{Name: "billing", Scope: memory.ScopeProject, Content: "Invoices require audit logs."},
			{Name: "payments", Scope: memory.ScopeProject, Content: "Payment tool result should not select this."},
		},
	})
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}
	if !strings.Contains(result.SystemPrompt, "Invoices require audit logs.") {
		t.Fatalf("system prompt missing billing memory:\n%s", result.SystemPrompt)
	}
	if strings.Contains(result.SystemPrompt, "Payment tool result should not select this.") {
		t.Fatalf("system prompt selected memory from tool-result noise:\n%s", result.SystemPrompt)
	}
}

func TestDefaultBuilderGolden(t *testing.T) {
	result, err := (DefaultBuilder{Profile: ProfileAnthropic}).Build(context.Background(), Request{
		Identity: identity.Identity{
			Name:        "Migration Reviewer",
			Role:        "database reviewer",
			Mission:     "find migration risks",
			Tone:        "concise",
			Autonomy:    identity.AutonomyHigh,
			Constraints: []string{"check rollback before approval"},
		},
		SystemPrompt:       "Host policy: read first.",
		AppendSystemPrompt: "Output risks only.",
		Tools: []model.ToolSpec{
			{Name: "read_file", ReadOnly: true, ConcurrencySafe: true},
			{Name: "write_file", Destructive: true},
		},
		Messages: []model.Message{{
			Role:    model.RoleUser,
			Content: []model.ContentBlock{{Type: model.ContentText, Text: "review SQL migration"}},
		}},
		Memories: []memory.Memory{{
			Name:        "migration-preferences",
			Scope:       memory.ScopeProject,
			Description: "Project review memory.",
			Content:     "Prefer reversible migrations with explicit rollback checks.",
			AlwaysOn:    true,
		}},
		Skills: []skill.Skill{{
			Name:        "database-review",
			Description: "Review database migrations.",
			AlwaysOn:    true,
			Content:     "Check lock behavior and rollback safety.",
		}},
	})
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}
	want, err := os.ReadFile("../testdata/golden/basic_prompt.txt")
	if err != nil {
		t.Fatalf("read golden prompt: %v", err)
	}
	if strings.TrimSpace(result.SystemPrompt) != strings.TrimSpace(string(want)) {
		t.Fatalf("prompt golden mismatch\n got:\n%s\nwant:\n%s", result.SystemPrompt, string(want))
	}
}

func TestDefaultBuilderHashIsStable(t *testing.T) {
	req := Request{Identity: identity.Identity{Name: "agent"}}
	first, err := (DefaultBuilder{}).Build(context.Background(), req)
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}
	second, err := (DefaultBuilder{}).Build(context.Background(), req)
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}
	if first.Hash != second.Hash {
		t.Fatalf("hashes differ: %q != %q", first.Hash, second.Hash)
	}
}

func TestDefaultBuilderProviderProfile(t *testing.T) {
	result, err := (DefaultBuilder{Profile: ProfileOpenAI}).Build(context.Background(), Request{
		Identity: identity.Identity{Name: "agent"},
	})
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}
	if !strings.Contains(result.SystemPrompt, "Provider profile") {
		t.Fatalf("system prompt = %q, want provider profile guidance", result.SystemPrompt)
	}
}
