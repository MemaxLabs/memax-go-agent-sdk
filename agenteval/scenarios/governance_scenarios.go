package scenarios

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	memaxagent "github.com/MemaxLabs/memax-go-agent-sdk"
	"github.com/MemaxLabs/memax-go-agent-sdk/agenteval"
	"github.com/MemaxLabs/memax-go-agent-sdk/budget"
	"github.com/MemaxLabs/memax-go-agent-sdk/hook"
	"github.com/MemaxLabs/memax-go-agent-sdk/model"
	"github.com/MemaxLabs/memax-go-agent-sdk/permission"
	"github.com/MemaxLabs/memax-go-agent-sdk/resultstore"
	"github.com/MemaxLabs/memax-go-agent-sdk/tool"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/toolsearch"
)

// PermissionDenialRecovery returns a single-use scenario where a denied
// destructive tool call is surfaced as a model-visible tool error and the model
// recovers with a read-only alternative.
func PermissionDenialRecovery() agenteval.Case {
	modelClient := agenteval.NewScriptedModel(
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:    "tool-1",
				Name:  "write_file",
				Input: json.RawMessage(`{"path":"README.md","content":"unsafe"}`),
			},
		}},
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:    "tool-2",
				Name:  "read_file",
				Input: json.RawMessage(`{"path":"README.md"}`),
			},
		}},
		[]model.StreamEvent{{Kind: model.StreamText, Text: "recovered with read-only path"}},
	)

	return agenteval.Case{
		Name:   "permission_denial_recovery",
		Prompt: "Update README.md, but recover safely if writing is denied.",
		Options: memaxagent.Options{
			Model:       modelClient,
			Tools:       tool.NewRegistry(readFileTool(), writeFileTool()),
			Permissions: permission.ReadOnly{},
		},
		Assertions: []agenteval.Assertion{
			agenteval.ToolUsed("write_file"),
			agenteval.ToolUsed("read_file"),
			agenteval.FinalEquals("recovered with read-only path"),
			toolResultContains("write_file", true, "not read-only"),
			toolResultContains("read_file", false, "read README.md"),
			requestCountEquals(modelClient, 3),
		},
	}
}

// HookDenialRecovery returns a single-use scenario where a before-tool hook
// denies execution and the model recovers with an allowed tool call.
func HookDenialRecovery() agenteval.Case {
	modelClient := agenteval.NewScriptedModel(
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:    "tool-1",
				Name:  "read_secret",
				Input: json.RawMessage(`{"name":"prod-token"}`),
			},
		}},
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:    "tool-2",
				Name:  "read_file",
				Input: json.RawMessage(`{"path":"README.md"}`),
			},
		}},
		[]model.StreamEvent{{Kind: model.StreamText, Text: "recovered after hook denial"}},
	)
	hooks := hook.NewRunner(hook.WithBeforeToolUse(func(_ context.Context, input hook.BeforeToolUseInput) (hook.BeforeToolUseResult, error) {
		if input.Use.Name == "read_secret" {
			return hook.BeforeToolUseResult{DenyReason: "secret access denied by host policy"}, nil
		}
		return hook.BeforeToolUseResult{}, nil
	}))

	return agenteval.Case{
		Name:   "hook_denial_recovery",
		Prompt: "Read the secret, but recover if the host denies it.",
		Options: memaxagent.Options{
			Model:       modelClient,
			Tools:       tool.NewRegistry(readSecretTool(), readFileTool()),
			Hooks:       hooks,
			Permissions: permission.AllowAll{},
		},
		Assertions: []agenteval.Assertion{
			agenteval.ToolUsed("read_secret"),
			agenteval.ToolUsed("read_file"),
			agenteval.FinalEquals("recovered after hook denial"),
			toolResultContains("read_secret", true, "secret access denied"),
			toolResultContains("read_file", false, "read README.md"),
			requestCountEquals(modelClient, 3),
		},
	}
}

// LargeResultStorageRecovery returns a single-use scenario where an oversized
// tool result is stored out of band, the model receives a bounded preview plus
// handle metadata, and the run completes.
func LargeResultStorageRecovery() agenteval.Case {
	store := resultstore.NewMemoryStore()
	modelClient := agenteval.NewScriptedModel(
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:    "tool-1",
				Name:  "read_large_report",
				Input: json.RawMessage(`{"name":"audit"}`),
			},
		}},
		[]model.StreamEvent{{Kind: model.StreamText, Text: "used stored large result handle"}},
	)

	return agenteval.Case{
		Name:   "large_result_storage_recovery",
		Prompt: "Read the large audit report and summarize the available preview.",
		Options: memaxagent.Options{
			Model:       modelClient,
			Tools:       tool.NewRegistry(largeReportTool()),
			ResultStore: store,
		},
		Assertions: []agenteval.Assertion{
			agenteval.ToolUsed("read_large_report"),
			agenteval.FinalEquals("used stored large result handle"),
			agenteval.NoToolErrors(),
			{
				Name: "tool result preview has storage metadata",
				Check: func(result agenteval.Result) error {
					for _, toolResult := range result.ToolResults() {
						if toolResult.Name != "read_large_report" {
							continue
						}
						if !strings.Contains(toolResult.Content, "section-000") {
							return fmt.Errorf("preview = %q, want leading report content", toolResult.Content)
						}
						if strings.Contains(toolResult.Content, "section-040") {
							return fmt.Errorf("preview was not truncated: %q", toolResult.Content)
						}
						if toolResult.Metadata["truncated"] != true {
							return fmt.Errorf("metadata = %#v, want truncated", toolResult.Metadata)
						}
						if id, _ := toolResult.Metadata["stored_result_id"].(string); id == "" {
							return fmt.Errorf("metadata = %#v, want stored_result_id", toolResult.Metadata)
						}
						return nil
					}
					return fmt.Errorf("missing large report tool result")
				},
			},
			{
				Name: "full result stored",
				Check: func(agenteval.Result) error {
					entries, err := store.List(context.Background())
					if err != nil {
						return err
					}
					if len(entries) != 1 {
						return fmt.Errorf("stored entries = %d, want 1", len(entries))
					}
					if !strings.Contains(entries[0].Content, "section-040") {
						return fmt.Errorf("stored content missing full report tail")
					}
					if entries[0].ToolName != "read_large_report" {
						return fmt.Errorf("stored tool = %q, want read_large_report", entries[0].ToolName)
					}
					return nil
				},
			},
		},
	}
}

// BudgetStopsBeforeSecondModelCall returns a single-use scenario where the
// first turn executes a tool, then the budget governor blocks the follow-up
// model call before it starts.
func BudgetStopsBeforeSecondModelCall() agenteval.Case {
	modelClient := agenteval.NewScriptedModel(
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:    "tool-1",
				Name:  "read_file",
				Input: json.RawMessage(`{"path":"README.md"}`),
			},
		}},
		[]model.StreamEvent{{Kind: model.StreamText, Text: "should not run"}},
	)

	return agenteval.Case{
		Name:       "budget_stops_before_second_model_call",
		Prompt:     "Read a file, then continue.",
		AllowError: true,
		Options: memaxagent.Options{
			Model:  modelClient,
			Tools:  tool.NewRegistry(readFileTool()),
			Budget: budget.Policy{MaxModelCalls: 1},
		},
		Assertions: []agenteval.Assertion{
			agenteval.ToolUsed("read_file"),
			toolResultContains("read_file", false, "read README.md"),
			agenteval.RunErrorContains("max model calls"),
			requestCountEquals(modelClient, 1),
		},
	}
}

// BudgetStopsBeforeToolBatch returns a single-use scenario where the model asks
// for more tool calls than the configured budget allows and no handler runs.
func BudgetStopsBeforeToolBatch() agenteval.Case {
	runCount := 0
	guardedRead := tool.Definition{
		ToolSpec: model.ToolSpec{
			Name:        "read_file",
			Description: "Read a workspace file.",
			ReadOnly:    true,
			InputSchema: stringFieldSchema("path"),
		},
		Handler: func(context.Context, tool.Call) (model.ToolResult, error) {
			runCount++
			return model.ToolResult{Content: "handler should not run"}, nil
		},
	}
	modelClient := agenteval.NewScriptedModel(
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:    "tool-1",
				Name:  "read_file",
				Input: json.RawMessage(`{"path":"README.md"}`),
			},
		}, {
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:    "tool-2",
				Name:  "read_file",
				Input: json.RawMessage(`{"path":"docs/architecture.md"}`),
			},
		}},
	)

	return agenteval.Case{
		Name:       "budget_stops_before_tool_batch",
		Prompt:     "Read two files.",
		AllowError: true,
		Options: memaxagent.Options{
			Model:  modelClient,
			Tools:  tool.NewRegistry(guardedRead),
			Budget: budget.Policy{MaxToolCalls: 1},
		},
		Assertions: []agenteval.Assertion{
			agenteval.ToolUsed("read_file"),
			agenteval.RunErrorContains("max tool calls"),
			{
				Name: "tool handlers did not run",
				Check: func(agenteval.Result) error {
					if runCount != 0 {
						return fmt.Errorf("tool handlers ran %d times, want 0", runCount)
					}
					return nil
				},
			},
			requestCountEquals(modelClient, 1),
		},
	}
}

// BudgetStopsAfterTokenUsage returns a single-use scenario where reported usage
// exceeds the token budget and the run stops before producing a final result.
func BudgetStopsAfterTokenUsage() agenteval.Case {
	modelClient := agenteval.NewScriptedModel(
		[]model.StreamEvent{{
			Kind: model.StreamText,
			Text: "expensive answer",
		}, {
			Kind:  model.StreamUsage,
			Usage: &model.Usage{InputTokens: 6, OutputTokens: 5, TotalTokens: 11},
		}},
	)

	return agenteval.Case{
		Name:       "budget_stops_after_token_usage",
		Prompt:     "Answer with usage.",
		AllowError: true,
		Options: memaxagent.Options{
			Model:  modelClient,
			Budget: budget.Policy{MaxTotalTokens: 10},
		},
		Assertions: []agenteval.Assertion{
			agenteval.EventKindEmitted(memaxagent.EventUsage),
			agenteval.RunErrorContains("max total tokens"),
			{
				Name: "no final result emitted",
				Check: func(result agenteval.Result) error {
					if result.Final != "" {
						return fmt.Errorf("final = %q, want empty result after budget stop", result.Final)
					}
					return nil
				},
			},
			requestCountEquals(modelClient, 1),
		},
	}
}

// DeferredToolDiscoveryRecovery returns a single-use scenario where the model
// starts with only the search tool visible, discovers a deferred tool, and uses
// that tool on a later turn through normal tool selection.
func DeferredToolDiscoveryRecovery() agenteval.Case {
	modelClient := agenteval.NewScriptedModel(
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:    "tool-1",
				Name:  toolsearch.ToolName,
				Input: json.RawMessage(`{"query":"archive insight","limit":1}`),
			},
		}},
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:    "tool-2",
				Name:  "archive_lookup",
				Input: json.RawMessage(`{"topic":"migration"}`),
			},
		}},
		[]model.StreamEvent{{Kind: model.StreamText, Text: "completed after deferred discovery"}},
	)
	registry := tool.NewRegistry(archiveLookupTool())
	searchTool, searchErr := toolsearch.NewTool(toolsearch.Config{Registry: registry, Limit: 1})
	if searchErr == nil {
		searchErr = registry.Register(searchTool)
	}

	return agenteval.Case{
		Name:   "deferred_tool_discovery_recovery",
		Prompt: "Find the right capability, then use it.",
		Options: memaxagent.Options{
			Model:        modelClient,
			Tools:        registry,
			ToolSelector: tool.SearchSelector{MaxTools: 2},
		},
		Assertions: []agenteval.Assertion{
			setupSucceeded(searchErr),
			agenteval.ToolUsed(toolsearch.ToolName),
			agenteval.ToolUsed("archive_lookup"),
			agenteval.FinalEquals("completed after deferred discovery"),
			agenteval.NoToolErrors(),
			toolResultContains(toolsearch.ToolName, false, "archive_lookup"),
			toolResultContains("archive_lookup", false, "archive insight: migration"),
			{
				Name: "deferred tool hidden until discovered",
				Check: func(agenteval.Result) error {
					requests := modelClient.Requests()
					if len(requests) != 3 {
						return fmt.Errorf("model requests = %d, want 3", len(requests))
					}
					if tools := requestToolNames(requests[0]); !sameStringSet(tools, []string{toolsearch.ToolName}) {
						return fmt.Errorf("first request tools = %#v, want search tool only", tools)
					}
					if tools := requestToolNames(requests[1]); !sameStringSet(tools, []string{toolsearch.ToolName, "archive_lookup"}) {
						return fmt.Errorf("second request tools = %#v, want discovered archive tool", tools)
					}
					return nil
				},
			},
		},
	}
}

func readFileTool() tool.Tool {
	return tool.Definition{
		ToolSpec: model.ToolSpec{
			Name:        "read_file",
			Description: "Read a workspace file.",
			SearchHint:  "read workspace file",
			ReadOnly:    true,
			InputSchema: stringFieldSchema("path"),
		},
		Handler: func(_ context.Context, call tool.Call) (model.ToolResult, error) {
			var input struct {
				Path string `json:"path"`
			}
			if err := json.Unmarshal(call.Use.Input, &input); err != nil {
				return model.ToolResult{}, err
			}
			return model.ToolResult{Content: "read " + input.Path}, nil
		},
	}
}

func writeFileTool() tool.Tool {
	return tool.Definition{
		ToolSpec: model.ToolSpec{
			Name:        "write_file",
			Description: "Write a workspace file.",
			SearchHint:  "write workspace file",
			Destructive: true,
			InputSchema: map[string]any{
				"type":                 "object",
				"required":             []any{"path", "content"},
				"additionalProperties": false,
				"properties": map[string]any{
					"path":    map[string]any{"type": "string"},
					"content": map[string]any{"type": "string"},
				},
			},
		},
		Handler: func(context.Context, tool.Call) (model.ToolResult, error) {
			return model.ToolResult{Content: "write should not run"}, nil
		},
	}
}

func readSecretTool() tool.Tool {
	return tool.Definition{
		ToolSpec: model.ToolSpec{
			Name:        "read_secret",
			Description: "Read a protected secret.",
			ReadOnly:    true,
			InputSchema: stringFieldSchema("name"),
		},
		Handler: func(context.Context, tool.Call) (model.ToolResult, error) {
			return model.ToolResult{Content: "secret should not be exposed"}, nil
		},
	}
}

func largeReportTool() tool.Tool {
	var b strings.Builder
	for i := 0; i < 48; i++ {
		fmt.Fprintf(&b, "section-%03d: durable audit detail\n", i)
	}
	content := b.String()
	return tool.Definition{
		ToolSpec: model.ToolSpec{
			Name:           "read_large_report",
			Description:    "Read a large audit report.",
			ReadOnly:       true,
			MaxResultBytes: 96,
			InputSchema:    stringFieldSchema("name"),
		},
		Handler: func(context.Context, tool.Call) (model.ToolResult, error) {
			return model.ToolResult{Content: content}, nil
		},
	}
}

func archiveLookupTool() tool.Tool {
	return tool.Definition{
		ToolSpec: model.ToolSpec{
			Name:        "archive_lookup",
			Description: "Look up migration insight from the archive.",
			SearchHint:  "archive insight migration deferred",
			ReadOnly:    true,
			ShouldDefer: true,
			InputSchema: stringFieldSchema("topic"),
		},
		Handler: func(_ context.Context, call tool.Call) (model.ToolResult, error) {
			var input struct {
				Topic string `json:"topic"`
			}
			if err := json.Unmarshal(call.Use.Input, &input); err != nil {
				return model.ToolResult{}, err
			}
			return model.ToolResult{Content: "archive insight: " + input.Topic}, nil
		},
	}
}

func stringFieldSchema(field string) map[string]any {
	return map[string]any{
		"type":                 "object",
		"required":             []any{field},
		"additionalProperties": false,
		"properties": map[string]any{
			field: map[string]any{"type": "string"},
		},
	}
}

func toolResultContains(name string, isError bool, substring string) agenteval.Assertion {
	return agenteval.Assertion{
		Name: fmt.Sprintf("tool result %s contains", name),
		Check: func(result agenteval.Result) error {
			for _, toolResult := range result.ToolResults() {
				if toolResult.Name != name {
					continue
				}
				if toolResult.IsError != isError {
					return fmt.Errorf("tool %q error = %v, want %v", name, toolResult.IsError, isError)
				}
				if !strings.Contains(toolResult.Content, substring) {
					return fmt.Errorf("tool %q content = %q, want substring %q", name, toolResult.Content, substring)
				}
				return nil
			}
			return fmt.Errorf("missing tool result %q", name)
		},
	}
}

func requestCountEquals(modelClient *agenteval.ScriptedModel, want int) agenteval.Assertion {
	return agenteval.Assertion{
		Name: "model request count",
		Check: func(agenteval.Result) error {
			if got := len(modelClient.Requests()); got != want {
				return fmt.Errorf("model requests = %d, want %d", got, want)
			}
			return nil
		},
	}
}

func requestToolNames(req model.Request) []string {
	names := make([]string, 0, len(req.Tools))
	for _, spec := range req.Tools {
		names = append(names, spec.Name)
	}
	return names
}

func sameStringSet(got []string, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	seen := make(map[string]int, len(got))
	for _, item := range got {
		seen[item]++
	}
	for _, item := range want {
		if seen[item] == 0 {
			return false
		}
		seen[item]--
	}
	return true
}
