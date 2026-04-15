package toolsearch

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/MemaxLabs/memax-go-agent-sdk/model"
	"github.com/MemaxLabs/memax-go-agent-sdk/tool"
)

func TestToolSearchFindsMatchingTools(t *testing.T) {
	registry := tool.NewRegistry(
		tool.Definition{ToolSpec: model.ToolSpec{Name: "read_file", Description: "Read workspace files", SearchHint: "read file workspace", ReadOnly: true}},
		tool.Definition{ToolSpec: model.ToolSpec{Name: "write_file", Description: "Write workspace files", SearchHint: "write file workspace", Destructive: true}},
	)
	search, err := NewTool(Config{Registry: registry})
	if err != nil {
		t.Fatalf("NewTool returned error: %v", err)
	}
	if err := registry.Register(search); err != nil {
		t.Fatalf("Register returned error: %v", err)
	}

	result, err := search.Execute(context.Background(), tool.Call{
		Use: model.ToolUse{
			ID:    "search-1",
			Name:  ToolName,
			Input: json.RawMessage(`{"query":"read"}`),
		},
	})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if !strings.Contains(result.Content, "read_file") {
		t.Fatalf("Content = %q, want read_file", result.Content)
	}
	if strings.Contains(result.Content, ToolName) {
		t.Fatalf("Content = %q, should not include search tool itself", result.Content)
	}
	if result.Metadata["count"] != 1 {
		t.Fatalf("metadata = %#v, want count 1", result.Metadata)
	}
}

func TestToolSearchNoMatches(t *testing.T) {
	search, err := NewTool(Config{Registry: tool.NewRegistry(
		tool.Definition{ToolSpec: model.ToolSpec{Name: "read_file", SearchHint: "read workspace"}},
	)})
	if err != nil {
		t.Fatalf("NewTool returned error: %v", err)
	}
	result, err := search.Execute(context.Background(), tool.Call{
		Use: model.ToolUse{
			ID:    "search-1",
			Name:  ToolName,
			Input: json.RawMessage(`{"query":"database"}`),
		},
	})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if result.Content != "no matching tools" {
		t.Fatalf("Content = %q, want no matching tools", result.Content)
	}
}

func TestToolSearchLimitDoesNotCountSearchTool(t *testing.T) {
	registry := tool.NewRegistry(
		tool.Definition{ToolSpec: model.ToolSpec{Name: "database_tool", SearchHint: "tool database"}},
	)
	search, err := NewTool(Config{Registry: registry, Limit: 1})
	if err != nil {
		t.Fatalf("NewTool returned error: %v", err)
	}
	if err := registry.Register(search); err != nil {
		t.Fatalf("Register returned error: %v", err)
	}

	result, err := search.Execute(context.Background(), tool.Call{
		Use: model.ToolUse{
			ID:    "search-1",
			Name:  ToolName,
			Input: json.RawMessage(`{"query":"tool"}`),
		},
	})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if !strings.Contains(result.Content, "database_tool") {
		t.Fatalf("Content = %q, want database_tool", result.Content)
	}
}

func TestNewToolRequiresRegistry(t *testing.T) {
	_, err := NewTool(Config{})
	if err == nil {
		t.Fatal("NewTool returned nil, want missing registry error")
	}
}
