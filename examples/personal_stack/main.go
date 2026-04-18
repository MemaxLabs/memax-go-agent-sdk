package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"strings"

	memaxagent "github.com/MemaxLabs/memax-go-agent-sdk"
	"github.com/MemaxLabs/memax-go-agent-sdk/memory"
	"github.com/MemaxLabs/memax-go-agent-sdk/model"
	"github.com/MemaxLabs/memax-go-agent-sdk/stack/personal"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/approvaltools"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/memorytools"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/tasktools"
)

func main() {
	if err := runExample(context.Background(), os.Stdout); err != nil {
		log.Fatal(err)
	}
}

// runExample is a human-oriented walkthrough of the personal_assistant preset.
// The authoritative behavioral spec lives in the deterministic eval scenarios,
// especially personal_preset_personal_assistant and
// personal_preset_personal_assistant_memory_approval_recovery.
func runExample(ctx context.Context, w io.Writer) error {
	memoryStore := memory.NewMemoryStore([]memory.Memory{{
		ID:      "memory-1",
		Name:    "meeting-style",
		Scope:   memory.ScopeUser,
		Content: "Meeting follow-ups should stay action-oriented and easy to skim.",
	}})
	tasks := tasktools.NewMemoryStore([]tasktools.Task{{
		ID:     "task-1",
		Title:  "Capture durable follow-up preference",
		Status: tasktools.StatusInProgress,
		Notes:  "recall existing preference first, then save the durable follow-up rule through the approval flow",
	}})

	config := personal.PersonalAssistant()
	config.Memory = memorytools.Config{
		Source:       memoryStore,
		Writer:       memoryStore,
		DefaultLimit: 3,
	}
	config.Tasks = tasks
	config.Approval.Approver = approvaltools.StaticApprover{
		Decision: approvaltools.Decision{
			Approved: true,
			Reason:   "approved personal memory update",
		},
	}

	stack, err := personal.New(config)
	if err != nil {
		return err
	}

	events, err := memaxagent.Query(ctx, "Capture the user's follow-up preference and make sure it fits the recalled personal style.", stack.WithModel(&personalModel{}))
	if err != nil {
		return err
	}

	for event := range events {
		switch event.Kind {
		case memaxagent.EventToolUse:
			fmt.Fprintf(w, "tool use: %s\n", event.ToolUse.Name)
		case memaxagent.EventApprovalRequested:
			fmt.Fprintf(w, "approval requested: %s\n", event.Approval.Action)
		case memaxagent.EventApprovalGranted:
			fmt.Fprintf(w, "approval granted: %s\n", event.Approval.Action)
		case memaxagent.EventApprovalConsumed:
			fmt.Fprintf(w, "approval consumed: %s\n", event.Approval.Action)
		case memaxagent.EventToolResult:
			fmt.Fprintf(w, "tool result: %s\n", event.ToolResult.Content)
		case memaxagent.EventResult:
			fmt.Fprintf(w, "result: %s\n", event.Result)
		case memaxagent.EventError:
			return event.Err
		}
	}
	return nil
}

type personalModel struct {
	turn      int
	guided    bool
	saveInput map[string]any
}

func (m *personalModel) Stream(_ context.Context, req model.Request) (model.Stream, error) {
	m.turn++
	switch m.turn {
	case 1:
		m.guided = strings.Contains(req.SystemPrompt, "Meeting follow-ups should stay action-oriented and easy to skim.")
		saveContent := "Meeting follow-ups should list owners and due dates in an action-oriented format."
		if !m.guided {
			saveContent = "Meeting follow-ups can be informal and open-ended."
		}
		m.saveInput = map[string]any{
			"name":    "meeting-follow-up-format",
			"scope":   "user",
			"content": saveContent,
		}
		return newStream(toolUse("tool-1", approvaltools.ToolName, map[string]any{
			"action":     memorytools.SaveToolName,
			"reason":     "saving a durable personal follow-up preference requires approval",
			"tool_input": m.saveInput,
			"summary": map[string]any{
				"title":       "Review durable meeting follow-up preference",
				"description": "Persist a reusable personal rule for meeting follow-ups",
				"risk":        "updates long-lived personal context",
				"changes":     1,
			},
		})), nil
	case 2:
		return newStream(toolUse("tool-2", memorytools.SaveToolName, m.saveInput)), nil
	case 3:
		return newStream(toolUse("tool-3", memorytools.SearchToolName, map[string]any{
			"query": "action-oriented follow-up owners due dates",
			"limit": 3,
		})), nil
	default:
		text := "Saved a durable follow-up preference."
		if m.guided {
			text = "Recalled the user's action-oriented style, saved a matching durable follow-up preference, and confirmed it is now searchable."
		}
		return newStream(model.StreamEvent{
			Kind: model.StreamText,
			Text: text,
		}), nil
	}
}

func toolUse(id string, name string, input map[string]any) model.StreamEvent {
	return model.StreamEvent{
		Kind: model.StreamToolUse,
		ToolUse: model.ToolUse{
			ID:    id,
			Name:  name,
			Input: mustJSON(input),
		},
	}
}

func mustJSON(v any) json.RawMessage {
	data, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return data
}

type stream struct {
	events []model.StreamEvent
	index  int
}

func newStream(events ...model.StreamEvent) *stream {
	return &stream{events: events}
}

func (s *stream) Recv() (model.StreamEvent, error) {
	if s.index >= len(s.events) {
		return model.StreamEvent{}, model.ErrEndOfStream
	}
	event := s.events[s.index]
	s.index++
	return event, nil
}

func (s *stream) Close() error {
	return nil
}
