package skill

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"testing/fstest"
	"time"
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

func TestLoadFSParsesEmbeddedSkills(t *testing.T) {
	fsys := fstest.MapFS{
		"skills/security/SKILL.md": &fstest.MapFile{Data: []byte(`---
description: Security review
tags: security, auth
---
Check authorization boundaries.
`)},
	}

	got, err := LoadFS(context.Background(), fsys, "skills")
	if err != nil {
		t.Fatalf("LoadFS returned error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len(skills) = %d, want 1", len(got))
	}
	if got[0].Name != "security" || got[0].Source != "fs" || got[0].Path != "skills/security/SKILL.md" {
		t.Fatalf("skill = %#v", got[0])
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

func TestSourceFunc(t *testing.T) {
	source := SourceFunc(func(context.Context) ([]Skill, error) {
		return []Skill{{Name: "dynamic"}}, nil
	})
	got, err := source.Skills(context.Background())
	if err != nil {
		t.Fatalf("Skills returned error: %v", err)
	}
	if len(got) != 1 || got[0].Name != "dynamic" {
		t.Fatalf("skills = %#v", got)
	}
}

func TestMultiSourceDeduplicatesByName(t *testing.T) {
	source := MultiSource{
		StaticSource{{Name: "a"}, {Name: "b"}},
		StaticSource{{Name: "a"}, {Name: "c"}},
	}
	got, err := source.Skills(context.Background())
	if err != nil {
		t.Fatalf("Skills returned error: %v", err)
	}
	names := []string{got[0].Name, got[1].Name, got[2].Name}
	want := []string{"a", "b", "c"}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("names = %#v, want %#v", names, want)
		}
	}
}

func TestCachedSourceCachesSuccessfulLoads(t *testing.T) {
	calls := 0
	source := &CachedSource{
		TTL: time.Hour,
		Source: SourceFunc(func(context.Context) ([]Skill, error) {
			calls++
			return []Skill{{Name: "cached", Tags: []string{"x"}}}, nil
		}),
	}

	first, err := source.Skills(context.Background())
	if err != nil {
		t.Fatalf("Skills returned error: %v", err)
	}
	first[0].Tags[0] = "mutated"
	second, err := source.Skills(context.Background())
	if err != nil {
		t.Fatalf("Skills returned error: %v", err)
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1", calls)
	}
	if second[0].Tags[0] != "x" {
		t.Fatalf("cached skills were mutated: %#v", second)
	}
}

func TestCachedSourceExpires(t *testing.T) {
	calls := 0
	source := &CachedSource{
		TTL: time.Millisecond,
		Source: SourceFunc(func(context.Context) ([]Skill, error) {
			calls++
			return []Skill{{Name: "cached"}}, nil
		}),
	}

	if _, err := source.Skills(context.Background()); err != nil {
		t.Fatalf("Skills returned error: %v", err)
	}
	time.Sleep(5 * time.Millisecond)
	if _, err := source.Skills(context.Background()); err != nil {
		t.Fatalf("Skills returned error: %v", err)
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2 after TTL expiry", calls)
	}
}

func TestCachedSourceDeduplicatesConcurrentRefresh(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	called := make(chan struct{})
	release := make(chan struct{})
	source := &CachedSource{
		TTL: time.Hour,
		Source: SourceFunc(func(context.Context) ([]Skill, error) {
			mu.Lock()
			calls++
			if calls == 1 {
				close(called)
			}
			mu.Unlock()
			<-release
			return []Skill{{Name: "cached"}}, nil
		}),
	}

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := source.Skills(context.Background()); err != nil {
				t.Errorf("Skills returned error: %v", err)
			}
		}()
	}
	<-called
	time.Sleep(10 * time.Millisecond)
	close(release)
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	if calls != 1 {
		t.Fatalf("calls = %d, want one underlying load", calls)
	}
}

func TestHTTPSourceLoadsWrappedSkills(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer token" {
			t.Fatalf("Authorization header = %q", r.Header.Get("Authorization"))
		}
		if r.Header.Get("Accept") != "application/json" {
			t.Fatalf("Accept header = %q", r.Header.Get("Accept"))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"skills": []Skill{{Name: "remote", Tags: []string{"api"}}},
		})
	}))
	defer server.Close()

	got, err := HTTPSource{
		URL:    server.URL,
		Header: http.Header{"Authorization": []string{"Bearer token"}},
	}.Skills(context.Background())
	if err != nil {
		t.Fatalf("Skills returned error: %v", err)
	}
	if len(got) != 1 || got[0].Name != "remote" || got[0].Source != "http" {
		t.Fatalf("skills = %#v", got)
	}
}
