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
	// ResourceToolName is the default tool name used by progressive skill
	// disclosure to load a supporting resource for a named skill.
	ResourceToolName = "read_skill_resource"

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
	Name        string        `json:"name"`
	Description string        `json:"description,omitempty"`
	WhenToUse   string        `json:"when_to_use,omitempty"`
	Content     string        `json:"content,omitempty"`
	Source      string        `json:"source,omitempty"`
	Path        string        `json:"path,omitempty"`
	Resources   []ResourceRef `json:"resources,omitempty"`
	AlwaysOn    bool          `json:"always_on,omitempty"`
	Tags        []string      `json:"tags,omitempty"`
	PolicyHints []string      `json:"policy_hints,omitempty"`
}

// ResourceRef is lightweight metadata for a skill supporting resource. Resource
// content is intentionally not stored here so progressive disclosure can expose
// metadata first and load content through a tool only when needed. The resource
// can be identified by Name or Path; if both are provided, resource loaders
// receive both so they can resolve a canonical backing object.
type ResourceRef struct {
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Path        string   `json:"path,omitempty"`
	MIMEType    string   `json:"mime_type,omitempty"`
	Bytes       int      `json:"bytes,omitempty"`
	Tags        []string `json:"tags,omitempty"`
}

// Resource is the full content returned by a ResourceSource.
type Resource struct {
	SkillName   string         `json:"skill_name,omitempty"`
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Path        string         `json:"path,omitempty"`
	MIMEType    string         `json:"mime_type,omitempty"`
	Content     string         `json:"content"`
	Bytes       int            `json:"bytes,omitempty"`
	Metadata    map[string]any `json:"metadata,omitempty"`
}

// ResourceRequest identifies a supporting resource for a skill.
type ResourceRequest struct {
	SkillName string `json:"skill_name"`
	Name      string `json:"name,omitempty"`
	Path      string `json:"path,omitempty"`
}

// ResourceSource loads supporting skill resources on demand.
type ResourceSource interface {
	SkillResource(context.Context, ResourceRequest) (Resource, error)
}

// ResourceSourceFunc adapts a function to ResourceSource.
type ResourceSourceFunc func(context.Context, ResourceRequest) (Resource, error)

// SkillResource calls f(ctx, req).
func (f ResourceSourceFunc) SkillResource(ctx context.Context, req ResourceRequest) (Resource, error) {
	if f == nil {
		return Resource{}, fmt.Errorf("skill: nil ResourceSourceFunc")
	}
	return f(ctx, req)
}

// StaticResourceSource is an in-memory ResourceSource implementation.
type StaticResourceSource []Resource

// SkillResource returns a defensive copy of a matching resource.
func (s StaticResourceSource) SkillResource(ctx context.Context, req ResourceRequest) (Resource, error) {
	if err := ctx.Err(); err != nil {
		return Resource{}, err
	}
	for _, item := range s {
		if item.SkillName != req.SkillName {
			continue
		}
		if req.Name != "" && item.Name == req.Name {
			return cloneResource(item), nil
		}
		if req.Path != "" && item.Path == req.Path {
			return cloneResource(item), nil
		}
	}
	return Resource{}, fmt.Errorf("skill: resource %q for skill %q not found", firstNonEmpty(req.Name, req.Path), req.SkillName)
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
	in.Resources = cloneResourceRefs(in.Resources)
	return in
}

func cloneResourceRefs(refs []ResourceRef) []ResourceRef {
	if len(refs) == 0 {
		return nil
	}
	out := make([]ResourceRef, len(refs))
	for i, ref := range refs {
		out[i] = ref
		out[i].Tags = append([]string(nil), ref.Tags...)
	}
	return out
}

func cloneResource(in Resource) Resource {
	in.Metadata = cloneMetadata(in.Metadata)
	return in
}

func cloneMetadata(in map[string]any) map[string]any {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]any, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}
