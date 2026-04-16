package prompt

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/MemaxLabs/memax-go-agent-sdk/identity"
	"github.com/MemaxLabs/memax-go-agent-sdk/memory"
	"github.com/MemaxLabs/memax-go-agent-sdk/model"
	"github.com/MemaxLabs/memax-go-agent-sdk/planner"
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

func TestDefaultBuilderProgressiveSkillsExposeMetadataOnly(t *testing.T) {
	result, err := (DefaultBuilder{}).Build(context.Background(), Request{
		Messages: []model.Message{{
			Role:    model.RoleUser,
			Content: []model.ContentBlock{{Type: model.ContentText, Text: "review SQL migration"}},
		}},
		SkillDisclosure: skill.DisclosureProgressive,
		SkillResources:  true,
		Skills: []skill.Skill{{
			Name:        "database-review",
			Description: "Review database migrations.",
			WhenToUse:   "SQL changes are involved.",
			Tags:        []string{"database", "migration"},
			AlwaysOn:    true,
			Content:     "Check lock behavior and rollback safety.",
			Resources: []skill.ResourceRef{{
				Name:        "migration-checklist",
				Description: "Step-by-step migration checklist.",
				Path:        "resources/migration-checklist.md",
				MIMEType:    "text/markdown",
				Bytes:       128,
			}},
		}},
	})
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}
	for _, want := range []string{"Available skill metadata", "load_skill", "read_skill_resource", "database-review", "Review database migrations.", "database, migration", "migration-checklist", "resources/migration-checklist.md"} {
		if !strings.Contains(result.SystemPrompt, want) {
			t.Fatalf("system prompt missing %q:\n%s", want, result.SystemPrompt)
		}
	}
	for _, leaked := range []string{"Check lock behavior", "Step 1: take backup"} {
		if strings.Contains(result.SystemPrompt, leaked) {
			t.Fatalf("progressive skill prompt leaked %q:\n%s", leaked, result.SystemPrompt)
		}
	}
}

func TestDefaultBuilderProgressiveSkillsAreBoundedByDefault(t *testing.T) {
	skills := makeLargeSkillCatalog()
	result, err := (DefaultBuilder{}).Build(context.Background(), Request{
		Messages: []model.Message{{
			Role:    model.RoleUser,
			Content: []model.ContentBlock{{Type: model.ContentText, Text: "review the database migration checklist"}},
		}},
		SkillDisclosure: skill.DisclosureProgressive,
		SkillResources:  true,
		Skills:          skills,
	})
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}
	if !strings.Contains(result.SystemPrompt, "database-review") || !strings.Contains(result.SystemPrompt, "migration-checklist") {
		t.Fatalf("system prompt missing relevant skill metadata:\n%s", result.SystemPrompt)
	}
	if strings.Contains(result.SystemPrompt, "migration-helper-020") || strings.Contains(result.SystemPrompt, "frontend-020") {
		t.Fatalf("system prompt included irrelevant skill past default limit:\n%s", result.SystemPrompt)
	}
	for _, leaked := range []string{"Full database migration instructions", "Full frontend instructions", "resource body"} {
		if strings.Contains(result.SystemPrompt, leaked) {
			t.Fatalf("progressive prompt leaked %q:\n%s", leaked, result.SystemPrompt)
		}
	}
	if got := strings.Count(result.SystemPrompt, "\n\n- "); got != defaultProgressiveSkillLimit {
		t.Fatalf("selected skill count = %d, want %d:\n%s", got, defaultProgressiveSkillLimit, result.SystemPrompt)
	}
	if result.SkillDiscovery == nil || result.SkillDiscovery.Selected != defaultProgressiveSkillLimit || result.SkillDiscovery.Omitted != 17 || result.SkillDiscovery.PromptBytes == 0 {
		t.Fatalf("skill discovery = %#v, want selected count, omitted count, and prompt bytes", result.SkillDiscovery)
	}
	if !strings.Contains(result.SystemPrompt, "omitted because the skill discovery budget was reached") {
		t.Fatalf("system prompt missing item-bound omission note:\n%s", result.SystemPrompt)
	}
}

func TestDefaultBuilderProgressiveSkillDiscoveryRespectsByteBudget(t *testing.T) {
	const budget = 2300
	skills := make([]skill.Skill, 0, 12)
	for i := 0; i < 12; i++ {
		skills = append(skills, skill.Skill{
			Name:        fmt.Sprintf("migration-helper-%03d", i),
			Description: fmt.Sprintf("Database migration helper %03d. %s", i, strings.Repeat("detailed metadata ", 36)),
			WhenToUse:   "Use for database migration checklist and rollback review.",
			Tags:        []string{"database", "migration"},
			Content:     fmt.Sprintf("Full migration helper instructions %03d.", i),
		})
	}

	result, err := (DefaultBuilder{
		SkillSelector:          skill.Selector{MaxSkills: 12},
		SkillDiscoveryMaxBytes: budget,
	}).Build(context.Background(), Request{
		Messages: []model.Message{{
			Role:    model.RoleUser,
			Content: []model.ContentBlock{{Type: model.ContentText, Text: "review database migration checklist"}},
		}},
		SkillDisclosure: skill.DisclosureProgressive,
		Skills:          skills,
	})
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}
	if discovery := promptPartContent(result, "memax.skill_discovery"); len(discovery) > budget {
		t.Fatalf("skill discovery bytes = %d, want <= %d:\n%s", len(discovery), budget, discovery)
	}
	discovery := promptPartContent(result, "memax.skill_discovery")
	if got := strings.Count(discovery, "\n\n- "); got == 0 || got >= 8 {
		t.Fatalf("discovered skill count = %d, want bounded non-zero count:\n%s", got, discovery)
	}
	if !strings.Contains(discovery, "omitted because the skill discovery budget was reached") {
		t.Fatalf("discovery prompt missing omission note:\n%s", discovery)
	}
	if strings.Contains(discovery, "migration-helper-011") || strings.Contains(discovery, "Full migration helper instructions") {
		t.Fatalf("discovery prompt over-selected or leaked content:\n%s", discovery)
	}
}

func TestDefaultBuilderProgressiveSkillDiscoveryBudgetCanBeDisabled(t *testing.T) {
	skills := make([]skill.Skill, 0, 10)
	for i := 0; i < 10; i++ {
		skills = append(skills, skill.Skill{
			Name:        fmt.Sprintf("database-skill-%03d", i),
			Description: strings.Repeat("database migration metadata ", 64),
			WhenToUse:   "Database migration reviews.",
		})
	}
	result, err := (DefaultBuilder{
		SkillSelector:          skill.Selector{MaxSkills: 10},
		SkillDiscoveryMaxBytes: -1,
	}).Build(context.Background(), Request{
		Messages: []model.Message{{
			Role:    model.RoleUser,
			Content: []model.ContentBlock{{Type: model.ContentText, Text: "database migration"}},
		}},
		SkillDisclosure: skill.DisclosureProgressive,
		Skills:          skills,
	})
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}
	discovery := promptPartContent(result, "memax.skill_discovery")
	if got := strings.Count(discovery, "\n\n- "); got != 10 {
		t.Fatalf("discovered skill count = %d, want 10:\n%s", got, discovery)
	}
}

func TestDefaultBuilderDirectSkillInjectionKeepsUnboundedDefault(t *testing.T) {
	skills := makeLargeSkillCatalog()
	result, err := (DefaultBuilder{}).Build(context.Background(), Request{
		Skills: skills,
	})
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}
	if !strings.Contains(result.SystemPrompt, "frontend-020") || !strings.Contains(result.SystemPrompt, "Full frontend instructions 020") {
		t.Fatalf("direct skill injection should keep unbounded zero-value selector:\n%s", result.SystemPrompt)
	}
}

func promptPartContent(result Result, name string) string {
	for _, part := range result.Parts {
		if part.Name == name {
			return part.Content
		}
	}
	return ""
}

func TestDefaultBuilderIncludesPlan(t *testing.T) {
	result, err := (DefaultBuilder{}).Build(context.Background(), Request{
		Plan: planner.Plan{
			Goal:        "review migration safely",
			State:       planner.StateActive,
			Constraints: []string{"inspect before changing"},
			Steps: []planner.Step{{
				ID:                "step-1",
				Title:             "read migration",
				Status:            planner.StatusInProgress,
				VerificationHints: []string{"workspace_verify test migrations/001.sql"},
				ToolHints:         []string{"read_file"},
				Evidence:          []string{"migrations/001.sql"},
			}},
		},
	})
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}
	for _, want := range []string{"Host-provided plan", "review migration safely", "inspect before changing", "step-1", "read_file", "workspace_verify test migrations/001.sql", "migrations/001.sql"} {
		if !strings.Contains(result.SystemPrompt, want) {
			t.Fatalf("system prompt missing %q:\n%s", want, result.SystemPrompt)
		}
	}
}

func makeLargeSkillCatalog() []skill.Skill {
	skills := []skill.Skill{{
		Name:        "database-review",
		Description: "Review database migrations.",
		WhenToUse:   "Database migrations and rollback plans.",
		Tags:        []string{"database", "migration"},
		Content:     "Full database migration instructions.",
		Resources: []skill.ResourceRef{{
			Name:        "migration-checklist",
			Description: "Migration rollout checklist.",
			Path:        "resources/migration-checklist.md",
			MIMEType:    "text/markdown",
			Bytes:       128,
		}},
	}}
	for i := 0; i < 24; i++ {
		skills = append(skills, skill.Skill{
			Name:        fmt.Sprintf("migration-helper-%03d", i),
			Description: fmt.Sprintf("Database migration helper %03d.", i),
			WhenToUse:   "Database migration review subtasks.",
			Tags:        []string{"database", "migration"},
			Content:     fmt.Sprintf("Full migration helper instructions %03d with resource body.", i),
		})
	}
	for i := 0; i < 24; i++ {
		skills = append(skills, skill.Skill{
			Name:        fmt.Sprintf("frontend-%03d", i),
			Description: fmt.Sprintf("Frontend pattern guide %03d.", i),
			WhenToUse:   "React component styling tasks.",
			Tags:        []string{"frontend"},
			Content:     fmt.Sprintf("Full frontend instructions %03d with resource body.", i),
		})
	}
	return skills
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

func TestDefaultBuilderIncludesOutputContract(t *testing.T) {
	result, err := (DefaultBuilder{}).Build(context.Background(), Request{
		OutputSchema: map[string]any{
			"type":     "object",
			"required": []any{"answer"},
			"properties": map[string]any{
				"answer": map[string]any{"type": "string"},
			},
			"additionalProperties": false,
		},
	})
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}
	for _, want := range []string{"Final answer contract", "valid JSON", `"answer"`} {
		if !strings.Contains(result.SystemPrompt, want) {
			t.Fatalf("system prompt missing %q:\n%s", want, result.SystemPrompt)
		}
	}
}
