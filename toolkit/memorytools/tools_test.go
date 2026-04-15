package memorytools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/MemaxLabs/memax-go-agent-sdk/identity"
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
	if id == "" || !strings.Contains(result.Content, "created memory project/billing") || result.Metadata["action"] != "created" {
		t.Fatalf("save result = %#v, want memory metadata", result)
	}

	result, err = save.Execute(context.Background(), tool.Call{
		Use: model.ToolUse{Input: json.RawMessage(`{
			"name":"billing",
			"scope":"project",
			"content":"Invoices require audit logs and rollback notes."
		}`)},
	})
	if err != nil {
		t.Fatalf("second save Execute returned error: %v", err)
	}
	if !strings.Contains(result.Content, "updated memory project/billing") || result.Metadata["action"] != "updated" {
		t.Fatalf("second save result = %#v, want updated action", result)
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

func TestToolsPassRuntimeIdentity(t *testing.T) {
	source := &recordingMemoryBackend{}
	search, err := NewSearchTool(Config{Source: source})
	if err != nil {
		t.Fatalf("NewSearchTool returned error: %v", err)
	}
	save, err := NewSaveTool(Config{Writer: source})
	if err != nil {
		t.Fatalf("NewSaveTool returned error: %v", err)
	}
	deleteTool, err := NewDeleteTool(Config{Deleter: source})
	if err != nil {
		t.Fatalf("NewDeleteTool returned error: %v", err)
	}
	runtime := tool.Runtime{Identity: identity.Identity{Name: "reviewer"}}

	if _, err := search.Execute(context.Background(), tool.Call{Runtime: runtime, Use: model.ToolUse{Input: json.RawMessage(`{}`)}}); err != nil {
		t.Fatalf("search Execute returned error: %v", err)
	}
	if _, err := save.Execute(context.Background(), tool.Call{Runtime: runtime, Use: model.ToolUse{Input: json.RawMessage(`{"content":"remember"}`)}}); err != nil {
		t.Fatalf("save Execute returned error: %v", err)
	}
	if _, err := deleteTool.Execute(context.Background(), tool.Call{Runtime: runtime, Use: model.ToolUse{Input: json.RawMessage(`{"id":"memory-1"}`)}}); err != nil {
		t.Fatalf("delete Execute returned error: %v", err)
	}

	if source.search.Identity.Name != "reviewer" || source.put.Identity.Name != "reviewer" || source.delete.Identity.Name != "reviewer" {
		t.Fatalf("identity not propagated: search=%#v put=%#v delete=%#v", source.search.Identity, source.put.Identity, source.delete.Identity)
	}
}

func TestSaveToolRejectsInvalidScopeWithoutExecutorValidation(t *testing.T) {
	store := memory.NewMemoryStore(nil)
	save, err := NewSaveTool(Config{Writer: store})
	if err != nil {
		t.Fatalf("NewSaveTool returned error: %v", err)
	}
	_, err = save.Execute(context.Background(), tool.Call{
		Use: model.ToolUse{Input: json.RawMessage(`{"scope":"global","content":"remember"}`)},
	})
	if err == nil || !strings.Contains(err.Error(), "invalid memory scope") {
		t.Fatalf("Execute error = %v, want invalid scope", err)
	}
}

type recordingMemoryBackend struct {
	search memory.Request
	put    memory.PutRequest
	delete memory.DeleteRequest
}

func (b *recordingMemoryBackend) Memories(_ context.Context, req memory.Request) ([]memory.Memory, error) {
	b.search = req
	return nil, nil
}

func (b *recordingMemoryBackend) PutMemory(_ context.Context, req memory.PutRequest) (memory.PutResult, error) {
	b.put = req
	return memory.PutResult{Memory: memory.Memory{ID: "memory-1", Scope: memory.ScopeCustom, Content: req.Memory.Content}, Created: true}, nil
}

func (b *recordingMemoryBackend) DeleteMemory(_ context.Context, req memory.DeleteRequest) error {
	b.delete = req
	return nil
}
