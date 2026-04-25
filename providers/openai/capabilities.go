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
// is deliberately conservative: aliases that require explicit long-context
// provider configuration report the standard context window, while hosts can
// still override with their own context policy.
func CapabilitiesForModel(modelName string) model.Capabilities {
	name := strings.ToLower(strings.TrimSpace(modelName))
	caps := model.Capabilities{
		Provider: "openai",
		Model:    modelName,
	}
	switch {
	case strings.HasPrefix(name, "gpt-4.1"):
		caps.ContextWindowTokens = contextWindowGPT41
		caps.MaxOutputTokens = 32_768
	case strings.HasPrefix(name, "gpt-5.4"),
		strings.HasPrefix(name, "gpt-5.3"),
		strings.HasPrefix(name, "gpt-5.2"),
		strings.HasPrefix(name, "gpt-5.1"),
		strings.HasPrefix(name, "gpt-5"):
		caps.ContextWindowTokens = contextWindowGPT5
	case strings.HasPrefix(name, "gpt-4o"):
		caps.ContextWindowTokens = contextWindowGPT4o
	}
	return caps
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
