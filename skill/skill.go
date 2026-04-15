package skill

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// Skill is a local instruction bundle that can be injected into the model
// prompt when it is relevant to the current run.
type Skill struct {
	Name        string
	Description string
	WhenToUse   string
	Content     string
	Source      string
	Path        string
	AlwaysOn    bool
	Tags        []string
}

// Source provides skills to the prompt layer.
type Source interface {
	Skills(context.Context) ([]Skill, error)
}

// SourceFunc adapts a function to Source.
type SourceFunc func(context.Context) ([]Skill, error)

// Skills calls f(ctx).
func (f SourceFunc) Skills(ctx context.Context) ([]Skill, error) {
	if f == nil {
		return nil, fmt.Errorf("skill: nil SourceFunc")
	}
	return f(ctx)
}

// StaticSource is an in-memory Source implementation.
type StaticSource []Skill

// Skills returns a defensive copy of the configured skills.
func (s StaticSource) Skills(context.Context) ([]Skill, error) {
	return cloneSkills(s), nil
}

// MultiSource merges skills from multiple sources.
type MultiSource []Source

// Skills loads each source in order and deduplicates by skill name.
func (s MultiSource) Skills(ctx context.Context) ([]Skill, error) {
	out := make([]Skill, 0)
	seen := map[string]struct{}{}
	for _, source := range s {
		if source == nil {
			continue
		}
		skills, err := source.Skills(ctx)
		if err != nil {
			return nil, err
		}
		for _, item := range skills {
			key := item.Name
			if key == "" {
				out = append(out, clone(item))
				continue
			}
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			out = append(out, clone(item))
		}
	}
	return out, nil
}

// CachedSource caches another source's skills for a configurable duration.
type CachedSource struct {
	Source Source
	TTL    time.Duration

	mu        sync.Mutex
	expiresAt time.Time
	skills    []Skill
}

// Skills returns cached skills until TTL expires. Non-positive TTL means cache
// forever after the first successful load.
func (s *CachedSource) Skills(ctx context.Context) ([]Skill, error) {
	if s == nil || s.Source == nil {
		return nil, fmt.Errorf("skill: cached source requires Source")
	}
	now := time.Now()
	s.mu.Lock()
	if s.skills != nil && (s.TTL <= 0 || now.Before(s.expiresAt)) {
		out := cloneSkills(s.skills)
		s.mu.Unlock()
		return out, nil
	}
	s.mu.Unlock()

	loaded, err := s.Source.Skills(ctx)
	if err != nil {
		return nil, err
	}
	loaded = cloneSkills(loaded)

	s.mu.Lock()
	s.skills = loaded
	if s.TTL > 0 {
		s.expiresAt = now.Add(s.TTL)
	} else {
		s.expiresAt = time.Time{}
	}
	out := cloneSkills(s.skills)
	s.mu.Unlock()
	return out, nil
}

func cloneSkills(skills []Skill) []Skill {
	out := make([]Skill, len(skills))
	for i, item := range skills {
		out[i] = clone(item)
	}
	return out
}

func clone(in Skill) Skill {
	in.Tags = append([]string(nil), in.Tags...)
	return in
}
