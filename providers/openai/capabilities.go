package openai

import (
	"strings"

	"github.com/MemaxLabs/memax-go-agent-sdk/model"
)

const (
	contextWindowGPT41 = 1_047_576
	contextWindowGPT5  = 272_000
	contextWindowGPT4o = 128_000
)

// CapabilitiesForModel returns locally-known OpenAI model limits. The registry
// is deliberately conservative for model families whose long-context behavior
// depends on deployment or product configuration; hosts can still override with
// their own context policy.
func CapabilitiesForModel(modelName string) model.Capabilities {
	name := normalizeCapabilityModelName(modelName)
	caps := model.Capabilities{
		Provider: "openai",
		Model:    modelName,
	}
	switch {
	case strings.HasPrefix(name, "gpt-4.1"):
		caps.ContextWindowTokens = contextWindowGPT41
		caps.MaxOutputTokens = 32_768
	case strings.HasPrefix(name, "gpt-5"):
		caps.ContextWindowTokens = contextWindowGPT5
	case strings.HasPrefix(name, "gpt-4o"):
		caps.ContextWindowTokens = contextWindowGPT4o
	}
	return caps
}

func normalizeCapabilityModelName(modelName string) string {
	name := strings.ToLower(strings.TrimSpace(modelName))
	if family, modelID, ok := strings.Cut(name, "/"); ok && family == "openai" {
		return strings.TrimSpace(modelID)
	}
	return name
}

// Capabilities reports the configured client's locally-known model limits.
func (c *Client) Capabilities() model.Capabilities {
	if c == nil {
		return model.Capabilities{}
	}
	caps := CapabilitiesForModel(c.Model)
	if c.MaxOutputTokens > 0 {
		caps.MaxOutputTokens = c.MaxOutputTokens
	}
	return caps
}
