package anthropic

import (
	"strings"

	"github.com/MemaxLabs/memax-go-agent-sdk/model"
)

const (
	contextWindowClaude1M   = 1_000_000
	contextWindowClaude200K = 200_000
)

// CapabilitiesForModel returns locally-known Anthropic model limits. Unknown
// Claude model names use the broadly supported 200k-token window rather than
// assuming a newer long-context beta capability.
func CapabilitiesForModel(modelName string) model.Capabilities {
	name := strings.ToLower(strings.TrimSpace(modelName))
	caps := model.Capabilities{
		Provider: "anthropic",
		Model:    modelName,
	}
	switch {
	case strings.Contains(name, "opus-4-7"),
		strings.Contains(name, "opus-4.7"),
		strings.Contains(name, "sonnet-4-6"),
		strings.Contains(name, "sonnet-4.6"):
		caps.ContextWindowTokens = contextWindowClaude1M
		caps.MaxOutputTokens = 64_000
		if strings.Contains(name, "opus") {
			caps.MaxOutputTokens = 128_000
		}
	case strings.Contains(name, "claude"):
		caps.ContextWindowTokens = contextWindowClaude200K
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
