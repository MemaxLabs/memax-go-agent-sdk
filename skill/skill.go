package skill

import "context"

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

// StaticSource is an in-memory Source implementation.
type StaticSource []Skill

// Skills returns a defensive copy of the configured skills.
func (s StaticSource) Skills(context.Context) ([]Skill, error) {
	out := make([]Skill, len(s))
	for i, item := range s {
		out[i] = clone(item)
	}
	return out, nil
}

func clone(in Skill) Skill {
	in.Tags = append([]string(nil), in.Tags...)
	return in
}
