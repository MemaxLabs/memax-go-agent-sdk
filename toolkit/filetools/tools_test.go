package filetools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/MemaxLabs/memax-go-agent-sdk/model"
	"github.com/MemaxLabs/memax-go-agent-sdk/tool"
)

func TestMemoryFileTools(t *testing.T) {
	fs := NewMemoryFS(map[string]string{
		"README.md":      "hello",
		"docs/guide.md":  "guide",
		"docs/notes.txt": "notes",
	})

	read := mustRunTool(t, NewReadTool(fs), model.ToolUse{
		ID:    "read-1",
		Name:  ReadToolName,
		Input: json.RawMessage(`{"path":"README.md"}`),
	})
	if read.Content != "hello" {
		t.Fatalf("read content = %q, want hello", read.Content)
	}

	write := mustRunTool(t, NewWriteTool(fs), model.ToolUse{
		ID:    "write-1",
		Name:  WriteToolName,
		Input: json.RawMessage(`{"path":"docs/new.md","content":"new"}`),
	})
	if write.Content != "wrote docs/new.md" {
		t.Fatalf("write content = %q, want write confirmation", write.Content)
	}

	list := mustRunTool(t, NewListTool(fs), model.ToolUse{
		ID:    "list-1",
		Name:  ListToolName,
		Input: json.RawMessage(`{"prefix":"docs"}`),
	})
	if list.Content != "docs/guide.md\ndocs/new.md\ndocs/notes.txt" {
		t.Fatalf("list content = %q", list.Content)
	}
}

func TestMemoryFSReadMissingFile(t *testing.T) {
	fs := NewMemoryFS(nil)
	_, err := fs.ReadFile(context.Background(), "missing.txt")
	if err == nil {
		t.Fatal("ReadFile returned nil, want missing file error")
	}
}

func TestMemoryFSRejectsInvalidWritePath(t *testing.T) {
	fs := NewMemoryFS(nil)
	err := fs.WriteFile(context.Background(), "/", "content")
	if err == nil {
		t.Fatal("WriteFile returned nil, want invalid path error")
	}
}

func TestListToolEmptyPrefixListsAllFiles(t *testing.T) {
	fs := NewMemoryFS(map[string]string{
		"b.txt": "b",
		"a.txt": "a",
	})
	result := mustRunTool(t, NewListTool(fs), model.ToolUse{
		ID:    "list-1",
		Name:  ListToolName,
		Input: json.RawMessage(`{}`),
	})
	if got, want := strings.Split(result.Content, "\n"), []string{"a.txt", "b.txt"}; len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("listed files = %#v, want %#v", got, want)
	}
}

func mustRunTool(t *testing.T, impl tool.Tool, use model.ToolUse) model.ToolResult {
	t.Helper()
	result, err := impl.Execute(context.Background(), tool.Call{Use: use})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	return result
}
