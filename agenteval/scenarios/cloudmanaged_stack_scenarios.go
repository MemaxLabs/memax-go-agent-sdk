package scenarios

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	memaxagent "github.com/MemaxLabs/memax-go-agent-sdk"
	"github.com/MemaxLabs/memax-go-agent-sdk/agenteval"
	"github.com/MemaxLabs/memax-go-agent-sdk/model"
	"github.com/MemaxLabs/memax-go-agent-sdk/stack/cloudmanaged"
	"github.com/MemaxLabs/memax-go-agent-sdk/tenant"
	"github.com/MemaxLabs/memax-go-agent-sdk/tool"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/subagents"
)

// CloudManagedPresetManagedWorkerQuotaDenial returns a single-use scenario
// where a managed-worker preset allows one productive turn, then denies the
// next model request at the tenant seam before the second provider call starts.
func CloudManagedPresetManagedWorkerQuotaDenial() agenteval.Case {
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
	config, configErr := cloudmanaged.PresetManagedWorker.Config()
	if configErr == nil {
		config.Base.Tools = tool.NewRegistry(readFileTool())
		config.Policies.Quota.MaxModelRequests = 1
		config.Policies.Quota.MaxToolUses = 4
	}
	stack, stackErr := cloudmanaged.New(config)

	options := memaxagent.Options{}
	if stackErr == nil {
		options = stack.WithModel(modelClient)
		options.Tenant = tenant.Scope{
			ID:        "tenant-1",
			SubjectID: "user-1",
			Attributes: map[string]string{
				"plan": "managed",
			},
		}
	}

	return agenteval.Case{
		Name:       "cloudmanaged_preset_managed_worker_quota_denial",
		Prompt:     "Read README.md, then continue if more work remains.",
		AllowError: true,
		Options:    options,
		Assertions: []agenteval.Assertion{
			toolConstructionSucceeded(configErr, stackErr),
			agenteval.ToolUsed("read_file"),
			toolResultContains("read_file", false, "read README.md"),
			agenteval.EventKindEmitted(memaxagent.EventTenantDenied),
			agenteval.RunErrorContains("tenant quota exceeded: max model requests"),
			requestCountEquals(modelClient, 1),
			{
				Name: "managed preset guidance appears in first prompt",
				Check: func(result agenteval.Result) error {
					requests := modelClient.Requests()
					if len(requests) != 1 {
						return fmt.Errorf("model requests = %d, want 1", len(requests))
					}
					prompt := requests[0].AppendSystemPrompt
					if !strings.Contains(prompt, "tenant's explicit scope and quota") {
						return fmt.Errorf("append system prompt = %q, want managed quota guidance", prompt)
					}
					return nil
				},
			},
			{
				Name: "tenant denial records model-request boundary",
				Check: func(result agenteval.Result) error {
					for _, event := range result.Events {
						if event.Kind != memaxagent.EventTenantDenied || event.Tenant == nil {
							continue
						}
						if event.Tenant.Boundary != string(tenant.BoundaryModelRequest) {
							return fmt.Errorf("tenant denial boundary = %q, want %q", event.Tenant.Boundary, tenant.BoundaryModelRequest)
						}
						if event.Tenant.TenantID != "tenant-1" || event.Tenant.SubjectID != "user-1" {
							return fmt.Errorf("tenant event = %#v, want tenant-1/user-1", event.Tenant)
						}
						return nil
					}
					return fmt.Errorf("missing tenant denial event")
				},
			},
		},
	}
}

// CloudManagedPresetManagedWorkerDelegatedAuditTrail returns a single-use
// scenario where a managed-worker run delegates to a child agent and the
// host-owned audit sink records the child's internal events under the child
// session, not just the parent's run_subagent wrapper result.
func CloudManagedPresetManagedWorkerDelegatedAuditTrail() agenteval.Case {
	childModel := agenteval.NewScriptedModel(
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:    "child-tool-1",
				Name:  "read_file",
				Input: json.RawMessage(`{"path":"README.md"}`),
			},
		}},
		[]model.StreamEvent{{Kind: model.StreamText, Text: "child done"}},
	)
	delegate, delegateErr := subagents.NewTool(subagents.Config{
		Agents: []subagents.Agent{{
			Name: "worker",
			Options: memaxagent.Options{
				Model: childModel,
				Tools: tool.NewRegistry(readFileTool()),
			},
		}},
	})

	parentModel := agenteval.NewScriptedModel(
		[]model.StreamEvent{{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:    "delegate-1",
				Name:  subagents.ToolName,
				Input: json.RawMessage(`{"agent":"worker","prompt":"Read README.md in the child run."}`),
			},
		}},
		[]model.StreamEvent{{Kind: model.StreamText, Text: "parent done"}},
	)
	sink := &cloudmanaged.MemorySink{}
	config, configErr := cloudmanaged.PresetManagedWorker.Config()
	if configErr == nil && delegateErr == nil {
		config.Base.Model = parentModel
		config.Base.Tools = tool.NewRegistry(delegate)
		config.Policies.Quota.MaxModelRequests = 8
		config.Policies.Quota.MaxToolUses = 8
		config.Audit = cloudmanaged.AuditConfig{Sink: sink}
	}
	stack, stackErr := cloudmanaged.New(config)
	scope := tenant.Scope{
		ID:        "tenant-1",
		SubjectID: "user-1",
		Attributes: map[string]string{
			"plan": "managed",
		},
	}

	return agenteval.Case{
		Name:   "cloudmanaged_preset_managed_worker_delegated_audit_trail",
		Prompt: "Delegate the README read to the worker and continue when it finishes.",
		Run: func(ctx context.Context) (<-chan memaxagent.Event, error) {
			if stackErr != nil {
				return nil, stackErr
			}
			return stack.Query(ctx, "Delegate the README read to the worker and continue when it finishes.", scope)
		},
		Assertions: []agenteval.Assertion{
			toolConstructionSucceeded(configErr, delegateErr, stackErr),
			agenteval.ToolUsed(subagents.ToolName),
			{
				Name: "parent returns final result",
				Check: func(result agenteval.Result) error {
					if !strings.Contains(result.Final, "parent done") {
						return fmt.Errorf("final result = %q, want parent completion", result.Final)
					}
					return nil
				},
			},
			{
				Name: "managed preset guidance appears in first prompt",
				Check: func(result agenteval.Result) error {
					requests := parentModel.Requests()
					if len(requests) == 0 {
						return fmt.Errorf("parent model requests = 0, want at least 1")
					}
					prompt := requests[0].AppendSystemPrompt
					if !strings.Contains(prompt, "tenant's explicit scope and quota") {
						return fmt.Errorf("append system prompt = %q, want managed quota guidance", prompt)
					}
					return nil
				},
			},
			{
				Name: "audit sink records child session events",
				Check: func(result agenteval.Result) error {
					parentSessionID := result.SessionID
					if parentSessionID == "" {
						return fmt.Errorf("result session id is empty")
					}
					var foundChildToolUse bool
					var foundChildToolResult bool
					var foundChildResult bool
					for _, record := range sink.Records() {
						if record.SessionID == "" || record.SessionID == parentSessionID || record.ParentSessionID != parentSessionID {
							continue
						}
						switch record.Kind {
						case memaxagent.EventToolUse:
							if record.ToolUse != nil && record.ToolUse.Name == "read_file" {
								foundChildToolUse = true
							}
						case memaxagent.EventToolResult:
							if record.ToolResult != nil && record.ToolResult.Name == "read_file" && strings.Contains(record.ToolResult.Content, "read README.md") {
								foundChildToolResult = true
							}
						case memaxagent.EventResult:
							if record.Result == "child done" {
								foundChildResult = true
							}
						}
					}
					if !foundChildToolUse || !foundChildToolResult || !foundChildResult {
						return fmt.Errorf("audit records = %#v, want child tool use, tool result, and final result", sink.Records())
					}
					return nil
				},
			},
		},
	}
}
