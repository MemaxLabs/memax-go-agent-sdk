package agenteval

import (
	"fmt"
	"strings"

	memaxagent "github.com/MemaxLabs/memax-go-agent-sdk"
)

// FinalEquals requires the final assistant result to equal want.
func FinalEquals(want string) Assertion {
	return Assertion{
		Name: "final equals",
		Check: func(result Result) error {
			if result.Final != want {
				return fmt.Errorf("final = %q, want %q", result.Final, want)
			}
			return nil
		},
	}
}

// FinalContains requires the final assistant result to contain substring.
func FinalContains(substring string) Assertion {
	return Assertion{
		Name: "final contains",
		Check: func(result Result) error {
			if !strings.Contains(result.Final, substring) {
				return fmt.Errorf("final = %q, want substring %q", result.Final, substring)
			}
			return nil
		},
	}
}

// ToolUsed requires at least one use of toolName.
func ToolUsed(toolName string) Assertion {
	return Assertion{
		Name: "tool used",
		Check: func(result Result) error {
			for _, use := range result.ToolUses() {
				if use.Name == toolName {
					return nil
				}
			}
			return fmt.Errorf("tool %q was not used", toolName)
		},
	}
}

// NoToolErrors requires every emitted tool result to be successful.
func NoToolErrors() Assertion {
	return Assertion{
		Name: "no tool errors",
		Check: func(result Result) error {
			for _, toolResult := range result.ToolResults() {
				if toolResult.IsError {
					return fmt.Errorf("tool %q returned error: %s", toolResult.Name, toolResult.Content)
				}
			}
			return nil
		},
	}
}

// EventKindEmitted requires the run to emit at least one event of kind.
func EventKindEmitted(kind memaxagent.EventKind) Assertion {
	return Assertion{
		Name: "event kind emitted",
		Check: func(result Result) error {
			for _, event := range result.Events {
				if event.Kind == kind {
					return nil
				}
			}
			return fmt.Errorf("event kind %q was not emitted", kind)
		},
	}
}
