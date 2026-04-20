package tasktools

import (
	"context"
	"fmt"
	"strings"

	"github.com/MemaxLabs/memax-go-agent-sdk/model"
	"github.com/MemaxLabs/memax-go-agent-sdk/tool"
)

const (
	ListToolName   = "list_tasks"
	UpsertToolName = "upsert_task"
	DeleteToolName = "delete_task"
)

func NewListTool(store Store) tool.Tool {
	return tool.Definition{
		ToolSpec: model.ToolSpec{
			Name:            ListToolName,
			Description:     "List current task state for this agent run.",
			SearchHint:      "list todo task plan state",
			ReadOnly:        true,
			ConcurrencySafe: true,
			MaxResultBytes:  32 * 1024,
			InputSchema: map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"properties": map[string]any{
					"status": map[string]any{
						"type":        "string",
						"description": "Optional status filter.",
						"enum":        statusEnum(),
					},
				},
			},
		},
		Handler: func(ctx context.Context, call tool.Call) (model.ToolResult, error) {
			input, err := tool.DecodeInput[listInput](call.Use)
			if err != nil {
				return model.ToolResult{}, err
			}
			if !isValidStatus(input.Status) {
				return model.ToolResult{}, fmt.Errorf("invalid task status: %s", input.Status)
			}
			tasks, err := store.List(ctx)
			if err != nil {
				return model.ToolResult{}, err
			}
			tasks = filterTasks(tasks, input.Status)
			return model.ToolResult{Content: formatTasks(tasks)}, nil
		},
	}
}

func NewUpsertTool(store Store) tool.Tool {
	return tool.Definition{
		ToolSpec: model.ToolSpec{
			Name:           UpsertToolName,
			Description:    "Create or update a task in the agent's task state.",
			SearchHint:     "create update todo task status plan",
			Destructive:    true,
			MaxResultBytes: 8 * 1024,
			InputSchema: map[string]any{
				"type":                 "object",
				"minProperties":        1,
				"additionalProperties": false,
				"properties": map[string]any{
					"id": map[string]any{
						"type":        "string",
						"description": "Existing task ID to update. Omit to create a task.",
						"minLength":   1,
					},
					"title": map[string]any{
						"type":        "string",
						"description": "Short task title. Required when creating a task.",
						"minLength":   1,
					},
					"status": map[string]any{
						"type":        "string",
						"description": "Task status.",
						"enum":        statusEnum(),
					},
					"notes": map[string]any{
						"type":        "string",
						"description": "Optional details, blocker, or completion note.",
					},
					"priority": map[string]any{
						"type":        "integer",
						"description": "Optional priority where lower numbers are more important.",
						"minimum":     0,
					},
					"evidence": map[string]any{
						"type":        "array",
						"description": "Optional evidence proving task progress, such as paths, checks, or verification names.",
						"items": map[string]any{
							"type": "string",
						},
					},
				},
			},
		},
		Handler: func(ctx context.Context, call tool.Call) (model.ToolResult, error) {
			input, err := tool.DecodeInput[upsertInput](call.Use)
			if err != nil {
				return model.ToolResult{}, err
			}
			task, err := store.Upsert(ctx, Task{
				ID:       input.ID,
				Title:    input.Title,
				Status:   input.Status,
				Notes:    input.Notes,
				Priority: input.Priority,
				Evidence: input.Evidence,
			})
			if err != nil {
				return model.ToolResult{}, err
			}
			return model.ToolResult{
				Content:  "upserted " + task.ID,
				Metadata: taskMetadata(task),
			}, nil
		},
	}
}

func NewDeleteTool(store Store) tool.Tool {
	return tool.Definition{
		ToolSpec: model.ToolSpec{
			Name:           DeleteToolName,
			Description:    "Delete a task from the agent's task state.",
			SearchHint:     "delete remove todo task",
			Destructive:    true,
			MaxResultBytes: 8 * 1024,
			InputSchema: map[string]any{
				"type":                 "object",
				"required":             []any{"id"},
				"additionalProperties": false,
				"properties": map[string]any{
					"id": map[string]any{
						"type":        "string",
						"description": "Task ID to delete.",
						"minLength":   1,
					},
				},
			},
		},
		Handler: func(ctx context.Context, call tool.Call) (model.ToolResult, error) {
			input, err := tool.DecodeInput[deleteInput](call.Use)
			if err != nil {
				return model.ToolResult{}, err
			}
			if err := store.Delete(ctx, input.ID); err != nil {
				return model.ToolResult{}, err
			}
			return model.ToolResult{Content: "deleted " + strings.TrimSpace(input.ID)}, nil
		},
	}
}

type listInput struct {
	Status Status `json:"status"`
}

type upsertInput struct {
	ID       string   `json:"id"`
	Title    string   `json:"title"`
	Status   Status   `json:"status"`
	Notes    string   `json:"notes"`
	Priority int      `json:"priority"`
	Evidence []string `json:"evidence"`
}

type deleteInput struct {
	ID string `json:"id"`
}

func filterTasks(tasks []Task, status Status) []Task {
	if status == "" {
		return sortTasks(tasks)
	}
	var out []Task
	for _, task := range tasks {
		if task.Status == status {
			out = append(out, task)
		}
	}
	return sortTasks(out)
}

func formatTasks(tasks []Task) string {
	if len(tasks) == 0 {
		return "no tasks"
	}
	var b strings.Builder
	for i, task := range tasks {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString("- [")
		b.WriteString(string(task.Status))
		b.WriteString("] ")
		b.WriteString(task.ID)
		if task.Priority > 0 {
			fmt.Fprintf(&b, " p%d", task.Priority)
		}
		b.WriteString(": ")
		b.WriteString(task.Title)
		if task.Notes != "" {
			b.WriteString(" - ")
			b.WriteString(task.Notes)
		}
		if len(task.Evidence) > 0 {
			b.WriteString(" evidence: ")
			b.WriteString(strings.Join(task.Evidence, ", "))
		}
	}
	return b.String()
}

func taskMetadata(task Task) map[string]any {
	return map[string]any{
		"id":                       task.ID,
		"title":                    task.Title,
		"status":                   string(task.Status),
		"notes":                    task.Notes,
		"priority":                 task.Priority,
		"evidence":                 append([]string(nil), task.Evidence...),
		model.MetadataTaskID:       task.ID,
		model.MetadataTaskStatus:   string(task.Status),
		model.MetadataTaskEvidence: append([]string(nil), task.Evidence...),
	}
}

func statusEnum() []any {
	return []any{
		string(StatusPending),
		string(StatusInProgress),
		string(StatusCompleted),
		string(StatusBlocked),
		string(StatusCanceled),
	}
}
