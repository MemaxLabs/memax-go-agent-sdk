package output

import (
	"context"
	"strings"
	"testing"
)

func TestContractValidateAcceptsMatchingJSON(t *testing.T) {
	contract := Contract{Schema: map[string]any{
		"type":     "object",
		"required": []any{"answer"},
		"properties": map[string]any{
			"answer": map[string]any{"type": "string"},
		},
		"additionalProperties": false,
	}}
	if err := contract.Validate(context.Background(), `{"answer":"ok"}`); err != nil {
		t.Fatalf("Validate returned error: %v", err)
	}
}

func TestContractValidateRejectsInvalidJSON(t *testing.T) {
	contract := Contract{Schema: map[string]any{"type": "object"}}
	err := contract.Validate(context.Background(), `not json`)
	if err == nil || !strings.Contains(err.Error(), "not valid JSON") {
		t.Fatalf("Validate error = %v, want JSON parse error", err)
	}
}

func TestContractValidateRejectsSchemaMismatch(t *testing.T) {
	contract := Contract{Schema: map[string]any{
		"type":     "object",
		"required": []any{"answer"},
		"properties": map[string]any{
			"answer": map[string]any{"type": "string"},
		},
	}}
	err := contract.Validate(context.Background(), `{"answer":42}`)
	if err == nil || !strings.Contains(err.Error(), "does not match schema") {
		t.Fatalf("Validate error = %v, want schema mismatch", err)
	}
}

func TestContractRetryLimit(t *testing.T) {
	if got := (Contract{}).RetryLimit(); got != 0 {
		t.Fatalf("disabled RetryLimit = %d, want 0", got)
	}
	if got := (Contract{Schema: map[string]any{"type": "object"}}).RetryLimit(); got != DefaultMaxRetries {
		t.Fatalf("default RetryLimit = %d, want %d", got, DefaultMaxRetries)
	}
	if got := (Contract{Schema: map[string]any{"type": "object"}, MaxRetries: -1}).RetryLimit(); got != 0 {
		t.Fatalf("negative RetryLimit = %d, want 0", got)
	}
	if got := (Contract{Schema: map[string]any{"type": "object"}, MaxRetries: 3}).RetryLimit(); got != 3 {
		t.Fatalf("explicit RetryLimit = %d, want 3", got)
	}
}
