package skill

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func BenchmarkSelector(b *testing.B) {
	skills := make([]Skill, 1000)
	for i := range skills {
		skills[i] = Skill{
			Name:        fmt.Sprintf("skill-%04d", i),
			Description: "database migration frontend backend security tests",
			Content:     "Use the checklist for the matching domain.",
		}
	}
	selector := Selector{MaxSkills: 16}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		out := selector.Select(skills, "database migration rollback")
		if len(out) == 0 {
			b.Fatal("empty selection")
		}
	}
}

func BenchmarkPrefetchSourceCacheHit(b *testing.B) {
	source := &PrefetchSource{
		TTL:    time.Hour,
		Source: StaticSource{{Name: "cached"}},
	}
	if _, err := source.Skills(context.Background()); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		out, err := source.Skills(context.Background())
		if err != nil {
			b.Fatal(err)
		}
		if len(out) != 1 {
			b.Fatal("missing skill")
		}
	}
}

func BenchmarkPrefetchSourceStalePath(b *testing.B) {
	source := &PrefetchSource{
		TTL:            time.Nanosecond,
		RefreshTimeout: time.Second,
		Source:         StaticSource{{Name: "cached"}},
	}
	if _, err := source.Skills(context.Background()); err != nil {
		b.Fatal(err)
	}
	time.Sleep(time.Millisecond)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		out, err := source.Skills(context.Background())
		if err != nil {
			b.Fatal(err)
		}
		if len(out) != 1 {
			b.Fatal("missing skill")
		}
	}
}
