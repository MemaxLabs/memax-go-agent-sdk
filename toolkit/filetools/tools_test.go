package filetools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"testing/fstest"

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

func TestOSFSReadWriteListAndRejectEscape(t *testing.T) {
	fsys, err := NewOSFS(t.TempDir())
	if err != nil {
		t.Fatalf("NewOSFS returned error: %v", err)
	}
	if err := fsys.WriteFile(context.Background(), "docs/guide.md", "guide"); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}
	content, err := fsys.ReadFile(context.Background(), "docs/guide.md")
	if err != nil {
		t.Fatalf("ReadFile returned error: %v", err)
	}
	if content != "guide" {
		t.Fatalf("content = %q, want guide", content)
	}
	files, err := fsys.ListFiles(context.Background(), "docs")
	if err != nil {
		t.Fatalf("ListFiles returned error: %v", err)
	}
	if got, want := strings.Join(files, "\n"), "docs/guide.md"; got != want {
		t.Fatalf("files = %q, want %q", got, want)
	}
	files, err = fsys.ListFiles(context.Background(), "")
	if err != nil {
		t.Fatalf("ListFiles root returned error: %v", err)
	}
	if got, want := strings.Join(files, "\n"), "docs/guide.md"; got != want {
		t.Fatalf("root files = %q, want %q", got, want)
	}
	if err := fsys.WriteFile(context.Background(), "../escape.txt", "nope"); err == nil {
		t.Fatal("WriteFile returned nil, want path escape error")
	}
	if _, err := fsys.ReadFile(context.Background(), "../escape.txt"); err == nil {
		t.Fatal("ReadFile returned nil, want path escape error")
	}
}

func TestReadOnlyFSAdapter(t *testing.T) {
	fsys, err := NewReadOnlyFS(fstest.MapFS{
		"README.md":     {Data: []byte("hello")},
		"docs/guide.md": {Data: []byte("guide")},
	})
	if err != nil {
		t.Fatalf("NewReadOnlyFS returned error: %v", err)
	}
	content, err := fsys.ReadFile(context.Background(), "README.md")
	if err != nil {
		t.Fatalf("ReadFile returned error: %v", err)
	}
	if content != "hello" {
		t.Fatalf("content = %q, want hello", content)
	}
	files, err := fsys.ListFiles(context.Background(), "docs")
	if err != nil {
		t.Fatalf("ListFiles returned error: %v", err)
	}
	if got, want := strings.Join(files, "\n"), "docs/guide.md"; got != want {
		t.Fatalf("files = %q, want %q", got, want)
	}
	files, err = fsys.ListFiles(context.Background(), "")
	if err != nil {
		t.Fatalf("ListFiles root returned error: %v", err)
	}
	if got, want := strings.Join(files, "\n"), "README.md\ndocs/guide.md"; got != want {
		t.Fatalf("root files = %q, want %q", got, want)
	}
	if err := fsys.WriteFile(context.Background(), "README.md", "updated"); err == nil {
		t.Fatal("WriteFile returned nil, want read-only error")
	}
	if _, err := fsys.ReadFile(context.Background(), "../escape.txt"); err == nil {
		t.Fatal("ReadFile returned nil, want invalid fs path error")
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
