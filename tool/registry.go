package tool

import (
	"fmt"
	"sync"

	"github.com/MemaxLabs/memax-go-agent-sdk/model"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

type Registry struct {
	mu      sync.RWMutex
	entries map[string]entry
	order   []string
}

type entry struct {
	tool        Tool
	inputSchema *jsonschema.Schema
}

func NewRegistry(tools ...Tool) *Registry {
	r := &Registry{entries: make(map[string]entry)}
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
	schema, err := compileInputSchema(spec.Name, spec.InputSchema)
	if err != nil {
		return err
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.entries[spec.Name]; ok {
		return fmt.Errorf("tool %q already registered", spec.Name)
	}
	r.entries[spec.Name] = entry{tool: t, inputSchema: schema}
	r.order = append(r.order, spec.Name)
	return nil
}

func (r *Registry) Get(name string) (Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	entry, ok := r.entries[name]
	return entry.tool, ok
}

func (r *Registry) InputSchema(name string) (*jsonschema.Schema, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	entry, ok := r.entries[name]
	return entry.inputSchema, ok
}

func (r *Registry) Specs() []model.ToolSpec {
	r.mu.RLock()
	defer r.mu.RUnlock()
	specs := make([]model.ToolSpec, 0, len(r.order))
	for _, name := range r.order {
		specs = append(specs, r.entries[name].tool.Spec())
	}
	return specs
}

func compileInputSchema(toolName string, raw map[string]any) (*jsonschema.Schema, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	compiler := jsonschema.NewCompiler()
	const loc = "input.schema.json"
	if err := compiler.AddResource(loc, raw); err != nil {
		return nil, fmt.Errorf("compile %q input schema: add resource: %w", toolName, err)
	}
	schema, err := compiler.Compile(loc)
	if err != nil {
		return nil, fmt.Errorf("compile %q input schema: %w", toolName, err)
	}
	return schema, nil
}
