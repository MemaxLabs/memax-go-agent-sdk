package contextwindow

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/MemaxLabs/memax-go-agent-sdk/model"
)

const defaultSummarizerPrompt = `Summarize the transcript for a future autonomous agent turn.
Preserve user goals, decisions, constraints, tool results, unresolved tasks, and any facts needed to continue.
Return only the summary.`

// ModelSummarizer uses a model.Client to summarize compacted transcript
// messages without exposing any tools to the summarization call.
type ModelSummarizer struct {
	Model        model.Client
	SystemPrompt string
	Prompt       string
}

// Summarize asks the configured model client to summarize messages for future
// turns and returns the streamed text as a compact summary.
func (s ModelSummarizer) Summarize(ctx context.Context, messages []model.Message) (string, error) {
	if s.Model == nil {
		return "", fmt.Errorf("contextwindow: summarizer model is required")
	}
	prompt := s.Prompt
	if prompt == "" {
		prompt = defaultSummarizerPrompt
	}
	stream, err := s.Model.Stream(ctx, model.Request{
		SystemPrompt: s.SystemPrompt,
		Messages: []model.Message{
			{
				Role: model.RoleUser,
				Content: []model.ContentBlock{
					{Type: model.ContentText, Text: prompt + "\n\nTranscript:\n" + formatTranscript(messages)},
				},
			},
		},
	})
	if err != nil {
		return "", fmt.Errorf("contextwindow: stream summarizer: %w", err)
	}
	defer stream.Close()

	var out strings.Builder
	for {
		event, err := stream.Recv()
		if errors.Is(err, model.ErrEndOfStream) {
			summary := strings.TrimSpace(out.String())
			if summary == "" {
				return "", fmt.Errorf("contextwindow: summarizer returned empty summary")
			}
			return summary, nil
		}
		if err != nil {
			return "", fmt.Errorf("contextwindow: receive summarizer event: %w", err)
		}
		switch event.Kind {
		case model.StreamText:
			out.WriteString(event.Text)
		case model.StreamToolUse:
			return "", fmt.Errorf("contextwindow: summarizer emitted tool use %q", event.ToolUse.Name)
		}
	}
}

func formatTranscript(messages []model.Message) string {
	var out strings.Builder
	for _, msg := range messages {
		switch msg.Role {
		case model.RoleUser, model.RoleAssistant:
			text := msg.PlainText()
			if text != "" {
				fmt.Fprintf(&out, "%s: %s\n", msg.Role, text)
			}
			for _, block := range msg.Content {
				if block.ToolUse != nil {
					fmt.Fprintf(&out, "assistant tool_use %s %s: %s\n", block.ToolUse.ID, block.ToolUse.Name, string(block.ToolUse.Input))
				}
			}
		case model.RoleTool:
			if msg.ToolResult != nil {
				prefix := "tool_result"
				if msg.ToolResult.IsError {
					prefix = "tool_error"
				}
				fmt.Fprintf(&out, "%s %s %s: %s\n", prefix, msg.ToolResult.ToolUseID, msg.ToolResult.Name, msg.ToolResult.Content)
			}
		case model.RoleSystem:
			text := msg.PlainText()
			if text != "" {
				fmt.Fprintf(&out, "system: %s\n", text)
			}
		}
	}
	return out.String()
}
