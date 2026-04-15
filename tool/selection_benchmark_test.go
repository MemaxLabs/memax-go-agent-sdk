package tool

import (
	"fmt"
	"testing"

	"github.com/MemaxLabs/memax-go-agent-sdk/model"
)

func BenchmarkSelectSpecsLargeRegistry(b *testing.B) {
	specs := benchmarkSpecs(1000)
	query := "read workspace file and inspect task status"

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		selected := SelectSpecs(specs, query, 32)
		if len(selected) == 0 {
			b.Fatal("SelectSpecs returned no tools")
		}
	}
}

func BenchmarkSearchSpecsLargeRegistry(b *testing.B) {
	specs := benchmarkSpecs(1000)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		selected := SearchSpecs(specs, "database snapshot", 16)
		if len(selected) == 0 {
			b.Fatal("SearchSpecs returned no tools")
		}
	}
}

func benchmarkSpecs(count int) []model.ToolSpec {
	specs := make([]model.ToolSpec, 0, count)
	for i := 0; i < count; i++ {
		spec := model.ToolSpec{
			Name:        fmt.Sprintf("tool_%04d", i),
			Description: fmt.Sprintf("Tool %d for workspace operations", i),
			SearchHint:  "workspace file task database snapshot",
			ShouldDefer: true,
		}
		if i%100 == 0 {
			spec.AlwaysLoad = true
		}
		specs = append(specs, spec)
	}
	return specs
}
