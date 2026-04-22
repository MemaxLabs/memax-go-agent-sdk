package coding

import (
	"fmt"
	"strings"

	"github.com/MemaxLabs/memax-go-agent-sdk/providers/anthropic"
	"github.com/MemaxLabs/memax-go-agent-sdk/providers/openai"
)

// ModelProfile identifies a provider-neutral coding-model depth preset.
//
// Profiles are a coding-stack convenience for CLI and product surfaces that
// want stable names such as "fast" or "deep" while still letting provider
// adapters own the concrete request fields. The neutral runtime kernel does
// not know about these profiles.
type ModelProfile string

const (
	// DefaultModelProfile is the coding stack's default model depth when a CLI
	// or config file leaves the profile unset.
	DefaultModelProfile = ModelProfileBalanced

	// ModelProfileFast favors latency and cost for small edits and quick
	// inspection loops.
	ModelProfileFast ModelProfile = "fast"
	// ModelProfileBalanced is the default profile for normal coding-agent
	// sessions.
	ModelProfileBalanced ModelProfile = "balanced"
	// ModelProfileDeep favors harder planning, debugging, and long-horizon
	// repair loops.
	ModelProfileDeep ModelProfile = "deep"
)

var modelProfiles = []ModelProfile{
	ModelProfileFast,
	ModelProfileBalanced,
	ModelProfileDeep,
}

// ModelEffort identifies a provider-neutral reasoning/thinking effort override
// for coding-agent model calls.
//
// Profiles remain the recommended high-level control. Effort is the narrower
// escape hatch for hosts and CLIs that want to keep a profile's other provider
// controls while adjusting only reasoning depth.
type ModelEffort string

const (
	// ModelEffortAuto leaves the selected ModelProfile's provider mapping in
	// place.
	ModelEffortAuto ModelEffort = ""
	// ModelEffortLow favors latency and cost.
	ModelEffortLow ModelEffort = "low"
	// ModelEffortMedium uses normal reasoning depth.
	ModelEffortMedium ModelEffort = "medium"
	// ModelEffortHigh increases reasoning depth for harder tasks.
	ModelEffortHigh ModelEffort = "high"
	// ModelEffortXHigh uses the highest portable named reasoning depth.
	ModelEffortXHigh ModelEffort = "xhigh"
)

var modelEfforts = []ModelEffort{
	ModelEffortLow,
	ModelEffortMedium,
	ModelEffortHigh,
	ModelEffortXHigh,
}

// ModelProfiles returns the supported coding-model depth presets.
func ModelProfiles() []ModelProfile {
	return append([]ModelProfile(nil), modelProfiles...)
}

// ModelEfforts returns supported provider-neutral effort overrides.
func ModelEfforts() []ModelEffort {
	return append([]ModelEffort(nil), modelEfforts...)
}

// ParseModelProfile parses a CLI/config profile value. Empty input resolves to
// DefaultModelProfile so hosts can pass unset flag or environment values
// directly.
func ParseModelProfile(raw string) (ModelProfile, error) {
	normalized := strings.ToLower(strings.TrimSpace(raw))
	if normalized == "" {
		return DefaultModelProfile, nil
	}
	for _, profile := range modelProfiles {
		if normalized == string(profile) {
			return profile, nil
		}
	}
	return "", fmt.Errorf("coding stack: unknown model profile %q", raw)
}

// ParseModelEffort parses a CLI/config effort value. Empty input and "auto"
// both resolve to ModelEffortAuto so callers can distinguish "use profile
// default" from an explicit effort override.
func ParseModelEffort(raw string) (ModelEffort, error) {
	normalized := strings.ToLower(strings.TrimSpace(raw))
	if normalized == "" || normalized == "auto" {
		return ModelEffortAuto, nil
	}
	for _, effort := range modelEfforts {
		if normalized == string(effort) {
			return effort, nil
		}
	}
	return "", fmt.Errorf("coding stack: unknown model effort %q", raw)
}

// String returns the profile's stable CLI/config spelling.
func (p ModelProfile) String() string {
	return string(p)
}

// Description returns short user-facing help text for the profile. It is
// intended for CLIs and product surfaces that let users pick model depth.
func (p ModelProfile) Description() string {
	switch p {
	case ModelProfileFast:
		return "Prioritize latency and cost for small edits and quick inspection loops."
	case ModelProfileBalanced:
		return "Use the default coding depth for normal implementation and review work."
	case ModelProfileDeep:
		return "Use maximum practical reasoning depth for hard debugging and long-horizon repairs."
	default:
		return ""
	}
}

// String returns the effort's stable CLI/config spelling.
func (e ModelEffort) String() string {
	if e == ModelEffortAuto {
		return "auto"
	}
	return string(e)
}

// Description returns short user-facing help text for the effort override.
func (e ModelEffort) Description() string {
	switch e {
	case ModelEffortAuto:
		return "Use the selected model profile's default effort."
	case ModelEffortLow:
		return "Minimize reasoning cost and latency."
	case ModelEffortMedium:
		return "Use normal reasoning depth."
	case ModelEffortHigh:
		return "Increase reasoning depth for harder tasks."
	case ModelEffortXHigh:
		return "Use the highest portable named reasoning depth."
	default:
		return ""
	}
}

// OpenAIModelOptions maps a coding model profile onto OpenAI-specific model
// controls. Hosts can append additional OpenAI options after these to override
// individual provider fields.
func OpenAIModelOptions(profile ModelProfile) ([]openai.Option, error) {
	switch profile {
	case ModelProfileFast:
		return []openai.Option{
			openai.WithReasoningEffort(openai.ReasoningEffortLow),
			openai.WithTextVerbosity(openai.TextVerbosityLow),
			openai.WithReasoningArtifacts(),
		}, nil
	case ModelProfileBalanced:
		return []openai.Option{
			openai.WithReasoningEffort(openai.ReasoningEffortMedium),
			openai.WithTextVerbosity(openai.TextVerbosityMedium),
			openai.WithReasoningArtifacts(),
		}, nil
	case ModelProfileDeep:
		// Deep intentionally maps to xhigh: it is the "spend reasoning to
		// finish hard coding work" profile, not merely the next step above
		// balanced. Hosts can append openai.WithReasoningEffort(openai.ReasoningEffortHigh)
		// to soften it.
		return []openai.Option{
			openai.WithReasoningEffort(openai.ReasoningEffortXHigh),
			openai.WithTextVerbosity(openai.TextVerbosityHigh),
			openai.WithReasoningArtifacts(),
		}, nil
	default:
		return nil, fmt.Errorf("coding stack: unknown model profile %q", profile)
	}
}

// OpenAIModelEffortOptions maps a provider-neutral effort override onto
// OpenAI-specific model controls. ModelEffortAuto returns no options so the
// caller's selected ModelProfile remains authoritative. The override uses the
// provider's whole ReasoningConfig setter today because effort is the only
// reasoning field managed by coding profiles.
func OpenAIModelEffortOptions(effort ModelEffort) ([]openai.Option, error) {
	switch effort {
	case ModelEffortAuto:
		return nil, nil
	case ModelEffortLow:
		return []openai.Option{openai.WithReasoningEffort(openai.ReasoningEffortLow)}, nil
	case ModelEffortMedium:
		return []openai.Option{openai.WithReasoningEffort(openai.ReasoningEffortMedium)}, nil
	case ModelEffortHigh:
		return []openai.Option{openai.WithReasoningEffort(openai.ReasoningEffortHigh)}, nil
	case ModelEffortXHigh:
		return []openai.Option{openai.WithReasoningEffort(openai.ReasoningEffortXHigh)}, nil
	default:
		return nil, fmt.Errorf("coding stack: unknown model effort %q", effort)
	}
}

// AnthropicModelOptions maps a coding model profile onto Anthropic-specific
// effort and thinking controls. Hosts can append additional Anthropic options
// after these to override individual provider fields. Anthropic effort is
// paired with adaptive thinking for every profile because that is the
// provider's native effort-control path; hosts can append WithThinking to
// disable or replace it for latency-sensitive deployments.
func AnthropicModelOptions(profile ModelProfile) ([]anthropic.Option, error) {
	switch profile {
	case ModelProfileFast:
		return []anthropic.Option{
			anthropic.WithEffort(anthropic.EffortLow),
			anthropic.WithAdaptiveThinking(),
		}, nil
	case ModelProfileBalanced:
		return []anthropic.Option{
			anthropic.WithEffort(anthropic.EffortMedium),
			anthropic.WithAdaptiveThinking(),
		}, nil
	case ModelProfileDeep:
		return []anthropic.Option{
			anthropic.WithEffort(anthropic.EffortXHigh),
			anthropic.WithAdaptiveThinking(),
		}, nil
	default:
		return nil, fmt.Errorf("coding stack: unknown model profile %q", profile)
	}
}

// AnthropicModelEffortOptions maps a provider-neutral effort override onto
// Anthropic-specific model controls. ModelEffortAuto returns no options so the
// caller's selected ModelProfile remains authoritative.
func AnthropicModelEffortOptions(effort ModelEffort) ([]anthropic.Option, error) {
	switch effort {
	case ModelEffortAuto:
		return nil, nil
	case ModelEffortLow:
		return []anthropic.Option{anthropic.WithEffort(anthropic.EffortLow)}, nil
	case ModelEffortMedium:
		return []anthropic.Option{anthropic.WithEffort(anthropic.EffortMedium)}, nil
	case ModelEffortHigh:
		return []anthropic.Option{anthropic.WithEffort(anthropic.EffortHigh)}, nil
	case ModelEffortXHigh:
		return []anthropic.Option{anthropic.WithEffort(anthropic.EffortXHigh)}, nil
	default:
		return nil, fmt.Errorf("coding stack: unknown model effort %q", effort)
	}
}
