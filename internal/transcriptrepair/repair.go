package transcriptrepair

import "github.com/MemaxLabs/memax-go-agent-sdk/model"

const interruptedToolResultContent = "tool call was interrupted before a result was recorded; continue without relying on this tool output"

// RepairToolUseAdjacency returns a transcript whose assistant tool-use messages
// are immediately followed by the matching tool-result messages required by
// provider protocols such as Anthropic Messages.
func RepairToolUseAdjacency(messages []model.Message) []model.Message {
	if len(messages) == 0 {
		return nil
	}
	out := make([]model.Message, 0, len(messages))
	for i := 0; i < len(messages); i++ {
		msg := messages[i]
		if msg.Role == model.RoleTool {
			continue
		}

		out = append(out, model.CloneMessage(msg))
		uses := assistantToolUses(msg)
		if len(uses) == 0 {
			continue
		}

		known := make(map[string]model.ToolUse, len(uses))
		for _, use := range uses {
			if use.ID != "" {
				known[use.ID] = use
			}
		}
		seen := make(map[string]bool, len(uses))
		j := i + 1
		for j < len(messages) && messages[j].Role == model.RoleTool {
			result := messages[j].ToolResult
			if result == nil {
				j++
				continue
			}
			if _, ok := known[result.ToolUseID]; !ok || seen[result.ToolUseID] {
				j++
				continue
			}
			out = append(out, model.CloneMessage(messages[j]))
			seen[result.ToolUseID] = true
			j++
		}
		for _, use := range uses {
			if use.ID == "" || seen[use.ID] {
				continue
			}
			out = append(out, model.Message{
				Role: model.RoleTool,
				ToolResult: &model.ToolResult{
					ToolUseID: use.ID,
					Name:      use.Name,
					Content:   interruptedToolResultContent,
					IsError:   true,
				},
			})
		}
		i = j - 1
	}
	return out
}

func assistantToolUses(msg model.Message) []model.ToolUse {
	if msg.Role != model.RoleAssistant {
		return nil
	}
	var uses []model.ToolUse
	for _, block := range msg.Content {
		if block.Type == model.ContentToolUse && block.ToolUse != nil {
			uses = append(uses, model.NormalizeToolUse(*block.ToolUse))
		}
	}
	return uses
}
