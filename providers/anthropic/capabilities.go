package anthropic

import (
	"strings"

	"github.com/MemaxLabs/memax-go-agent-sdk/model"
)

const (
	contextWindowClaude200K = 200_000
)

// CapabilitiesForModel returns locally-known Anthropic model limits. Unknown
// Claude model names use the broadly supported 200k-token window. Anthropic's
// 1M-token context window currently requires a beta header that this adapter
// does not send yet, so the registry stays at the standard API limit.
func CapabilitiesForModel(modelName string) model.Capabilities {
	name := strings.ToLower(strings.TrimSpace(modelName))
	caps := model.Capabilities{
		Provider: "anthropic",
		Model:    modelName,
	}
	if strings.Contains(name, "claude") {
		caps.ContextWindowTokens = contextWindowClaude200K
		if strings.Contains(name, "opus") {
			caps.MaxOutputTokens = 128_000
		} else if strings.Contains(name, "sonnet-4-6") || strings.Contains(name, "sonnet-4.6") || strings.Contains(name, "haiku-4-5") || strings.Contains(name, "haiku-4.5") {
			caps.MaxOutputTokens = 64_000
		}
	}
	return caps
}

// Capabilities reports the configured client's locally-known model limits.
func (c *Client) Capabilities() model.Capabilities {
	if c == nil {
		return model.Capabilities{}
	}
	caps := CapabilitiesForModel(c.Model)
	if c.MaxTokens > 0 {
		caps.MaxOutputTokens = c.MaxTokens
	}
	return caps
}
