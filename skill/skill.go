package skill

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// DisclosureMode controls how skills are exposed to the model.
type DisclosureMode string

const (
	// LoadToolName is the default tool name used by progressive skill
	// disclosure to load full instructions for a named skill.
	LoadToolName = "load_skill"

	// DisclosureInjectSelected injects selected skill instructions directly into
	// the system prompt. This is the default for backward compatibility and for
	// small trusted skill sets.
	DisclosureInjectSelected DisclosureMode = "inject_selected"
	// DisclosureProgressive exposes only selected skill metadata in the prompt
	// and expects the model to load full instructions through an explicit tool.
	DisclosureProgressive DisclosureMode = "progressive"
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
	PolicyHints []string
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

// Skills loads each source in order and deduplicates by skill name. Unnamed
// skills are treated as anonymous instruction blocks and are not deduplicated.
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
	loading   bool
	ready     chan struct{}
}

// Skills returns cached skills until TTL expires. Non-positive TTL means cache
// forever after the first successful load.
func (s *CachedSource) Skills(ctx context.Context) ([]Skill, error) {
	if s == nil || s.Source == nil {
		return nil, fmt.Errorf("skill: cached source requires Source")
	}

	for {
		s.mu.Lock()
		if s.cacheValidLocked(time.Now()) {
			out := cloneSkills(s.skills)
			s.mu.Unlock()
			return out, nil
		}
		if s.loading {
			ready := s.ready
			s.mu.Unlock()
			select {
			case <-ready:
				continue
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		s.loading = true
		s.ready = make(chan struct{})
		s.mu.Unlock()
		break
	}

	loaded, err := s.Source.Skills(ctx)
	loaded = cloneSkills(loaded)

	s.mu.Lock()
	ready := s.ready
	if err == nil {
		s.skills = loaded
		if s.TTL > 0 {
			s.expiresAt = time.Now().Add(s.TTL)
		} else {
			s.expiresAt = time.Time{}
		}
	}
	out := cloneSkills(s.skills)
	s.loading = false
	s.ready = nil
	close(ready)
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *CachedSource) cacheValidLocked(now time.Time) bool {
	return s.skills != nil && (s.TTL <= 0 || now.Before(s.expiresAt))
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
	in.PolicyHints = append([]string(nil), in.PolicyHints...)
	return in
}
