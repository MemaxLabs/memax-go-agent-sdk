package scenarios

import (
	"encoding/json"
	"fmt"
	"strings"

	memaxagent "github.com/MemaxLabs/memax-go-agent-sdk"
	"github.com/MemaxLabs/memax-go-agent-sdk/agenteval"
	"github.com/MemaxLabs/memax-go-agent-sdk/model"
	"github.com/MemaxLabs/memax-go-agent-sdk/stack/cloudmanaged"
	"github.com/MemaxLabs/memax-go-agent-sdk/tenant"
	"github.com/MemaxLabs/memax-go-agent-sdk/tool"
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
