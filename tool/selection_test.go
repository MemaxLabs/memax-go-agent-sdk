package tool

import (
	"context"
	"testing"

	"github.com/MemaxLabs/memax-go-agent-sdk/model"
)

func TestSelectSpecsDefersUnmatchedDeferredTools(t *testing.T) {
	specs := []model.ToolSpec{
		{Name: "search_tools", AlwaysLoad: true},
		{Name: "read_file", Description: "Read workspace files", SearchHint: "read file workspace", ShouldDefer: true},
		{Name: "write_file", Description: "Write workspace files", SearchHint: "write file workspace", ShouldDefer: true},
		{Name: "list_tasks", Description: "List task state"},
	}

	selected := SelectSpecs(specs, "please read the workspace", 0)
	names := specNames(selected)
	want := []string{"search_tools", "read_file", "list_tasks"}
	if !sameNames(names, want) {
		t.Fatalf("selected = %#v, want %#v", names, want)
	}
}

func TestSelectSpecsPreservesAlwaysLoadBeyondLimit(t *testing.T) {
	specs := []model.ToolSpec{
		{Name: "always_a", AlwaysLoad: true},
		{Name: "always_b", AlwaysLoad: true},
		{Name: "read_file", SearchHint: "read file", ShouldDefer: true},
	}

	selected := SelectSpecs(specs, "read", 1)
	names := specNames(selected)
	want := []string{"always_a", "always_b"}
	if !sameNames(names, want) {
		t.Fatalf("selected = %#v, want %#v", names, want)
	}
}

func TestSearchSpecsReturnsOnlyMatchedTools(t *testing.T) {
	specs := []model.ToolSpec{
		{Name: "read_file", SearchHint: "read file workspace"},
		{Name: "write_file", SearchHint: "write file workspace"},
		{Name: "list_tasks", SearchHint: "todo task state"},
	}

	results := SearchSpecs(specs, "task", 10)
	names := specNames(results)
	want := []string{"list_tasks"}
	if !sameNames(names, want) {
		t.Fatalf("results = %#v, want %#v", names, want)
	}
}

func TestSearchSelectorUsesTranscriptQuery(t *testing.T) {
	registry := NewRegistry(
		Definition{ToolSpec: model.ToolSpec{Name: "search_tools", AlwaysLoad: true}},
		Definition{ToolSpec: model.ToolSpec{Name: "read_file", SearchHint: "read workspace file", ShouldDefer: true}},
		Definition{ToolSpec: model.ToolSpec{Name: "write_file", SearchHint: "write workspace file", ShouldDefer: true}},
	)
	specs, err := (SearchSelector{}).Select(context.Background(), registry, SelectRequest{
		Messages: []model.Message{{
			Role:    model.RoleUser,
			Content: []model.ContentBlock{{Type: model.ContentText, Text: "read the workspace"}},
		}},
	})
	if err != nil {
		t.Fatalf("Select returned error: %v", err)
	}
	names := specNames(specs)
	want := []string{"search_tools", "read_file"}
	if !sameNames(names, want) {
		t.Fatalf("selected = %#v, want %#v", names, want)
	}
}

func specNames(specs []model.ToolSpec) []string {
	out := make([]string, 0, len(specs))
	for _, spec := range specs {
		out = append(out, spec.Name)
	}
	return out
}

func sameNames(a []string, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
