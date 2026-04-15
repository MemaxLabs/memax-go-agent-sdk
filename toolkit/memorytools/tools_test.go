package memorytools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/MemaxLabs/memax-go-agent-sdk/memory"
	"github.com/MemaxLabs/memax-go-agent-sdk/model"
	"github.com/MemaxLabs/memax-go-agent-sdk/tool"
)

func TestSearchToolSelectsRelevantMemories(t *testing.T) {
	store := memory.NewMemoryStore([]memory.Memory{
		{Name: "billing", Scope: memory.ScopeProject, Content: "Invoices require audit logs."},
		{Name: "frontend", Scope: memory.ScopeProject, Content: "Buttons use accessible labels."},
	})
	search, err := NewSearchTool(Config{Source: store})
	if err != nil {
		t.Fatalf("NewSearchTool returned error: %v", err)
	}
	result, err := search.Execute(context.Background(), tool.Call{
		Use: model.ToolUse{Input: json.RawMessage(`{"query":"invoice audit","limit":1}`)},
	})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if !strings.Contains(result.Content, "billing") || strings.Contains(result.Content, "frontend") {
		t.Fatalf("content = %q, want relevant billing memory only", result.Content)
	}
	if result.Metadata["matches"] != 1 {
		t.Fatalf("metadata = %#v, want one match", result.Metadata)
	}
}

func TestSaveAndDeleteToolsMutateStore(t *testing.T) {
	store := memory.NewMemoryStore(nil)
	save, err := NewSaveTool(Config{Writer: store})
	if err != nil {
		t.Fatalf("NewSaveTool returned error: %v", err)
	}
	deleteTool, err := NewDeleteTool(Config{Deleter: store})
	if err != nil {
		t.Fatalf("NewDeleteTool returned error: %v", err)
	}

	result, err := save.Execute(context.Background(), tool.Call{
		Runtime: tool.Runtime{SessionID: "session-1", ParentSessionID: "parent-1"},
		Use: model.ToolUse{Input: json.RawMessage(`{
			"name":"billing",
			"scope":"project",
			"description":"Billing rule",
			"content":"Invoices require audit logs.",
			"tags":["billing"]
		}`)},
	})
	if err != nil {
		t.Fatalf("save Execute returned error: %v", err)
	}
	id, _ := result.Metadata["id"].(string)
	if id == "" || !strings.Contains(result.Content, "project/billing") {
		t.Fatalf("save result = %#v, want memory metadata", result)
	}

	memories, err := store.Memories(context.Background(), memory.Request{})
	if err != nil {
		t.Fatalf("Memories returned error: %v", err)
	}
	if len(memories) != 1 || memories[0].Name != "billing" {
		t.Fatalf("memories = %#v, want saved memory", memories)
	}

	_, err = deleteTool.Execute(context.Background(), tool.Call{
		Use: model.ToolUse{Input: json.RawMessage(`{"id":"` + id + `"}`)},
	})
	if err != nil {
		t.Fatalf("delete Execute returned error: %v", err)
	}
	memories, err = store.Memories(context.Background(), memory.Request{})
	if err != nil {
		t.Fatalf("Memories returned error: %v", err)
	}
	if len(memories) != 0 {
		t.Fatalf("memories = %#v, want deleted", memories)
	}
}

func TestNewToolsUsesConfiguredCapabilities(t *testing.T) {
	store := memory.NewMemoryStore(nil)
	tools, err := NewTools(Config{Source: store, Writer: store, Deleter: store})
	if err != nil {
		t.Fatalf("NewTools returned error: %v", err)
	}
	if len(tools) != 3 {
		t.Fatalf("tools = %d, want 3", len(tools))
	}
}
