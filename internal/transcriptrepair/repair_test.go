package transcriptrepair

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/MemaxLabs/memax-go-agent-sdk/model"
)

func TestRepairToolUseAdjacencyDropsOrphanToolResults(t *testing.T) {
	messages := []model.Message{
		{
			Role: model.RoleTool,
			ToolResult: &model.ToolResult{
				ToolUseID: "orphan",
				Name:      "read",
				Content:   "stale",
			},
		},
		{
			Role: model.RoleAssistant,
			Content: []model.ContentBlock{{
				Type: model.ContentToolUse,
				ToolUse: &model.ToolUse{
					ID:    "tool-1",
					Name:  "read",
					Input: json.RawMessage(`{}`),
				},
			}},
		},
		{
			Role: model.RoleTool,
			ToolResult: &model.ToolResult{
				ToolUseID: "wrong",
				Name:      "read",
				Content:   "wrong result",
			},
		},
	}

	repaired := RepairToolUseAdjacency(messages)
	if len(repaired) != 2 {
		t.Fatalf("len(repaired) = %d, want 2: %#v", len(repaired), repaired)
	}
	if !messageHasToolUse(repaired[0], "tool-1") {
		t.Fatalf("first repaired message = %#v, want assistant tool use", repaired[0])
	}
	if repaired[1].ToolResult == nil || repaired[1].ToolResult.ToolUseID != "tool-1" || !repaired[1].ToolResult.IsError {
		t.Fatalf("second repaired message = %#v, want synthetic result for tool-1", repaired[1])
	}
}

func TestRepairToolUseAdjacencyHandlesPartialDuplicateAndNilResults(t *testing.T) {
	messages := []model.Message{
		{
			Role: model.RoleAssistant,
			Content: []model.ContentBlock{
				{
					Type: model.ContentToolUse,
					ToolUse: &model.ToolUse{
						ID:    "tool-1",
						Name:  "read",
						Input: json.RawMessage(`{}`),
					},
				},
				{
					Type: model.ContentToolUse,
					ToolUse: &model.ToolUse{
						ID:    "tool-2",
						Name:  "write",
						Input: json.RawMessage(`{"path":"README.md"}`),
					},
				},
			},
		},
		{
			Role: model.RoleTool,
			ToolResult: &model.ToolResult{
				ToolUseID: "tool-1",
				Name:      "read",
				Content:   "first result",
			},
		},
		{
			Role: model.RoleTool,
			ToolResult: &model.ToolResult{
				ToolUseID: "tool-1",
				Name:      "read",
				Content:   "duplicate result",
			},
		},
		{
			Role: model.RoleTool,
			ToolResult: &model.ToolResult{
				ToolUseID: "wrong",
				Name:      "read",
				Content:   "wrong result",
			},
		},
		{Role: model.RoleTool},
	}

	repaired := RepairToolUseAdjacency(messages)
	if len(repaired) != 3 {
		t.Fatalf("len(repaired) = %d, want 3: %#v", len(repaired), repaired)
	}
	if !messageHasToolUse(repaired[0], "tool-1") || !messageHasToolUse(repaired[0], "tool-2") {
		t.Fatalf("first repaired message = %#v, want both assistant tool uses", repaired[0])
	}
	if repaired[1].ToolResult == nil || repaired[1].ToolResult.ToolUseID != "tool-1" || repaired[1].ToolResult.Content != "first result" {
		t.Fatalf("second repaired message = %#v, want original first result", repaired[1])
	}
	if repaired[2].ToolResult == nil || repaired[2].ToolResult.ToolUseID != "tool-2" || !repaired[2].ToolResult.IsError {
		t.Fatalf("third repaired message = %#v, want synthetic result for missing tool-2", repaired[2])
	}
}

func TestRepairToolUseAdjacencyIsIdempotent(t *testing.T) {
	messages := []model.Message{
		{
			Role: model.RoleAssistant,
			Content: []model.ContentBlock{{
				Type: model.ContentToolUse,
				ToolUse: &model.ToolUse{
					ID:    "tool-1",
					Name:  "read",
					Input: json.RawMessage(`{}`),
				},
			}},
		},
		{
			Role: model.RoleTool,
			ToolResult: &model.ToolResult{
				ToolUseID: "tool-1",
				Name:      "read",
				Content:   "ok",
			},
		},
		{Role: model.RoleUser, Content: []model.ContentBlock{{Type: model.ContentText, Text: "next"}}},
	}

	once := RepairToolUseAdjacency(messages)
	twice := RepairToolUseAdjacency(once)
	if len(once) != len(twice) {
		t.Fatalf("len twice = %d, want %d", len(twice), len(once))
	}
	for i := range once {
		if once[i].Role != twice[i].Role || once[i].PlainText() != twice[i].PlainText() {
			t.Fatalf("message %d changed on second repair: %#v -> %#v", i, once[i], twice[i])
		}
		if once[i].ToolResult != nil && twice[i].ToolResult != nil && once[i].ToolResult.ToolUseID != twice[i].ToolResult.ToolUseID {
			t.Fatalf("tool result %d changed on second repair: %#v -> %#v", i, once[i], twice[i])
		}
	}
}

func TestRepairToolUseAdjacencySynthesizesInterruptedResultBeforeFollowingUserText(t *testing.T) {
	messages := []model.Message{
		{
			Role: model.RoleAssistant,
			Content: []model.ContentBlock{{
				Type: model.ContentToolUse,
				ToolUse: &model.ToolUse{
					ID:    "call_1",
					Name:  "run_command",
					Input: json.RawMessage(`{"command":"sleep 10"}`),
				},
			}},
		},
		{Role: model.RoleUser, Content: []model.ContentBlock{{Type: model.ContentText, Text: "continue"}}},
	}

	repaired := RepairToolUseAdjacency(messages)
	if len(repaired) != 3 {
		t.Fatalf("len(repaired) = %d, want 3: %#v", len(repaired), repaired)
	}
	if repaired[1].Role != model.RoleTool || repaired[1].ToolResult == nil || repaired[1].ToolResult.ToolUseID != "call_1" || !repaired[1].ToolResult.IsError {
		t.Fatalf("second repaired message = %#v, want synthetic interrupted tool result", repaired[1])
	}
	if !strings.Contains(repaired[1].ToolResult.Content, "interrupted") {
		t.Fatalf("synthetic result content = %q, want interrupted marker", repaired[1].ToolResult.Content)
	}
	if repaired[2].Role != model.RoleUser || repaired[2].PlainText() != "continue" {
		t.Fatalf("third repaired message = %#v, want resume prompt", repaired[2])
	}
}

func messageHasToolUse(msg model.Message, id string) bool {
	for _, block := range msg.Content {
		if block.Type == model.ContentToolUse && block.ToolUse != nil && block.ToolUse.ID == id {
			return true
		}
	}
	return false
}
