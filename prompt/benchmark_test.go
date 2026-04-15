package prompt

import (
	"context"
	"fmt"
	"testing"

	"github.com/MemaxLabs/memax-go-agent-sdk/identity"
	"github.com/MemaxLabs/memax-go-agent-sdk/model"
	"github.com/MemaxLabs/memax-go-agent-sdk/skill"
)

func BenchmarkDefaultBuilder(b *testing.B) {
	req := Request{
		Identity:     identity.Default(),
		SystemPrompt: "Follow host policy.",
		Tools:        []model.ToolSpec{{Name: "read_file"}, {Name: "write_file"}, {Name: "search"}},
		Messages: []model.Message{{
			Role:    model.RoleUser,
			Content: []model.ContentBlock{{Type: model.ContentText, Text: "review SQL migration and tests"}},
		}},
		Skills: make([]skill.Skill, 100),
	}
	for i := range req.Skills {
		req.Skills[i] = skill.Skill{
			Name:        fmt.Sprintf("skill-%03d", i),
			Description: "review code migrations tests database frontend",
			Content:     "Use the relevant checklist.",
		}
	}

	builder := DefaultBuilder{Profile: ProfileOpenAI}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		result, err := builder.Build(context.Background(), req)
		if err != nil {
			b.Fatal(err)
		}
		if result.Hash == "" {
			b.Fatal("empty hash")
		}
	}
}
