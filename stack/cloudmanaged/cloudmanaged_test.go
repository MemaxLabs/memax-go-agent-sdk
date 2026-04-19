package cloudmanaged

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	memaxagent "github.com/MemaxLabs/memax-go-agent-sdk"
	"github.com/MemaxLabs/memax-go-agent-sdk/hook"
	"github.com/MemaxLabs/memax-go-agent-sdk/model"
	"github.com/MemaxLabs/memax-go-agent-sdk/session"
	"github.com/MemaxLabs/memax-go-agent-sdk/tenant"
	"github.com/MemaxLabs/memax-go-agent-sdk/tool"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/subagents"
)

func TestQuotaValidatorSessionScopedLimitsAndCleanup(t *testing.T) {
	t.Parallel()

	validator := NewQuotaValidator(Quota{
		MaxModelRequests: 1,
		MaxToolUses:      1,
	}, WithRequiredTenantScope())
	scope := tenant.Scope{ID: "tenant-1", SubjectID: "user-1"}

	if err := validator.Validate(context.Background(), tenant.Request{
		Boundary:  tenant.BoundarySessionStart,
		SessionID: "session-1",
		Scope:     scope,
	}); err != nil {
		t.Fatalf("session start validation error = %v", err)
	}
	if err := validator.Validate(context.Background(), tenant.Request{
		Boundary:  tenant.BoundaryModelRequest,
		SessionID: "session-1",
		Scope:     scope,
	}); err != nil {
		t.Fatalf("first model request error = %v", err)
	}
	if err := validator.Validate(context.Background(), tenant.Request{
		Boundary:  tenant.BoundaryModelRequest,
		Scope:     scope,
		SessionID: "session-1",
	}); err == nil || !strings.Contains(err.Error(), "max model requests") {
		t.Fatalf("second model request error = %v, want max model requests denial", err)
	}
	if err := validator.Validate(context.Background(), tenant.Request{
		Boundary:  tenant.BoundaryToolUse,
		SessionID: "session-1",
		Scope:     scope,
	}); err != nil {
		t.Fatalf("first tool use error = %v", err)
	}
	if err := validator.Validate(context.Background(), tenant.Request{
		Boundary:  tenant.BoundaryToolUse,
		SessionID: "session-1",
		Scope:     scope,
	}); err == nil || !strings.Contains(err.Error(), "max tool uses") {
		t.Fatalf("second tool use error = %v, want max tool uses denial", err)
	}
	if err := validator.SessionEnded(context.Background(), hook.SessionEndedInput{
		SessionID: "session-1",
		Tenant:    scope,
	}); err != nil {
		t.Fatalf("SessionEnded() error = %v", err)
	}
	if err := validator.Validate(context.Background(), tenant.Request{
		Boundary:  tenant.BoundaryModelRequest,
		SessionID: "session-1",
		Scope:     scope,
	}); err != nil {
		t.Fatalf("model request after cleanup error = %v", err)
	}
}

func TestMemoryQuotaStoreReserveAndReset(t *testing.T) {
	t.Parallel()

	store := NewMemoryQuotaStore()
	scope := tenant.Scope{ID: "tenant-1", SubjectID: "user-1"}
	if err := store.EnsureSession(context.Background(), scope, "session-1"); err != nil {
		t.Fatalf("EnsureSession() error = %v", err)
	}
	if used, granted, err := store.Reserve(context.Background(), scope, "session-1", QuotaCounterModelRequests, 1); err != nil || !granted || used != 1 {
		t.Fatalf("Reserve(first model) = (%d, %t, %v), want (1, true, nil)", used, granted, err)
	}
	if used, granted, err := store.Reserve(context.Background(), scope, "session-1", QuotaCounterModelRequests, 1); err != nil || granted || used != 1 {
		t.Fatalf("Reserve(second model) = (%d, %t, %v), want (1, false, nil)", used, granted, err)
	}
	if used, granted, err := store.Reserve(context.Background(), scope, "session-1", QuotaCounterToolUses, 1); err != nil || !granted || used != 1 {
		t.Fatalf("Reserve(first tool) = (%d, %t, %v), want (1, true, nil)", used, granted, err)
	}
	if err := store.ResetSession(context.Background(), scope, "session-1"); err != nil {
		t.Fatalf("ResetSession() error = %v", err)
	}
	if used, granted, err := store.Reserve(context.Background(), scope, "session-1", QuotaCounterModelRequests, 1); err != nil || !granted || used != 1 {
		t.Fatalf("Reserve(after reset) = (%d, %t, %v), want (1, true, nil)", used, granted, err)
	}
}

func TestMemoryQuotaStoreRejectsUnknownCounter(t *testing.T) {
	t.Parallel()

	store := NewMemoryQuotaStore()
	scope := tenant.Scope{ID: "tenant-1", SubjectID: "user-1"}
	if err := store.EnsureSession(context.Background(), scope, "session-1"); err != nil {
		t.Fatalf("EnsureSession() error = %v", err)
	}
	if used, granted, err := store.Reserve(context.Background(), scope, "session-1", QuotaCounter("unknown"), 1); err == nil || granted || used != 0 {
		t.Fatalf("Reserve(unknown) = (%d, %t, %v), want (0, false, error)", used, granted, err)
	}
}

func TestMemoryQuotaStoreReserveIsAtomic(t *testing.T) {
	t.Parallel()

	store := NewMemoryQuotaStore()
	scope := tenant.Scope{ID: "tenant-1", SubjectID: "user-1"}
	if err := store.EnsureSession(context.Background(), scope, "session-1"); err != nil {
		t.Fatalf("EnsureSession() error = %v", err)
	}

	const goroutines = 16
	var wg sync.WaitGroup
	granted := make(chan bool, goroutines)
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, ok, err := store.Reserve(context.Background(), scope, "session-1", QuotaCounterModelRequests, 1)
			if err != nil {
				t.Errorf("Reserve() error = %v", err)
				return
			}
			granted <- ok
		}()
	}
	wg.Wait()
	close(granted)

	var grantedCount int
	for ok := range granted {
		if ok {
			grantedCount++
		}
	}
	if grantedCount != 1 {
		t.Fatalf("granted count = %d, want 1", grantedCount)
	}
}

func TestQuotaValidatorRequiresTenantScope(t *testing.T) {
	t.Parallel()

	validator := NewQuotaValidator(Quota{}, WithRequiredTenantScope())
	err := validator.Validate(context.Background(), tenant.Request{
		Boundary:  tenant.BoundarySessionStart,
		SessionID: "session-1",
	})
	if err == nil || !strings.Contains(err.Error(), "tenant scope required") {
		t.Fatalf("Validate() error = %v, want tenant scope denial", err)
	}
}

func TestNewComposesBaseTenantValidator(t *testing.T) {
	t.Parallel()

	var calls []tenant.Request
	baseValidator := tenant.ValidatorFunc(func(_ context.Context, req tenant.Request) error {
		calls = append(calls, req)
		return nil
	})
	stack, err := New(Config{
		Base: memaxagent.Options{
			TenantValidator: baseValidator,
		},
		Policies: Policies{
			RequireTenantScope: true,
			Quota: Quota{
				MaxModelRequests: 1,
			},
		},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	opts := stack.Options()
	scope := tenant.Scope{ID: "tenant-1", SubjectID: "user-1"}
	for _, req := range []tenant.Request{
		{Boundary: tenant.BoundarySessionStart, SessionID: "session-1", Scope: scope},
		{Boundary: tenant.BoundaryModelRequest, SessionID: "session-1", Scope: scope},
	} {
		if err := tenant.Check(context.Background(), opts.TenantValidator, req); err != nil {
			t.Fatalf("tenant.Check(%s) error = %v", req.Boundary, err)
		}
	}
	err = tenant.Check(context.Background(), opts.TenantValidator, tenant.Request{
		Boundary:  tenant.BoundaryModelRequest,
		SessionID: "session-1",
		Scope:     scope,
	})
	var denied *tenant.DeniedError
	if !errors.As(err, &denied) || denied == nil || !strings.Contains(err.Error(), "max model requests") {
		t.Fatalf("tenant.Check(second model request) error = %v, want denied max model requests", err)
	}
	if len(calls) != 3 {
		t.Fatalf("base validator calls = %d, want 3", len(calls))
	}
	if calls[0].Boundary != tenant.BoundarySessionStart || calls[1].Boundary != tenant.BoundaryModelRequest || calls[2].Boundary != tenant.BoundaryModelRequest {
		t.Fatalf("base validator boundaries = %#v", calls)
	}
	if opts.Hooks == nil {
		t.Fatal("stack hooks = nil, want cleanup hook")
	}
	errs := opts.Hooks.SessionEnded(context.Background(), hook.SessionEndedInput{
		SessionID: "session-1",
		Tenant:    scope,
	})
	if len(errs) != 0 {
		t.Fatalf("SessionEnded() errors = %v, want none", errs)
	}
	if err := tenant.Check(context.Background(), opts.TenantValidator, tenant.Request{
		Boundary:  tenant.BoundaryModelRequest,
		SessionID: "session-1",
		Scope:     scope,
	}); err != nil {
		t.Fatalf("tenant.Check(model request after cleanup) error = %v", err)
	}
}

func TestNewUsesConfiguredQuotaStore(t *testing.T) {
	t.Parallel()

	store := &quotaSpyStore{}
	stack, err := New(Config{
		QuotaStore: store,
		Policies: Policies{
			RequireTenantScope: true,
			Quota: Quota{
				MaxModelRequests: 2,
				MaxToolUses:      3,
			},
		},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	opts := stack.Options()
	scope := tenant.Scope{ID: "tenant-1", SubjectID: "user-1"}
	for _, req := range []tenant.Request{
		{Boundary: tenant.BoundarySessionStart, SessionID: "session-1", Scope: scope},
		{Boundary: tenant.BoundaryModelRequest, SessionID: "session-1", Scope: scope},
		{Boundary: tenant.BoundaryToolUse, SessionID: "session-1", Scope: scope},
	} {
		if err := tenant.Check(context.Background(), opts.TenantValidator, req); err != nil {
			t.Fatalf("tenant.Check(%s) error = %v", req.Boundary, err)
		}
	}
	if store.ensureCalls != 1 {
		t.Fatalf("ensure calls = %d, want 1", store.ensureCalls)
	}
	if len(store.reserveCalls) != 2 {
		t.Fatalf("reserve calls = %#v, want 2", store.reserveCalls)
	}
	if store.reserveCalls[0].counter != QuotaCounterModelRequests || store.reserveCalls[0].limit != 2 {
		t.Fatalf("reserve call 0 = %#v, want model request limit 2", store.reserveCalls[0])
	}
	if store.reserveCalls[1].counter != QuotaCounterToolUses || store.reserveCalls[1].limit != 3 {
		t.Fatalf("reserve call 1 = %#v, want tool use limit 3", store.reserveCalls[1])
	}
	if opts.Hooks == nil {
		t.Fatal("stack hooks = nil, want cleanup hook")
	}
	if errs := opts.Hooks.SessionEnded(context.Background(), hook.SessionEndedInput{
		SessionID: "session-1",
		Tenant:    scope,
	}); len(errs) != 0 {
		t.Fatalf("SessionEnded() errors = %v, want none", errs)
	}
	if store.resetCalls != 1 {
		t.Fatalf("reset calls = %d, want 1", store.resetCalls)
	}
	if store.resetScope.ID != scope.ID || store.resetScope.SubjectID != scope.SubjectID {
		t.Fatalf("reset scope = %#v, want tenant %q subject %q", store.resetScope, scope.ID, scope.SubjectID)
	}
}

func TestQuotaValidatorDelegatedChildGetsFreshSessionEnvelope(t *testing.T) {
	t.Parallel()

	store := session.NewMemoryStore()
	childModel := &quotaTestModel{turns: [][]model.StreamEvent{{{Kind: model.StreamText, Text: "child ok"}}}}
	delegate, err := subagents.NewTool(subagents.Config{
		Agents: []subagents.Agent{{
			Name: "worker",
			Options: memaxagent.Options{
				Model: childModel,
			},
		}},
	})
	if err != nil {
		t.Fatalf("NewTool() error = %v", err)
	}

	validator := NewQuotaValidator(Quota{
		MaxModelRequests: 1,
	}, WithRequiredTenantScope())
	scope := tenant.Scope{ID: "tenant-1", SubjectID: "user-1"}
	for _, req := range []tenant.Request{
		{Boundary: tenant.BoundarySessionStart, SessionID: "parent-session", Scope: scope},
		{Boundary: tenant.BoundaryModelRequest, SessionID: "parent-session", Scope: scope},
	} {
		if err := tenant.Check(context.Background(), validator, req); err != nil {
			t.Fatalf("parent tenant.Check(%s) error = %v", req.Boundary, err)
		}
	}

	result, err := delegate.Execute(context.Background(), tool.Call{
		Use: model.ToolUse{
			ID:    "delegate-1",
			Name:  delegate.Spec().Name,
			Input: json.RawMessage(`{"prompt":"delegate after parent quota use"}`),
		},
		Runtime: tool.Runtime{
			SessionID:       "parent-session",
			Sessions:        store,
			Tenant:          scope,
			TenantValidator: validator,
		},
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.IsError {
		t.Fatalf("result = %#v, want successful child run", result)
	}
	if got := result.Content; got != "child ok" {
		t.Fatalf("result content = %q, want %q", got, "child ok")
	}
	if len(childModel.requests) != 1 {
		t.Fatalf("child model requests = %d, want 1", len(childModel.requests))
	}
	if childModel.requests[0].SessionID == "parent-session" {
		t.Fatalf("child session id = %q, want fresh child session", childModel.requests[0].SessionID)
	}
	if childModel.requests[0].Tenant.ID != "tenant-1" {
		t.Fatalf("child tenant = %#v, want inherited tenant", childModel.requests[0].Tenant)
	}
}

func TestPresetsAndDefaultPolicies(t *testing.T) {
	t.Parallel()

	if got := Presets(); len(got) != 1 || got[0] != PresetManagedWorker {
		t.Fatalf("Presets() = %v, want managed worker preset", got)
	}
	policies := DefaultPolicies()
	if !policies.RequireTenantScope || policies.Quota.MaxModelRequests <= 0 || policies.Quota.MaxToolUses <= 0 {
		t.Fatalf("DefaultPolicies() = %#v, want tenant scope and active quotas", policies)
	}
	cfg, err := PresetManagedWorker.Config()
	if err != nil {
		t.Fatalf("PresetManagedWorker.Config() error = %v", err)
	}
	if !strings.Contains(cfg.Base.AppendSystemPrompt, "tenant's explicit scope and quota") {
		t.Fatalf("AppendSystemPrompt = %q, want tenant/quota guidance", cfg.Base.AppendSystemPrompt)
	}
	if _, err := Preset("unknown").Config(); err == nil {
		t.Fatal("unknown preset returned nil error")
	}
}

func TestStackQueryAsyncAuditsTenantDenialEvents(t *testing.T) {
	t.Parallel()

	modelClient := &quotaTestModel{
		turns: [][]model.StreamEvent{
			{{
				Kind: model.StreamToolUse,
				ToolUse: model.ToolUse{
					ID:    "tool-1",
					Name:  "read_file",
					Input: json.RawMessage(`{"path":"README.md"}`),
				},
			}},
			{{Kind: model.StreamText, Text: "should not run"}},
		},
	}
	sink := &MemorySink{}
	stack, err := New(Config{
		Base: memaxagent.Options{
			Model: modelClient,
			Tools: tool.NewRegistry(tool.Definition{
				ToolSpec: model.ToolSpec{
					Name:        "read_file",
					Description: "Read a file.",
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
					return model.ToolResult{Content: "read README.md"}, nil
				},
			}),
		},
		Policies: Policies{
			RequireTenantScope: true,
			Quota: Quota{
				MaxModelRequests: 1,
				MaxToolUses:      4,
			},
		},
		Audit: AuditConfig{Sink: sink},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	var events []memaxagent.Event
	scope := tenant.Scope{ID: "tenant-1", SubjectID: "user-1"}
	for event := range stack.QueryAsync(context.Background(), "Read README.md, then continue.", scope) {
		events = append(events, event)
	}
	if len(events) == 0 {
		t.Fatal("events = 0, want audited event stream")
	}
	if got := stack.Options().Tenant; !got.IsZero() {
		t.Fatalf("stack.Options().Tenant = %#v, want zero scope after per-request query", got)
	}

	records := sink.Records()
	if len(records) == 0 {
		t.Fatal("audit records = 0, want mirrored events")
	}
	if len(records) != len(events) {
		t.Fatalf("audit records = %d, want %d", len(records), len(events))
	}
	foundTenantDenied := false
	foundError := false
	for _, record := range records {
		switch record.Kind {
		case memaxagent.EventTenantDenied:
			foundTenantDenied = true
			if record.Tenant == nil || record.Tenant.Boundary != string(tenant.BoundaryModelRequest) || record.Tenant.TenantID != "tenant-1" {
				t.Fatalf("tenant denial record = %#v, want model-request tenant payload", record)
			}
		case memaxagent.EventError:
			foundError = true
			if !strings.Contains(record.Error, "tenant quota exceeded: max model requests") {
				t.Fatalf("error record = %#v, want quota error", record)
			}
		}
	}
	if !foundTenantDenied || !foundError {
		t.Fatalf("records = %#v, want tenant_denied and error", records)
	}
}

func TestStackQueryAsyncAuditsDelegatedChildEvents(t *testing.T) {
	t.Parallel()

	childModel := &quotaTestModel{
		turns: [][]model.StreamEvent{
			{{
				Kind: model.StreamToolUse,
				ToolUse: model.ToolUse{
					ID:    "child-tool-1",
					Name:  "read_file",
					Input: json.RawMessage(`{"path":"README.md"}`),
				},
			}},
			{{Kind: model.StreamText, Text: "child done"}},
		},
	}
	delegate, err := subagents.NewTool(subagents.Config{
		Agents: []subagents.Agent{{
			Name: "worker",
			Options: memaxagent.Options{
				Model: childModel,
				Tools: tool.NewRegistry(tool.Definition{
					ToolSpec: model.ToolSpec{
						Name:        "read_file",
						Description: "Read a file.",
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
						return model.ToolResult{Content: "child read README.md"}, nil
					},
				}),
			},
		}},
	})
	if err != nil {
		t.Fatalf("NewTool() error = %v", err)
	}

	parentModel := &quotaTestModel{
		turns: [][]model.StreamEvent{
			{{
				Kind: model.StreamToolUse,
				ToolUse: model.ToolUse{
					ID:    "delegate-1",
					Name:  delegate.Spec().Name,
					Input: json.RawMessage(`{"agent":"worker","prompt":"read README.md in a child run"}`),
				},
			}},
			{{Kind: model.StreamText, Text: "parent done"}},
		},
	}
	sink := &MemorySink{}
	stack, err := New(Config{
		Base: memaxagent.Options{
			Model: parentModel,
			Tools: tool.NewRegistry(delegate),
		},
		Policies: Policies{
			RequireTenantScope: true,
			Quota: Quota{
				MaxModelRequests: 8,
				MaxToolUses:      8,
			},
		},
		Audit: AuditConfig{Sink: sink},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	scope := tenant.Scope{ID: "tenant-1", SubjectID: "user-1"}
	var events []memaxagent.Event
	for event := range stack.QueryAsync(context.Background(), "Delegate the README read to the worker.", scope) {
		events = append(events, event)
	}
	if len(events) == 0 {
		t.Fatal("events = 0, want delegated run output")
	}
	parentSessionID := parentModel.requests[0].SessionID
	if parentSessionID == "" {
		t.Fatalf("parent model request = %#v, want session id", parentModel.requests[0])
	}

	var foundChildToolUse bool
	var foundChildToolResult bool
	var foundChildResult bool
	records := sink.Records()
	for _, record := range records {
		if record.SessionID == "" || record.SessionID == parentSessionID || record.ParentSessionID != parentSessionID {
			continue
		}
		switch record.Kind {
		case memaxagent.EventToolUse:
			if record.ToolUse != nil && record.ToolUse.Name == "read_file" {
				foundChildToolUse = true
			}
		case memaxagent.EventToolResult:
			if record.ToolResult != nil && record.ToolResult.Name == "read_file" && record.ToolResult.Content == "child read README.md" {
				foundChildToolResult = true
			}
		case memaxagent.EventResult:
			if record.Result == "child done" {
				foundChildResult = true
			}
		}
	}
	if !foundChildToolUse || !foundChildToolResult || !foundChildResult {
		t.Fatalf("records = %#v, want child tool use, tool result, and final result under delegated child session", records)
	}
}

func TestJSONLSinkWritesStructuredRecords(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	sink := NewJSONLSink(&buf)
	err := sink.WriteAudit(context.Background(), AuditRecord{
		Kind:      memaxagent.EventTenantDenied,
		SessionID: "session-1",
		Time:      timeDate(2026, 4, 18, 0, 0, 0),
		Tenant: &memaxagent.TenantEvent{
			Boundary:  string(tenant.BoundaryModelRequest),
			TenantID:  "tenant-1",
			SubjectID: "user-1",
			Reason:    "quota exceeded",
		},
	})
	if err != nil {
		t.Fatalf("WriteAudit() error = %v", err)
	}
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 1 {
		t.Fatalf("JSONL lines = %d, want 1", len(lines))
	}
	var record AuditRecord
	if err := json.Unmarshal([]byte(lines[0]), &record); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if record.Kind != memaxagent.EventTenantDenied || record.Tenant == nil || record.Tenant.TenantID != "tenant-1" {
		t.Fatalf("record = %#v, want tenant denial JSON", record)
	}
}

func TestAuditSubscriberReportsSinkErrorsWithoutBreakingEvents(t *testing.T) {
	t.Parallel()

	var handled []string
	config := AuditConfig{
		Sink: AuditSinkFunc(func(context.Context, AuditRecord) error {
			return errors.New("sink unavailable")
		}),
		ErrorHandler: AuditErrorHandlerFunc(func(_ context.Context, err error) {
			handled = append(handled, err.Error())
		}),
	}
	in := make(chan memaxagent.Event, 1)
	in <- memaxagent.Event{Kind: memaxagent.EventSessionStarted, SessionID: "session-1"}
	close(in)

	var out []memaxagent.Event
	for event := range config.Subscribe(context.Background(), in) {
		out = append(out, event)
	}
	if len(out) != 1 || out[0].Kind != memaxagent.EventSessionStarted {
		t.Fatalf("out = %#v, want forwarded event", out)
	}
	if len(handled) != 1 || handled[0] != "sink unavailable" {
		t.Fatalf("handled errors = %v, want sink error callback", handled)
	}
}

func TestAsyncSinkPreservesOrderAndDrainsOnClose(t *testing.T) {
	t.Parallel()

	inner := &MemorySink{}
	sink, err := NewAsyncSink(inner, WithAsyncSinkBufferSize(2))
	if err != nil {
		t.Fatalf("NewAsyncSink() error = %v", err)
	}
	for i := 0; i < 3; i++ {
		if err := sink.WriteAudit(context.Background(), AuditRecord{
			Kind:      memaxagent.EventToolResult,
			SessionID: "session-1",
			Turn:      i + 1,
			Result:    fmt.Sprintf("result-%d", i+1),
		}); err != nil {
			t.Fatalf("WriteAudit(%d) error = %v", i, err)
		}
	}
	if err := sink.Close(context.Background()); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	stats := sink.Stats()
	if stats.WrittenCount != 3 || stats.DroppedCount != 0 || stats.QueueDepth != 0 {
		t.Fatalf("Stats() = %#v, want written=3 dropped=0 depth=0", stats)
	}
	records := inner.Records()
	if len(records) != 3 {
		t.Fatalf("records = %#v, want 3 drained records", records)
	}
	for i, record := range records {
		want := fmt.Sprintf("result-%d", i+1)
		if record.Result != want {
			t.Fatalf("record %d = %#v, want result %q", i, record, want)
		}
	}
	if err := sink.WriteAudit(context.Background(), AuditRecord{}); !errors.Is(err, ErrAsyncSinkClosed) {
		t.Fatalf("WriteAudit(after close) error = %v, want ErrAsyncSinkClosed", err)
	}
}

func TestAsyncSinkSinkErrorsAreReportedAsynchronously(t *testing.T) {
	t.Parallel()

	var handled []string
	var mu sync.Mutex
	sink, err := NewAsyncSink(
		AuditSinkFunc(func(context.Context, AuditRecord) error {
			return errors.New("sink unavailable")
		}),
		WithAsyncSinkErrorHandler(AuditErrorHandlerFunc(func(_ context.Context, err error) {
			mu.Lock()
			defer mu.Unlock()
			handled = append(handled, err.Error())
		})),
	)
	if err != nil {
		t.Fatalf("NewAsyncSink() error = %v", err)
	}
	if err := sink.WriteAudit(context.Background(), AuditRecord{Kind: memaxagent.EventSessionStarted}); err != nil {
		t.Fatalf("WriteAudit() error = %v", err)
	}
	if err := sink.Close(context.Background()); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	stats := sink.Stats()
	if stats.WrittenCount != 1 || stats.DroppedCount != 0 || stats.QueueDepth != 0 {
		t.Fatalf("Stats() = %#v, want written=1 dropped=0 depth=0", stats)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(handled) != 1 || handled[0] != "sink unavailable" {
		t.Fatalf("handled errors = %v, want sink error callback", handled)
	}
}

func TestAsyncSinkDropOldestKeepsNewestQueuedRecords(t *testing.T) {
	t.Parallel()

	inner := &blockingAuditSink{
		release: make(chan struct{}),
		started: make(chan struct{}, 1),
	}
	var handled []error
	var handledMu sync.Mutex
	sink, err := NewAsyncSink(
		inner,
		WithAsyncSinkBufferSize(2),
		WithAsyncOverflowPolicy(AsyncOverflowDropOldest),
		WithAsyncSinkErrorHandler(AuditErrorHandlerFunc(func(_ context.Context, err error) {
			handledMu.Lock()
			defer handledMu.Unlock()
			handled = append(handled, err)
		})),
	)
	if err != nil {
		t.Fatalf("NewAsyncSink() error = %v", err)
	}

	if err := sink.WriteAudit(context.Background(), AuditRecord{
		Kind:      memaxagent.EventToolResult,
		SessionID: "session-1",
		Result:    "result-1",
	}); err != nil {
		t.Fatalf("WriteAudit(%q) error = %v", "result-1", err)
	}
	select {
	case <-inner.started:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("first async write did not reach inner sink")
	}
	for _, result := range []string{"result-2", "result-3", "result-4"} {
		if err := sink.WriteAudit(context.Background(), AuditRecord{
			Kind:      memaxagent.EventToolResult,
			SessionID: "session-1",
			Result:    result,
		}); err != nil {
			t.Fatalf("WriteAudit(%q) error = %v", result, err)
		}
	}
	stats := sink.Stats()
	if stats.WrittenCount != 4 {
		t.Fatalf("Stats().WrittenCount = %d, want 4", stats.WrittenCount)
	}
	if stats.DroppedCount == 0 {
		t.Fatalf("Stats().DroppedCount = %d, want > 0 during overflow", stats.DroppedCount)
	}
	if stats.QueueDepth == 0 {
		t.Fatalf("Stats().QueueDepth = %d, want buffered records during overflow", stats.QueueDepth)
	}
	close(inner.release)
	if err := sink.Close(context.Background()); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	stats = sink.Stats()
	if stats.QueueDepth != 0 {
		t.Fatalf("Stats().QueueDepth after close = %d, want 0", stats.QueueDepth)
	}

	records := inner.Records()
	if len(records) != 3 {
		t.Fatalf("records = %#v, want 3 retained records", records)
	}
	got := []string{records[0].Result, records[1].Result, records[2].Result}
	want := []string{"result-1", "result-3", "result-4"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("retained results = %v, want %v", got, want)
	}
	handledMu.Lock()
	defer handledMu.Unlock()
	if len(handled) == 0 {
		t.Fatal("handled overflow errors = 0, want at least one")
	}
	var overflow *AsyncSinkOverflowError
	if !errors.As(handled[0], &overflow) || overflow.Policy != AsyncOverflowDropOldest {
		t.Fatalf("handled overflow error = %#v, want AsyncSinkOverflowError(drop_oldest)", handled[0])
	}
}

func TestAsyncSinkBlockHonorsContextWhenFull(t *testing.T) {
	t.Parallel()

	inner := &blockingAuditSink{release: make(chan struct{})}
	sink, err := NewAsyncSink(inner, WithAsyncSinkBufferSize(1))
	if err != nil {
		t.Fatalf("NewAsyncSink() error = %v", err)
	}
	if err := sink.WriteAudit(context.Background(), AuditRecord{Kind: memaxagent.EventToolResult, Result: "result-1"}); err != nil {
		t.Fatalf("WriteAudit(result-1) error = %v", err)
	}
	if err := sink.WriteAudit(context.Background(), AuditRecord{Kind: memaxagent.EventToolResult, Result: "result-2"}); err != nil {
		t.Fatalf("WriteAudit(result-2) error = %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := sink.WriteAudit(ctx, AuditRecord{Kind: memaxagent.EventToolResult, Result: "result-3"}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("WriteAudit(result-3) error = %v, want deadline exceeded", err)
	}
	stats := sink.Stats()
	if stats.WrittenCount != 2 || stats.DroppedCount != 0 {
		t.Fatalf("Stats() before close = %#v, want written=2 dropped=0", stats)
	}
	if stats.QueueDepth == 0 {
		t.Fatalf("Stats().QueueDepth = %d, want buffered records before release", stats.QueueDepth)
	}
	close(inner.release)
	if err := sink.Close(context.Background()); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	stats = sink.Stats()
	if stats.QueueDepth != 0 {
		t.Fatalf("Stats().QueueDepth after close = %d, want 0", stats.QueueDepth)
	}
}

type quotaTestModel struct {
	requests []model.Request
	turns    [][]model.StreamEvent
	err      error
}

type blockingAuditSink struct {
	mu      sync.Mutex
	release chan struct{}
	started chan struct{}
	records []AuditRecord
}

func (s *blockingAuditSink) WriteAudit(_ context.Context, record AuditRecord) error {
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
	s.records = append(s.records, cloneAuditRecord(record))
	return nil
}

func (s *blockingAuditSink) Records() []AuditRecord {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]AuditRecord, len(s.records))
	for i, record := range s.records {
		out[i] = cloneAuditRecord(record)
	}
	return out
}

type quotaReserveCall struct {
	sessionID string
	counter   QuotaCounter
	limit     int
}

type quotaSpyStore struct {
	ensureCalls  int
	resetCalls   int
	resetScope   tenant.Scope
	reserveCalls []quotaReserveCall
}

func (s *quotaSpyStore) EnsureSession(context.Context, tenant.Scope, string) error {
	s.ensureCalls++
	return nil
}

func (s *quotaSpyStore) Reserve(_ context.Context, _ tenant.Scope, sessionID string, counter QuotaCounter, limit int) (int, bool, error) {
	s.reserveCalls = append(s.reserveCalls, quotaReserveCall{
		sessionID: sessionID,
		counter:   counter,
		limit:     limit,
	})
	return 1, true, nil
}

func (s *quotaSpyStore) ResetSession(_ context.Context, scope tenant.Scope, _ string) error {
	s.resetCalls++
	s.resetScope = scope
	return nil
}

func (m *quotaTestModel) Stream(_ context.Context, req model.Request) (model.Stream, error) {
	m.requests = append(m.requests, req)
	if m.err != nil {
		return nil, m.err
	}
	if len(m.turns) == 0 {
		return &quotaTestStream{}, nil
	}
	events := m.turns[0]
	m.turns = m.turns[1:]
	return &quotaTestStream{events: events}, nil
}

type quotaTestStream struct {
	events []model.StreamEvent
	index  int
}

func (s *quotaTestStream) Recv() (model.StreamEvent, error) {
	if s.index >= len(s.events) {
		return model.StreamEvent{}, model.ErrEndOfStream
	}
	event := s.events[s.index]
	s.index++
	return event, nil
}

func (s *quotaTestStream) Close() error {
	return nil
}

func timeDate(year int, month time.Month, day, hour, min, sec int) time.Time {
	return time.Date(year, month, day, hour, min, sec, 0, time.UTC)
}
