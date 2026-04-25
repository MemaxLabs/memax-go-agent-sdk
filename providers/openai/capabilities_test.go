package openai

import "testing"

func TestCapabilitiesForModel(t *testing.T) {
	tests := []struct {
		name       string
		wantWindow int
		wantOutput int
	}{
		{name: "gpt-4.1", wantWindow: contextWindowGPT41, wantOutput: 32_768},
		{name: "gpt-4.1-mini-2025-04-14", wantWindow: contextWindowGPT41, wantOutput: 32_768},
		{name: "openai/gpt-4.1-mini-2025-04-14", wantWindow: contextWindowGPT41, wantOutput: 32_768},
		{name: "gpt-5.4", wantWindow: contextWindowGPT5},
		{name: "gpt-5.3-codex", wantWindow: contextWindowGPT5},
		{name: "openai/gpt-5.5-pro", wantWindow: contextWindowGPT5},
		{name: "gpt-4o", wantWindow: contextWindowGPT4o},
		{name: "anthropic/claude-opus-4.7"},
		{name: "unknown"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CapabilitiesForModel(tt.name)
			if got.Provider != "openai" || got.Model != tt.name {
				t.Fatalf("CapabilitiesForModel provider/model = %q/%q, want openai/%q", got.Provider, got.Model, tt.name)
			}
			if got.ContextWindowTokens != tt.wantWindow {
				t.Fatalf("ContextWindowTokens = %d, want %d", got.ContextWindowTokens, tt.wantWindow)
			}
			if got.MaxOutputTokens != tt.wantOutput {
				t.Fatalf("MaxOutputTokens = %d, want %d", got.MaxOutputTokens, tt.wantOutput)
			}
		})
	}
}

func TestClientCapabilitiesUseConfiguredMaxOutput(t *testing.T) {
	client := &Client{Model: "gpt-4.1", MaxOutputTokens: 123}
	got := client.Capabilities()
	if got.ContextWindowTokens != contextWindowGPT41 {
		t.Fatalf("ContextWindowTokens = %d, want %d", got.ContextWindowTokens, contextWindowGPT41)
	}
	if got.MaxOutputTokens != 123 {
		t.Fatalf("MaxOutputTokens = %d, want configured 123", got.MaxOutputTokens)
	}
}

func TestNilClientCapabilities(t *testing.T) {
	var client *Client
	if got := client.Capabilities(); !got.IsZero() {
		t.Fatalf("nil Capabilities = %#v, want zero", got)
	}
}
