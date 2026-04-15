package hook

import (
	"context"
	"errors"
	"testing"

	"github.com/MemaxLabs/memax-go-agent-sdk/model"
)

func TestBeforeToolUseShortCircuitsOnDenial(t *testing.T) {
	runner := NewRunner()
	runner.AddBeforeToolUse(func(context.Context, BeforeToolUseInput) (BeforeToolUseResult, error) {
		return BeforeToolUseResult{DenyReason: "deny"}, nil
	})
	runner.AddBeforeToolUse(func(context.Context, BeforeToolUseInput) (BeforeToolUseResult, error) {
		t.Fatal("second hook should not run")
		return BeforeToolUseResult{}, nil
	})

	result, err := runner.BeforeToolUse(context.Background(), BeforeToolUseInput{
		Use:  model.ToolUse{ID: "1", Name: "write"},
		Spec: model.ToolSpec{Name: "write"},
	})
	if err != nil {
		t.Fatalf("BeforeToolUse returned error: %v", err)
	}
	if result.DenyReason != "deny" {
		t.Fatalf("DenyReason = %q, want deny", result.DenyReason)
	}
}

func TestAfterToolUseRunsAllHooks(t *testing.T) {
	runner := NewRunner()
	var calls int
	runner.AddAfterToolUse(func(context.Context, AfterToolUseInput) error {
		calls++
		return errors.New("first")
	})
	runner.AddAfterToolUse(func(context.Context, AfterToolUseInput) error {
		calls++
		return errors.New("second")
	})

	errs := runner.AfterToolUse(context.Background(), AfterToolUseInput{
		Use:    model.ToolUse{ID: "1", Name: "read"},
		Spec:   model.ToolSpec{Name: "read"},
		Result: model.ToolResult{ToolUseID: "1", Name: "read", Content: "ok"},
	})
	if calls != 2 {
		t.Fatalf("calls = %d, want 2", calls)
	}
	if len(errs) != 2 {
		t.Fatalf("len(errs) = %d, want 2", len(errs))
	}
}
