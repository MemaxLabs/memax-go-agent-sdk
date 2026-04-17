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

func TestBeforeFinalShortCircuitsOnDenial(t *testing.T) {
	runner := NewRunner()
	runner.AddBeforeFinal(func(context.Context, BeforeFinalInput) (BeforeFinalResult, error) {
		return BeforeFinalResult{DenyReason: "verify first"}, nil
	})
	runner.AddBeforeFinal(func(context.Context, BeforeFinalInput) (BeforeFinalResult, error) {
		t.Fatal("second hook should not run")
		return BeforeFinalResult{}, nil
	})

	result, err := runner.BeforeFinal(context.Background(), BeforeFinalInput{
		SessionID: "session-1",
		Turn:      2,
		Answer:    "done",
	})
	if err != nil {
		t.Fatalf("BeforeFinal returned error: %v", err)
	}
	if result.DenyReason != "verify first" {
		t.Fatalf("DenyReason = %q, want verify first", result.DenyReason)
	}
}

func TestUserPromptCanRewritePrompt(t *testing.T) {
	runner := NewRunner(WithUserPrompt(func(_ context.Context, input UserPromptInput) (UserPromptResult, error) {
		return UserPromptResult{Prompt: input.Prompt + " rewritten"}, nil
	}))

	result, err := runner.UserPrompt(context.Background(), UserPromptInput{
		SessionID: "session-1",
		Prompt:    "start",
	})
	if err != nil {
		t.Fatalf("UserPrompt returned error: %v", err)
	}
	if result.Prompt != "start rewritten" {
		t.Fatalf("Prompt = %q, want rewrite", result.Prompt)
	}
}

func TestUserPromptCanDenyPrompt(t *testing.T) {
	runner := NewRunner()
	runner.AddUserPrompt(func(context.Context, UserPromptInput) (UserPromptResult, error) {
		return UserPromptResult{DenyReason: "blocked"}, nil
	})

	result, err := runner.UserPrompt(context.Background(), UserPromptInput{
		SessionID: "session-1",
		Prompt:    "start",
	})
	if err != nil {
		t.Fatalf("UserPrompt returned error: %v", err)
	}
	if result.DenyReason != "blocked" {
		t.Fatalf("DenyReason = %q, want blocked", result.DenyReason)
	}
}

func TestLifecycleObserverHooksRunAll(t *testing.T) {
	runner := NewRunner()
	var calls []string
	runner.AddSessionStarted(func(context.Context, SessionStartedInput) error {
		calls = append(calls, "session_started")
		return nil
	})
	runner.AddSessionEnded(func(context.Context, SessionEndedInput) error {
		calls = append(calls, "session_ended")
		return nil
	})
	runner.AddStop(func(context.Context, StopInput) error {
		calls = append(calls, "stop")
		return nil
	})
	runner.AddContextApplied(func(context.Context, ContextAppliedInput) error {
		calls = append(calls, "context_applied")
		return nil
	})

	if errs := runner.SessionStarted(context.Background(), SessionStartedInput{SessionID: "session-1"}); len(errs) != 0 {
		t.Fatalf("SessionStarted errs = %#v", errs)
	}
	if errs := runner.ContextApplied(context.Background(), ContextAppliedInput{SessionID: "session-1"}); len(errs) != 0 {
		t.Fatalf("ContextApplied errs = %#v", errs)
	}
	if errs := runner.Stop(context.Background(), StopInput{SessionID: "session-1", Reason: StopReasonResult}); len(errs) != 0 {
		t.Fatalf("Stop errs = %#v", errs)
	}
	if errs := runner.SessionEnded(context.Background(), SessionEndedInput{SessionID: "session-1", Reason: StopReasonResult}); len(errs) != 0 {
		t.Fatalf("SessionEnded errs = %#v", errs)
	}

	want := []string{"session_started", "context_applied", "stop", "session_ended"}
	if len(calls) != len(want) {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}
	for i := range want {
		if calls[i] != want[i] {
			t.Fatalf("calls = %#v, want %#v", calls, want)
		}
	}
}

func TestCloneReturnsIndependentRunner(t *testing.T) {
	baseCalls := 0
	clonedCalls := 0
	original := NewRunner(
		WithBeforeToolUse(func(context.Context, BeforeToolUseInput) (BeforeToolUseResult, error) {
			baseCalls++
			return BeforeToolUseResult{}, nil
		}),
	)
	cloned := original.Clone()
	cloned.AddBeforeToolUse(func(context.Context, BeforeToolUseInput) (BeforeToolUseResult, error) {
		clonedCalls++
		return BeforeToolUseResult{}, nil
	})

	if _, err := original.BeforeToolUse(context.Background(), BeforeToolUseInput{
		Use:  model.ToolUse{ID: "1", Name: "read"},
		Spec: model.ToolSpec{Name: "read"},
	}); err != nil {
		t.Fatalf("original BeforeToolUse returned error: %v", err)
	}
	if _, err := cloned.BeforeToolUse(context.Background(), BeforeToolUseInput{
		Use:  model.ToolUse{ID: "1", Name: "read"},
		Spec: model.ToolSpec{Name: "read"},
	}); err != nil {
		t.Fatalf("cloned BeforeToolUse returned error: %v", err)
	}
	if baseCalls != 2 {
		t.Fatalf("baseCalls = %d, want 2", baseCalls)
	}
	if clonedCalls != 1 {
		t.Fatalf("clonedCalls = %d, want 1", clonedCalls)
	}
}
