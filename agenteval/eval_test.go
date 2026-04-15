package agenteval

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	memaxagent "github.com/MemaxLabs/memax-go-agent-sdk"
	"github.com/MemaxLabs/memax-go-agent-sdk/model"
	"github.com/MemaxLabs/memax-go-agent-sdk/tool"
)

func TestRunnerRunsDeterministicCase(t *testing.T) {
	report := Runner{}.Run(context.Background(), Case{
		Name:   "final answer",
		Prompt: "answer",
		Options: memaxagent.Options{
			Model: NewScriptedModel([]model.StreamEvent{
				{Kind: model.StreamText, Text: "done"},
				{Kind: model.StreamUsage, Usage: &model.Usage{InputTokens: 3, OutputTokens: 4, TotalTokens: 7}},
			}),
		},
		Assertions: []Assertion{
			FinalEquals("done"),
			EventKindEmitted(memaxagent.EventUsage),
		},
	})
	if err := report.Error(); err != nil {
		t.Fatalf("report error = %v", err)
	}
	if !report.Passed() || len(report.Results) != 1 {
		t.Fatalf("report = %#v, want one passing result", report)
	}
	if got := report.Results[0].Usage.TotalTokens; got != 7 {
		t.Fatalf("usage total = %d, want 7", got)
	}
}

func TestRunnerCapturesToolRecoveryBehavior(t *testing.T) {
	registry := tool.NewRegistry(tool.Definition{
		ToolSpec: model.ToolSpec{
			Name: "read",
			InputSchema: map[string]any{
				"type":     "object",
				"required": []any{"path"},
				"properties": map[string]any{
					"path": map[string]any{"type": "string"},
				},
				"additionalProperties": false,
			},
			ReadOnly: true,
		},
		Handler: func(context.Context, tool.Call) (model.ToolResult, error) {
			t.Fatal("handler should not run for invalid tool input")
			return model.ToolResult{}, nil
		},
	})
	report := Runner{}.Run(context.Background(), Case{
		Name:   "tool input recovery",
		Prompt: "read the file",
		Options: memaxagent.Options{
			Model: NewScriptedModel(
				[]model.StreamEvent{{
					Kind: model.StreamToolUse,
					ToolUse: model.ToolUse{
						ID:    "tool-1",
						Name:  "read",
						Input: json.RawMessage(`{"path":42}`),
					},
				}},
				[]model.StreamEvent{{Kind: model.StreamText, Text: "recovered"}},
			),
			Tools: registry,
		},
		Assertions: []Assertion{
			ToolUsed("read"),
			FinalEquals("recovered"),
			{
				Name: "tool error returned to model",
				Check: func(result Result) error {
					for _, toolResult := range result.ToolResults() {
						if toolResult.IsError && strings.Contains(toolResult.Content, "jsonschema") {
							return nil
						}
					}
					return errors.New("missing tool validation error")
				},
			},
		},
	})
	if err := report.Error(); err != nil {
		t.Fatalf("report error = %v", err)
	}
}

func TestReportErrorListsFailedCases(t *testing.T) {
	report := Runner{}.Run(context.Background(), Case{
		Name:   "mismatch",
		Prompt: "answer",
		Options: memaxagent.Options{
			Model: NewScriptedModel([]model.StreamEvent{{Kind: model.StreamText, Text: "actual"}}),
		},
		Assertions: []Assertion{
			FinalEquals("expected"),
			FinalContains("missing-substring"),
		},
	})
	if report.Passed() {
		t.Fatal("report passed, want failure")
	}
	err := report.Error()
	if err == nil ||
		!strings.Contains(err.Error(), "mismatch") ||
		!strings.Contains(err.Error(), "expected") ||
		!strings.Contains(err.Error(), "missing-substring") {
		t.Fatalf("report error = %v, want all assertion details", err)
	}
}

func TestRunnerUsesResultUsageWithoutDoubleCounting(t *testing.T) {
	report := Runner{}.Run(context.Background(), Case{
		Name:   "usage",
		Prompt: "answer",
		Options: memaxagent.Options{
			Model: NewScriptedModel([]model.StreamEvent{
				{Kind: model.StreamUsage, Usage: &model.Usage{InputTokens: 3, OutputTokens: 4, TotalTokens: 7}},
				{Kind: model.StreamText, Text: "done"},
			}),
		},
	})
	if err := report.Error(); err != nil {
		t.Fatalf("report error = %v", err)
	}
	if got := report.Results[0].Usage.TotalTokens; got != 7 {
		t.Fatalf("usage total = %d, want final aggregate without double count", got)
	}
}

func TestScriptedModelReturnsDefensiveRequestCopies(t *testing.T) {
	client := NewScriptedModel([]model.StreamEvent{{Kind: model.StreamText, Text: "done"}})
	_, err := client.Stream(context.Background(), model.Request{
		Messages: []model.Message{{
			Role:    model.RoleUser,
			Content: []model.ContentBlock{{Type: model.ContentText, Text: "hello"}},
		}},
		Tools: []model.ToolSpec{{
			Name:        "read",
			InputSchema: map[string]any{"properties": map[string]any{"path": map[string]any{"type": "string"}}},
		}},
	})
	if err != nil {
		t.Fatalf("Stream error = %v", err)
	}
	requests := client.Requests()
	requests[0].Messages[0].Content[0].Text = "mutated"
	requests[0].Tools[0].InputSchema["properties"].(map[string]any)["path"] = "mutated"

	requests = client.Requests()
	if got := requests[0].Messages[0].Content[0].Text; got != "hello" {
		t.Fatalf("captured message text = %q, want defensive copy", got)
	}
	if got := requests[0].Tools[0].InputSchema["properties"].(map[string]any)["path"]; got == "mutated" {
		t.Fatalf("captured schema was mutated through Requests")
	}
}
