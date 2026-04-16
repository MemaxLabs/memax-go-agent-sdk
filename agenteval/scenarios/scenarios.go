// Package scenarios provides reusable deterministic eval cases for core agent
// behaviors.
package scenarios

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	memaxagent "github.com/MemaxLabs/memax-go-agent-sdk"
	"github.com/MemaxLabs/memax-go-agent-sdk/agenteval"
	"github.com/MemaxLabs/memax-go-agent-sdk/contextwindow"
	"github.com/MemaxLabs/memax-go-agent-sdk/memory"
	"github.com/MemaxLabs/memax-go-agent-sdk/model"
	"github.com/MemaxLabs/memax-go-agent-sdk/output"
	"github.com/MemaxLabs/memax-go-agent-sdk/planner"
	"github.com/MemaxLabs/memax-go-agent-sdk/session"
	"github.com/MemaxLabs/memax-go-agent-sdk/tool"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/memorytools"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/subagents"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/tasktools"
)

// All returns the default deterministic autonomy scenario suite. Returned cases
// are single-use because they contain stateful scripted models.
func All() []agenteval.Case {
	return []agenteval.Case{
		ToolRecovery(),
		StructuredOutputRepair(),
		MemorySearchAndSave(),
		MemoryDistillationCandidates(),
		SessionResume(),
		ContextRetry(),
		SubagentDelegation(),
		PlannerGuidedToolUse(),
		PlannerTaskStateUpdates(),
		OpenAIProviderTextAndUsage(),
		AnthropicProviderTextAndUsage(),
		OpenAIProviderToolUseRoundTrip(),
		AnthropicProviderToolUseRoundTrip(),
		PermissionDenialRecovery(),
		HookDenialRecovery(),
		LargeResultStorageRecovery(),
		BudgetStopsBeforeSecondModelCall(),
		BudgetStopsBeforeToolBatch(),
		BudgetStopsAfterTokenUsage(),
		DeferredToolDiscoveryRecovery(),
	}
}

// MemoryDistillationCandidates returns a single-use scenario where successful
// completion produces host-reviewable memory candidates without writing them.
func MemoryDistillationCandidates() agenteval.Case {
	modelClient := agenteval.NewScriptedModel(
		[]model.StreamEvent{{Kind: model.StreamText, Text: "Rollback notes were added before merge."}},
	)
	store := memory.NewMemoryStore(nil)

	return agenteval.Case{
		Name:   "memory_distillation_candidates",
		Prompt: "Finish the migration review.",
		Options: memaxagent.Options{
			Model: modelClient,
			Planner: planner.Static(planner.Plan{
				Goal: "review migration",
				Steps: []planner.Step{{
					ID:     "task-1",
					Title:  "check rollback",
					Status: planner.StatusCompleted,
				}},
			}),
			MemoryDistiller: memory.RuleDistiller{{
				WhenResultContains: "rollback",
				WhenPlanContains:   "migration",
				Memory: memory.Memory{
					Name:    "migration-rollback",
					Scope:   memory.ScopeProject,
					Content: "Migration reviews require rollback notes.",
				},
				Reason:     "completed review established rollback requirement",
				Confidence: 0.9,
			}},
			MemorySource: store,
		},
		Assertions: []agenteval.Assertion{
			agenteval.FinalEquals("Rollback notes were added before merge."),
			agenteval.EventKindEmitted(memaxagent.EventMemoryCandidates),
			requestCountEquals(modelClient, 1),
			{
				Name: "memory candidate emitted without write",
				Check: func(result agenteval.Result) error {
					candidates := result.MemoryCandidates()
					if len(candidates) != 1 || candidates[0].Memory.Name != "migration-rollback" {
						return fmt.Errorf("candidates = %#v, want migration rollback candidate", candidates)
					}
					items, err := store.Memories(context.Background(), memory.Request{})
					if err != nil {
						return err
					}
					if len(items) != 0 {
						return fmt.Errorf("stored memories = %#v, want no automatic writes", items)
					}
					return nil
				},
			},
		},
	}
}

// PlannerTaskStateUpdates returns a single-use scenario where tasktools are
// both model-editable state and the source for prompt-visible planner context.
func PlannerTaskStateUpdates() agenteval.Case {
	store := tasktools.NewMemoryStore([]tasktools.Task{{
		ID:       "task-1",
		Title:    "read migration file",
		Status:   tasktools.StatusInProgress,
		Notes:    "check rollback",
		Priority: 1,
	}})
	modelClient := agenteval.NewScriptedModel(
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:   "tool-1",
				Name: tasktools.UpsertToolName,
				Input: json.RawMessage(`{
					"id":"task-1",
					"status":"completed",
					"notes":"migration file reviewed"
				}`),
			},
		}},
		[]model.StreamEvent{{Kind: model.StreamText, Text: "task plan updated"}},
	)

	return agenteval.Case{
		Name:   "planner_task_state_updates",
		Prompt: "Use the task plan and mark progress.",
		Options: memaxagent.Options{
			Model: modelClient,
			Tools: tool.NewRegistry(
				tasktools.NewListTool(store),
				tasktools.NewUpsertTool(store),
			),
			Planner: tasktools.Planner(store,
				planner.WithTaskGoal("review migration safely"),
				planner.WithTaskToolHints(tasktools.ListToolName, tasktools.UpsertToolName),
			),
		},
		Assertions: []agenteval.Assertion{
			agenteval.ToolUsed(tasktools.UpsertToolName),
			toolResultContains(tasktools.UpsertToolName, false, "upserted task-1"),
			agenteval.FinalEquals("task plan updated"),
			requestCountEquals(modelClient, 2),
			{
				Name: "plan reflected task update",
				Check: func(agenteval.Result) error {
					requests := modelClient.Requests()
					if len(requests) != 2 {
						return fmt.Errorf("model requests = %d, want 2", len(requests))
					}
					first := requests[0].SystemPrompt
					if !strings.Contains(first, "[in_progress] task-1: read migration file") || !strings.Contains(first, "check rollback") {
						return fmt.Errorf("first prompt missing initial task state:\n%s", first)
					}
					second := requests[1].SystemPrompt
					if !strings.Contains(second, "[completed] task-1: read migration file") || !strings.Contains(second, "migration file reviewed") {
						return fmt.Errorf("second prompt missing updated task state:\n%s", second)
					}
					return nil
				},
			},
		},
	}
}

// PlannerGuidedToolUse returns a single-use scenario where host-provided plan
// context is injected into the prompt and the model follows the planned tool
// path.
func PlannerGuidedToolUse() agenteval.Case {
	modelClient := agenteval.NewScriptedModel(
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:    "tool-1",
				Name:  "read_file",
				Input: json.RawMessage(`{"path":"migrations/001.sql"}`),
			},
		}},
		[]model.StreamEvent{{Kind: model.StreamText, Text: "planner-guided review complete"}},
	)

	return agenteval.Case{
		Name:   "planner_guided_tool_use",
		Prompt: "Review the migration using the host plan.",
		Options: memaxagent.Options{
			Model: modelClient,
			Tools: tool.NewRegistry(readFileTool()),
			Planner: planner.Static(planner.Plan{
				Goal:        "review migration safely",
				State:       planner.StateActive,
				Constraints: []string{"inspect the migration before judging risk"},
				Steps: []planner.Step{{
					ID:        "step-1",
					Title:     "read migration file",
					Status:    planner.StatusInProgress,
					ToolHints: []string{"read_file"},
					Evidence:  []string{"migrations/001.sql"},
				}},
			}),
		},
		Assertions: []agenteval.Assertion{
			agenteval.ToolUsed("read_file"),
			toolResultContains("read_file", false, "read migrations/001.sql"),
			agenteval.FinalEquals("planner-guided review complete"),
			requestCountEquals(modelClient, 2),
			{
				Name: "plan injected into prompt",
				Check: func(agenteval.Result) error {
					requests := modelClient.Requests()
					if len(requests) == 0 {
						return fmt.Errorf("missing model request")
					}
					prompt := requests[0].SystemPrompt
					for _, want := range []string{"Host-provided plan", "review migration safely", "read_file", "migrations/001.sql"} {
						if !strings.Contains(prompt, want) {
							return fmt.Errorf("system prompt missing %q:\n%s", want, prompt)
						}
					}
					return nil
				},
			},
		},
	}
}

// ToolRecovery returns a single-use scenario where the model emits invalid tool
// input, receives the validation error as a tool result, and recovers.
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

// StructuredOutputRepair returns a single-use scenario where invalid final JSON
// is persisted, the SDK appends a repair prompt, and the model returns valid
// JSON.
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

// MemorySearchAndSave returns a single-use scenario where the model searches
// durable memories, saves a new memory, and completes with the saved memory in
// the backing store.
func MemorySearchAndSave() agenteval.Case {
	store := memory.NewMemoryStore([]memory.Memory{{
		Name:    "billing-rule",
		Scope:   memory.ScopeProject,
		Content: "Invoices require audit logs.",
		Tags:    []string{"billing"},
	}})
	searchTool, searchErr := memorytools.NewSearchTool(memorytools.Config{Source: store})
	saveTool, saveErr := memorytools.NewSaveTool(memorytools.Config{Writer: store})
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
			toolConstructionSucceeded(searchErr, saveErr),
			agenteval.ToolUsed(memorytools.SearchToolName),
			agenteval.ToolUsed(memorytools.SaveToolName),
			agenteval.FinalEquals("memory saved"),
			{
				Name: "search returned seeded memory",
				Check: func(result agenteval.Result) error {
					for _, toolResult := range result.ToolResults() {
						if toolResult.Name == memorytools.SearchToolName &&
							strings.Contains(toolResult.Content, "billing-rule") &&
							strings.Contains(toolResult.Content, "Invoices require audit logs") {
							return nil
						}
					}
					return fmt.Errorf("search result did not contain seeded billing memory: %#v", result.ToolResults())
				},
			},
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
			agenteval.NoToolErrors(),
		},
	}
}

// SessionResume returns a single-use scenario where a run resumes an existing
// durable transcript and sends both previous and new user messages to the model.
func SessionResume() agenteval.Case {
	store := session.NewMemoryStore()
	sess, createErr := store.Create(context.Background())
	appendErr := error(nil)
	if createErr == nil {
		appendErr = store.Append(context.Background(), sess.ID, textMessage(model.RoleUser, "previous session context"))
	}
	modelClient := agenteval.NewScriptedModel(
		[]model.StreamEvent{{Kind: model.StreamText, Text: "resumed session"}},
	)

	return agenteval.Case{
		Name:   "session_resume",
		Prompt: "continue from previous context",
		Options: memaxagent.Options{
			Model:     modelClient,
			Sessions:  store,
			SessionID: sess.ID,
		},
		Assertions: []agenteval.Assertion{
			setupSucceeded(createErr, appendErr),
			agenteval.FinalEquals("resumed session"),
			{
				Name: "resumed session id used",
				Check: func(result agenteval.Result) error {
					if result.SessionID != sess.ID {
						return fmt.Errorf("session id = %q, want %q", result.SessionID, sess.ID)
					}
					return nil
				},
			},
			{
				Name: "previous transcript sent",
				Check: func(agenteval.Result) error {
					requests := modelClient.Requests()
					if len(requests) != 1 {
						return fmt.Errorf("model requests = %d, want 1", len(requests))
					}
					messages := requests[0].Messages
					if len(messages) != 2 {
						return fmt.Errorf("messages = %#v, want previous and current user messages", messages)
					}
					if messages[0].PlainText() != "previous session context" || messages[1].PlainText() != "continue from previous context" {
						return fmt.Errorf("messages = %#v, want resumed transcript", messages)
					}
					return nil
				},
			},
		},
	}
}

// ContextRetry returns a single-use scenario where a context-window rejection
// triggers the configured retry policy and the compacted retry succeeds.
func ContextRetry() agenteval.Case {
	store := session.NewMemoryStore()
	sess, createErr := store.Create(context.Background())
	appendErr := error(nil)
	if createErr == nil {
		appendErr = store.Append(context.Background(), sess.ID, textMessage(model.RoleUser, "old context that should be dropped"))
	}
	modelClient := &contextRetryClient{
		success: agenteval.NewScriptedModel(
			[]model.StreamEvent{{Kind: model.StreamText, Text: "retried after context pressure"}},
		),
	}

	return agenteval.Case{
		Name:   "context_retry",
		Prompt: "current context retry request",
		Options: memaxagent.Options{
			Model:        modelClient,
			Sessions:     store,
			SessionID:    sess.ID,
			ContextRetry: contextwindow.RecentMessages{MaxMessages: 1},
		},
		Assertions: []agenteval.Assertion{
			setupSucceeded(createErr, appendErr),
			agenteval.FinalEquals("retried after context pressure"),
			{
				Name: "retry used compacted messages",
				Check: func(agenteval.Result) error {
					requests := modelClient.Requests()
					if len(requests) != 2 {
						return fmt.Errorf("model requests = %d, want failed request and retry", len(requests))
					}
					if got := len(requests[0].Messages); got != 2 {
						return fmt.Errorf("first request messages = %d, want full resumed transcript", got)
					}
					retryMessages := requests[1].Messages
					if len(retryMessages) != 1 || retryMessages[0].PlainText() != "current context retry request" {
						return fmt.Errorf("retry messages = %#v, want compacted current request only", retryMessages)
					}
					return nil
				},
			},
		},
	}
}

// SubagentDelegation returns a single-use scenario where a parent agent calls a
// bounded child agent through the normal tool layer and receives child session
// correlation metadata.
func SubagentDelegation() agenteval.Case {
	store := session.NewMemoryStore()
	childModel := agenteval.NewScriptedModel(
		[]model.StreamEvent{{Kind: model.StreamText, Text: "child investigation complete"}},
	)
	delegate, delegateErr := subagents.NewTool(subagents.Config{
		Agents: []subagents.Agent{{
			Name:        "investigator",
			Description: "Investigates a focused question.",
			Options: memaxagent.Options{
				Model:    childModel,
				Sessions: store,
			},
		}},
		DefaultOptions: memaxagent.Options{Sessions: store},
	})
	parentModel := agenteval.NewScriptedModel(
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:   "tool-1",
				Name: "run_subagent",
				Input: json.RawMessage(`{
					"agent":"investigator",
					"prompt":"investigate the migration risk"
				}`),
			},
		}},
		[]model.StreamEvent{{Kind: model.StreamText, Text: "parent received child result"}},
	)

	return agenteval.Case{
		Name:   "subagent_delegation",
		Prompt: "delegate a focused investigation",
		Options: memaxagent.Options{
			Model:    parentModel,
			Tools:    tool.NewRegistry(delegate),
			Sessions: store,
		},
		Assertions: []agenteval.Assertion{
			toolConstructionSucceeded(delegateErr),
			agenteval.ToolUsed("run_subagent"),
			agenteval.FinalEquals("parent received child result"),
			agenteval.NoToolErrors(),
			{
				Name: "subagent result metadata linked",
				Check: func(result agenteval.Result) error {
					for _, toolResult := range result.ToolResults() {
						if toolResult.Name != "run_subagent" {
							continue
						}
						if toolResult.Content != "child investigation complete" {
							return fmt.Errorf("subagent result content = %q, want child result", toolResult.Content)
						}
						if toolResult.Metadata["agent"] != "investigator" {
							return fmt.Errorf("subagent metadata = %#v, want agent", toolResult.Metadata)
						}
						if toolResult.Metadata["parent_session_id"] != result.SessionID {
							return fmt.Errorf("subagent metadata = %#v, want parent session %q", toolResult.Metadata, result.SessionID)
						}
						if child, _ := toolResult.Metadata["child_session_id"].(string); child == "" {
							return fmt.Errorf("subagent metadata = %#v, want child session id", toolResult.Metadata)
						}
						return nil
					}
					return fmt.Errorf("missing subagent tool result")
				},
			},
			{
				Name: "child model ran once",
				Check: func(agenteval.Result) error {
					if got := len(childModel.Requests()); got != 1 {
						return fmt.Errorf("child model requests = %d, want 1", got)
					}
					return nil
				},
			},
		},
	}
}

func toolConstructionSucceeded(errs ...error) agenteval.Assertion {
	return agenteval.Assertion{
		Name: "tool construction succeeded",
		Check: func(agenteval.Result) error {
			for _, err := range errs {
				if err != nil {
					return err
				}
			}
			return nil
		},
	}
}

func setupSucceeded(errs ...error) agenteval.Assertion {
	return agenteval.Assertion{
		Name: "setup succeeded",
		Check: func(agenteval.Result) error {
			for _, err := range errs {
				if err != nil {
					return err
				}
			}
			return nil
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

func textMessage(role model.Role, text string) model.Message {
	return model.Message{
		Role: role,
		Content: []model.ContentBlock{{
			Type: model.ContentText,
			Text: text,
		}},
	}
}

type contextRetryClient struct {
	mu             sync.Mutex
	failed         bool
	failedRequests []model.Request
	success        *agenteval.ScriptedModel
}

func (c *contextRetryClient) Stream(ctx context.Context, req model.Request) (model.Stream, error) {
	c.mu.Lock()
	if !c.failed {
		c.failed = true
		c.failedRequests = append(c.failedRequests, cloneRequest(req))
		c.mu.Unlock()
		return nil, model.ErrContextWindowExceeded
	}
	success := c.success
	c.mu.Unlock()
	return success.Stream(ctx, req)
}

func (c *contextRetryClient) Requests() []model.Request {
	c.mu.Lock()
	failed := make([]model.Request, len(c.failedRequests))
	for i, req := range c.failedRequests {
		failed[i] = cloneRequest(req)
	}
	success := c.success
	c.mu.Unlock()
	if success == nil {
		return failed
	}
	return append(failed, success.Requests()...)
}

func cloneRequest(req model.Request) model.Request {
	req.Messages = cloneMessages(req.Messages)
	req.Tools = cloneToolSpecs(req.Tools)
	return req
}

func cloneMessages(messages []model.Message) []model.Message {
	if len(messages) == 0 {
		return nil
	}
	out := make([]model.Message, len(messages))
	for i, msg := range messages {
		out[i] = msg
		out[i].Content = cloneContentBlocks(msg.Content)
		if msg.ToolResult != nil {
			toolResult := *msg.ToolResult
			toolResult.Metadata = cloneMetadata(toolResult.Metadata)
			out[i].ToolResult = &toolResult
		}
	}
	return out
}

func cloneContentBlocks(blocks []model.ContentBlock) []model.ContentBlock {
	if len(blocks) == 0 {
		return nil
	}
	out := make([]model.ContentBlock, len(blocks))
	for i, block := range blocks {
		out[i] = block
		if block.ToolUse != nil {
			toolUse := *block.ToolUse
			toolUse.Input = append([]byte(nil), block.ToolUse.Input...)
			out[i].ToolUse = &toolUse
		}
	}
	return out
}

func cloneToolSpecs(specs []model.ToolSpec) []model.ToolSpec {
	if len(specs) == 0 {
		return nil
	}
	out := make([]model.ToolSpec, len(specs))
	for i, spec := range specs {
		out[i] = spec
		out[i].InputSchema = cloneSchemaMap(spec.InputSchema)
	}
	return out
}

func cloneSchemaMap(value map[string]any) map[string]any {
	if len(value) == 0 {
		return nil
	}
	out := make(map[string]any, len(value))
	for key, item := range value {
		out[key] = cloneSchemaValue(item)
	}
	return out
}

func cloneSchemaValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return cloneSchemaMap(typed)
	case []any:
		out := make([]any, len(typed))
		for i, item := range typed {
			out[i] = cloneSchemaValue(item)
		}
		return out
	default:
		return typed
	}
}

func cloneMetadata(metadata map[string]any) map[string]any {
	if len(metadata) == 0 {
		return nil
	}
	out := make(map[string]any, len(metadata))
	for key, value := range metadata {
		out[key] = value
	}
	return out
}
