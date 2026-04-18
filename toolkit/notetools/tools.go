// Package notetools exposes host-owned note and lightweight document tools.
package notetools

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/MemaxLabs/memax-go-agent-sdk/model"
	"github.com/MemaxLabs/memax-go-agent-sdk/notes"
	"github.com/MemaxLabs/memax-go-agent-sdk/tool"
)

const (
	SearchToolName = "search_notes"
	ReadToolName   = "read_note"
	SaveToolName   = "save_note"
	DeleteToolName = "delete_note"

	defaultSearchLimit          = 8
	defaultSearchToolMaxBytes   = 64 * 1024
	defaultMutationToolMaxBytes = 16 * 1024
	defaultReadToolMaxBytes     = 64 * 1024
)

// Config controls the note tools exposed for one notes backend.
type Config struct {
	Searcher       notes.Searcher
	Reader         notes.Reader
	Writer         notes.Writer
	Deleter        notes.Deleter
	SearchName     string
	ReadName       string
	SaveName       string
	DeleteName     string
	DefaultLimit   int
	MaxResultBytes int
}

// NewTools returns tools for the capabilities configured in config.
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
	if config.Writer != nil {
		save, err := NewSaveTool(config)
		if err != nil {
			return nil, err
		}
		tools = append(tools, save)
	}
	if config.Deleter != nil {
		deleteTool, err := NewDeleteTool(config)
		if err != nil {
			return nil, err
		}
		tools = append(tools, deleteTool)
	}
	if len(tools) == 0 {
		return nil, fmt.Errorf("notetools: at least one searcher, reader, writer, or deleter is required")
	}
	return tools, nil
}

// NewSearchTool returns a metadata-only note search tool.
func NewSearchTool(config Config) (tool.Tool, error) {
	if config.Searcher == nil {
		return nil, fmt.Errorf("notetools: searcher is required")
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
			Description:     "Search host-owned personal notes or lightweight documents by metadata and relevance without loading full content.",
			SearchHint:      "search notes docs knowledge personal documents metadata summary title tags",
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
			items, err := config.Searcher.SearchNotes(ctx, notes.SearchRequest{
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

// NewReadTool returns a full-content note read tool.
func NewReadTool(config Config) (tool.Tool, error) {
	if config.Reader == nil {
		return nil, fmt.Errorf("notetools: reader is required")
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
			Description:     "Read the full content of one host-owned note or lightweight document by ID or title.",
			SearchHint:      "read note doc document content full by id title",
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
			item, err := config.Reader.ReadNote(ctx, notes.ReadRequest{
				SessionID:       call.Runtime.SessionID,
				ParentSessionID: call.Runtime.ParentSessionID,
				Identity:        call.Runtime.Identity,
				ID:              strings.TrimSpace(input.ID),
				Title:           strings.TrimSpace(input.Title),
			})
			if err != nil {
				return model.ToolResult{}, err
			}
			metadata := noteMetadata(item)
			metadata["content_bytes"] = len(item.Content)
			return model.ToolResult{
				Content:  formatNote(item),
				Metadata: metadata,
			}, nil
		},
	}, nil
}

// NewSaveTool returns a tool that lets an agent request durable note writes.
func NewSaveTool(config Config) (tool.Tool, error) {
	if config.Writer == nil {
		return nil, fmt.Errorf("notetools: writer is required")
	}
	name := config.SaveName
	if name == "" {
		name = SaveToolName
	}
	maxResultBytes := config.MaxResultBytes
	if maxResultBytes <= 0 {
		maxResultBytes = defaultMutationToolMaxBytes
	}
	return tool.Definition{
		ToolSpec: model.ToolSpec{
			Name:           name,
			Description:    "Save a host-owned personal note or lightweight document when the user or host policy allows it.",
			SearchHint:     "save note doc document personal knowledge",
			Destructive:    true,
			MaxResultBytes: maxResultBytes,
			InputSchema:    saveInputSchema(),
		},
		Handler: func(ctx context.Context, call tool.Call) (model.ToolResult, error) {
			input, err := tool.DecodeInput[saveInput](call.Use)
			if err != nil {
				return model.ToolResult{}, err
			}
			result, err := config.Writer.PutNote(ctx, notes.PutRequest{
				SessionID:       call.Runtime.SessionID,
				ParentSessionID: call.Runtime.ParentSessionID,
				Identity:        call.Runtime.Identity,
				Note: notes.Note{
					ID:       strings.TrimSpace(input.ID),
					Title:    strings.TrimSpace(input.Title),
					Kind:     strings.TrimSpace(input.Kind),
					Summary:  strings.TrimSpace(input.Summary),
					Content:  strings.TrimSpace(input.Content),
					Tags:     append([]string(nil), input.Tags...),
					Metadata: model.CloneMetadata(input.Metadata),
				},
			})
			if err != nil {
				return model.ToolResult{}, err
			}
			item := result.Note
			action := "saved"
			if result.Created {
				action = "created"
			} else if result.Updated {
				action = "updated"
			}
			metadata := noteMetadata(item)
			metadata["action"] = action
			return model.ToolResult{
				Content:  fmt.Sprintf("%s note %s", action, noteLabel(item)),
				Metadata: metadata,
			}, nil
		},
	}, nil
}

// NewDeleteTool returns a tool that lets an agent request note deletion.
func NewDeleteTool(config Config) (tool.Tool, error) {
	if config.Deleter == nil {
		return nil, fmt.Errorf("notetools: deleter is required")
	}
	name := config.DeleteName
	if name == "" {
		name = DeleteToolName
	}
	maxResultBytes := config.MaxResultBytes
	if maxResultBytes <= 0 {
		maxResultBytes = defaultMutationToolMaxBytes
	}
	return tool.Definition{
		ToolSpec: model.ToolSpec{
			Name:           name,
			Description:    "Delete a host-owned note or lightweight document by ID or title when the user or host policy allows it.",
			SearchHint:     "delete remove forget note doc document",
			Destructive:    true,
			MaxResultBytes: maxResultBytes,
			InputSchema:    deleteInputSchema(),
		},
		Handler: func(ctx context.Context, call tool.Call) (model.ToolResult, error) {
			input, err := tool.DecodeInput[deleteInput](call.Use)
			if err != nil {
				return model.ToolResult{}, err
			}
			req := notes.DeleteRequest{
				SessionID:       call.Runtime.SessionID,
				ParentSessionID: call.Runtime.ParentSessionID,
				Identity:        call.Runtime.Identity,
				ID:              strings.TrimSpace(input.ID),
				Title:           strings.TrimSpace(input.Title),
			}
			if req.ID == "" && req.Title == "" {
				return model.ToolResult{}, fmt.Errorf("delete_note requires id or title")
			}
			if err := config.Deleter.DeleteNote(ctx, req); err != nil {
				return model.ToolResult{}, err
			}
			label := req.ID
			if label == "" {
				label = req.Title
			}
			return model.ToolResult{Content: "deleted note " + label}, nil
		},
	}, nil
}

type searchInput struct {
	Query string `json:"query"`
	Limit int    `json:"limit"`
}

type readInput struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

type saveInput struct {
	ID       string         `json:"id"`
	Title    string         `json:"title"`
	Kind     string         `json:"kind"`
	Summary  string         `json:"summary"`
	Content  string         `json:"content"`
	Tags     []string       `json:"tags"`
	Metadata map[string]any `json:"metadata"`
}

type deleteInput struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

func searchInputSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"query": map[string]any{"type": "string", "description": "Note or document search query."},
			"limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 50, "description": "Maximum notes to return."},
		},
	}
}

func readInputSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"id":    map[string]any{"type": "string", "description": "Exact note ID to load."},
			"title": map[string]any{"type": "string", "description": "Exact note title to load when ID is unknown."},
		},
	}
}

func saveInputSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"required":             []any{"content"},
		"additionalProperties": false,
		"properties": map[string]any{
			"id":       map[string]any{"type": "string", "description": "Optional existing note ID to replace."},
			"title":    map[string]any{"type": "string", "description": "Stable note title for lookup and deduplication. Strongly recommended so search results stay readable."},
			"kind":     map[string]any{"type": "string", "description": "Optional note/document kind, such as note, brief, summary, or checklist."},
			"summary":  map[string]any{"type": "string", "description": "Short metadata summary for search results."},
			"content":  map[string]any{"type": "string", "minLength": 1, "description": "Full durable note content."},
			"tags":     map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"metadata": map[string]any{"type": "object", "additionalProperties": true},
		},
	}
}

func deleteInputSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"id":    map[string]any{"type": "string", "description": "Exact note ID to delete."},
			"title": map[string]any{"type": "string", "description": "Exact note title to delete when ID is unknown."},
		},
	}
}

func formatSearchResults(items []notes.Note) string {
	if len(items) == 0 {
		return "No notes matched."
	}
	var b strings.Builder
	for i, item := range items {
		if i > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString("- ")
		b.WriteString(noteLabel(item))
		if item.ID != "" {
			b.WriteString(" (id: ")
			b.WriteString(item.ID)
			b.WriteString(")")
		}
		if item.Kind != "" {
			b.WriteString("\n  Kind: ")
			b.WriteString(item.Kind)
		}
		if !item.UpdatedAt.IsZero() {
			b.WriteString("\n  Updated: ")
			b.WriteString(item.UpdatedAt.Format(time.RFC3339))
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

func formatNote(item notes.Note) string {
	var b strings.Builder
	b.WriteString("Title: ")
	b.WriteString(noteLabel(item))
	if item.ID != "" {
		b.WriteString("\nID: ")
		b.WriteString(item.ID)
	}
	if item.Kind != "" {
		b.WriteString("\nKind: ")
		b.WriteString(item.Kind)
	}
	if !item.CreatedAt.IsZero() {
		b.WriteString("\nCreated: ")
		b.WriteString(item.CreatedAt.Format(time.RFC3339))
	}
	if !item.UpdatedAt.IsZero() {
		b.WriteString("\nUpdated: ")
		b.WriteString(item.UpdatedAt.Format(time.RFC3339))
	}
	if item.Summary != "" {
		b.WriteString("\nSummary: ")
		b.WriteString(item.Summary)
	}
	if len(item.Tags) > 0 {
		b.WriteString("\nTags: ")
		b.WriteString(strings.Join(item.Tags, ", "))
	}
	b.WriteString("\n\n")
	b.WriteString(item.Content)
	return b.String()
}

func noteLabel(item notes.Note) string {
	if item.Title != "" {
		return item.Title
	}
	return item.ID
}

func noteMetadata(item notes.Note) map[string]any {
	metadata := model.CloneMetadata(item.Metadata)
	if metadata == nil {
		metadata = make(map[string]any, 4)
	}
	metadata["note_id"] = item.ID
	metadata["note_title"] = item.Title
	metadata["note_kind"] = item.Kind
	metadata["note_tags"] = append([]string(nil), item.Tags...)
	if !item.CreatedAt.IsZero() {
		metadata["note_created_at"] = item.CreatedAt.Format(time.RFC3339Nano)
	}
	if !item.UpdatedAt.IsZero() {
		metadata["note_updated_at"] = item.UpdatedAt.Format(time.RFC3339Nano)
	}
	return metadata
}
