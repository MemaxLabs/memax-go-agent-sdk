package subagents

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	memaxagent "github.com/MemaxLabs/memax-go-agent-sdk"
	"github.com/MemaxLabs/memax-go-agent-sdk/model"
	"github.com/MemaxLabs/memax-go-agent-sdk/planner"
	"github.com/MemaxLabs/memax-go-agent-sdk/session"
	"github.com/MemaxLabs/memax-go-agent-sdk/tool"
)

func TestToolRunsChildAgentWithParentCorrelation(t *testing.T) {
	store := session.NewMemoryStore()
	childModel := &fakeModel{turns: [][]model.StreamEvent{{{Kind: model.StreamText, Text: "child done"}}}}
	delegate, err := NewTool(Config{
		Agents: []Agent{{
			Name:        "worker",
			Description: "Worker",
			Options: memaxagent.Options{
				Model:    childModel,
				Sessions: store,
				MaxTurns: 2,
			},
		}},
	})
	if err != nil {
		t.Fatalf("NewTool returned error: %v", err)
	}

	result, err := delegate.Execute(context.Background(), tool.Call{
		Use: model.ToolUse{
			ID:    "delegate-1",
			Name:  delegate.Spec().Name,
			Input: json.RawMessage(`{"prompt":"inspect the subsystem"}`),
		},
		Runtime: tool.Runtime{
			SessionID: "parent-session",
			Sessions:  store,
		},
	})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if result.IsError {
		t.Fatalf("result is error: %#v", result)
	}
	if result.Content != "child done" {
		t.Fatalf("Content = %q, want child done", result.Content)
	}
	if result.Metadata["parent_session_id"] != "parent-session" {
		t.Fatalf("metadata = %#v, want parent session id", result.Metadata)
	}
	childSessionID, ok := result.Metadata["child_session_id"].(string)
	if !ok || childSessionID == "" {
		t.Fatalf("metadata = %#v, want child session id", result.Metadata)
	}
	messages, err := store.Messages(context.Background(), childSessionID)
	if err != nil {
		t.Fatalf("Messages returned error: %v", err)
	}
	if len(messages) != 2 || messages[0].PlainText() != "inspect the subsystem" {
		t.Fatalf("child messages = %#v", messages)
	}
	if len(childModel.requests) != 1 || childModel.requests[0].ParentSessionID != "parent-session" {
		t.Fatalf("model request = %#v, want parent correlation", childModel.requests)
	}
}

func TestToolUsesRuntimeSessionStoreWhenAgentDoesNotSetOne(t *testing.T) {
	store := session.NewMemoryStore()
	delegate, err := NewTool(Config{
		Agents: []Agent{{
			Name:        "worker",
			Description: "Worker",
			Options: memaxagent.Options{
				Model: &fakeModel{turns: [][]model.StreamEvent{{{Kind: model.StreamText, Text: "ok"}}}},
			},
		}},
	})
	if err != nil {
		t.Fatalf("NewTool returned error: %v", err)
	}

	result, err := delegate.Execute(context.Background(), tool.Call{
		Use: model.ToolUse{
			Name:  delegate.Spec().Name,
			Input: json.RawMessage(`{"prompt":"use runtime store"}`),
		},
		Runtime: tool.Runtime{SessionID: "parent-session", Sessions: store},
	})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	childSessionID, _ := result.Metadata["child_session_id"].(string)
	if childSessionID == "" {
		t.Fatalf("metadata = %#v, want child session id", result.Metadata)
	}
	if _, err := store.Messages(context.Background(), childSessionID); err != nil {
		t.Fatalf("runtime store did not receive child session: %v", err)
	}
}

func TestToolScopesChildPlanAndHandlesResult(t *testing.T) {
	store := session.NewMemoryStore()
	childModel := &fakeModel{turns: [][]model.StreamEvent{{{Kind: model.StreamText, Text: "child done"}}}}
	handlerCalled := false
	delegate, err := NewTool(Config{
		Agents: []Agent{{
			Name: "worker",
			Options: memaxagent.Options{
				Model:    childModel,
				Sessions: store,
			},
		}},
		PlanSource: PlanSourceFunc(func(_ context.Context, req PlanRequest) (planner.Plan, error) {
			if req.TaskID != "task-1" {
				t.Fatalf("task id = %q, want task-1", req.TaskID)
			}
			return planner.Plan{
				Goal: "scoped child goal",
				Steps: []planner.Step{{
					ID:     "task-1",
					Title:  "scoped task",
					Status: planner.StatusInProgress,
				}},
			}, nil
		}),
		ResultHandler: ResultHandlerFunc(func(_ context.Context, req ResultRequest) (map[string]any, error) {
			handlerCalled = true
			if req.TaskID != "task-1" || req.Result != "child done" {
				t.Fatalf("result request = %#v, want task and child result", req)
			}
			return map[string]any{model.MetadataTaskStatus: "completed"}, nil
		}),
	})
	if err != nil {
		t.Fatalf("NewTool returned error: %v", err)
	}

	result, err := delegate.Execute(context.Background(), tool.Call{
		Use: model.ToolUse{
			ID:    "delegate-1",
			Name:  delegate.Spec().Name,
			Input: json.RawMessage(`{"prompt":"run scoped work","task_id":"task-1"}`),
		},
		Runtime: tool.Runtime{SessionID: "parent-session", Sessions: store},
	})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if !handlerCalled {
		t.Fatal("result handler was not called")
	}
	if result.Metadata[model.MetadataTaskID] != "task-1" || result.Metadata[model.MetadataTaskStatus] != "completed" {
		t.Fatalf("metadata = %#v, want task metadata", result.Metadata)
	}
	if len(childModel.requests) != 1 || !strings.Contains(childModel.requests[0].SystemPrompt, "scoped child goal") {
		t.Fatalf("child request = %#v, want scoped plan prompt", childModel.requests)
	}
}

func TestToolReportsChildAgentErrorAsToolResult(t *testing.T) {
	delegate, err := NewTool(Config{
		Agents: []Agent{{
			Name:        "worker",
			Description: "Worker",
			Options: memaxagent.Options{
				Model: &fakeModel{err: errors.New("model unavailable")},
			},
		}},
	})
	if err != nil {
		t.Fatalf("NewTool returned error: %v", err)
	}

	result, err := delegate.Execute(context.Background(), tool.Call{
		Use: model.ToolUse{
			Name:  delegate.Spec().Name,
			Input: json.RawMessage(`{"prompt":"run"}`),
		},
		Runtime: tool.Runtime{SessionID: "parent-session", Sessions: session.NewMemoryStore()},
	})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if !result.IsError {
		t.Fatalf("result = %#v, want error result", result)
	}
	if result.Metadata["agent"] != "worker" {
		t.Fatalf("metadata = %#v, want agent", result.Metadata)
	}
}

func TestNewToolRejectsDuplicateAgents(t *testing.T) {
	_, err := NewTool(Config{
		Agents: []Agent{
			{Name: "worker"},
			{Name: "worker"},
		},
	})
	if err == nil {
		t.Fatal("NewTool returned nil, want duplicate error")
	}
}

type fakeModel struct {
	requests []model.Request
	turns    [][]model.StreamEvent
	err      error
}

func (m *fakeModel) Stream(_ context.Context, req model.Request) (model.Stream, error) {
	m.requests = append(m.requests, req)
	if m.err != nil {
		return nil, m.err
	}
	if len(m.turns) == 0 {
		return &fakeStream{}, nil
	}
	events := m.turns[0]
	m.turns = m.turns[1:]
	return &fakeStream{events: events}, nil
}

type fakeStream struct {
	events []model.StreamEvent
	index  int
}

func (s *fakeStream) Recv() (model.StreamEvent, error) {
	if s.index >= len(s.events) {
		return model.StreamEvent{}, model.ErrEndOfStream
	}
	event := s.events[s.index]
	s.index++
	return event, nil
}

func (s *fakeStream) Close() error {
	return nil
}
