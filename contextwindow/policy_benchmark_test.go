package contextwindow

import (
	"context"
	"fmt"
	"testing"

	"github.com/MemaxLabs/memax-go-agent-sdk/model"
)

func BenchmarkTokenBudgetLongTranscript(b *testing.B) {
	messages := benchmarkMessages(5000)
	policy := TokenBudget{MaxTokens: 20_000}
	ctx := context.Background()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		out, err := policy.Apply(ctx, messages)
		if err != nil {
			b.Fatalf("Apply returned error: %v", err)
		}
		if len(out) == 0 {
			b.Fatal("Apply returned no messages")
		}
	}
}

func BenchmarkSummarizingBudgetLongTranscript(b *testing.B) {
	messages := benchmarkMessages(5000)
	policy := SummarizingBudget{
		MaxTokens:        20_000,
		MaxSummaryTokens: 2_000,
		Summarizer: SummarizerFunc(func(context.Context, []model.Message) (string, error) {
			return "summary", nil
		}),
	}
	ctx := context.Background()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		out, err := policy.Apply(ctx, messages)
		if err != nil {
			b.Fatalf("Apply returned error: %v", err)
		}
		if len(out) == 0 {
			b.Fatal("Apply returned no messages")
		}
	}
}

func benchmarkMessages(count int) []model.Message {
	messages := make([]model.Message, 0, count)
	for i := 0; i < count; i++ {
		messages = append(messages, model.Message{
			ID:   fmt.Sprintf("m-%d", i),
			Role: model.RoleUser,
			Content: []model.ContentBlock{
				{Type: model.ContentText, Text: fmt.Sprintf("message %d with enough text to count toward the budget", i)},
			},
		})
	}
	return messages
}
