package memaxagent

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/MemaxLabs/memax-go-agent-sdk/budget"
	"github.com/MemaxLabs/memax-go-agent-sdk/identity"
	"github.com/MemaxLabs/memax-go-agent-sdk/memory"
	"github.com/MemaxLabs/memax-go-agent-sdk/model"
	"github.com/MemaxLabs/memax-go-agent-sdk/output"
	"github.com/MemaxLabs/memax-go-agent-sdk/planner"
	"github.com/MemaxLabs/memax-go-agent-sdk/session"
	"github.com/MemaxLabs/memax-go-agent-sdk/skill"
	"github.com/MemaxLabs/memax-go-agent-sdk/tool"
)

func TestOptionsMergeAppliesOverridesAndCopiesSlices(t *testing.T) {
	base := Options{
		Model:           &staticClient{id: "base"},
		Tools:           tool.NewRegistry(),
		Sessions:        session.NewMemoryStore(),
		Output:          output.Contract{Schema: map[string]any{"type": "string"}},
		Budget:          budget.Policy{MaxTurns: 1},
		Identity:        identity.Identity{Name: "base"},
		Planner:         planner.Static(planner.Plan{Goal: "base"}),
		MemoryDistiller: memory.StaticDistiller{{Memory: memory.Memory{Name: "base-distilled", Content: "base"}}},
		MemoryCandidateHandler: memory.CandidateHandlerFunc(func(context.Context, memory.CandidateRequest) error {
			return nil
		}),
		SkillDisclosure:    skill.DisclosureInjectSelected,
		Memories:           []memory.Memory{{Name: "base"}},
		Skills:             []skill.Skill{{Name: "base"}},
		SystemPrompt:       "base system",
		SessionID:          "base-session",
		MaxTurns:           3,
		MaxToolConcurrency: 2,
		MaxRunDuration:     time.Second,
	}
	overrideMemories := []memory.Memory{{Name: "override"}}
	overrideSkills := []skill.Skill{{Name: "override"}}
	override := Options{
		Model:           &staticClient{id: "override"},
		Output:          output.Contract{MaxRetries: -1},
		Budget:          budget.Policy{MaxModelCalls: 1},
		Identity:        identity.Identity{Name: "override"},
		Planner:         planner.Static(planner.Plan{Goal: "override"}),
		MemoryDistiller: memory.StaticDistiller{{Memory: memory.Memory{Name: "override-distilled", Content: "override"}}},
		MemoryCandidateHandler: memory.CandidateHandlerFunc(func(context.Context, memory.CandidateRequest) error {
			return errors.New("override handler")
		}),
		SkillDisclosure:    skill.DisclosureProgressive,
		Memories:           overrideMemories,
		Skills:             overrideSkills,
		SystemPrompt:       "override system",
		SessionID:          "override-session",
		MaxTurns:           9,
		MaxToolConcurrency: 4,
		MaxRunDuration:     2 * time.Second,
	}

	got := base.Merge(override)
	overrideMemories[0].Name = "mutated"
	overrideSkills[0].Name = "mutated"

	if got.Model.(*staticClient).id != "override" {
		t.Fatalf("Model override not applied")
	}
	if got.Output.MaxRetries != -1 {
		t.Fatalf("Output = %#v, want override", got.Output)
	}
	if decision := got.Budget.Check(context.Background(), budget.Snapshot{ModelCalls: 2}); decision.Allow {
		t.Fatalf("Budget override not applied: %#v", decision)
	}
	if got.Identity.Name != "override" {
		t.Fatalf("Identity = %#v, want override", got.Identity)
	}
	plan, err := got.Planner.Prepare(context.Background(), planner.Request{})
	if err != nil {
		t.Fatalf("Planner returned error: %v", err)
	}
	if plan.Goal != "override" {
		t.Fatalf("Plan = %#v, want override", plan)
	}
	candidates, err := got.MemoryDistiller.Distill(context.Background(), memory.DistillRequest{})
	if err != nil {
		t.Fatalf("MemoryDistiller returned error: %v", err)
	}
	if len(candidates) != 1 || candidates[0].Memory.Name != "override-distilled" {
		t.Fatalf("MemoryDistiller = %#v, want override", candidates)
	}
	if err := got.MemoryCandidateHandler.HandleCandidates(context.Background(), memory.CandidateRequest{}); err == nil || err.Error() != "override handler" {
		t.Fatalf("MemoryCandidateHandler error = %v, want override handler", err)
	}
	if got.Memories[0].Name != "override" {
		t.Fatalf("Memories = %#v, want copied override", got.Memories)
	}
	if got.Skills[0].Name != "override" {
		t.Fatalf("Skills = %#v, want copied override", got.Skills)
	}
	if got.SkillDisclosure != skill.DisclosureProgressive {
		t.Fatalf("SkillDisclosure = %q, want progressive", got.SkillDisclosure)
	}
	if got.SystemPrompt != "override system" || got.SessionID != "override-session" {
		t.Fatalf("string overrides not applied: %#v", got)
	}
	if got.MaxTurns != 9 || got.MaxToolConcurrency != 4 || got.MaxRunDuration != 2*time.Second {
		t.Fatalf("limit overrides not applied: %#v", got)
	}
	if got.Tools != base.Tools || got.Sessions != base.Sessions {
		t.Fatalf("base references should be preserved when override is nil")
	}
}

func TestOptionsMergeCanOverrideSlicesWithEmpty(t *testing.T) {
	got := Options{
		Memories: []memory.Memory{{Name: "base"}},
		Skills:   []skill.Skill{{Name: "base"}},
	}.Merge(Options{
		Memories: []memory.Memory{},
		Skills:   []skill.Skill{},
	})
	if got.Memories != nil && len(got.Memories) != 0 {
		t.Fatalf("Memories = %#v, want empty override", got.Memories)
	}
	if got.Skills != nil && len(got.Skills) != 0 {
		t.Fatalf("Skills = %#v, want empty override", got.Skills)
	}
}

type staticClient struct {
	id string
}

func (c *staticClient) Stream(context.Context, model.Request) (model.Stream, error) {
	return nil, nil
}
