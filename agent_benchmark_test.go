package memaxagent

import (
	"context"
	"testing"

	"github.com/MemaxLabs/memax-go-agent-sdk/model"
)

func BenchmarkQueryAsyncStartup(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		events := QueryAsync(context.Background(), "start", Options{
			Model: &fakeModel{turns: [][]model.StreamEvent{{{Kind: model.StreamText, Text: "done"}}}},
		})
		if _, err := Drain(events); err != nil {
			b.Fatal(err)
		}
	}
}
