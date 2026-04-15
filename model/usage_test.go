package model

import "testing"

func TestUsageAddAggregatesTokenCounts(t *testing.T) {
	total := (Usage{Provider: "openai", Model: "gpt", InputTokens: 1, Metadata: map[string]any{"source": "first"}}).Add(Usage{
		Provider:     "anthropic",
		Model:        "claude",
		OutputTokens: 2,
		TotalTokens:  3,
		Metadata: map[string]any{
			"source":        "second",
			"cache_created": 4,
		},
	})
	if total.Provider != "openai" || total.Model != "gpt" {
		t.Fatalf("identity = %q/%q, want first non-empty provider/model", total.Provider, total.Model)
	}
	if total.InputTokens != 1 || total.OutputTokens != 2 || total.TotalTokens != 3 {
		t.Fatalf("usage = %#v, want aggregated counts", total)
	}
	if total.Metadata["source"] != "first" {
		t.Fatalf("metadata = %#v, want first metadata", total.Metadata)
	}
	if total.Metadata["cache_created"] != 4 {
		t.Fatalf("metadata = %#v, want merged metadata", total.Metadata)
	}
}
