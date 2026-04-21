package coding

import (
	"fmt"

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

// ModelProfiles returns the supported coding-model depth presets.
func ModelProfiles() []ModelProfile {
	return append([]ModelProfile(nil), modelProfiles...)
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
