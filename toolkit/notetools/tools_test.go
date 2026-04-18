package notetools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/MemaxLabs/memax-go-agent-sdk/model"
	"github.com/MemaxLabs/memax-go-agent-sdk/notes"
	"github.com/MemaxLabs/memax-go-agent-sdk/tool"
)

func TestNewToolsRequiresCapability(t *testing.T) {
	t.Parallel()

	if _, err := NewTools(Config{}); err == nil {
		t.Fatal("NewTools() returned nil error without configured capabilities")
	}
}

func TestSearchAndReadToolsAreProgressive(t *testing.T) {
	t.Parallel()

	store := notes.NewNoteStore([]notes.Note{{
		ID:      "note-1",
		Title:   "meeting follow-up preference",
		Kind:    "preference",
		Summary: "Reusable follow-up style",
		Content: "List owners and due dates in an action-oriented format.",
		Tags:    []string{"meeting", "follow-up"},
	}})
	searchTool, err := NewSearchTool(Config{Searcher: store})
	if err != nil {
		t.Fatalf("NewSearchTool() error = %v", err)
	}
	readTool, err := NewReadTool(Config{Reader: store})
	if err != nil {
		t.Fatalf("NewReadTool() error = %v", err)
	}

	searchResult := runTool(t, searchTool, SearchToolName, map[string]any{
		"query": "action-oriented follow-up",
		"limit": 3,
	})
	if searchResult.IsError {
		t.Fatalf("search result = %#v", searchResult)
	}
	if !strings.Contains(searchResult.Content, "meeting follow-up preference") {
		t.Fatalf("search content = %q, want title", searchResult.Content)
	}
	if strings.Contains(searchResult.Content, "List owners and due dates") {
		t.Fatalf("search content leaked full note content: %q", searchResult.Content)
	}

	readResult := runTool(t, readTool, ReadToolName, map[string]any{"id": "note-1"})
	if readResult.IsError {
		t.Fatalf("read result = %#v", readResult)
	}
	if !strings.Contains(readResult.Content, "List owners and due dates in an action-oriented format.") {
		t.Fatalf("read content = %q, want full note content", readResult.Content)
	}
}

func TestSaveAndDeleteTools(t *testing.T) {
	t.Parallel()

	store := notes.NewNoteStore(nil)
	saveTool, err := NewSaveTool(Config{Writer: store})
	if err != nil {
		t.Fatalf("NewSaveTool() error = %v", err)
	}
	deleteTool, err := NewDeleteTool(Config{Deleter: store})
	if err != nil {
		t.Fatalf("NewDeleteTool() error = %v", err)
	}

	saveResult := runTool(t, saveTool, SaveToolName, map[string]any{
		"title":   "travel checklist",
		"kind":    "checklist",
		"summary": "Trip preparation checklist",
		"content": "Passport, charger, and medication.",
		"tags":    []string{"travel"},
	})
	if saveResult.IsError {
		t.Fatalf("save result = %#v", saveResult)
	}
	if saveResult.Metadata["note_id"] == "" {
		t.Fatalf("save metadata = %#v, want note_id", saveResult.Metadata)
	}

	deleteResult := runTool(t, deleteTool, DeleteToolName, map[string]any{
		"id": saveResult.Metadata["note_id"],
	})
	if deleteResult.IsError {
		t.Fatalf("delete result = %#v", deleteResult)
	}
}

func runTool(t *testing.T, toolImpl tool.Tool, name string, input map[string]any) model.ToolResult {
	t.Helper()
	registry := tool.NewRegistry(toolImpl)
	exec := tool.Executor{Registry: registry}

	payload, err := json.Marshal(input)
	if err != nil {
		t.Fatalf("Marshal(%s) error = %v", name, err)
	}
	results := exec.Run(context.Background(), []model.ToolUse{{
		ID:    name + "-1",
		Name:  name,
		Input: payload,
	}})
	var out []model.ToolResult
	for item := range results {
		out = append(out, item)
	}
	if len(out) != 1 {
		t.Fatalf("Run(%s) results = %d, want 1", name, len(out))
	}
	return out[0]
}
