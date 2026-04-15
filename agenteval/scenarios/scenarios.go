// Package scenarios provides reusable deterministic eval cases for core agent
// behaviors.
package scenarios

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	memaxagent "github.com/MemaxLabs/memax-go-agent-sdk"
	"github.com/MemaxLabs/memax-go-agent-sdk/agenteval"
	"github.com/MemaxLabs/memax-go-agent-sdk/memory"
	"github.com/MemaxLabs/memax-go-agent-sdk/model"
	"github.com/MemaxLabs/memax-go-agent-sdk/output"
	"github.com/MemaxLabs/memax-go-agent-sdk/tool"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/memorytools"
)

// All returns the default deterministic autonomy scenario suite.
func All() []agenteval.Case {
	return []agenteval.Case{
		ToolRecovery(),
		StructuredOutputRepair(),
		MemorySearchAndSave(),
	}
}

// ToolRecovery returns a scenario where the model emits invalid tool input,
// receives the validation error as a tool result, and recovers.
func ToolRecovery() agenteval.Case {
	modelClient := agenteval.NewScriptedModel(
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:    "tool-1",
				Name:  "read_file",
				Input: json.RawMessage(`{"path":42}`),
			},
		}},
		[]model.StreamEvent{{Kind: model.StreamText, Text: "recovered after tool validation"}},
	)
	registry := tool.NewRegistry(tool.Definition{
		ToolSpec: model.ToolSpec{
			Name:        "read_file",
			Description: "Read a file by path.",
			ReadOnly:    true,
			InputSchema: map[string]any{
				"type":                 "object",
				"required":             []any{"path"},
				"additionalProperties": false,
				"properties": map[string]any{
					"path": map[string]any{"type": "string"},
				},
			},
		},
		Handler: func(context.Context, tool.Call) (model.ToolResult, error) {
			return model.ToolResult{Content: "handler should not run"}, nil
		},
	})

	return agenteval.Case{
		Name:   "tool_recovery",
		Prompt: "Read README.md and recover if tool input is invalid.",
		Options: memaxagent.Options{
			Model: modelClient,
			Tools: registry,
		},
		Assertions: []agenteval.Assertion{
			agenteval.ToolUsed("read_file"),
			agenteval.FinalEquals("recovered after tool validation"),
			{
				Name: "tool validation error surfaced",
				Check: func(result agenteval.Result) error {
					for _, toolResult := range result.ToolResults() {
						if toolResult.IsError && strings.Contains(toolResult.Content, "jsonschema") {
							return nil
						}
					}
					return fmt.Errorf("missing model-visible tool validation error")
				},
			},
			{
				Name: "model retried after tool error",
				Check: func(agenteval.Result) error {
					if got := len(modelClient.Requests()); got != 2 {
						return fmt.Errorf("model requests = %d, want 2", got)
					}
					return nil
				},
			},
		},
	}
}

// StructuredOutputRepair returns a scenario where invalid final JSON is
// persisted, the SDK appends a repair prompt, and the model returns valid JSON.
func StructuredOutputRepair() agenteval.Case {
	modelClient := agenteval.NewScriptedModel(
		[]model.StreamEvent{{Kind: model.StreamText, Text: "not json"}},
		[]model.StreamEvent{{Kind: model.StreamText, Text: `{"answer":"fixed"}`}},
	)
	return agenteval.Case{
		Name:   "structured_output_repair",
		Prompt: "Return a structured answer.",
		Options: memaxagent.Options{
			Model:  modelClient,
			Output: answerContract(),
		},
		Assertions: []agenteval.Assertion{
			agenteval.FinalEquals(`{"answer":"fixed"}`),
			{
				Name: "repair prompt visible on retry",
				Check: func(agenteval.Result) error {
					requests := modelClient.Requests()
					if len(requests) != 2 {
						return fmt.Errorf("model requests = %d, want 2", len(requests))
					}
					messages := requests[1].Messages
					if len(messages) < 3 {
						return fmt.Errorf("retry messages = %#v, want invalid answer and repair prompt", messages)
					}
					previous := messages[len(messages)-2]
					if previous.Role != model.RoleAssistant || previous.PlainText() != "not json" {
						return fmt.Errorf("previous retry message = %#v, want invalid assistant answer", previous)
					}
					last := messages[len(messages)-1]
					if last.Role != model.RoleUser || !strings.Contains(last.PlainText(), "structured output contract") {
						return fmt.Errorf("last retry message = %#v, want structured output repair prompt", last)
					}
					return nil
				},
			},
		},
	}
}

// MemorySearchAndSave returns a scenario where the model searches durable
// memories, saves a new memory, and completes with the saved memory in the
// backing store.
func MemorySearchAndSave() agenteval.Case {
	store := memory.NewMemoryStore([]memory.Memory{{
		Name:    "billing-rule",
		Scope:   memory.ScopeProject,
		Content: "Invoices require audit logs.",
		Tags:    []string{"billing"},
	}})
	searchTool, _ := memorytools.NewSearchTool(memorytools.Config{Source: store})
	saveTool, _ := memorytools.NewSaveTool(memorytools.Config{Writer: store})
	modelClient := agenteval.NewScriptedModel(
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:    "tool-1",
				Name:  memorytools.SearchToolName,
				Input: json.RawMessage(`{"query":"invoice audit","limit":1}`),
			},
		}},
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:   "tool-2",
				Name: memorytools.SaveToolName,
				Input: json.RawMessage(`{
					"name":"billing-rollback",
					"scope":"project",
					"description":"Billing rollback requirement",
					"content":"Billing changes require rollback notes.",
					"tags":["billing","rollback"]
				}`),
			},
		}},
		[]model.StreamEvent{{Kind: model.StreamText, Text: "memory saved"}},
	)

	return agenteval.Case{
		Name:   "memory_search_and_save",
		Prompt: "Search billing memory, then save the rollback requirement.",
		Options: memaxagent.Options{
			Model:        modelClient,
			Tools:        tool.NewRegistry(searchTool, saveTool),
			MemorySource: store,
		},
		Assertions: []agenteval.Assertion{
			agenteval.ToolUsed(memorytools.SearchToolName),
			agenteval.ToolUsed(memorytools.SaveToolName),
			agenteval.FinalEquals("memory saved"),
			{
				Name: "memory persisted",
				Check: func(agenteval.Result) error {
					items, err := store.Memories(context.Background(), memory.Request{})
					if err != nil {
						return err
					}
					for _, item := range items {
						if item.Name == "billing-rollback" && strings.Contains(item.Content, "rollback notes") {
							return nil
						}
					}
					return fmt.Errorf("saved memory not found: %#v", items)
				},
			},
			{
				Name: "memory tools succeeded",
				Check: func(result agenteval.Result) error {
					for _, toolResult := range result.ToolResults() {
						if toolResult.IsError {
							return fmt.Errorf("%s failed: %s", toolResult.Name, toolResult.Content)
						}
					}
					return nil
				},
			},
		},
	}
}

func answerContract() output.Contract {
	return output.Contract{Schema: map[string]any{
		"type":                 "object",
		"required":             []any{"answer"},
		"additionalProperties": false,
		"properties": map[string]any{
			"answer": map[string]any{"type": "string"},
		},
	}}
}
