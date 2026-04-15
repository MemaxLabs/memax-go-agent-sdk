package skilltools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/MemaxLabs/memax-go-agent-sdk/model"
	"github.com/MemaxLabs/memax-go-agent-sdk/skill"
	"github.com/MemaxLabs/memax-go-agent-sdk/tool"
)

func TestSearchToolReturnsRelevantSkills(t *testing.T) {
	search, err := NewSearchTool(Config{
		Source: skill.StaticSource{
			{Name: "database-review", Description: "SQL migration review", Content: "Check rollback."},
			{Name: "frontend-review", Description: "CSS review"},
		},
	})
	if err != nil {
		t.Fatalf("NewSearchTool returned error: %v", err)
	}

	result, err := search.Execute(context.Background(), tool.Call{
		Use: model.ToolUse{
			ID:    "tool-1",
			Name:  "search_skills",
			Input: json.RawMessage(`{"query":"SQL migration"}`),
		},
	})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if !strings.Contains(result.Content, "database-review") || strings.Contains(result.Content, "frontend-review") {
		t.Fatalf("result = %q, want only database skill", result.Content)
	}
	if result.Metadata["matches"] != 1 {
		t.Fatalf("metadata = %#v, want one match", result.Metadata)
	}
}

func TestSearchToolSpecIsAlwaysLoaded(t *testing.T) {
	search, err := NewSearchTool(Config{Source: skill.StaticSource{{Name: "x"}}})
	if err != nil {
		t.Fatalf("NewSearchTool returned error: %v", err)
	}
	spec := search.Spec()
	if !spec.ReadOnly || !spec.ConcurrencySafe || !spec.AlwaysLoad {
		t.Fatalf("spec flags = %#v, want read-only concurrency-safe always-load", spec)
	}
	if spec.MaxResultBytes == 0 || spec.SearchHint == "" {
		t.Fatalf("spec = %#v, want result bound and search hint", spec)
	}
}

func TestSearchToolRejectsMissingSource(t *testing.T) {
	if _, err := NewSearchTool(Config{}); err == nil {
		t.Fatal("NewSearchTool should reject missing source")
	}
}
