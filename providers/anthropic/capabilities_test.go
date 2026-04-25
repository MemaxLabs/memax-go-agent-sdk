package anthropic

import "testing"

func TestCapabilitiesForModel(t *testing.T) {
	tests := []struct {
		name       string
		wantWindow int
		wantOutput int
	}{
		{name: "claude-opus-4-7", wantWindow: contextWindowClaude200K, wantOutput: 128_000},
		{name: "claude-3-opus-20240229", wantWindow: contextWindowClaude200K},
		{name: "claude-sonnet-4-6", wantWindow: contextWindowClaude200K, wantOutput: 64_000},
		{name: "claude-haiku-4-5-20251001", wantWindow: contextWindowClaude200K, wantOutput: 64_000},
		{name: "claude-3-5-sonnet-20241022", wantWindow: contextWindowClaude200K},
		{name: "unknown"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CapabilitiesForModel(tt.name)
			if got.Provider != "anthropic" || got.Model != tt.name {
				t.Fatalf("CapabilitiesForModel provider/model = %q/%q, want anthropic/%q", got.Provider, got.Model, tt.name)
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

func TestClientCapabilitiesUseConfiguredMaxTokens(t *testing.T) {
	client := &Client{Model: "claude-opus-4-7", MaxTokens: 123}
	got := client.Capabilities()
	if got.ContextWindowTokens != contextWindowClaude200K {
		t.Fatalf("ContextWindowTokens = %d, want %d", got.ContextWindowTokens, contextWindowClaude200K)
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
