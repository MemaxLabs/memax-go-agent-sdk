package scheduletools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/MemaxLabs/memax-go-agent-sdk/model"
	"github.com/MemaxLabs/memax-go-agent-sdk/scheduling"
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

	start := time.Date(2026, 4, 20, 15, 0, 0, 0, time.UTC)
	store := scheduling.NewEventStore([]scheduling.Event{{
		ID:       "event-1",
		Title:    "Project kickoff",
		Summary:  "Weekly kickoff with owners and due dates",
		Location: "Zoom",
		Organizer: scheduling.Participant{
			Name: "Alex",
		},
		Start:       start,
		End:         start.Add(time.Hour),
		TimeZone:    "UTC",
		Description: "Discuss detailed staffing risk matrix and approval path.",
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
		"query": "kickoff owners due dates",
		"limit": 3,
	})
	if searchResult.IsError {
		t.Fatalf("search result = %#v", searchResult)
	}
	if !strings.Contains(searchResult.Content, "Project kickoff") {
		t.Fatalf("search content = %q, want title", searchResult.Content)
	}
	if strings.Contains(searchResult.Content, "staffing risk matrix") {
		t.Fatalf("search content leaked full event description: %q", searchResult.Content)
	}

	readResult := runTool(t, readTool, ReadToolName, map[string]any{"id": "event-1"})
	if readResult.IsError {
		t.Fatalf("read result = %#v", readResult)
	}
	if !strings.Contains(readResult.Content, "Discuss detailed staffing risk matrix and approval path.") {
		t.Fatalf("read content = %q, want full event description", readResult.Content)
	}
}

func TestCreateRescheduleAndCancelTools(t *testing.T) {
	t.Parallel()

	start := time.Date(2026, 4, 20, 15, 0, 0, 0, time.UTC)
	store := scheduling.NewEventStore(nil)
	createTool, err := NewCreateTool(Config{Creator: store})
	if err != nil {
		t.Fatalf("NewCreateTool() error = %v", err)
	}
	rescheduleTool, err := NewRescheduleTool(Config{Rescheduler: store})
	if err != nil {
		t.Fatalf("NewRescheduleTool() error = %v", err)
	}
	cancelTool, err := NewCancelTool(Config{Canceller: store})
	if err != nil {
		t.Fatalf("NewCancelTool() error = %v", err)
	}

	createResult := runTool(t, createTool, CreateToolName, map[string]any{
		"title":       "Design review",
		"summary":     "Review updated design with PM",
		"description": "Discuss detailed staffing risk matrix and approval path.",
		"location":    "Room 2",
		"organizer": map[string]any{
			"name": "Taylor",
		},
		"start":     start.Format(time.RFC3339),
		"end":       start.Add(time.Hour).Format(time.RFC3339),
		"time_zone": "UTC",
	})
	if createResult.IsError {
		t.Fatalf("create result = %#v", createResult)
	}
	eventID, _ := createResult.Metadata["event_id"].(string)
	if eventID == "" {
		t.Fatalf("create metadata = %#v, want event_id", createResult.Metadata)
	}

	rescheduleResult := runTool(t, rescheduleTool, RescheduleToolName, map[string]any{
		"id":        eventID,
		"start":     start.Add(2 * time.Hour).Format(time.RFC3339),
		"end":       start.Add(3 * time.Hour).Format(time.RFC3339),
		"time_zone": "America/Los_Angeles",
	})
	if rescheduleResult.IsError {
		t.Fatalf("reschedule result = %#v", rescheduleResult)
	}
	if rescheduleResult.Metadata["event_time_zone"] != "America/Los_Angeles" {
		t.Fatalf("reschedule metadata = %#v, want updated time zone", rescheduleResult.Metadata)
	}
	if got := rescheduleResult.Metadata["previous_event_time_zone"]; got != "UTC" {
		t.Fatalf("reschedule metadata = %#v, want previous time zone UTC", rescheduleResult.Metadata)
	}
	if got := rescheduleResult.Metadata["previous_event_start"]; got != start.Format(time.RFC3339Nano) {
		t.Fatalf("reschedule metadata = %#v, want previous start %q", rescheduleResult.Metadata, start.Format(time.RFC3339Nano))
	}

	cancelResult := runTool(t, cancelTool, CancelToolName, map[string]any{
		"id":     eventID,
		"reason": "conflict with customer call",
	})
	if cancelResult.IsError {
		t.Fatalf("cancel result = %#v", cancelResult)
	}
	if cancelResult.Metadata["event_status"] != string(scheduling.StatusCancelled) {
		t.Fatalf("cancel metadata = %#v, want cancelled status", cancelResult.Metadata)
	}
	if got := cancelResult.Metadata["previous_event_status"]; got != string(scheduling.StatusScheduled) {
		t.Fatalf("cancel metadata = %#v, want previous scheduled status", cancelResult.Metadata)
	}
}

func TestRescheduleToolOmitsOptionalPreviousMetadataForZeroPrevious(t *testing.T) {
	t.Parallel()

	start := time.Date(2026, 4, 20, 15, 0, 0, 0, time.UTC)
	rescheduleTool, err := NewRescheduleTool(Config{
		Rescheduler: scheduling.ReschedulerFunc(func(context.Context, scheduling.RescheduleRequest) (scheduling.RescheduleResult, error) {
			return scheduling.RescheduleResult{
				Event: scheduling.Event{
					ID:       "event-1",
					Title:    "Project kickoff",
					Start:    start.Add(2 * time.Hour),
					End:      start.Add(3 * time.Hour),
					TimeZone: "America/Los_Angeles",
					Status:   scheduling.StatusScheduled,
				},
			}, nil
		}),
	})
	if err != nil {
		t.Fatalf("NewRescheduleTool() error = %v", err)
	}

	result := runTool(t, rescheduleTool, RescheduleToolName, map[string]any{
		"id":        "event-1",
		"start":     start.Add(2 * time.Hour).Format(time.RFC3339),
		"end":       start.Add(3 * time.Hour).Format(time.RFC3339),
		"time_zone": "America/Los_Angeles",
	})
	if result.IsError {
		t.Fatalf("reschedule result = %#v", result)
	}
	if got := result.Metadata["previous_event_id"]; got != "" {
		t.Fatalf("metadata = %#v, want empty previous_event_id", result.Metadata)
	}
	if attendees, ok := result.Metadata["previous_event_attendees"].([]string); !ok || len(attendees) != 0 {
		t.Fatalf("metadata = %#v, want empty previous_event_attendees", result.Metadata)
	}
	for _, key := range []string{"previous_event_start", "previous_event_end", "previous_event_organizer"} {
		if _, ok := result.Metadata[key]; ok {
			t.Fatalf("metadata = %#v, want %s omitted for zero previous event", result.Metadata, key)
		}
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
