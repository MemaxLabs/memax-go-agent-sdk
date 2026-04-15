package tool

import (
	"fmt"
	"sync"

	"github.com/MemaxLabs/memax-go-agent-sdk/model"
)

type Registry struct {
	mu    sync.RWMutex
	tools map[string]Tool
	order []string
}

func NewRegistry(tools ...Tool) *Registry {
	r := &Registry{tools: make(map[string]Tool)}
	for _, t := range tools {
		_ = r.Register(t)
	}
	return r
}

func (r *Registry) Register(t Tool) error {
	if t == nil {
		return fmt.Errorf("register nil tool")
	}
	spec := t.Spec()
	if spec.Name == "" {
		return fmt.Errorf("register tool with empty name")
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.tools[spec.Name]; ok {
		return fmt.Errorf("tool %q already registered", spec.Name)
	}
	r.tools[spec.Name] = t
	r.order = append(r.order, spec.Name)
	return nil
}

func (r *Registry) Get(name string) (Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.tools[name]
	return t, ok
}

func (r *Registry) Specs() []model.ToolSpec {
	r.mu.RLock()
	defer r.mu.RUnlock()
	specs := make([]model.ToolSpec, 0, len(r.order))
	for _, name := range r.order {
		specs = append(specs, r.tools[name].Spec())
	}
	return specs
}
