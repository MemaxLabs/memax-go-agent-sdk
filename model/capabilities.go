package model

// Capabilities describes provider-neutral model limits and behavior hints known
// by a model client or adapter registry. Zero values mean unknown.
type Capabilities struct {
	// Provider is the provider or adapter name, for example "openai".
	Provider string
	// Model is the provider model identifier these capabilities describe.
	Model string
	// ContextWindowTokens is the maximum prompt/context token window.
	ContextWindowTokens int
	// MaxOutputTokens is the maximum output token budget, when known.
	MaxOutputTokens int
	// AutoCompactTokens is the provider or adapter recommended token threshold
	// for starting transcript compaction. Hosts may use their own policy when
	// this is zero.
	AutoCompactTokens int
	// Metadata carries adapter-specific facts without changing the stable SDK
	// contract. Hosts should not rely on a key unless an adapter documents it.
	Metadata map[string]any
}

// ClientWithCapabilities is implemented by model clients that can report model
// limits without issuing a request. It is intentionally optional so existing
// model.Client implementations remain source-compatible.
type ClientWithCapabilities interface {
	Capabilities() Capabilities
}

// ClientCapabilities returns client-reported model capabilities when the client
// implements ClientWithCapabilities.
func ClientCapabilities(client Client) (Capabilities, bool) {
	if client == nil {
		return Capabilities{}, false
	}
	withCapabilities, ok := client.(ClientWithCapabilities)
	if !ok {
		return Capabilities{}, false
	}
	caps := withCapabilities.Capabilities()
	return caps, !caps.IsZero()
}

// IsZero reports whether no useful capability limits are set. Provider and
// model labels alone are identity, not limits; callers that need a specific
// limit should still check that field directly.
func (c Capabilities) IsZero() bool {
	return c.ContextWindowTokens == 0 &&
		c.MaxOutputTokens == 0 &&
		c.AutoCompactTokens == 0 &&
		len(c.Metadata) == 0
}
