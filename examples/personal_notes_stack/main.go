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
	"github.com/MemaxLabs/memax-go-agent-sdk/model"
	"github.com/MemaxLabs/memax-go-agent-sdk/notes"
	"github.com/MemaxLabs/memax-go-agent-sdk/stack/personal"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/approvaltools"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/notetools"
	"github.com/MemaxLabs/memax-go-agent-sdk/toolkit/tasktools"
)

func main() {
	if err := runExample(context.Background(), os.Stdout); err != nil {
		log.Fatal(err)
	}
}

// runExample walks through a note-first personal_assistant flow. The scripted
// model searches note metadata, reads one seeded note, and only then saves a
// reusable template that matches the recalled note content.
func runExample(ctx context.Context, w io.Writer) error {
	noteStore := notes.NewNoteStore([]notes.Note{{
		ID:      "note-1",
		Title:   "meeting brief style",
		Kind:    "brief",
		Summary: "Reusable style for concise meeting briefs",
		Content: "Use one short summary paragraph followed by owner and due-date bullets.",
		Tags:    []string{"meeting", "brief"},
	}})
	tasks := tasktools.NewMemoryStore([]tasktools.Task{{
		ID:     "task-1",
		Title:  "Capture reusable meeting template",
		Status: tasktools.StatusInProgress,
		Notes:  "search notes first, load the relevant note, then save a reusable template through approval",
	}})

	config := personal.PersonalAssistant()
	config.Notes = notetools.Config{
		Searcher:     noteStore,
		Reader:       noteStore,
		Writer:       noteStore,
		DefaultLimit: 3,
	}
	config.Tasks = tasks
	config.Approval.Approver = approvaltools.StaticApprover{
		Decision: approvaltools.Decision{
			Approved: true,
			Reason:   "approved reusable note template",
		},
	}

	stack, err := personal.New(config)
	if err != nil {
		return err
	}

	events, err := memaxagent.Query(ctx, "Capture a reusable meeting follow-up template, but search notes first and reuse the existing style before saving anything.", stack.WithModel(&personalNotesModel{}))
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

type personalNotesModel struct {
	turn      int
	saveInput map[string]any
}

func (m *personalNotesModel) Stream(_ context.Context, req model.Request) (model.Stream, error) {
	m.turn++
	switch m.turn {
	case 1:
		return newStream(toolUse("search-1", notetools.SearchToolName, map[string]any{
			"query": "meeting brief style owner due date",
			"limit": 3,
		})), nil
	case 2:
		return newStream(toolUse("read-1", notetools.ReadToolName, map[string]any{
			"id": "note-1",
		})), nil
	case 3:
		content := "Meeting follow-ups can stay informal and open-ended."
		if requestContains(req, "owner and due-date bullets") {
			content = "Use one short summary paragraph followed by owner and due-date bullets for every follow-up."
		}
		m.saveInput = map[string]any{
			"title":   "meeting follow-up template",
			"kind":    "template",
			"summary": "Reusable action-oriented follow-up template",
			"content": content,
		}
		return newStream(toolUse("approval-1", approvaltools.ToolName, map[string]any{
			"action":     notetools.SaveToolName,
			"reason":     "saving a reusable personal note template requires approval",
			"tool_input": m.saveInput,
		})), nil
	case 4:
		return newStream(toolUse("save-1", notetools.SaveToolName, m.saveInput)), nil
	case 5:
		return newStream(toolUse("search-2", notetools.SearchToolName, map[string]any{
			"query": "meeting follow-up template owner due-date bullets",
			"limit": 3,
		})), nil
	default:
		text := "Saved a reusable meeting template."
		if content, _ := m.saveInput["content"].(string); strings.Contains(content, "owner and due-date bullets") {
			text = "Recalled the existing note style, saved a matching reusable template, and confirmed it is now searchable."
		}
		return newStream(model.StreamEvent{Kind: model.StreamText, Text: text}), nil
	}
}

func requestContains(req model.Request, needle string) bool {
	for _, msg := range req.Messages {
		if strings.Contains(msg.PlainText(), needle) {
			return true
		}
		if msg.ToolResult != nil && strings.Contains(msg.ToolResult.Content, needle) {
			return true
		}
	}
	return strings.Contains(req.SystemPrompt, needle)
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
