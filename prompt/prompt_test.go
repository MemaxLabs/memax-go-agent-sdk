package prompt

import (
	"context"
	"strings"
	"testing"

	"github.com/MemaxLabs/memax-go-agent-sdk/identity"
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
