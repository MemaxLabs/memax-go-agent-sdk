package personal

import (
	"fmt"

	memaxagent "github.com/MemaxLabs/memax-go-agent-sdk"
	"github.com/MemaxLabs/memax-go-agent-sdk/identity"
	"github.com/MemaxLabs/memax-go-agent-sdk/skill"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/approvaltools"
)

// Preset identifies a named personal-intelligence workflow profile.
//
// Presets keep the governance baseline stable through DefaultPolicies. The
// primary differences between profiles are identity, prompt guidance, turn and
// concurrency budgets, and how aggressively they lean on delegation.
type Preset string

const (
	// PresetPersonalAssistant is a careful personal-assistant profile focused on
	// durable context recall, explicit task tracking, and cautious memory writes.
	PresetPersonalAssistant Preset = "personal_assistant"
	// PresetResearchPartner is a broader research-and-synthesis profile that
	// keeps the same durable-memory guardrails while allowing longer,
	// higher-concurrency investigations.
	PresetResearchPartner Preset = "research_partner"
)

var allPresets = []Preset{
	PresetPersonalAssistant,
	PresetResearchPartner,
}

// Presets returns the supported personal presets in stable order.
func Presets() []Preset {
	return append([]Preset(nil), allPresets...)
}

// Config returns the default stack configuration for p. Callers should fill in
// host-owned backends such as Memory, Tasks, Approval, or Subagents before
// passing the resulting config to New.
func (p Preset) Config() (Config, error) {
	switch p {
	case PresetPersonalAssistant:
		return PersonalAssistant(), nil
	case PresetResearchPartner:
		return ResearchPartner(), nil
	default:
		return Config{}, fmt.Errorf("personal stack: unknown preset %q", p)
	}
}

// PersonalAssistant returns a careful personal-assistant profile. It favors
// durable context recall before mutation, explicit task tracking, and a
// conservative approval posture for memory writes.
func PersonalAssistant() Config {
	return Config{
		Base: memaxagent.Options{
			MaxTurns:           28,
			MaxToolConcurrency: 4,
			Identity: identity.Identity{
				Name:     "Memax Personal Assistant",
				Role:     "personal intelligence assistant",
				Mission:  "recall durable context, organize the user's active tasks, and preserve only future-useful personal knowledge",
				Tone:     "direct, calm, and context-aware",
				Autonomy: identity.AutonomyBalanced,
				Constraints: []string{
					"favor recalling existing context before creating new durable memory",
					"keep durable memory concise, specific, and clearly reusable",
					"treat personal data and long-lived notes as approval-sensitive state",
				},
			},
			AppendSystemPrompt: "Recall durable user and project context before writing new memory. Keep task state explicit, prefer concise summaries, and use approvals before mutating long-lived personal context.",
		},
		SkillDisclosure: skill.DisclosureProgressive,
		Approval: approvaltools.Config{
			ConcurrencySafe: true,
		},
		Policies: DefaultPolicies(),
	}
}

// ResearchPartner returns a longer-horizon personal research profile. It keeps
// the same durable-memory guardrails while encouraging scoped delegation and
// synthesis of multiple research threads.
func ResearchPartner() Config {
	return Config{
		Base: memaxagent.Options{
			MaxTurns:           36,
			MaxToolConcurrency: 6,
			Identity: identity.Identity{
				Name:     "Memax Research Partner",
				Role:     "personal research and synthesis agent",
				Mission:  "investigate the user's questions, coordinate focused delegated work, and preserve durable findings without polluting long-lived memory",
				Tone:     "direct, analytical, and concise",
				Autonomy: identity.AutonomyHigh,
				Constraints: []string{
					"separate tentative working notes from durable personal memory",
					"use delegation for independent research threads when the host exposes it",
					"keep final conclusions traceable to the gathered evidence",
				},
			},
			AppendSystemPrompt: "Use scoped delegation for independent research threads when it helps. Separate working notes from durable memory, keep conclusions traceable, and avoid saving long-lived memories without clear future value.",
		},
		SkillDisclosure: skill.DisclosureProgressive,
		Approval: approvaltools.Config{
			ConcurrencySafe: true,
		},
		Policies: DefaultPolicies(),
	}
}
