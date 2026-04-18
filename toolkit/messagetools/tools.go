// Package messagetools exposes host-owned messaging tools.
package messagetools

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/MemaxLabs/memax-go-agent-sdk/messaging"
	"github.com/MemaxLabs/memax-go-agent-sdk/model"
	"github.com/MemaxLabs/memax-go-agent-sdk/tool"
)

const (
	SearchToolName = "search_message_threads"
	ReadToolName   = "read_message_thread"
	SendToolName   = "send_message"

	defaultSearchLimit          = 8
	defaultSearchToolMaxBytes   = 64 * 1024
	defaultReadToolMaxBytes     = 64 * 1024
	defaultMutationToolMaxBytes = 16 * 1024
)

// Config controls the messaging tools exposed for one messaging backend.
type Config struct {
	Searcher       messaging.Searcher
	Reader         messaging.Reader
	Sender         messaging.Sender
	SearchName     string
	ReadName       string
	SendName       string
	DefaultLimit   int
	MaxResultBytes int
}

// NewTools returns tools for the configured messaging capabilities.
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
	if config.Sender != nil {
		send, err := NewSendTool(config)
		if err != nil {
			return nil, err
		}
		tools = append(tools, send)
	}
	if len(tools) == 0 {
		return nil, fmt.Errorf("messagetools: at least one searcher, reader, or sender is required")
	}
	return tools, nil
}

// NewSearchTool returns a metadata-only message thread search tool.
func NewSearchTool(config Config) (tool.Tool, error) {
	if config.Searcher == nil {
		return nil, fmt.Errorf("messagetools: searcher is required")
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
			Description:     "Search host-owned message thread metadata by subject, participants, tags, and summaries without loading full thread content.",
			SearchHint:      "search messages threads inbox email chat dm subject participants summary",
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
			selectedLimit := input.Limit
			if selectedLimit <= 0 {
				selectedLimit = limit
			}
			items, err := config.Searcher.SearchThreads(ctx, messaging.SearchRequest{
				SessionID:       call.Runtime.SessionID,
				ParentSessionID: call.Runtime.ParentSessionID,
				Identity:        call.Runtime.Identity,
				Query:           input.Query,
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

// NewReadTool returns a full-thread read tool.
func NewReadTool(config Config) (tool.Tool, error) {
	if config.Reader == nil {
		return nil, fmt.Errorf("messagetools: reader is required")
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
			Description:     "Read the full contents of one host-owned message thread by thread ID or subject.",
			SearchHint:      "read message thread conversation inbox email chat dm",
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
			thread, err := config.Reader.ReadThread(ctx, messaging.ReadRequest{
				SessionID:       call.Runtime.SessionID,
				ParentSessionID: call.Runtime.ParentSessionID,
				Identity:        call.Runtime.Identity,
				ThreadID:        strings.TrimSpace(input.ThreadID),
				Subject:         strings.TrimSpace(input.Subject),
			})
			if err != nil {
				return model.ToolResult{}, err
			}
			metadata := threadMetadata(thread)
			metadata["message_count"] = len(thread.Messages)
			return model.ToolResult{
				Content:  formatThread(thread),
				Metadata: metadata,
			}, nil
		},
	}, nil
}

// NewSendTool returns an outbound send tool.
func NewSendTool(config Config) (tool.Tool, error) {
	if config.Sender == nil {
		return nil, fmt.Errorf("messagetools: sender is required")
	}
	name := config.SendName
	if name == "" {
		name = SendToolName
	}
	maxResultBytes := config.MaxResultBytes
	if maxResultBytes <= 0 {
		maxResultBytes = defaultMutationToolMaxBytes
	}
	return tool.Definition{
		ToolSpec: model.ToolSpec{
			Name:           name,
			Description:    "Send an outbound message or reply through a host-owned messaging backend when the user or host policy allows it.",
			SearchHint:     "send message reply email chat dm",
			Destructive:    true,
			MaxResultBytes: maxResultBytes,
			InputSchema:    sendInputSchema(),
		},
		Handler: func(ctx context.Context, call tool.Call) (model.ToolResult, error) {
			input, err := tool.DecodeInput[sendInput](call.Use)
			if err != nil {
				return model.ToolResult{}, err
			}
			result, err := config.Sender.SendMessage(ctx, messaging.SendRequest{
				SessionID:       call.Runtime.SessionID,
				ParentSessionID: call.Runtime.ParentSessionID,
				Identity:        call.Runtime.Identity,
				ThreadID:        strings.TrimSpace(input.ThreadID),
				Subject:         strings.TrimSpace(input.Subject),
				Summary:         strings.TrimSpace(input.Summary),
				Body:            strings.TrimSpace(input.Body),
				Recipients:      input.Recipients.participants(),
				Metadata:        model.CloneMetadata(input.Metadata),
			})
			if err != nil {
				return model.ToolResult{}, err
			}
			metadata := threadMetadata(result.Thread)
			metadata["message_id"] = result.Message.ID
			metadata["created_thread"] = result.CreatedThread
			return model.ToolResult{
				Content:  sendResultContent(result),
				Metadata: metadata,
			}, nil
		},
	}, nil
}

type searchInput struct {
	Query string `json:"query"`
	Limit int    `json:"limit"`
}

type readInput struct {
	ThreadID string `json:"thread_id"`
	Subject  string `json:"subject"`
}

type sendInput struct {
	ThreadID   string            `json:"thread_id"`
	Subject    string            `json:"subject"`
	Summary    string            `json:"summary"`
	Body       string            `json:"body"`
	Recipients participantInputs `json:"recipients"`
	Metadata   map[string]any    `json:"metadata"`
}

type participantInputs []participantInput

type participantInput struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Address string `json:"address"`
	Role    string `json:"role"`
}

func (p participantInputs) participants() []messaging.Participant {
	if len(p) == 0 {
		return nil
	}
	out := make([]messaging.Participant, len(p))
	for i, item := range p {
		out[i] = messaging.Participant{
			ID:      strings.TrimSpace(item.ID),
			Name:    strings.TrimSpace(item.Name),
			Address: strings.TrimSpace(item.Address),
			Role:    strings.TrimSpace(item.Role),
		}
	}
	return out
}

func searchInputSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"query": map[string]any{"type": "string", "description": "Message-thread search query."},
			"limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 50, "description": "Maximum threads to return."},
		},
	}
}

func readInputSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"thread_id": map[string]any{"type": "string", "description": "Exact thread ID to load."},
			"subject":   map[string]any{"type": "string", "description": "Exact subject to load when thread ID is unknown."},
		},
	}
}

func sendInputSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"required":             []any{"body"},
		"additionalProperties": false,
		"properties": map[string]any{
			"thread_id": map[string]any{"type": "string", "description": "Optional existing thread ID to reply to."},
			"subject":   map[string]any{"type": "string", "description": "Subject for a new thread. Strongly recommended when thread_id is omitted."},
			"summary":   map[string]any{"type": "string", "description": "Optional thread summary metadata."},
			"body":      map[string]any{"type": "string", "minLength": 1, "description": "Outbound message body."},
			"recipients": map[string]any{
				"type":        "array",
				"description": "Recipients for the message or new thread.",
				"items": map[string]any{
					"type":                 "object",
					"additionalProperties": false,
					"properties": map[string]any{
						"id":      map[string]any{"type": "string"},
						"name":    map[string]any{"type": "string"},
						"address": map[string]any{"type": "string"},
						"role":    map[string]any{"type": "string"},
					},
				},
			},
			"metadata": map[string]any{"type": "object", "additionalProperties": true},
		},
	}
}

func formatSearchResults(items []messaging.Thread) string {
	if len(items) == 0 {
		return "No message threads matched."
	}
	var b strings.Builder
	for i, item := range items {
		if i > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString("- ")
		b.WriteString(threadLabel(item))
		if item.ID != "" {
			b.WriteString(" (id: ")
			b.WriteString(item.ID)
			b.WriteString(")")
		}
		if !item.LastMessageAt.IsZero() {
			b.WriteString("\n  Updated: ")
			b.WriteString(item.LastMessageAt.Format(time.RFC3339))
		}
		if len(item.Participants) > 0 {
			b.WriteString("\n  Participants: ")
			b.WriteString(formatParticipants(item.Participants))
		}
		if item.Summary != "" {
			b.WriteString("\n  Summary: ")
			b.WriteString(item.Summary)
		}
		if len(item.Tags) > 0 {
			b.WriteString("\n  Tags: ")
			b.WriteString(strings.Join(item.Tags, ", "))
		}
	}
	return b.String()
}

func formatThread(thread messaging.Thread) string {
	var b strings.Builder
	b.WriteString("Subject: ")
	b.WriteString(threadLabel(thread))
	if thread.ID != "" {
		b.WriteString("\nThread ID: ")
		b.WriteString(thread.ID)
	}
	if !thread.LastMessageAt.IsZero() {
		b.WriteString("\nLast Message: ")
		b.WriteString(thread.LastMessageAt.Format(time.RFC3339))
	}
	if len(thread.Participants) > 0 {
		b.WriteString("\nParticipants: ")
		b.WriteString(formatParticipants(thread.Participants))
	}
	if thread.Summary != "" {
		b.WriteString("\nSummary: ")
		b.WriteString(thread.Summary)
	}
	if len(thread.Tags) > 0 {
		b.WriteString("\nTags: ")
		b.WriteString(strings.Join(thread.Tags, ", "))
	}
	for _, message := range thread.Messages {
		b.WriteString("\n\n")
		b.WriteString(formatMessage(message))
	}
	return b.String()
}

func formatMessage(message messaging.Message) string {
	var b strings.Builder
	b.WriteString(directionLabel(message.Direction))
	if !message.SentAt.IsZero() {
		b.WriteString(" @ ")
		b.WriteString(message.SentAt.Format(time.RFC3339))
	}
	sender := participantLabel(message.Sender)
	if sender != "" {
		b.WriteString("\nFrom: ")
		b.WriteString(sender)
	}
	if len(message.Recipients) > 0 {
		b.WriteString("\nTo: ")
		b.WriteString(formatParticipants(message.Recipients))
	}
	if message.Summary != "" {
		b.WriteString("\nSummary: ")
		b.WriteString(message.Summary)
	}
	b.WriteString("\n\n")
	b.WriteString(message.Body)
	return b.String()
}

func directionLabel(direction messaging.Direction) string {
	if direction == "" {
		return "Message"
	}
	text := string(direction)
	runes := []rune(text)
	runes[0] = unicode.ToUpper(runes[0])
	return string(runes)
}

func threadLabel(thread messaging.Thread) string {
	if thread.Subject != "" {
		return thread.Subject
	}
	return thread.ID
}

func participantLabel(item messaging.Participant) string {
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

func formatParticipants(items []messaging.Participant) string {
	values := make([]string, 0, len(items))
	for _, item := range items {
		if label := participantLabel(item); label != "" {
			values = append(values, label)
		}
	}
	return strings.Join(values, ", ")
}

func threadMetadata(thread messaging.Thread) map[string]any {
	metadata := model.CloneMetadata(thread.Metadata)
	if metadata == nil {
		metadata = make(map[string]any, 8)
	}
	metadata["thread_id"] = thread.ID
	metadata["thread_subject"] = thread.Subject
	metadata["thread_tags"] = append([]string(nil), thread.Tags...)
	metadata["thread_participants"] = participantStrings(thread.Participants)
	if !thread.LastMessageAt.IsZero() {
		metadata["thread_last_message_at"] = thread.LastMessageAt.Format(time.RFC3339Nano)
	}
	return metadata
}

func participantStrings(items []messaging.Participant) []string {
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

func sendResultContent(result messaging.SendResult) string {
	action := "sent message"
	if result.CreatedThread {
		action = "created thread and sent message"
	}
	label := threadLabel(result.Thread)
	if label == "" {
		label = result.Thread.ID
	}
	return fmt.Sprintf("%s %s", action, label)
}
