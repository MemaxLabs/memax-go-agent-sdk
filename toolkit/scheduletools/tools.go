// Package scheduletools exposes host-owned scheduling tools.
package scheduletools

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/MemaxLabs/memax-go-agent-sdk/model"
	"github.com/MemaxLabs/memax-go-agent-sdk/scheduling"
	"github.com/MemaxLabs/memax-go-agent-sdk/tool"
)

const (
	SearchToolName     = "search_schedule_events"
	ReadToolName       = "read_schedule_event"
	CreateToolName     = "create_schedule_event"
	RescheduleToolName = "reschedule_schedule_event"
	CancelToolName     = "cancel_schedule_event"

	defaultSearchLimit          = 8
	defaultSearchToolMaxBytes   = 64 * 1024
	defaultReadToolMaxBytes     = 64 * 1024
	defaultMutationToolMaxBytes = 16 * 1024
)

// Config controls the scheduling tools exposed for one backend.
type Config struct {
	Searcher       scheduling.Searcher
	Reader         scheduling.Reader
	Creator        scheduling.Creator
	Rescheduler    scheduling.Rescheduler
	Canceller      scheduling.Canceller
	SearchName     string
	ReadName       string
	CreateName     string
	RescheduleName string
	CancelName     string
	DefaultLimit   int
	MaxResultBytes int
}

// NewTools returns tools for the configured scheduling capabilities.
func NewTools(config Config) ([]tool.Tool, error) {
	var tools []tool.Tool
	if config.Searcher != nil {
		search, err := NewSearchTool(config)
		if err != nil {
			return nil, err
		}
		tools = append(tools, search)
	}
	if config.Reader != nil {
		read, err := NewReadTool(config)
		if err != nil {
			return nil, err
		}
		tools = append(tools, read)
	}
	if config.Creator != nil {
		create, err := NewCreateTool(config)
		if err != nil {
			return nil, err
		}
		tools = append(tools, create)
	}
	if config.Rescheduler != nil {
		reschedule, err := NewRescheduleTool(config)
		if err != nil {
			return nil, err
		}
		tools = append(tools, reschedule)
	}
	if config.Canceller != nil {
		cancel, err := NewCancelTool(config)
		if err != nil {
			return nil, err
		}
		tools = append(tools, cancel)
	}
	if len(tools) == 0 {
		return nil, fmt.Errorf("scheduletools: at least one searcher, reader, creator, rescheduler, or canceller is required")
	}
	return tools, nil
}

// NewSearchTool returns a metadata-only schedule search tool.
func NewSearchTool(config Config) (tool.Tool, error) {
	if config.Searcher == nil {
		return nil, fmt.Errorf("scheduletools: searcher is required")
	}
	name := config.SearchName
	if name == "" {
		name = SearchToolName
	}
	limit := config.DefaultLimit
	if limit <= 0 {
		limit = defaultSearchLimit
	}
	maxResultBytes := config.MaxResultBytes
	if maxResultBytes <= 0 {
		maxResultBytes = defaultSearchToolMaxBytes
	}
	return tool.Definition{
		ToolSpec: model.ToolSpec{
			Name:            name,
			Description:     "Search host-owned schedule and calendar event metadata by title, attendees, time window, and summaries without loading full event descriptions.",
			SearchHint:      "search schedule calendar events meetings appointments metadata time window attendees",
			ReadOnly:        true,
			ConcurrencySafe: true,
			AlwaysLoad:      true,
			MaxResultBytes:  maxResultBytes,
			InputSchema:     searchInputSchema(),
		},
		Handler: func(ctx context.Context, call tool.Call) (model.ToolResult, error) {
			input, err := tool.DecodeInput[searchInput](call.Use)
			if err != nil {
				return model.ToolResult{}, err
			}
			windowStart, err := parseOptionalTime(input.Start)
			if err != nil {
				return model.ToolResult{}, fmt.Errorf("scheduletools: start: %w", err)
			}
			windowEnd, err := parseOptionalTime(input.End)
			if err != nil {
				return model.ToolResult{}, fmt.Errorf("scheduletools: end: %w", err)
			}
			selectedLimit := input.Limit
			if selectedLimit <= 0 {
				selectedLimit = limit
			}
			items, err := config.Searcher.SearchEvents(ctx, scheduling.SearchRequest{
				SessionID:       call.Runtime.SessionID,
				ParentSessionID: call.Runtime.ParentSessionID,
				Identity:        call.Runtime.Identity,
				Query:           input.Query,
				WindowStart:     windowStart,
				WindowEnd:       windowEnd,
				Limit:           selectedLimit,
			})
			if err != nil {
				return model.ToolResult{}, err
			}
			return model.ToolResult{
				Content: formatSearchResults(items),
				Metadata: map[string]any{
					"query":   input.Query,
					"matches": len(items),
				},
			}, nil
		},
	}, nil
}

// NewReadTool returns a full-event read tool.
func NewReadTool(config Config) (tool.Tool, error) {
	if config.Reader == nil {
		return nil, fmt.Errorf("scheduletools: reader is required")
	}
	name := config.ReadName
	if name == "" {
		name = ReadToolName
	}
	maxResultBytes := config.MaxResultBytes
	if maxResultBytes <= 0 {
		maxResultBytes = defaultReadToolMaxBytes
	}
	return tool.Definition{
		ToolSpec: model.ToolSpec{
			Name:            name,
			Description:     "Read one host-owned schedule or calendar event by ID or title, including full description and typed timing details.",
			SearchHint:      "read schedule calendar event meeting appointment full details by id title",
			ReadOnly:        true,
			ConcurrencySafe: true,
			AlwaysLoad:      true,
			MaxResultBytes:  maxResultBytes,
			InputSchema:     readInputSchema(),
		},
		Handler: func(ctx context.Context, call tool.Call) (model.ToolResult, error) {
			input, err := tool.DecodeInput[readInput](call.Use)
			if err != nil {
				return model.ToolResult{}, err
			}
			item, err := config.Reader.ReadEvent(ctx, scheduling.ReadRequest{
				SessionID:       call.Runtime.SessionID,
				ParentSessionID: call.Runtime.ParentSessionID,
				Identity:        call.Runtime.Identity,
				ID:              strings.TrimSpace(input.ID),
				Title:           strings.TrimSpace(input.Title),
			})
			if err != nil {
				return model.ToolResult{}, err
			}
			metadata := eventMetadata(item)
			metadata["description_bytes"] = len(item.Description)
			return model.ToolResult{
				Content:  formatEvent(item),
				Metadata: metadata,
			}, nil
		},
	}, nil
}

// NewCreateTool returns an event-creation tool.
func NewCreateTool(config Config) (tool.Tool, error) {
	if config.Creator == nil {
		return nil, fmt.Errorf("scheduletools: creator is required")
	}
	name := config.CreateName
	if name == "" {
		name = CreateToolName
	}
	maxResultBytes := config.MaxResultBytes
	if maxResultBytes <= 0 {
		maxResultBytes = defaultMutationToolMaxBytes
	}
	return tool.Definition{
		ToolSpec: model.ToolSpec{
			Name:           name,
			Description:    "Create a host-owned schedule or calendar event when the user or host policy allows it.",
			SearchHint:     "create schedule calendar event meeting appointment",
			Destructive:    true,
			MaxResultBytes: maxResultBytes,
			InputSchema:    createInputSchema(),
		},
		Handler: func(ctx context.Context, call tool.Call) (model.ToolResult, error) {
			input, err := tool.DecodeInput[createInput](call.Use)
			if err != nil {
				return model.ToolResult{}, err
			}
			item, err := input.event()
			if err != nil {
				return model.ToolResult{}, err
			}
			result, err := config.Creator.CreateEvent(ctx, scheduling.CreateRequest{
				SessionID:       call.Runtime.SessionID,
				ParentSessionID: call.Runtime.ParentSessionID,
				Identity:        call.Runtime.Identity,
				Event:           item,
			})
			if err != nil {
				return model.ToolResult{}, err
			}
			metadata := eventMetadata(result.Event)
			metadata["action"] = "created"
			return model.ToolResult{
				Content:  fmt.Sprintf("created schedule event %s", eventLabel(result.Event)),
				Metadata: metadata,
			}, nil
		},
	}, nil
}

// NewRescheduleTool returns an event-reschedule tool.
func NewRescheduleTool(config Config) (tool.Tool, error) {
	if config.Rescheduler == nil {
		return nil, fmt.Errorf("scheduletools: rescheduler is required")
	}
	name := config.RescheduleName
	if name == "" {
		name = RescheduleToolName
	}
	maxResultBytes := config.MaxResultBytes
	if maxResultBytes <= 0 {
		maxResultBytes = defaultMutationToolMaxBytes
	}
	return tool.Definition{
		ToolSpec: model.ToolSpec{
			Name:           name,
			Description:    "Reschedule an existing host-owned schedule or calendar event when the user or host policy allows it.",
			SearchHint:     "reschedule move calendar event meeting appointment",
			Destructive:    true,
			MaxResultBytes: maxResultBytes,
			InputSchema:    rescheduleInputSchema(),
		},
		Handler: func(ctx context.Context, call tool.Call) (model.ToolResult, error) {
			input, err := tool.DecodeInput[rescheduleInput](call.Use)
			if err != nil {
				return model.ToolResult{}, err
			}
			start, err := requireTime(input.Start)
			if err != nil {
				return model.ToolResult{}, fmt.Errorf("scheduletools: start: %w", err)
			}
			end, err := requireTime(input.End)
			if err != nil {
				return model.ToolResult{}, fmt.Errorf("scheduletools: end: %w", err)
			}
			result, err := config.Rescheduler.RescheduleEvent(ctx, scheduling.RescheduleRequest{
				SessionID:       call.Runtime.SessionID,
				ParentSessionID: call.Runtime.ParentSessionID,
				Identity:        call.Runtime.Identity,
				ID:              strings.TrimSpace(input.ID),
				Title:           strings.TrimSpace(input.Title),
				Start:           start,
				End:             end,
				TimeZone:        strings.TrimSpace(input.TimeZone),
				Metadata:        model.CloneMetadata(input.Metadata),
			})
			if err != nil {
				return model.ToolResult{}, err
			}
			metadata := eventMetadata(result.Event)
			addPreviousEventMetadata(metadata, result.Previous)
			metadata["action"] = "rescheduled"
			return model.ToolResult{
				Content:  fmt.Sprintf("rescheduled schedule event %s", eventLabel(result.Event)),
				Metadata: metadata,
			}, nil
		},
	}, nil
}

// NewCancelTool returns an event-cancel tool.
func NewCancelTool(config Config) (tool.Tool, error) {
	if config.Canceller == nil {
		return nil, fmt.Errorf("scheduletools: canceller is required")
	}
	name := config.CancelName
	if name == "" {
		name = CancelToolName
	}
	maxResultBytes := config.MaxResultBytes
	if maxResultBytes <= 0 {
		maxResultBytes = defaultMutationToolMaxBytes
	}
	return tool.Definition{
		ToolSpec: model.ToolSpec{
			Name:           name,
			Description:    "Cancel an existing host-owned schedule or calendar event when the user or host policy allows it.",
			SearchHint:     "cancel schedule calendar event meeting appointment",
			Destructive:    true,
			MaxResultBytes: maxResultBytes,
			InputSchema:    cancelInputSchema(),
		},
		Handler: func(ctx context.Context, call tool.Call) (model.ToolResult, error) {
			input, err := tool.DecodeInput[cancelInput](call.Use)
			if err != nil {
				return model.ToolResult{}, err
			}
			result, err := config.Canceller.CancelEvent(ctx, scheduling.CancelRequest{
				SessionID:       call.Runtime.SessionID,
				ParentSessionID: call.Runtime.ParentSessionID,
				Identity:        call.Runtime.Identity,
				ID:              strings.TrimSpace(input.ID),
				Title:           strings.TrimSpace(input.Title),
				Reason:          strings.TrimSpace(input.Reason),
				Metadata:        model.CloneMetadata(input.Metadata),
			})
			if err != nil {
				return model.ToolResult{}, err
			}
			metadata := eventMetadata(result.Event)
			addPreviousEventMetadata(metadata, result.Previous)
			metadata["action"] = "cancelled"
			return model.ToolResult{
				Content:  fmt.Sprintf("cancelled schedule event %s", eventLabel(result.Event)),
				Metadata: metadata,
			}, nil
		},
	}, nil
}

type searchInput struct {
	Query string `json:"query"`
	Start string `json:"start"`
	End   string `json:"end"`
	Limit int    `json:"limit"`
}

type readInput struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

type createInput struct {
	ID          string            `json:"id"`
	Title       string            `json:"title"`
	Summary     string            `json:"summary"`
	Description string            `json:"description"`
	Location    string            `json:"location"`
	Organizer   participantInput  `json:"organizer"`
	Attendees   participantInputs `json:"attendees"`
	Start       string            `json:"start"`
	End         string            `json:"end"`
	TimeZone    string            `json:"time_zone"`
	Tags        []string          `json:"tags"`
	Metadata    map[string]any    `json:"metadata"`
}

type rescheduleInput struct {
	ID       string         `json:"id"`
	Title    string         `json:"title"`
	Start    string         `json:"start"`
	End      string         `json:"end"`
	TimeZone string         `json:"time_zone"`
	Metadata map[string]any `json:"metadata"`
}

type cancelInput struct {
	ID       string         `json:"id"`
	Title    string         `json:"title"`
	Reason   string         `json:"reason"`
	Metadata map[string]any `json:"metadata"`
}

type participantInputs []participantInput

type participantInput struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Address string `json:"address"`
	Role    string `json:"role"`
}

func (p participantInputs) participants() []scheduling.Participant {
	if len(p) == 0 {
		return nil
	}
	out := make([]scheduling.Participant, len(p))
	for i, item := range p {
		out[i] = item.participant()
	}
	return out
}

func (p participantInput) participant() scheduling.Participant {
	return scheduling.Participant{
		ID:      strings.TrimSpace(p.ID),
		Name:    strings.TrimSpace(p.Name),
		Address: strings.TrimSpace(p.Address),
		Role:    strings.TrimSpace(p.Role),
	}
}

func (i createInput) event() (scheduling.Event, error) {
	start, err := requireTime(i.Start)
	if err != nil {
		return scheduling.Event{}, fmt.Errorf("scheduletools: start: %w", err)
	}
	end, err := requireTime(i.End)
	if err != nil {
		return scheduling.Event{}, fmt.Errorf("scheduletools: end: %w", err)
	}
	return scheduling.Event{
		ID:          strings.TrimSpace(i.ID),
		Title:       strings.TrimSpace(i.Title),
		Summary:     strings.TrimSpace(i.Summary),
		Description: strings.TrimSpace(i.Description),
		Location:    strings.TrimSpace(i.Location),
		Organizer:   i.Organizer.participant(),
		Attendees:   i.Attendees.participants(),
		Start:       start,
		End:         end,
		TimeZone:    strings.TrimSpace(i.TimeZone),
		Tags:        append([]string(nil), i.Tags...),
		Metadata:    model.CloneMetadata(i.Metadata),
	}, nil
}

func searchInputSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"query": map[string]any{"type": "string", "description": "Schedule-event search query."},
			"start": map[string]any{"type": "string", "description": "Optional RFC3339 window start."},
			"end":   map[string]any{"type": "string", "description": "Optional RFC3339 window end."},
			"limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 50, "description": "Maximum events to return."},
		},
	}
}

func readInputSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"id":    map[string]any{"type": "string", "description": "Exact event ID to load."},
			"title": map[string]any{"type": "string", "description": "Exact event title to load when the ID is unknown."},
		},
	}
}

func createInputSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"required":             []any{"title", "start", "end"},
		"additionalProperties": false,
		"properties": map[string]any{
			"id":          map[string]any{"type": "string"},
			"title":       map[string]any{"type": "string", "minLength": 1},
			"summary":     map[string]any{"type": "string"},
			"description": map[string]any{"type": "string"},
			"location":    map[string]any{"type": "string"},
			"organizer":   participantSchema("Optional organizer metadata."),
			"attendees": map[string]any{
				"type":        "array",
				"description": "Optional attendees for the event.",
				"items":       participantSchema("Attendee details."),
			},
			"start":     map[string]any{"type": "string", "description": "RFC3339 start time."},
			"end":       map[string]any{"type": "string", "description": "RFC3339 end time."},
			"time_zone": map[string]any{"type": "string", "description": "Optional IANA time zone such as America/Los_Angeles."},
			"tags": map[string]any{
				"type":  "array",
				"items": map[string]any{"type": "string"},
			},
			"metadata": map[string]any{"type": "object", "additionalProperties": true},
		},
	}
}

func rescheduleInputSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"required":             []any{"start", "end"},
		"additionalProperties": false,
		"properties": map[string]any{
			"id":        map[string]any{"type": "string"},
			"title":     map[string]any{"type": "string"},
			"start":     map[string]any{"type": "string", "description": "RFC3339 replacement start time."},
			"end":       map[string]any{"type": "string", "description": "RFC3339 replacement end time."},
			"time_zone": map[string]any{"type": "string", "description": "Optional replacement IANA time zone."},
			"metadata":  map[string]any{"type": "object", "additionalProperties": true},
		},
	}
}

func cancelInputSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"id":       map[string]any{"type": "string"},
			"title":    map[string]any{"type": "string"},
			"reason":   map[string]any{"type": "string", "description": "Optional cancellation reason."},
			"metadata": map[string]any{"type": "object", "additionalProperties": true},
		},
	}
}

func participantSchema(description string) map[string]any {
	return map[string]any{
		"type":                 "object",
		"description":          description,
		"additionalProperties": false,
		"properties": map[string]any{
			"id":      map[string]any{"type": "string"},
			"name":    map[string]any{"type": "string"},
			"address": map[string]any{"type": "string"},
			"role":    map[string]any{"type": "string"},
		},
	}
}

func formatSearchResults(items []scheduling.Event) string {
	if len(items) == 0 {
		return "No schedule events matched."
	}
	var b strings.Builder
	for i, item := range items {
		if i > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString("- ")
		b.WriteString(eventLabel(item))
		if item.ID != "" {
			b.WriteString(" (id: ")
			b.WriteString(item.ID)
			b.WriteString(")")
		}
		b.WriteString("\n  Time: ")
		b.WriteString(formatTimeRange(item))
		if item.TimeZone != "" {
			b.WriteString(" ")
			b.WriteString(item.TimeZone)
		}
		if item.Location != "" {
			b.WriteString("\n  Location: ")
			b.WriteString(item.Location)
		}
		if organizer := participantLabel(item.Organizer); organizer != "" {
			b.WriteString("\n  Organizer: ")
			b.WriteString(organizer)
		}
		if len(item.Attendees) > 0 {
			b.WriteString("\n  Attendees: ")
			b.WriteString(formatParticipants(item.Attendees))
		}
		if item.Summary != "" {
			b.WriteString("\n  Summary: ")
			b.WriteString(item.Summary)
		}
		if item.Status != "" {
			b.WriteString("\n  Status: ")
			b.WriteString(string(item.Status))
		}
		if len(item.Tags) > 0 {
			b.WriteString("\n  Tags: ")
			b.WriteString(strings.Join(item.Tags, ", "))
		}
	}
	return b.String()
}

func formatEvent(item scheduling.Event) string {
	var b strings.Builder
	b.WriteString("Title: ")
	b.WriteString(eventLabel(item))
	if item.ID != "" {
		b.WriteString("\nEvent ID: ")
		b.WriteString(item.ID)
	}
	b.WriteString("\nTime: ")
	b.WriteString(formatTimeRange(item))
	if item.TimeZone != "" {
		b.WriteString(" ")
		b.WriteString(item.TimeZone)
	}
	if item.Location != "" {
		b.WriteString("\nLocation: ")
		b.WriteString(item.Location)
	}
	if organizer := participantLabel(item.Organizer); organizer != "" {
		b.WriteString("\nOrganizer: ")
		b.WriteString(organizer)
	}
	if len(item.Attendees) > 0 {
		b.WriteString("\nAttendees: ")
		b.WriteString(formatParticipants(item.Attendees))
	}
	if item.Status != "" {
		b.WriteString("\nStatus: ")
		b.WriteString(string(item.Status))
	}
	if item.Summary != "" {
		b.WriteString("\nSummary: ")
		b.WriteString(item.Summary)
	}
	if len(item.Tags) > 0 {
		b.WriteString("\nTags: ")
		b.WriteString(strings.Join(item.Tags, ", "))
	}
	if item.Description != "" {
		b.WriteString("\n\n")
		b.WriteString(item.Description)
	}
	return b.String()
}

func formatTimeRange(item scheduling.Event) string {
	if item.Start.IsZero() || item.End.IsZero() {
		return "unscheduled"
	}
	return item.Start.Format(time.RFC3339) + " to " + item.End.Format(time.RFC3339)
}

func eventLabel(item scheduling.Event) string {
	if item.Title != "" {
		return item.Title
	}
	return item.ID
}

func participantLabel(item scheduling.Participant) string {
	switch {
	case item.Name != "" && item.Address != "":
		return item.Name + " <" + item.Address + ">"
	case item.Name != "":
		return item.Name
	case item.Address != "":
		return item.Address
	default:
		return item.ID
	}
}

func formatParticipants(items []scheduling.Participant) string {
	values := make([]string, 0, len(items))
	for _, item := range items {
		if label := participantLabel(item); label != "" {
			values = append(values, label)
		}
	}
	return strings.Join(values, ", ")
}

func eventMetadata(item scheduling.Event) map[string]any {
	metadata := model.CloneMetadata(item.Metadata)
	if metadata == nil {
		metadata = make(map[string]any, 10)
	}
	metadata["event_id"] = item.ID
	metadata["event_title"] = item.Title
	metadata["event_status"] = string(item.Status)
	metadata["event_location"] = item.Location
	metadata["event_time_zone"] = item.TimeZone
	metadata["event_attendees"] = participantStrings(item.Attendees)
	if organizer := participantLabel(item.Organizer); organizer != "" {
		metadata["event_organizer"] = organizer
	}
	if !item.Start.IsZero() {
		metadata["event_start"] = item.Start.Format(time.RFC3339Nano)
	}
	if !item.End.IsZero() {
		metadata["event_end"] = item.End.Format(time.RFC3339Nano)
	}
	return metadata
}

func addPreviousEventMetadata(metadata map[string]any, item scheduling.Event) {
	metadata["previous_event_id"] = item.ID
	metadata["previous_event_title"] = item.Title
	metadata["previous_event_status"] = string(item.Status)
	metadata["previous_event_location"] = item.Location
	metadata["previous_event_time_zone"] = item.TimeZone
	metadata["previous_event_attendees"] = participantStrings(item.Attendees)
	if organizer := participantLabel(item.Organizer); organizer != "" {
		metadata["previous_event_organizer"] = organizer
	}
	if !item.Start.IsZero() {
		metadata["previous_event_start"] = item.Start.Format(time.RFC3339Nano)
	}
	if !item.End.IsZero() {
		metadata["previous_event_end"] = item.End.Format(time.RFC3339Nano)
	}
}

func participantStrings(items []scheduling.Participant) []string {
	if len(items) == 0 {
		return nil
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		if label := participantLabel(item); label != "" {
			out = append(out, label)
		}
	}
	return out
}

func parseOptionalTime(raw string) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, nil
	}
	return requireTime(raw)
}

func requireTime(raw string) (time.Time, error) {
	value, err := time.Parse(time.RFC3339, strings.TrimSpace(raw))
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid RFC3339 timestamp %q", raw)
	}
	return value.UTC(), nil
}
