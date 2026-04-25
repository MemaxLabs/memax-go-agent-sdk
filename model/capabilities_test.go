package model

import (
	"context"
	"testing"
)

type capabilitiesClient struct {
	CapabilitiesValue Capabilities
}

func (c capabilitiesClient) Stream(context.Context, Request) (Stream, error) {
	return nil, ErrEndOfStream
}

func (c capabilitiesClient) Capabilities() Capabilities {
	return c.CapabilitiesValue
}

type plainClient struct{}

func (plainClient) Stream(context.Context, Request) (Stream, error) {
	return nil, ErrEndOfStream
}

func TestClientCapabilities(t *testing.T) {
	caps, ok := ClientCapabilities(capabilitiesClient{
		CapabilitiesValue: Capabilities{
			Provider:            "test",
			Model:               "test-model",
			ContextWindowTokens: 123,
		},
	})
	if !ok {
		t.Fatal("ClientCapabilities ok = false, want true")
	}
	if caps.Provider != "test" || caps.Model != "test-model" || caps.ContextWindowTokens != 123 {
		t.Fatalf("Capabilities = %#v, want provider/model/context values", caps)
	}
}

func TestClientCapabilitiesFallbacks(t *testing.T) {
	if caps, ok := ClientCapabilities(nil); ok || !caps.IsZero() {
		t.Fatalf("nil ClientCapabilities = (%#v, %t), want zero false", caps, ok)
	}
	if caps, ok := ClientCapabilities(plainClient{}); ok || !caps.IsZero() {
		t.Fatalf("plain ClientCapabilities = (%#v, %t), want zero false", caps, ok)
	}
	if caps, ok := ClientCapabilities(capabilitiesClient{}); ok || !caps.IsZero() {
		t.Fatalf("zero ClientCapabilities = (%#v, %t), want zero false", caps, ok)
	}
	if caps, ok := ClientCapabilities(capabilitiesClient{
		CapabilitiesValue: Capabilities{Provider: "test", Model: "unknown"},
	}); ok || !caps.IsZero() {
		t.Fatalf("identity-only ClientCapabilities = (%#v, %t), want zero false", caps, ok)
	}
}
