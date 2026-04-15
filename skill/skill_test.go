package skill

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadDirParsesSkillFiles(t *testing.T) {
	dir := t.TempDir()
	skillDir := filepath.Join(dir, "reviewer")
	if err := os.Mkdir(skillDir, 0o755); err != nil {
		t.Fatalf("Mkdir returned error: %v", err)
	}
	data := `---
name: code-review
description: Review code changes.
when_to_use: reviewing pull requests
always_on: true
tags: review, quality
---
Read the diff and report risks first.
`
	if err := os.WriteFile(filepath.Join(skillDir, skillFileName), []byte(data), 0o644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}

	got, err := LoadDir(context.Background(), dir)
	if err != nil {
		t.Fatalf("LoadDir returned error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len(skills) = %d, want 1", len(got))
	}
	if got[0].Name != "code-review" || got[0].Description == "" || got[0].WhenToUse == "" {
		t.Fatalf("loaded skill = %#v", got[0])
	}
	if !got[0].AlwaysOn || len(got[0].Tags) != 2 || got[0].Source != "local" || got[0].Path == "" {
		t.Fatalf("loaded metadata = %#v", got[0])
	}
}

func TestSelectorKeepsAlwaysOnAndRanksRelevantSkills(t *testing.T) {
	skills := []Skill{
		{Name: "always", AlwaysOn: true},
		{Name: "database", Description: "SQL migrations and query tuning"},
		{Name: "frontend", Description: "CSS layout"},
	}

	got := (Selector{MaxSkills: 2}).Select(skills, "fix SQL query")
	if len(got) != 2 {
		t.Fatalf("len(skills) = %d, want 2", len(got))
	}
	if got[0].Name != "always" || got[1].Name != "database" {
		t.Fatalf("selected = %#v, want always then database", got)
	}
}

func TestStaticSourceReturnsDefensiveCopy(t *testing.T) {
	source := StaticSource{{Name: "x", Tags: []string{"a"}}}
	got, err := source.Skills(context.Background())
	if err != nil {
		t.Fatalf("Skills returned error: %v", err)
	}
	got[0].Tags[0] = "mutated"
	again, _ := source.Skills(context.Background())
	if again[0].Tags[0] != "a" {
		t.Fatalf("source mutated through returned copy: %#v", again)
	}
}
