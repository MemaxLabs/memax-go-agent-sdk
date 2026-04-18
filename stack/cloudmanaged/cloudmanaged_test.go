package cloudmanaged

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
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

type quotaTestModel struct {
	requests []model.Request
	turns    [][]model.StreamEvent
	err      error
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
