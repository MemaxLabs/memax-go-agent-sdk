package scenarios

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

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

// CloudManagedPresetManagedWorkerAsyncAuditBackpressure returns a managed-stack
// scenario where a blocked host audit sink forces AsyncSink into sustained
// overflow, while the agent run still completes and exposes queue telemetry.
func CloudManagedPresetManagedWorkerAsyncAuditBackpressure() agenteval.Case {
	var firstTurn []model.StreamEvent
	for i := 0; i < 8; i++ {
		firstTurn = append(firstTurn, model.StreamEvent{
			Kind: model.StreamToolUse,
			ToolUse: model.ToolUse{
				ID:    fmt.Sprintf("tool-%d", i+1),
				Name:  "read_file",
				Input: json.RawMessage(`{"path":"README.md"}`),
			},
		})
	}
	modelClient := agenteval.NewScriptedModel(
		firstTurn,
		[]model.StreamEvent{{Kind: model.StreamText, Text: "managed done"}},
	)

	inner := &scenarioBlockingAuditSink{
		release: make(chan struct{}),
		started: make(chan struct{}, 1),
	}
	overflowErrors := &scenarioAuditErrors{}
	asyncSink, sinkErr := cloudmanaged.NewAsyncSink(
		inner,
		cloudmanaged.WithAsyncSinkBufferSize(4),
		cloudmanaged.WithAsyncOverflowPolicy(cloudmanaged.AsyncOverflowDropOldest),
		cloudmanaged.WithAsyncSinkErrorHandler(cloudmanaged.AuditErrorHandlerFunc(func(_ context.Context, err error) {
			overflowErrors.Add(err)
		})),
	)
	closeOnce := sync.Once{}
	closeResources := func() {
		closeOnce.Do(func() {
			close(inner.release)
			_ = asyncSink.Close(context.Background())
		})
	}

	config, configErr := cloudmanaged.PresetManagedWorker.Config()
	if configErr == nil && sinkErr == nil {
		config.Base.Model = modelClient
		config.Base.Tools = tool.NewRegistry(readFileTool())
		config.Policies.Quota.MaxModelRequests = 8
		config.Policies.Quota.MaxToolUses = 32
		config.Audit = cloudmanaged.AuditConfig{Sink: asyncSink}
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
		Name:    "cloudmanaged_preset_managed_worker_async_audit_backpressure",
		Prompt:  "Read README.md several times, then finish.",
		Timeout: 2 * time.Second,
		Run: func(ctx context.Context) (<-chan memaxagent.Event, error) {
			if stackErr != nil {
				return nil, stackErr
			}
			return stack.Query(ctx, "Read README.md several times, then finish.", scope)
		},
		Cleanup: closeResources,
		Assertions: []agenteval.Assertion{
			toolConstructionSucceeded(configErr, sinkErr, stackErr),
			agenteval.ToolUsed("read_file"),
			{
				Name: "managed run completes while inner audit sink is blocked",
				Check: func(result agenteval.Result) error {
					if result.Final != "managed done" {
						return fmt.Errorf("final result = %q, want %q", result.Final, "managed done")
					}
					return nil
				},
			},
			{
				Name: "async sink reports overflow telemetry before drain",
				Check: func(result agenteval.Result) error {
					stats := asyncSink.Stats()
					if stats.DroppedCount == 0 {
						return fmt.Errorf("async sink stats = %#v, want dropped records under backpressure", stats)
					}
					if stats.QueueDepth == 0 {
						return fmt.Errorf("async sink stats = %#v, want buffered records before drain", stats)
					}
					if stats.WrittenCount <= stats.DroppedCount {
						return fmt.Errorf("async sink stats = %#v, want accepted records beyond drops", stats)
					}
					return nil
				},
			},
			{
				Name: "async sink reports overflow errors and drains retained records in order",
				Check: func(result agenteval.Result) error {
					closeResources()
					stats := asyncSink.Stats()
					if stats.QueueDepth != 0 {
						return fmt.Errorf("async sink stats after close = %#v, want empty queue", stats)
					}
					if !overflowErrors.ContainsAsyncOverflow() {
						return fmt.Errorf("overflow errors = %#v, want AsyncSinkOverflowError", overflowErrors.Errors())
					}
					records := inner.Records()
					if len(records) == 0 {
						return fmt.Errorf("audit records = 0, want drained records")
					}
					last := records[len(records)-1]
					if last.Kind != memaxagent.EventResult || last.Result != "managed done" {
						return fmt.Errorf("last audit record = %#v, want final result record", last)
					}
					return nil
				},
			},
		},
	}
}

type scenarioBlockingAuditSink struct {
	mu      sync.Mutex
	release chan struct{}
	started chan struct{}
	records []cloudmanaged.AuditRecord
}

func (s *scenarioBlockingAuditSink) WriteAudit(_ context.Context, record cloudmanaged.AuditRecord) error {
	if s == nil {
		return nil
	}
	if s.started != nil {
		select {
		case s.started <- struct{}{}:
		default:
		}
	}
	if s.release != nil {
		<-s.release
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records = append(s.records, record)
	return nil
}

func (s *scenarioBlockingAuditSink) Records() []cloudmanaged.AuditRecord {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]cloudmanaged.AuditRecord, len(s.records))
	copy(out, s.records)
	return out
}

type scenarioAuditErrors struct {
	mu     sync.Mutex
	errors []error
}

func (s *scenarioAuditErrors) Add(err error) {
	if s == nil || err == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.errors = append(s.errors, err)
}

func (s *scenarioAuditErrors) Errors() []error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]error, len(s.errors))
	copy(out, s.errors)
	return out
}

func (s *scenarioAuditErrors) ContainsAsyncOverflow() bool {
	for _, err := range s.Errors() {
		var overflow *cloudmanaged.AsyncSinkOverflowError
		if errors.As(err, &overflow) {
			return true
		}
	}
	return false
}
